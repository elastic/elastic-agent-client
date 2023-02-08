// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/elastic/elastic-agent-client/v7/pkg/client/mock"
	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestStore(t *testing.T) {
	token := mock.NewID()
	store := NewMemoryStore()
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
		StoreImpl: store,
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
			case change := <-client.Changes():
				switch change.Type {
				case ChangeUnitAdded:
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
	storeClient := unit.Store()

	t.Run("SetGet", func(t *testing.T) {
		store.Clear()
		tx, err := storeClient.BeginTx(ctx, true)
		require.NoError(t, err)
		defer tx.Discard(ctx)
		err = tx.SetKey(ctx, "test", []byte("value"), 0)
		require.NoError(t, err)
		res, ok, err := tx.GetKey(ctx, "test")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []byte("value"), res)
		err = tx.Commit(ctx)
		require.NoError(t, err)

		// get value in separate transaction
		tx, err = storeClient.BeginTx(ctx, false)
		require.NoError(t, err)
		defer tx.Discard(ctx)
		res, ok, err = tx.GetKey(ctx, "test")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []byte("value"), res)
	})

	t.Run("SetGetSeperateTx", func(t *testing.T) {
		store.Clear()
		tx, err := storeClient.BeginTx(ctx, true)
		require.NoError(t, err)
		defer tx.Discard(ctx)
		err = tx.SetKey(ctx, "test", []byte("value"), 0)
		require.NoError(t, err)
		res, ok, err := tx.GetKey(ctx, "test")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []byte("value"), res)

		// get value in separate transaction (before commit)
		newTx, err := storeClient.BeginTx(ctx, false)
		require.NoError(t, err)
		defer newTx.Discard(ctx)
		res, ok, err = newTx.GetKey(ctx, "test")
		require.NoError(t, err)
		require.False(t, ok)

		// commit original
		err = tx.Commit(ctx)
		require.NoError(t, err)

		// get value in one opened before commit (should still fail)
		res, ok, err = newTx.GetKey(ctx, "test")
		require.NoError(t, err)
		require.False(t, ok)
		newTx.Commit(ctx)

		// create another transaction (should be able to get)
		newTx, err = storeClient.BeginTx(ctx, false)
		require.NoError(t, err)
		defer newTx.Discard(ctx)
		res, ok, err = newTx.GetKey(ctx, "test")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []byte("value"), res)
	})

	t.Run("WriteReadOnly", func(t *testing.T) {
		store.Clear()
		tx, err := storeClient.BeginTx(ctx, false)
		require.NoError(t, err)
		defer tx.Discard(ctx)
		err = tx.SetKey(ctx, "test", []byte("value"), 0)
		require.Equal(t, ErrStoreTxReadOnly, err)
	})

	t.Run("Discarded", func(t *testing.T) {
		store.Clear()
		tx, err := storeClient.BeginTx(ctx, true)
		require.NoError(t, err)
		err = tx.Discard(ctx)
		require.NoError(t, err)
		err = tx.SetKey(ctx, "test", []byte("value"), 0)
		require.Equal(t, ErrStoreTxDiscarded, err)
		_, _, err = tx.GetKey(ctx, "test")
		require.Equal(t, ErrStoreTxDiscarded, err)
		err = tx.DeleteKey(ctx, "test")
		require.Equal(t, ErrStoreTxDiscarded, err)
		err = tx.Commit(ctx)
		require.Equal(t, ErrStoreTxDiscarded, err)
	})

	t.Run("Committed", func(t *testing.T) {
		store.Clear()
		tx, err := storeClient.BeginTx(ctx, true)
		require.NoError(t, err)
		err = tx.Commit(ctx)
		require.NoError(t, err)
		err = tx.SetKey(ctx, "test", []byte("value"), 0)
		require.Equal(t, ErrStoreTxCommitted, err)
		_, _, err = tx.GetKey(ctx, "test")
		require.Equal(t, ErrStoreTxCommitted, err)
		err = tx.DeleteKey(ctx, "test")
		require.Equal(t, ErrStoreTxCommitted, err)
		err = tx.Commit(ctx)
		require.NoError(t, err)
	})

	t.Run("DiscardAfterCommitted", func(t *testing.T) {
		store.Clear()
		tx, err := storeClient.BeginTx(ctx, true)
		require.NoError(t, err)
		err = tx.Commit(ctx)
		require.NoError(t, err)
		err = tx.Discard(ctx)
		require.NoError(t, err)
	})

	t.Run("BrokenTx", func(t *testing.T) {
		store.Clear()
		tx, err := storeClient.BeginTx(ctx, false)
		require.NoError(t, err)
		// mark the tx as writeable, when server-side it's not
		// it will then get into the broken state.
		sTx := tx.(*storeClientTx)
		sTx.write = true
		err = tx.SetKey(ctx, "test", []byte("value"), 0)
		require.Error(t, err)

		err = tx.SetKey(ctx, "test", []byte("value"), 0)
		require.Equal(t, ErrStoreTxBroken, err)
		_, _, err = tx.GetKey(ctx, "test")
		require.Equal(t, ErrStoreTxBroken, err)
		err = tx.DeleteKey(ctx, "test")
		require.Equal(t, ErrStoreTxBroken, err)
		err = tx.Commit(ctx)
		require.Equal(t, ErrStoreTxBroken, err)
		err = tx.Discard(ctx)
		require.Equal(t, ErrStoreTxBroken, err)
	})
}

