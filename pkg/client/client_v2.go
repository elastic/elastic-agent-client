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

	grpc_retry "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/retry"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/elastic/elastic-agent-client/v7/pkg/utils"
)

type (
	// UnitChangedType defines types for when units are adjusted.
	UnitChangedType int
	// Trigger indicates what triggered a change. This is a bitmask as
	// a unit change can have one or more triggers that caused the change.
	Trigger uint
)

const (
	// UnitChangedAdded is when a new unit is added.
	UnitChangedAdded UnitChangedType = iota + 1 // unit_added
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
	// This constant represents a single bit in a bitmask. @see Trigger.
	TriggeredConfigChange Trigger = 1 << iota // config_change_triggered
	// TriggeredFeatureChange indicates a change in the features triggered the change.
	// This constant represents a single bit in a bitmask. @see Trigger.
	TriggeredFeatureChange // feature_change_triggered
	// TriggeredLogLevelChange indicates a change the log level triggered the change.
	// This constant represents a single bit in a bitmask. @see Trigger.
	TriggeredLogLevelChange // log_level_triggered
	// TriggeredStateChange indicates when a unit state has ganged.
	// This constant represents a single bit in a bitmask. @see Trigger.
	TriggeredStateChange // state_change_triggered
)

func (t Trigger) String() string {
	var triggers []string
	if t == 0 {
		return "nothing_triggered"
	}

	current := t
	if current&TriggeredConfigChange == TriggeredConfigChange {
		current &= ^TriggeredConfigChange
		triggers = append(triggers, "config_change_triggered")
	}
	if current&TriggeredFeatureChange == TriggeredFeatureChange {
		current &= ^TriggeredFeatureChange
		triggers = append(triggers, "feature_change_triggered")
	}
	if current&TriggeredLogLevelChange == TriggeredLogLevelChange {
		current &= ^TriggeredLogLevelChange
		triggers = append(triggers, "log_level_triggered")
	}
	if current&TriggeredStateChange == TriggeredStateChange {
		current &= ^TriggeredStateChange
		triggers = append(triggers, "state_change_triggered")
	}

	if current != 0 {
		return fmt.Sprintf("invalid trigger value: %d", t)
	}

	return strings.Join(triggers, ", ")
}

// GoString is the same as String, but for the %#v format.
func (t Trigger) GoString() string { return t.String() }

// String returns a string representation for the unit changed type.
func (t UnitChangedType) String() string {
	switch t {
	case UnitChangedAdded:
		return "added"
	case UnitChangedModified:
		return "modified"
	case UnitChangedRemoved:
		return "removed"
	}

	return "unknown"
}

// GoString is the same as String, but for the %#v format.
func (t UnitChangedType) GoString() string {
	return t.String()
}

// UnitChanged is what is sent over the UnitChanged channel any time a change happens:
//   - a unit is added, modified, or removed
//   - a feature changes
type UnitChanged struct {
	Type     UnitChangedType
	Triggers Trigger
	// Unit is any change in a unit.
	Unit *Unit
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

	versionInfo         VersionInfo
	sendVersionInfoOnce sync.Once

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	client proto.ElasticAgentClient

	errCh     chan error
	changesCh chan UnitChanged

	// stateChangeObservedCh is an internal channel that notifies checkinWriter
	// that a unit state has changed. To trigger it, call unitsStateChanged.
	stateChangeObservedCh chan struct{}

	unitsMu sync.RWMutex
	// Must hold unitsMu to access units or featuresIdx, since they
	// must be synchronized with each other.
	units       []*Unit
	featuresIdx uint64

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
		target:                target,
		opts:                  opts,
		token:                 token,
		versionInfo:           versionInfo,
		stateChangeObservedCh: make(chan struct{}, 1),
		errCh:                 make(chan error),
		changesCh:             make(chan UnitChanged),
		diagHooks:             make(map[string]diagHook),
		minCheckTimeout:       CheckinMinimumTimeout,
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
		for c.ctx.Err() == nil {
			c.checkinRoundTrip()
		}
	}()
}

func (c *clientV2) checkinRoundTrip() {
	checkinCtx, checkinCancel := context.WithCancel(c.ctx)
	defer checkinCancel()

	// Return immediately if we can't establish an initial RPC connection.
	checkinClient, err := c.client.CheckinV2(checkinCtx, grpc_retry.WithPerRetryTimeout(1*time.Second))
	if err != nil {
		c.errCh <- err
		return
	}

	// wg tracks when the reader and writer loops terminate.
	var wg sync.WaitGroup

	// readerDone is closed by the reader (expected state) loop when it
	// terminates, so the writer (observed state) loop knows to return
	// as well.
	readerDone := make(chan struct{})

	// expected state check-ins (reader)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(readerDone)
		expected, err := checkinClient.Recv()
		for ; err == nil; expected, err = checkinClient.Recv() {
			c.applyExpected(expected)
		}
		if !errors.Is(err, io.EOF) {
			c.errCh <- err
		}
	}()

	// observed state check-ins (writer)
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.checkinWriter(checkinClient, readerDone)
		_ = checkinClient.CloseSend()
	}()

	// Wait for reader and writer to finish before returning.
	wg.Wait()
}

