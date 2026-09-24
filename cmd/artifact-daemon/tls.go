package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/concourse/concourse/atc"
)

// daemonTLSMode reads the --tls-cert / --tls-key / --tls-ca-cert triple. All
// three named is mTLS; none is plaintext. A partial triple has no honest
// reading and is refused: this used to require all three and otherwise serve
// plaintext without a word, so an operator who dropped one flag got a daemon
// listening in the clear for an ATC and peers that dial https — the same rule
// the ATC (jetbridge.ValidateDaemonTLSFlags) and the Hangar output daemon
// enforce on their halves.
func daemonTLSMode(cert, key, ca string) (bool, error) {
	var missing []string
	for _, f := range []struct{ name, value string }{
		{"--tls-cert", cert},
		{"--tls-key", key},
		{"--tls-ca-cert", ca},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	switch len(missing) {
	case 0:
		return true, nil
	case 3:
		return false, nil
	}
	return false, fmt.Errorf("TLS is partially configured: %s must also be set. "+
		"mTLS needs the server certificate, its key and the client CA together; with only part of "+
		"them this daemon would listen in plaintext for callers that dial https",
		strings.Join(missing, " and "))
}

// BuildTLSConfig creates a TLS configuration for the daemon server with mTLS
// support. The server cert/key are used for the TLS listener. The CA cert is
// used to verify client certificates. ClientAuth is set to
// VerifyClientCertIfGiven so that health probes and init containers can
// connect without presenting a client cert — the requireClientCert middleware
// enforces client certs on protected routes.
func BuildTLSConfig(certPath, keyPath, caCertPath string) (*tls.Config, error) {
	tlsCfg := atc.DefaultTLSConfig()

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load server cert: %w", err)
	}
	tlsCfg.Certificates = []tls.Certificate{cert}

	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertPEM) {
		return nil, fmt.Errorf("failed to parse CA cert from %s", caCertPath)
	}
	tlsCfg.ClientCAs = caPool
	tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven

	return tlsCfg, nil
}

// requireClientCert is middleware that returns 401 if the request does not
// contain a verified client certificate. It should wrap routes that require
// mTLS authentication (e.g., /artifacts, /register, /stream-in,
// /resource-caches). Routes exempt from mTLS (e.g., /healthz, /resolve)
// should NOT be wrapped.
func (s *Server) requireClientCert(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			s.refuse(w, r, http.StatusUnauthorized, reasonClientCert, errors.New("client certificate required"))
			return
		}
		next(w, r)
	}
}
