// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elastic/elastic-agent-libs/atomic"
	"github.com/google/pprof/profile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

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

	client := NewV2(listener.Addr().String(), mock.NewID(), VersionInfo{}, WithGRPCDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
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
	ca, err := NewCA()
	require.NoError(t, err)
	peer, err := ca.GeneratePair()
	require.NoError(t, err)
	peerCert, err := peer.Certificate()
	require.NoError(t, err)

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(ca.caPEM)
	serverCreds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{peerCert},
		ClientCAs:    caCertPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	})

	clientCreds := credentials.NewTLS(&tls.Config{
		ServerName:   "localhost", // important(!): should match the cert settings, otherwise fails on windows
		Certificates: []tls.Certificate{peerCert},
		RootCAs:      caCertPool,
	})

	// Test without and with TLS over tcp, unix/pipe
	tests := []struct {
		name        string
		serverCreds credentials.TransportCredentials
		clientCreds credentials.TransportCredentials
		localRPC    string
	}{
		{
			name: "tcp",
		},
		{
			name:     "local",
			localRPC: "elagtes",
		},
		{
			name:        "tcp",
			serverCreds: serverCreds,
			clientCreds: clientCreds,
		},
		{
			name:        "local",
			localRPC:    "elagtes",
			serverCreds: serverCreds,
			clientCreds: clientCreds,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testClientV2CheckinInitial(
				t, tc.localRPC, tc.serverCreds, tc.clientCreds)
		})
	}
}

func testClientV2CheckinInitial(t *testing.T, localRPC string, serverCreds, clientCreds credentials.TransportCredentials) {
	var m sync.Mutex
	token := mock.NewID()
	unitOneID := mock.NewID()
	unitTwoID := mock.NewID()
	wantFQDN := true

	packageVersion := "8.13.0+build20060102"
	wantBuildHash := "build_hash"
	wantAgentInfo := AgentInfo{
		ID:           "elastic-agent-id",
		Version:      packageVersion,
		Snapshot:     true,
		ManagedMode:  proto.AgentManagedMode_STANDALONE,
		Unprivileged: true,
	}
	vInfo := VersionInfo{
		Name:      "program",
		BuildHash: wantBuildHash,
		Meta: map[string]string{
			"key": "value",
		},
	}

	checkInDone := make(chan *proto.CheckinObserved, 1)
	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			m.Lock()
			defer m.Unlock()

			defer func() {
				checkInDone <- observed
			}()

			return &proto.CheckinExpected{
				AgentInfo: &proto.AgentInfo{
					Id:           wantAgentInfo.ID,
					Version:      wantAgentInfo.Version,
					Snapshot:     wantAgentInfo.Snapshot,
					Mode:         wantAgentInfo.ManagedMode,
					Unprivileged: wantAgentInfo.Unprivileged,
				},
				Features: &proto.Features{
					Fqdn: &proto.FQDNFeature{Enabled: wantFQDN},
				},
				Component: &proto.Component{
					Limits: &proto.ComponentLimits{
						GoMaxProcs: 0,
					},
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

		},
		ActionImpl: func(response *proto.ActionResponse) error {
			// actions not tested here
			return nil
		},
		ActionsChan: make(chan *mock.PerformAction, 100),
		LocalRPC:    localRPC,
	}

	var serverOptions []grpc.ServerOption
	if serverCreds != nil {
		serverOptions = append(serverOptions, grpc.Creds(serverCreds))
	}

	require.NoError(t, srv.Start(serverOptions...), "failed to start GRPC server")
	defer srv.Stop()

	if clientCreds == nil {
		clientCreds = insecure.NewCredentials()
	}
	dialOptions := []grpc.DialOption{grpc.WithTransportCredentials(clientCreds)}

	var errsMu sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clientv2 := NewV2(srv.GetTarget(), token, vInfo,
		WithAgentInfo(wantAgentInfo),
		WithGRPCDialOptions(dialOptions...))
	storeErrors(ctx, clientv2, &errs, &errsMu)

	// receive the units
	receivedUnits := make(chan struct{})
	var units []*Unit
	var gotFQDN bool
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-clientv2.UnitChanges():
				if change.Triggers&TriggeredFeatureChange == TriggeredFeatureChange {
					gotFQDN = change.Unit.Expected().Features.Fqdn.Enabled
				}

				switch change.Type {
				case UnitChangedAdded:
					units = append(units, change.Unit)
				default:
					panic(fmt.Errorf("received unnexpected change type: %s",
						change.Type))
				}
			case <-time.After(5 * time.Minute):
				panic(fmt.Errorf("timed out waiting for unit changes after 2s"))
			}

			if len(units) == 2 {
				close(receivedUnits)
				return
			}
		}
	}()

	require.NoError(t, clientv2.Start(ctx), "failed starting client V2")
	defer clientv2.Stop()

	<-receivedUnits

	var gotObserved *proto.CheckinObserved
	timeout := 1 * time.Second
	select {
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for 1st checkin to complete",
			timeout)
	case gotObserved = <-checkInDone:
	}

	assert.Empty(t, errs, "client should not have sent eny error")

	clientv2.Stop()
	srv.Stop()
	cancel()

	agentInfo := clientv2.AgentInfo()
	if assert.NotNilf(t, agentInfo, "AgentInfo should not be nil") {
		assert.Equal(t, wantAgentInfo.ID, agentInfo.ID)
		assert.Equal(t, wantAgentInfo.Version, agentInfo.Version)
		assert.True(t, wantAgentInfo.Snapshot, agentInfo.Snapshot)
		assert.Equal(t, wantAgentInfo.ManagedMode, agentInfo.ManagedMode)
		assert.Equal(t, wantAgentInfo.Unprivileged, agentInfo.Unprivileged)
	}

	assert.Truef(t, gotFQDN, "FQND should be true")

	if assert.Lenf(t, units, 2, "should have received 2 units") {
		assert.Equal(t, units[0].ID(), unitOneID)
		assert.Equal(t, units[0].Type(), UnitTypeOutput)
		assert.Equal(t, units[1].ID(), unitTwoID)
		assert.Equal(t, units[1].Type(), UnitTypeInput)
	}

	assert.Equal(t, "program", gotObserved.VersionInfo.Name)
	assert.Equal(t, wantBuildHash, gotObserved.VersionInfo.BuildHash)
	assert.Equal(t, map[string]string{"key": "value"}, gotObserved.VersionInfo.Meta)
}

