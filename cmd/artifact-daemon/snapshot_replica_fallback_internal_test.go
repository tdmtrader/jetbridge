package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSnapshotResolveFallsThroughCorruptReplica(t *testing.T) {
	archive := snapshotReplicaArchive(t, "verified.txt", "from the healthy replica")
	digestSum := sha256.Sum256(archive)
	digest := hex.EncodeToString(digestSum[:])

	transport := &snapshotReplicaTransport{
		bodies: map[string][]byte{
			"peer-a": []byte("corrupt archive"),
			"peer-b": archive,
		},
	}
	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-slice",
			Namespace: "concourse",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"peer-b", "peer-a"},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	})
	resolver := &PeerResolver{
		logger:      lagertest.NewTestLogger("snapshot-replicas"),
		clientset:   clientset,
		namespace:   "concourse",
		service:     "artifact-daemon",
		port:        7780,
		scheme:      "http",
		probeClient: &http.Client{Transport: transport},
		fetchClient: &http.Client{Transport: transport},
	}

	storage := t.TempDir()
	server := NewServer(lagertest.NewTestLogger("snapshot-fallback"), storage, "node")
	server.SetPeerResolver(resolver)
	destination := filepath.Join(storage, "steps", "consumer", "repository")
	if err := os.MkdirAll(destination, 0755); err != nil {
		t.Fatal(err)
	}

	result := server.resolveSnapshotOne(context.Background(), snapshotKey(digest), digest, destination, "")
	if result.Status != "ok" || result.Method != "snapshot-peer" {
		t.Fatalf("snapshot resolve result = %+v", result)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "verified.txt"))
	if err != nil || string(contents) != "from the healthy replica" {
		t.Fatalf("materialized contents = %q, %v", contents, err)
	}
	if got := transport.getHosts(); len(got) != 4 || got[0] != "peer-a" || got[1] != "peer-a" || got[2] != "peer-a" || got[3] != "peer-b" {
		t.Fatalf("peer GET order = %v, want corrupt replica retries followed by healthy replica", got)
	}
}

type snapshotReplicaTransport struct {
	mu     sync.Mutex
	bodies map[string][]byte
	gets   []string
}

func (transport *snapshotReplicaTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	host := request.URL.Hostname()
	body := []byte(nil)
	if request.Method == http.MethodGet {
		transport.mu.Lock()
		transport.gets = append(transport.gets, host)
		transport.mu.Unlock()
		body = transport.bodies[host]
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}, nil
}

func (transport *snapshotReplicaTransport) getHosts() []string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]string(nil), transport.gets...)
}

func snapshotReplicaArchive(t *testing.T, name, contents string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	payload := []byte(contents)
	if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
