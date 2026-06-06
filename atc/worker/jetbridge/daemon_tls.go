package jetbridge

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"
)

// daemonURLScheme returns the URL scheme to use when talking to the artifact
// daemon: "https" when mTLS is enabled, "http" otherwise. It mirrors the
// scheme the daemon server itself selects from the same flag, so ATC-side
// callers address the daemon over the same protocol it is listening on.
func daemonURLScheme(cfg Config) string {
	if cfg.ArtifactDaemonTLSEnabled {
		return "https"
	}
	return "http"
}

// daemonClientTLSConfigured reports whether the config has a complete set of
// client certificate paths for mTLS with the artifact daemon.
func daemonClientTLSConfigured(cfg Config) bool {
	return cfg.ArtifactDaemonTLSEnabled &&
		cfg.ArtifactDaemonTLSCert != "" &&
		cfg.ArtifactDaemonTLSKey != "" &&
		cfg.ArtifactDaemonTLSCACert != ""
}

// daemonTLSServerName returns the DNS name to verify the daemon's server
// certificate against. ATC dials daemon pods by their (dynamic) pod IP, which
// cannot be a cert SAN; the chart-issued server cert instead carries the
// headless service DNS name. Setting this as the TLS ServerName makes Go verify
// against that SAN regardless of the IP dialed. Returns "" when the service or
// namespace is unknown (verification then falls back to the dial host).
func daemonTLSServerName(cfg Config) string {
	if cfg.ArtifactDaemonService == "" || cfg.Namespace == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s.svc", cfg.ArtifactDaemonService, cfg.Namespace)
}

// loadDaemonClientTLS builds a *tls.Config that presents the configured client
// certificate and trusts the daemon CA, for mTLS with the artifact daemon. It
// is the single source of truth for the ATC-side daemon TLS config, shared by
// NewDaemonClient and newDaemonHTTPClient. serverName (when non-empty) is the
// SAN to verify the daemon's server cert against — required because daemons are
// dialed by pod IP, not by a name in the cert.
func loadDaemonClientTLS(certPath, keyPath, caCertPath, serverName string) (*tls.Config, error) {
	clientCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load daemon client cert: %w", err)
	}
	caPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read daemon CA cert: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse daemon CA cert: no certificates in %s", caCertPath)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   serverName,
	}, nil
}

// newDaemonHTTPClient returns an *http.Client for talking to the artifact
// daemon. When mTLS is configured it presents the client certificate and
// trusts the daemon CA, so requests to protected daemon endpoints
// (/artifacts/*, /stream-in/*, /register, /resource-caches/*) authenticate
// successfully. The scheme returned by daemonURLScheme matches.
//
// If the certs are configured but fail to load, it logs a warning to stderr
// and returns a plain client; the subsequent request then fails loudly against
// the HTTPS-only daemon, surfacing the misconfiguration rather than hiding it.
func newDaemonHTTPClient(cfg Config, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if daemonClientTLSConfigured(cfg) {
		tlsConfig, err := loadDaemonClientTLS(
			cfg.ArtifactDaemonTLSCert,
			cfg.ArtifactDaemonTLSKey,
			cfg.ArtifactDaemonTLSCACert,
			daemonTLSServerName(cfg),
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: artifact daemon mTLS: %v — falling back to plain HTTP\n", err)
		} else {
			transport.TLSClientConfig = tlsConfig
		}
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}
