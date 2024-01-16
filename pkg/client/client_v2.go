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
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/elastic/elastic-agent-client/v7/pkg/client/chunk"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/elastic/elastic-agent-client/v7/pkg/utils"
	"github.com/elastic/elastic-agent-libs/api/npipe"
)

// DefaultMaxMessageSize is the maximum message size that is allowed to be sent.
const DefaultMaxMessageSize = 1024 * 1024 * 4 // copied from the gRPC default

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
	// TriggeredAPMChange indicates that the APM configuration has changed
	// This constant represents a single bit in a bitmask
	TriggeredAPMChange // apm_config_change_triggered
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

	if current&TriggeredAPMChange == TriggeredAPMChange {
		current &= ^TriggeredAPMChange
		triggers = append(triggers, "apm_config_change_triggered")
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
	// Meta is any extra metadata information about the version.
	Meta map[string]string
	// BuildHash is the VCS commit hash the program was built from.
	BuildHash string
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
	// RegisterOptionalDiagnosticHook is the same as RegisterDiagnosticHook, but marks a given callback as optional, and tied to a given
	// paramater tag that is specified in the `params` field of the V2 Action request. The diagnostic will not be run unless the
	// diagnostic action event contains that paramiter tag. See ActionRequest in elastic-agent-client.proto and DiagnosticParams in
	// diagnostics.go
	RegisterOptionalDiagnosticHook(paramTag string, name string, description string, filename string, contentType string, hook DiagnosticHook)
}

// v2options hold the client options.
type v2options struct {
	maxMessageSize  int
	chunkingAllowed bool
	dialOptions     []grpc.DialOption
	agentInfo       *AgentInfo
}

// DialOptions returns the dial options for the GRPC connection.
func (o *v2options) DialOptions() []grpc.DialOption {
	opts := make([]grpc.DialOption, 0, len(o.dialOptions)+1)
	opts = append(opts, o.dialOptions...)
	opts = append(opts, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(o.maxMessageSize), grpc.MaxCallSendMsgSize(o.maxMessageSize)))
	return opts
}

// V2ClientOption is an option that can be used when creating the client.
type V2ClientOption func(*v2options)

// WithMaxMessageSize sets the maximum message size.
func WithMaxMessageSize(size int) V2ClientOption {
	return func(o *v2options) {
		o.maxMessageSize = size
	}
}

// WithChunking sets if the client can use chunking with the server.
func WithChunking(enabled bool) V2ClientOption {
	return func(o *v2options) {
		o.chunkingAllowed = enabled
	}
}

// WithGRPCDialOptions allows the setting of GRPC dial options.
func WithGRPCDialOptions(opts ...grpc.DialOption) V2ClientOption {
	return func(o *v2options) {
		o.dialOptions = append(o.dialOptions, opts...)
	}
}

// WithAgentInfo sets the AgentInfo and updates the client's VersionInfo.Version
// to match the given agentInfo.Version.
func WithAgentInfo(agentInfo AgentInfo) V2ClientOption {
	return func(o *v2options) {
		o.agentInfo = &agentInfo
	}
}

// clientV2 manages the state and communication to the Elastic Agent over the V2 control protocol.
type clientV2 struct {
	target string
	opts   v2options
	token  string

	agentInfoMu sync.RWMutex
	agentInfo   *AgentInfo

	versionInfo     VersionInfo
	versionInfoSent bool

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

	componentMu sync.RWMutex
	// Must hold `componentMu` for changing component fields below
	componentIdx    uint64
	componentConfig *proto.Component

	dmx       sync.RWMutex
	diagHooks map[string]diagHook

	storeClient    proto.ElasticAgentStoreClient
	artifactClient proto.ElasticAgentArtifactClient
	logClient      proto.ElasticAgentLogClient

	// overridden in tests to make fast
	minCheckTimeout time.Duration
}

