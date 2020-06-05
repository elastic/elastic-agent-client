// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package healthcheck

import (
	"context"
	"sync"
	"time"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/hashicorp/go-multierror"
)

type defaultHealthChecker struct {
	hcLock       sync.Mutex
	healthchecks []healthcheckCallback
	ctx          context.Context
	cancel       context.CancelFunc
	isRunning    bool
}

// Register register a healthcheck callback.
func (d *defaultHealthChecker) Register(hc healthcheckCallback) {
	d.hcLock.Lock()
	defer d.hcLock.Unlock()

	if d.healthchecks == nil {
		d.healthchecks = make([]healthcheckCallback, 0, 1)
	}

	d.healthchecks = append(d.healthchecks, hc)
}

func (d *defaultHealthChecker) Start(period time.Duration, updateFn statusUpdater) {
	d.hcLock.Lock()
	defer d.hcLock.Unlock()

	// run only one instance
	if d.isRunning {
		return
	}

	d.isRunning = true
	d.ctx, d.cancel = context.WithCancel(context.Background())

	go d.work(period, updateFn)
}

func (d *defaultHealthChecker) Stop() {
	d.hcLock.Lock()
	defer d.hcLock.Unlock()

	// if stopped then ok
	if !d.isRunning {
		return
	}

	d.isRunning = false
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
}

func (d *defaultHealthChecker) work(period time.Duration, updateFn statusUpdater) {
	// run checks every X not with X as a pause period
	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.executeHealthChecks(updateFn)
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *defaultHealthChecker) executeHealthChecks(updateFn statusUpdater) {
	reportedStatus := proto.StateObserved_HEALTHY
	var errors error

	// enable register/start/stop operations in between health checks
	d.hcLock.Lock()
	hcc := make([]healthcheckCallback, len(d.healthchecks))
	copy(hcc, d.healthchecks)
	ctx := d.ctx
	d.hcLock.Unlock()

	for _, hc := range hcc {
		// healthchecks stopped
		// return without reporting state, it might collide
		// with another loop
		if ctx.Err() != nil {
			return
		}

		if err := hc(ctx); err != nil {
			errors = multierror.Append(err, errors)

			if _, ok := err.(ErrFatal); ok {
				reportedStatus = proto.StateObserved_FAILED
			} else if reportedStatus == proto.StateObserved_HEALTHY {
				reportedStatus = proto.StateObserved_DEGRADED
			}
		}
	}

	updateFn(reportedStatus, errors.Error())
}
