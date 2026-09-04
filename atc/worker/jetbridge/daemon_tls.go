package jetbridge

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
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

// DaemonTLSConfigured is the single predicate for "the ATC speaks mTLS to the
// artifact daemon": all three of the client certificate, its key, and the
// daemon CA must be named. Every site that decides whether TLS is on -- the
// scheme the ATC dials, the http.Client it dials with, the DaemonClient, and
// the ArtifactDaemonTLSEnabled the ATC derives at startup -- asks this
// function. When they were separate predicates a cert-only config made the ATC
// dial https at a plaintext daemon while presenting no certificate.
func DaemonTLSConfigured(certPath, keyPath, caCertPath string) bool {
	return certPath != "" && keyPath != "" && caCertPath != ""
}

// ValidateDaemonTLSFlags refuses at STARTUP a daemon TLS configuration that
// names some but not all of the triple. Such a config has no honest reading:
// mTLS cannot be established without all three, and silently falling back to
// plaintext would send artifact traffic in the clear to an operator who asked
// for TLS. The error names exactly the flags left unset.
func ValidateDaemonTLSFlags(certPath, keyPath, caCertPath string) error {
	var missing []string
	for _, f := range []struct {
		flag, value string
	}{
		{"kubernetes-artifact-daemon-tls-cert", certPath},
		{"kubernetes-artifact-daemon-tls-key", keyPath},
		{"kubernetes-artifact-daemon-tls-ca-cert", caCertPath},
	} {
		if f.value == "" {
			missing = append(missing, "--"+f.flag)
		}
	}
	if len(missing) == 0 || len(missing) == 3 {
		// All three set (mTLS) or none set (plaintext) are both coherent.
		return nil
	}
	return fmt.Errorf(
		"artifact daemon TLS is partially configured: %s must also be set. "+
			"mTLS with the artifact daemon needs the client certificate, its key, and the daemon CA together; "+
			"with only part of them the ATC would dial https at a daemon that may be listening in plaintext, "+
			"presenting no client certificate",
		strings.Join(missing, " and "),
	)
}

// daemonClientTLSConfigured reports whether the config has a complete set of
// client certificate paths for mTLS with the artifact daemon.
func daemonClientTLSConfigured(cfg Config) bool {
	return cfg.ArtifactDaemonTLSEnabled &&
		DaemonTLSConfigured(cfg.ArtifactDaemonTLSCert, cfg.ArtifactDaemonTLSKey, cfg.ArtifactDaemonTLSCACert)
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

// newDaemonStreamingHTTPClient returns an *http.Client for streaming artifact
// data to/from the daemon. Unlike newDaemonHTTPClient, it sets no
// whole-request timeout: http.Client.Timeout covers reading the entire
// response body, which would sever long-running tar streams mid-read
// (surfacing as "unexpected EOF" at the consumer). The handshake is still
// bounded via the transport's ResponseHeaderTimeout, so a dead daemon fails
// fast while an active stream can run as long as it needs.
func newDaemonStreamingHTTPClient(cfg Config) *http.Client {
	client := newDaemonHTTPClient(cfg, 0)
	client.Transport.(*http.Transport).ResponseHeaderTimeout = 30 * time.Second
	return client
}
