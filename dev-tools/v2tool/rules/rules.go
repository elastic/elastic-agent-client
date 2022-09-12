// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package rules

import (
	"time"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

// Checker is the main interface used by the server runtime to decide
// if a given unit needs to be stopped or started
type Checker interface {
	Check(time.Time, *proto.UnitObserved) bool
}

// OnStart starts the unit during initial checkin
type OnStart struct {
}

// Check to see if the unit is ready to start or stop
func (m OnStart) Check(_ time.Time, observed *proto.UnitObserved) bool {
	// on first checkin, the V2 server will get a nil value
	if observed == nil {
		return true
	}
	return false
}

// After starts the unit after a given duration of seconds
type After struct {
	Time time.Duration `config:"time"`
}

// Check to see if the unit is ready to start or stop
func (m After) Check(startTime time.Time, _ *proto.UnitObserved) bool {
	if time.Since(startTime) > m.Time {
		return true
	}
	return false
}
