package tlsutil

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// encodePair serialises a certificate and its EC private key to PEM, for the
// on-disk load test.
func encodePair(t *testing.T, cert tls.Certificate) (certPEM, keyPEM []byte) {
	t.Helper()
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("private key is %T, want *ecdsa.PrivateKey", cert.PrivateKey)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	return certPEM, keyPEM
}
