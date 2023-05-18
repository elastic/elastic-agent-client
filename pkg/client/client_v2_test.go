// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/pprof/profile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/elastic/elastic-agent-client/v7/pkg/client/mock"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestClientV2_DialError(t *testing.T) {
	srv := mock.StubServerV2{}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	invalidClient := NewV2(fmt.Sprintf(":%d", srv.Port), "invalid_token", VersionInfo{})
	assert.Error(t, invalidClient.Start(context.Background()))
	defer invalidClient.Stop()
}

// Test that RPC errors on client startup delay before retrying instead
// of producing a constant stream of errors.
func TestRPCErrorRetryTimer(t *testing.T) {
	// Create a TCP listener that rejects all incoming connections, to
	// induce an RPC error when the client starts.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	go rejectingListener(listener)

	client := NewV2(listener.Addr().String(), mock.NewID(), VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials()))
	assert.NoError(t, client.Start(context.Background()))

	// We expect one error each from checkinRoundTrip and actionRoundTrip.
	// After those first RPC errors, the next ones shouldn't be sent for at least a
	// second. We don't want to delay the tests that long for a non-failure
	// case, so just do a short timeout here -- this is really just to make
	// sure we don't fall back on the old behavior of sending rpc failures
	// in a continuous stream.
	rpcErrorCount := 0
	timeoutChan := time.After(50 * time.Millisecond)
testLoop:
	for {
		select {
		case err := <-client.Errors():
			if strings.Contains(err.Error(), "rpc error") {
				rpcErrorCount++
			}
		case <-timeoutChan:
			break testLoop
		}
	}

	assert.Equal(t, 2, rpcErrorCount,
		"expected 2 RPC errors, one from checkinRoundTrip and one from actionRoundTrip")
}

func rejectingListener(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil && strings.Contains(err.Error(), "use of closed network connection") {
			// End the loop when the listener is closed
			return
		}
		conn.Close()
	}
}

func TestClientV2_Checkin_Initial(t *testing.T) {
	var m sync.Mutex
	token := mock.NewID()
	gotInvalid := false
	gotValid := false
	unitOneID := mock.NewID()
	unitTwoID := mock.NewID()
	wantFQDN := true
	reportedVersion := VersionInfo{}
	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				gotValid = true
				reportedVersion.Name = observed.VersionInfo.Name
				reportedVersion.Version = observed.VersionInfo.Version
				reportedVersion.Meta = observed.VersionInfo.Meta
				return &proto.CheckinExpected{
					AgentInfo: &proto.CheckinAgentInfo{
						Id:       "elastic-agent-id",
						Version:  "8.5.0",
						Snapshot: true,
					},
					Features: &proto.Features{
						Fqdn: &proto.FQDNFeature{Enabled: wantFQDN},
					},
					Units: []*proto.UnitExpected{
						{
							Id:             unitOneID,
							Type:           proto.UnitType_OUTPUT,
							ConfigStateIdx: 1,
							Config:         &proto.UnitExpectedConfig{},
							State:          proto.State_HEALTHY,
							LogLevel:       proto.UnitLogLevel_INFO,
						},
						{
							Id:             unitTwoID,
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							Config:         &proto.UnitExpectedConfig{},
							State:          proto.State_HEALTHY,
							LogLevel:       proto.UnitLogLevel_INFO,
						},
					},
				}
			}
			// disconnect
			gotInvalid = true
			return nil
		},
		ActionImpl: func(response *proto.ActionResponse) error {
			// actions not tested here
			return nil
		},
		ActionsChan: make(chan *mock.PerformAction, 100),
	}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	// connect with an invalid token
	var errsMu sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	invalidClient := NewV2(fmt.Sprintf(":%d", srv.Port), mock.NewID(), VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials()))
	storeErrors(ctx, invalidClient, &errs, &errsMu)
	require.NoError(t, invalidClient.Start(ctx))
	defer invalidClient.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !gotInvalid {
			return fmt.Errorf("server never received invalid token")
		}
		return nil
	}))
	invalidClient.Stop()
	cancel()

	// connect with an valid token
	var errs2Mu sync.Mutex
	var errs2 []error
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	validClient := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{
		Name:    "program",
		Version: "v1.0.0",
		Meta: map[string]string{
			"key": "value",
		},
	}, grpc.WithTransportCredentials(insecure.NewCredentials()))
	storeErrors(ctx, validClient, &errs2, &errs2Mu)

	// receive the units
	var unitsMu sync.Mutex
	var units []*Unit
	var gotFQDN bool
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-validClient.UnitChanges():
				if change.Triggers&TriggeredFeatureChange == TriggeredFeatureChange {
					gotFQDN = change.Unit.Expected().Features.Fqdn.Enabled
				}

				switch change.Type {
				case UnitChangedAdded:
					unitsMu.Lock()
					units = append(units, change.Unit)
					unitsMu.Unlock()
				default:
					panic("not implemented")
				}
			}
		}
	}()

	require.NoError(t, validClient.Start(ctx))
	defer validClient.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()
		unitsMu.Lock()
		defer unitsMu.Unlock()

		if !gotValid {
			return fmt.Errorf("server never received valid token")
		}
		if len(units) < 2 {
			return fmt.Errorf("never received 2 units")
		}
		return nil
	}))

	agentInfo := validClient.AgentInfo()
	require.NotNil(t, agentInfo)

	validClient.Stop()
	cancel()

	assert.Equal(t, agentInfo.ID, "elastic-agent-id")
	assert.Equal(t, agentInfo.Version, "8.5.0")
	assert.True(t, agentInfo.Snapshot)

	assert.Equal(t, wantFQDN, gotFQDN)

	assert.Equal(t, units[0].ID(), unitOneID)
	assert.Equal(t, units[0].Type(), UnitTypeOutput)
	assert.Equal(t, units[1].ID(), unitTwoID)
	assert.Equal(t, units[1].Type(), UnitTypeInput)

	assert.Equal(t, reportedVersion.Name, "program")
	assert.Equal(t, reportedVersion.Version, "v1.0.0")
	assert.Equal(t, reportedVersion.Meta, map[string]string{
		"key": "value",
	})
}

