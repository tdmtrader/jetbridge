package artifactwire

import (
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
	"testing"
	"time"
)

// selfSignedTriple writes a self-signed certificate, its key, and the
// certificate again as the CA, so a triple can be loaded for real.
func selfSignedTriple(t *testing.T) TLS {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	dir := t.TempDir()
	triple := TLS{
		CertPath:   filepath.Join(dir, "client.crt"),
		KeyPath:    filepath.Join(dir, "client.key"),
		CACertPath: filepath.Join(dir, "ca.crt"),
		ServerName: "artifact-daemon.test-ns.svc",
	}
	for path, data := range map[string][]byte{triple.CertPath: certPEM, triple.KeyPath: keyPEM, triple.CACertPath: certPEM} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return triple
}

// A loaded triple means https, a presented certificate, the daemon CA as
// the trust root, and the service SAN as the name to verify. The last guards
// the regression where mTLS by pod IP failed verification ("certificate is
// valid for 127.0.0.1, not <podIP>") because no ServerName was set.
func TestConfiguredTLSDialsHTTPSWithTheTriple(t *testing.T) {
	c, err := NewClient(0, selfSignedTriple(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.Scheme() != "https" {
		t.Errorf("scheme %q", c.Scheme())
	}
	if c.WgetOptions() != "--no-check-certificate" {
		t.Errorf("wget options %q: an init container dials by node IP, which is not a SAN", c.WgetOptions())
	}
	for name, via := range map[string]*http.Client{"plain": c.plain, "streaming": c.streaming, "restore": c.restore} {
		cfg := via.Transport.(*http.Transport).TLSClientConfig
		if cfg == nil {
			t.Fatalf("%s: no TLS config on an https client", name)
		}
		if len(cfg.Certificates) != 1 {
			t.Errorf("%s: expected 1 client certificate, got %d", name, len(cfg.Certificates))
		}
		if cfg.RootCAs == nil {
			t.Errorf("%s: expected the daemon CA as the trust root", name)
		}
		if cfg.ServerName != "artifact-daemon.test-ns.svc" {
			t.Errorf("%s: ServerName %q, want the service SAN so by-IP dials verify", name, cfg.ServerName)
		}
	}
}
