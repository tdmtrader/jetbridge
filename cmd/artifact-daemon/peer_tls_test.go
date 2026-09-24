package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Peers are discovered as EndpointSlice addresses, so a daemon always dials a
// peer by pod IP. A pod IP is not known when the chart issues the certificate
// and cannot be one of its SANs: the certificate names the headless service
// instead. Every peer path — probe, fetch, and the mirror PUT that preemption
// evacuation also rides — must therefore verify the peer against the service
// name, not against the address it dialed. Before this held, every one of
// them failed verification under TLS and each node silently became an island.
//
// The peer here is a real daemon behind a real mTLS listener, holding a
// certificate whose ONLY SAN is the service DNS name, and it is dialed at
// 127.0.0.1 — exactly the shape of the chart's certificate dialed by pod IP.
func TestPeerPathsVerifyThePeerByServiceNameWhenDialedByIP(t *testing.T) {
	const (
		service   = "artifact-daemon"
		namespace = "ns"
	)
	certs := newServiceNameOnlyCerts(t, service+"."+namespace+".svc")

	// The peer daemon: holds one artifact to be probed and fetched, and
	// receives the mirror PUT.
	peerStorage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(peerStorage, "steps", "handle-x", "result"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(peerStorage, "steps", "handle-x", "result", "output.txt"), []byte("peer data"), 0644); err != nil {
		t.Fatal(err)
	}
	peerServer, err := NewServer(lagertest.NewTestLogger("peer"), peerStorage, "peer-node")
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := BuildTLSConfig(certs.certPath, certs.keyPath, certs.caPath)
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	peerHTTP := &http.Server{Handler: peerServer.Handler(WithTLS())}
	go peerHTTP.Serve(listener)
	t.Cleanup(func() { peerHTTP.Close() })
	peerPort := listener.Addr().(*net.TCPAddr).Port

	// Discovery hands back the peer's IP, as an EndpointSlice does.
	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      service + "-slice",
			Namespace: namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: service},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"127.0.0.1"},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	})

	// The local daemon presents its own server certificate to peers, as
	// main does, and names the peer the way main does.
	peerTLS := &PeerTLSConfig{
		CertPath:   certs.certPath,
		KeyPath:    certs.keyPath,
		CACertPath: certs.caPath,
		ServerName: peerTLSServerName(service, namespace),
	}
	logger := lagertest.NewTestLogger("local")
	peers := NewPeerResolver(logger, clientset, namespace, service, peerPort, "10.0.0.99", peerTLS)

	t.Run("probe", func(t *testing.T) {
		ip, found := peers.Probe(t.Context(), "handle-x/result")
		if !found || ip != "127.0.0.1" {
			t.Fatalf("Probe = (%q, %v), want (127.0.0.1, true): the peer holds the artifact, so a miss means the request never got through TLS", ip, found)
		}
	})

	t.Run("fetch", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "fetched")
		if err := peers.Fetch(t.Context(), "127.0.0.1", "handle-x/result", dest); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dest, "output.txt"))
		if err != nil || string(data) != "peer data" {
			t.Fatalf("fetched output.txt = %q, %v; want %q", data, err, "peer data")
		}
	})

	t.Run("mirror PUT", func(t *testing.T) {
		localStorage := t.TempDir()
		if err := os.MkdirAll(filepath.Join(localStorage, "steps", "handle-m", "out"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(localStorage, "steps", "handle-m", "out", "data.txt"), []byte("mirrored"), 0644); err != nil {
			t.Fatal(err)
		}
		localServer, err := NewServer(logger, localStorage, "local-node")
		if err != nil {
			t.Fatal(err)
		}

		mirror := NewMirror(MirrorConfig{
			StoragePath:    localStorage,
			Port:           peerPort,
			Scheme:         peers.scheme,
			Replicas:       2,
			Concurrency:    1,
			PerPeerTimeout: 10 * time.Second,
			Peers:          peers,
			Client:         buildMirrorHTTPClient(logger, peerTLS, 10*time.Second),
			Logger:         logger.Session("mirror"),
			Guard:          localServer.Guard(),
			Root:           localServer.Root(),
		})
		mirror.Trigger(context.Background(), "handle-m/out")
		mirror.Stop() // drains the job

		mirror.mu.RLock()
		status := mirror.status["handle-m/out"]["127.0.0.1"]
		mirror.mu.RUnlock()
		if status != "ok" {
			t.Fatalf("mirror outcome for 127.0.0.1 = %q, want ok", status)
		}
		data, err := os.ReadFile(filepath.Join(peerStorage, "steps", "handle-m", "out", "data.txt"))
		if err != nil || string(data) != "mirrored" {
			t.Fatalf("peer copy = %q, %v; want %q", data, err, "mirrored")
		}
	})
}

type serviceNameOnlyCerts struct {
	caPath, certPath, keyPath string
}

// newServiceNameOnlyCerts issues a CA and one leaf certificate whose only SAN
// is dnsName — no IP SAN at all. The leaf carries both server and client auth,
// as the chart's genSignedCert does, because the daemon presents its server
// certificate as its client certificate to peers.
func newServiceNameOnlyCerts(t *testing.T, dnsName string) serviceNameOnlyCerts {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "peer-tls-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "artifact-daemon"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{dnsName},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}

	c := serviceNameOnlyCerts{
		caPath:   filepath.Join(dir, "ca.crt"),
		certPath: filepath.Join(dir, "tls.crt"),
		keyPath:  filepath.Join(dir, "tls.key"),
	}
	for path, block := range map[string]*pem.Block{
		c.caPath:   {Type: "CERTIFICATE", Bytes: caDER},
		c.certPath: {Type: "CERTIFICATE", Bytes: leafDER},
		c.keyPath:  {Type: "EC PRIVATE KEY", Bytes: leafKeyDER},
	} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return c
}