func TestClientV2_Checkin_UnitState(t *testing.T) {
	var m sync.Mutex
	token := mock.NewID()
	connected := false
	unitOne := newUnit(mock.NewID(), UnitTypeOutput, UnitStateStarting, UnitLogLevelInfo, nil, 0, nil, nil)
	unitTwo := newUnit(mock.NewID(), UnitTypeInput, UnitStateStarting, UnitLogLevelInfo, nil, 0, nil, nil)
	wantFQDN := true
	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				connected = true
				updateUnits(t, observed, unitOne, unitTwo)
				if unitOne.state == UnitStateStarting && unitTwo.state == UnitStateStarting {
					// first checkin; create units
					return &proto.CheckinExpected{
						Features:    &proto.Features{Fqdn: &proto.FQDNFeature{Enabled: false}},
						FeaturesIdx: 1,
						Units: []*proto.UnitExpected{
							{
								Id:             unitOne.id,
								Type:           proto.UnitType_OUTPUT,
								State:          proto.State_HEALTHY,
								LogLevel:       proto.UnitLogLevel_INFO,
								ConfigStateIdx: 1,
								Config: &proto.UnitExpectedConfig{
									Id: "config_unit_one",
								},
							},
							{
								Id:             unitTwo.id,
								Type:           proto.UnitType_INPUT,
								State:          proto.State_HEALTHY,
								LogLevel:       proto.UnitLogLevel_INFO,
								ConfigStateIdx: 1,
								Config: &proto.UnitExpectedConfig{
									Id: "config_unit_two",
								},
							},
						},
					}
				} else if (unitOne.state == UnitStateHealthy && unitTwo.state == UnitStateHealthy) || (unitOne.state == UnitStateHealthy && unitTwo.state == UnitStateStopping) {
					// stop second input
					return &proto.CheckinExpected{
						Features:    &proto.Features{Fqdn: &proto.FQDNFeature{Enabled: wantFQDN}},
						FeaturesIdx: 1,
						Units: []*proto.UnitExpected{
							{
								Id:             unitOne.id,
								Type:           proto.UnitType_OUTPUT,
								State:          proto.State_HEALTHY,
								LogLevel:       proto.UnitLogLevel_INFO,
								ConfigStateIdx: 1,
								Config:         nil,
							},
							{
								Id:             unitTwo.id,
								Type:           proto.UnitType_INPUT,
								State:          proto.State_STOPPED,
								LogLevel:       proto.UnitLogLevel_INFO,
								ConfigStateIdx: 1,
								Config:         nil,
							},
						},
					}
				} else if unitOne.state == UnitStateHealthy && unitTwo.state == UnitStateStopped {
					// input stopped, remove the unit
					return &proto.CheckinExpected{
						Features:    &proto.Features{Fqdn: &proto.FQDNFeature{Enabled: wantFQDN}},
						FeaturesIdx: 1,
						Units: []*proto.UnitExpected{
							{
								Id:             unitOne.id,
								Type:           proto.UnitType_OUTPUT,
								State:          proto.State_HEALTHY,
								LogLevel:       proto.UnitLogLevel_INFO,
								ConfigStateIdx: 1,
								Config:         nil,
							},
						},
					}
				} else {
					// outside of the state diagram we are testing
					t.Fatal(fmt.Errorf("invalid state; unitOne(%v) - unitTwo(%v)", unitOne.state, unitTwo.state))
				}
			}
			// disconnect
			return nil
		},
		ActionImpl: func(response *proto.ActionResponse) error {
			// actions not tested here
			return nil
		},
		ActionsChan: make(chan *mock.PerformAction, 100),
	}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	var errsMu sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials())).(*clientV2)
	storeErrors(ctx, client, &errs, &errsMu)

	// receive the units
	var unitsMu sync.Mutex
	units := make(map[string]*Unit)
	var gotFQDN bool
	var gotTriggers Trigger
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-client.UnitChanges():
				if change.Triggers&TriggeredFeatureChange == TriggeredFeatureChange {
					gotFQDN = change.Unit.Expected().Features.Fqdn.Enabled
				}

				switch change.Type {
				case UnitChangedAdded:
					unitsMu.Lock()
					units[change.Unit.ID()] = change.Unit
					unitsMu.Unlock()
					change.Unit.UpdateState(UnitStateHealthy, "Healthy", map[string]interface{}{
						"custom": "payload",
					})
				case UnitChangedModified:
					expected := change.Unit.Expected()
					gotFQDN = expected.Features.Fqdn.Enabled
					gotTriggers = change.Triggers

					if expected.State == UnitStateStopped {
						change.Unit.UpdateState(UnitStateStopping, "Stopping", nil)
						go func() {
							time.Sleep(100 * time.Millisecond)
							change.Unit.UpdateState(UnitStateStopped, "Stopped", nil)
						}()
					}
				case UnitChangedRemoved:
					unitsMu.Lock()
					delete(units, change.Unit.ID())
					unitsMu.Unlock()
				}

			}
		}
	}()

	require.NoError(t, client.Start(ctx))
	defer client.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !connected {
			return fmt.Errorf("server never received valid token")
		}
		return nil
	}))
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if unitTwo.state != UnitStateStopped {
			return fmt.Errorf("never unitTwo into stopped state")
		}
		return nil
	}))
	require.NoError(t, waitFor(func() error {
		unitsMu.Lock()
		defer unitsMu.Unlock()

		if len(units) != 1 {
			return fmt.Errorf("never got the removal of unitTwo")
		}
		return nil
	}))

	assert.Equal(t, wantFQDN, gotFQDN)
	assert.Equal(t, gotTriggers&TriggeredFeatureChange, TriggeredFeatureChange)
	assert.Equal(t, UnitStateHealthy, unitOne.state)
	assert.Equal(t, "Healthy", unitOne.stateMsg)
	assert.Equal(t, UnitStateStopped, unitTwo.state)
	assert.Equal(t, "Stopped", unitTwo.stateMsg)
}

