package main_test

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// Peer discovery, probing, and transfer share one TLS decision. A successful
// HEAD followed by a plaintext GET is especially dangerous here: scheduling
// picks the peer and the actual cache transfer then fails only at build time.
func TestPeerResolverProbeAndFetchOverMTLS(t *testing.T) {
	fix := newTLSTestFixture(t)
	baseURL, storagePath := startTLSServer(t, fix)

	source := filepath.Join(storagePath, "steps", "handle", "output")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create peer artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("peer cache"), 0o644); err != nil {
		t.Fatalf("write peer artifact: %v", err)
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse peer URL: %v", err)
	}
	host, rawPort, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split peer address: %v", err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatalf("parse peer port: %v", err)
	}

	const namespace = "concourse"
	const service = "artifact-daemon"
	clientset := k8sfake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-peer",
			Namespace: namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: service},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}}},
	})
	resolver := daemon.NewPeerResolver(
		lagertest.NewTestLogger("peer-mtls"),
		clientset,
		namespace,
		service,
		port,
		"127.0.0.2",
		&daemon.PeerTLSConfig{
			CertPath:   fix.ClientCertPath,
			KeyPath:    fix.ClientKeyPath,
			CACertPath: fix.CACertPath,
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	peer, found := resolver.Probe(ctx, "handle/output")
	if !found || peer != host {
		t.Fatalf("mTLS probe = (%q, %t), want (%q, true)", peer, found, host)
	}

	destination := filepath.Join(t.TempDir(), "fetched")
	if err := resolver.Fetch(ctx, peer, "handle/output", destination); err != nil {
		t.Fatalf("mTLS fetch: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(destination, "payload"))
	if err != nil {
		t.Fatalf("read fetched artifact: %v", err)
	}
	if string(payload) != "peer cache" {
		t.Fatalf("fetched payload = %q, want %q", payload, "peer cache")
	}
}
