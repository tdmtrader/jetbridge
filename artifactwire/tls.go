package artifactwire

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// TLS is the client half of mTLS with the daemon: the certificate the ATC
// presents, its key, the CA that signed the daemon, and the name to verify the
// daemon's certificate against.
//
// A zero value means plaintext. A partial triple has no honest reading, and
// the flag validation that refuses one lives with the flags; here Configured
// is simply "all three named".
type TLS struct {
	CertPath   string
	KeyPath    string
	CACertPath string

	// ServerName is the DNS name to verify the daemon's server certificate
	// against. The ATC dials daemon pods by their pod IP, which cannot be a
	// certificate SAN; the chart-issued server certificate instead carries the
	// headless service DNS name. Empty falls back to the dial host.
	ServerName string
}

// Configured reports whether every part of the triple is named. It is the one
// predicate behind both the scheme the client dials and the certificate it
// presents, so they cannot disagree: https always presents a certificate.
func (t TLS) Configured() bool {
	return t.CertPath != "" && t.KeyPath != "" && t.CACertPath != ""
}

// load builds the tls.Config that presents the client certificate and trusts
// the daemon CA.
func (t TLS) load() (*tls.Config, error) {
	clientCert, err := tls.LoadX509KeyPair(t.CertPath, t.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load daemon client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(t.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("read daemon CA certificate: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse daemon CA certificate: no certificates in %s", t.CACertPath)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   t.ServerName,
	}, nil
}
