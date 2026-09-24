package jetbridge

// RESTORED Go 2026-09-18 (round 2) — live-only replacement.
//
// Five `DaemonSetBackend` tests come back from `storage_daemonset_test.go` at
// core `b294dafc49`. The 2026-09-15/16 status-writer sweep retired them against
// `artifact-recording.feature` rows ("A step's output is copied to a second
// node", "Every output is copied, not just the first", "A cache registered for
// a get step is copied off its node too") and a "real-refusal recording case".
// Those scenarios carry `@live-kubernetes` and live under `features/live/`, so
// they run only against a real cluster and never under `make test-unit` — with
// these Go tests gone, nothing on an ordinary unit run asserted that
// RecordOutputs reaches the daemon at all, that /register precedes /mirror on
// the output path, that every output gets its own /mirror, that a failing
// /mirror is survivable, or that /mirror precedes /register on the resource
// cache path.
//
// The rule: a live-tier brine scenario is a valid *replacement* only for a Go
// test that itself only ran on the live tier. It cannot retire a test that ran
// on every unit run.
//
// Bodies are the originals, unweakened.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDaemonSetBackend_RecordOutputs_CallsDaemon(t *testing.T) {
	var registered []map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/register" {
			var req map[string]string
			json.NewDecoder(r.Body).Decode(&req)
			registered = append(registered, req)
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	// Parse test server address to get IP and port
	addr := strings.TrimPrefix(ts.URL, "http://")

	// Create a resolver that returns the test server's IP
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: strings.Split(addr, ":")[0]}},
		},
	}
	resolver := NewNodeIPResolver(fake.NewSimpleClientset(node))

	// Use the test server's port
	cfg := testDaemonConfig()
	// We need to work around the port being different from config. Just test locator recording.
	locator := NewArtifactLocator()
	b := NewDaemonSetBackend(cfg, locator, resolver, nil)

	volumes := []*Volume{
		NewStubVolume("vol-1", "worker", "/tmp/output"),
	}
	spec := runtime.ContainerSpec{
		Outputs: runtime.OutputPaths{"result": "/tmp/output"},
		Type:    db.ContainerTypeTask,
	}

	b.RecordOutputs(context.Background(), "handle", "node-1", volumes, spec)

	// Verify locator was updated
	if _, found := locator.Locate("vol-1"); !found {
		t.Error("expected locator to have entry")
	}
}

func TestDaemonSetBackend_RecordOutputs_TriggersMirrorAfterAlias(t *testing.T) {
	// Track the order /register and /mirror are called so we can assert
	// alias is registered first (the mirror trigger fires after alias on
	// the producer's daemon).
	var (
		mu             sync.Mutex
		callOrder      []string
		registerBodies []map[string]string
		mirrorBodies   []map[string]string
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callOrder = append(callOrder, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/register":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			registerBodies = append(registerBodies, body)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/mirror":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			mirrorBodies = append(mirrorBodies, body)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	addr := strings.TrimPrefix(ts.URL, "http://")
	colonIdx := strings.LastIndex(addr, ":")
	host := addr[:colonIdx]
	port, _ := strconv.Atoi(addr[colonIdx+1:])

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}},
		},
	}
	clientset := fake.NewSimpleClientset(node, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-abc",
			Namespace: "test-ns",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}}},
	})
	resolver := NewNodeIPResolver(clientset)

	cfg := testDaemonConfig()
	cfg.ArtifactDaemonPort = port

	locator := NewArtifactLocator()
	logger := lagertest.NewTestLogger("test")
	b := NewDaemonSetBackend(cfg, locator, resolver, NewDaemonClient(logger, clientset, "test-ns", "artifact-daemon", port, nil))

	volumes := []*Volume{NewStubVolume("vol-1", "worker", "/tmp/output")}
	spec := runtime.ContainerSpec{
		Outputs: runtime.OutputPaths{"result": "/tmp/output"},
		Type:    db.ContainerTypeTask,
	}

	b.RecordOutputs(context.Background(), "handle", "node-1", volumes, spec)

	// Locator entry still recorded (existing behavior).
	if _, found := locator.Locate("vol-1"); !found {
		t.Error("expected locator entry for vol-1")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(registerBodies) != 1 {
		t.Errorf("expected 1 /register call, got %d", len(registerBodies))
	}
	if len(mirrorBodies) != 1 {
		t.Errorf("expected 1 /mirror call, got %d (call order: %v)", len(mirrorBodies), callOrder)
	}
	if len(mirrorBodies) >= 1 {
		// Mirror key is the daemonKey "{handle}/{subdir}", not the volume handle.
		if got := mirrorBodies[0]["key"]; got != "handle/result" {
			t.Errorf("expected mirror key 'handle/result', got %q", got)
		}
	}

	// Order: /register MUST come before /mirror so that if the trigger
	// races with the daemon's mirror.run, the data path is settled first.
	registerIdx, mirrorIdx := -1, -1
	for i, c := range callOrder {
		if c == "POST /register" && registerIdx < 0 {
			registerIdx = i
		}
		if c == "POST /mirror" && mirrorIdx < 0 {
			mirrorIdx = i
		}
	}
	if registerIdx >= 0 && mirrorIdx >= 0 && registerIdx > mirrorIdx {
		t.Errorf("/register must precede /mirror; call order: %v", callOrder)
	}
}

