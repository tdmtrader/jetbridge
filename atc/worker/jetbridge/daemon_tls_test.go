package jetbridge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
)

// writeSelfSignedCerts generates a self-signed cert/key pair and writes them to
// a temp dir, using the cert itself as the CA. Returns the cert, key, and CA
// file paths. Enough for exercising the ATC-side mTLS client construction.
func writeSelfSignedCerts(t *testing.T) (certPath, keyPath, caPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-daemon-client"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	dir := t.TempDir()
	certPath = filepath.Join(dir, "client.crt")
	keyPath = filepath.Join(dir, "client.key")
	caPath = filepath.Join(dir, "ca.crt")
	for path, data := range map[string][]byte{certPath: certPEM, keyPath: keyPEM, caPath: certPEM} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return certPath, keyPath, caPath
}

// tlsDaemonConfig returns a daemon Config with mTLS enabled and real cert paths.
func tlsDaemonConfig(t *testing.T) Config {
	t.Helper()
	cert, key, ca := writeSelfSignedCerts(t)
	cfg := testDaemonConfig()
	cfg.ArtifactDaemonTLSEnabled = true
	cfg.ArtifactDaemonTLSCert = cert
	cfg.ArtifactDaemonTLSKey = key
	cfg.ArtifactDaemonTLSCACert = ca
	return cfg
}

// DaemonTLSConfigured is the one predicate for "the ATC speaks mTLS to the
// artifact daemon". It used to be four: atccmd derived TLSEnabled from the
// cert alone, daemonClientTLSConfigured required all three paths, and
// NewDaemonClient carried its own inline copy. A cert-only config therefore
// dialled https at a plaintext daemon, presenting no client certificate.
func TestDaemonTLSConfiguredRequiresTheWholeTriple(t *testing.T) {
	for _, tc := range []struct {
		name             string
		cert, key, caCrt string
		want             bool
	}{
		{name: "all three", cert: "c", key: "k", caCrt: "a", want: true},
		{name: "nothing", want: false},
		{name: "cert only", cert: "c", want: false},
		{name: "key only", key: "k", want: false},
		{name: "ca only", caCrt: "a", want: false},
		{name: "cert and key, no ca", cert: "c", key: "k", want: false},
		{name: "cert and ca, no key", cert: "c", caCrt: "a", want: false},
	} {
		if got := DaemonTLSConfigured(tc.cert, tc.key, tc.caCrt); got != tc.want {
			t.Errorf("%s: DaemonTLSConfigured(%q, %q, %q) = %v, want %v", tc.name, tc.cert, tc.key, tc.caCrt, got, tc.want)
		}
	}
}

// The scheme the ATC dials and the certificate it presents come from one
// predicate inside the wire client, so they cannot disagree: https always
// presents a certificate. A partial triple therefore dials plaintext, and
// ValidateDaemonTLSFlags is what refuses it before it gets this far.
func TestDaemonSchemeAndClientNeverDisagree(t *testing.T) {
	for _, tc := range []struct{ name, cert, key, caCrt string }{
		{name: "cert only", cert: "/etc/tls/client.crt"},
		{name: "cert and key", cert: "/etc/tls/client.crt", key: "/etc/tls/client.key"},
		{name: "nothing"},
	} {
		cfg := testDaemonConfig()
		cfg.ArtifactDaemonTLSCert = tc.cert
		cfg.ArtifactDaemonTLSKey = tc.key
		cfg.ArtifactDaemonTLSCACert = tc.caCrt
		cfg.ArtifactDaemonTLSEnabled = DaemonTLSConfigured(tc.cert, tc.key, tc.caCrt)

		if got := newWireClient(cfg).Scheme(); got != "http" {
			t.Errorf("%s: ATC dials %s but presents no client certificate", tc.name, got)
		}
	}
}

func TestValidateDaemonTLSFlags(t *testing.T) {
	if err := ValidateDaemonTLSFlags("", "", ""); err != nil {
		t.Errorf("plaintext daemon traffic refused: %v", err)
	}
	if err := ValidateDaemonTLSFlags("c", "k", "a"); err != nil {
		t.Errorf("complete triple refused: %v", err)
	}

	err := ValidateDaemonTLSFlags("c", "", "")
	if err == nil {
		t.Fatal("a cert without a key or CA was accepted; the ATC would dial https at a plaintext daemon")
	}
	for _, want := range []string{"kubernetes-artifact-daemon-tls-key", "kubernetes-artifact-daemon-tls-ca-cert"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the missing flag --%s", err, want)
		}
	}
	if strings.Contains(err.Error(), "kubernetes-artifact-daemon-tls-cert") {
		t.Errorf("error %q names --kubernetes-artifact-daemon-tls-cert, which was in fact set", err)
	}
}

