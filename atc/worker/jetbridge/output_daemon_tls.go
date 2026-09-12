package jetbridge

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// The output plane's transport, and why it is not the artifact daemon's.
//
// These two daemons are deliberately not one process: Req 20 forbids them
// sharing a bucket, and a Kubernetes service account is Pod-wide, so the
// isolation is a second Pod under a second identity. They are also not one
// TRUST DOMAIN, and the code used to assume they were. Every output-plane call
// site asked `daemonURLScheme` -- a predicate over `artifactDaemon.tls.enabled`
// -- and dialed with `newDaemonHTTPClient`, which presents the ARTIFACT
// daemon's client certificate and trusts the ARTIFACT daemon's CA. Two
// consequences, both silent:
//
//   - with that switch at its default (false) the ATC dialed `http://` at a
//     listener that has no plaintext branch. `cmd/hangar-output-daemon`'s
//     control API is wrapped in `tls.NewListener` unconditionally and the chart
//     requires its TLS Secret, so the answer is "Client sent an HTTP request to
//     an HTTPS server" on every call, for a deployment that renders correctly
//     under the chart's own documented values;
//   - and with it on, the ATC presented a certificate issued by one CA to a
//     daemon whose ClientCAs pool is the other. The control routes refuse an
//     operation whose request carries no VERIFIED peer certificate, so the two
//     halves handshake and then refuse everything.
//
// So the output plane gets its own triple, its own server name and its own
// client. There is exactly ONE supported mode -- TLS -- because the daemon can
// serve exactly one, and the startup validation below refuses a plane that is
// enabled without it rather than falling back to a scheme nothing listens on.

// outputDaemonURLScheme is the scheme every ATC-side caller of the output
// daemon uses.
//
// It is a function returning a constant rather than a constant, so that this
// comment sits at the one place the decision is made and every call site reads
// the same spelling. The daemon has no plaintext branch; there is no
// configuration under which the answer is "http", and the flag validation is
// what makes that safe rather than optimistic.
func outputDaemonURLScheme() string {
	return "https"
}

// outputWgetTLSOptions are the BusyBox wget options the capture control init
// needs to reach its node's output daemon.
//
// --no-check-certificate, always, and for the reason `wgetTLSOptions` gives for
// the artifact daemon: the init container dials its own node by IP from the
// Downward API, and an IP is not a certificate SAN, so server authentication
// cannot succeed however correctly the deployment is provisioned. What the
// transport buys the hold is confidentiality for the one-shot capability it
// carries in a header. The AUTHORIZATION is that signed, facet-scoped,
// single-use grant, verified by the daemon, and it is unchanged by this.
//
// The ATC's own off-node calls are a different matter and DO verify: they are
// given a server name the operator puts in the daemon's certificate
// (OutputDaemonTLSServerName), because an ATC that skipped verification would
// accept any listener on any node IP as the authority for that node's ledger.
func outputWgetTLSOptions() string {
	return "--no-check-certificate"
}

// OutputDaemonTLSConfigured is the single predicate for "the ATC can speak to
// the output daemon at all": the client certificate, its key and the daemon CA
// must all be named. Anything less is not a weaker configuration, it is a
// plane the ATC cannot make one call on.
func OutputDaemonTLSConfigured(certPath, keyPath, caCertPath string) bool {
	return certPath != "" && keyPath != "" && caCertPath != ""
}

// ValidateOutputDaemonTLSFlags refuses at STARTUP an output plane whose client
// credential is missing or partial.
//
// This is a refusal rather than a fallback on purpose. The artifact daemon has
// two honest modes and its validation therefore accepts "none of the three";
// the output daemon has one, so "none of the three" is an ATC that will dial
// an HTTPS listener with no certificate and be refused by every control route
// it calls -- at the first capture, in production, rather than at startup.
func ValidateOutputDaemonTLSFlags(certPath, keyPath, caCertPath string) error {
	var missing []string
	for _, f := range []struct {
		flag, value string
	}{
		{"kubernetes-hangar-output-tls-cert", certPath},
		{"kubernetes-hangar-output-tls-key", keyPath},
		{"kubernetes-hangar-output-tls-ca-cert", caCertPath},
	} {
		if f.value == "" {
			missing = append(missing, "--"+f.flag)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf(
		"the Hangar output plane is enabled and %s unset. "+
			"The output daemon's control API is TLS-only and refuses every control operation "+
			"whose request carries no verified peer certificate, so an ATC without a client "+
			"certificate of its own cannot hold a source, issue a writer ticket, seal, publish "+
			"or grant a read. This is the output plane's OWN certificate and CA, issued in the "+
			"same trust domain as hangarOutput.daemon.tls.existingSecret: the artifact daemon's "+
			"is a different daemon, a different bucket and a different identity",
		strings.Join(missing, " and "),
	)
}

// newOutputDaemonHTTPClient returns the *http.Client the ATC calls the output
// daemon's control API with: it presents the output plane's client certificate
// and trusts the output plane's CA.
//
// The server name is configured rather than derived. The output daemon has no
// Service -- it is reached at `<node InternalIP>:<port>` -- and a node IP can
// be in no certificate issued before the node existed, so verification is
// against a name the operator puts in the daemon's certificate and the chart
// hands to both halves. Without it the ATC would have to skip verification and
// accept any listener on any node IP as the authority for that node's ledger.
func newOutputDaemonHTTPClient(cfg Config, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if OutputDaemonTLSConfigured(
		cfg.OutputDaemonTLSCert, cfg.OutputDaemonTLSKey, cfg.OutputDaemonTLSCACert,
	) {
		tlsConfig, err := loadDaemonClientTLS(
			cfg.OutputDaemonTLSCert,
			cfg.OutputDaemonTLSKey,
			cfg.OutputDaemonTLSCACert,
			cfg.OutputDaemonTLSServerName,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"WARNING: Hangar output daemon mTLS: %v — every control call will be refused\n", err)
		} else {
			transport.TLSClientConfig = tlsConfig
		}
	}

	return &http.Client{Timeout: timeout, Transport: transport}
}
