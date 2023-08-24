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
	featuresIdx:   0,
	features:      nil,
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
	got := defaultTest.updateState(UnitStateHealthy, UnitLogLevelDebug, 0, nil, newUnit, 2, nil)
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
	got := defaultTest.updateState(UnitStateHealthy, UnitLogLevelDebug, 0, nil, newUnit, 2, nil)
	assert.Equal(t, TriggeredConfigChange, got)
}

func TestUnitUpdateLog(t *testing.T) {
	got := defaultTest.updateState(UnitStateHealthy, UnitLogLevelInfo, 0, nil, &proto.UnitExpectedConfig{}, 2, nil)
	assert.Equal(t, TriggeredLogLevelChange, got)
}

func TestUnitUpdateFeatureFlags(t *testing.T) {
	got := defaultTest.updateState(UnitStateHealthy, UnitLogLevelInfo, 1, &proto.Features{}, &proto.UnitExpectedConfig{}, 2, nil)
	assert.Equal(t, TriggeredFeatureChange, got)
}

func TestUnitUpdateState(t *testing.T) {
	got := defaultTest.updateState(UnitStateStopped, UnitLogLevelInfo, 1, &proto.Features{}, &proto.UnitExpectedConfig{}, 2, nil)
	assert.Equal(t, TriggeredStateChange, got)
}

func TestUnitUpdateAPMConfig(t *testing.T) {

	var initialUnitNoAPM = Unit{
		expectedState: UnitStateHealthy,
		logLevel:      UnitLogLevelDebug,
		featuresIdx:   0,
		features:      nil,
		configIdx:     1,
		config:        &proto.UnitExpectedConfig{},
		apm:           nil,
	}

	notEmptyAPMCfg := &proto.APMConfig{
		Elastic: &proto.ElasticAPM{
			Environment: "test",
			ApiKey:      "apikey",
			SecretToken: "",
			Hosts:       []string{"host1", "host2"},
			Tls: &proto.ElasticAPMTLS{
				SkipVerify: false,
				ServerCert: "/path/to/server/cert",
				ServerCa:   "/path/to/server/ca",
			},
		},
	}

	type testcase struct {
		name         string
		initialState Unit
		update       struct {
			//unitState   UnitState
			//UnitLogLvl  UnitLogLevel
			//featuresIdx uint64
			//features    *proto.Features
			//unitConfig  *proto.UnitExpectedConfig
			//cfgIdx      uint64
			apmConfig *proto.APMConfig
		}
		triggeredFlags Trigger
	}
	testcases := []testcase{
		{
			name:         "Updated APM config from nil",
			initialState: initialUnitNoAPM,
			update: struct {
				apmConfig *proto.APMConfig
			}{apmConfig: notEmptyAPMCfg},
			triggeredFlags: TriggeredAPMChange,
		},
		{
			name:         "Updated APM config from nil with empty",
			initialState: initialUnitNoAPM,
			update: struct {
				apmConfig *proto.APMConfig
			}{
				apmConfig: &proto.APMConfig{},
			},
			triggeredFlags: TriggeredAPMChange,
		},
		{
			name:         "Updated APM config from nil with nil",
			initialState: initialUnitNoAPM,
			update: struct {
				apmConfig *proto.APMConfig
			}{
				apmConfig: nil,
			},
			triggeredFlags: TriggeredNothing,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {

			got := tc.initialState.updateState(
				tc.initialState.Expected().State,
				tc.initialState.Expected().LogLevel,
				tc.initialState.featuresIdx,
				tc.initialState.Expected().Features,
				tc.initialState.Expected().Config,
				tc.initialState.configIdx,
				tc.update.apmConfig,
			)
			assert.Equal(t, tc.triggeredFlags, got)
		})
	}
}
