package jetbridge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// rewriteTransport redirects all requests to the test server URL.
type rewriteTransport struct {
	url string
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.url, "http://")
	return http.DefaultTransport.RoundTrip(req)
}
