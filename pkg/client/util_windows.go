// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build windows
// +build windows

package client

// transformNPipeUrl converts npipe:/// url to the local named pipe name
func transformNPipeUrl(s string) string {
	return npipe.TransformString(s)
}
