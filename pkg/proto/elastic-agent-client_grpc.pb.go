// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.2.0
// - protoc             v4.23.4
// source: elastic-agent-client.proto

package proto

import (
	context "context"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// ElasticAgentClient is the client API for ElasticAgent service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type ElasticAgentClient interface {
	// Called by the client to provide the Elastic Agent the state of the application over the V2
	// protocol.
	//
	// Implements a reconciliation loop where a component periodically tells the agent what its
	// current observed configuration is, and the agent replies with the configuration it is
	// expected to be running.
	//
	// Each configuration block included in the expected message is accompanied by an index or
	// revision number. Corresponding observed messages do not need to waste CPU copying the entire
	// applied configuration back to the agent on each checkin; instead, they can simply echo back
	// the index or revision number from the expected message upon successful reconciliation.
	// Configurations in large deployments can be 1MB or more.
	//
	// A `CheckinObserved` must be streamed at least every 30 seconds or it will result in the set
	// of units automatically marked as FAILED. After several missed checkins the Elastic Agent will
	// force kill the entire process and restart it.
	//
	// The V2 protocol is designed to operate knowing as little as possible about the units and
	// components it communicates with. Each unit or component can accept arbitrary user
	// configuration from the agent policy which is encoded in a `google.protobuf.Struct source`
	// field. The agent does not fully parse or inspect the contents of the source field and
	// passes it through to components unmodified.
	//
	// Use of the source field allows the input configurations to evolve without needing to modify
	// the control protocol itself. In some cases commonly used or important fields are extracted as
	// a dedicated message type, but these definitions do not completely define the contents of the
	// source field which is free to contain additional fields.
	CheckinV2(ctx context.Context, opts ...grpc.CallOption) (ElasticAgent_CheckinV2Client, error)
	// Called by the client after receiving connection info to allow the Elastic Agent to stream action
	// requests to the application and the application stream back responses to those requests.
	//
	// Request and response is swapped here because the Elastic Agent sends the requests in a stream
	// to the connected process. The order of response from the process does not matter, it is acceptable
	// for the response order to be different then the request order.
	Actions(ctx context.Context, opts ...grpc.CallOption) (ElasticAgent_ActionsClient, error)
	// DEPRECATED: DO NOT USE
	//
	// Called by the client to provide the Elastic Agent the state of the application.
	//
	// A `StateObserved` must be streamed at least every 30 seconds or it will result in the
	// application be automatically marked as FAILED, and after 60 seconds the Elastic Agent will
	// force kill the entire process and restart it.
	//
	// Messages definitions are preserved in elastic-agent-client-deprecated.proto.
	Checkin(ctx context.Context, opts ...grpc.CallOption) (ElasticAgent_CheckinClient, error)
}

type elasticAgentClient struct {
	cc grpc.ClientConnInterface
}

func NewElasticAgentClient(cc grpc.ClientConnInterface) ElasticAgentClient {
	return &elasticAgentClient{cc}
}

func (c *elasticAgentClient) CheckinV2(ctx context.Context, opts ...grpc.CallOption) (ElasticAgent_CheckinV2Client, error) {
	stream, err := c.cc.NewStream(ctx, &ElasticAgent_ServiceDesc.Streams[0], "/proto.ElasticAgent/CheckinV2", opts...)
	if err != nil {
		return nil, err
	}
	x := &elasticAgentCheckinV2Client{stream}
	return x, nil
}

type ElasticAgent_CheckinV2Client interface {
	Send(*CheckinObserved) error
	Recv() (*CheckinExpected, error)
	grpc.ClientStream
}

type elasticAgentCheckinV2Client struct {
	grpc.ClientStream
}

func (x *elasticAgentCheckinV2Client) Send(m *CheckinObserved) error {
	return x.ClientStream.SendMsg(m)
}

