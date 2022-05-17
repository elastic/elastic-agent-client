// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestClientV2_DialError(t *testing.T) {
	srv := StubServerV2{}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	invalidClient := NewV2(fmt.Sprintf(":%d", srv.Port), "invalid_token", VersionInfo{})
	assert.Error(t, invalidClient.Start(context.Background()))
	defer invalidClient.Stop()
}

func TestClientV2_Checkin_Initial(t *testing.T) {
	var m sync.Mutex
	token := newID()
	gotInvalid := false
	gotValid := false
	unitOneId := newID()
	unitTwoId := newID()
	reportedVersion := VersionInfo{}
	srv := StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				gotValid = true
				reportedVersion.Name = observed.VersionInfo.Name
				reportedVersion.Version = observed.VersionInfo.Version
				reportedVersion.Meta = observed.VersionInfo.Meta
				return &proto.CheckinExpected{
					Units: []*proto.UnitExpected{
						{
							Id:             unitOneId,
							Type:           proto.UnitType_OUTPUT,
							ConfigStateIdx: 1,
							Config:         "config",
							State:          proto.State_HEALTHY,
						},
						{
							Id:             unitTwoId,
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							Config:         "config",
							State:          proto.State_HEALTHY,
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
		actions: make(chan *performAction, 100),
	}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	// connect with an invalid token
	var errsLock sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	invalidClient := NewV2(fmt.Sprintf(":%d", srv.Port), newID(), VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials()))
	storeErrors(ctx, invalidClient, &errs, &errsLock)
	require.NoError(t, invalidClient.Start(ctx))
	defer invalidClient.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !gotInvalid {
			return fmt.Errorf("server never recieved invalid token")
		}
		return nil
	}))
	invalidClient.Stop()
	cancel()

	// connect with an valid token
	var errs2Lock sync.Mutex
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
	storeErrors(ctx, validClient, &errs2, &errs2Lock)

	// receive the units
	var unitsLock sync.Mutex
	var units []*Unit
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-validClient.UnitChanges():
				switch change.Type {
				case UnitChangedAdded:
					unitsLock.Lock()
					units = append(units, change.Unit)
					unitsLock.Unlock()
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
		unitsLock.Lock()
		defer unitsLock.Unlock()

		if !gotValid {
			return fmt.Errorf("server never recieved valid token")
		}
		if len(units) < 2 {
			return fmt.Errorf("never recieved 2 units")
		}
		return nil
	}))
	validClient.Stop()
	cancel()

	assert.Equal(t, units[0].ID(), unitOneId)
	assert.Equal(t, units[0].Type(), UnitTypeOutput)
	assert.Equal(t, units[1].ID(), unitTwoId)
	assert.Equal(t, units[1].Type(), UnitTypeInput)
	assert.Equal(t, reportedVersion.Name, "program")
	assert.Equal(t, reportedVersion.Version, "v1.0.0")
	assert.Equal(t, reportedVersion.Meta, map[string]string{
		"key": "value",
	})
}

func TestClientV2_Checkin_UnitState(t *testing.T) {
	var m sync.Mutex
	token := newID()
	connected := false
	unitOne := newUnit(newID(), UnitTypeOutput, UnitStateStarting, "", 0, nil)
	unitTwo := newUnit(newID(), UnitTypeInput, UnitStateStarting, "", 0, nil)
	srv := StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				connected = true
				updateUnits(observed, unitOne, unitTwo)
				if unitOne.state == UnitStateStarting && unitTwo.state == UnitStateStarting {
					// first checkin; create units
					return &proto.CheckinExpected{
						Units: []*proto.UnitExpected{
							{
								Id:             unitOne.id,
								Type:           proto.UnitType_OUTPUT,
								State:          proto.State_HEALTHY,
								ConfigStateIdx: 1,
								Config:         "config_unit_one",
							},
							{
								Id:             unitTwo.id,
								Type:           proto.UnitType_INPUT,
								State:          proto.State_HEALTHY,
								ConfigStateIdx: 1,
								Config:         "config_unit_two",
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
								ConfigStateIdx: 1,
								Config:         "",
							},
							{
								Id:             unitTwo.id,
								Type:           proto.UnitType_INPUT,
								State:          proto.State_STOPPED,
								ConfigStateIdx: 1,
								Config:         "",
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
								ConfigStateIdx: 1,
								Config:         "",
							},
						},
					}
				} else {
					// outside of the state diagram we are testing
					panic(fmt.Errorf("invalid state; unitOne(%v) - unitTwo(%v)", unitOne.state, unitTwo.state))
				}
			}
			// disconnect
			return nil
		},
		ActionImpl: func(response *proto.ActionResponse) error {
			// actions not tested here
			return nil
		},
		actions: make(chan *performAction, 100),
	}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	var errsLock sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials())).(*clientV2)
	storeErrors(ctx, client, &errs, &errsLock)

	// receive the units
	var unitsLock sync.Mutex
	units := make(map[string]*Unit)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-client.UnitChanges():
				switch change.Type {
				case UnitChangedAdded:
					unitsLock.Lock()
					units[change.Unit.ID()] = change.Unit
					unitsLock.Unlock()
					change.Unit.UpdateState(UnitStateHealthy, "Healthy", map[string]interface{}{
						"custom": "payload",
					})
				case UnitChangedModified:
					state, _ := change.Unit.Expected()
					if state == UnitStateStopped {
						change.Unit.UpdateState(UnitStateStopping, "Stopping", nil)
						go func() {
							time.Sleep(100 * time.Millisecond)
							change.Unit.UpdateState(UnitStateStopped, "Stopped", nil)
						}()
					}
				case UnitChangedRemoved:
					unitsLock.Lock()
					delete(units, change.Unit.ID())
					unitsLock.Unlock()
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
			return fmt.Errorf("server never recieved valid token")
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

	unitsLock.Lock()
	defer unitsLock.Unlock()
	assert.Len(t, units, 1)
	assert.Equal(t, UnitStateHealthy, unitOne.state)
	assert.Equal(t, "Healthy", unitOne.stateMsg)
	assert.Equal(t, UnitStateStopped, unitTwo.state)
	assert.Equal(t, "Stopped", unitTwo.stateMsg)
}

