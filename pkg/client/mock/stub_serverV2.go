// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/gofrs/uuid"
	"google.golang.org/grpc"
)

// StubServerCheckinV2 is the checkin function for the V2 controller
type StubServerCheckinV2 func(observed *proto.CheckinObserved) *proto.CheckinExpected

// StubServerArtifactFetch is the artifact fetch function for the V2 controller
type StubServerArtifactFetch func(request *proto.ArtifactFetchRequest, server proto.ElasticAgentArtifact_FetchServer) error

// StubServerLog is the logging function for the V2 controller
type StubServerLog func(fetch *proto.LogMessageRequest) (*proto.LogMessageResponse, error)

// StubServerStore is the interface that mocks the server artifact store
type StubServerStore interface {
	BeginTx(request *proto.StoreBeginTxRequest) (*proto.StoreBeginTxResponse, error)
	GetKey(request *proto.StoreGetKeyRequest) (*proto.StoreGetKeyResponse, error)
	SetKey(request *proto.StoreSetKeyRequest) (*proto.StoreSetKeyResponse, error)
	DeleteKey(request *proto.StoreDeleteKeyRequest) (*proto.StoreDeleteKeyResponse, error)
	CommitTx(request *proto.StoreCommitTxRequest) (*proto.StoreCommitTxResponse, error)
	DiscardTx(request *proto.StoreDiscardTxRequest) (*proto.StoreDiscardTxResponse, error)
}

// StubServerV2 is the mocked server implementation for the V2 controller
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
	ActionsChan chan *PerformAction
	SentActions map[string]*PerformAction
}

// Start the mock server
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

// Stop the mock server
func (s *StubServerV2) Stop() {
	if s.server != nil {
		s.server.Stop()
		s.server = nil
	}
}

// Checkin is the checkin implementation for the mock server
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

// CheckinV2 is the V2 checkin implementation for the mock server
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

// Actions is the action implementation for the mock V2 server
func (s *StubServerV2) Actions(server proto.ElasticAgent_ActionsServer) error {
	var m sync.Mutex
	done := make(chan bool)
	go func() {
		for {
			select {
			case <-done:
				return
			case act := <-s.ActionsChan:
				id := uuid.Must(uuid.NewV4())
				m.Lock()
				s.SentActions[id.String()] = act
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
		action, ok := s.SentActions[response.Id]
		if !ok {
			// nothing to do, unknown action
			m.Unlock()
			continue
		}
		delete(s.SentActions, response.Id)
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

// PerformAction is the implementation for the V2 mock server
func (s *StubServerV2) PerformAction(unitID string, unitType proto.UnitType, name string, params map[string]interface{}) (map[string]interface{}, error) {
	paramBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	resCh := make(chan actionResultCh)
	s.ActionsChan <- &PerformAction{
		UnitID:   unitID,
		UnitType: unitType,
		Name:     name,
		Params:   paramBytes,
		Callback: func(m map[string]interface{}, err error) {
			resCh <- actionResultCh{
				Result: m,
				Err:    err,
			}
		},
	}
	res := <-resCh
	return res.Result, res.Err
}

// BeginTx implmenentation for the V2 stub server
func (s *StubServerV2) BeginTx(_ context.Context, request *proto.StoreBeginTxRequest) (*proto.StoreBeginTxResponse, error) {
	return s.StoreImpl.BeginTx(request)
}

// GetKey implmenentation for the V2 stub server
func (s *StubServerV2) GetKey(_ context.Context, request *proto.StoreGetKeyRequest) (*proto.StoreGetKeyResponse, error) {
	return s.StoreImpl.GetKey(request)
}

// SetKey implmenentation for the V2 stub server
func (s *StubServerV2) SetKey(_ context.Context, request *proto.StoreSetKeyRequest) (*proto.StoreSetKeyResponse, error) {
	return s.StoreImpl.SetKey(request)
}

// DeleteKey implmenentation for the V2 stub server
func (s *StubServerV2) DeleteKey(_ context.Context, request *proto.StoreDeleteKeyRequest) (*proto.StoreDeleteKeyResponse, error) {
	return s.StoreImpl.DeleteKey(request)
}

// CommitTx implmenentation for the V2 stub server
func (s *StubServerV2) CommitTx(_ context.Context, request *proto.StoreCommitTxRequest) (*proto.StoreCommitTxResponse, error) {
	return s.StoreImpl.CommitTx(request)
}

// DiscardTx implmenentation for the V2 stub server
func (s *StubServerV2) DiscardTx(_ context.Context, request *proto.StoreDiscardTxRequest) (*proto.StoreDiscardTxResponse, error) {
	return s.StoreImpl.DiscardTx(request)
}

// Fetch implmenentation for the V2 stub server
func (s *StubServerV2) Fetch(request *proto.ArtifactFetchRequest, server proto.ElasticAgentArtifact_FetchServer) error {
	return s.ArtifactFetchImpl(request, server)
}

// Log implmenentation for the V2 stub server
func (s *StubServerV2) Log(_ context.Context, request *proto.LogMessageRequest) (*proto.LogMessageResponse, error) {
	return s.LogImpl(request)
}
