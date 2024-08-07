// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package chunk

import (
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	protobuf "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

// Expected chunks `proto.CheckinExpected` message into multiple chunks to be sent across the protocol.
func Expected(msg *proto.CheckinExpected, maxSize int, opts ...Option) ([]*proto.CheckinExpected, error) {
	var options options
	options.timestamp = time.Now() // timestamp used for chunk set
	options.repeatPadding = defaultRepeatPadding
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
		if s+us.size+options.repeatPadding < maxSize {
			// unit fits add it
			m.Units = append(m.Units, us.unit)
			s += us.size + options.repeatPadding
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
	msgs = append(msgs, m)

	// all chunks created, create the empty chunk
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

// RecvExpected handles the receiving of chunked proto.CheckinObjected.
func RecvExpected(recv CheckinExpectedReceiver) (*proto.CheckinExpected, error) {
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
