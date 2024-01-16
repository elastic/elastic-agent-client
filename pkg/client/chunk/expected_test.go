// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package chunk

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slices"
	gproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestExpected(t *testing.T) {
	timestamp := time.Now()

	scenarios := []struct {
		Name     string
		MaxSize  int
		Original *proto.CheckinExpected
		Expected []*proto.CheckinExpected
		Error    string
	}{
		{
			Name:    "unit too large to fit",
			MaxSize: 30,
			Error:   "unable to chunk proto.CheckinExpected the unit id-one is larger than max",
			Original: &proto.CheckinExpected{
				Units: []*proto.UnitExpected{
					{
						Id:             "id-one",
						Type:           proto.UnitType_OUTPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						LogLevel:       proto.UnitLogLevel_INFO,
						Config: &proto.UnitExpectedConfig{
							Id:   "testing",
							Type: "testing",
							Name: "testing",
						},
					},
					{
						Id:             "id-two",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
					},
				},
			},
		},
		{
			Name:    "first chunk too large",
			MaxSize: 50,
			Error:   "unable to chunk proto.CheckinExpected the first chunk with",
			Original: &proto.CheckinExpected{
				Units: []*proto.UnitExpected{
					{
						Id:             "id-one",
						Type:           proto.UnitType_OUTPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						LogLevel:       proto.UnitLogLevel_INFO,
						Config: &proto.UnitExpectedConfig{
							Id:   "testing1",
							Type: "testing",
							Name: "testing1",
						},
					},
					{
						Id:             "id-two",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						LogLevel:       proto.UnitLogLevel_INFO,
						Config: &proto.UnitExpectedConfig{
							Id:   "testing2",
							Type: "testing",
							Name: "testing2",
						},
					},
				},
			},
		},
		{
			Name:    "chunk checkin message",
			MaxSize: 70,
			Original: &proto.CheckinExpected{
				FeaturesIdx:  2,
				ComponentIdx: 3,
				Units: []*proto.UnitExpected{
					{
						Id:             "id-one",
						Type:           proto.UnitType_OUTPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Config: &proto.UnitExpectedConfig{
							Id:   "testing",
							Type: "testing",
							Name: "testing",
						},
					},
					{
						Id:             "id-two",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
					},
					{
						Id:             "id-three",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Config: &proto.UnitExpectedConfig{
							Id: "little-larger",
						},
					},
				},
			},
			Expected: []*proto.CheckinExpected{
				{
					FeaturesIdx:  2,
					ComponentIdx: 3,
					Units: []*proto.UnitExpected{
						{
							Id:             "id-two",
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
						},
						{
							Id:             "id-three",
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
							Config: &proto.UnitExpectedConfig{
								Id: "little-larger",
							},
						},
					},
					UnitsTimestamp: timestamppb.New(timestamp),
				},
				{
					Units: []*proto.UnitExpected{
						{
							Id:             "id-one",
							Type:           proto.UnitType_OUTPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
							Config: &proto.UnitExpectedConfig{
								Id:   "testing",
								Type: "testing",
								Name: "testing",
							},
						},
					},
					UnitsTimestamp: timestamppb.New(timestamp),
				},
				{
					Units:          []*proto.UnitExpected{},
					UnitsTimestamp: timestamppb.New(timestamp),
				},
			},
		},
		{
			Name:    "fits in single message",
			MaxSize: 200,
			Original: &proto.CheckinExpected{
				FeaturesIdx:  2,
				ComponentIdx: 3,
				Units: []*proto.UnitExpected{
					{
						Id:             "id-one",
						Type:           proto.UnitType_OUTPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
						Config: &proto.UnitExpectedConfig{
							Id:   "testing",
							Type: "testing",
							Name: "testing",
						},
					},
					{
						Id:             "id-two",
						Type:           proto.UnitType_INPUT,
						ConfigStateIdx: 1,
						State:          proto.State_HEALTHY,
					},
				},
			},
			Expected: []*proto.CheckinExpected{
				{
					FeaturesIdx:  2,
					ComponentIdx: 3,
					Units: []*proto.UnitExpected{
						{
							Id:             "id-one",
							Type:           proto.UnitType_OUTPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
							Config: &proto.UnitExpectedConfig{
								Id:   "testing",
								Type: "testing",
								Name: "testing",
							},
						},
						{
							Id:             "id-two",
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
						},
					},
				},
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			observed, err := Expected(scenario.Original, scenario.MaxSize, WithTimestamp(timestamp))
			if scenario.Error != "" {
				require.Error(t, err)
				assert.True(t, strings.Contains(err.Error(), scenario.Error))
			} else {
				require.NoError(t, err)
				diff := cmp.Diff(scenario.Expected, observed, protocmp.Transform())
				require.Empty(t, diff)

				// re-assemble and it should now match the original
				assembled, err := RecvExpected(&fakeCheckinExpectedReceiver{msgs: observed})
				require.NoError(t, err)

				// to compare we need to remove the units timestamp and ensure the units are in the same order
				// completely acceptable that they get re-ordered in the chunking process
				assembled.UnitsTimestamp = nil
				slices.SortStableFunc(assembled.Units, sortExpectedUnits)
				slices.SortStableFunc(scenario.Original.Units, sortExpectedUnits)

				diff = cmp.Diff(scenario.Original, assembled, protocmp.Transform())
				assert.Empty(t, diff)
			}
		})
	}
}