func TestClientV2_Checkin_UnitState(t *testing.T) {
	tests := []struct {
		name     string
		localRPC string
	}{
		{
			name: "tcp",
		},
		{
			name:     "local",
			localRPC: "elagtes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(n string) func(t *testing.T) {
			return func(t *testing.T) {
				testClientV2CheckinUnitState(t, n)
			}
		}(tc.localRPC))
	}
}

func testClientV2CheckinUnitState(t *testing.T, localRPC string) {
	var m sync.Mutex
	token := mock.NewID()
	connected := false
	unitOne := newUnit(mock.NewID(), UnitTypeOutput, UnitStateStarting, UnitLogLevelInfo, nil, 0, nil, nil, nil)
	unitTwo := newUnit(mock.NewID(), UnitTypeInput, UnitStateStarting, UnitLogLevelInfo, nil, 0, nil, nil, nil)
	wantFQDN := true
	srv := mock.StubServerV2{
		LocalRPC: localRPC,
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
						Component: &proto.Component{
							Limits: &proto.ComponentLimits{
								GoMaxProcs: 0,
							},
						},
						ComponentIdx: 1,
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
						Component: &proto.Component{
							Limits: &proto.ComponentLimits{
								GoMaxProcs: 0,
							},
						},
						ComponentIdx: 1,
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
						Component: &proto.Component{
							Limits: &proto.ComponentLimits{
								GoMaxProcs: 0,
							},
						},
						ComponentIdx: 1,
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
	client := NewV2(
		srv.GetTarget(), token, VersionInfo{},
		WithChunking(true), WithMaxMessageSize(150),
		WithGRPCDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()))).(*clientV2)
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
	m.Lock()
	defer m.Unlock()
	assert.Equal(t, UnitStateHealthy, unitOne.state)
	assert.Equal(t, "Healthy", unitOne.stateMsg)
	assert.Equal(t, UnitStateStopped, unitTwo.state)
	assert.Equal(t, "Stopped", unitTwo.stateMsg)
}

