package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestSelfSignedHasSANs(t *testing.T) {
	cert, err := SelfSigned("localhost", "203.0.113.4")
	if err != nil {
		t.Fatalf("SelfSigned: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "localhost" {
		t.Errorf("DNS names = %v, want [localhost]", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.ParseIP("203.0.113.4")) {
		t.Errorf("IP SANs = %v, want [203.0.113.4]", leaf.IPAddresses)
	}
	// Blank hosts must be skipped, not recorded as empty names.
	cert2, _ := SelfSigned("", "localhost")
	leaf2, _ := x509.ParseCertificate(cert2.Certificate[0])
	if len(leaf2.DNSNames) != 1 {
		t.Errorf("a blank host was not skipped: %v", leaf2.DNSNames)
	}
}

func TestConfigSelfSignedWhenNoFiles(t *testing.T) {
	cfg, err := Config("", "", "localhost")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected one certificate, got %d", len(cfg.Certificates))
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want TLS 1.2", cfg.MinVersion)
	}
}

func TestConfigLoadsSuppliedPair(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "c.pem")
	keyPath := filepath.Join(dir, "k.pem")

	// Write a self-signed pair out and read it back through Config.
	cert, _ := SelfSigned("localhost")
	writePair(t, certPath, keyPath, cert)

	cfg, err := Config(certPath, keyPath)
	if err != nil {
		t.Fatalf("Config with a supplied pair: %v", err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatal("supplied pair did not load")
	}
}

func TestConfigErrorsOnMissingFile(t *testing.T) {
	if _, err := Config("/nonexistent/c.pem", "/nonexistent/k.pem"); err == nil {
		t.Fatal("expected an error for a missing certificate file")
	}
}

// writePair serialises a certificate and its EC key to PEM files.
func writePair(t *testing.T, certPath, keyPath string, cert tls.Certificate) {
	t.Helper()
	// Round-trip through the standard encoders the same way an operator's tools
	// would, so the test exercises a real on-disk pair.
	certPEM, keyPEM := encodePair(t, cert)
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}