// NewV2 creates a client connection to Elastic Agent over the V2 control protocol.
// The "target" can be prefixed with scheme as explained here https://github.com/grpc/grpc/blob/master/doc/naming.md.
// unix://absolute_path for unix domain socket
// npipe:///pipe_name for windows named pipe
func NewV2(target string, token string, versionInfo VersionInfo, opts ...V2ClientOption) V2 {
	var options v2options
	options.maxMessageSize = DefaultMaxMessageSize
	for _, o := range opts {
		o(&options)
	}

	// For compatibility with existing interface the target could contain npipe:// scheme prefix
	// Set the named pipe dialer option if npipe:// prefix specified on windows
	if runtime.GOOS == "windows" && npipe.IsNPipe(target) {
		target = transformNPipeURL(target)
		// Set the winio named pipe dialer
		options.dialOptions = append(options.dialOptions, getOptions()...)
	}

	c := &clientV2{
		agentInfo:             options.agentInfo,
		target:                target,
		opts:                  options,
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
	conn, err := grpc.DialContext(ctx, c.target, c.opts.DialOptions()...)
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
		description:          description,
		filename:             filename,
		contentType:          contentType,
		hook:                 hook,
		optionalWithParamTag: "",
	}
}

// RegisterOptionalDiagnosticHook is the same as RegisterDiagnosticHook, but marks a given callback as optional, and tied to a given
// paramater tag that is specified in the `params` field of the V2 Action request. The diagnostic will not be run unless the
// diagnostic action event contains that paramiter tag. See ActionRequest in elastic-agent-client.proto and DiagnosticParams in
// diagnostics.go
func (c *clientV2) RegisterOptionalDiagnosticHook(paramTag string, name string, description string, filename string, contentType string, hook DiagnosticHook) {
	c.dmx.Lock()
	defer c.dmx.Unlock()
	c.diagHooks[name] = diagHook{
		description:          description,
		filename:             filename,
		contentType:          contentType,
		hook:                 hook,
		optionalWithParamTag: paramTag,
	}
}

// durationOf wraps a given function call and measures the time it consumed.
func durationOf(callback func()) time.Duration {
	before := time.Now()
	callback()
	return time.Since(before)
}

func nextExpBackoff(current time.Duration, max time.Duration) time.Duration {
	if current*2 >= max {
		return max
	}
	return current * 2
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
		// If the initial RPC connection fails, checkinRoundTrip
		// returns immediately, so we set a timer to avoid spinlocking
		// on the error.
		retryInterval := 1 * time.Second
		retryTimer := time.NewTimer(retryInterval)
		for c.ctx.Err() == nil {
			checkinDuration := durationOf(c.checkinRoundTrip)

			if checkinDuration < time.Second {
				// If the checkin round trip lasted less than a second, we're
				// probably in an error loop -- apply exponential backoff up to 30s.
				retryInterval = nextExpBackoff(retryInterval, 30*time.Second)
			} else {
				retryInterval = 1 * time.Second
			}

			// After the RPC client closes, wait until either the timer interval
			// expires or the context is cancelled. Note that the timer waits
			// one second since the checkinRoundTrip was last _called_, not
			// since it returns; immediate retries are ok after an active
			// connection shuts down.
			select {
			case <-retryTimer.C:
				retryTimer.Reset(retryInterval)
			case <-c.ctx.Done():
			}
		}
	}()
}

