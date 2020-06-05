// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package healthcheck

// ErrFatal is a fatal error.
type ErrFatal struct {
	msg string
}

// NewFatalFromError creates fatal error from error.
func NewFatalFromError(err error) ErrFatal {
	return ErrFatal{err.Error()}
}

// NewFatal creates fatal error with message.
func NewFatal(msg string) ErrFatal {
	return ErrFatal{msg}
}

func (e ErrFatal) Error() string {
	return e.msg
}

var _ error = &ErrFatal{}
