// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFileClientRead(t *testing.T) {
	filename := "mock/file-client-agent.yml"
	client, err := newV2Reader(filename)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	err = client.Start(ctx)
	require.NoError(t, err)

	gotOutputUnit := false
	gotInputUnit := false
	unitWait := sync.WaitGroup{}
	unitWait.Add(1)

	go func() {

		gotUnits := 0
		t.Logf("waiting for response from client")
		for {
			select {
			case _ = <-ctx.Done():
				t.Logf("got context done")
				unitWait.Done()
				return

			case unit := <-client.UnitChanges():
				_, _, cfg := unit.Unit.Expected()

				// extra test: make sure we don't get any random nil pointer panics from us disabling things
				_, actionFound := unit.Unit.GetAction("test")
				require.False(t, actionFound)
				storeC := unit.Unit.Store()
				_, err = storeC.BeginTx(ctx, false)
				require.Error(t, err)

				// make sure we have all the expected units
				if key, ok := cfg.GetSource().AsMap()["username"]; ok {
					require.Equal(t, "elastic", key)
					gotOutputUnit = true
					gotUnits++
				}
				if key, ok := cfg.GetSource().AsMap()["type"]; ok && unit.Unit.Type() == UnitTypeInput {
					require.Equal(t, "system/metrics", key)
					gotInputUnit = true
					gotUnits++
				}
				if gotUnits == 2 {
					t.Logf("got both units")
					unitWait.Done()
					return
				}
			}
		}
	}()

	unitWait.Wait()
	require.True(t, gotOutputUnit)
	require.True(t, gotInputUnit)

	client.Stop()

}

func TestFileClientDisabledFeatures(t *testing.T) {
	filename := "mock/file-client-agent.yml"
	client, err := newV2Reader(filename)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	err = client.Start(ctx)
	require.NoError(t, err)

	// make sure that none of these explode

	agentInfo := client.AgentInfo()
	require.NotNil(t, agentInfo)

	artifacts := client.Artifacts()
	_, err = artifacts.Fetch(ctx, "test", "test")
	require.NoError(t, err)

}
