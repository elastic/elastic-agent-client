// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build windows
// +build windows

package mock

import (
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
)

func newNPipeListener(name, sd string) (net.Listener, error) {
	return newListener(name, sd)
}

func newListener(name, sd string) (net.Listener, error) {
	c := &winio.PipeConfig{
		SecurityDescriptor: sd,
	}

	l, err := winio.ListenPipe(name, c)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on the named pipe %s: %w", name, err)
	}

	return l, nil
}
