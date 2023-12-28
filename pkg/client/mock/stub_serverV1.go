// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package mock

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/gofrs/uuid"
	"google.golang.org/grpc"
)

// StubServerCheckin is used by the StubServer
type StubServerCheckin func(*proto.StateObserved) *proto.StateExpected

// StubServerAction is used by the StubServer
type StubServerAction func(*proto.ActionResponse) error

// PerformAction is the stubbed action type for the mocked server
type PerformAction struct {
	Type         proto.ActionRequest_Type
	Name         string
	Params       []byte
	Callback     func(map[string]interface{}, error)
	DiagCallback func([]*proto.ActionDiagnosticUnitResult, error)
	UnitID       string
	UnitType     proto.UnitType
	Level        proto.ActionRequest_Level
}

type actionResultCh struct {
	Result map[string]interface{}
	Diag   []*proto.ActionDiagnosticUnitResult
	Err    error
}

// StubServer is the type that mocks an elastic agent controller server
type StubServer struct {
	proto.UnimplementedElasticAgentServer

	Port        int
	CheckinImpl StubServerCheckin
	ActionImpl  StubServerAction

	server      *grpc.Server
	ActionsChan chan *PerformAction
	SentActions map[string]*PerformAction
}

// Start the sub V2 server
func (s *StubServer) Start(opt ...grpc.ServerOption) error {
	lis, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}
	s.Port = lis.Addr().(*net.TCPAddr).Port
	srv := grpc.NewServer(opt...)
	s.server = srv
	proto.RegisterElasticAgentServer(s.server, s)
	go func() {
		srv.Serve(lis)
	}()
	return nil
}

// Stop the stub V1 server
func (s *StubServer) Stop() {
	if s.server != nil {
		s.server.Stop()
		s.server = nil
	}
}

// Checkin implementaiton for the stub server
func (s *StubServer) Checkin(server proto.ElasticAgent_CheckinServer) error {
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

// Actions implementation for the stub server
func (s *StubServer) Actions(server proto.ElasticAgent_ActionsServer) error {
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
					Id:     id.String(),
					Name:   act.Name,
					Params: act.Params,
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

// PerformAction implementation for the stub server
func (s *StubServer) PerformAction(name string, params map[string]interface{}) (map[string]interface{}, error) {
	paramBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	resCh := make(chan actionResultCh)
	s.ActionsChan <- &PerformAction{
		Name:   name,
		Params: paramBytes,
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