func TestClientV2_Actions(t *testing.T) {
	var m sync.Mutex
	token := mock.NewID()
	gotInit := false
	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			if observed.Token == token {
				return &proto.CheckinExpected{
					Units: []*proto.UnitExpected{
						{
							Id:             mock.NewID(),
							Type:           proto.UnitType_OUTPUT,
							State:          proto.State_HEALTHY,
							LogLevel:       proto.UnitLogLevel_INFO,
							ConfigStateIdx: 1,
							Config:         &proto.UnitExpectedConfig{},
						},
					},
				}
			}
			// disconnect
			return nil
		},
		ActionImpl: func(response *proto.ActionResponse) error {
			m.Lock()
			defer m.Unlock()

			if response.Token != token {
				return fmt.Errorf("invalid token")
			}
			if response.Id == "init" {
				gotInit = true
			}
			return nil
		},
		ActionsChan: make(chan *mock.PerformAction, 100),
		SentActions: make(map[string]*mock.PerformAction),
	}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	var errsMu sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials()))
	storeErrors(ctx, client, &errs, &errsMu)

	var unitsMu sync.Mutex
	var units []*Unit
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-client.UnitChanges():
				switch change.Type {
				case UnitChangedAdded:
					unitsMu.Lock()
					units = append(units, change.Unit)
					unitsMu.Unlock()
				default:
					panic("not implemented")
				}
			}
		}
	}()

	require.NoError(t, client.Start(ctx))
	defer client.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !gotInit {
			return fmt.Errorf("server never received valid token")
		}
		return nil
	}))
	require.NoError(t, waitFor(func() error {
		unitsMu.Lock()
		defer unitsMu.Unlock()

		if len(units) != 1 {
			return fmt.Errorf("client never got unit")
		}
		return nil
	}))

	unitsMu.Lock()
	unit := units[0]
	unitsMu.Unlock()
	unit.RegisterAction(&AddAction{})

	// send an none registered action
	_, err := srv.PerformAction(unit.id, proto.UnitType(unit.unitType), "invalid", nil)
	assert.Error(t, err)

	// send an action to invalid unit
	_, err = srv.PerformAction("invalid", proto.UnitType(unit.unitType), "add", map[string]interface{}{
		"numbers": []int{10, 20, 30},
	})
	assert.Error(t, err)

	// send successful add action
	res, err := srv.PerformAction(unit.id, proto.UnitType(unit.unitType), "add", map[string]interface{}{
		"numbers": []int{10, 20, 30},
	})
	require.NoError(t, err)
	total, _ := res["total"]
	assert.Equal(t, float64(60), total.(float64))

	// send bad params
	_, err = srv.PerformAction(unit.id, proto.UnitType(unit.unitType), "add", map[string]interface{}{
		"numbers": []interface{}{"bad", 20, 30},
	})
	assert.Error(t, err)
}

