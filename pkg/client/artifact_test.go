// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"context"
	"crypto/rand"
	"errors"
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

func TestArtifact(t *testing.T) {
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
			case change := <-client.UnitChanges():
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
	artifactClient := unit.Artifacts()

	t.Run("ErrorOnFetch", func(t *testing.T) {
		id := mock.NewID()
		badID := mock.NewID()
		srv.ArtifactFetchImpl = func(request *proto.ArtifactFetchRequest, server proto.ElasticAgentArtifact_FetchServer) error {
			if request.Id != id || request.Sha256 != id {
				return errors.New("missing artifact")
			}
			t.Fatal("should not get this far")
			return nil
		}
		_, err := artifactClient.Fetch(ctx, badID, badID)
		require.Error(t, err)
	})

	t.Run("Fetch", func(t *testing.T) {
		id := mock.NewID()
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
