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

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestStore(t *testing.T) {
	token := newID()
	store := NewMemoryStore()
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
		StoreImpl: store,
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
	storeClient := unit.Store()

	t.Run("SetGet", func(t *testing.T) {
		store.Clear()
		txn, err := storeClient.BeginTxn(ctx, true)
		require.NoError(t, err)
		defer txn.Discard(ctx)
		err = txn.SetKey(ctx, "test", []byte("value"), 0)
		require.NoError(t, err)
		res, ok, err := txn.GetKey(ctx, "test")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []byte("value"), res)
		err = txn.Commit(ctx)
		require.NoError(t, err)

		// get value in separate transaction
		txn, err = storeClient.BeginTxn(ctx, false)
		require.NoError(t, err)
		defer txn.Discard(ctx)
		res, ok, err = txn.GetKey(ctx, "test")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []byte("value"), res)
	})

	t.Run("SetGetSeperateTxn", func(t *testing.T) {
		store.Clear()
		txn, err := storeClient.BeginTxn(ctx, true)
		require.NoError(t, err)
		defer txn.Discard(ctx)
		err = txn.SetKey(ctx, "test", []byte("value"), 0)
		require.NoError(t, err)
		res, ok, err := txn.GetKey(ctx, "test")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []byte("value"), res)

		// get value in separate transaction (before commit)
		newTxn, err := storeClient.BeginTxn(ctx, false)
		require.NoError(t, err)
		defer newTxn.Discard(ctx)
		res, ok, err = newTxn.GetKey(ctx, "test")
		require.NoError(t, err)
		require.False(t, ok)

		// commit original
		err = txn.Commit(ctx)
		require.NoError(t, err)

		// get value in one opened before commit (should still fail)
		res, ok, err = newTxn.GetKey(ctx, "test")
		require.NoError(t, err)
		require.False(t, ok)
		newTxn.Commit(ctx)

		// create another txn (should be able to get)
		newTxn, err = storeClient.BeginTxn(ctx, false)
		require.NoError(t, err)
		defer newTxn.Discard(ctx)
		res, ok, err = newTxn.GetKey(ctx, "test")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []byte("value"), res)
	})

	t.Run("WriteReadOnly", func(t *testing.T) {
		store.Clear()
		txn, err := storeClient.BeginTxn(ctx, false)
		require.NoError(t, err)
		defer txn.Discard(ctx)
		err = txn.SetKey(ctx, "test", []byte("value"), 0)
		require.Equal(t, ErrStoreTxnReadOnly, err)
	})

	t.Run("Discarded", func(t *testing.T) {
		store.Clear()
		txn, err := storeClient.BeginTxn(ctx, true)
		require.NoError(t, err)
		err = txn.Discard(ctx)
		require.NoError(t, err)
		err = txn.SetKey(ctx, "test", []byte("value"), 0)
		require.Equal(t, ErrStoreTxnDiscarded, err)
		_, _, err = txn.GetKey(ctx, "test")
		require.Equal(t, ErrStoreTxnDiscarded, err)
		err = txn.DeleteKey(ctx, "test")
		require.Equal(t, ErrStoreTxnDiscarded, err)
		err = txn.Commit(ctx)
		require.Equal(t, ErrStoreTxnDiscarded, err)
	})

	t.Run("Committed", func(t *testing.T) {
		store.Clear()
		txn, err := storeClient.BeginTxn(ctx, true)
		require.NoError(t, err)
		err = txn.Commit(ctx)
		require.NoError(t, err)
		err = txn.SetKey(ctx, "test", []byte("value"), 0)
		require.Equal(t, ErrStoreTxnCommitted, err)
		_, _, err = txn.GetKey(ctx, "test")
		require.Equal(t, ErrStoreTxnCommitted, err)
		err = txn.DeleteKey(ctx, "test")
		require.Equal(t, ErrStoreTxnCommitted, err)
		err = txn.Commit(ctx)
		require.NoError(t, err)
	})

	t.Run("DiscardAfterCommitted", func(t *testing.T) {
		store.Clear()
		txn, err := storeClient.BeginTxn(ctx, true)
		require.NoError(t, err)
		err = txn.Commit(ctx)
		require.NoError(t, err)
		err = txn.Discard(ctx)
		require.NoError(t, err)
	})

	t.Run("BrokenTxn", func(t *testing.T) {
		store.Clear()
		txn, err := storeClient.BeginTxn(ctx, false)
		require.NoError(t, err)
		// mark the txn as writeable, when server-side it's not
		// it will then get into the broken state.
		sTxn := txn.(*storeClientTxn)
		sTxn.write = true
		err = txn.SetKey(ctx, "test", []byte("value"), 0)
		require.Error(t, err)

		err = txn.SetKey(ctx, "test", []byte("value"), 0)
		require.Equal(t, ErrStoreTxnBroken, err)
		_, _, err = txn.GetKey(ctx, "test")
		require.Equal(t, ErrStoreTxnBroken, err)
		err = txn.DeleteKey(ctx, "test")
		require.Equal(t, ErrStoreTxnBroken, err)
		err = txn.Commit(ctx)
		require.Equal(t, ErrStoreTxnBroken, err)
		err = txn.Discard(ctx)
		require.Equal(t, ErrStoreTxnBroken, err)
	})
}

