// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build windows
// +build windows

package client

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
	"google.golang.org/grpc"
)

func getOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
			return winio.DialPipeContext(ctx, s)
		}),
	}
}
