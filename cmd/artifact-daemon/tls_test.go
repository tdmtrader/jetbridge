package main_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// tlsTestFixture holds CA, server, and client certs for mTLS testing.
type tlsTestFixture struct {
	CACertPath     string
	ServerCertPath string
	ServerKeyPath  string
	ClientCertPath string
	ClientKeyPath  string

	CACertPEM []byte
	CACert    *x509.Certificate
	CAKey     *ecdsa.PrivateKey
}

// newTLSTestFixture generates a CA, server cert, and client cert for testing.
func newTLSTestFixture(t *testing.T) *tlsTestFixture {
	t.Helper()

	dir := t.TempDir()

	// Generate CA.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})

	// Generate server cert.
	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "artifact-daemon"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	serverCertDER, _ := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCertDER})
	serverKeyDER, _ := x509.MarshalECPrivateKey(serverKey)
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER})

	// Generate client cert.
	clientKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "concourse-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientCertDER, _ := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCertDER})
	clientKeyDER, _ := x509.MarshalECPrivateKey(clientKey)
	clientKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: clientKeyDER})

	// Write files.
	caCertPath := filepath.Join(dir, "ca.crt")
	serverCertPath := filepath.Join(dir, "server.crt")
	serverKeyPath := filepath.Join(dir, "server.key")
	clientCertPath := filepath.Join(dir, "client.crt")
	clientKeyPath := filepath.Join(dir, "client.key")

	os.WriteFile(caCertPath, caCertPEM, 0600)
	os.WriteFile(serverCertPath, serverCertPEM, 0600)
	os.WriteFile(serverKeyPath, serverKeyPEM, 0600)
	os.WriteFile(clientCertPath, clientCertPEM, 0600)
	os.WriteFile(clientKeyPath, clientKeyPEM, 0600)

	return &tlsTestFixture{
		CACertPath:     caCertPath,
		ServerCertPath: serverCertPath,
		ServerKeyPath:  serverKeyPath,
		ClientCertPath: clientCertPath,
		ClientKeyPath:  clientKeyPath,
		CACertPEM:      caCertPEM,
		CACert:         caCert,
		CAKey:          caKey,
	}
}

// clientWithMTLS returns an HTTP client configured with the client cert and CA trust.
func (f *tlsTestFixture) clientWithMTLS(t *testing.T) *http.Client {
	t.Helper()
	clientCert, err := tls.LoadX509KeyPair(f.ClientCertPath, f.ClientKeyPath)
	if err != nil {
		t.Fatalf("load client cert: %v", err)
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(f.CACertPEM)
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{clientCert},
				RootCAs:      caPool,
			},
		},
	}
}

// clientWithTLSOnly returns an HTTP client that trusts the CA but does not present a client cert.
func (f *tlsTestFixture) clientWithTLSOnly(t *testing.T) *http.Client {
	t.Helper()
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(f.CACertPEM)
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caPool,
			},
		},
	}
}

// startTLSServer creates an artifact-daemon server with mTLS and returns its HTTPS URL.
func startTLSServer(t *testing.T, fix *tlsTestFixture) (string, string) {
	t.Helper()

	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("artifact-daemon-tls")
	server := daemon.NewServer(logger, storagePath, "test-node")

	tlsCfg, err := daemon.BuildTLSConfig(fix.ServerCertPath, fix.ServerKeyPath, fix.CACertPath)
	if err != nil {
		t.Fatalf("build TLS config: %v", err)
	}

	listener, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("TLS listen: %v", err)
	}

	httpServer := &http.Server{Handler: server.Handler(daemon.WithTLS())}
	go httpServer.Serve(listener)
	t.Cleanup(func() { httpServer.Close() })

	addr := listener.Addr().String()
	return fmt.Sprintf("https://%s", addr), storagePath
}

// ---------------------------------------------------------------------------
// Test: daemon starts HTTPS when TLS flags provided
// ---------------------------------------------------------------------------

func TestTLS_DaemonStartsHTTPS(t *testing.T) {
	fix := newTLSTestFixture(t)
	baseURL, _ := startTLSServer(t, fix)

	client := fix.clientWithMTLS(t)
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("HTTPS request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	if resp.TLS == nil {
		t.Fatal("expected TLS connection")
	}
}

// ---------------------------------------------------------------------------
// Test: daemon starts HTTP when no TLS flags (backwards compat)
// ---------------------------------------------------------------------------

func TestTLS_DaemonStartsHTTPWithoutFlags(t *testing.T) {
	// Without TLS config, the existing setupServer uses httptest.NewServer (HTTP).
	ts, _ := setupServer(t)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify no TLS.
	if resp.TLS != nil {
		t.Error("expected plain HTTP connection, got TLS")
	}
}