type unitMemoryValue struct {
	value   []byte
	exp     time.Time
	deleted bool
	touched bool
}

type unitMemoryStoreTxn struct {
	writeable bool
	data      map[string]unitMemoryValue
}

type unitMemoryStore struct {
	unitID   string
	unitType proto.UnitType

	mux  sync.RWMutex
	data map[string]unitMemoryValue
	txns map[string]*unitMemoryStoreTxn
}

func (s *unitMemoryStore) startTxn(writeable bool) string {
	txnID := newID()
	s.mux.Lock()
	defer s.mux.Unlock()
	data := s.copyData()
	s.txns[txnID] = &unitMemoryStoreTxn{
		writeable: writeable,
		data:      data,
	}
	return txnID
}

func (s *unitMemoryStore) commitTxn(txn *unitMemoryStoreTxn) {
	s.mux.Lock()
	defer s.mux.Unlock()
	for key, value := range txn.data {
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
	storeLock sync.RWMutex
	store     []*unitMemoryStore
	txnLock   sync.RWMutex
	txns      map[string]*unitMemoryStore
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		txns: make(map[string]*unitMemoryStore),
	}
}

func (s *MemoryStore) Clear() {
	s.storeLock.Lock()
	defer s.storeLock.Unlock()
	s.txnLock.Lock()
	defer s.txnLock.Unlock()
	s.store = nil
	s.txns = make(map[string]*unitMemoryStore)
}

func (s *MemoryStore) Clean(unitID string, unitType proto.UnitType) bool {
	s.storeLock.RLock()
	defer s.storeLock.RUnlock()
	unitStore := s.findUnitStore(unitID, unitType)
	if unitStore == nil {
		return true
	}
	unitStore.mux.RLock()
	defer unitStore.mux.RUnlock()
	return len(unitStore.txns) == 0
}

func (s *MemoryStore) Dirty(unitID string, unitType proto.UnitType) bool {
	s.storeLock.RLock()
	defer s.storeLock.RUnlock()
	unitStore := s.findUnitStore(unitID, unitType)
	if unitStore == nil {
		return true
	}
	unitStore.mux.RLock()
	defer unitStore.mux.RUnlock()
	return len(unitStore.txns) > 0
}

