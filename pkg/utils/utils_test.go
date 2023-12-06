// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package utils

import (
	"golang.org/x/exp/slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestChunkedObserved(t *testing.T) {
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
			Name:    "chunk",
			MaxSize: 100,
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
							Id:             "id-two",
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
							Message:        "Healthy",
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
			observed, err := ChunkedObserved(scenario.Original, scenario.MaxSize, WithTimestamp(timestamp))
			if scenario.Error != "" {
				require.Error(t, err)
				assert.True(t, strings.Contains(err.Error(), scenario.Error))
			} else {
				require.NoError(t, err)
				diff := cmp.Diff(scenario.Expected, observed, protocmp.Transform())
				require.Empty(t, diff)

				// re-assemble and it should now match the original
				assembled, err := RecvChunkedObserved(&fakeCheckinObservedReceiver{msgs: observed})
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

func TestChunkedExpected(t *testing.T) {
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
			Name:    "chunk",
			MaxSize: 50,
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
							Id:             "id-two",
							Type:           proto.UnitType_INPUT,
							ConfigStateIdx: 1,
							State:          proto.State_HEALTHY,
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
			observed, err := ChunkedExpected(scenario.Original, scenario.MaxSize, WithTimestamp(timestamp))
			if scenario.Error != "" {
				require.Error(t, err)
				assert.True(t, strings.Contains(err.Error(), scenario.Error))
			} else {
				require.NoError(t, err)
				diff := cmp.Diff(scenario.Expected, observed, protocmp.Transform())
				require.Empty(t, diff)

				// re-assemble and it should now match the original
				assembled, err := RecvChunkedExpected(&fakeCheckinExpectedReceiver{msgs: observed})
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

func sortExpectedUnits(a *proto.UnitExpected, b *proto.UnitExpected) int {
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

type fakeCheckinExpectedReceiver struct {
	msgs []*proto.CheckinExpected
}

func (f *fakeCheckinExpectedReceiver) Recv() (*proto.CheckinExpected, error) {
	var msg *proto.CheckinExpected
	msg, f.msgs = f.msgs[0], f.msgs[1:]
	return msg, nil
}
