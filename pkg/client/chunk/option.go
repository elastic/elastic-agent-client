// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package chunk

import "time"

type options struct {
	timestamp time.Time
}

// Option is an option for adjusting chunking.
type Option func(opts *options)

// WithTimestamp adjusts the timestamp used for the chunking.
//
// Note: Mainly used for testing to ensure a specific timestamp is used.
func WithTimestamp(t time.Time) Option {
	return func(opts *options) {
		opts.timestamp = t
	}
}