func TestClientV2_Actions(t *testing.T) {
	tests := []struct {
		name     string
		localRPC string
	}{
		{
			name: "tcp",
		},
		{
			name:     "local",
			localRPC: "elagtes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(n string) func(t *testing.T) {
			return func(t *testing.T) {
				testClientV2Actions(t, n)
			}
		}(tc.localRPC))
	}
}

func testClientV2Actions(t *testing.T, localRPC string) {
	var m sync.Mutex
	token := mock.NewID()
	gotInit := false
	srv := mock.StubServerV2{
		LocalRPC: localRPC,
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
	client := NewV2(srv.GetTarget(), token, VersionInfo{}, WithGRPCDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
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

func TestComponentDiagnostics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	unit, client, srv := setupClientForDiagnostics(ctx, t)
	defer srv.Stop()
	defer client.Stop()

	client.RegisterDiagnosticHook("custom_component", "customer diagnostic for the component", "custom_component.txt", "plain/text", func() []byte {
		return []byte("custom component")
	})

	res, err := srv.PerformDiagnostic(unit.id, proto.UnitType(unit.unitType), proto.ActionRequest_COMPONENT, []byte{})
	assert.NoError(t, err)

	expectedNames := []string{"goroutine", "heap", "allocs", "threadcreate", "block", "mutex", "custom_component"}
	assert.Equal(t, len(expectedNames), len(res))
	for _, d := range res {
		assert.Contains(t, expectedNames, d.Name)
		switch d.Name {
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

func TestUnitDiagnostics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	unit, client, srv := setupClientForDiagnostics(ctx, t)
	defer srv.Stop()
	defer client.Stop()

	// add an optional callback, make sure we don't actually call it.
	unit.RegisterOptionalDiagnosticHook("optional", "custom_unit", "custom diag for a unit", "custom_diag.txt", "plain/text", func() []byte {
		return []byte("custom unit")
	})

	unit.RegisterDiagnosticHook("custom_unit", "custom diagnostic for the unit", "custom_unit.txt", "plain/text", func() []byte {
		return []byte("custom unit")
	})

	res, err := srv.PerformDiagnostic(unit.id, proto.UnitType(unit.unitType), proto.ActionRequest_UNIT, []byte{})
	require.NoError(t, err)

	expectedNames := []string{"custom_unit"}
	require.Equal(t, len(expectedNames), len(res))

	require.Equal(t, []byte("custom unit"), res[0].Content)
}

func TestComponentDiagnosticsWithTag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	unit, client, srv := setupClientForDiagnostics(ctx, t)
	defer srv.Stop()
	defer client.Stop()

	client.RegisterOptionalDiagnosticHook("opt", "custom_component", "custom diag for component", "custom_diag.txt", "plain/text", func() []byte {
		return []byte("custom component")
	})

	optParams := DiagnosticParams{AdditionalMetrics: []string{"opt"}}
	bytesParams, err := json.Marshal(&optParams)
	require.NoError(t, err)

	res, err := srv.PerformDiagnostic(unit.id, proto.UnitType(unit.unitType), proto.ActionRequest_COMPONENT, bytesParams)
	require.NoError(t, err)

	expectedNames := []string{"goroutine", "heap", "allocs", "threadcreate", "block", "mutex", "custom_component"}
	assert.Equal(t, len(expectedNames), len(res))
	for _, d := range res {
		assert.Contains(t, expectedNames, d.Name)
		switch d.Name {
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

func TestUnitDiagnosticsWithTag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	unit, client, srv := setupClientForDiagnostics(ctx, t)
	defer srv.Stop()
	defer client.Stop()

	unit.RegisterOptionalDiagnosticHook("opt", "custom_unit", "custom diag for a unit", "custom_diag.txt", "plain/text", func() []byte {
		return []byte("custom unit")
	})

	optParams := DiagnosticParams{AdditionalMetrics: []string{"opt"}}
	bytesParams, err := json.Marshal(&optParams)
	require.NoError(t, err)

	res, err := srv.PerformDiagnostic(unit.id, proto.UnitType(unit.unitType), proto.ActionRequest_UNIT, bytesParams)
	require.NoError(t, err)

	expectedNames := []string{"custom_unit"}
	require.Equal(t, len(expectedNames), len(res))

	require.Equal(t, []byte("custom unit"), res[0].Content)
}

func TestDiagnosticsAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	unit, client, srv := setupClientForDiagnostics(ctx, t)
	defer srv.Stop()
	defer client.Stop()

	unit.RegisterDiagnosticHook("custom_unit", "custom diag for a unit", "custom_diag.txt", "plain/text", func() []byte {
		return []byte("custom unit")
	})

	client.RegisterDiagnosticHook("custom_component", "custom diag for component", "custom_diag.txt", "plain/text", func() []byte {
		return []byte("custom component")
	})

	res, err := srv.PerformDiagnostic(unit.id, proto.UnitType(unit.unitType), proto.ActionRequest_ALL, []byte{})
	require.NoError(t, err)

	expectedNames := []string{"goroutine", "heap", "allocs", "threadcreate", "block", "mutex", "custom_component", "custom_unit"}
	assert.Equal(t, len(expectedNames), len(res))
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
			Name:    "two changes: feature_change_triggered, apm_config_change_triggered",
			Trigger: TriggeredFeatureChange | TriggeredAPMChange,
			Want:    "feature_change_triggered, apm_config_change_triggered",
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
	unit := newUnit(mock.NewID(), UnitTypeInput, UnitStateStarting, UnitLogLevelInfo, nil, 0, nil, nil, nil)
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
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, WithGRPCDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()))).(*clientV2)
	client.minCheckTimeout = 100 * time.Millisecond // otherwise the test will run for too long
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

type unitChangesAccumulator struct {
	mx          sync.Mutex
	unitChanges []UnitChanged
}

func (uca *unitChangesAccumulator) Start(ctx context.Context, t *testing.T, c <-chan UnitChanged) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case uc := <-c:
				func() {
					t.Logf("received unit change %v", uc)
					switch uc.Type {
					case UnitChangedAdded:
						err := uc.Unit.UpdateState(UnitStateHealthy, "Healthy", map[string]interface{}{
							"custom": "payload",
						})
						require.NoError(t, err)
					}

					uca.mx.Lock()
					defer uca.mx.Unlock()

					uca.unitChanges = append(uca.unitChanges, uc)
				}()
			}
		}
	}()
}