func (c *clientV2) checkinWriter(
	checkinClient proto.ElasticAgent_CheckinV2Client,
	done chan struct{},
) {
	t := time.NewTicker(c.minCheckTimeout)
	defer t.Stop()

	// Keep sending until the call returns an error
	for c.sendObserved(checkinClient) == nil {

		// Wait until the ticker goes off, or we're notified of an update.
		select {
		case <-c.stateChangeObservedCh:
			// We're sending an off-cycle update, so reset the ticker timeout
			t.Reset(c.minCheckTimeout)
		case <-t.C:
		case <-done:
			return
		}
	}
}

// sendObserved sends the observed state of all the units.
// If the send produces any error except io.EOF, sendObserved
// forwards it to the client's error channel before returning.
func (c *clientV2) sendObserved(client proto.ElasticAgent_CheckinV2Client) error {
	c.unitsMu.RLock()
	observed := make([]*proto.UnitObserved, 0, len(c.units))
	for _, unit := range c.units {
		observed = append(observed, unit.toObserved())
	}
	// Check featuresIdx within the same mutex block since we
	// need observed and featuresIdx to be synchronized with each other.
	featuresIdx := c.featuresIdx
	c.unitsMu.RUnlock()

	msg := &proto.CheckinObserved{
		Token:       c.token,
		Units:       observed,
		FeaturesIdx: featuresIdx,
		VersionInfo: nil,
	}
	c.sendVersionInfoOnce.Do(func() {
		msg.VersionInfo = &proto.CheckinObservedVersionInfo{
			Name:    c.versionInfo.Name,
			Version: c.versionInfo.Version,
			Meta:    c.versionInfo.Meta,
		}
	})
	err := client.Send(msg)
	if err != nil && !errors.Is(err, io.EOF) {
		c.errCh <- err
	}
	return err
}

// applyExpected syncs the expected state with the current state.
func (c *clientV2) applyExpected(expected *proto.CheckinExpected) {
	if expected.AgentInfo != nil {
		c.agentInfoMu.Lock()
		c.agentInfo = &AgentInfo{
			ID:       expected.AgentInfo.Id,
			Version:  expected.AgentInfo.Version,
			Snapshot: expected.AgentInfo.Snapshot,
		}
		c.agentInfoMu.Unlock()
	}

	c.syncUnits(expected)
}

// Remove from the client any units not in the given expected list,
// returning a count of the units removed.
// Caller must hold c.unitsMu.
func (c *clientV2) removeUnexpectedUnits(expected *proto.CheckinExpected) int {
	remainingCount := 0
	removedCount := 0
	// Coalesce c.units by moving all units that are still expected to
	// the beginning of the array and truncating the rest.
	for _, unit := range c.units {
		if inExpected(unit, expected.Units) {
			c.units[remainingCount] = unit
			remainingCount++
		} else {
			c.changesCh <- UnitChanged{
				Type: UnitChangedRemoved,
				Unit: unit,
			}
			removedCount++
		}
	}
	c.units = c.units[:remainingCount]
	return removedCount
}

func (c *clientV2) syncUnits(expected *proto.CheckinExpected) {
	c.unitsMu.Lock()
	defer c.unitsMu.Unlock()

	if c.removeUnexpectedUnits(expected) > 0 {
		// Most state changes are reported by the unit itself via
		// Unit.UpdateState, which schedules a checkin update by calling
		// unitsStateChanged(), but removals are handled immediately
		// here so we also need to trigger the update ourselves.
		c.unitsStateChanged()
	}

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
				expected.Features,
				c)
			c.units = append(c.units, unit)

			changed := UnitChanged{
				Type: UnitChangedAdded,
				Unit: unit,
			}

			if expected.Features != nil {
				changed.Triggers = TriggeredFeatureChange
			}

			c.changesCh <- changed
		} else {
			// existing unit
			triggers := unit.updateState(
				UnitState(agentUnit.State),
				UnitLogLevel(agentUnit.LogLevel),
				expected.FeaturesIdx,
				expected.Features,
				agentUnit.Config,
				agentUnit.ConfigStateIdx)

			changed := UnitChanged{
				Triggers: triggers,
				Type:     UnitChangedModified,
				Unit:     unit,
			}

			if changed.Triggers > TriggeredNothing { // a.k.a something changed
				c.changesCh <- changed
			}
		}
	}

	// Now that we've propagated feature flags' information to units, record
	// the featuresIdx on the client so we can send it up as part of the observed
	// state in the next checkin.
	// (It's safe to write to featuresIdx here since we hold c.unitsMu.)
	c.featuresIdx = expected.FeaturesIdx
}

