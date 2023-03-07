// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"reflect"
	"sync"

	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

// UnitType is the type of the unit, either input or output
type UnitType proto.UnitType

const (
	// UnitTypeInput is an input unit.
	UnitTypeInput = UnitType(proto.UnitType_INPUT)
	// UnitTypeOutput is an output unit.
	UnitTypeOutput = UnitType(proto.UnitType_OUTPUT)
)

// String returns string representation for the unit type.
func (t UnitType) String() string {
	switch t {
	case UnitTypeInput:
		return "input"
	case UnitTypeOutput:
		return "output"
	}
	return "unknown"
}

// UnitLogLevel is the log level the unit should run at.
type UnitLogLevel proto.UnitLogLevel

const (
	// UnitLogLevelError is when the unit should log at error level.
	UnitLogLevelError = UnitLogLevel(proto.UnitLogLevel_ERROR)
	// UnitLogLevelWarn is when the unit should log at warn level.
	UnitLogLevelWarn = UnitLogLevel(proto.UnitLogLevel_WARN)
	// UnitLogLevelInfo is when the unit should log at info level.
	UnitLogLevelInfo = UnitLogLevel(proto.UnitLogLevel_INFO)
	// UnitLogLevelDebug is when the unit should log at debug level.
	UnitLogLevelDebug = UnitLogLevel(proto.UnitLogLevel_DEBUG)
	// UnitLogLevelTrace is when the unit should log at trace level.
	UnitLogLevelTrace = UnitLogLevel(proto.UnitLogLevel_TRACE)
)

// String returns string representation for the unit log level.
func (l UnitLogLevel) String() string {
	switch l {
	case UnitLogLevelError:
		return "error"
	case UnitLogLevelWarn:
		return "warn"
	case UnitLogLevelInfo:
		return "info"
	case UnitLogLevelDebug:
		return "debug"
	case UnitLogLevelTrace:
		return "trace"
	}
	return "unknown"
}

// UnitState is the state for the unit, used both for expected and observed state.
type UnitState proto.State

const (
	// UnitStateStarting is when a unit is starting.
	UnitStateStarting = UnitState(proto.State_STARTING)
	// UnitStateConfiguring is when a unit is currently configuring.
	UnitStateConfiguring = UnitState(proto.State_CONFIGURING)
	// UnitStateHealthy is when the unit is working exactly as it should.
	UnitStateHealthy = UnitState(proto.State_HEALTHY)
	// UnitStateDegraded is when the unit is working but not exactly as its expected.
	UnitStateDegraded = UnitState(proto.State_DEGRADED)
	// UnitStateFailed is when the unit is completely broken and failing to work.
	UnitStateFailed = UnitState(proto.State_FAILED)
	// UnitStateStopping is when the unit is stopping.
	UnitStateStopping = UnitState(proto.State_STOPPING)
	// UnitStateStopped is when the unit is stopped.
	UnitStateStopped = UnitState(proto.State_STOPPED)
)

// String returns string representation for the unit state.
func (l UnitState) String() string {
	switch l {
	case UnitStateStarting:
		return "STARTING"
	case UnitStateConfiguring:
		return "CONFIGURING"
	case UnitStateHealthy:
		return "HEALTHY"
	case UnitStateDegraded:
		return "DEGRADED"
	case UnitStateFailed:
		return "FAILED"
	case UnitStateStopping:
		return "STOPPING"
	case UnitStateStopped:
		return "STOPPED"
	}
	return "UNKNOWN"
}

// Unit represents a distinct item that needs to be operating with-in this process.
//
// This is normally N number of inputs and 1 output (possible for multiple in the future).
type Unit struct {
	id       string
	unitType UnitType

	expectedStateMu sync.RWMutex
	expectedState   UnitState
	logLevel        UnitLogLevel
	config          *proto.UnitExpectedConfig
	configIdx       uint64

	stateMu      sync.RWMutex
	state        UnitState
	stateMsg     string
	statePayload *structpb.Struct

	amx     sync.RWMutex
	actions map[string]Action

	client *clientV2

	dmx       sync.RWMutex
	diagHooks map[string]diagHook
}

// ID of the unit.
func (u *Unit) ID() string {
	return u.id
}

// Type of the unit.
func (u *Unit) Type() UnitType {
	return u.unitType
}

// Expected returns the expected state and config for the unit.
func (u *Unit) Expected() (UnitState, UnitLogLevel, *proto.UnitExpectedConfig) {
	u.expectedStateMu.RLock()
	defer u.expectedStateMu.RUnlock()
	return u.expectedState, u.logLevel, u.config
}

