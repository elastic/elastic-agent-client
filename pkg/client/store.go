// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"context"
	"errors"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

// ErrStoreTxnReadOnly is an error when write actions are performed on a read-only transaction.
var ErrStoreTxnReadOnly = errors.New("txn is read-only")

// ErrStoreTxnBroken is an error when an action on a transaction has failed causing the whole transaction to be broken.
var ErrStoreTxnBroken = errors.New("txn is broken")

// ErrStoreTxnDiscarded is an error when Commit is called on an already discarded transaction.
var ErrStoreTxnDiscarded = errors.New("txn was already discarded")

// ErrStoreTxnCommitted is an error action is performed on an already committed transaction.
var ErrStoreTxnCommitted = errors.New("txn was already committed")

// StoreTxnClient provides actions allowed for a started transaction.
type StoreTxnClient interface {
	// GetKey fetches a value from its key in the store.
	GetKey(ctx context.Context, name string) ([]byte, bool, error)
	// SetKey sets a value for a key in the store.
	SetKey(ctx context.Context, name string, value []byte, ttl uint64) error
	// DeleteKey deletes a key from the store.
	DeleteKey(ctx context.Context, name string) error
	// Commit commits the transaction.
	Commit(ctx context.Context) error
	// Discard discards the transaction.
	//
	// Can be called even if already committed, in that case it does nothing.
	Discard(ctx context.Context) error
}

// StoreClient provides access to the key-value store from Elastic Agent for this unit.
type StoreClient interface {
	// BeginTxn starts a transaction for the key-value store.
	BeginTxn(ctx context.Context, write bool) (StoreTxnClient, error)
}

type storeClientTxn struct {
	client *storeClient
	txnId string
	write bool

	brokenErr error
	discarded bool
	committed bool
}

type storeClient struct {
	client *clientV2
	unitId string
	unitType UnitType
}

// BeginTxn starts a transaction for the key-value store.
func (c *storeClient) BeginTxn(ctx context.Context, write bool) (StoreTxnClient, error) {
	txnType := proto.StoreTxnType_READ_ONLY
	if write {
		txnType = proto.StoreTxnType_READ_WRITE
	}
	res, err := c.client.storeClient.BeginTxn(ctx, &proto.StoreBeginTxnRequest{
		Token:    c.client.token,
		UnitId:   c.unitId,
		UnitType: proto.UnitType(c.unitType),
		Type:     txnType,
	})
	if err != nil {
		return nil, err
	}
	return &storeClientTxn{
		client: c,
		txnId: res.Id,
		write: write,
	}, nil
}

// GetKey fetches a value from its key in the store.
func (c *storeClientTxn) GetKey(ctx context.Context, name string) ([]byte, bool, error) {
	if c.brokenErr != nil {
		return nil, false, ErrStoreTxnBroken
	}
	if c.discarded {
		return nil, false, ErrStoreTxnDiscarded
	}
	if c.committed {
		return nil, false, ErrStoreTxnCommitted
	}
	res, err := c.client.client.storeClient.GetKey(ctx, &proto.StoreGetKeyRequest{
		Token:    c.client.client.token,
		TxnId:    c.txnId,
		Name:     name,
	})
	if err != nil {
		c.brokenErr = err
		return nil, false, err
	}
	switch res.Status {
	case proto.StoreGetKeyResponse_FOUND:
		return res.Value, true, nil
	case proto.StoreGetKeyResponse_NOT_FOUND:
		return nil, false, nil
	}
	err = errors.New("unknown StoreGetKeyResponseStatus")
	c.brokenErr = err
	return nil, false, err
}

// SetKey sets a value for a key in the store.
func (c *storeClientTxn) SetKey(ctx context.Context, name string, value []byte, ttl uint64) error {
	if c.brokenErr != nil {
		return ErrStoreTxnBroken
	}
	if c.discarded {
		return ErrStoreTxnDiscarded
	}
	if c.committed {
		return ErrStoreTxnCommitted
	}
	if !c.write {
		return ErrStoreTxnReadOnly
	}
	_, err := c.client.client.storeClient.SetKey(ctx, &proto.StoreSetKeyRequest{
		Token:    c.client.client.token,
		TxnId:    c.txnId,
		Name:     name,
		Value:    value,
		Ttl:      ttl,
	})
	if err != nil {
		c.brokenErr = err
		return err
	}
	return nil
}

// DeleteKey deletes a key from the store.
func (c *storeClientTxn) DeleteKey(ctx context.Context, name string) error {
	if c.brokenErr != nil {
		return ErrStoreTxnBroken
	}
	if c.discarded {
		return ErrStoreTxnDiscarded
	}
	if c.committed {
		return ErrStoreTxnCommitted
	}
	if !c.write {
		return ErrStoreTxnReadOnly
	}
	_, err := c.client.client.storeClient.DeleteKey(ctx, &proto.StoreDeleteKeyRequest{
		Token:    c.client.client.token,
		TxnId:    c.txnId,
		Name:     name,
	})
	if err != nil {
		c.brokenErr = err
		return err
	}
	return err
}

// Commit commits the transaction.
func (c *storeClientTxn) Commit(ctx context.Context) error {
	if c.brokenErr != nil {
		return ErrStoreTxnBroken
	}
	if c.discarded {
		return ErrStoreTxnDiscarded
	}
	if c.committed {
		return nil
	}
	_, err := c.client.client.storeClient.CommitTxn(ctx, &proto.StoreCommitTxnRequest{
		Token:    c.client.client.token,
		TxnId:    c.txnId,
	})
	if err != nil {
		c.brokenErr = err
		return err
	}
	c.committed = true
	return nil
}

// Discard discards the transaction.
//
// Can be called even if already committed, in that case it does nothing.
func (c *storeClientTxn) Discard(ctx context.Context) error {
	if c.brokenErr != nil {
		return ErrStoreTxnBroken
	}
	if c.discarded || c.committed {
		return nil
	}
	_, err := c.client.client.storeClient.DiscardTxn(ctx, &proto.StoreDiscardTxnRequest{
		Token:    c.client.client.token,
		TxnId:    c.txnId,
	})
	if err != nil {
		c.brokenErr = err
		return err
	}
	c.discarded = true
	return nil
}
