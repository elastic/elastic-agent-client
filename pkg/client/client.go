// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/elastic/elastic-agent-client/v7/pkg/utils"
)

// CheckinMinimumTimeout is the amount of time the client must send a new checkin even if the status has not changed.
const CheckinMinimumTimeout = time.Second * 30

// ActionErrUndefined is returned to Elastic Agent as result to an action request
// when the request action is not registered in the client.
var ActionErrUndefined = utils.JSONMustMarshal(map[string]string{
	"error": "action undefined",
})

// ActionErrUnmarshableParams is returned to Elastic Agent as result to an action request
// when the request params could not be un-marshaled to send to the action.
var ActionErrUnmarshableParams = utils.JSONMustMarshal(map[string]string{
	"error": "action params failed to be un-marshaled",
})

// ActionErrInvalidParams is returned to Elastic Agent as result to an action request
// when the request params are invalid for the action.
var ActionErrInvalidParams = utils.JSONMustMarshal(map[string]string{
	"error": "action params invalid",
})

// ActionErrUnmarshableResult is returned to Elastic Agent as result to an action request
// when the action was performed but the response could not be marshalled to send back to
// the agent.
var ActionErrUnmarshableResult = utils.JSONMustMarshal(map[string]string{
	"error": "action result failed to be marshaled",
})

// Action is an action the client exposed to the Elastic Agent.
type Action interface {
	// Name of the action.
	Name() string

	// Execute performs the action.
	Execute(map[string]interface{}) (map[string]interface{}, error)
}

// StateInterface defines how to handle config and stop requests.
type StateInterface interface {
	// OnConfig is called when the Elastic Agent is requesting that the configuration
	// be set to the provided new value.
	OnConfig(string)

	// OnStop is called when the Elastic Agent is requesting the application to stop.
	OnStop()

	// OnError is called when an errors occurs communicating with Elastic Agent.
	//
	// These error messages are not given by the Elastic Agent, they are just errors exposed
	// from the client-side GRPC connection.
	OnError(error)
}

// Client manages the state and communication to the Elastic Agent.
type Client struct {
	target          string
	opts            []grpc.DialOption
	token           string
	impl            StateInterface
	actions         map[string]Action
	cfgIdx          uint64
	cfg             string
	expected        proto.StateExpected_State
	observed        proto.StateObserved_Status
	observedMessage string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	client proto.ElasticAgentClient
	cfgLock   sync.RWMutex
	obsLock   sync.RWMutex

	// overridden in tests to make fast
	minCheckTimeout time.Duration
}

// New creates a client connection to Elastic Agent.
func New(target string, token string, impl StateInterface, actions []Action, opts ...grpc.DialOption) *Client {
	actionMap := map[string]Action{}
	if actions != nil {
		for _, act := range actions {
			actionMap[act.Name()] = act
		}
	}
	return &Client{
		target:          target,
		opts:            opts,
		token:           token,
		impl:            impl,
		actions:         actionMap,
		cfgIdx:          0, // 0 is initial, no config state
		expected:        proto.StateExpected_RUNNING,
		observed:        proto.StateObserved_STARTING,
		observedMessage: "Starting",
		minCheckTimeout: CheckinMinimumTimeout,
	}
}

// Start starts the connection to Elastic Agent.
func (c *Client) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c.ctx = ctx
	c.cancel = cancel
	conn, err := grpc.DialContext(ctx, c.target, c.opts...)
	if err != nil {
		return err
	}
	c.client = proto.NewElasticAgentClient(conn)
	c.startCheckin()
	c.startActions()
	return nil
}

// Stop stops the connection to Elastic Agent.
func (c *Client) Stop() {
	if c.cancel != nil {
		c.cancel()
		c.wg.Wait()
		c.ctx = nil
		c.cancel = nil
	}
}

// Status updates the current status of the client in the Elastic Agent.
func (c *Client) Status(status proto.StateObserved_Status, message string) {
	c.obsLock.Lock()
	c.observed = status
	c.observedMessage = message
	c.obsLock.Unlock()
}