func (uca *unitChangesAccumulator) GetAccumulatedChanges() []UnitChanged {
	uca.mx.Lock()
	defer uca.mx.Unlock()

	ret := make([]UnitChanged, len(uca.unitChanges))
	copy(ret, uca.unitChanges)
	return ret
}

func TestClientV2_Checkin_APMConfig(t *testing.T) {
	var m sync.Mutex
	token := mock.NewID()
	unit := newUnit(mock.NewID(), UnitTypeInput, UnitStateStarting, UnitLogLevelInfo, nil, 0, nil, nil, nil)
	connected := false
	checkinCounter := 0

	apmConfig := &proto.APMConfig{
		Elastic: &proto.ElasticAPM{
			Environment: "test-client",
			ApiKey:      "someAPIKey",
			SecretToken: "",
			Hosts:       []string{"host1", "host2"},
			Tls:         nil,
		},
	}

	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				connected = true

				switch checkinCounter {
				case 0:
					// first checkin
					checkinCounter++
					return &proto.CheckinExpected{
						FeaturesIdx: 0,
						Component: &proto.Component{
							ApmConfig: apmConfig,
						},
						ComponentIdx: 1,
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
					assert.Equal(t, uint64(1), observed.ComponentIdx)
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
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, WithGRPCDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()))).(*clientV2)
	storeErrors(ctx, client, &errs, &errsMu)

	var uca unitChangesAccumulator
	uca.Start(ctx, t, client.UnitChanges())

	require.NoError(t, client.Start(ctx))
	defer client.Stop()

	require.Eventually(t, func() bool {
		m.Lock()
		defer m.Unlock()
		return checkinCounter >= 2
	}, time.Second, 100*time.Millisecond, "server did not receive expected number of checkins")

	require.Empty(t, errs)
	assert.True(t, connected)
	changes := uca.GetAccumulatedChanges()
	assert.NotEmpty(t, changes)
	for _, c := range changes {
		assert.Truef(
			t,
			gproto.Equal(apmConfig, c.Unit.apm),
			"unit id %s has wrong apm config: expected: %v, actual: %v",
			c.Unit.ID(),
			apmConfig,
			c.Unit.apm,
		)
	}
}

