// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package client

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	protobuf "github.com/golang/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/elastic/elastic-agent-client/v7/pkg/proto"
)

func TestNewFromReader_Empty(t *testing.T) {
	_, err := NewFromReader(bytes.NewBuffer([]byte("")), nil)
	assert.Error(t, err)
}

func TestNewFromReader_BadProto(t *testing.T) {
	_, err := NewFromReader(bytes.NewBuffer([]byte("bad proto")), nil)
	assert.Error(t, err)
}

func TestNewFromReader_Connects(t *testing.T) {
	ca, err := NewCA()
	require.NoError(t, err)
	peer, err := ca.GeneratePair()
	require.NoError(t, err)
	peerCert, err := peer.Certificate()
	require.NoError(t, err)

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(ca.caPEM)
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{peerCert},
		ClientCAs:    caCertPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	})

	var m sync.Mutex
	token := "expected_token"
	gotValid := false
	srv := StubServer{
		CheckinImpl: func(observed *proto.StateObserved) *proto.StateExpected {
			m.Lock()
			defer m.Unlock()

			if observed.Token == token {
				gotValid = true
				return &proto.StateExpected{
					State:          proto.StateExpected_RUNNING,
					ConfigStateIdx: 1,
					Config:         "config",
				}
			}
			// disconnect
			return nil
		},
		ActionImpl: func(response *proto.ActionResponse) error {
			// actions not tested here
			return nil
		},
		actions: make(chan *performAction, 100),
	}
	require.NoError(t, srv.Start(grpc.Creds(creds)))
	defer srv.Stop()

	connInfo := &proto.ConnInfo{
		Addr:     fmt.Sprintf(":%d", srv.Port),
		Token:    token,
		CaCert:   ca.caPEM,
		PeerCert: peer.Crt,
		PeerKey:  peer.Key,
	}
	connInfoBytes, err := protobuf.Marshal(connInfo)
	require.NoError(t, err)
	connInfoReader := bytes.NewReader(connInfoBytes)

	impl := &StubClientImpl{}
	validClient, err := NewFromReader(connInfoReader, impl)
	require.NoError(t, err)
	require.NoError(t, validClient.Start(context.Background()))
	defer validClient.Stop()
	require.NoError(t, waitFor(func() error {
		m.Lock()
		defer m.Unlock()

		if !gotValid {
			return fmt.Errorf("server never received valid token")
		}
		return nil
	}))
}

type CertificateAuthority struct {
	caCert     *x509.Certificate
	privateKey crypto.PrivateKey
	caPEM      []byte
}

type Pair struct {
	Crt []byte
	Key []byte
}

func NewCA() (*CertificateAuthority, error) {
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1653),
		Subject: pkix.Name{
			Organization: []string{"elastic-fleet"},
			CommonName:   "localhost",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	publicKey := &privateKey.PublicKey
	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, publicKey, privateKey)
	if err != nil {
		return nil, err
	}

	var pubKeyBytes, privateKeyBytes []byte

	certOut := bytes.NewBuffer(pubKeyBytes)
	keyOut := bytes.NewBuffer(privateKeyBytes)

	// Public key
	err = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: caBytes})
	if err != nil {
		return nil, err
	}

	// Private key
	err = pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	if err != nil {
		return nil, err
	}

	// prepare tls
	caPEM := certOut.Bytes()
	caTLS, err := tls.X509KeyPair(caPEM, keyOut.Bytes())
	if err != nil {
		return nil, err
	}

	caCert, err := x509.ParseCertificate(caTLS.Certificate[0])
	if err != nil {
		return nil, err
	}

	return &CertificateAuthority{
		privateKey: caTLS.PrivateKey,
		caCert:     caCert,
		caPEM:      caPEM,
	}, nil
}

// GeneratePair generates child certificate
func (c *CertificateAuthority) GeneratePair() (*Pair, error) {
	// Prepare certificate
	certTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1658),
		Subject: pkix.Name{
			Organization: []string{"elastic-fleet"},
			CommonName:   "localhost",
		},
		NotBefore:   time.Now(),
		NotAfter:    time.Now().AddDate(10, 0, 0),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageDigitalSignature,
	}
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	publicKey := &privateKey.PublicKey

	// Sign the certificate
	certBytes, err := x509.CreateCertificate(rand.Reader, certTemplate, c.caCert, publicKey, c.privateKey)
	if err != nil {
		return nil, err
	}

	var pubKeyBytes, privateKeyBytes []byte

	certOut := bytes.NewBuffer(pubKeyBytes)
	keyOut := bytes.NewBuffer(privateKeyBytes)

	// Public key
	err = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes})
	if err != nil {
		return nil, err
	}

	// Private key
	err = pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	if err != nil {
		return nil, err
	}

	return &Pair{
		Crt: certOut.Bytes(),
		Key: keyOut.Bytes(),
	}, nil
}

func (p *Pair) Certificate() (tls.Certificate, error) {
	return tls.X509KeyPair(p.Crt, p.Key)
}
