package jetbridge

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	"github.com/concourse/concourse/atc/compression"
	discoveryv1 "k8s.io/api/discovery/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeNodeIPResolver creates a NodeIPResolver backed by a fake K8s client
// with nodes pre-loaded so Resolve() returns deterministic IPs.
func fakeNodeIPResolver(nodes ...corev1.Node) *NodeIPResolver {
	objs := make([]interface{}, 0, len(nodes))
	for i := range nodes {
		objs = append(objs, &nodes[i])
	}
	// Use runtime.Object slice for NewSimpleClientset.
	cs := fake.NewSimpleClientset()
	for i := range nodes {
		cs.CoreV1().Nodes().Create(context.Background(), &nodes[i], metav1.CreateOptions{})
	}
	return NewNodeIPResolver(cs)
}

func testNode(name, ip string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: ip},
			},
		},
	}
}

func TestDaemonSetVolume_StreamOut_Success(t *testing.T) {
	content := "tar data here"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))

	vol := &DaemonSetVolume{
		key:            "abc",
		handle:         "abc",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     srv.Client(),
		nodeIPResolver: resolver,
	}
	// Override the URL to point to test server
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	reader, err := vol.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("StreamOut: %v", err)
	}
	defer reader.Close()

	data, _ := io.ReadAll(reader)
	if string(data) != content {
		t.Errorf("expected %q, got %q", content, string(data))
	}
}

func TestDaemonSetVolume_StreamOut_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))

	vol := &DaemonSetVolume{
		key:            "missing",
		handle:         "missing",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     srv.Client(),
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	_, err := vol.StreamOut(context.Background(), ".", nil)
	if err == nil {
		t.Error("expected error for 404")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestDaemonSetVolume_StreamOut_NoSourceNode(t *testing.T) {
	vol := &DaemonSetVolume{
		key:    "abc",
		handle: "abc",
	}

	_, err := vol.StreamOut(context.Background(), ".", nil)
	if err == nil {
		t.Error("expected error for no source node")
	}
}

func TestDaemonSetVolumeFromIP_StreamOut_Success(t *testing.T) {
	content := "cached resource data"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request path contains the artifact key.
		if !strings.Contains(r.URL.Path, "/artifacts/rc-42") {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(content))
	}))
	defer srv.Close()

	vol := NewDaemonSetVolumeFromIP("rc-42", "rc-42", "worker-1", "10.0.0.5", Config{ArtifactDaemonPort: 7780})
	// Override transport to route to test server.
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	reader, err := vol.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("StreamOut: %v", err)
	}
	defer reader.Close()

	data, _ := io.ReadAll(reader)
	if string(data) != content {
		t.Errorf("expected %q, got %q", content, string(data))
	}
}

func TestDaemonSetVolumeFromIP_Handle(t *testing.T) {
	vol := NewDaemonSetVolumeFromIP("rc-42", "rc-42", "worker-1", "10.0.0.5", Config{})
	if vol.Handle() != "rc-42" {
		t.Errorf("expected handle rc-42, got %s", vol.Handle())
	}
	if vol.Source() != "worker-1" {
		t.Errorf("expected source worker-1, got %s", vol.Source())
	}
}

func TestDaemonSetVolumeFromIP_StreamOut_NoIP(t *testing.T) {
	// Verify that a DaemonSetVolume with neither sourceNode nor sourceIP errors.
	vol := NewDaemonSetVolumeFromIP("rc-42", "rc-42", "worker-1", "", Config{})
	_, err := vol.StreamOut(context.Background(), ".", nil)
	if err == nil {
		t.Error("expected error for empty source IP")
	}
}

// makeTarball creates a tar archive containing a single file with the given
// name and content. This simulates what the artifact daemon returns from
// GET /artifacts/ for a directory.
func makeTarball(t *testing.T, fileName, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: fileName,
		Size: int64(len(content)),
		Mode: 0644,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDaemonSetVolume_StreamOut_WithGzipCompression(t *testing.T) {
	// Daemon returns raw tar (simulating GET /artifacts/).
	tarData := makeTarball(t, "task.yml", "platform: linux\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Write(tarData)
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))
	vol := &DaemonSetVolume{
		key:            "abc",
		handle:         "abc",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     srv.Client(),
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	reader, err := vol.StreamOut(context.Background(), ".", compression.NewGzipCompression())
	if err != nil {
		t.Fatalf("StreamOut with gzip: %v", err)
	}
	defer reader.Close()

	// The returned stream must be valid gzip.
	gr, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatalf("gzip.NewReader on StreamOut result: %v", err)
	}
	defer gr.Close()

	// Inside the gzip, we must find valid tar with our file.
	tr := tar.NewReader(gr)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next: %v", err)
	}
	if hdr.Name != "task.yml" {
		t.Errorf("expected tar entry 'task.yml', got %q", hdr.Name)
	}
	content, _ := io.ReadAll(tr)
	if string(content) != "platform: linux\n" {
		t.Errorf("expected 'platform: linux\\n', got %q", string(content))
	}
}

