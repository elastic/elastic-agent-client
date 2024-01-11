// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"io/ioutil"

	protobuf "github.com/golang/protobuf/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

// ErrV2Unavailable error returned when Elastic Agent doesn't support V2.
var ErrV2Unavailable = errors.New("v2 protocol is not available")

// Service defined different services that the Elastic Agent states is available.
type Service proto.ConnInfoServices

const (
	// ServiceCheckin V1 checkin service is available.
	ServiceCheckin = Service(proto.ConnInfoServices_Checkin)
	// ServiceCheckinV2 V2 checkin service is available.
	ServiceCheckinV2 Service = Service(proto.ConnInfoServices_CheckinV2)
	// ServiceStore store service is available.
	ServiceStore Service = Service(proto.ConnInfoServices_Store)
	// ServiceArtifact artifact service is available.
	ServiceArtifact Service = Service(proto.ConnInfoServices_Artifact)
	// ServiceLog log service is available.
	ServiceLog Service = Service(proto.ConnInfoServices_Log)
)

// NewFromReader creates a new client reading the connection information from the io.Reader.
func NewFromReader(reader io.Reader, impl StateInterface, actions ...Action) (Client, error) {
	connInfo := &proto.ConnInfo{}
	data, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	err = protobuf.Unmarshal(data, connInfo)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(connInfo.PeerCert, connInfo.PeerKey)
	if err != nil {
		return nil, err
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(connInfo.CaCert)
	trans := credentials.NewTLS(&tls.Config{
		ServerName:   connInfo.ServerName,
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	})
	return New(connInfo.Addr, connInfo.Token, impl, actions, grpc.WithTransportCredentials(trans)), nil
}

// NewV2FromReader creates a new V2 client reading the connection information from the io.Reader.
func NewV2FromReader(reader io.Reader, ver VersionInfo, opts ...V2ClientOption) (V2, []Service, error) {
	info := &proto.StartupInfo{}
	data, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, nil, err
	}
	err = protobuf.Unmarshal(data, info)
	if err != nil {
		return nil, nil, err
	}

	if info.AgentInfo!=nil{
		ver.Version=info.AgentInfo.Version
	}

	if info.ConnInfo.Services == nil {
		return nil, []Service{ServiceCheckin}, ErrV2Unavailable
	}
	cert, err := tls.X509KeyPair(info.ConnInfo.PeerCert, info.ConnInfo.PeerKey)
	if err != nil {
		return nil, nil, err
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(info.ConnInfo.CaCert)
	trans := credentials.NewTLS(&tls.Config{
		ServerName:   info.ConnInfo.ServerName,
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	})
	for _, s := range info.ConnInfo.Supports {
		if s == proto.ConnectionSupports_CheckinChunking {
			opts = append(opts, WithChunking(true))
		}
	}
	if info.ConnInfo.MaxMessageSize > 0 {
		opts = append(opts, WithMaxMessageSize(int(info.ConnInfo.MaxMessageSize)))
	}
	opts = append(opts, WithGRPCDialOptions(grpc.WithTransportCredentials(trans)))
	client := NewV2(
		info.ConnInfo.Addr,
		info.ConnInfo.Token,
		ver,
		opts...,
	)
	services := make([]Service, 0, len(info.ConnInfo.Services))
	for _, srv := range info.ConnInfo.Services {
		services = append(services, Service(srv))
	}
	return client, services, nil
}