// startCheckin starts the go rountines to send and receive check-ins
func (c *Client) startCheckin() {
	c.wg.Add(1)

	go func() {
		defer c.wg.Done()
		for {
			select {
			case <-c.ctx.Done():
				// stopped
				return
			default:
			}

			checkinClient, err := c.client.Checkin(c.ctx)
			if err != nil {
				c.impl.OnError(err)
				continue
			}

			var wg sync.WaitGroup
			wg.Add(2)
			done := make(chan bool)

			// expected state check-ins
			go func() {
				defer wg.Done()
				for {
					expected, err := checkinClient.Recv()
					if err != nil {
						if err != io.EOF {
							c.impl.OnError(err)
						}
						close(done)
						return
					}

					if c.expected == proto.StateExpected_STOPPING {
						// in stopping state, do nothing with any other expected states
						continue
					}
					if expected.State == proto.StateExpected_STOPPING {
						// Elastic Agent is requesting us to stop.
						c.expected = expected.State
						c.impl.OnStop()
						continue
					}
					if expected.ConfigStateIdx != c.cfgIdx {
						// Elastic Agent is requesting us to update config.
						c.cfgLock.Lock()
						c.cfgIdx = expected.ConfigStateIdx
						c.cfg = expected.Config
						c.cfgLock.Unlock()
						c.impl.OnConfig(expected.Config)
						continue
					}
				}
			}()

			// action responses
			go func() {
				defer wg.Done()

				var lastSent time.Time
				var lastSentCfgIdx uint64
				var lastSentStatus proto.StateObserved_Status
				var lastSentMessage string
				for {
					select {
					case <-done:
						return
					case <-time.After(500 * time.Millisecond):
					}

					c.cfgLock.RLock()
					cfgIdx := c.cfgIdx
					c.cfgLock.RUnlock()
					c.obsLock.RLock()
					observed := c.observed
					observedMsg := c.observedMessage
					c.obsLock.RUnlock()

					sendMessage := func() {
						err := checkinClient.Send(&proto.StateObserved{
							Token:          c.token,
							ConfigStateIdx: cfgIdx,
							Status:         observed,
							Message:        observedMsg,
						})
						if err != nil {
							c.impl.OnError(err)
							return
						}
						lastSent = time.Now()
						lastSentCfgIdx = cfgIdx
						lastSentStatus = observed
						lastSentMessage = observedMsg
					}

					// On start keep trying to send the initial check-in.
					if lastSent.IsZero() {
						sendMessage()
						continue
					}

					// Send new status when it has changed.
					if lastSentCfgIdx != cfgIdx || lastSentStatus != observed || lastSentMessage != observedMsg {
						sendMessage()
						continue
					}

					// Send when more than 30 seconds has passed without any status change.
					if time.Now().Sub(lastSent) >= c.minCheckTimeout {
						sendMessage()
						continue
					}
				}
			}()

			// wait for both send and recv go routines to stop before
			// starting a new stream.
			wg.Wait()
		}
	}()
}

// startActions starts the go rountines to send and receive actions
func (c *Client) startActions() {
	c.wg.Add(1)

	// results our held outside of the retry loop, because on re-connect
	// we still want to send the responses that either failed or haven't been
	// sent back to the agent.
	actionResults := make(chan *proto.ActionResponse, 100)
	go func() {
		defer c.wg.Done()
		for {
			select {
			case <-c.ctx.Done():
				// stopped
				return
			default:
			}

			actionsClient, err := c.client.Actions(c.ctx)
			if err != nil {
				c.impl.OnError(err)
				continue
			}

			// initial connection of stream must send the token so
			// the Elastic Agent knows this clients token.
			actionResults <- &proto.ActionResponse{
				Token:  c.token,
				Id:     "init",
				Status: proto.ActionResponse_SUCCESS,
				Result: []byte("{}"),
			}

			var wg sync.WaitGroup
			wg.Add(2)
			done := make(chan bool)

			// action requests
			go func() {
				defer wg.Done()
				for {
					action, err := actionsClient.Recv()
					if err != nil {
						if err != io.EOF {
							c.impl.OnError(err)
						}
						close(done)
						return
					}

					actionImpl, ok := c.actions[action.Name]
					if !ok {
						actionResults <- &proto.ActionResponse{
							Token:  c.token,
							Id:     action.Id,
							Status: proto.ActionResponse_FAILED,
							Result: ActionErrUndefined,
						}
						continue
					}

					var params map[string]interface{}
					err = json.Unmarshal(action.Params, &params)
					if err != nil {
						actionResults <- &proto.ActionResponse{
							Token:  c.token,
							Id:     action.Id,
							Status: proto.ActionResponse_FAILED,
							Result: ActionErrUnmarshableParams,
						}
						continue
					}

					// perform the action
					go func() {
						res, err := actionImpl.Execute(params)
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
							c.impl.OnError(err)
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
			}()

			// action responses
			go func() {
				defer wg.Done()
				for {
					select {
					case <-done:
						return
					case res := <-actionResults:
						err := actionsClient.Send(res)
						if err != nil {
							// failed to send, add back to response to try again
							actionResults <- res
							c.impl.OnError(err)
						}
					}
				}
			}()

			// wait for both send and recv go routines to stop before
			// starting a new stream.
			wg.Wait()
		}
	}()
}
