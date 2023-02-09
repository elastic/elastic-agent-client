// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/elastic/elastic-agent-client/v7/pkg/client/mock"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestLog(t *testing.T) {
	token := mock.NewID()
	srv := mock.StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			if observed.Token == token {
				return &proto.CheckinExpected{
					Units: []*proto.UnitExpected{
						{
							Id:             mock.NewID(),
							Type:           proto.UnitType_OUTPUT,
							State:          proto.State_HEALTHY,
							LogLevel:       proto.UnitLogLevel_INFO,
							ConfigStateIdx: 1,
							Config:         &proto.UnitExpectedConfig{},
						},
					},
				}
			}
			// disconnect
			return nil
		},
		ActionImpl: func(response *proto.ActionResponse) error {
			// Not tested
			return nil
		},
	}
	require.NoError(t, srv.Start())
	defer srv.Stop()

	var errsMu sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials()))
	storeErrors(ctx, client, &errs, &errsMu)

	var unitsMu sync.Mutex
	var units []*Unit
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-client.UnitChanged():
				switch change.Type {
				case UnitChangedAdded:
					unitsMu.Lock()
					units = append(units, change.Unit)
					unitsMu.Unlock()
				default:
					panic("not implemented")
				}
			}
		}
	}()

	require.NoError(t, client.Start(ctx))
	defer client.Stop()
	require.NoError(t, waitFor(func() error {
		unitsMu.Lock()
		defer unitsMu.Unlock()

		if len(units) != 1 {
			return fmt.Errorf("client never got unit")
		}
		return nil
	}))

	unit := units[0]
	logClient := unit.Logger()

	t.Run("ErrorOnLog", func(t *testing.T) {
		var msg []*proto.LogMessage
		srv.LogImpl = func(fetch *proto.LogMessageRequest) (*proto.LogMessageResponse, error) {
			if len(fetch.Messages) == 0 {
				return nil, fmt.Errorf("no messages")
			}
			for _, m := range fetch.Messages {
				if m.Message == nil {
					return nil, fmt.Errorf("no message")
				}
				msg = append(msg, m)
			}
			return &proto.LogMessageResponse{}, nil
		}
		err := logClient.Log(ctx, nil)
		require.Error(t, err)
	})

	t.Run("Log", func(t *testing.T) {
		var msg []*proto.LogMessage
		srv.LogImpl = func(fetch *proto.LogMessageRequest) (*proto.LogMessageResponse, error) {
			if len(fetch.Messages) == 0 {
				return nil, fmt.Errorf("no messages")
			}
			for _, m := range fetch.Messages {
				if m.Message == nil {
					return nil, fmt.Errorf("no message")
				}
				msg = append(msg, m)
			}
			return &proto.LogMessageResponse{}, nil
		}
		err := logClient.Log(ctx, []byte("message"))
		require.NoError(t, err)
		assert.Len(t, msg, 1)
		assert.Equal(t, []byte("message"), msg[0].Message)
	})
}
