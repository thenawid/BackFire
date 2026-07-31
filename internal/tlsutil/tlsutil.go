// Package tlsutil builds the TLS material shared by the wss transport and the
// web panel: a certificate loaded from disk when the operator supplies one, or a
// self-signed certificate generated in memory otherwise.
//
// A self-signed certificate is fine for both callers — the wss transport
// authenticates peers by the shared token rather than the chain, and the panel
// is reached by an operator who accepts the certificate once (or puts a real one
// in front). It saves every install from having to obtain a CA-signed cert just
// to turn encryption on.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// SelfSigned mints a throwaway P-256 certificate valid for a decade. Any hosts
// given are recorded as subject-alternative names, so a client that does verify
// (a browser opening the panel by IP, say) matches the address instead of only
// failing on the name.
func SelfSigned(hosts ...string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "backfire"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// Config returns a server TLS config. When certFile and keyFile are both set it
// loads that pair; otherwise it generates a self-signed certificate covering the
// given SAN hosts.
func Config(certFile, keyFile string, selfSignedHosts ...string) (*tls.Config, error) {
	var cert tls.Certificate
	var err error
	if certFile != "" && keyFile != "" {
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load tls keypair: %w", err)
		}
	} else {
		cert, err = SelfSigned(selfSignedHosts...)
		if err != nil {
			return nil, fmt.Errorf("generate self-signed cert: %w", err)
		}
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