// State returns the currently reported state for the unit.
func (u *Unit) State() (UnitState, string, map[string]interface{}) {
	u.stateMu.RLock()
	defer u.stateMu.RUnlock()
	return u.state, u.stateMsg, u.statePayload.AsMap()
}

// UpdateState updates the state for the unit.
func (u *Unit) UpdateState(state UnitState, message string, payload map[string]interface{}) error {
	var statePayload *structpb.Struct
	var err error
	if payload != nil {
		statePayload, err = structpb.NewStruct(payload)
		if err != nil {
			return err
		}
	}
	u.stateMu.Lock()
	defer u.stateMu.Unlock()
	changed := false
	if u.state != state {
		u.state = state
		changed = true
	}
	if u.stateMsg != message {
		u.stateMsg = message
		changed = true
	}
	if (u.statePayload == nil && statePayload != nil) ||
		(u.statePayload != nil && statePayload == nil) ||
		!reflect.DeepEqual(u.statePayload, statePayload) {
		u.statePayload = statePayload
		changed = true
	}
	if changed {
		u.client.unitChanged()
	}
	return nil
}

// RegisterAction registers action handler for this unit.
func (u *Unit) RegisterAction(action Action) {
	u.amx.Lock()
	defer u.amx.Unlock()
	u.actions[action.Name()] = action
}

// UnregisterAction unregisters action handler with the client
func (u *Unit) UnregisterAction(action Action) {
	u.amx.Lock()
	defer u.amx.Unlock()
	delete(u.actions, action.Name())
}

// GetAction finds an action by its name.
func (u *Unit) GetAction(name string) (Action, bool) {
	u.amx.RLock()
	defer u.amx.RUnlock()
	act, ok := u.actions[name]
	return act, ok
}

// Store returns the store client.
func (u *Unit) Store() StoreClient {
	return &storeClient{
		client:   u.client,
		unitID:   u.id,
		unitType: u.unitType,
	}
}

// Artifacts returns the artifacts client.
func (u *Unit) Artifacts() ArtifactsClient {
	return u.client.Artifacts()
}

// Logger returns the log client.
func (u *Unit) Logger() LogClient {
	return &logClient{
		client:   u.client,
		unitID:   u.id,
		unitType: u.unitType,
	}
}

// RegisterDiagnosticHook registers a diagnostic hook function that will get called when diagnostics is called for
// as this unit. Registering the hook at the unit level means it will only be called when diagnostics is requested
// for this specific unit.
func (u *Unit) RegisterDiagnosticHook(name string, description string, filename string, contentType string, hook DiagnosticHook) {
	u.dmx.Lock()
	defer u.dmx.Unlock()
	u.diagHooks[name] = diagHook{
		description: description,
		filename:    filename,
		contentType: contentType,
		hook:        hook,
	}
}

// updateState updates the configuration for this unit, triggering the delegate
// function if set.
func (u *Unit) updateState(
	exp UnitState,
	logLevel UnitLogLevel,
	cfg *proto.UnitExpectedConfig,
	cfgIdx uint64) bool {
	u.expectedStateMu.Lock()
	defer u.expectedStateMu.Unlock()
	changed := false
	if u.expectedState != exp {
		u.expectedState = exp
		changed = true
	}
	if u.logLevel != logLevel {
		u.logLevel = logLevel
		changed = true
	}
	if u.configIdx != cfgIdx {
		u.configIdx = cfgIdx
		if !gproto.Equal(u.config.GetSource(), cfg.GetSource()) {
			u.config = cfg
			changed = true
		}
	}
	return changed
}

// toObserved returns the observed unit protocol to send over the stream.
func (u *Unit) toObserved() *proto.UnitObserved {
	u.expectedStateMu.RLock()
	cfgIdx := u.configIdx
	u.expectedStateMu.RUnlock()
	u.stateMu.RLock()
	defer u.stateMu.RUnlock()
	return &proto.UnitObserved{
		Id:             u.id,
		Type:           proto.UnitType(u.unitType),
		ConfigStateIdx: cfgIdx,
		State:          proto.State(u.state),
		Message:        u.stateMsg,
		Payload:        u.statePayload,
	}
}

// newUnit creates a new unit that needs to be created in this process.
func newUnit(id string, unitType UnitType, exp UnitState, logLevel UnitLogLevel, cfg *proto.UnitExpectedConfig, cfgIdx uint64, client *clientV2) *Unit {
	return &Unit{
		id:            id,
		unitType:      unitType,
		config:        cfg,
		configIdx:     cfgIdx,
		expectedState: exp,
		logLevel:      logLevel,
		state:         UnitStateStarting,
		stateMsg:      "Starting",
		client:        client,
		actions:       make(map[string]Action),
		diagHooks:     make(map[string]diagHook),
	}
}