func TestClientV2_Actions(t *testing.T) {
	var m sync.Mutex
	token := newID()
	gotInit := false
	srv := StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			if observed.Token == token {
				return &proto.CheckinExpected{
					Units: []*proto.UnitExpected{
						{
							Id:             newID(),
							Type:           proto.UnitType_OUTPUT,
							State:          proto.State_HEALTHY,
							ConfigStateIdx: 1,
							Config:         "config",
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
		actions:     make(chan *performAction, 100),
		sentActions: make(map[string]*performAction),
	}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	var errsLock sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials()))
	storeErrors(ctx, client, &errs, &errsLock)

	var unitsLock sync.Mutex
	var units []*Unit
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-client.UnitChanges():
				switch change.Type {
				case UnitChangedAdded:
					unitsLock.Lock()
					units = append(units, change.Unit)
					unitsLock.Unlock()
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
			return fmt.Errorf("server never recieved valid token")
		}
		return nil
	}))
	require.NoError(t, waitFor(func() error {
		unitsLock.Lock()
		defer unitsLock.Unlock()

		if len(units) != 1 {
			return fmt.Errorf("client never got unit")
		}
		return nil
	}))

	unitsLock.Lock()
	unit := units[0]
	unitsLock.Unlock()
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

type StubServerCheckinV2 func(observed *proto.CheckinObserved) *proto.CheckinExpected
type StubServerArtifactFetch func(request *proto.ArtifactFetchRequest, server proto.ElasticAgentArtifact_FetchServer) error
type StubServerLog func(fetch *proto.LogMessageRequest) (*proto.LogMessageResponse, error)

type StubServerStore interface {
	BeginTxn(request *proto.StoreBeginTxnRequest) (*proto.StoreBeginTxnResponse, error)
	GetKey(request *proto.StoreGetKeyRequest) (*proto.StoreGetKeyResponse, error)
	SetKey(request *proto.StoreSetKeyRequest) (*proto.StoreSetKeyResponse, error)
	DeleteKey(request *proto.StoreDeleteKeyRequest) (*proto.StoreDeleteKeyResponse, error)
	CommitTxn(request *proto.StoreCommitTxnRequest) (*proto.StoreCommitTxnResponse, error)
	DiscardTxn(request *proto.StoreDiscardTxnRequest) (*proto.StoreDiscardTxnResponse, error)
}

type StubServerV2 struct {
	proto.UnimplementedElasticAgentServer
	proto.UnimplementedElasticAgentStoreServer
	proto.UnimplementedElasticAgentArtifactServer
	proto.UnimplementedElasticAgentLogServer

	Port              int
	CheckinImpl       StubServerCheckin
	ActionImpl        StubServerAction
	CheckinV2Impl     StubServerCheckinV2
	StoreImpl         StubServerStore
	ArtifactFetchImpl StubServerArtifactFetch
	LogImpl           StubServerLog

	server      *grpc.Server
	actions     chan *performAction
	sentActions map[string]*performAction
}

func (s *StubServerV2) Start(opt ...grpc.ServerOption) error {
	lis, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	s.Port = lis.Addr().(*net.TCPAddr).Port
	srv := grpc.NewServer(opt...)
	s.server = srv
	proto.RegisterElasticAgentServer(s.server, s)
	proto.RegisterElasticAgentStoreServer(s.server, s)
	proto.RegisterElasticAgentArtifactServer(s.server, s)
	proto.RegisterElasticAgentLogServer(s.server, s)
	go func() {
		srv.Serve(lis)
	}()
	return nil
}

func (s *StubServerV2) Stop() {
	if s.server != nil {
		s.server.Stop()
		s.server = nil
	}
}

func (s *StubServerV2) Checkin(server proto.ElasticAgent_CheckinServer) error {
	for {
		checkin, err := server.Recv()
		if err != nil {
			return err
		}
		resp := s.CheckinImpl(checkin)
		if resp == nil {
			// close connection to client
			return nil
		}
		err = server.Send(resp)
		if err != nil {
			return err
		}
	}
}

