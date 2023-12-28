// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build !windows
// +build !windows

package client

// transformNPipeURL is noop on non-windows platforms.
// The npipe.TransformString is windows only in github.com/elastic/elastic-agent-libs/api/npipe
func transformNPipeURL(s string) string {
	return s
}
