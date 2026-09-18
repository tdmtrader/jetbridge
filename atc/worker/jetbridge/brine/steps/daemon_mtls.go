package steps

// The mTLS cases run the production artifact-daemon with real certificates,
// registered files and HTTPS. Real warm ownership lives in real_warm.go.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// -----------------------------------------------------------------------
// Domain states — mTLS
// -----------------------------------------------------------------------

// MTLSPlan is a TLS-speaking artifact daemon and the ATC that will talk to it,
// under description. Nothing is wired until the When step: the certificate
// names must be settled before its real TLS listener starts.
type MTLSPlan struct {
	Ctx       context.Context
	Namespace string
	Service   string

	// Artifacts is what the daemon holds. A key it does not hold is a 404,
	// which is how a scenario can tell arrival from mere connectivity.
	Artifacts map[string]string

	// RequireClientCert verifies the Given's authentication premise with a
	// real unauthenticated read of a held artifact. TLS artifact routes are
	// always protected in the production daemon.
	RequireClientCert bool

	// CertOmitsAddress narrows the daemon's own certificate to the service DNS
	// name alone, dropping the address it is dialled at. That is the deployed
	// shape — pod IPs are handed out at schedule time and can be in no SAN —
	// and it is what makes the ATC's ServerName load-bearing. A scenario that
	// is about something else leaves it off, so the certificate names both and
	// the hostname question cannot be what decided the outcome.
	CertOmitsAddress bool

	// ATCHasCerts and ATCCertsMissing are the two configurations an operator
	// actually ends up in: the certificate files are where the chart put them,
	// or a bad rollout means they are not there at all.
	ATCHasCerts     bool
	ATCCertsMissing bool
}

// MTLSFetch is what the consumer got. The error is a value, so a refusal is
// assertable rather than fatal to the scenario.
type MTLSFetch struct {
	Raw     []byte
	Err     error
	Message string
}

// -----------------------------------------------------------------------
// The TLS material
// -----------------------------------------------------------------------

// mtlsMaterial is one small PKI: a CA, a server certificate the daemon serves,
// and a client certificate the ATC presents. Real certificates verified by
// real Go TLS — there is no other way to make "the handshake succeeded" mean
// anything.
type mtlsMaterial struct {
	caPEM      []byte
	clientCert []byte
	clientKey  []byte
	serverCert []byte
	serverKey  []byte
}

func mintMTLSMaterial(dnsName string, ips []net.IP) (mtlsMaterial, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("generate CA key: %w", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "brine-daemon-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("create CA certificate: %w", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("parse CA certificate: %w", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	serverPEM, serverKeyPEM, err := signLeaf(ca, caKey, 2, "daemon-server",
		x509.ExtKeyUsageServerAuth, []string{dnsName}, ips)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("server certificate: %w", err)
	}
	clientPEM, clientKeyPEM, err := signLeaf(ca, caKey, 3, "atc-client",
		x509.ExtKeyUsageClientAuth, nil, nil)
	if err != nil {
		return mtlsMaterial{}, fmt.Errorf("client certificate: %w", err)
	}

	return mtlsMaterial{
		caPEM:      caPEM,
		clientCert: clientPEM,
		clientKey:  clientKeyPEM,
		serverCert: serverPEM,
		serverKey:  serverKeyPEM,
	}, nil
}

func signLeaf(
	ca *x509.Certificate,
	caKey *ecdsa.PrivateKey,
	serial int64,
	commonName string,
	usage x509.ExtKeyUsage,
	dnsNames []string,
	ips []net.IP,
) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		nil
}

// writeMTLSCredentials writes the real daemon and ATC credentials into one
// scenario-owned directory. Missing ATC paths are derived beneath this owned
// directory too, so another process cannot accidentally satisfy the setup.
func writeMTLSCredentials(m mtlsMaterial) (string, error) {
	dir, err := os.MkdirTemp("", "brine-daemon-mtls-")
	if err != nil {
		return "", fmt.Errorf("make certificate directory: %w", err)
	}
	for name, body := range map[string][]byte{
		"client.crt": m.clientCert,
		"client.key": m.clientKey,
		"server.crt": m.serverCert,
		"server.key": m.serverKey,
		"ca.crt":     m.caPEM,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}
	return dir, nil
}

