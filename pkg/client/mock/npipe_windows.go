// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build windows
// +build windows

package mock

import (
	"net"

	"github.com/elastic/elastic-agent-libs/api/npipe"
)

func newNPipeListener(name, sd string) (net.Listener, error) {
	return npipe.NewListener(name, sd)
}
