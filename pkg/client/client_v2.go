// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/elastic/elastic-agent-client/v7/pkg/utils"
)

//go:generate stringer -type=ChangeType -linecomment -output client_v2_strings.go
type (
	// ChangeType defines types for when units are adjusted.
	ChangeType int
	// Trigger indicates what triggered a change
	Trigger uint
)

const (
	// UnitChangedAdded is when a new unit is added.
	UnitChangedAdded ChangeType = iota // unit_added
	// UnitChangedModified is when an existing unit is modified.
	UnitChangedModified // unit_modified
	// UnitChangedRemoved is when an existing unit is removed.
	UnitChangedRemoved // unit_removed
)

const (
	// TriggeredNothing is Trigger zero value, nothing was triggered.
	// It exists only for completeness and documentation purposes.
	TriggeredNothing Trigger = 0 // nothing_triggered

	// TriggeredConfigChange indicates a change in config triggered the change.
	TriggeredConfigChange Trigger = 1 << iota // config_change_triggered
	// TriggeredFeatureChange indicates a change in the features triggered the change.
	TriggeredFeatureChange // feature_change_triggered
	// TriggeredLogLevelChange indicates a change the log level triggered the change.
	TriggeredLogLevelChange // log_level_triggered
	// TriggeredStateChange indicates when a unit state has ganged.
	TriggeredStateChange // state_change_triggered
)

func (t Trigger) String() string {
	var triggers []string
	if t == 0 {
		return "nothing_triggered"
	}

	if t&TriggeredConfigChange == TriggeredConfigChange {
		t &= ^TriggeredConfigChange
		triggers = append(triggers, "config_change_triggered")
	}
	if t&TriggeredFeatureChange == TriggeredFeatureChange {
		t &= ^TriggeredConfigChange
		triggers = append(triggers, "feature_change_triggered")
	}
	if t&TriggeredLogLevelChange == TriggeredLogLevelChange {
		t &= ^TriggeredConfigChange
		triggers = append(triggers, "log_level_triggered")
	}
	if t&TriggeredStateChange == TriggeredStateChange {
		t &= ^TriggeredConfigChange
		triggers = append(triggers, "state_change_triggered")
	}

	if t != 0 {
		return fmt.Sprintf("invalid triggers: %s and %d leftover",
			strings.Join(triggers, ", "), t)
	}

	return strings.Join(triggers, ", ")
}

// UnitChanged is what is sent over the UnitChanged channel any time a change happens:
//   - a unit is added, modified, or removed
//   - a feature changes
type UnitChanged struct {
	Type     ChangeType
	Triggers Trigger
	// Unit is any change in a unit.
	Unit *Unit
	// Features are all the feature flags and their configs.
	Features *proto.Features
}

// AgentInfo is the information about the running Elastic Agent that the client is connected to.
type AgentInfo struct {
	// ID is the Elastic Agent ID.
	ID string
	// Version is the version of the running Elastic Agent.
	Version string
	// Snapshot is true when the running version of the Elastic Agent is a snapshot.
	Snapshot bool
}

// VersionInfo is the version information for the connecting client.
type VersionInfo struct {
	// Name is the name of the program.
	Name string
	// Version is the current version of the program.
	Version string
	// Meta is any extra metadata information about the version.
	Meta map[string]string
}

// V2 manages the state and communication to the Elastic Agent over the V2 control protocol.
type V2 interface {
	// Start starts the connection to Elastic Agent.
	Start(ctx context.Context) error
	// Stop stops the connection to Elastic Agent.
	Stop()
	// UnitChanges returns the channel the client sends change notifications to.
	//
	// User of this client must read from this channel, or it will block the client.
	UnitChanges() <-chan UnitChanged
	// Errors returns channel of errors that occurred during communication.
	//
	// User of this client must read from this channel, or it will block the client.
	Errors() <-chan error
	// Artifacts returns the artifacts' client.
	Artifacts() ArtifactsClient
	// AgentInfo returns the information about the running Elastic Agent that the client is connected to.
	//
	// nil can be returned when the client has never connected.
	AgentInfo() *AgentInfo
	// RegisterDiagnosticHook registers a diagnostic hook function that will get called when diagnostics is called for
	// a specific unit. Registering the hook at the client level means it will be called for every unit that has
	// diagnostics requested.
	RegisterDiagnosticHook(name string, description string, filename string, contentType string, hook DiagnosticHook)
}