func TestClientV2_DiagnosticAction(t *testing.T) {
	var m sync.Mutex
	token := mock.NewID()
	gotInit := false
	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			if observed.Token == token {
				return &proto.CheckinExpected{
					Units: []*proto.UnitExpected{
						{
							Id:             mock.NewID(),
							Type:           proto.UnitType_OUTPUT,
							State:          proto.State_HEALTHY,
							LogLevel:       proto.UnitLogLevel_INFO,
							ConfigStateIdx: 1,
							Config:         &proto.UnitExpectedConfig{},
						},
					},
				}
			}
			// disconnect
			return nil
		},
		ActionImpl: func(response *proto.ActionResponse) error {
			m.Lock()
			defer m.Unlock()

			if response.Token != token {
				return fmt.Errorf("invalid token")
			}
			if response.Id == "init" {
				gotInit = true
			}
			return nil
		},
		ActionsChan: make(chan *mock.PerformAction, 100),
		SentActions: make(map[string]*mock.PerformAction),
	}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	var errsMu sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials()))
	storeErrors(ctx, client, &errs, &errsMu)

	var unitsMu sync.Mutex
	var units []*Unit
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-client.UnitChanges():
				switch change.Type {
				case UnitChangedAdded:
					unitsMu.Lock()
					units = append(units, change.Unit)
					unitsMu.Unlock()
				default:
					panic("not implemented")
				}
			}
		}
	}()

	require.NoError(t, client.Start(ctx))
	defer client.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !gotInit {
			return fmt.Errorf("server never received valid token")
		}
		return nil
	}))
	require.NoError(t, waitFor(func() error {
		unitsMu.Lock()
		defer unitsMu.Unlock()

		if len(units) != 1 {
			return fmt.Errorf("client never got unit")
		}
		return nil
	}))

	unitsMu.Lock()
	unit := units[0]
	unitsMu.Unlock()

	client.RegisterDiagnosticHook("custom_component", "customer diagnostic for the component", "custom_component.txt", "plain/text", func() []byte {
		return []byte("custom component")
	})
	unit.RegisterDiagnosticHook("custom_unit", "custom diagnostic for the unit", "custom_unit.txt", "plain/text", func() []byte {
		return []byte("custom unit")
	})

	res, err := srv.PerformDiagnostic(unit.id, proto.UnitType(unit.unitType))
	assert.NoError(t, err)

	expectedNames := []string{"goroutine", "heap", "allocs", "threadcreate", "block", "mutex", "custom_component", "custom_unit"}
	assert.Equal(t, len(expectedNames), len(res))
	for _, d := range res {
		assert.Contains(t, expectedNames, d.Name)
		switch d.Name {
		case "custom_unit":
			assert.Equal(t, []byte("custom unit"), d.Content)
		case "custom_component":
			assert.Equal(t, []byte("custom component"), d.Content)
		default:
			gz, err := gzip.NewReader(bytes.NewBuffer(d.Content))
			assert.NoError(t, err)
			uncompressed, err := io.ReadAll(gz)
			assert.NoError(t, err)
			_, err = profile.ParseUncompressed(uncompressed)
			assert.NoError(t, err)
		}
	}
}

