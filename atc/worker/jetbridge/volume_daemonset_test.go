package jetbridge

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/compression"
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

// rewriteTransport redirects all requests to the test server URL.
type rewriteTransport struct {
	url string
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.url, "http://")
	return http.DefaultTransport.RoundTrip(req)
}
