package jetbridge

// RESTORED 2026-09-05 (rebase onto core for 0.3.2), from the deleted
// atc/worker/jetbridge/volume_daemonset_test.go (merge-base aef2244a63, via the
// compile-adapted copy the port stage produced for this branch).
//
// Rows JB-volume_daemonset-011, -012 and -013 of DISPOSITION-jetbridge.md were
// recorded DELETED on FILE-level evidence only. Re-verifying them on the rebased
// head refuted all three: the brine artifact-daemon scenarios reach the peer
// fallback through a single daemon endpoint, so the producer-dead / peer-alive
// routing these tests build cannot be expressed there. Restoring is always
// acceptable; deleting on inference is not.
//
// Only the three refuted tests and the helpers they alone use are here; the
// other thirteen tests of the original file stay deleted, their evidence intact.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ---------------------------------------------------------------------------
// StreamOut peer-fallback (P1 of artifact_daemon_resilience_20260425)
// ---------------------------------------------------------------------------

// peerFallbackEndpointSlice constructs a fake EndpointSlice with the given
// IPs as endpoints — used to drive DaemonClient discovery in tests.
func peerFallbackEndpointSlice(namespace, service string, ips ...string) *discoveryv1.EndpointSlice {
	endpoints := make([]discoveryv1.Endpoint, 0, len(ips))
	for _, ip := range ips {
		endpoints = append(endpoints, discoveryv1.Endpoint{Addresses: []string{ip}})
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      service + "-fixture",
			Namespace: namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: service},
		},
		Endpoints: endpoints,
	}
}

// newPeerFallbackTestRig wires up a DaemonSetVolume against a routingTransport
// that maps producer/peer hostports to test servers. Both vol.httpClient and
// vol.daemonClient.client share the transport so probe HEADs and fetch GETs
// land on the right server.
func newPeerFallbackTestRig(producerNode, producerIP string, transport routingTransport, peerIPs []string) *DaemonSetVolume {
	resolver := fakeNodeIPResolver(testNode(producerNode, producerIP))
	clientset := fake.NewSimpleClientset(peerFallbackEndpointSlice("ns", "artifact-daemon", peerIPs...))

	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	logger := lagertest.NewTestLogger("test")

	// White-box construction — NewDaemonClient builds its own client, but we
	// need to inject the routingTransport so probe HEADs hit the test peer
	// server and not the real network. Same-package access lets us assemble
	// the struct directly.
	dc := &DaemonClient{
		logger:    logger,
		clientset: clientset,
		namespace: "ns",
		service:   "artifact-daemon",
		port:      7780,
		client:    httpClient,
		scheme:    "http",
	}

	vol := &DaemonSetVolume{
		key:            "h/o",
		handle:         "h",
		workerName:     "w",
		sourceNode:     producerNode,
		config:         Config{ArtifactDaemonPort: 7780},
		httpClient:     httpClient,
		nodeIPResolver: resolver,
		daemonClient:   dc,
	}
	return vol
}

func TestDaemonSetVolume_StreamOut_FallsBackToPeer_OnConnectionRefused(t *testing.T) {
	const peerIP = "10.0.0.2"
	const producerIP = "10.0.0.1"

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/artifacts/steps/h/o":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/artifacts/steps/h/o":
			w.Write([]byte("peer-served-content"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer peer.Close()

	transport := routingTransport{routes: map[string]string{
		producerIP + ":7780": "",       // producer node refuses (simulating dead node)
		peerIP + ":7780":     peer.URL, // peer serves
	}}

	vol := newPeerFallbackTestRig("node-1", producerIP, transport, []string{peerIP})

	reader, err := vol.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("expected fallback to peer to succeed, got: %v", err)
	}
	defer reader.Close()

	body, _ := io.ReadAll(reader)
	if string(body) != "peer-served-content" {
		t.Errorf("expected peer-served-content, got: %q", string(body))
	}
}

func TestDaemonSetVolume_StreamOut_FallsBack_PreservesNotFoundOnProbeMiss(t *testing.T) {
	const peerIP = "10.0.0.2"
	const producerIP = "10.0.0.1"

	// Peer is up but doesn't have the artifact — HEAD returns 404.
	// We track HEAD requests to PROVE the probe path was exercised; otherwise
	// this test could pass simply because the producer fails first (and the
	// code never bothered to peer-probe).
	var probeHits int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && r.URL.Path == "/artifacts/steps/h/o" {
			atomic.AddInt32(&probeHits, 1)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer peer.Close()

	transport := routingTransport{routes: map[string]string{
		producerIP + ":7780": "",       // producer dead
		peerIP + ":7780":     peer.URL, // peer responsive but lacks artifact
	}}

	vol := newPeerFallbackTestRig("node-1", producerIP, transport, []string{peerIP})

	_, err := vol.StreamOut(context.Background(), ".", nil)
	if err == nil {
		t.Fatal("expected error when neither producer nor any peer has artifact")
	}
	if got := atomic.LoadInt32(&probeHits); got == 0 {
		t.Errorf("expected peer probe to be attempted after producer failed, got 0 HEAD requests")
	}
	// Error should surface the not-found situation — we don't pin the exact
	// wording but it should not be a raw connection-refused leaking from
	// the producer attempt.
	if strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error should not be raw connection-refused after fallback; got: %v", err)
	}
}

func TestDaemonSetVolume_StreamOut_HappyPath_PerformsZeroPeerProbes(t *testing.T) {
	const peerIP = "10.0.0.2"
	const producerIP = "10.0.0.1"

	var producerHits, peerHits int32

	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&producerHits, 1)
		if r.Method == http.MethodGet && r.URL.Path == "/artifacts/h/o" {
			w.Write([]byte("producer-content"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer producer.Close()

	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&peerHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer peer.Close()

	transport := routingTransport{routes: map[string]string{
		producerIP + ":7780": producer.URL,
		peerIP + ":7780":     peer.URL,
	}}

	vol := newPeerFallbackTestRig("node-1", producerIP, transport, []string{peerIP})

	reader, err := vol.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("StreamOut: %v", err)
	}
	defer reader.Close()

	body, _ := io.ReadAll(reader)
	if string(body) != "producer-content" {
		t.Errorf("expected producer-content, got: %q", string(body))
	}
	if got := atomic.LoadInt32(&producerHits); got != 1 {
		t.Errorf("expected exactly 1 producer hit, got %d", got)
	}
	if got := atomic.LoadInt32(&peerHits); got != 0 {
		t.Errorf("expected 0 peer probes on happy path, got %d", got)
	}
}

// routingTransport routes HTTP requests by the request URL host. A target of
// "" simulates a refused / unreachable host (returns a synthetic error
// without actually attempting a TCP dial). Used to compose multi-server
// scenarios — e.g. a "dead producer" + "live peer" pair — without juggling
// real listener ports.
type routingTransport struct {
	routes map[string]string // hostport → target server URL ("" = refuse)
}

func (t routingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, ok := t.routes[req.URL.Host]
	if !ok || target == "" {
		return nil, fmt.Errorf("connection refused: %s", req.URL.Host)
	}
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(target, "http://")
	return http.DefaultTransport.RoundTrip(req)
}
