// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package mock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/gofrs/uuid"
	"google.golang.org/grpc"

	"github.com/elastic/elastic-agent-client/v7/pkg/client/chunk"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
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

	// LocalRPC is unix socket or windows named pipe name
	//
	// Given the LocalRPC value "elastic_agent" the getRPCPath creates the platform specific path
	// Typically something like \\.\pipe\elastic_agent for windows
	// and /var/run/123456345/elastic_agent.sock for unix
	LocalRPC string

	// Port for TCP RPC only
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

	// target for gRPC
	target string
}

// listen over tcp or local unix socket or named windows pipe
func (s *StubServerV2) listen(opt ...grpc.ServerOption) (lis net.Listener, cleanup func() error, err error) {
	cleanup = func() error { return nil }
	if s.LocalRPC == "" {
		lis, err := net.Listen("tcp", fmt.Sprintf(":%d", s.Port))
		if err != nil {
			return nil, nil, err
		}
		s.Port = lis.Addr().(*net.TCPAddr).Port
		s.target = lis.Addr().String()
		return lis, cleanup, nil
	}

	if runtime.GOOS == "windows" {
		s.target = fmt.Sprintf("\\\\.\\pipe\\%s", s.LocalRPC)
		lis, err = newNPipeListener(s.target, "")
	} else {
		socketDir := filepath.Join(os.TempDir(), randomString(3))
		err = os.MkdirAll(socketDir, 0750)
		if err != nil {
			return nil, nil, err
		}

		cleanup = func() error {
			return os.RemoveAll(socketDir)
		}
		// Cleanup in case if transport.Listen fails
		defer func() {
			if err != nil {
				_ = cleanup()
				cleanup = nil
			}
		}()
		rpcPath := fmt.Sprintf("%s/%s.sock", socketDir, s.LocalRPC)
		s.target = fmt.Sprintf("unix://%s", rpcPath)
		lis, err = net.Listen("unix", rpcPath)
	}

	return lis, cleanup, err
}

func randomString(length int) string {
	r := make([]byte, length)
	_, err := rand.Read(r)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(r)
}

// GetTarget returns full target for gRPC with prefix for local transport, depending on the current platform and the s.LocalRPC value
func (s *StubServerV2) GetTarget() string {
	return s.target
}

// Start the mock server
func (s *StubServerV2) Start(opt ...grpc.ServerOption) error {
	lis, cleanup, err := s.listen()
	if err != nil {
		return err
	}
	srv := grpc.NewServer(opt...)
	s.server = srv
	proto.RegisterElasticAgentServer(s.server, s)
	proto.RegisterElasticAgentStoreServer(s.server, s)
	proto.RegisterElasticAgentArtifactServer(s.server, s)
	proto.RegisterElasticAgentLogServer(s.server, s)
	go func() {
		srv.Serve(lis)
		defer cleanup()
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
		checkin, err := chunk.RecvObserved(server)
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
					Type:     act.Type,
					Id:       id.String(),
					Name:     act.Name,
					Params:   act.Params,
					UnitId:   act.UnitID,
					UnitType: act.UnitType,
					Level:    act.Level,
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
		if response.Result != nil {
			err = json.Unmarshal(response.Result, &result)
			if err != nil {
				return err
			}
		}
		if action.Type == proto.ActionRequest_CUSTOM {
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
		} else if action.Type == proto.ActionRequest_DIAGNOSTICS {
			if response.Status == proto.ActionResponse_FAILED {
				error, ok := result["error"]
				if ok {
					err = fmt.Errorf("%s", error)
				} else {
					err = fmt.Errorf("unknown error")
				}
				action.DiagCallback(nil, err)
			} else {
				action.DiagCallback(response.Diagnostic, nil)
			}
		} else {
			panic("unknown action type")
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

// PerformDiagnostic is the implementation for the V2 mock server
func (s *StubServerV2) PerformDiagnostic(unitID string, unitType proto.UnitType, level proto.ActionRequest_Level, params []byte) ([]*proto.ActionDiagnosticUnitResult, error) {
	resCh := make(chan actionResultCh)
	s.ActionsChan <- &PerformAction{
		Type:     proto.ActionRequest_DIAGNOSTICS,
		UnitID:   unitID,
		UnitType: unitType,
		Level:    level,
		Params:   params,
		DiagCallback: func(diag []*proto.ActionDiagnosticUnitResult, err error) {
			resCh <- actionResultCh{
				Diag: diag,
				Err:  err,
			}
		},
	}
	res := <-resCh
	return res.Diag, res.Err
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