func TestClientV2_Checkin_Component(t *testing.T) {
	type componentConfigServerValidation struct {
		componentIdx uint64
		goMaxProcs   int
	}

	var m sync.Mutex
	token := mock.NewID()
	connected := false
	checkinCounter := 0
	initialComponentsIdx := rand.Uint64()
	componentsIdx := initialComponentsIdx

	initialGoMaxProcs := runtime.GOMAXPROCS(0)
	expGoMaxProcs := 999 // unlikely to match the actual core count
	require.NotEqual(t, expGoMaxProcs, initialGoMaxProcs, "the actual GOMAXPROCS should not equal to the test value")

	// every validation takes place before the checkin response from the server
	serverValidations := []struct {
		name             string
		actual, expected componentConfigServerValidation
	}{
		{
			name: "returns no component config, sets initial idx",
			expected: componentConfigServerValidation{
				componentIdx: 0,
				goMaxProcs:   initialGoMaxProcs,
			},
		},
		{
			name: "returns a component config but no limits defined",
			expected: componentConfigServerValidation{
				componentIdx: initialComponentsIdx,
				goMaxProcs:   initialGoMaxProcs,
			},
		},
		{
			name: "returns GOMAXPROCS set",
			expected: componentConfigServerValidation{
				componentIdx: initialComponentsIdx + 1,
				goMaxProcs:   initialGoMaxProcs,
			},
		},
		{
			name: "checks GOMAXPROCS is set",
			expected: componentConfigServerValidation{
				componentIdx: initialComponentsIdx + 2,
				goMaxProcs:   expGoMaxProcs,
			},
		},
		{
			name: "unsets GOMAXPROCS",
			expected: componentConfigServerValidation{
				// no increment in this, previous step didn't change the config
				componentIdx: initialComponentsIdx + 2,
				goMaxProcs:   expGoMaxProcs,
			},
		},
		{
			name: "checks GOMAXPROCS is reset",
			expected: componentConfigServerValidation{
				componentIdx: initialComponentsIdx + 3,
				goMaxProcs:   initialGoMaxProcs,
			},
		},
	}

	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				connected = true
				if checkinCounter > len(serverValidations) {
					return nil
				}
				serverValidations[checkinCounter].actual = componentConfigServerValidation{
					componentIdx: observed.ComponentIdx,
					goMaxProcs:   runtime.GOMAXPROCS(0),
				}

				switch checkinCounter {
				case 0:
					checkinCounter++
					return &proto.CheckinExpected{
						ComponentIdx: componentsIdx,
						Units:        []*proto.UnitExpected{},
					}

				case 1:
					checkinCounter++
					componentsIdx++

					return &proto.CheckinExpected{
						ComponentIdx: componentsIdx,
						Component:    &proto.Component{},
						Units:        []*proto.UnitExpected{},
					}

				case 2:
					checkinCounter++
					componentsIdx++

					return &proto.CheckinExpected{
						ComponentIdx: componentsIdx,
						Component: &proto.Component{
							Limits: &proto.ComponentLimits{
								GoMaxProcs: uint64(expGoMaxProcs),
							},
						},
						Units: []*proto.UnitExpected{},
					}

				case 3:
					checkinCounter++

				case 4:
					checkinCounter++
					componentsIdx++

					return &proto.CheckinExpected{
						ComponentIdx: componentsIdx,
						Units:        []*proto.UnitExpected{},
					}

				case 5:
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

	serverAddr := fmt.Sprintf(":%d", srv.Port)
	client := NewV2(serverAddr, token, VersionInfo{}, WithGRPCDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials()))).(*clientV2)
	client.minCheckTimeout = 100 * time.Millisecond // otherwise the test will run for too long
	storeErrors(ctx, client, &errs, &errsMu)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-client.UnitChanges(): // otherwise the client can block forever
				// we don't need to react to units since we test the component-level change
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
		expectedCheckinCounter := len(serverValidations)
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

	for _, sv := range serverValidations {
		t.Run(sv.name, func(t *testing.T) {
			require.Equal(t, sv.expected, sv.actual)
		})
	}
}