// TestDaemonSetBackend_RecordOutputs_MultipleOutputs_TriggersMirrorForEach
// verifies the realistic case where a task step produces multiple outputs
// (e.g. compiled binary + report). Each output must get both a /register
// AND a /mirror call independently.
func TestDaemonSetBackend_RecordOutputs_MultipleOutputs_TriggersMirrorForEach(t *testing.T) {
	var (
		mu           sync.Mutex
		registerKeys []string
		mirrorKeys   []string
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/register":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			registerKeys = append(registerKeys, body["key"])
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case "/mirror":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			mirrorKeys = append(mirrorKeys, body["key"])
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	addr := strings.TrimPrefix(ts.URL, "http://")
	colonIdx := strings.LastIndex(addr, ":")
	host := addr[:colonIdx]
	port, _ := strconv.Atoi(addr[colonIdx+1:])

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}},
		},
	}
	clientset := fake.NewSimpleClientset(node, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-multi",
			Namespace: "test-ns",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}}},
	})
	resolver := NewNodeIPResolver(clientset)

	cfg := testDaemonConfig()
	cfg.ArtifactDaemonPort = port

	locator := NewArtifactLocator()
	logger := lagertest.NewTestLogger("multi")
	b := NewDaemonSetBackend(cfg, locator, resolver, NewDaemonClient(logger, clientset, "test-ns", "artifact-daemon", port, nil))

	// Three outputs from the same step.
	volumes := []*Volume{
		NewStubVolume("vol-binary", "worker", "/tmp/binary"),
		NewStubVolume("vol-report", "worker", "/tmp/report"),
		NewStubVolume("vol-logs", "worker", "/tmp/logs"),
	}
	spec := runtime.ContainerSpec{
		Outputs: runtime.OutputPaths{
			"binary": "/tmp/binary",
			"report": "/tmp/report",
			"logs":   "/tmp/logs",
		},
		Type: db.ContainerTypeTask,
	}

	b.RecordOutputs(context.Background(), "multi-handle", "node-1", volumes, spec)

	mu.Lock()
	defer mu.Unlock()

	// Expect 3 /register calls and 3 /mirror calls (one per output).
	if len(registerKeys) != 3 {
		t.Errorf("expected 3 /register calls (one per output), got %d: %v", len(registerKeys), registerKeys)
	}
	if len(mirrorKeys) != 3 {
		t.Errorf("expected 3 /mirror calls (one per output), got %d: %v", len(mirrorKeys), mirrorKeys)
	}

	// Each mirror key should be of the form multi-handle/{output} —
	// daemonKey, not the volume handle.
	gotMirrorSet := make(map[string]bool)
	for _, k := range mirrorKeys {
		gotMirrorSet[k] = true
	}
	for _, expected := range []string{"multi-handle/binary", "multi-handle/report", "multi-handle/logs"} {
		if !gotMirrorSet[expected] {
			t.Errorf("expected mirror key %q in set %v", expected, mirrorKeys)
		}
	}
}

