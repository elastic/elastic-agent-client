// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.
package client

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

var defaultTest = Unit{
	expectedState: UnitStateHealthy,
	logLevel:      UnitLogLevelDebug,
	configIdx:     1,
	config:        &proto.UnitExpectedConfig{},
}

func TestUnitUpdateWithSameMap(t *testing.T) {
	defaultTest.configIdx = 1
	sameMap := map[string]interface{}{"username": "test"}
	pbStruct, err := structpb.NewStruct(sameMap)
	require.NoError(t, err)
	defaultTest.config.Source = pbStruct

	pbStructNew, err := structpb.NewStruct(sameMap)
	newUnit := &proto.UnitExpectedConfig{
		Source: pbStructNew,
	}
	// Marshal the message to populate the size cache and ensure the internal proto files are populated.
	_, err = gproto.Marshal(newUnit)
	require.NoError(t, err)

	// This should return TriggeredNothing, as the two underlying maps in `source` are the same
	got := defaultTest.updateState(UnitStateHealthy, UnitLogLevelDebug, newUnit, 2)
	assert.Equal(t, TriggeredNothing, got)
}

func TestUnitUpdateWithNewMap(t *testing.T) {
	defaultTest.configIdx = 1
	pbStruct, err := structpb.NewStruct(map[string]interface{}{"username": "test"})
	require.NoError(t, err)
	defaultTest.config.Source = pbStruct

	pbStructNew, err := structpb.NewStruct(map[string]interface{}{"username": "other"})
	require.NoError(t, err)
	newUnit := &proto.UnitExpectedConfig{
		Source: pbStructNew,
	}

	_, err = gproto.Marshal(newUnit)
	require.NoError(t, err)

	// This should return TriggeredConfigChange, as we have an actually new map
	got := defaultTest.updateState(UnitStateHealthy, UnitLogLevelDebug, newUnit, 2)
	assert.Equal(t, TriggeredConfigChange, got)
}

func TestUnitUpdateLog(t *testing.T) {
	got := defaultTest.updateState(UnitStateHealthy, UnitLogLevelInfo, &proto.UnitExpectedConfig{}, 2)
	assert.Equal(t, TriggeredLogLevelChange, got)
}

func TestUnitUpdateState(t *testing.T) {
	got := defaultTest.updateState(UnitStateStopped, UnitLogLevelInfo, &proto.UnitExpectedConfig{}, 2)
	assert.Equal(t, TriggeredStateChange, got)
}