func TestTrigger_String(t *testing.T) {
	tcs := []struct {
		Name    string
		Trigger Trigger
		Want    string
	}{
		{
			Name: "nothing triggered, zero value",
			Want: "nothing_triggered",
		},
		{
			Name:    "one change: feature_change_triggered",
			Trigger: TriggeredFeatureChange,
			Want:    "feature_change_triggered",
		},
		{
			Name:    "two changes: feature_change_triggered, state_change_triggered",
			Trigger: TriggeredFeatureChange | TriggeredStateChange,
			Want:    "feature_change_triggered, state_change_triggered",
		},
		{
			Name:    "invalid trigger value",
			Trigger: 1618,
			Want:    "invalid trigger value: 1618",
		},
	}

	for _, tc := range tcs {
		t.Run(tc.Name, func(t *testing.T) {
			got := tc.Trigger.String()
			assert.Equal(t, got, tc.Want)
		})
	}
}

func TestClientV2_Checkin_FeatureFlags(t *testing.T) {
	var m sync.Mutex
	token := mock.NewID()
	unit := newUnit(mock.NewID(), UnitTypeInput, UnitStateStarting, UnitLogLevelInfo, nil, 0, nil, nil)
	connected := false
	checkinCounter := 0
	featuresIdx := rand.Uint64()

	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				connected = true

				switch checkinCounter {
				case 0:
					// first checkin
					require.Equal(t, uint64(0), observed.FeaturesIdx)

					checkinCounter++
					return &proto.CheckinExpected{
						FeaturesIdx: featuresIdx,
						Units: []*proto.UnitExpected{
							{
								Id:             unit.id,
								Type:           proto.UnitType_INPUT,
								State:          proto.State_HEALTHY,
								LogLevel:       proto.UnitLogLevel_INFO,
								ConfigStateIdx: 1,
								Config: &proto.UnitExpectedConfig{
									Id: "config_unit",
								},
							},
						},
					}

				case 1:
					// second checkin
					require.Equal(t, featuresIdx, observed.FeaturesIdx)
					checkinCounter++
				}
			}
			// disconnect
			return nil
		},
		ActionImpl: func(response *proto.ActionResponse) error {
			// actions not tested here
			return nil
		},
		ActionsChan: make(chan *mock.PerformAction, 100),
	}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	var errsMu sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials())).(*clientV2)
	storeErrors(ctx, client, &errs, &errsMu)

	// receive the units
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-client.UnitChanges():
				switch change.Type {
				case UnitChangedAdded:
					err := change.Unit.UpdateState(UnitStateHealthy, "Healthy", map[string]interface{}{
						"custom": "payload",
					})
					require.NoError(t, err)
				}
			}
		}
	}()

	require.NoError(t, client.Start(ctx))
	defer client.Stop()

	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !connected {
			return fmt.Errorf("server never received valid token")
		}

		expectedCheckinCounter := 2
		if checkinCounter < expectedCheckinCounter {
			return fmt.Errorf(
				"server did not receive expected number of checkins = %d; actual checkins received = %d",
				expectedCheckinCounter,
				checkinCounter,
			)
		}

		return nil
	}))

	require.Empty(t, errs)
}

func storeErrors(ctx context.Context, client V2, errs *[]error, lock *sync.Mutex) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-client.Errors():
				lock.Lock()
				*errs = append(*errs, err)
				lock.Unlock()
			}
		}
	}()
}

func updateUnits(t *testing.T, observed *proto.CheckinObserved, units ...*Unit) {
	t.Helper()
	for _, unit := range units {
		for _, observedUnit := range observed.Units {
			if unit.id == observedUnit.Id && unit.unitType == UnitType(observedUnit.Type) {
				unit.state = UnitState(observedUnit.State)
				unit.stateMsg = observedUnit.Message
				unit.statePayload = observedUnit.Payload
			}
		}
	}
}
