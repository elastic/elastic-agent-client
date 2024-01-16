// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build windows
// +build windows

package client

import (
	"context"
	"net"

	"github.com/elastic/elastic-agent-libs/api/npipe"
	"google.golang.org/grpc"
)

func getOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, s string) (net.Conn, error) {
			// Awkward call of what's exposed from elastic-agent-libs/api/npipe
			// in order to replace the direct use of winio.DialPipeContext
			// elastic-agent-libs/api/npipe does the same under the hood anyways
			return npipe.DialContext(s)(ctx, "", "")
		}),
	}
}
