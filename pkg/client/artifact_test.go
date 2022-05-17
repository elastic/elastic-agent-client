// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestArtifact(t *testing.T) {
	token := newID()
	srv := StubServerV2{
		CheckinV2Impl: func(observed *proto.CheckinObserved) *proto.CheckinExpected {
			if observed.Token == token {
				return &proto.CheckinExpected{
					Units: []*proto.UnitExpected{
						{
							Id:             newID(),
							Type:           proto.UnitType_OUTPUT,
							State:          proto.State_HEALTHY,
							ConfigStateIdx: 1,
							Config:         "config",
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

	var errsLock sync.Mutex
	var errs []error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewV2(fmt.Sprintf(":%d", srv.Port), token, VersionInfo{}, grpc.WithTransportCredentials(insecure.NewCredentials()))
	storeErrors(ctx, client, &errs, &errsLock)

	var unitsLock sync.Mutex
	var units []*Unit
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case change := <-client.UnitChanges():
				switch change.Type {
				case UnitChangedAdded:
					unitsLock.Lock()
					units = append(units, change.Unit)
					unitsLock.Unlock()
				default:
					panic("not implemented")
				}
			}
		}
	}()

	require.NoError(t, client.Start(ctx))
	defer client.Stop()
	require.NoError(t, waitFor(func() error {
		unitsLock.Lock()
		defer unitsLock.Unlock()

		if len(units) != 1 {
			return fmt.Errorf("client never got unit")
		}
		return nil
	}))

	unit := units[0]
	artifactClient := unit.Artifacts()

	t.Run("ErrorOnFetch", func(t *testing.T) {
		id := newID()
		badId := newID()
		srv.ArtifactFetchImpl = func(request *proto.ArtifactFetchRequest, server proto.ElasticAgentArtifact_FetchServer) error {
			if request.Id != id || request.Sha256 != id {
				return errors.New("missing artifact")
			}
			panic("should not get this far")
		}
		_, err := artifactClient.Fetch(ctx, badId, badId)
		require.Error(t, err)
	})

	t.Run("Fetch", func(t *testing.T) {
		id := newID()
		content := make([]byte, 256)
		_, err := rand.Read(content)
		require.NoError(t, err)

		srv.ArtifactFetchImpl = func(request *proto.ArtifactFetchRequest, server proto.ElasticAgentArtifact_FetchServer) error {
			if request.Id != id || request.Sha256 != id {
				return errors.New("missing artifact")
			}
			for i := 0; i < 256; i += 128 {
				err := server.Send(&proto.ArtifactFetchResponse{ContentEof: &proto.ArtifactFetchResponse_Content{Content: content[i : i+128]}})
				if err != nil {
					return err
				}
			}
			return server.Send(&proto.ArtifactFetchResponse{ContentEof: &proto.ArtifactFetchResponse_Eof{}})
		}
		received, err := artifactClient.Fetch(ctx, id, id)
		require.NoError(t, err)
		assert.Equal(t, content, received)
	})
}
