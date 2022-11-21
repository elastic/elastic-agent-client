package client

import (
	"testing"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestUnitUpdateWithSameMap(t *testing.T) {
	sameStruct := map[string]interface{}{"username": "test"}
	pbStruct, err := structpb.NewStruct(sameStruct)
	require.NoError(t, err)
	existingUnit := Unit{
		exp:       UnitStateHealthy,
		logLevel:  UnitLogLevelDebug,
		configIdx: 1,
		config:    &proto.UnitExpectedConfig{Source: pbStruct},
	}

	pbStructNew, err := structpb.NewStruct(sameStruct)
	newUnit := &proto.UnitExpectedConfig{
		Source: pbStructNew,
	}
	// This should return false, as the two underlying maps in `source` are the same
	result := existingUnit.updateState(UnitStateHealthy, UnitLogLevelDebug, newUnit, 2)
	require.False(t, result)
}

func TestUnitUpdateWithNewMap(t *testing.T) {
	pbStruct, err := structpb.NewStruct(map[string]interface{}{"username": "test"})
	require.NoError(t, err)
	existingUnit := Unit{
		exp:       UnitStateHealthy,
		logLevel:  UnitLogLevelDebug,
		configIdx: 1,
		config:    &proto.UnitExpectedConfig{Source: pbStruct},
	}

	pbStructNew, err := structpb.NewStruct(map[string]interface{}{"username": "other"})
	newUnit := &proto.UnitExpectedConfig{
		Source: pbStructNew,
	}

	// This should return true, as we have an actually new map
	result := existingUnit.updateState(UnitStateHealthy, UnitLogLevelDebug, newUnit, 2)
	require.True(t, result)
}

func TestUnitUpdateLog(t *testing.T) {
	existingUnit := Unit{
		exp:       UnitStateHealthy,
		logLevel:  UnitLogLevelDebug,
		configIdx: 1,
		config:    &proto.UnitExpectedConfig{},
	}

	// This should return true, as we have an actually new map
	result := existingUnit.updateState(UnitStateHealthy, UnitLogLevelInfo, &proto.UnitExpectedConfig{}, 2)
	require.True(t, result)
}

func TestUnitUpdateState(t *testing.T) {
	existingUnit := Unit{
		exp:       UnitStateHealthy,
		logLevel:  UnitLogLevelDebug,
		configIdx: 1,
		config:    &proto.UnitExpectedConfig{},
	}

	// This should return true, as we have an actually new map
	result := existingUnit.updateState(UnitStateStopped, UnitLogLevelInfo, &proto.UnitExpectedConfig{}, 2)
	require.True(t, result)
}