// wireTLS is the one adapter from Config to the wire client's triple: off
// means plaintext whatever paths are set, on hands over the triple and the
// service name to verify against.
func TestWireTLSFollowsTheConfig(t *testing.T) {
	off := testDaemonConfig()
	off.ArtifactDaemonTLSCert, off.ArtifactDaemonTLSKey, off.ArtifactDaemonTLSCACert = "c", "k", "a"
	if got := wireTLS(off); got.Configured() {
		t.Errorf("TLS disabled: expected an empty triple, got %+v", got)
	}
	if got := newWireClient(off).Scheme(); got != "http" {
		t.Errorf("TLS disabled: expected scheme http, got %q", got)
	}

	on := tlsDaemonConfig(t)
	triple := wireTLS(on)
	if !triple.Configured() || triple.ServerName != "artifact-daemon.test-ns.svc" {
		t.Errorf("TLS enabled: expected the triple with the service SAN, got %+v", triple)
	}
	if got := newWireClient(on).Scheme(); got != "https" {
		t.Errorf("TLS enabled: expected scheme https, got %q", got)
	}
}

// TLS asked for and not loadable is NOT plaintext. The old fallback dialed
// https with no certificate and failed at the handshake; a fallback to http
// would be the unauthenticated path the certificate exists to close. The
// client keeps https for the init containers and refuses every request
// naming the certificate.
func TestNewWireClient_RefusesWhenCertsMissing(t *testing.T) {
	cfg := testDaemonConfig()
	cfg.ArtifactDaemonTLSEnabled = true
	cfg.ArtifactDaemonTLSCert = "/nonexistent/client.crt"
	cfg.ArtifactDaemonTLSKey = "/nonexistent/client.key"
	cfg.ArtifactDaemonTLSCACert = "/nonexistent/ca.crt"

	client := newWireClient(cfg)
	if got := client.Scheme(); got != "https" {
		t.Errorf("expected the scheme the deployment asked for, got %q", got)
	}
	err := client.Mirror(context.Background(), "10.0.0.5", "k")
	if err == nil || !strings.Contains(err.Error(), "cert") {
		t.Errorf("expected every request refused naming the certificate, got %v", err)
	}

	partial := testDaemonConfig()
	partial.ArtifactDaemonTLSEnabled = true
	partial.ArtifactDaemonTLSCert = "/etc/tls/client.crt"
	err = newWireClient(partial).Mirror(context.Background(), "10.0.0.5", "k")
	if err == nil || !strings.Contains(err.Error(), "kubernetes-artifact-daemon-tls-key") {
		t.Errorf("expected a partial triple refused naming the missing flags, got %v", err)
	}
}

func TestDaemonTLSServerName(t *testing.T) {
	// testDaemonConfig: service "artifact-daemon", namespace "test-ns".
	if got, want := daemonTLSServerName(testDaemonConfig()), "artifact-daemon.test-ns.svc"; got != want {
		t.Errorf("expected ServerName %q, got %q", want, got)
	}
	separateDaemonNamespace := testDaemonConfig()
	separateDaemonNamespace.ArtifactDaemonNamespace = "cicd"
	if got, want := daemonTLSServerName(separateDaemonNamespace), "artifact-daemon.cicd.svc"; got != want {
		t.Errorf("expected daemon-namespace ServerName %q, got %q", want, got)
	}
	empty := testDaemonConfig()
	empty.ArtifactDaemonService = ""
	if got := daemonTLSServerName(empty); got != "" {
		t.Errorf("expected empty ServerName when service unknown, got %q", got)
	}
}