// clientV2 manages the state and communication to the Elastic Agent over the V2 control protocol.
type clientV2 struct {
	target string
	opts   []grpc.DialOption
	token  string

	agentInfoMu sync.RWMutex
	agentInfo   *AgentInfo

	versionInfo     VersionInfo
	versionInfoSent bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	client proto.ElasticAgentClient
	cfgMu  sync.RWMutex
	obsMu  sync.RWMutex

	kickSendObservedCh chan struct{}
	errCh              chan error
	changesCh          chan UnitChanged
	featuresMu         sync.Mutex
	features           *proto.Features
	featuresIdx        uint64
	unitsMu            sync.RWMutex
	units              []*Unit

	dmx       sync.RWMutex
	diagHooks map[string]diagHook

	storeClient    proto.ElasticAgentStoreClient
	artifactClient proto.ElasticAgentArtifactClient
	logClient      proto.ElasticAgentLogClient

	// overridden in tests to make fast
	minCheckTimeout time.Duration
}

// NewV2 creates a client connection to Elastic Agent over the V2 control protocol.
func NewV2(target string, token string, versionInfo VersionInfo, opts ...grpc.DialOption) V2 {
	c := &clientV2{
		target:             target,
		opts:               opts,
		token:              token,
		versionInfo:        versionInfo,
		kickSendObservedCh: make(chan struct{}, 1),
		errCh:              make(chan error),
		changesCh:          make(chan UnitChanged),
		diagHooks:          make(map[string]diagHook),
		minCheckTimeout:    CheckinMinimumTimeout,
		features:           &proto.Features{Fqdn: &proto.FQDNFeature{}},
	}
	c.registerDefaultDiagnostics()
	return c
}

// Start starts the connection to Elastic Agent.
func (c *clientV2) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)
	conn, err := grpc.DialContext(ctx, c.target, c.opts...)
	if err != nil {
		return err
	}
	c.client = proto.NewElasticAgentClient(conn)
	c.storeClient = proto.NewElasticAgentStoreClient(conn)
	c.artifactClient = proto.NewElasticAgentArtifactClient(conn)
	c.logClient = proto.NewElasticAgentLogClient(conn)
	c.startCheckin()
	c.startActions()
	return nil
}

// Stop stops the connection to Elastic Agent.
func (c *clientV2) Stop() {
	if c.cancel != nil {
		c.cancel()
		c.wg.Wait()
		c.ctx = nil
		c.cancel = nil
	}
}

// UnitChanges returns channel client send unit change notifications to.
func (c *clientV2) UnitChanges() <-chan UnitChanged {
	return c.changesCh
}

// Errors returns channel of errors that occurred during communication.
func (c *clientV2) Errors() <-chan error {
	return c.errCh
}

// Artifacts returns the artifacts client.
func (c *clientV2) Artifacts() ArtifactsClient {
	return &artifactsClient{c}
}

// AgentInfo returns the information about the running Elastic Agent that the client is connected to.
//
// nil can be returned when the client has never connected.
func (c *clientV2) AgentInfo() *AgentInfo {
	c.agentInfoMu.RLock()
	defer c.agentInfoMu.RUnlock()
	return c.agentInfo
}

// RegisterDiagnosticHook registers a diagnostic hook function that will get called when diagnostics is called for
// as specific unit. Registering the hook at the client level means it will be called for every unit that has
// diagnostics requested.
func (c *clientV2) RegisterDiagnosticHook(name string, description string, filename string, contentType string, hook DiagnosticHook) {
	c.dmx.Lock()
	defer c.dmx.Unlock()
	c.diagHooks[name] = diagHook{
		description: description,
		filename:    filename,
		contentType: contentType,
		hook:        hook,
	}
}

// startCheckin starts the go routines to send and receive check-ins
//
// This starts 3 go routines to manage the check-in bi-directional stream. The first
// go routine starts the stream then starts one go routine to receive messages and
// another go routine to send messages. The first go routine then blocks waiting on
// the receive and send to finish, then restarts the stream or exits if the context
// has been cancelled.
func (c *clientV2) startCheckin() {
	c.wg.Add(1)

	go func() {
		defer c.wg.Done()
		for {
			select {
			case <-c.ctx.Done():
				// stopped
				return
			default:
			}

			c.checkinRoundTrip()
		}
	}()
}