func TestDaemonSetVolume_StreamOut_NilCompression_ReturnsRawTar(t *testing.T) {
	// Daemon returns raw tar.
	tarData := makeTarball(t, "README.md", "hello")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarData)
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))
	vol := &DaemonSetVolume{
		key:            "abc",
		handle:         "abc",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     srv.Client(),
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	reader, err := vol.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("StreamOut with nil compression: %v", err)
	}
	defer reader.Close()

	// With nil compression, the stream should be raw tar (NOT gzip).
	data, _ := io.ReadAll(reader)
	if !bytes.Equal(data, tarData) {
		t.Error("expected raw tar bytes to match daemon response")
	}
}

// makeMultiFileTarball creates a tar archive containing multiple files.
// entries is a map of filename → content.
func makeMultiFileTarball(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range entries {
		hdr := &tar.Header{
			Name: name,
			Size: int64(len(content)),
			Mode: 0644,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDaemonSetVolume_StreamOut_SubPath_WithGzip(t *testing.T) {
	// Daemon returns a tar with multiple files (simulating a full repo).
	tarData := makeMultiFileTarball(t, map[string]string{
		"README.md":    "# My Repo",
		"ci/task.yml":  "platform: linux\n",
		"src/main.go":  "package main",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Write(tarData)
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))
	vol := &DaemonSetVolume{
		key:            "abc",
		handle:         "abc",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     srv.Client(),
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	reader, err := vol.StreamOut(context.Background(), "ci/task.yml", compression.NewGzipCompression())
	if err != nil {
		t.Fatalf("StreamOut sub-path: %v", err)
	}
	defer reader.Close()

	// Decompress gzip, then read tar — should contain ONLY ci/task.yml.
	gr, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("tar.Next: %v", err)
	}
	if hdr.Name != "ci/task.yml" {
		t.Errorf("expected tar entry 'ci/task.yml', got %q", hdr.Name)
	}
	content, _ := io.ReadAll(tr)
	if string(content) != "platform: linux\n" {
		t.Errorf("expected 'platform: linux\\n', got %q", string(content))
	}

	// There should be no more entries.
	_, err = tr.Next()
	if err != io.EOF {
		t.Errorf("expected EOF after single entry, got err=%v", err)
	}
}

func TestDaemonSetVolume_StreamOut_RootPath_ReturnsAllFiles(t *testing.T) {
	tarData := makeMultiFileTarball(t, map[string]string{
		"file1.txt": "aaa",
		"file2.txt": "bbb",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarData)
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))
	vol := &DaemonSetVolume{
		key:            "abc",
		handle:         "abc",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     srv.Client(),
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	// "." means root — should return the full tar, not filtered.
	reader, err := vol.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("StreamOut root: %v", err)
	}
	defer reader.Close()

	tr := tar.NewReader(reader)
	count := 0
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 tar entries for root path, got %d", count)
	}
}

func TestDaemonSetVolume_StreamOut_SubPath_FileNotFound(t *testing.T) {
	tarData := makeMultiFileTarball(t, map[string]string{
		"README.md": "hello",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarData)
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))
	vol := &DaemonSetVolume{
		key:            "abc",
		handle:         "abc",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     srv.Client(),
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	reader, err := vol.StreamOut(context.Background(), "nonexistent.yml", compression.NewGzipCompression())
	if err != nil {
		t.Fatalf("StreamOut: %v", err)
	}
	defer reader.Close()

	// Decompressing and reading tar should yield EOF (no matching entry).
	gr, err := gzip.NewReader(reader)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	_, err = tr.Next()
	if err != io.EOF {
		t.Errorf("expected EOF for nonexistent file, got err=%v", err)
	}
}

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

// rewriteTransport redirects all requests to the test server URL.
type rewriteTransport struct {
	url string
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.url, "http://")
	return http.DefaultTransport.RoundTrip(req)
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
