// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package chunk

import "time"

const defaultRepeatPadding = 3 // 3 bytes

type options struct {
	timestamp     time.Time
	repeatPadding int
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

// WithRepeatPadding adjusts the padding used on each repeated structure.
//
// Note: Mainly used for testing to validate that without padding that message
// size will be too large.
func WithRepeatPadding(padding int) Option {
	return func(opts *options) {
		opts.repeatPadding = padding
	}
}