func TestClientV2_Checkin_OptInComponent(t *testing.T) {

	type args struct {
		checkinExpectedGenerator mock.StubServerCheckinV2
	}

	type expected struct {
		componentExpected        *proto.Component
		expectedComponentIdx     uint64
		unitsExpected            []Unit
		numberOfComponentConfigs uint
	}

	testcases := []struct {
		name     string
		args     args
		expected expected
	}{
		{
			name: "Simple component config",
			args: args{
				checkinExpectedGenerator: func(observed *proto.CheckinObserved) *proto.CheckinExpected {

					expectedComponentIdx := uint64(1)

					if observed.ComponentIdx != expectedComponentIdx {

						return &proto.CheckinExpected{
							ComponentIdx: expectedComponentIdx,
							Component: &proto.Component{
								Processors: &proto.GlobalProcessorsConfig{
									Configs: map[string]*proto.ProcessorConfig{
										"provider1": {Enabled: true, Config: mustStructFromMap(t, nil)},
									},
								},
							},
							Units: []*proto.UnitExpected{},
						}
					}
					// disconnect otherwise
					return nil
				},
			},
			expected: expected{
				componentExpected: &proto.Component{
					Processors: &proto.GlobalProcessorsConfig{
						Configs: map[string]*proto.ProcessorConfig{
							"provider1": {Enabled: true, Config: mustStructFromMap(t, nil)},
						},
					},
				},
				expectedComponentIdx:     1,
				unitsExpected:            nil,
				numberOfComponentConfigs: 1,
			},
		},
	}

	for _, tt := range testcases {
		t.Run(tt.name, func(t *testing.T) {
			srv := mock.StubServerV2{
				CheckinV2Impl: tt.args.checkinExpectedGenerator,
				ActionImpl: func(response *proto.ActionResponse) error {
					// actions not tested here
					t.Logf("handling action response %v", response)
					return nil
				},
				ActionsChan: make(chan *mock.PerformAction, 100),
			}
			require.NoError(t, srv.Start())
			defer srv.Stop()

			var errsMu sync.Mutex
			var errs []error
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			serverAddr := fmt.Sprintf(":%d", srv.Port)
			token := mock.NewID()
			v2Client := NewV2(serverAddr, token, VersionInfo{}, WithGRPCDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())), WithEmitComponentChanges(true)).(*clientV2)
			v2Client.minCheckTimeout = 100 * time.Millisecond // otherwise the test will run for too long
			storeErrors(ctx, v2Client, &errs, &errsMu)
			require.NoError(t, v2Client.Start(ctx))
			defer v2Client.Stop()

			componentConfigsReceived := atomic.MakeUint(0)

			go func() {
				t.Log("consumer goroutine started...")
				for {
					select {
					case <-ctx.Done():
						t.Log("consumer goroutine exiting...")
						return
					case cc := <-v2Client.ComponentChanges():
						t.Logf("component received: %+v", cc)
						componentConfigsReceived.Inc()
					case <-v2Client.UnitChanges(): // otherwise the v2Client can block forever
						// we don't need to react to units since we test the component-level change
						t.Logf("unit received")
					}
				}
			}()

			// wait until we processed the target componentIdx
			assert.Eventually(t, func() bool {
				return v2Client.componentIdx == tt.expected.expectedComponentIdx
			}, 10*time.Second, 500*time.Millisecond, "didn't process up to the expected componentIdx")

			assert.Equal(t, tt.expected.numberOfComponentConfigs, componentConfigsReceived.Load(), "didn't receive the expected number of component configs")
			assert.True(t, gproto.Equal(tt.expected.componentExpected, v2Client.componentConfig), "last component stored by client \n%s\n is different from expected\n%s", v2Client.componentConfig, tt.expected.componentExpected)
		})
	}
}

func setupClientForDiagnostics(ctx context.Context, t *testing.T) (*Unit, V2, mock.StubServerV2) {
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
			// sent on initial startup connection
			if response.Id == "init" {
				gotInit = true
			}
			return nil
		},
		ActionsChan: make(chan *mock.PerformAction, 100),
		SentActions: make(map[string]*mock.PerformAction),
	}
	require.NoError(t, srv.Start())

	var errsMu sync.Mutex
	var errs []error
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, WithGRPCDialOptions(grpc.WithTransportCredentials(insecure.NewCredentials())))
	storeErrors(context.Background(), client, &errs, &errsMu)

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

	return unit, client, srv
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

func mustStructFromMap(t *testing.T, m map[string]any) *structpb.Struct {
	newStruct, err := structpb.NewStruct(m)
	require.NoErrorf(t, err, "unable to create struct from %s", m)
	return newStruct
}