func (c *clientV2) checkinRoundTrip() {
	checkinCtx, checkinCancel := context.WithCancel(c.ctx)
	defer checkinCancel()

	checkinClient, err := c.client.CheckinV2(checkinCtx)
	if err != nil {
		c.errCh <- err
		return
	}

	// ensure first checkin include version information
	c.versionInfoSent = false

	var checkinRead sync.WaitGroup
	var checkinWrite sync.WaitGroup
	done := make(chan bool)

	// expected state check-ins
	checkinRead.Add(1)
	go func() {
		defer checkinRead.Done()
		for {
			expected, err := checkinClient.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					c.errCh <- err
				}
				close(done)
				return
			}
			c.sync(expected)
		}
	}()

	// observed state check-ins
	checkinWrite.Add(1)
	go func() {
		defer checkinWrite.Done()

		if err := c.sendObserved(checkinClient); err != nil {
			if !errors.Is(err, io.EOF) {
				c.errCh <- err
			}
			return
		}

		t := time.NewTicker(c.minCheckTimeout)
		defer t.Stop()

		for {
			select {
			case <-done:
				return
			case <-c.kickSendObservedCh:
				if err := c.sendObserved(checkinClient); err != nil {
					if !errors.Is(err, io.EOF) {
						c.errCh <- err
					}
					return
				}
				t.Reset(c.minCheckTimeout)
			case <-t.C:
				if err := c.sendObserved(checkinClient); err != nil {
					if !errors.Is(err, io.EOF) {
						c.errCh <- err
					}
					return
				}
			}
		}
	}()

	// wait for write goroutine to quit then close the client
	checkinWrite.Wait()
	checkinClient.CloseSend()

	// then wait for the read to quit
	checkinRead.Wait()
}

// sendObserved sends the observed state of all the units.
func (c *clientV2) sendObserved(client proto.ElasticAgent_CheckinV2Client) error {
	c.unitsMu.RLock()
	observed := make([]*proto.UnitObserved, 0, len(c.units))
	for _, unit := range c.units {
		observed = append(observed, unit.toObserved())
	}
	c.unitsMu.RUnlock()
	msg := &proto.CheckinObserved{
		Token:       c.token,
		Units:       observed,
		VersionInfo: nil,
	}
	if !c.versionInfoSent {
		c.versionInfoSent = true
		msg.VersionInfo = &proto.CheckinObservedVersionInfo{
			Name:    c.versionInfo.Name,
			Version: c.versionInfo.Version,
			Meta:    c.versionInfo.Meta,
		}
	}
	return client.Send(msg)
}

// sync syncs the expected state with the current state.
func (c *clientV2) sync(expected *proto.CheckinExpected) {
	if expected.AgentInfo != nil {
		c.agentInfoMu.Lock()
		c.agentInfo = &AgentInfo{
			ID:       expected.AgentInfo.Id,
			Version:  expected.AgentInfo.Version,
			Snapshot: expected.AgentInfo.Snapshot,
		}
		c.agentInfoMu.Unlock()
	}

	if expected.Features != nil {
		c.features = expected.Features
		c.featuresIdx = expected.FeaturesIdx
	}

	c.syncUnits(expected)
}