func (x *elasticAgentCheckinV2Client) Recv() (*CheckinExpected, error) {
	m := new(CheckinExpected)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *elasticAgentClient) Actions(ctx context.Context, opts ...grpc.CallOption) (ElasticAgent_ActionsClient, error) {
	stream, err := c.cc.NewStream(ctx, &ElasticAgent_ServiceDesc.Streams[1], "/proto.ElasticAgent/Actions", opts...)
	if err != nil {
		return nil, err
	}
	x := &elasticAgentActionsClient{stream}
	return x, nil
}

type ElasticAgent_ActionsClient interface {
	Send(*ActionResponse) error
	Recv() (*ActionRequest, error)
	grpc.ClientStream
}

type elasticAgentActionsClient struct {
	grpc.ClientStream
}

func (x *elasticAgentActionsClient) Send(m *ActionResponse) error {
	return x.ClientStream.SendMsg(m)
}

func (x *elasticAgentActionsClient) Recv() (*ActionRequest, error) {
	m := new(ActionRequest)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *elasticAgentClient) Checkin(ctx context.Context, opts ...grpc.CallOption) (ElasticAgent_CheckinClient, error) {
	stream, err := c.cc.NewStream(ctx, &ElasticAgent_ServiceDesc.Streams[2], "/proto.ElasticAgent/Checkin", opts...)
	if err != nil {
		return nil, err
	}
	x := &elasticAgentCheckinClient{stream}
	return x, nil
}

type ElasticAgent_CheckinClient interface {
	Send(*StateObserved) error
	Recv() (*StateExpected, error)
	grpc.ClientStream
}

type elasticAgentCheckinClient struct {
	grpc.ClientStream
}

func (x *elasticAgentCheckinClient) Send(m *StateObserved) error {
	return x.ClientStream.SendMsg(m)
}

func (x *elasticAgentCheckinClient) Recv() (*StateExpected, error) {
	m := new(StateExpected)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// ElasticAgentServer is the server API for ElasticAgent service.
// All implementations must embed UnimplementedElasticAgentServer
// for forward compatibility
type ElasticAgentServer interface {
	// Called by the client to provide the Elastic Agent the state of the application over the V2
	// protocol.
	//
	// Implements a reconciliation loop where a component periodically tells the agent what its
	// current observed configuration is, and the agent replies with the configuration it is
	// expected to be running.
	//
	// Each configuration block included in the expected message is accompanied by an index or
	// revision number. Corresponding observed messages do not need to waste CPU copying the entire
	// applied configuration back to the agent on each checkin; instead, they can simply echo back
	// the index or revision number from the expected message upon successful reconciliation.
	// Configurations in large deployments can be 1MB or more.
	//
	// A `CheckinObserved` must be streamed at least every 30 seconds or it will result in the set
	// of units automatically marked as FAILED. After several missed checkins the Elastic Agent will
	// force kill the entire process and restart it.
	//
	// The V2 protocol is designed to operate knowing as little as possible about the units and
	// components it communicates with. Each unit or component can accept arbitrary user
	// configuration from the agent policy which is encoded in a `google.protobuf.Struct source`
	// field. The agent does not fully parse or inspect the contents of the source field and
	// passes it through to components unmodified.
	//
	// Use of the source field allows the input configurations to evolve without needing to modify
	// the control protocol itself. In some cases commonly used or important fields are extracted as
	// a dedicated message type, but these definitions do not completely define the contents of the
	// source field which is free to contain additional fields.
	CheckinV2(ElasticAgent_CheckinV2Server) error
	// Called by the client after receiving connection info to allow the Elastic Agent to stream action
	// requests to the application and the application stream back responses to those requests.
	//
	// Request and response is swapped here because the Elastic Agent sends the requests in a stream
	// to the connected process. The order of response from the process does not matter, it is acceptable
	// for the response order to be different then the request order.
	Actions(ElasticAgent_ActionsServer) error
	// DEPRECATED: DO NOT USE
	//
	// Called by the client to provide the Elastic Agent the state of the application.
	//
	// A `StateObserved` must be streamed at least every 30 seconds or it will result in the
	// application be automatically marked as FAILED, and after 60 seconds the Elastic Agent will
	// force kill the entire process and restart it.
	//
	// Messages definitions are preserved in elastic-agent-client-deprecated.proto.
	Checkin(ElasticAgent_CheckinServer) error
	mustEmbedUnimplementedElasticAgentServer()
}

// UnimplementedElasticAgentServer must be embedded to have forward compatible implementations.
type UnimplementedElasticAgentServer struct {
}

func (UnimplementedElasticAgentServer) CheckinV2(ElasticAgent_CheckinV2Server) error {
	return status.Errorf(codes.Unimplemented, "method CheckinV2 not implemented")
}
func (UnimplementedElasticAgentServer) Actions(ElasticAgent_ActionsServer) error {
	return status.Errorf(codes.Unimplemented, "method Actions not implemented")
}
func (UnimplementedElasticAgentServer) Checkin(ElasticAgent_CheckinServer) error {
	return status.Errorf(codes.Unimplemented, "method Checkin not implemented")
}
func (UnimplementedElasticAgentServer) mustEmbedUnimplementedElasticAgentServer() {}

// UnsafeElasticAgentServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to ElasticAgentServer will
// result in compilation errors.
type UnsafeElasticAgentServer interface {
	mustEmbedUnimplementedElasticAgentServer()
}

func RegisterElasticAgentServer(s grpc.ServiceRegistrar, srv ElasticAgentServer) {
	s.RegisterService(&ElasticAgent_ServiceDesc, srv)
}

func _ElasticAgent_CheckinV2_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(ElasticAgentServer).CheckinV2(&elasticAgentCheckinV2Server{stream})
}