// TestDaemonSetVolume_DaemonURLSchemeFollowsTLS guards the regression where the
// ATC-side data-plane URLs were hardcoded to http:// even with mTLS enabled.
// The volume no longer assembles a URL; its wire client does, from the same
// triple, so the check is on the client the volume was built with.
func TestDaemonSetVolume_DaemonURLSchemeFollowsTLS(t *testing.T) {
	httpVol := NewDaemonSetVolumeFromIP("art-key", "h", "w", "10.0.0.5", testDaemonConfig())
	if got := httpVol.wire.URL("10.0.0.5", artifactwire.ArtifactsPrefix+"art-key"); got != "http://10.0.0.5:7780/artifacts/art-key" {
		t.Errorf("TLS disabled: expected http:// artifact URL, got %q", got)
	}

	httpsVol := NewDaemonSetVolumeFromIP("art-key", "h", "w", "10.0.0.5", tlsDaemonConfig(t))
	if got := httpsVol.wire.URL("10.0.0.5", artifactwire.ArtifactsPrefix+"art-key"); got != "https://10.0.0.5:7780/artifacts/art-key" {
		t.Errorf("TLS enabled: expected https:// artifact URL, got %q", got)
	}
}

// TestBuildFetchInitContainers_TLSWiring verifies the init container fetches
// over HTTPS with --no-check-certificate (the daemon is dialed by node IP,
// which is not a cert SAN) and that NO CA volume is mounted — mounting a volume
// the pod doesn't have previously made the pod spec invalid.
func TestBuildFetchInitContainers_TLSWiring(t *testing.T) {
	b := NewDaemonSetBackend(tlsDaemonConfig(t), nil, nil)
	inputs := []runtime.Input{
		{Artifact: constructionArtifact("vol-a", "worker-1"), DestinationPath: "/tmp/input"},
	}
	mounts := []corev1.VolumeMount{{Name: "input-0", MountPath: "/tmp/input"}}
	volumes := []corev1.Volume{b.StepVolume("input-0", "handle", "input-0")}

	inits, err := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)
	if err != nil {
		t.Fatalf("BuildFetchInitContainers: %v", err)
	}
	if len(inits) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(inits))
	}
	c := inits[0]

	cmdStr := strings.Join(c.Command, " ")
	if !strings.Contains(cmdStr, "https://") {
		t.Errorf("expected init container command to use https://, got: %s", cmdStr)
	}
	if !strings.Contains(cmdStr, "--no-check-certificate") {
		t.Errorf("expected --no-check-certificate (node IP is not a cert SAN), got: %s", cmdStr)
	}

	for _, e := range c.Env {
		if e.Name == "SSL_CERT_FILE" {
			t.Error("did not expect SSL_CERT_FILE — server cert verification is skipped")
		}
	}
	for _, m := range c.VolumeMounts {
		if m.Name == "artifact-daemon-tls-ca" {
			t.Error("did not expect a CA volume mount — the pod has no such volume, which invalidates the pod spec")
		}
	}
}

// TestBuildFetchInitContainers_NoTLSMountWhenDisabled confirms the scheme stays
// http and no TLS options are added when TLS is off.
func TestBuildFetchInitContainers_NoTLSMountWhenDisabled(t *testing.T) {
	b := NewDaemonSetBackend(testDaemonConfig(), nil, nil)
	inputs := []runtime.Input{
		{Artifact: constructionArtifact("vol-a", "worker-1"), DestinationPath: "/tmp/input"},
	}
	mounts := []corev1.VolumeMount{{Name: "input-0", MountPath: "/tmp/input"}}
	volumes := []corev1.Volume{b.StepVolume("input-0", "handle", "input-0")}

	inits, err := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)
	if err != nil {
		t.Fatalf("BuildFetchInitContainers: %v", err)
	}
	if len(inits) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(inits))
	}
	c := inits[0]

	cmdStr := strings.Join(c.Command, " ")
	if strings.Contains(cmdStr, "https://") {
		t.Error("expected http:// scheme when TLS disabled")
	}
	if strings.Contains(cmdStr, "--no-check-certificate") {
		t.Error("did not expect --no-check-certificate when TLS disabled")
	}
	for _, e := range c.Env {
		if e.Name == "SSL_CERT_FILE" {
			t.Error("expected no SSL_CERT_FILE when TLS disabled")
		}
	}
}

func TestDaemonSetVolumeUsesStreamingClient(t *testing.T) {
	vol := NewDaemonSetVolume("key", "handle", "worker", nil, "node", testDaemonConfig(), nil)
	if vol.wire == nil {
		t.Error("NewDaemonSetVolume: expected a wire client")
	}

	volFromIP := NewDaemonSetVolumeFromIP("key", "handle", "worker", "10.0.0.1", testDaemonConfig())
	if volFromIP.wire == nil {
		t.Error("NewDaemonSetVolumeFromIP: expected a wire client")
	}
}
