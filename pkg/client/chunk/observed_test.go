// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package chunk

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestObserved(t *testing.T) {
	timestamp := time.Now()

	scenarios := []struct {
		Name     string
		MaxSize  int
		Original *proto.CheckinObserved
		Expected []*proto.CheckinObserved
		Error    string
	}{
		{
			Name:    "unit too large to fit",
			MaxSize: 50,
			Error:   "unable to chunk proto.CheckinObserved the unit id-one is larger than max",
			Original: &proto.CheckinObserved{
				Token: "token",
				Units: []*proto.UnitObserved{
					{
						Id:             "id-one",
						Type:           proto.UnitType_OUTPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Message:        "Healthy",
						Payload: mustStruct(map[string]interface{}{
							"large": "this structure places this unit over the maximum size",
						}),
					},
					{
						Id:             "id-two",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Message:        "Healthy",
					},
				},
			},
		},
		{
			Name:    "first chunk too large",
			MaxSize: 110,
			Error:   "unable to chunk proto.CheckinObserved the first chunk with",
			Original: &proto.CheckinObserved{
				Token: "token",
				Units: []*proto.UnitObserved{
					{
						Id:             "id-one",
						Type:           proto.UnitType_OUTPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Message:        "Healthy",
						Payload: mustStruct(map[string]interface{}{
							"large": "this structure places this unit over the maximum size for first chunk",
						}),
					},
					{
						Id:             "id-two",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Message:        "Healthy",
						Payload: mustStruct(map[string]interface{}{
							"large": "this structure places this unit over the maximum size for first chunk",
						}),
					},
				},
			},
		},
		{
			Name:    "chunk checkin message",
			MaxSize: 120,
			Original: &proto.CheckinObserved{
				Token:        "token",
				FeaturesIdx:  2,
				ComponentIdx: 3,
				Units: []*proto.UnitObserved{
					{
						Id:             "id-one",
						Type:           proto.UnitType_OUTPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Message:        "Healthy",
						Payload: mustStruct(map[string]interface{}{
							"large": "this structure places this unit over the maximum size",
						}),
					},
					{
						Id:             "id-two",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Message:        "Healthy",
					},
					{
						Id:             "id-three",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Message:        "Healthy",
						Payload: mustStruct(map[string]interface{}{
							"large": "larger than id-two",
						}),
					},
				},
			},
			Expected: []*proto.CheckinObserved{
				{
					Token:        "token",
					FeaturesIdx:  2,
					ComponentIdx: 3,
					Units: []*proto.UnitObserved{
						{
							Id:             "id-two",
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
							Message:        "Healthy",
						},
						{
							Id:             "id-three",
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
							Message:        "Healthy",
							Payload: mustStruct(map[string]interface{}{
								"large": "larger than id-two",
							}),
						},
					},
					UnitsTimestamp: timestamppb.New(timestamp),
				},
				{
					Token: "token",
					Units: []*proto.UnitObserved{
						{
							Id:             "id-one",
							Type:           proto.UnitType_OUTPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
							Message:        "Healthy",
							Payload: mustStruct(map[string]interface{}{
								"large": "this structure places this unit over the maximum size",
							}),
						},
					},
					UnitsTimestamp: timestamppb.New(timestamp),
				},
				{
					Token:          "token",
					Units:          []*proto.UnitObserved{},
					UnitsTimestamp: timestamppb.New(timestamp),
				},
			},
		},
		{
			Name:    "fits in single message",
			MaxSize: 200,
			Original: &proto.CheckinObserved{
				Token:        "token",
				FeaturesIdx:  2,
				ComponentIdx: 3,
				Units: []*proto.UnitObserved{
					{
						Id:             "id-one",
						Type:           proto.UnitType_OUTPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Message:        "Healthy",
						Payload: mustStruct(map[string]interface{}{
							"large": "this structure places this unit over the maximum size",
						}),
					},
					{
						Id:             "id-two",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Message:        "Healthy",
					},
				},
			},
			Expected: []*proto.CheckinObserved{
				{
					Token:        "token",
					FeaturesIdx:  2,
					ComponentIdx: 3,
					Units: []*proto.UnitObserved{
						{
							Id:             "id-one",
							Type:           proto.UnitType_OUTPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
							Message:        "Healthy",
							Payload: mustStruct(map[string]interface{}{
								"large": "this structure places this unit over the maximum size",
							}),
						},
						{
							Id:             "id-two",
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
							Message:        "Healthy",
						},
					},
				},
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			observed, err := Observed(scenario.Original, scenario.MaxSize, WithTimestamp(timestamp))
			if scenario.Error != "" {
				require.Error(t, err)
				assert.True(t, strings.Contains(err.Error(), scenario.Error))
			} else {
				require.NoError(t, err)
				diff := cmp.Diff(scenario.Expected, observed, protocmp.Transform())
				require.Empty(t, diff)

				// re-assemble and it should now match the original
				assembled, err := RecvObserved(&fakeCheckinObservedReceiver{msgs: observed})
				require.NoError(t, err)

				// to compare we need to remove the units timestamp and ensure the units are in the same order
				// completely acceptable that they get re-ordered in the chunking process
				assembled.UnitsTimestamp = nil
				slices.SortStableFunc(assembled.Units, sortObservedUnits)
				slices.SortStableFunc(scenario.Original.Units, sortObservedUnits)

				diff = cmp.Diff(scenario.Original, assembled, protocmp.Transform())
				assert.Empty(t, diff)
			}
		})
	}
}