type unitMemoryValue struct {
	value   []byte
	exp     time.Time
	deleted bool
	touched bool
}

type unitMemoryStoreTx struct {
	writeable bool
	data      map[string]unitMemoryValue
}

type unitMemoryStore struct {
	unitID   string
	unitType proto.UnitType

	mux  sync.RWMutex
	data map[string]unitMemoryValue
	txs  map[string]*unitMemoryStoreTx
}

func (s *unitMemoryStore) startTx(writeable bool) string {
	txID := mock.NewID()
	s.mux.Lock()
	defer s.mux.Unlock()
	data := s.copyData()
	s.txs[txID] = &unitMemoryStoreTx{
		writeable: writeable,
		data:      data,
	}
	return txID
}

func (s *unitMemoryStore) commitTx(tx *unitMemoryStoreTx) {
	s.mux.Lock()
	defer s.mux.Unlock()
	for key, value := range tx.data {
		if !value.touched {
			continue
		}
		if value.deleted {
			delete(s.data, key)
			continue
		}
		s.data[key] = value
	}
}

func (s *unitMemoryStore) copyData() map[string]unitMemoryValue {
	copy := make(map[string]unitMemoryValue)
	for key, value := range s.data {
		copy[key] = value
	}
	return copy
}

type MemoryStore struct {
	storeMu sync.RWMutex
	store   []*unitMemoryStore
	txMu    sync.RWMutex
	txs     map[string]*unitMemoryStore
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		txs: make(map[string]*unitMemoryStore),
	}
}

func (s *MemoryStore) Clear() {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	s.txMu.Lock()
	defer s.txMu.Unlock()
	s.store = nil
	s.txs = make(map[string]*unitMemoryStore)
}

func (s *MemoryStore) Clean(unitID string, unitType proto.UnitType) bool {
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()
	unitStore := s.findUnitStore(unitID, unitType)
	if unitStore == nil {
		return true
	}
	unitStore.mux.RLock()
	defer unitStore.mux.RUnlock()
	return len(unitStore.txs) == 0
}

func (s *MemoryStore) Dirty(unitID string, unitType proto.UnitType) bool {
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()
	unitStore := s.findUnitStore(unitID, unitType)
	if unitStore == nil {
		return true
	}
	unitStore.mux.RLock()
	defer unitStore.mux.RUnlock()
	return len(unitStore.txs) > 0
}

func (s *MemoryStore) Current(unitID string, unitType proto.UnitType) map[string][]byte {
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()
	unitStore := s.findUnitStore(unitID, unitType)
	if unitStore == nil {
		return nil
	}
	unitStore.mux.RLock()
	defer unitStore.mux.RUnlock()
	res := make(map[string][]byte)
	for key, val := range unitStore.data {
		if !val.exp.IsZero() && val.exp.Before(time.Now().UTC()) {
			// expired
			continue
		}
		res[key] = val.value
	}
	return res
}

func (s *MemoryStore) BeginTx(request *proto.StoreBeginTxRequest) (*proto.StoreBeginTxResponse, error) {
	unitStore := s.findOrCreateUnitStore(request.UnitId, request.UnitType)
	txID := unitStore.startTx(request.Type == proto.StoreTxType_READ_WRITE)
	s.txMu.Lock()
	defer s.txMu.Unlock()
	s.txs[txID] = unitStore
	return &proto.StoreBeginTxResponse{Id: txID}, nil
}

