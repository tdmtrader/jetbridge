package jetbridge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDaemonSetVolume_StreamOut_Success(t *testing.T) {
	content := "tar data here"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer srv.Close()

	vol := &DaemonSetVolume{
		key:        "artifacts/abc.tar",
		handle:     "abc",
		workerName: "w1",
		sourceNode: "node-1",
		config:     Config{Namespace: "test-ns", ArtifactDaemonPort: 8080, ArtifactDaemonService: "artifact-daemon"},
		httpClient: srv.Client(),
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

	vol := &DaemonSetVolume{
		key:        "artifacts/missing.tar",
		handle:     "missing",
		workerName: "w1",
		sourceNode: "node-1",
		config:     Config{Namespace: "test-ns", ArtifactDaemonPort: 8080},
		httpClient: srv.Client(),
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
		key:    "artifacts/abc.tar",
		handle: "abc",
	}

	_, err := vol.StreamOut(context.Background(), ".", nil)
	if err == nil {
		t.Error("expected error for no source node")
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
