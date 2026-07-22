package main

import (
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

func TestMirrorHTTPClientUsesConfiguredPeerServiceIdentity(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer fixture.Close()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	caPath := filepath.Join(dir, "ca.crt")
	certificateDER := fixture.TLS.Certificates[0].Certificate[0]
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(fixture.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, certificatePEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caPath, certificatePEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}), 0600); err != nil {
		t.Fatal(err)
	}

	const serverName = "artifact-daemon.test-ns.svc"
	client := buildMirrorHTTPClient(lagertest.NewTestLogger("mirror-tls"), &PeerTLSConfig{
		CertPath: certPath, KeyPath: keyPath, CACertPath: caPath, ServerName: serverName,
	}, time.Minute)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		t.Fatalf("mirror client transport = %T, want configured TLS transport", client.Transport)
	}
	if got := transport.TLSClientConfig.ServerName; got != serverName {
		t.Fatalf("mirror TLS ServerName = %q, want issued service identity %q", got, serverName)
	}
}

func TestPeerTLSConfigurationFailureDoesNotDowngradeToHTTP(t *testing.T) {
	resolver := NewPeerResolver(lagertest.NewTestLogger("peer-fail-closed"), nil, "test-ns", "artifact-daemon", 7780, "", &PeerTLSConfig{
		CertPath: "/missing/client.crt", KeyPath: "/missing/client.key", CACertPath: "/missing/ca.crt",
	})
	if resolver.scheme != "https" {
		t.Fatalf("invalid TLS configuration downgraded peer scheme to %q", resolver.scheme)
	}
	if _, ok := resolver.fetchClient.Transport.(failingRoundTripper); !ok {
		t.Fatalf("invalid TLS configuration transport = %T, want fail-closed transport", resolver.fetchClient.Transport)
	}
}
