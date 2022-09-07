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

func (m After) Check(startTime time.Time, _ *proto.UnitObserved) bool {
	if time.Since(startTime) > m.Time {
		return true
	}
	return false
}
