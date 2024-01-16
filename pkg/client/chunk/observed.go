// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package chunk

import (
	"time"

	"golang.org/x/exp/slices"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	protobuf "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

// Observed chunks `proto.CheckinObserved` message into multiple chunks to be sent across the protocol.
func Observed(msg *proto.CheckinObserved, maxSize int, opts ...Option) ([]*proto.CheckinObserved, error) {
	var options options
	options.timestamp = time.Now() // timestamp used for chunk set
	options.repeatPadding = defaultRepeatPadding
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
		if s+us.size+options.repeatPadding < maxSize {
			// unit fits add it
			m.Units = append(m.Units, us.unit)
			s += us.size + options.repeatPadding
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
	msgs = append(msgs, m)

	// all chunks created, create the empty chunk
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

// RecvObserved handles the receiving of chunked proto.CheckinObserved.
func RecvObserved(recv CheckinObservedReceiver) (*proto.CheckinObserved, error) {
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
