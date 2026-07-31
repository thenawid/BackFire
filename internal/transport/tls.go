package transport

import (
	"crypto/tls"

	"github.com/thenawid/backfire/config"
	"github.com/thenawid/backfire/internal/tlsutil"
)

// serverTLSConfig builds the TLS config for the wss listener. When the config
// names a certificate/key pair it is loaded from disk; otherwise a self-signed
// certificate is generated in memory, which is fine because the client
// authenticates the server by the shared token, not by the certificate chain.
func serverTLSConfig(cfg config.ServerConfig) (*tls.Config, error) {
	return tlsutil.Config(cfg.TLSCert, cfg.TLSKey)
}
