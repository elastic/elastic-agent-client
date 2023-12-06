// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package utils

import (
	"encoding/json"
	"time"

	"golang.org/x/exp/slices"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	protobuf "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

// JSONMustMarshal marshals the input to JSON []byte and panics if it fails.
func JSONMustMarshal(input interface{}) []byte {
	res, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return res
}

type chunkedOptions struct {
	timestamp time.Time
}

// ChunkOption is an option for adjusting chunking.
type ChunkOption func(opts *chunkedOptions)

// WithTimestamp adjusts the timestamp used for the chunking.
//
// Note: Mainly used for testing to ensure a specific timestamp is used.
func WithTimestamp(t time.Time) ChunkOption {
	return func(opts *chunkedOptions) {
		opts.timestamp = t
	}
}

// ChunkedObserved chunks `proto.CheckinObserved` message into multiple chunks to be sent across the protocol.
func ChunkedObserved(msg *proto.CheckinObserved, maxSize int, opts ...ChunkOption) ([]*proto.CheckinObserved, error) {
	var options chunkedOptions
	options.timestamp = time.Now() // timestamp used for chunk set
	for _, opt := range opts {
		opt(&options)
	}

	s := protobuf.Size(msg)
	if s <= maxSize || len(msg.Units) <= 1 {
		// fits so no chunking needed or has 0 or 1 units which cannot be chunked
		return []*proto.CheckinObserved{msg}, nil
	}

	msgs := make([]*proto.CheckinObserved, 0, 3) // start at 3 minimum

	// a single unit is the smallest a chunk can be
	// pre-calculate the size and ensure that a single unit is less than the maxSize
	bySize := make([]observedBySize, len(msg.Units))
	for i, u := range msg.Units {
		bySize[i].unit = u
		bySize[i].size = protobuf.Size(u)
		// >= is used because even if it's at the maxSize, with overhead
		// it will still be too big even if it's at the exact maxSize
		if bySize[i].size >= maxSize {
			return nil, status.Errorf(
				codes.ResourceExhausted,
				"unable to chunk proto.CheckinObserved the unit %s is larger than max (%d vs. %d)",
				u.Id, bySize[i].size, maxSize)
		}
	}

	// sort the smallest units first, this ensures that the first chunk that includes extra
	// fields uses the smallest unit to ensure that it all fits
	slices.SortStableFunc(bySize, func(a, b observedBySize) int {
		return a.size - b.size
	})

	// first message all fields are set; except units is made smaller
	m := shallowCopyCheckinObserved(msg)
	m.Units = make([]*proto.UnitObserved, 0, 1)
	m.Units = append(m.Units, bySize[0].unit)
	m.UnitsTimestamp = timestamppb.New(options.timestamp)
	s = protobuf.Size(m)
	if s >= maxSize {
		// not possible even for the first chunk to fit
		return nil, status.Errorf(
			codes.ResourceExhausted,
			"unable to chunk proto.CheckinObserved the first chunk with unit %s is larger than max (%d vs. %d)",
			m.Units[0].Id, s, maxSize)
	}

	// keep adding units until it doesn't fit
	for nextUnit := 1; s < maxSize && nextUnit < len(bySize); nextUnit++ {
		us := bySize[nextUnit]
		if s+us.size < maxSize {
			// unit fits add it
			m.Units = append(m.Units, us.unit)
			s += us.size
		} else {
			// doesn't fit, create a new chunk
			msgs = append(msgs, m)
			m = &proto.CheckinObserved{}
			m.Token = msg.Token
			m.UnitsTimestamp = timestamppb.New(options.timestamp)
			m.Units = make([]*proto.UnitObserved, 0, 1)
			m.Units = append(m.Units, us.unit)
			s = protobuf.Size(m)
		}
	}

	// all chunks created, create the empty chunk
	msgs = append(msgs, m)
	m = &proto.CheckinObserved{}
	m.Token = msg.Token
	m.UnitsTimestamp = timestamppb.New(options.timestamp)
	m.Units = make([]*proto.UnitObserved, 0)
	msgs = append(msgs, m)
	return msgs, nil
}

// CheckinObservedReceiver provides a Recv interface to receive proto.CheckinObserved messages.
type CheckinObservedReceiver interface {
	Recv() (*proto.CheckinObserved, error)
}

// RecvChunkedObserved handles the receiving of chunked proto.CheckinObserved.
func RecvChunkedObserved(recv CheckinObservedReceiver) (*proto.CheckinObserved, error) {
	var first *proto.CheckinObserved
	for {
		msg, err := recv.Recv()
		if err != nil {
			return nil, err
		}
		if msg.UnitsTimestamp == nil {
			// all included in a single message
			return msg, nil
		}
		if first == nil {
			// first message in batch
			first = msg
		} else if first.UnitsTimestamp.AsTime() != msg.UnitsTimestamp.AsTime() {
			// only used if the new timestamp is newer
			if first.UnitsTimestamp.AsTime().After(msg.UnitsTimestamp.AsTime()) {
				// not newer so we ignore the message
				continue
			}
			// different batch; restart
			first = msg
		}
		if len(msg.Units) == 0 {
			// ending match message
			return first, nil
		}
		if first != msg {
			first.Units = append(first.Units, msg.Units...)
		}
	}
}

func shallowCopyCheckinObserved(msg *proto.CheckinObserved) *proto.CheckinObserved {
	return &proto.CheckinObserved{
		Token:          msg.Token,
		Units:          msg.Units,
		VersionInfo:    msg.VersionInfo,
		FeaturesIdx:    msg.FeaturesIdx,
		ComponentIdx:   msg.ComponentIdx,
		UnitsTimestamp: msg.UnitsTimestamp,
		Supports:       msg.Supports,
	}
}

type observedBySize struct {
	unit *proto.UnitObserved
	size int
}

// ChunkedExpected chunks `proto.CheckinExpected` message into multiple chunks to be sent across the protocol.
func ChunkedExpected(msg *proto.CheckinExpected, maxSize int, opts ...ChunkOption) ([]*proto.CheckinExpected, error) {
	var options chunkedOptions
	options.timestamp = time.Now() // timestamp used for chunk set
	for _, opt := range opts {
		opt(&options)
	}

	s := protobuf.Size(msg)
	if s <= maxSize || len(msg.Units) <= 1 {
		// fits so no chunking needed or has 0 or 1 units which cannot be chunked
		return []*proto.CheckinExpected{msg}, nil
	}

	msgs := make([]*proto.CheckinExpected, 0, 3) // start at 3 minimum

	// a single unit is the smallest a chunk can be
	// pre-calculate the size and ensure that a single unit is less than the maxSize
	bySize := make([]expectedBySize, len(msg.Units))
	for i, u := range msg.Units {
		bySize[i].unit = u
		bySize[i].size = protobuf.Size(u)
		// >= is used because even if it's at the maxSize, with overhead
		// it will still be too big even if it's at the exact maxSize
		if bySize[i].size >= maxSize {
			return nil, status.Errorf(
				codes.ResourceExhausted,
				"unable to chunk proto.CheckinExpected the unit %s is larger than max (%d vs. %d)",
				u.Id, bySize[i].size, maxSize)
		}
	}

	// sort the smallest units first, this ensures that the first chunk that includes extra
	// fields uses the smallest unit to ensure that it all fits
	slices.SortStableFunc(bySize, func(a, b expectedBySize) int {
		return a.size - b.size
	})

	// first message all fields are set; except units is made smaller
	m := shallowCopyCheckinExpected(msg)
	m.Units = make([]*proto.UnitExpected, 0, 1)
	m.Units = append(m.Units, bySize[0].unit)
	m.UnitsTimestamp = timestamppb.New(options.timestamp)
	s = protobuf.Size(m)
	if s >= maxSize {
		// not possible even for the first chunk to fit
		return nil, status.Errorf(
			codes.ResourceExhausted,
			"unable to chunk proto.CheckinExpected the first chunk with unit %s is larger than max (%d vs. %d)",
			m.Units[0].Id, s, maxSize)
	}

	// keep adding units until it doesn't fit
	for nextUnit := 1; s < maxSize && nextUnit < len(bySize); nextUnit++ {
		us := bySize[nextUnit]
		if s+us.size < maxSize {
			// unit fits add it
			m.Units = append(m.Units, us.unit)
			s += us.size
		} else {
			// doesn't fit, create a new chunk
			msgs = append(msgs, m)
			m = &proto.CheckinExpected{}
			m.UnitsTimestamp = timestamppb.New(options.timestamp)
			m.Units = make([]*proto.UnitExpected, 0, 1)
			m.Units = append(m.Units, us.unit)
			s = protobuf.Size(m)
		}
	}

	// all chunks created, create the empty chunk
	msgs = append(msgs, m)
	m = &proto.CheckinExpected{}
	m.UnitsTimestamp = timestamppb.New(options.timestamp)
	m.Units = make([]*proto.UnitExpected, 0)
	msgs = append(msgs, m)
	return msgs, nil
}

// CheckinExpectedReceiver provides a Recv interface to receive proto.CheckinExpected messages.
type CheckinExpectedReceiver interface {
	Recv() (*proto.CheckinExpected, error)
}

// RecvChunkedExpected handles the receiving of chunked proto.CheckinObjected.
func RecvChunkedExpected(recv CheckinExpectedReceiver) (*proto.CheckinExpected, error) {
	var first *proto.CheckinExpected
	for {
		msg, err := recv.Recv()
		if err != nil {
			return nil, err
		}
		if msg.UnitsTimestamp == nil {
			// all included in a single message
			return msg, nil
		}
		if first == nil {
			// first message in batch
			first = msg
		} else if first.UnitsTimestamp.AsTime() != msg.UnitsTimestamp.AsTime() {
			// only used if the new timestamp is newer
			if first.UnitsTimestamp.AsTime().After(msg.UnitsTimestamp.AsTime()) {
				// not newer so we ignore the message
				continue
			}
			// different batch; restart
			first = msg
		}
		if len(msg.Units) == 0 {
			// ending match message
			return first, nil
		}
		if first != msg {
			first.Units = append(first.Units, msg.Units...)
		}
	}
}

func shallowCopyCheckinExpected(msg *proto.CheckinExpected) *proto.CheckinExpected {
	return &proto.CheckinExpected{
		AgentInfo:      msg.AgentInfo,
		Features:       msg.Features,
		FeaturesIdx:    msg.FeaturesIdx,
		Component:      msg.Component,
		ComponentIdx:   msg.ComponentIdx,
		Units:          msg.Units,
		UnitsTimestamp: msg.UnitsTimestamp,
	}
}

type expectedBySize struct {
	unit *proto.UnitExpected
	size int
}