type ElasticAgent_CheckinV2Server interface {
	Send(*CheckinExpected) error
	Recv() (*CheckinObserved, error)
	grpc.ServerStream
}

type elasticAgentCheckinV2Server struct {
	grpc.ServerStream
}

func (x *elasticAgentCheckinV2Server) Send(m *CheckinExpected) error {
	return x.ServerStream.SendMsg(m)
}

func (x *elasticAgentCheckinV2Server) Recv() (*CheckinObserved, error) {
	m := new(CheckinObserved)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func _ElasticAgent_Actions_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(ElasticAgentServer).Actions(&elasticAgentActionsServer{stream})
}

type ElasticAgent_ActionsServer interface {
	Send(*ActionRequest) error
	Recv() (*ActionResponse, error)
	grpc.ServerStream
}

type elasticAgentActionsServer struct {
	grpc.ServerStream
}

func (x *elasticAgentActionsServer) Send(m *ActionRequest) error {
	return x.ServerStream.SendMsg(m)
}

func (x *elasticAgentActionsServer) Recv() (*ActionResponse, error) {
	m := new(ActionResponse)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func _ElasticAgent_Checkin_Handler(srv interface{}, stream grpc.ServerStream) error {
	return srv.(ElasticAgentServer).Checkin(&elasticAgentCheckinServer{stream})
}

type ElasticAgent_CheckinServer interface {
	Send(*StateExpected) error
	Recv() (*StateObserved, error)
	grpc.ServerStream
}

type elasticAgentCheckinServer struct {
	grpc.ServerStream
}

func (x *elasticAgentCheckinServer) Send(m *StateExpected) error {
	return x.ServerStream.SendMsg(m)
}

func (x *elasticAgentCheckinServer) Recv() (*StateObserved, error) {
	m := new(StateObserved)
	if err := x.ServerStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

// ElasticAgent_ServiceDesc is the grpc.ServiceDesc for ElasticAgent service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var ElasticAgent_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "proto.ElasticAgent",
	HandlerType: (*ElasticAgentServer)(nil),
	Methods:     []grpc.MethodDesc{},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "CheckinV2",
			Handler:       _ElasticAgent_CheckinV2_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
		{
			StreamName:    "Actions",
			Handler:       _ElasticAgent_Actions_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
		{
			StreamName:    "Checkin",
			Handler:       _ElasticAgent_Checkin_Handler,
			ServerStreams: true,
			ClientStreams: true,
		},
	},
	Metadata: "elastic-agent-client.proto",
}