func (s *MemoryStore) Current(unitID string, unitType proto.UnitType) map[string][]byte {
	s.storeLock.RLock()
	defer s.storeLock.RUnlock()
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

func (s *MemoryStore) BeginTxn(request *proto.StoreBeginTxnRequest) (*proto.StoreBeginTxnResponse, error) {
	unitStore := s.findOrCreateUnitStore(request.UnitId, request.UnitType)
	txnID := unitStore.startTxn(request.Type == proto.StoreTxnType_READ_WRITE)
	s.txnLock.Lock()
	defer s.txnLock.Unlock()
	s.txns[txnID] = unitStore
	return &proto.StoreBeginTxnResponse{Id: txnID}, nil
}

func (s *MemoryStore) GetKey(request *proto.StoreGetKeyRequest) (*proto.StoreGetKeyResponse, error) {
	s.txnLock.RLock()
	unitStore, ok := s.txns[request.TxnId]
	s.txnLock.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid transaction id: %s", request.TxnId)
	}
	txn, ok := unitStore.txns[request.TxnId]
	if !ok {
		return nil, fmt.Errorf("error with transaction id: %s", request.TxnId)
	}
	val, ok := txn.data[request.Name]
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
	s.txnLock.RLock()
	unitStore, ok := s.txns[request.TxnId]
	s.txnLock.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid transaction id: %s", request.TxnId)
	}
	txn, ok := unitStore.txns[request.TxnId]
	if !ok {
		return nil, fmt.Errorf("error with transaction id: %s", request.TxnId)
	}
	if !txn.writeable {
		return nil, fmt.Errorf("not writeable transaction id: %s", request.TxnId)
	}
	var val unitMemoryValue
	val.value = request.Value
	val.deleted = false
	val.exp = time.Time{}
	val.touched = true
	if request.Ttl > 0 {
		val.exp = time.Now().UTC().Add(time.Duration(request.Ttl) * time.Millisecond)
	}
	txn.data[request.Name] = val
	return &proto.StoreSetKeyResponse{}, nil
}

func (s *MemoryStore) DeleteKey(request *proto.StoreDeleteKeyRequest) (*proto.StoreDeleteKeyResponse, error) {
	s.txnLock.RLock()
	unitStore, ok := s.txns[request.TxnId]
	s.txnLock.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid transaction id: %s", request.TxnId)
	}
	txn, ok := unitStore.txns[request.TxnId]
	if !ok {
		return nil, fmt.Errorf("error with transaction id: %s", request.TxnId)
	}
	if !txn.writeable {
		return nil, fmt.Errorf("not writeable transaction id: %s", request.TxnId)
	}
	val, ok := txn.data[request.Name]
	if !ok {
		// doesn't exist at all
		return &proto.StoreDeleteKeyResponse{}, nil
	}
	val.deleted = true
	val.touched = true
	txn.data[request.Name] = val
	return &proto.StoreDeleteKeyResponse{}, nil
}

func (s *MemoryStore) CommitTxn(request *proto.StoreCommitTxnRequest) (*proto.StoreCommitTxnResponse, error) {
	s.txnLock.RLock()
	unitStore, ok := s.txns[request.TxnId]
	s.txnLock.RUnlock()
	if !ok {
		return nil, fmt.Errorf("invalid transaction id: %s", request.TxnId)
	}
	txn, ok := unitStore.txns[request.TxnId]
	if !ok {
		return nil, fmt.Errorf("error with transaction id: %s", request.TxnId)
	}
	unitStore.commitTxn(txn)
	delete(unitStore.txns, request.TxnId)
	delete(s.txns, request.TxnId)
	return &proto.StoreCommitTxnResponse{}, nil
}

func (s *MemoryStore) DiscardTxn(request *proto.StoreDiscardTxnRequest) (*proto.StoreDiscardTxnResponse, error) {
	s.txnLock.RLock()
	unitStore, ok := s.txns[request.TxnId]
	s.txnLock.RUnlock()
	if ok {
		delete(unitStore.txns, request.TxnId)
		delete(s.txns, request.TxnId)
	}
	return &proto.StoreDiscardTxnResponse{}, nil
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
	s.storeLock.Lock()
	defer s.storeLock.Unlock()
	unitStore := s.findUnitStore(unitID, unitType)
	if unitStore == nil {
		unitStore = &unitMemoryStore{
			unitID:   unitID,
			unitType: unitType,
			data:     make(map[string]unitMemoryValue),
			txns:     make(map[string]*unitMemoryStoreTxn),
		}
		s.store = append(s.store, unitStore)
	}
	return unitStore
}
