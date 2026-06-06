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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestDaemonURLScheme(t *testing.T) {
	if got := daemonURLScheme(testDaemonConfig()); got != "http" {
		t.Errorf("TLS disabled: expected scheme http, got %q", got)
	}
	cfg := testDaemonConfig()
	cfg.ArtifactDaemonTLSEnabled = true
	if got := daemonURLScheme(cfg); got != "https" {
		t.Errorf("TLS enabled: expected scheme https, got %q", got)
	}
}

func TestNewDaemonHTTPClient_PlainWhenTLSDisabled(t *testing.T) {
	client := newDaemonHTTPClient(testDaemonConfig(), 10*time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.TLSClientConfig != nil && len(transport.TLSClientConfig.Certificates) > 0 {
		t.Error("expected no client certificate when TLS disabled")
	}
	if client.Timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", client.Timeout)
	}
}

func TestNewDaemonHTTPClient_PresentsClientCertWhenTLSEnabled(t *testing.T) {
	client := newDaemonHTTPClient(tlsDaemonConfig(t), 30*time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("expected TLSClientConfig to be set when TLS enabled")
	}
	if len(transport.TLSClientConfig.Certificates) != 1 {
		t.Errorf("expected 1 client certificate, got %d", len(transport.TLSClientConfig.Certificates))
	}
	if transport.TLSClientConfig.RootCAs == nil {
		t.Error("expected RootCAs (daemon CA trust) to be set")
	}
}

func TestNewDaemonHTTPClient_FallsBackWhenCertsMissing(t *testing.T) {
	cfg := testDaemonConfig()
	cfg.ArtifactDaemonTLSEnabled = true
	cfg.ArtifactDaemonTLSCert = "/nonexistent/client.crt"
	cfg.ArtifactDaemonTLSKey = "/nonexistent/client.key"
	cfg.ArtifactDaemonTLSCACert = "/nonexistent/ca.crt"

	// Should not panic; falls back to a plain client (warning to stderr).
	client := newDaemonHTTPClient(cfg, 10*time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	if transport.TLSClientConfig != nil && len(transport.TLSClientConfig.Certificates) > 0 {
		t.Error("expected fallback to plain client (no client cert) when certs cannot be loaded")
	}
}

func TestDaemonTLSServerName(t *testing.T) {
	// testDaemonConfig: service "artifact-daemon", namespace "test-ns".
	if got, want := daemonTLSServerName(testDaemonConfig()), "artifact-daemon.test-ns.svc"; got != want {
		t.Errorf("expected ServerName %q, got %q", want, got)
	}
	empty := testDaemonConfig()
	empty.ArtifactDaemonService = ""
	if got := daemonTLSServerName(empty); got != "" {
		t.Errorf("expected empty ServerName when service unknown, got %q", got)
	}
}

// TestNewDaemonHTTPClient_SetsServerName guards the regression where mTLS by pod
// IP failed cert verification ("certificate is valid for 127.0.0.1, not
// <podIP>") because no ServerName was set to the cert's service-DNS SAN.
func TestNewDaemonHTTPClient_SetsServerName(t *testing.T) {
	client := newDaemonHTTPClient(tlsDaemonConfig(t), 30*time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		t.Fatal("expected an mTLS transport")
	}
	if got, want := transport.TLSClientConfig.ServerName, "artifact-daemon.test-ns.svc"; got != want {
		t.Errorf("expected ServerName %q (cert SAN) so by-IP dials verify, got %q", want, got)
	}
}

// TestDaemonSetVolume_DaemonURLSchemeFollowsTLS guards the regression where the
// ATC-side data-plane URLs were hardcoded to http:// even with mTLS enabled.
func TestDaemonSetVolume_DaemonURLSchemeFollowsTLS(t *testing.T) {
	httpVol := &DaemonSetVolume{key: "art-key", sourceIP: "10.0.0.5", config: testDaemonConfig()}
	httpURL, err := httpVol.daemonURL(context.Background())
	if err != nil {
		t.Fatalf("daemonURL (http): %v", err)
	}
	if !strings.HasPrefix(httpURL, "http://10.0.0.5:7780/artifacts/art-key") {
		t.Errorf("TLS disabled: expected http:// artifact URL, got %q", httpURL)
	}

	httpsVol := &DaemonSetVolume{key: "art-key", sourceIP: "10.0.0.5", config: tlsDaemonConfig(t)}
	httpsURL, err := httpsVol.daemonURL(context.Background())
	if err != nil {
		t.Fatalf("daemonURL (https): %v", err)
	}
	if !strings.HasPrefix(httpsURL, "https://10.0.0.5:7780/artifacts/art-key") {
		t.Errorf("TLS enabled: expected https:// artifact URL, got %q", httpsURL)
	}
}

// TestBuildFetchInitContainers_TLSWiring verifies the init container fetches
// over HTTPS with --no-check-certificate (the daemon is dialed by node IP,
// which is not a cert SAN) and that NO CA volume is mounted — mounting a volume
// the pod doesn't have previously made the pod spec invalid.
func TestBuildFetchInitContainers_TLSWiring(t *testing.T) {
	b := NewDaemonSetBackend(tlsDaemonConfig(t), nil, nil)
	inputs := []runtime.Input{
		{Artifact: &testArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input"},
	}
	mounts := []corev1.VolumeMount{{Name: "input-0", MountPath: "/tmp/input"}}
	volumes := []corev1.Volume{b.StepVolume("input-0", "handle", "input-0")}

	inits := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)
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
		{Artifact: &testArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input"},
	}
	mounts := []corev1.VolumeMount{{Name: "input-0", MountPath: "/tmp/input"}}
	volumes := []corev1.Volume{b.StepVolume("input-0", "handle", "input-0")}

	inits := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)
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