func (c *clientV2) checkinRoundTrip() {
	checkinCtx, checkinCancel := context.WithCancel(c.ctx)
	defer checkinCancel()

	// Return immediately if we can't establish an initial RPC connection.
	checkinClient, err := c.client.CheckinV2(checkinCtx)
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
		expected, err := chunk.RecvExpected(checkinClient)
		for ; err == nil; expected, err = chunk.RecvExpected(checkinClient) {
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

	// Always resent the version information on restart of the loop.
	c.versionInfoSent = false

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

	c.componentMu.RLock()
	componentIdx := c.componentIdx
	c.componentMu.RUnlock()

	msg := &proto.CheckinObserved{
		Token:        c.token,
		Units:        observed,
		FeaturesIdx:  featuresIdx,
		ComponentIdx: componentIdx,
		VersionInfo:  nil,
	}
	if !c.versionInfoSent {
		msg.VersionInfo = &proto.CheckinObservedVersionInfo{
			Name:      c.versionInfo.Name,
			Meta:      c.versionInfo.Meta,
			BuildHash: c.versionInfo.BuildHash,
		}
		// supports information is sent when version information is set,
		// this ensures that its always sent once per connected loop
		if c.opts.chunkingAllowed {
			msg.Supports = []proto.ConnectionSupports{proto.ConnectionSupports_CheckinChunking}
		}
	}
	err := sendObservedChunked(client, msg, c.opts.chunkingAllowed, c.opts.maxMessageSize)
	if err != nil && !errors.Is(err, io.EOF) {
		c.errCh <- err
	} else {
		c.versionInfoSent = true
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

	c.syncComponent(expected)
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

// for now the component configuration is applied to the whole process globally
// when there is a need of propagating changes we should implement an update channel
// similar to `UnitChanges() <-chan UnitChanged`, however, this would be a breaking change
// because a user of the client will have to read from this channel,
// otherwise the client would stay blocked.
func (c *clientV2) syncComponent(expected *proto.CheckinExpected) {
	c.componentMu.Lock()
	defer c.componentMu.Unlock()

	// applying the component limits
	var (
		prevGoMaxProcs int
		newGoMaxProcs  int
	)
	if c.componentConfig != nil && c.componentConfig.Limits != nil {
		prevGoMaxProcs = int(c.componentConfig.Limits.GoMaxProcs)
	}
	if expected.Component != nil && expected.Component.Limits != nil {
		newGoMaxProcs = int(expected.Component.Limits.GoMaxProcs)
	}

	// calling `runtime.GOMAXPROCS` is expensive, so we call it only when the value really changed
	if newGoMaxProcs != prevGoMaxProcs {
		if newGoMaxProcs == 0 {
			_ = runtime.GOMAXPROCS(runtime.NumCPU())
		} else {
			_ = runtime.GOMAXPROCS(newGoMaxProcs)
		}
	}

	// Technically we should wait until the APM config is also applied, but the syncUnits is called after this and
	// we have a single index for the whole component
	c.componentConfig = expected.Component
	c.componentIdx = expected.ComponentIdx
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

	var apmConfig *proto.APMConfig
	func() {
		c.componentMu.RLock()
		defer c.componentMu.RUnlock()
		if c.componentConfig != nil {
			apmConfig = c.componentConfig.ApmConfig
		}
	}()

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
				apmConfig,
				c)
			c.units = append(c.units, unit)

			changed := UnitChanged{
				Type: UnitChangedAdded,
				Unit: unit,
			}

			if expected.Features != nil {
				changed.Triggers = TriggeredFeatureChange
			}

			if apmConfig != nil {
				changed.Triggers |= TriggeredAPMChange
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
				agentUnit.ConfigStateIdx,
				apmConfig,
			)

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
	// Sending to stateChangeObservedCh will trigger checkinWriter to
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
		// If the initial RPC connection fails, actionRoundTrip
		// returns immediately, so we set a timer to avoid spinlocking
		// on the error.
		retryInterval := 1 * time.Second
		retryTimer := time.NewTimer(retryInterval)
		for c.ctx.Err() == nil {
			actionDuration := durationOf(func() {
				c.actionRoundTrip(actionResults)
			})

			if actionDuration < time.Second {
				// If the action round trip lasted less than a second, we're
				// probably in an error loop -- apply exponential backoff up to 30s.
				retryInterval = nextExpBackoff(retryInterval, 30*time.Second)
			} else {
				retryInterval = 1 * time.Second
			}

			// After the RPC client closes, wait until either the timer interval
			// expires or the context is cancelled. Note that the timer waits
			// one second since the checkinRoundTrip was last _called_, not
			// since it returns; immediate retries are ok after an active
			// connection shuts down.
			select {
			case <-retryTimer.C:
				retryTimer.Reset(retryInterval)
			case <-c.ctx.Done():
			}
		}
	}()
}

func (c *clientV2) actionRoundTrip(actionResults chan *proto.ActionResponse) {
	actionsCtx, actionsCancel := context.WithCancel(c.ctx)
	defer actionsCancel()

	// Return immediately if we can't establish an initial RPC connection.
	actionsClient, err := c.client.Actions(actionsCtx)
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
	// see if we can unpack params
	foundParams := map[string]bool{}
	if len(action.GetParams()) > 0 {
		params := DiagnosticParams{}
		err := json.Unmarshal(action.GetParams(), &params)
		if err != nil {
			actionResults <- &proto.ActionResponse{
				Token:  c.token,
				Id:     action.GetId(),
				Status: proto.ActionResponse_FAILED,
				Result: []byte(fmt.Sprintf("error unmarshaling json in params: %s", err)),
			}
		}
		// convert to a map for easier checking
		for _, param := range params.AdditionalMetrics {
			foundParams[param] = true
		}
	}

	// break apart the action:
	// if it's unit-level, fetch unit-level diagnostics.
	// if component-level, just run diagnostics that are registered at the client level.
	diagHooks := make(map[string]diagHook)
	if action.GetLevel() == proto.ActionRequest_COMPONENT || action.GetLevel() == proto.ActionRequest_ALL {
		c.dmx.RLock()
		for n, d := range c.diagHooks {
			diagHooks[n] = d
		}
		c.dmx.RUnlock()

	}

	if action.GetLevel() == proto.ActionRequest_UNIT || action.GetLevel() == proto.ActionRequest_ALL {
		// find the unit
		c.unitsMu.RLock()
		unit := c.findUnit(action.GetUnitId(), UnitType(action.GetUnitType()))
		c.unitsMu.RUnlock()
		if unit == nil {
			actionResults <- &proto.ActionResponse{
				Token:  c.token,
				Id:     action.GetId(),
				Status: proto.ActionResponse_FAILED,
				Result: ActionErrUnitNotFound,
			}
			return
		}

		// gather diagnostics hooks for this unit
		unit.dmx.RLock()
		for n, d := range unit.diagHooks {
			diagHooks[n] = d
		}
		unit.dmx.RUnlock()
	}

	// perform diagnostics in goroutine, so we don't block other work
	go func() {
		res := make([]*proto.ActionDiagnosticUnitResult, 0, len(diagHooks))
		for diagName, diagHook := range diagHooks {
			// if the hook came with a tag, check it.
			// if the callback was registered with a tag but none was found in the request, skip it
			if diagHook.optionalWithParamTag != "" {
				if _, ok := foundParams[diagHook.optionalWithParamTag]; !ok {
					continue
				}
			}
			content := diagHook.hook()
			res = append(res, &proto.ActionDiagnosticUnitResult{
				Name:        diagName,
				Filename:    diagHook.filename,
				Description: diagHook.description,
				ContentType: diagHook.contentType,
				Content:     content,
				Generated:   timestamppb.New(time.Now().UTC()),
			})
		}
		actionResults <- &proto.ActionResponse{
			Token:      c.token,
			Id:         action.GetId(),
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

	c.RegisterOptionalDiagnosticHook("CPU", "cpu-pprof", "CPU profile", "cpu.pprof", "application/octet-stream", createCPUProfile)
}

func createCPUProfile() []byte {
	var writeBuf bytes.Buffer
	err := pprof.StartCPUProfile(&writeBuf)
	if err != nil {
		return []byte(fmt.Sprintf("error starting CPU profile: %s", err))
	}
	time.Sleep(time.Second * 30)
	pprof.StopCPUProfile()
	return writeBuf.Bytes()
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

func sendObservedChunked(client proto.ElasticAgent_CheckinV2Client, msg *proto.CheckinObserved, chunkingAllowed bool, maxSize int) error {
	if !chunkingAllowed {
		// chunking is disabled
		return client.Send(msg)
	}
	msgs, err := chunk.Observed(msg, maxSize)
	if err != nil {
		return err
	}
	for _, msg := range msgs {
		if err := client.Send(msg); err != nil {
			return err
		}
	}
	return nil
}
