package main_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// ---------------------------------------------------------------------------
// Test 15: Cross-node resolution via peer fallback
// ---------------------------------------------------------------------------

func TestDaemonResolve_CrossNode_PeerFallback(t *testing.T) {
	// Server A: the peer that has the artifact.
	storageA := t.TempDir()
	stepDir := filepath.Join(storageA, "steps", "cross-handle", "output")
	if err := os.MkdirAll(stepDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stepDir, "cross-data.txt"), []byte("cross-node-artifact"), 0644); err != nil {
		t.Fatal(err)
	}

	loggerA := lagertest.NewTestLogger("server-a")
	serverA := daemon.NewServer(loggerA, storageA, "node-a")
	tsA := httptest.NewServer(serverA.Handler())
	defer tsA.Close()

	hostA, portA := splitHostPort(t, tsA.Listener.Addr().String())

	// Server B: the local daemon that does NOT have the artifact.
	storageB := t.TempDir()
	loggerB := lagertest.NewTestLogger("server-b")
	serverB := daemon.NewServer(loggerB, storageB, "node-b")

	// Create a fake K8s clientset with an EndpointSlice pointing to server A.
	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-slice",
			Namespace: "concourse",
			Labels: map[string]string{
				discoveryv1.LabelServiceName: "artifact-daemon",
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{hostA},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			},
		},
	})

	// Attach PeerResolver to server B. Use a bogus myPodIP so server A
	// is not skipped as self.
	resolver := daemon.NewPeerResolver(loggerB, clientset, "concourse", "artifact-daemon", portA, "10.0.0.99", nil)
	serverB.SetPeerResolver(resolver)

	tsB := httptest.NewServer(serverB.Handler())
	defer tsB.Close()

	// POST /resolve to server B, which should discover the artifact on server A.
	destDir := filepath.Join(t.TempDir(), "cross-resolved")
	resolveBody := `{"key":"cross-handle/output","dest":"` + destDir + `"}`
	resp, err := http.Post(tsB.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatalf("POST /resolve to server B: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result resolveResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Status != "ok" {
		t.Errorf("expected status=ok, got %q", result.Status)
	}
	if result.Method != "peer" {
		t.Errorf("expected method=peer, got %q", result.Method)
	}

	// Verify the artifact data was copied to the destination.
	data, err := os.ReadFile(filepath.Join(destDir, "cross-data.txt"))
	if err != nil {
		t.Fatalf("artifact not resolved from peer: %v", err)
	}
	if string(data) != "cross-node-artifact" {
		t.Errorf("expected 'cross-node-artifact', got %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// Test 16: Local resolution via registry (no peer queries)
// ---------------------------------------------------------------------------

func TestDaemonResolve_LocalRegistry(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create artifact data on disk.
	srcDir := filepath.Join(storagePath, "steps", "local-handle", "output")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "local-file.txt"), []byte("local-data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Register artifact in the daemon's registry via POST /register.
	regBody := `{"key":"local-handle/output","local_path":"` + srcDir + `"}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d", resp.StatusCode)
	}

	// POST /resolve — should resolve via local registry.
	destDir := filepath.Join(t.TempDir(), "local-resolved")
	resolveBody := `{"key":"local-handle/output","dest":"` + destDir + `"}`
	resp, err = http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatalf("POST /resolve: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result resolveResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Status != "ok" {
		t.Errorf("expected status=ok, got %q", result.Status)
	}
	if result.Method != "registry" {
		t.Errorf("expected method=registry, got %q", result.Method)
	}

	// Verify file was copied to destination.
	data, err := os.ReadFile(filepath.Join(destDir, "local-file.txt"))
	if err != nil {
		t.Fatalf("file not resolved locally: %v", err)
	}
	if string(data) != "local-data" {
		t.Errorf("expected 'local-data', got %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// Test: Local resolution prevents peer queries
// ---------------------------------------------------------------------------

func TestDaemonResolve_LocalRegistry_NoPeerQueries(t *testing.T) {
	// Set up a peer daemon that would fail if queried.
	peerCalled := false
	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerCalled = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fakePeer.Close()

	peerHost, peerPort := splitHostPort(t, fakePeer.Listener.Addr().String())

	// Set up local daemon with artifact registered locally.
	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("local-no-peer")
	server := daemon.NewServer(logger, storagePath, "test-node")

	// Wire up peer resolver pointing to the fake peer.
	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "slice",
			Namespace: "ns",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "svc"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{peerHost},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			},
		},
	})
	resolver := daemon.NewPeerResolver(logger, clientset, "ns", "svc", peerPort, "10.0.0.99", nil)
	server.SetPeerResolver(resolver)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	// Create and register artifact locally.
	srcDir := filepath.Join(storagePath, "steps", "no-peer-handle", "dir")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("local-only"), 0644)

	regBody := `{"key":"no-peer-handle/dir","local_path":"` + srcDir + `"}`
	resp, _ := http.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
	resp.Body.Close()

	// Resolve — should use registry, NOT contact peer.
	destDir := filepath.Join(t.TempDir(), "no-peer-dest")
	resolveBody := `{"key":"no-peer-handle/dir","dest":"` + destDir + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if peerCalled {
		t.Error("peer was queried even though artifact was available locally")
	}

	data, _ := os.ReadFile(filepath.Join(destDir, "file.txt"))
	if string(data) != "local-only" {
		t.Errorf("expected 'local-only', got %q", string(data))
	}
}