func TestDaemonSetBackend_RecordOutputs_TriggerMirrorFailureDoesNotPanic(t *testing.T) {
	// Daemon /register works but /mirror returns 500 — RecordOutputs must
	// still complete without error and locator must still be populated.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/register":
			w.WriteHeader(http.StatusCreated)
		case "/mirror":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	addr := strings.TrimPrefix(ts.URL, "http://")
	colonIdx := strings.LastIndex(addr, ":")
	host := addr[:colonIdx]
	port, _ := strconv.Atoi(addr[colonIdx+1:])

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}},
		},
	}
	clientset := fake.NewSimpleClientset(node)
	resolver := NewNodeIPResolver(clientset)

	cfg := testDaemonConfig()
	cfg.ArtifactDaemonPort = port

	locator := NewArtifactLocator()
	logger := lagertest.NewTestLogger("test")
	b := NewDaemonSetBackend(cfg, locator, resolver, NewDaemonClient(logger, clientset, "test-ns", "artifact-daemon", port, nil))

	volumes := []*Volume{NewStubVolume("vol-1", "worker", "/tmp/output")}
	spec := runtime.ContainerSpec{
		Outputs: runtime.OutputPaths{"result": "/tmp/output"},
		Type:    db.ContainerTypeTask,
	}

	// Must not panic.
	b.RecordOutputs(context.Background(), "handle", "node-1", volumes, spec)

	if _, found := locator.Locate("vol-1"); !found {
		t.Error("expected locator entry even when mirror trigger failed")
	}
}

// ---------------------------------------------------------------------------
// RegisterResourceCache mirror-trigger ordering (P2b.5)
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_RegisterResourceCache_TriggersMirrorBeforeAlias(t *testing.T) {
	// Spec: mirror trigger fires BEFORE the alias broadcast so peers have
	// (or are receiving) the data by the time RegisterAlias broadcasts a
	// /register that requires the path to exist on disk.
	var (
		mu        sync.Mutex
		callOrder []string
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callOrder = append(callOrder, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/register":
			w.WriteHeader(http.StatusCreated)
		case "/mirror":
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	addr := strings.TrimPrefix(ts.URL, "http://")
	colonIdx := strings.LastIndex(addr, ":")
	host := addr[:colonIdx]
	port, _ := strconv.Atoi(addr[colonIdx+1:])

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}},
		},
	}
	clientset := fake.NewSimpleClientset(node, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-fixture",
			Namespace: "test-ns",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}}},
	})
	resolver := NewNodeIPResolver(clientset)

	cfg := testDaemonConfig()
	cfg.ArtifactDaemonPort = port

	locator := NewArtifactLocator()
	logger := lagertest.NewTestLogger("test")
	b := NewDaemonSetBackend(cfg, locator, resolver, NewDaemonClient(logger, clientset, "test-ns", "artifact-daemon", port, nil))

	if err := b.RegisterResourceCache(context.Background(), "rc-42", "", "container-handle-dir", "node-1"); err != nil {
		t.Fatalf("RegisterResourceCache: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	mirrorIdx, registerIdx := -1, -1
	for i, c := range callOrder {
		if c == "POST /mirror" && mirrorIdx < 0 {
			mirrorIdx = i
		}
		if c == "POST /register" && registerIdx < 0 {
			registerIdx = i
		}
	}
	if mirrorIdx < 0 {
		t.Fatalf("expected POST /mirror call, never observed; callOrder=%v", callOrder)
	}
	if registerIdx < 0 {
		t.Fatalf("expected POST /register call, never observed; callOrder=%v", callOrder)
	}
	if mirrorIdx > registerIdx {
		t.Errorf("/mirror MUST precede /register so peers have data when alias broadcasts arrive; got order: %v", callOrder)
	}
}
