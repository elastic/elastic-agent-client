// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

// DiagnosticHook is a function that returns content for a registered diagnostic hook.
type DiagnosticHook func() []byte

type diagHook struct {
	description string
	filename    string
	contentType string
	hook        DiagnosticHook
}
