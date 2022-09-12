// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package server

import (
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/elastic/elastic-agent-client/v7/dev-tools/v2tool/manager"
	"github.com/elastic/elastic-agent-client/v7/pkg/client/mock"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/elastic/elastic-agent-libs/logp"
	"github.com/gofrs/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	protobuf "google.golang.org/protobuf/proto"
)

// Tool carries the main management state for the application, including both the V2 server runtime
type Tool struct {
	ca         *CertificateAuthority
	pair       *Pair
	srv        mock.StubServerV2
	manager    *manager.InputManager
	serverName string
}

// NewToolServer initializes a new tool server instance
func NewToolServer(inputMgr *manager.InputManager) (*Tool, error) {
	serverName, err := genServerName()
	if err != nil {
		return nil, fmt.Errorf("error generating server name: %w", err)
	}

	ca, pair, err := generateCreds(serverName)
	if err != nil {
		return nil, fmt.Errorf("error generating credentials: %w", err)
	}

	var mut sync.Mutex

	start := time.Now()
	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			mut.Lock()
			defer mut.Unlock()
			return inputMgr.Checkin(observed, start)
		},
		ActionImpl: func(response *proto.ActionResponse) error {
			return nil
		},
		ActionsChan: make(chan *mock.PerformAction, 100),
	}

	tool := &Tool{
		ca:         ca,
		pair:       pair,
		srv:        srv,
		serverName: serverName,
		manager:    inputMgr,
	}

	return tool, nil
}

// StartServer starts the V2 mock server and writes the connection info to the client
func (tool *Tool) StartServer() error {
	log := logp.L()
	cert, err := tls.X509KeyPair(tool.pair.Crt, tool.pair.Key)
	if err != nil {
		return fmt.Errorf("error generating X509 keypair: %w", err)
	}
	creds := credentials.NewServerTLSFromCert(&cert)

	err = tool.srv.Start(grpc.Creds(creds))
	if err != nil {
		return fmt.Errorf("Error starting server: %w", err)
	}
	log.Debugf("Started V2 server")

	err = tool.writeConnInfo()
	if err != nil {
		return fmt.Errorf("Error writing connection info for process: %w", err)
	}
	log.Debugf("Wrote config to client")
	return nil
}

// writeConnInfo writes the GRPC connection info to the running client
func (tool *Tool) writeConnInfo() error {
	log := logp.L()
	services := []proto.ConnInfoServices{proto.ConnInfoServices_CheckinV2}
	token, err := uuid.NewV4()
	if err != nil {
		return fmt.Errorf("error generating token: %w", err)
	}

	addr := fmt.Sprintf(":%d", tool.srv.Port)
	connInfo := &proto.ConnInfo{
		Addr:       addr,
		ServerName: tool.serverName,
		Token:      token.String(),
		CaCert:     tool.ca.Crt(),
		PeerCert:   tool.pair.Crt,
		PeerKey:    tool.pair.Key,
		Services:   services,
	}
	log.Debugf("Creating config for client with address %s and services: %v", connInfo.Addr, connInfo.Services)

	infoBytes, err := protobuf.Marshal(connInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal connection information: %w", err)
	}

	err = tool.manager.WriteToClient(infoBytes)
	if err != nil {
		return fmt.Errorf("error writing connection info to client: %w", err)
	}

	return nil
}

func generateCreds(serverName string) (*CertificateAuthority, *Pair, error) {
	ca, err := NewCA()
	if err != nil {
		return nil, nil, fmt.Errorf("Error generatint CA certs: %w", err)
	}
	pair, err := ca.GeneratePairWithName(serverName)
	if err != nil {
		return nil, nil, fmt.Errorf("error generating cert pair: %w", err)
	}

	return ca, pair, nil
}

func genServerName() (string, error) {
	u, err := uuid.NewV4()
	if err != nil {
		return "", err
	}
	return strings.Replace(u.String(), "-", "", -1), nil
}