func (s *MemoryStore) GetKey(request *proto.StoreGetKeyRequest) (*proto.StoreGetKeyResponse, error) {
	s.txMu.RLock()
	unitStore, ok := s.txs[request.TxId]
	s.txMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid transaction id: %s", request.TxId)
	}
	tx, ok := unitStore.txs[request.TxId]
	if !ok {
		return nil, fmt.Errorf("error with transaction id: %s", request.TxId)
	}
	val, ok := tx.data[request.Name]
	if !ok || val.deleted {
		// not found or deleted
		return &proto.StoreGetKeyResponse{
			Status: proto.StoreGetKeyResponse_NOT_FOUND,
		}, nil
	}
	if !val.exp.IsZero() && val.exp.Before(time.Now().UTC()) {
		// expired
		return &proto.StoreGetKeyResponse{
			Status: proto.StoreGetKeyResponse_NOT_FOUND,
		}, nil
	}
	return &proto.StoreGetKeyResponse{
		Status: proto.StoreGetKeyResponse_FOUND,
		Value:  val.value,
	}, nil
}

func (s *MemoryStore) SetKey(request *proto.StoreSetKeyRequest) (*proto.StoreSetKeyResponse, error) {
	s.txMu.RLock()
	unitStore, ok := s.txs[request.TxId]
	s.txMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid transaction id: %s", request.TxId)
	}
	tx, ok := unitStore.txs[request.TxId]
	if !ok {
		return nil, fmt.Errorf("error with transaction id: %s", request.TxId)
	}
	if !tx.writeable {
		return nil, fmt.Errorf("not writeable transaction id: %s", request.TxId)
	}
	var val unitMemoryValue
	val.value = request.Value
	val.deleted = false
	val.exp = time.Time{}
	val.touched = true
	if request.Ttl > 0 {
		val.exp = time.Now().UTC().Add(time.Duration(request.Ttl) * time.Millisecond)
	}
	tx.data[request.Name] = val
	return &proto.StoreSetKeyResponse{}, nil
}

func (s *MemoryStore) DeleteKey(request *proto.StoreDeleteKeyRequest) (*proto.StoreDeleteKeyResponse, error) {
	s.txMu.RLock()
	unitStore, ok := s.txs[request.TxId]
	s.txMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid transaction id: %s", request.TxId)
	}
	tx, ok := unitStore.txs[request.TxId]
	if !ok {
		return nil, fmt.Errorf("error with transaction id: %s", request.TxId)
	}
	if !tx.writeable {
		return nil, fmt.Errorf("not writeable transaction id: %s", request.TxId)
	}
	val, ok := tx.data[request.Name]
	if !ok {
		// doesn't exist at all
		return &proto.StoreDeleteKeyResponse{}, nil
	}
	val.deleted = true
	val.touched = true
	tx.data[request.Name] = val
	return &proto.StoreDeleteKeyResponse{}, nil
}

func (s *MemoryStore) CommitTx(request *proto.StoreCommitTxRequest) (*proto.StoreCommitTxResponse, error) {
	s.txMu.RLock()
	unitStore, ok := s.txs[request.TxId]
	s.txMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid transaction id: %s", request.TxId)
	}
	tx, ok := unitStore.txs[request.TxId]
	if !ok {
		return nil, fmt.Errorf("error with transaction id: %s", request.TxId)
	}
	unitStore.commitTx(tx)
	delete(unitStore.txs, request.TxId)
	delete(s.txs, request.TxId)
	return &proto.StoreCommitTxResponse{}, nil
}

func (s *MemoryStore) DiscardTx(request *proto.StoreDiscardTxRequest) (*proto.StoreDiscardTxResponse, error) {
	s.txMu.RLock()
	unitStore, ok := s.txs[request.TxId]
	s.txMu.RUnlock()
	if ok {
		delete(unitStore.txs, request.TxId)
		delete(s.txs, request.TxId)
	}
	return &proto.StoreDiscardTxResponse{}, nil
}

func (s *MemoryStore) findUnitStore(unitID string, unitType proto.UnitType) *unitMemoryStore {
	for _, unit := range s.store {
		if unit.unitID == unitID && unit.unitType == unitType {
			return unit
		}
	}
	return nil
}

func (s *MemoryStore) findOrCreateUnitStore(unitID string, unitType proto.UnitType) *unitMemoryStore {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	unitStore := s.findUnitStore(unitID, unitType)
	if unitStore == nil {
		unitStore = &unitMemoryStore{
			unitID:   unitID,
			unitType: unitType,
			data:     make(map[string]unitMemoryValue),
			txs:      make(map[string]*unitMemoryStoreTx),
		}
		s.store = append(s.store, unitStore)
	}
	return unitStore
}
