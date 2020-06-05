// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package healthcheck

import (
	"context"
	"time"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

type healthcheckCallback func(context.Context) error
type statusUpdater func(status proto.StateObserved_Status, message string)

var defaultHealthCheck = &defaultHealthChecker{}

// Register register a healthcheck callback.
func Register(hc healthcheckCallback) {
	defaultHealthCheck.Register(hc)
}

// Start starts the execution of health checks.
func Start(period time.Duration, updateFn statusUpdater) {
	defaultHealthCheck.Start(period, updateFn)
}

// Stop stops the execution of health checks.
func Stop() {
	defaultHealthCheck.Stop()
}