func (s *StubServerV2) CheckinV2(server proto.ElasticAgent_CheckinV2Server) error {
	for {
		checkin, err := server.Recv()
		if err != nil {
			return err
		}
		resp := s.CheckinV2Impl(checkin)
		if resp == nil {
			// close connection to client
			return nil
		}
		err = server.Send(resp)
		if err != nil {
			return err
		}
	}
}

func (s *StubServerV2) Actions(server proto.ElasticAgent_ActionsServer) error {
	var m sync.Mutex
	done := make(chan bool)
	go func() {
		for {
			select {
			case <-done:
				return
			case act := <-s.actions:
				id := uuid.Must(uuid.NewV4())
				m.Lock()
				s.sentActions[id.String()] = act
				m.Unlock()
				err := server.Send(&proto.ActionRequest{
					Id:       id.String(),
					Name:     act.Name,
					Params:   act.Params,
					UnitId:   act.UnitID,
					UnitType: act.UnitType,
				})
				if err != nil {
					panic(err)
				}
			}
		}
	}()
	defer close(done)

	for {
		response, err := server.Recv()
		if err != nil {
			return err
		}
		err = s.ActionImpl(response)
		if err != nil {
			// close connection to client
			return nil
		}
		m.Lock()
		action, ok := s.sentActions[response.Id]
		if !ok {
			// nothing to do, unknown action
			m.Unlock()
			continue
		}
		delete(s.sentActions, response.Id)
		m.Unlock()
		var result map[string]interface{}
		err = json.Unmarshal(response.Result, &result)
		if err != nil {
			return err
		}
		if response.Status == proto.ActionResponse_FAILED {
			error, ok := result["error"]
			if ok {
				err = fmt.Errorf("%s", error)
			} else {
				err = fmt.Errorf("unknown error")
			}
			action.Callback(nil, err)
		} else {
			action.Callback(result, nil)
		}
	}
}

func (s *StubServerV2) PerformAction(unitId string, unitType proto.UnitType, name string, params map[string]interface{}) (map[string]interface{}, error) {
	paramBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	resChan := make(chan actionResultChan)
	s.actions <- &performAction{
		UnitID:   unitId,
		UnitType: unitType,
		Name:     name,
		Params:   paramBytes,
		Callback: func(m map[string]interface{}, err error) {
			resChan <- actionResultChan{
				Result: m,
				Err:    err,
			}
		},
	}
	res := <-resChan
	return res.Result, res.Err
}

func (s *StubServerV2) BeginTxn(_ context.Context, request *proto.StoreBeginTxnRequest) (*proto.StoreBeginTxnResponse, error) {
	return s.StoreImpl.BeginTxn(request)
}

func (s *StubServerV2) GetKey(_ context.Context, request *proto.StoreGetKeyRequest) (*proto.StoreGetKeyResponse, error) {
	return s.StoreImpl.GetKey(request)
}

func (s *StubServerV2) SetKey(_ context.Context, request *proto.StoreSetKeyRequest) (*proto.StoreSetKeyResponse, error) {
	return s.StoreImpl.SetKey(request)
}

func (s *StubServerV2) DeleteKey(_ context.Context, request *proto.StoreDeleteKeyRequest) (*proto.StoreDeleteKeyResponse, error) {
	return s.StoreImpl.DeleteKey(request)
}

func (s *StubServerV2) CommitTxn(_ context.Context, request *proto.StoreCommitTxnRequest) (*proto.StoreCommitTxnResponse, error) {
	return s.StoreImpl.CommitTxn(request)
}

func (s *StubServerV2) DiscardTxn(_ context.Context, request *proto.StoreDiscardTxnRequest) (*proto.StoreDiscardTxnResponse, error) {
	return s.StoreImpl.DiscardTxn(request)
}

func (s *StubServerV2) Fetch(request *proto.ArtifactFetchRequest, server proto.ElasticAgentArtifact_FetchServer) error {
	return s.ArtifactFetchImpl(request, server)
}

func (s *StubServerV2) Log(_ context.Context, request *proto.LogMessageRequest) (*proto.LogMessageResponse, error) {
	return s.LogImpl(request)
}

func newID() string {
	return uuid.Must(uuid.NewV4()).String()
}

func storeErrors(ctx context.Context, client ClientV2, errs *[]error, lock *sync.Mutex) {
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

func updateUnits(observed *proto.CheckinObserved, units ...*Unit) {
	for _, unit := range units {
		for _, observedUnit := range observed.Units {
			if unit.id == observedUnit.Id && unit.unitType == UnitType(observedUnit.Type) {
				unit.state = UnitState(observedUnit.State)
				unit.stateMsg = observedUnit.Message
				unit.statePayloadEncoded = observedUnit.Payload
				if unit.statePayloadEncoded == nil {
					unit.statePayload = nil
				} else {
					if err := json.Unmarshal(unit.statePayloadEncoded, &unit.statePayload); err != nil {
						panic(err)
					}
				}
			}
		}
	}
}