// serve uses an independent, verified TLS client for fixture setup. It must
// not call the ATC TLS helper under test: an ATC mutation should break the
// consumer's read, not the daemon's readiness probe or artifact registration.
func (p MTLSPlan) serve() (_ *realDaemon, _ string, err error) {
	dnsName := fmt.Sprintf("%s.%s.svc", p.Service, p.Namespace)
	var ips []net.IP
	if !p.CertOmitsAddress {
		ips = []net.IP{net.ParseIP("127.0.0.1")}
	}
	material, err := mintMTLSMaterial(dnsName, ips)
	if err != nil {
		return nil, "", err
	}
	dir, err := writeMTLSCredentials(material)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	cert, err := tls.X509KeyPair(material.clientCert, material.clientKey)
	if err != nil {
		return nil, "", fmt.Errorf("assemble setup client certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(material.caPEM) {
		return nil, "", fmt.Errorf("the minted CA certificate does not parse as PEM")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:   tls.VersionTLS12,
		RootCAs:      pool,
		ServerName:   dnsName,
		Certificates: []tls.Certificate{cert},
	}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	daemon, err := startRealDaemonWithClient("https", client,
		"--tls-cert", filepath.Join(dir, "server.crt"),
		"--tls-key", filepath.Join(dir, "server.key"),
		"--tls-ca-cert", filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if err != nil {
			_ = daemon.stop()
		}
	}()
	for key, body := range p.Artifacts {
		if !filepath.IsLocal(key) || key == "." {
			return nil, "", fmt.Errorf("artifact key must name a local file: %q", key)
		}
		path := filepath.Join(daemon.Root, "produced", key)
		// The daemon's existing file-serving path preserves these exact
		// bytes. Its directory/tar behavior has separate behavioral cases.
		if err := writeArtifactFile(path, body); err != nil {
			return nil, "", err
		}
		if err := registerDaemonArtifact(p.Ctx, client, daemon.URL, key, path); err != nil {
			return nil, "", err
		}
	}
	if p.RequireClientCert {
		if len(p.Artifacts) == 0 {
			return nil, "", fmt.Errorf("client authentication requires a held artifact to probe")
		}
		anonymous := transport.Clone()
		anonymous.TLSClientConfig.Certificates = nil
		guest := &http.Client{Transport: anonymous, Timeout: 5 * time.Second}
		defer guest.CloseIdleConnections()
		for key := range p.Artifacts {
			req, err := http.NewRequestWithContext(p.Ctx, http.MethodGet, daemon.URL+"/artifacts/"+key, nil)
			if err != nil {
				return nil, "", err
			}
			resp, err := guest.Do(req)
			if err != nil {
				return nil, "", fmt.Errorf("probe unauthenticated artifact read: %w", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				return nil, "", fmt.Errorf("daemon served an unauthenticated artifact read with status %d, want 401", resp.StatusCode)
			}
		}
	}
	return daemon, dir, nil
}

// config is the ATC side: TLS on, and the certificate paths in whichever of
// the two states the scenario described.
func (p MTLSPlan) config(port int, cert, key, ca string) jetbridge.Config {
	return jetbridge.Config{
		Namespace:                p.Namespace,
		ArtifactDaemonService:    p.Service,
		ArtifactDaemonPort:       port,
		ArtifactDaemonTLSEnabled: true,
		ArtifactDaemonTLSCert:    cert,
		ArtifactDaemonTLSKey:     key,
		ArtifactDaemonTLSCACert:  ca,
	}
}

// -----------------------------------------------------------------------
// Steps
// -----------------------------------------------------------------------

// DaemonMTLSDefinitions covers the mTLS data plane and which node a durable
// warm lands on. Nothing here names a transport field, a URL or a request
// count.
func DaemonMTLSDefinitions() []brine.StepDefinition {
	return append([]brine.StepDefinition{

		// --- mTLS: describing the daemon and the ATC ---

		brine.DefineMap[brine.Empty, MTLSPlan](
			"an artifact daemon serving over TLS",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (MTLSPlan, error) {
				return MTLSPlan{
					Ctx:       context.Background(),
					Namespace: "cicd",
					Service:   "artifact-daemon",
					Artifacts: map[string]string{},
				}, nil
			},
		),

		Refine[MTLSPlan]("it only serves clients whose certificate it can verify",
			func(in MTLSPlan, _ Args) MTLSPlan {
				in.RequireClientCert = true
				return in
			}),

		// The deployed shape. The chart issues the daemon a certificate for the
		// headless service; the address the ATC dials is a pod IP handed out
		// at schedule time, and is in no SAN.
		Refine[MTLSPlan]("its certificate names only the service, not the address it is dialled at",
			func(in MTLSPlan, _ Args) MTLSPlan {
				in.CertOmitsAddress = true
				return in
			}),

		Refine[MTLSPlan]("the daemon holds the artifact {string} containing {string}",
			func(in MTLSPlan, a Args) MTLSPlan {
				artifacts := map[string]string{}
				for k, v := range in.Artifacts {
					artifacts[k] = v
				}
				artifacts[a.String(0)] = a.String(1)
				in.Artifacts = artifacts
				return in
			}),

		Refine[MTLSPlan]("the ATC has its client certificate and the daemon's CA",
			func(in MTLSPlan, _ Args) MTLSPlan {
				in.ATCHasCerts = true
				return in
			}),

		Refine[MTLSPlan]("the ATC is configured for mTLS but its certificate files are not there",
			func(in MTLSPlan, _ Args) MTLSPlan {
				in.ATCCertsMissing = true
				return in
			}),

		// --- mTLS: reading ---

		// Everything is wired here, because the daemon's certificate and its
		// trust material are settled by the Givens and a listener cannot
		// be started before them.
		brine.DefineMap[MTLSPlan, MTLSFetch](
			"a consumer reads the artifact {string} over mTLS",
			func(in MTLSPlan, p brine.Params, _ *brine.Recorder) (MTLSFetch, error) {
				key, ok := p.GetString(0)
				if !ok {
					return MTLSFetch{}, fmt.Errorf("expected an artifact key parameter")
				}

				daemon, credentials, err := in.serve()
				if err != nil {
					return MTLSFetch{}, err
				}
				defer os.RemoveAll(credentials)
				defer daemon.stop()

				host, port, err := hostPortOfURL(daemon.URL)
				if err != nil {
					return MTLSFetch{}, err
				}

				atcCredentials := credentials
				switch {
				case in.ATCHasCerts:
				case in.ATCCertsMissing:
					atcCredentials = filepath.Join(credentials, "absent")
				default:
					return MTLSFetch{}, fmt.Errorf("the scenario did not say how the ATC is configured for mTLS")
				}
				cert := filepath.Join(atcCredentials, "client.crt")
				keyPath := filepath.Join(atcCredentials, "client.key")
				ca := filepath.Join(atcCredentials, "ca.crt")

				vol := jetbridge.NewDaemonSetVolumeFromIP(
					key, key, "k8s-worker-1", host, in.config(port, cert, keyPath, ca))

				stream, streamErr := vol.StreamOut(in.Ctx, ".", nil)
				if streamErr != nil {
					return MTLSFetch{Err: streamErr, Message: streamErr.Error()}, nil
				}
				raw, readErr := io.ReadAll(stream)
				_ = stream.Close()
				if readErr != nil {
					return MTLSFetch{Err: readErr, Message: readErr.Error()}, nil
				}
				return MTLSFetch{Raw: raw}, nil
			},
		),

		// --- mTLS: checks ---

		CheckString[MTLSFetch]("the artifact arrives over mTLS as {string}",
			"the artifact",
			func(in MTLSFetch) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("the read failed: %s", in.Message)
				}
				return string(in.Raw), nil
			}),

		// Asserts both halves of "loudly": that nothing came back, and that
		// what the operator is told names the cause. A client that quietly
		// dropped to an unauthenticated path would fail the first; one that
		// failed for some unrelated reason would fail the second.
		CheckContains[MTLSFetch]("the read is refused, naming {string}",
			"the refusal",
			func(in MTLSFetch) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf(
						"expected the read to be refused, but %d bytes arrived: %q",
						len(in.Raw), string(in.Raw))
				}
				return in.Message, nil
			}),
	}, daemonWarmDefinitions()...)
}
