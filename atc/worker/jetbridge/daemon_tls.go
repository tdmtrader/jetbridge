package jetbridge

import (
	"fmt"
	"os"
	"strings"

	"github.com/concourse/concourse/artifactwire"
)

// DaemonTLSConfigured is the single predicate for "the ATC speaks mTLS to the
// artifact daemon": all three of the client certificate, its key, and the
// daemon CA must be named. It is the same predicate the wire module applies
// to its TLS triple, so the ArtifactDaemonTLSEnabled the ATC derives at
// startup and the scheme the wire client dials cannot disagree. When they
// were separate predicates a cert-only config made the ATC dial https at a
// plaintext daemon while presenting no certificate.
func DaemonTLSConfigured(certPath, keyPath, caCertPath string) bool {
	return artifactwire.TLS{CertPath: certPath, KeyPath: keyPath, CACertPath: caCertPath}.Configured()
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

// daemonTLSServerName returns the DNS name to verify the daemon's server
// certificate against. ATC dials daemon pods by their (dynamic) pod IP, which
// cannot be a cert SAN; the chart-issued server cert instead carries the
// headless service DNS name. Setting this as the TLS ServerName makes Go verify
// against that SAN regardless of the IP dialed. Returns "" when the service or
// namespace is unknown (verification then falls back to the dial host). The
// daemon's namespace can differ from the namespace where this config schedules
// task pods; an unset override preserves the colocated deployment behavior.
func daemonTLSServerName(cfg Config) string {
	namespace := cfg.ArtifactDaemonNamespace
	if namespace == "" {
		namespace = cfg.Namespace
	}
	if cfg.ArtifactDaemonService == "" || namespace == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s.svc", cfg.ArtifactDaemonService, namespace)
}

// wireTLS is the one adapter from Config to the wire module's triple.
func wireTLS(cfg Config) artifactwire.TLS {
	if !cfg.ArtifactDaemonTLSEnabled {
		return artifactwire.TLS{}
	}
	return artifactwire.TLS{
		CertPath:   cfg.ArtifactDaemonTLSCert,
		KeyPath:    cfg.ArtifactDaemonTLSKey,
		CACertPath: cfg.ArtifactDaemonTLSCACert,
		ServerName: daemonTLSServerName(cfg),
	}
}

// newWireClient builds the client every ATC-side caller reaches a daemon
// with.
//
// A Config that asks for TLS gets TLS or nothing. If the triple is partial
// or its files fail to load, the client is a misconfigured one: it keeps the
// https scheme the deployment asked for and refuses every request naming the
// reason, so the misconfiguration surfaces at the first call rather than as
// a plaintext request that a TLS-only daemon turns away, and never as an
// unauthenticated read that happened to work.
func newWireClient(cfg Config) *artifactwire.Client {
	triple := wireTLS(cfg)
	if cfg.ArtifactDaemonTLSEnabled && !triple.Configured() {
		return artifactwire.Misconfigured(cfg.ArtifactDaemonPort, ValidateDaemonTLSFlags(
			cfg.ArtifactDaemonTLSCert, cfg.ArtifactDaemonTLSKey, cfg.ArtifactDaemonTLSCACert))
	}
	client, err := artifactwire.NewClient(cfg.ArtifactDaemonPort, triple)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: artifact daemon mTLS: %v — every artifact daemon request will be refused\n", err)
		return artifactwire.Misconfigured(cfg.ArtifactDaemonPort, err)
	}
	return client
}
