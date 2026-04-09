package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"

	"github.com/concourse/concourse/atc"
)

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
func requireClientCert(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
