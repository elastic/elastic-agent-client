// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build windows
// +build windows

package client

import "strings"

// transformNPipeURL converts npipe:/// url to the local named pipe name
func transformNPipeURL(s string) string {
	return transformString(s)
}

func transformString(name string) string {
	if strings.HasPrefix(name, "npipe:///") {
		path := strings.TrimPrefix(name, "npipe:///")
		return `\\.\pipe\` + path
	}

	return name
}