func (c *clientV2) syncUnits(expected *proto.CheckinExpected) {
	c.unitsMu.Lock()
	defer c.unitsMu.Unlock()
	i := 0
	removed := false
	for _, unit := range c.units {
		if inExpected(unit, expected.Units) {
			c.units[i] = unit
			i++
		} else {
			changed := UnitChanged{
				Type: UnitChangedRemoved,
				Unit: unit,
				// When a unity is removed, no change to other units but the
				// features changed as well.
				Triggers: TriggeredFeatureChange,
				Features: expected.Features,
			}

			c.changesCh <- changed
			removed = true
		}
	}

	// resize so units that no longer exist are removed from the slice
	c.units = c.units[:i]
	for _, agentUnit := range expected.Units {
		unit := c.findUnit(agentUnit.Id, UnitType(agentUnit.Type))
		if unit == nil {
			// new unit
			unit = newUnit(
				agentUnit.Id,
				UnitType(agentUnit.Type),
				UnitState(agentUnit.State),
				UnitLogLevel(agentUnit.LogLevel),
				agentUnit.Config,
				agentUnit.ConfigStateIdx,
				c)
			c.units = append(c.units, unit)

			changed := UnitChanged{
				Type:     UnitChangedAdded,
				Unit:     unit,
				Triggers: TriggeredFeatureChange,
				Features: expected.Features,
			}

			c.changesCh <- changed
		} else {
			// existing unit
			triggers := unit.updateState(
				UnitState(agentUnit.State),
				UnitLogLevel(agentUnit.LogLevel),
				agentUnit.Config,
				agentUnit.ConfigStateIdx) {

				changed := UnitChanged{
					Triggers: triggers,
					Type:     UnitChangedModified,
					Unit:     unit,
				}

			changed := UnitChanged{
				Triggers: triggers,
				Type:     UnitChangedModified,
				Unit:     unit,
			}

			if unit.featuresIdx != expected.FeaturesIdx {
				unit.featuresIdx = expected.FeaturesIdx
				if !gproto.Equal(unit.features, expected.Features) {
					unit.features = expected.Features
					changed.Triggers |= TriggeredFeatureChange
				}
			}

			if changed.Triggers > 0 { // a.k.a something changed
				c.changesCh <- changed
			}
		}
	}

	if removed {
		// unit removed send updated observed change so agent is notified now
		// otherwise it will not be notified until the next checkin timeout
		c.unitChanged()
	}
}

// findUnit finds an existing unit.
func (c *clientV2) findUnit(id string, unitType UnitType) *Unit {
	for _, unit := range c.units {
		if unit.id == id && unit.unitType == unitType {
			return unit
		}
	}
	return nil
}

// unitChanged triggers the send goroutine to send a new observed state
func (c *clientV2) unitChanged() {
	if len(c.kickSendObservedCh) <= 0 {
		c.kickSendObservedCh <- struct{}{}
	}
}

// startActions starts the go routines to send and receive actions
//
// This starts 3 go routines to manage the actions bi-directional stream. The first
// go routine starts the stream then starts one go routine to receive messages and
// another go routine to send messages. The first go routine then blocks waiting on
// the receive and send to finish, then restarts the stream or exits if the context
// has been cancelled.
func (c *clientV2) startActions() {
	c.wg.Add(1)

	// results are held outside of the retry loop, because on re-connect
	// we still want to send the responses that either failed or haven't been
	// sent back to the agent.
	actionResults := make(chan *proto.ActionResponse, 100)
	go func() {
		defer c.wg.Done()
		for {
			select {
			case <-c.ctx.Done():
				// stopped
				return
			default:
			}

			c.actionRoundTrip(actionResults)
		}
	}()
}

func (c *clientV2) actionRoundTrip(actionResults chan *proto.ActionResponse) {
	actionsCtx, actionsCancel := context.WithCancel(c.ctx)
	defer actionsCancel()
	actionsClient, err := c.client.Actions(actionsCtx)
	if err != nil {
		c.errCh <- err
		return
	}

	var actionsRead sync.WaitGroup
	var actionsWrite sync.WaitGroup
	done := make(chan bool)

	// action requests
	actionsRead.Add(1)
	go func() {
		defer actionsRead.Done()
		for {
			action, err := actionsClient.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					c.errCh <- err
				}
				close(done)
				return
			}

			switch action.Type {
			case proto.ActionRequest_CUSTOM:
				c.tryPerformAction(actionResults, action)
			case proto.ActionRequest_DIAGNOSTICS:
				c.tryPerformDiagnostics(actionResults, action)
			default:
				actionResults <- &proto.ActionResponse{
					Token:  c.token,
					Id:     action.Id,
					Status: proto.ActionResponse_FAILED,
					Result: ActionTypeUnknown,
				}
			}
		}
	}()

	// action responses
	actionsWrite.Add(1)
	go func() {
		defer actionsWrite.Done()

		// initial connection of stream must send the token so
		// the Elastic Agent knows this clients token.
		err := actionsClient.Send(&proto.ActionResponse{
			Token:  c.token,
			Id:     ActionResponseInitID,
			Status: proto.ActionResponse_SUCCESS,
			Result: []byte("{}"),
		})
		if err != nil {
			c.errCh <- err
			return
		}

		for {
			select {
			case <-done:
				return
			case res := <-actionResults:
				err := actionsClient.Send(res)
				if err != nil {
					// failed to send, add back to response to try again
					actionResults <- res
					c.errCh <- err
					return
				}
			}
		}
	}()

	// wait for write goroutine to quit then close the client
	actionsWrite.Wait()
	actionsClient.CloseSend()

	// then wait for the read to quit
	actionsRead.Wait()
}

