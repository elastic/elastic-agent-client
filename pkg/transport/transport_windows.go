// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build windows
// +build windows

package transport

import (
	"net"

	"github.com/Microsoft/go-winio"
)

// Listen returns net.Listener from net.Listen or winio.ListenPipe
// depending on the network value.
// The network must be "tcp", "tcp4", "tcp6", "unix", "unixpacket" for net.Listen
// or "pipe" for winio.ListenPipe.
// The "address" argument for windows pipe would be something like \\.\pipe\elastic_agent
func Listen(network, address string) (net.Listener, error) {
	if network == "pipe" {
		return winio.ListenPipe(address, nil)
	}
	return net.Listen(network, address)
}
