// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

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

func TestClientV2_Checkin_Initial(t *testing.T) {
	var m sync.Mutex
	token := mock.NewID()
	gotInvalid := false
	gotValid := false
	unitOneID := mock.NewID()
	unitTwoID := mock.NewID()
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
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-validClient.UnitChanges():
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
	unitOne := newUnit(mock.NewID(), UnitTypeOutput, UnitStateStarting, UnitLogLevelInfo, nil, 0, nil)
	unitTwo := newUnit(mock.NewID(), UnitTypeInput, UnitStateStarting, UnitLogLevelInfo, nil, 0, nil)
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
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-client.UnitChanges():
				switch change.Type {
				case UnitChangedAdded:
					unitsMu.Lock()
					units[change.Unit.ID()] = change.Unit
					unitsMu.Unlock()
					change.Unit.UpdateState(UnitStateHealthy, "Healthy", map[string]interface{}{
						"custom": "payload",
					})
				case UnitChangedModified:
					state, _, _ := change.Unit.Expected()
					if state == UnitStateStopped {
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

	res, err := srv.PerformDiagnostic(unit.id, proto.UnitType(unit.unitType))
	assert.NoError(t, err)

	names := make([]string, 0, len(res))
	for _, d := range res {
		names = append(names, d.Name)
	}
	assert.ElementsMatch(t, names, []string{"goroutine", "heap", "allocs", "threadcreate", "block", "mutex"})
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
