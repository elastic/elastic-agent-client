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

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestClient_DialError(t *testing.T) {
	srv := StubServer{
		CheckinImpl: func(observed *proto.StateObserved) *proto.StateExpected {
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

	impl := &StubClientImpl{}
	invalidClient := New(fmt.Sprintf(":%d", srv.Port), "invalid_token", impl, nil)
	assert.Error(t, invalidClient.Start(context.Background()))
	defer invalidClient.Stop()
}

func TestClient_Status(t *testing.T) {
	c := New(":0", "invalid_token", &StubClientImpl{}, nil).(*client)
	c.Status(proto.StateObserved_HEALTHY, "Running", map[string]interface{}{
		"ensure": "that",
		"order":  "does",
		"not":    "matter",
	})
	setStr := c.observedPayload
	c.Status(proto.StateObserved_HEALTHY, "Other", map[string]interface{}{
		"not":    "matter",
		"ensure": "that",
		"order":  "does",
	})
	assert.Equal(t, setStr, c.observedPayload)
}

func TestClient_Checkin_With_Token(t *testing.T) {
	var m sync.Mutex
	token := "expected_token"
	gotInvalid := false
	gotValid := false
	srv := StubServer{
		CheckinImpl: func(observed *proto.StateObserved) *proto.StateExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				gotValid = true
				return &proto.StateExpected{
					State:          proto.StateExpected_RUNNING,
					ConfigStateIdx: 1,
					Config:         "config",
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
	impl := &StubClientImpl{}
	invalidClient := New(fmt.Sprintf(":%d", srv.Port), "invalid_token", impl, nil, grpc.WithInsecure())
	require.NoError(t, invalidClient.Start(context.Background()))
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

	// connect with an valid token
	impl = &StubClientImpl{}
	validClient := New(fmt.Sprintf(":%d", srv.Port), token, impl, nil, grpc.WithInsecure()).(*client)
	require.NoError(t, validClient.Start(context.Background()))
	defer validClient.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !gotValid {
			return fmt.Errorf("server never recieved valid token")
		}
		return nil
	}))
	impl.Lock.Lock()
	defer impl.Lock.Unlock()
	validClient.cfgLock.Lock()
	defer validClient.cfgLock.Unlock()
	assert.Equal(t, uint64(1), validClient.cfgIdx)
	assert.Equal(t, "config", validClient.cfg)
	assert.Equal(t, "config", impl.Config)
}

func TestClient_Checkin_Status(t *testing.T) {
	var m sync.Mutex
	token := "expected_token"
	connected := false
	status := proto.StateObserved_STARTING
	message := ""
	payload := ""
	healthyCount := 0
	srv := StubServer{
		CheckinImpl: func(observed *proto.StateObserved) *proto.StateExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				if observed.ConfigStateIdx == 0 {
					connected = true
					return &proto.StateExpected{
						State:          proto.StateExpected_RUNNING,
						ConfigStateIdx: 1,
						Config:         "config",
					}
				} else if observed.ConfigStateIdx == 1 {
					status = observed.Status
					message = observed.Message
					payload = observed.Payload
					if status == proto.StateObserved_HEALTHY {
						healthyCount++
					}
					return &proto.StateExpected{
						State:          proto.StateExpected_RUNNING,
						ConfigStateIdx: 1,
						Config:         "",
					}
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

	impl := &StubClientImpl{}
	client := New(fmt.Sprintf(":%d", srv.Port), token, impl, nil, grpc.WithInsecure()).(*client)
	client.minCheckTimeout = 100 * time.Millisecond
	require.NoError(t, client.Start(context.Background()))
	defer client.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !connected {
			return fmt.Errorf("server never recieved valid token")
		}
		return nil
	}))
	client.Status(proto.StateObserved_CONFIGURING, "Configuring", nil)
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if status != proto.StateObserved_CONFIGURING {
			return fmt.Errorf("server never recieved updated status")
		}
		return nil
	}))
	require.Equal(t, proto.StateObserved_CONFIGURING, status)
	require.Equal(t, "Configuring", message)
	client.Status(proto.StateObserved_HEALTHY, "Running", map[string]interface{}{
		"payload": "sent",
	})

	// wait for at least 5 check-ins of healthy
	assert.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if healthyCount < 5 {
			return fmt.Errorf("server never recieved 5 healthy checkins")
		}
		return nil
	}))

	require.Equal(t, proto.StateObserved_HEALTHY, status)
	require.Equal(t, "Running", message)
	require.Equal(t, `{"payload":"sent"}`, payload)
}