func TestRecvObserved_Timestamp_Restart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping test on Windows for now. See https://github.com/elastic/elastic-agent-client/issues/120#issuecomment-2757564351")
	}
	firstTimestamp := time.Now()
	first := &proto.CheckinObserved{
		Token:        "token",
		FeaturesIdx:  2,
		ComponentIdx: 3,
		Units: []*proto.UnitObserved{
			{
				Id:             "first-one",
				Type:           proto.UnitType_OUTPUT,
				ConfigStateIdx: 1,
				State:          proto.State_HEALTHY,
				Message:        "Healthy",
				Payload: mustStruct(map[string]interface{}{
					"large": "this structure places this unit over the maximum size",
				}),
			},
			{
				Id:             "first-two",
				Type:           proto.UnitType_INPUT,
				ConfigStateIdx: 1,
				State:          proto.State_HEALTHY,
				Message:        "Healthy",
			},
		},
	}
	firstMsgs, err := Observed(first, 100, WithTimestamp(firstTimestamp))
	require.NoError(t, err)

	secondTimestamp := time.Now()
	second := &proto.CheckinObserved{
		Token:        "token",
		FeaturesIdx:  2,
		ComponentIdx: 3,
		Units: []*proto.UnitObserved{
			{
				Id:             "second-one",
				Type:           proto.UnitType_OUTPUT,
				ConfigStateIdx: 1,
				State:          proto.State_HEALTHY,
				Message:        "Healthy",
				Payload: mustStruct(map[string]interface{}{
					"large": "this structure places this unit over the maximum size",
				}),
			},
			{
				Id:             "second-two",
				Type:           proto.UnitType_INPUT,
				ConfigStateIdx: 1,
				State:          proto.State_HEALTHY,
				Message:        "Healthy",
			},
		},
	}
	secondsMsgs, err := Observed(second, 100, WithTimestamp(secondTimestamp))
	require.NoError(t, err)

	// ensure chunking results in exact length as the order in the test relies on it
	require.Len(t, firstMsgs, 3)
	require.Len(t, secondsMsgs, 3)

	// re-order the messages
	reorderedMsgs := make([]*proto.CheckinObserved, 6)
	reorderedMsgs[0] = firstMsgs[0]
	reorderedMsgs[1] = secondsMsgs[0] // becomes new set
	reorderedMsgs[2] = firstMsgs[1]   // ignored
	reorderedMsgs[3] = firstMsgs[2]   // ignored
	reorderedMsgs[4] = secondsMsgs[1]
	reorderedMsgs[5] = secondsMsgs[2]

	// re-assemble and it should now match the second
	assembled, err := RecvObserved(&fakeCheckinObservedReceiver{msgs: reorderedMsgs})
	require.NoError(t, err)

	// to compare we need to remove the units timestamp and ensure the units are in the same order
	// completely acceptable that they get re-ordered in the chunking process
	assembled.UnitsTimestamp = nil
	slices.SortStableFunc(assembled.Units, sortObservedUnits)
	slices.SortStableFunc(second.Units, sortObservedUnits)

	diff := cmp.Diff(second, assembled, protocmp.Transform())
	assert.Empty(t, diff)
}

func TestObserved_RepeatPadding(t *testing.T) {
	const grpcMaxSize = 1024 * 1024 * 4 // GRPC default max message size

	// build the units to ensure that there is more than double the units required for the GRPC configuration
	minimumMsgSize := grpcMaxSize * 2 // double it
	var units []*proto.UnitObserved
	var unitsSize int
	var nextUnitID int
	for unitsSize < minimumMsgSize {
		unit := &proto.UnitObserved{
			Id:             fmt.Sprintf("fake-input-%d", nextUnitID),
			Type:           proto.UnitType_INPUT,
			State:          proto.State_HEALTHY,
			Message:        fmt.Sprintf("fake-input-%d is healthy", nextUnitID),
			ConfigStateIdx: 1,
		}
		units = append(units, unit)
		unitsSize += gproto.Size(unit)
		nextUnitID++
	}

	chunked, err := Observed(&proto.CheckinObserved{
		Token:        "token",
		FeaturesIdx:  2,
		ComponentIdx: 3,
		Units:        units,
	}, grpcMaxSize, WithRepeatPadding(0))
	require.NoError(t, err)
	require.Greater(t, gproto.Size(chunked[0]), grpcMaxSize)

	chunked, err = Observed(&proto.CheckinObserved{
		Token:        "token",
		FeaturesIdx:  2,
		ComponentIdx: 3,
		Units:        units,
	}, grpcMaxSize)
	require.NoError(t, err)
	require.Greater(t, grpcMaxSize, gproto.Size(chunked[0]))
}

func mustStruct(v map[string]interface{}) *structpb.Struct {
	s, err := structpb.NewStruct(v)
	if err != nil {
		panic(err)
	}
	return s
}

func sortObservedUnits(a *proto.UnitObserved, b *proto.UnitObserved) int {
	return strings.Compare(a.Id, b.Id)
}

type fakeCheckinObservedReceiver struct {
	msgs []*proto.CheckinObserved
}

func (f *fakeCheckinObservedReceiver) Recv() (*proto.CheckinObserved, error) {
	var msg *proto.CheckinObserved
	msg, f.msgs = f.msgs[0], f.msgs[1:]
	return msg, nil
}
