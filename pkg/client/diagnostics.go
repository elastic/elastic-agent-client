// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

// DiagnosticHook is a function that returns content for a registered diagnostic hook.
type DiagnosticHook func() []byte

// diagHook carries the backend registeration for a diagnostic hook.
type diagHook struct {
	description string
	filename    string
	contentType string
	hook        DiagnosticHook
	// if not empty, the diagnostic will not be run unless the underlying action contains this tag in DiagnosticParams
	optionalWithParamTag string
}

// DiagnosticParams is an optional JSON field that can be sent in the `params` field
// of a diagnostic action request.
type DiagnosticParams struct {
	AdditionalMetrics []string `json:"additional_metrics"`
}