func (c *clientV2) tryPerformAction(actionResults chan *proto.ActionResponse, action *proto.ActionRequest) {
	// find the unit
	c.unitsMu.RLock()
	unit := c.findUnit(action.UnitId, UnitType(action.UnitType))
	c.unitsMu.RUnlock()
	if unit == nil {
		actionResults <- &proto.ActionResponse{
			Token:  c.token,
			Id:     action.Id,
			Status: proto.ActionResponse_FAILED,
			Result: ActionErrUnitNotFound,
		}
		return
	}

	// find the action registered with the unit
	unit.amx.RLock()
	actionImpl, ok := unit.actions[action.Name]
	unit.amx.RUnlock()
	if !ok {
		actionResults <- &proto.ActionResponse{
			Token:  c.token,
			Id:     action.Id,
			Status: proto.ActionResponse_FAILED,
			Result: ActionErrUndefined,
		}
		return
	}

	// ensure that the parameters can be unmarshalled
	var params map[string]interface{}
	err := json.Unmarshal(action.Params, &params)
	if err != nil {
		actionResults <- &proto.ActionResponse{
			Token:  c.token,
			Id:     action.Id,
			Status: proto.ActionResponse_FAILED,
			Result: ActionErrUnmarshableParams,
		}
		return
	}

	// perform the action (in goroutine)
	go func() {
		res, err := actionImpl.Execute(c.ctx, params)
		if err != nil {
			actionResults <- &proto.ActionResponse{
				Token:  c.token,
				Id:     action.Id,
				Status: proto.ActionResponse_FAILED,
				Result: utils.JSONMustMarshal(map[string]string{
					"error": err.Error(),
				}),
			}
			return
		}
		resBytes, err := json.Marshal(res)
		if err != nil {
			// client-side error, should have been marshal-able
			c.errCh <- err
			actionResults <- &proto.ActionResponse{
				Token:  c.token,
				Id:     action.Id,
				Status: proto.ActionResponse_FAILED,
				Result: ActionErrUnmarshableResult,
			}
			return
		}
		actionResults <- &proto.ActionResponse{
			Token:  c.token,
			Id:     action.Id,
			Status: proto.ActionResponse_SUCCESS,
			Result: resBytes,
		}
	}()
}

func (c *clientV2) tryPerformDiagnostics(actionResults chan *proto.ActionResponse, action *proto.ActionRequest) {
	// find the unit
	c.unitsMu.RLock()
	unit := c.findUnit(action.UnitId, UnitType(action.UnitType))
	c.unitsMu.RUnlock()
	if unit == nil {
		actionResults <- &proto.ActionResponse{
			Token:  c.token,
			Id:     action.Id,
			Status: proto.ActionResponse_FAILED,
			Result: ActionErrUnitNotFound,
		}
		return
	}

	// gather diagnostics hooks for this unit
	diagHooks := make(map[string]diagHook)
	c.dmx.RLock()
	for n, d := range c.diagHooks {
		diagHooks[n] = d
	}
	c.dmx.RUnlock()

	// unit hooks after client hooks; allows unit specific to override client hooks
	unit.dmx.RLock()
	for n, d := range unit.diagHooks {
		diagHooks[n] = d
	}
	unit.dmx.RUnlock()

	// perform diagnostics in goroutine, so we don't block other work
	go func() {
		res := make([]*proto.ActionDiagnosticUnitResult, 0, len(diagHooks))
		for n, d := range diagHooks {
			content := d.hook()
			res = append(res, &proto.ActionDiagnosticUnitResult{
				Name:        n,
				Filename:    d.filename,
				Description: d.description,
				ContentType: d.contentType,
				Content:     content,
				Generated:   timestamppb.New(time.Now().UTC()),
			})
		}
		actionResults <- &proto.ActionResponse{
			Token:      c.token,
			Id:         action.Id,
			Status:     proto.ActionResponse_SUCCESS,
			Diagnostic: res,
		}
	}()
}

func (c *clientV2) registerDefaultDiagnostics() {
	c.registerPprofDiagnostics("goroutine", "stack traces of all current goroutines")
	c.registerPprofDiagnostics("heap", "a sampling of memory allocations of live objects")
	c.registerPprofDiagnostics("allocs", "a sampling of all past memory allocations")
	c.registerPprofDiagnostics("threadcreate", "stack traces that led to the creation of new OS threads")
	c.registerPprofDiagnostics("block", "stack traces that led to blocking on synchronization primitives")
	c.registerPprofDiagnostics("mutex", "stack traces of holders of contended mutexes")
}

func (c *clientV2) registerPprofDiagnostics(name string, description string) {
	c.RegisterDiagnosticHook(name, description, fmt.Sprintf("%s.txt", name), "plain/text", func() []byte {
		var w bytes.Buffer
		err := pprof.Lookup(name).WriteTo(&w, 1)
		if err != nil {
			// error is returned as the content
			return []byte(fmt.Sprintf("failed to write pprof to bytes buffer: %s", err))
		}
		return w.Bytes()
	})
}

func inExpected(unit *Unit, expected []*proto.UnitExpected) bool {
	for _, agentUnit := range expected {
		if unit.id == agentUnit.Id && unit.unitType == UnitType(agentUnit.Type) {
			return true
		}
	}
	return false
}