func TestClient_Checkin_Stop(t *testing.T) {
	var m sync.Mutex
	token := "expected_token"
	connected := false
	shuttingDown := false
	srv := StubServer{
		CheckinImpl: func(observed *proto.StateObserved) *proto.StateExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				if observed.ConfigStateIdx == 0 {
					connected = true
					return &proto.StateExpected{
						State:          proto.StateExpected_RUNNING,
						ConfigStateIdx: 1,
						Config:         "config",
					}
				} else if observed.ConfigStateIdx == 1 {
					if observed.Status == proto.StateObserved_STOPPING {
						shuttingDown = true
					}
					return &proto.StateExpected{
						State:          proto.StateExpected_STOPPING,
						ConfigStateIdx: 1,
						Config:         "",
					}
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

	impl := &StubClientImpl{}
	client := New(fmt.Sprintf(":%d", srv.Port), token, impl, nil, grpc.WithInsecure()).(*client)
	client.minCheckTimeout = 100 * time.Millisecond
	require.NoError(t, client.Start(context.Background()))
	defer client.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !connected {
			return fmt.Errorf("server never recieved valid token")
		}
		return nil
	}))
	// wait for client to receive stop
	require.NoError(t, waitFor(func() error {
		impl.Lock.Lock()
		defer impl.Lock.Unlock()

		if !impl.Stop {
			return fmt.Errorf("client never recieved stop")
		}
		return nil
	}))
	client.Status(proto.StateObserved_STOPPING, "Shutting down", nil)
	// wait for server to receive stopping
	assert.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !shuttingDown {
			return fmt.Errorf("server never recieved stopping status from client")
		}
		return nil
	}))
}

func TestClient_Actions(t *testing.T) {
	var m sync.Mutex
	token := "expected_token"
	gotInit := false
	srv := StubServer{
		CheckinImpl: func(observed *proto.StateObserved) *proto.StateExpected {
			if observed.Token == token {
				if observed.ConfigStateIdx == 0 {
					return &proto.StateExpected{
						State:          proto.StateExpected_RUNNING,
						ConfigStateIdx: 1,
						Config:         "config",
					}
				} else if observed.ConfigStateIdx == 1 {
					return &proto.StateExpected{
						State:          proto.StateExpected_STOPPING,
						ConfigStateIdx: 1,
						Config:         "",
					}
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

	impl := &StubClientImpl{}
	client := New(fmt.Sprintf(":%d", srv.Port), token, impl, []Action{&AddAction{}}, grpc.WithInsecure())
	require.NoError(t, client.Start(context.Background()))
	defer client.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !gotInit {
			return fmt.Errorf("server never recieved valid token")
		}
		return nil
	}))

	// send an none registered action
	_, err := srv.PerformAction("invalid", nil)
	assert.Error(t, err)

	// send successful add action
	res, err := srv.PerformAction("add", map[string]interface{}{
		"numbers": []int{10, 20, 30},
	})
	require.NoError(t, err)
	total, _ := res["total"]
	assert.Equal(t, float64(60), total.(float64))

	// send bad params
	_, err = srv.PerformAction("add", map[string]interface{}{
		"numbers": []interface{}{"bad", 20, 30},
	})
	assert.Error(t, err)
}

type AddAction struct{}

func (a *AddAction) Name() string {
	return "add"
}

func (a *AddAction) Execute(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	numbersRaw, ok := params["numbers"]
	if !ok {
		return nil, fmt.Errorf("missing numbers parameter")
	}
	numbersArray, ok := numbersRaw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("numbers should be an array")
	}
	total := 0
	for _, number := range numbersArray {
		num, ok := number.(float64)
		if !ok {
			return nil, fmt.Errorf("numbers array must only include numbers")
		}
		total += int(num)
	}
	return map[string]interface{}{
		"total": total,
	}, nil
}

type StubClientImpl struct {
	Lock   sync.Mutex
	Config string
	Stop   bool
	Error  error
}

func (c *StubClientImpl) OnConfig(config string) {
	c.Lock.Lock()
	defer c.Lock.Unlock()
	c.Config = config
}

func (c *StubClientImpl) OnStop() {
	c.Lock.Lock()
	defer c.Lock.Unlock()
	c.Stop = true
}

func (c *StubClientImpl) OnError(err error) {
	c.Lock.Lock()
	defer c.Lock.Unlock()
	c.Error = err
}

type StubServerCheckin func(*proto.StateObserved) *proto.StateExpected
type StubServerAction func(*proto.ActionResponse) error

type performAction struct {
	Name     string
	Params   []byte
	Callback func(map[string]interface{}, error)
}

type actionResultChan struct {
	Result map[string]interface{}
	Err    error
}

type StubServer struct {
	Port        int
	CheckinImpl StubServerCheckin
	ActionImpl  StubServerAction

	server      *grpc.Server
	actions     chan *performAction
	sentActions map[string]*performAction
}

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

func (s *StubServer) Stop() {
	if s.server != nil {
		s.server.Stop()
		s.server = nil
	}
}

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

func (s *StubServer) Actions(server proto.ElasticAgent_ActionsServer) error {
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

func (s *StubServer) PerformAction(name string, params map[string]interface{}) (map[string]interface{}, error) {
	paramBytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	resChan := make(chan actionResultChan)
	s.actions <- &performAction{
		Name:   name,
		Params: paramBytes,
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

func waitFor(check func() error) error {
	started := time.Now()
	for {
		err := check()
		if err == nil {
			return nil
		}
		if time.Now().Sub(started) >= 5*time.Second {
			return fmt.Errorf("check timed out after 1 second: %s", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