// ---------------------------------------------------------------------------
// Test: mTLS middleware returns 401 on protected paths without client cert
// ---------------------------------------------------------------------------

func TestTLS_ProtectedPaths_RejectWithoutClientCert(t *testing.T) {
	fix := newTLSTestFixture(t)
	baseURL, storagePath := startTLSServer(t, fix)

	// Create artifact data so endpoints don't 404.
	stepDir := filepath.Join(storagePath, "steps", "build-1", "out")
	os.MkdirAll(stepDir, 0755)
	os.WriteFile(filepath.Join(stepDir, "f.txt"), []byte("data"), 0644)

	client := fix.clientWithTLSOnly(t) // no client cert

	protectedPaths := []struct {
		method string
		path   string
	}{
		{"GET", "/artifacts/steps/build-1/out"},
		{"PUT", "/artifacts/test-key"},
		{"DELETE", "/artifacts/test-key"},
		{"HEAD", "/artifacts/steps/build-1/out"},
		{"POST", "/register"},
		{"PUT", "/stream-in/build-1"},
		{"HEAD", "/resource-caches/rc-1"},
		{"GET", "/resource-caches/rc-1"},
	}

	for _, tc := range protectedPaths {
		t.Run(fmt.Sprintf("%s_%s", tc.method, tc.path), func(t *testing.T) {
			var body *strings.Reader
			if tc.method == "POST" || tc.method == "PUT" {
				body = strings.NewReader(`{"key":"k","dest":"/tmp"}`)
			}

			var req *http.Request
			var err error
			if body != nil {
				req, err = http.NewRequest(tc.method, baseURL+tc.path, body)
			} else {
				req, err = http.NewRequest(tc.method, baseURL+tc.path, nil)
			}
			if err != nil {
				t.Fatalf("create request: %v", err)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("expected 401 on %s %s without client cert, got %d", tc.method, tc.path, resp.StatusCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test: mTLS middleware allows protected paths with valid client cert
// ---------------------------------------------------------------------------

func TestTLS_ProtectedPaths_AllowWithClientCert(t *testing.T) {
	fix := newTLSTestFixture(t)
	baseURL, storagePath := startTLSServer(t, fix)

	// Create artifact data.
	stepDir := filepath.Join(storagePath, "steps", "build-1", "out")
	os.MkdirAll(stepDir, 0755)
	os.WriteFile(filepath.Join(stepDir, "f.txt"), []byte("data"), 0644)

	client := fix.clientWithMTLS(t) // with client cert

	// GET /artifacts should succeed (200, not 401).
	resp, err := client.Get(baseURL + "/artifacts/steps/build-1/out")
	if err != nil {
		t.Fatalf("GET /artifacts: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("expected non-401 with valid client cert on GET /artifacts, got 401")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// HEAD /resource-caches should succeed (404 is fine, not 401).
	req, _ := http.NewRequest("HEAD", baseURL+"/resource-caches/rc-1", nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("HEAD /resource-caches: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("expected non-401 with valid client cert on HEAD /resource-caches, got 401")
	}

	// POST /register with client cert should not get 401.
	req, _ = http.NewRequest("POST", baseURL+"/register", strings.NewReader(`{"key":"k","local_path":"/nonexistent"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("expected non-401 with valid client cert on POST /register, got 401")
	}
}

// ---------------------------------------------------------------------------
// Test: exempt paths work without client cert over HTTPS
// ---------------------------------------------------------------------------

func TestTLS_ExemptPaths_WorkWithoutClientCert(t *testing.T) {
	fix := newTLSTestFixture(t)
	baseURL, storagePath := startTLSServer(t, fix)

	// Create artifact data for resolve.
	stepDir := filepath.Join(storagePath, "steps", "build-1", "out")
	os.MkdirAll(stepDir, 0755)
	os.WriteFile(filepath.Join(stepDir, "f.txt"), []byte("data"), 0644)

	client := fix.clientWithTLSOnly(t) // no client cert

	// /healthz should succeed.
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		t.Fatalf("/healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz: expected 200, got %d", resp.StatusCode)
	}

	// /resolve should not get 401 (may get 400 or other, but not 401).
	req, _ := http.NewRequest("POST", baseURL+"/resolve", strings.NewReader(`{"key":"steps/build-1/out","dest":"/tmp/test"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("/resolve: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("/resolve: expected non-401 without client cert, got 401")
	}

	// /resolve-batch should not get 401.
	req, _ = http.NewRequest("POST", baseURL+"/resolve-batch", strings.NewReader(`{"items":[{"key":"steps/build-1/out","dest":"/tmp/test"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("/resolve-batch: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		t.Error("/resolve-batch: expected non-401 without client cert, got 401")
	}
}
