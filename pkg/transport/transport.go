// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build !windows
// +build !windows

package transport

import "net"

// Listen returns net.Listener from net.Listen
func Listen(network, address string) (net.Listener, error) {
	return net.Listen(network, address)
}