func TestRecvExpected_Timestamp_Restart(t *testing.T) {
	firstTimestamp := time.Now()
	first := &proto.CheckinExpected{
		FeaturesIdx:  2,
		ComponentIdx: 3,
		Units: []*proto.UnitExpected{
			{
				Id:             "first-one",
				Type:           proto.UnitType_OUTPUT,
				ConfigStateIdx: 1,
				State:          proto.State_HEALTHY,
				Config: &proto.UnitExpectedConfig{
					Id:   "testing",
					Type: "testing",
					Name: "testing",
				},
			},
			{
				Id:             "first-two",
				Type:           proto.UnitType_INPUT,
				ConfigStateIdx: 1,
				State:          proto.State_HEALTHY,
			},
		},
	}
	firstMsgs, err := Expected(first, 50, WithTimestamp(firstTimestamp))
	require.NoError(t, err)

	secondTimestamp := time.Now()
	second := &proto.CheckinExpected{
		FeaturesIdx:  2,
		ComponentIdx: 3,
		Units: []*proto.UnitExpected{
			{
				Id:             "second-one",
				Type:           proto.UnitType_OUTPUT,
				ConfigStateIdx: 1,
				State:          proto.State_HEALTHY,
				Config: &proto.UnitExpectedConfig{
					Id:   "testing",
					Type: "testing",
					Name: "testing",
				},
			},
			{
				Id:             "second-two",
				Type:           proto.UnitType_INPUT,
				ConfigStateIdx: 1,
				State:          proto.State_HEALTHY,
			},
		},
	}
	secondsMsgs, err := Expected(second, 50, WithTimestamp(secondTimestamp))
	require.NoError(t, err)

	// ensure chunking results in exact length as the order in the test relies on it
	require.Len(t, firstMsgs, 3)
	require.Len(t, secondsMsgs, 3)

	// re-order the messages
	reorderedMsgs := make([]*proto.CheckinExpected, 6)
	reorderedMsgs[0] = firstMsgs[0]
	reorderedMsgs[1] = secondsMsgs[0] // becomes new set
	reorderedMsgs[2] = firstMsgs[1]   // ignored
	reorderedMsgs[3] = firstMsgs[2]   // ignored
	reorderedMsgs[4] = secondsMsgs[1]
	reorderedMsgs[5] = secondsMsgs[2]

	// re-assemble and it should now match the second
	assembled, err := RecvExpected(&fakeCheckinExpectedReceiver{msgs: reorderedMsgs})
	require.NoError(t, err)

	// to compare we need to remove the units timestamp and ensure the units are in the same order
	// completely acceptable that they get re-ordered in the chunking process
	assembled.UnitsTimestamp = nil
	slices.SortStableFunc(assembled.Units, sortExpectedUnits)
	slices.SortStableFunc(second.Units, sortExpectedUnits)

	diff := cmp.Diff(second, assembled, protocmp.Transform())
	assert.Empty(t, diff)
}

func TestRecvExpected_RepeatPadding(t *testing.T) {
	const grpcMaxSize = 1024 * 1024 * 4 // GRPC default max message size

	// build the units to ensure that there is more than double the units required for the GRPC configuration
	minimumMsgSize := grpcMaxSize * 2 // double it
	var units []*proto.UnitExpected
	var unitsSize int
	var nextUnitID int
	for unitsSize < minimumMsgSize {
		unit := &proto.UnitExpected{
			Id:             fmt.Sprintf("fake-input-%d", nextUnitID),
			Type:           proto.UnitType_INPUT,
			State:          proto.State_HEALTHY,
			ConfigStateIdx: 1,
			Config: &proto.UnitExpectedConfig{
				Id:   "testing",
				Type: "testing",
				Name: "testing",
			},
			LogLevel: proto.UnitLogLevel_ERROR,
		}
		units = append(units, unit)
		unitsSize += gproto.Size(unit)
		nextUnitID++
	}

	chunked, err := Expected(&proto.CheckinExpected{
		FeaturesIdx:  2,
		ComponentIdx: 3,
		Units:        units,
	}, grpcMaxSize, WithRepeatPadding(0))
	require.NoError(t, err)
	require.Greater(t, gproto.Size(chunked[0]), grpcMaxSize)

	chunked, err = Expected(&proto.CheckinExpected{
		FeaturesIdx:  2,
		ComponentIdx: 3,
		Units:        units,
	}, grpcMaxSize)
	require.NoError(t, err)
	require.Greater(t, grpcMaxSize, gproto.Size(chunked[0]))
}

func sortExpectedUnits(a *proto.UnitExpected, b *proto.UnitExpected) int {
	return strings.Compare(a.Id, b.Id)
}

type fakeCheckinExpectedReceiver struct {
	msgs []*proto.CheckinExpected
}

func (f *fakeCheckinExpectedReceiver) Recv() (*proto.CheckinExpected, error) {
	var msg *proto.CheckinExpected
	msg, f.msgs = f.msgs[0], f.msgs[1:]
	return msg, nil
}