// findUnit finds an existing unit. Caller must hold a read lock
// on c.unitsMu.
func (c *clientV2) findUnit(id string, unitType UnitType) *Unit {
	for _, unit := range c.units {
		if unit.id == id && unit.unitType == unitType {
			return unit
		}
	}
	return nil
}

// unitsStateChanged notifies checkinWriter that there is new
// state data to send.
func (c *clientV2) unitsStateChanged() {
	// Sending to kickSendObservedCh will trigger checkinWriter to
	// send an update. If we can't send on the channel without blocking,
	// then a request is already pending so we're done.
	select {
	case c.stateChangeObservedCh <- struct{}{}:
	default:
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
		for c.ctx.Err() == nil {
			c.actionRoundTrip(actionResults)
		}
	}()
}

func (c *clientV2) actionRoundTrip(actionResults chan *proto.ActionResponse) {
	actionsCtx, actionsCancel := context.WithCancel(c.ctx)
	defer actionsCancel()

	// Return immediately if we can't establish an initial RPC connection.
	actionsClient, err := c.client.Actions(actionsCtx, grpc_retry.WithPerRetryTimeout(1*time.Second))
	if err != nil {
		c.errCh <- err
		return
	}

	// wg tracks when the reader and writer loops terminate.
	var wg sync.WaitGroup

	// readerDone is closed by the reader loop when it terminates, so the
	// writer loop knows to return as well.
	readerDone := make(chan struct{})

	// action requests
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(readerDone)
		c.actionsReader(actionsClient, actionResults)
	}()

	// action responses
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer actionsClient.CloseSend()
		c.actionsWriter(actionsClient, actionResults, readerDone)
	}()

	// Wait for reader and writer to finish before returning.
	wg.Wait()
}

func (c *clientV2) actionsReader(
	actionsClient proto.ElasticAgent_ActionsClient,
	actionResults chan *proto.ActionResponse,
) {
	action, err := actionsClient.Recv()
	for ; err == nil; action, err = actionsClient.Recv() {
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
	if !errors.Is(err, io.EOF) {
		c.errCh <- err
	}
}

func (c *clientV2) actionsWriter(
	actionsClient proto.ElasticAgent_ActionsClient,
	actionResults chan *proto.ActionResponse,
	readerDone chan struct{},
) {
	// initial connection of stream must send the token so
	// the Elastic Agent knows this clients token.
	err := actionsClient.Send(&proto.ActionResponse{
		Token:  c.token,
		Id:     ActionResponseInitID,
		Status: proto.ActionResponse_SUCCESS,
		Result: []byte("{}"),
	})
	var actionResponse *proto.ActionResponse

	for err == nil {
		select {
		case <-readerDone:
			return
		case actionResponse = <-actionResults:
			err = actionsClient.Send(actionResponse)
		}
	}
	c.errCh <- err
	if actionResponse != nil {
		// failed to send, add back to response queue to try again
		actionResults <- actionResponse
	}
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
	c.registerPprofDiagnostics("goroutine", "stack traces of all current goroutines", 0)
	c.registerPprofDiagnostics("heap", "a sampling of memory allocations of live objects", 0)
	c.registerPprofDiagnostics("allocs", "a sampling of all past memory allocations", 0)
	c.registerPprofDiagnostics("threadcreate", "stack traces that led to the creation of new OS threads", 0)
	c.registerPprofDiagnostics("block", "stack traces that led to blocking on synchronization primitives", 0)
	c.registerPprofDiagnostics("mutex", "stack traces of holders of contended mutexes", 0)
}

func (c *clientV2) registerPprofDiagnostics(name string, description string, debug int) {
	var filename string
	var contentType string
	switch debug {
	case 1:
		filename = name + ".txt"
		contentType = "plain/text"
	default:
		filename = name + ".pprof.gz"
		contentType = "application/octet-stream"
	}

	c.RegisterDiagnosticHook(name, description, filename, contentType, func() []byte {
		var w bytes.Buffer
		err := pprof.Lookup(name).WriteTo(&w, debug)
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
