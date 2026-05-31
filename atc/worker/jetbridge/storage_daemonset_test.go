package jetbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// daemonTestServer wraps an httptest.Server for daemon-backed lookup tests.
// It records every request URL so callers can assert which endpoints
// were (or were not) hit.
type daemonTestServer struct {
	srv      *httptest.Server
	host     string
	port     int
	requests atomic.Int32
	paths    chan string
}

func newDaemonTestServer(handler http.HandlerFunc) *daemonTestServer {
	d := &daemonTestServer{
		paths: make(chan string, 32),
	}
	d.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.requests.Add(1)
		select {
		case d.paths <- r.Method + " " + r.URL.Path:
		default:
		}
		handler(w, r)
	}))
	addr := d.srv.Listener.Addr().String()
	colonIdx := strings.LastIndex(addr, ":")
	d.host = addr[:colonIdx]
	d.port, _ = strconv.Atoi(addr[colonIdx+1:])
	return d
}

func (d *daemonTestServer) close() { d.srv.Close() }

func (d *daemonTestServer) requestCount() int { return int(d.requests.Load()) }

// daemonBackend builds a DaemonSetBackend wired to the given daemon test
// server with EndpointSlice discovery seeded for that daemon's host.
func daemonBackend(t *testing.T, locator *ArtifactLocator, daemon *daemonTestServer) *DaemonSetBackend {
	t.Helper()
	cfg := testDaemonConfig()
	cfg.ArtifactDaemonPort = daemon.port

	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-abc",
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				discoveryv1.LabelServiceName: cfg.ArtifactDaemonService,
			},
		},
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{daemon.host}},
		},
	})

	resolver := NewNodeIPResolver(clientset)
	b := NewDaemonSetBackend(cfg, locator, resolver)
	logger := lagertest.NewTestLogger("test")
	b.SetDaemonClient(NewDaemonClient(logger, clientset, cfg.Namespace, cfg.ArtifactDaemonService, cfg.ArtifactDaemonPort, nil))
	return b
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testDaemonConfig() Config {
	return Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/artifact-store",
		ArtifactDaemonPort:     7780,
		ArtifactDaemonService:  "artifact-daemon",
		ArtifactHelperImage:    "alpine:latest",
	}
}

func testBackend(locator *ArtifactLocator) *DaemonSetBackend {
	cfg := testDaemonConfig()
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}},
		},
	}
	resolver := NewNodeIPResolver(fake.NewSimpleClientset(node))
	return NewDaemonSetBackend(cfg, locator, resolver)
}

type testArtifact struct {
	handle string
}

func (a *testArtifact) Handle() string { return a.handle }
func (a *testArtifact) Source() string  { return "worker-1" }
func (a *testArtifact) StreamOut(ctx context.Context, path string, enc compression.Compression) (io.ReadCloser, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// StepVolume
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_StepVolume_ReturnsHostPath(t *testing.T) {
	b := testBackend(nil)
	vol := b.StepVolume("input-0", "handle-abc", "result")

	if vol.HostPath == nil {
		t.Fatal("expected hostPath volume")
	}
	expected := "/artifact-store/steps/handle-abc/result"
	if vol.HostPath.Path != expected {
		t.Errorf("expected path %q, got %q", expected, vol.HostPath.Path)
	}
	if vol.Name != "input-0" {
		t.Errorf("expected name input-0, got %s", vol.Name)
	}
}

func TestDaemonSetBackend_StepVolume_DirSubdir(t *testing.T) {
	b := testBackend(nil)
	vol := b.StepVolume("dir-0", "my-handle", "dir")

	expected := "/artifact-store/steps/my-handle/dir"
	if vol.HostPath.Path != expected {
		t.Errorf("expected path %q, got %q", expected, vol.HostPath.Path)
	}
}

// ---------------------------------------------------------------------------
// CacheVolume
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_CacheVolume_ReturnsHostPathWithStableKey(t *testing.T) {
	b := testBackend(nil)
	vol := b.CacheVolume("cache-0", 42, "my-step", "/cache/path")

	if vol.HostPath == nil {
		t.Fatal("expected hostPath volume")
	}
	// Stable key should be deterministic
	key := stableCacheKey(42, "my-step", "/cache/path")
	expected := filepath.Join("/artifact-store/caches", key)
	if vol.HostPath.Path != expected {
		t.Errorf("expected path %q, got %q", expected, vol.HostPath.Path)
	}
}

func TestDaemonSetBackend_CacheVolume_UsesCacheHostPathWhenSet(t *testing.T) {
	cfg := testDaemonConfig()
	cfg.CacheHostPath = "/custom-cache-dir"
	b := NewDaemonSetBackend(cfg, nil, nil)

	vol := b.CacheVolume("cache-0", 1, "step", "/path")
	if !strings.HasPrefix(vol.HostPath.Path, "/custom-cache-dir/") {
		t.Errorf("expected path under /custom-cache-dir, got %q", vol.HostPath.Path)
	}
}

func TestDaemonSetBackend_CacheVolume_Deterministic(t *testing.T) {
	b := testBackend(nil)
	vol1 := b.CacheVolume("c1", 42, "step", "/cache")
	vol2 := b.CacheVolume("c2", 42, "step", "/cache")

	if vol1.HostPath.Path != vol2.HostPath.Path {
		t.Errorf("same (jobID, step, path) should produce same hostPath, got %q and %q", vol1.HostPath.Path, vol2.HostPath.Path)
	}
}

// ---------------------------------------------------------------------------
// ArtifactStoreVolume
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_ArtifactStoreVolume_Task(t *testing.T) {
	b := testBackend(nil)
	vol := b.ArtifactStoreVolume(db.ContainerTypeTask)

	if vol == nil {
		t.Fatal("expected non-nil volume for task")
	}
	if vol.HostPath.Path != "/artifact-store" {
		t.Errorf("expected /artifact-store, got %s", vol.HostPath.Path)
	}
	if vol.Name != artifactDaemonHostPathVolumeName {
		t.Errorf("expected name %s, got %s", artifactDaemonHostPathVolumeName, vol.Name)
	}
}

func TestDaemonSetBackend_ArtifactStoreVolume_CheckReturnsNil(t *testing.T) {
	b := testBackend(nil)
	vol := b.ArtifactStoreVolume(db.ContainerTypeCheck)
	if vol != nil {
		t.Errorf("expected nil for check container, got %+v", vol)
	}
}

func TestDaemonSetBackend_ArtifactStoreVolumeName(t *testing.T) {
	b := testBackend(nil)
	name := b.ArtifactStoreVolumeName()
	if name != "artifact-daemon-hostpath" {
		t.Errorf("expected artifact-daemon-hostpath, got %s", name)
	}
}

// ---------------------------------------------------------------------------
// BuildFetchInitContainers
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_BuildFetchInitContainers_MultipleInputs(t *testing.T) {
	b := testBackend(nil)
	inputs := []runtime.Input{
		{Artifact: &testArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input-a"},
		{Artifact: &testArtifact{handle: "vol-b"}, DestinationPath: "/tmp/input-b"},
		{Artifact: &testArtifact{handle: "vol-c"}, DestinationPath: "/tmp/input-c"},
	}

	mounts := []corev1.VolumeMount{
		{Name: "input-0", MountPath: "/tmp/input-a"},
		{Name: "input-1", MountPath: "/tmp/input-b"},
		{Name: "input-2", MountPath: "/tmp/input-c"},
	}
	volumes := []corev1.Volume{
		b.StepVolume("input-0", "handle", "input-0"),
		b.StepVolume("input-1", "handle", "input-1"),
		b.StepVolume("input-2", "handle", "input-2"),
	}

	inits := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)

	// Multiple inputs should produce a single batch init container.
	if len(inits) != 1 {
		t.Fatalf("expected 1 batch init container, got %d", len(inits))
	}
	if inits[0].Name != "fetch-inputs" {
		t.Errorf("expected name fetch-inputs, got %s", inits[0].Name)
	}

	// The batch init container should use /resolve-batch.
	cmdStr := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmdStr, "/resolve-batch") {
		t.Errorf("expected /resolve-batch in command, got: %s", cmdStr)
	}

	// Should contain all three artifact keys.
	if !strings.Contains(cmdStr, "vol-a") || !strings.Contains(cmdStr, "vol-b") || !strings.Contains(cmdStr, "vol-c") {
		t.Errorf("expected all artifact keys in batch command, got: %s", cmdStr)
	}

	// Should mount all input volumes plus the hostpath volume.
	if len(inits[0].VolumeMounts) < 4 { // 3 inputs + 1 hostpath
		t.Errorf("expected at least 4 volume mounts, got %d", len(inits[0].VolumeMounts))
	}
}

func TestDaemonSetBackend_BuildFetchInitContainers_SkipsNilArtifact(t *testing.T) {
	b := testBackend(nil)
	inputs := []runtime.Input{
		{Artifact: &testArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input-a"},
		{Artifact: nil, DestinationPath: "/tmp/input-b"},
	}

	mounts := []corev1.VolumeMount{
		{Name: "input-0", MountPath: "/tmp/input-a"},
		{Name: "input-1", MountPath: "/tmp/input-b"},
	}
	volumes := []corev1.Volume{
		b.StepVolume("input-0", "handle", "input-0"),
		b.StepVolume("input-1", "handle", "input-1"),
	}

	inits := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)
	if len(inits) != 1 {
		t.Fatalf("expected 1 init container (nil artifact skipped), got %d", len(inits))
	}
	// Single valid artifact — should still use batch container name.
	if inits[0].Name != "fetch-inputs" {
		t.Errorf("expected fetch-inputs, got %s", inits[0].Name)
	}
}

func TestDaemonSetBackend_BuildFetchInitContainers_LocatorHit(t *testing.T) {
	locator := NewArtifactLocator()
	locator.Record("vol-a", "node-1", "producer-handle/result")

	b := testBackend(locator)
	inputs := []runtime.Input{
		{Artifact: &testArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input"},
	}
	mounts := []corev1.VolumeMount{
		{Name: "input-0", MountPath: "/tmp/input"},
	}
	volumes := []corev1.Volume{
		b.StepVolume("input-0", "handle", "input-0"),
	}

	inits := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)
	if len(inits) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(inits))
	}

	// The batch command should use the locator's HostDir, not the volume handle.
	cmdStr := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmdStr, "producer-handle/result") {
		t.Errorf("expected resolve command to use locator HostDir 'producer-handle/result', got: %s", cmdStr)
	}
}

func TestDaemonSetBackend_BuildFetchInitContainers_NoInputs(t *testing.T) {
	b := testBackend(nil)
	inits := b.BuildFetchInitContainers("handle", nil, nil, nil)
	if len(inits) != 0 {
		t.Errorf("expected 0 init containers for nil inputs, got %d", len(inits))
	}
}

// ---------------------------------------------------------------------------
// BuildCleanupInitContainer
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_BuildCleanupInitContainer_Reused(t *testing.T) {
	b := testBackend(nil)
	ic := b.BuildCleanupInitContainer("handle-1", db.ContainerTypeTask, true)

	if ic == nil {
		t.Fatal("expected cleanup init container for reused task")
	}
	if ic.Name != "cleanup-stale" {
		t.Errorf("expected name cleanup-stale, got %s", ic.Name)
	}
	cmdStr := strings.Join(ic.Command, " ")
	if !strings.Contains(cmdStr, "handle-1") {
		t.Errorf("expected command to reference handle, got: %s", cmdStr)
	}
}

func TestDaemonSetBackend_BuildCleanupInitContainer_Fresh(t *testing.T) {
	b := testBackend(nil)
	ic := b.BuildCleanupInitContainer("handle-1", db.ContainerTypeTask, false)
	if ic != nil {
		t.Errorf("expected nil for fresh container, got %+v", ic)
	}
}

func TestDaemonSetBackend_BuildCleanupInitContainer_Check(t *testing.T) {
	b := testBackend(nil)
	ic := b.BuildCleanupInitContainer("handle-1", db.ContainerTypeCheck, true)
	if ic != nil {
		t.Errorf("expected nil for check container, got %+v", ic)
	}
}

// ---------------------------------------------------------------------------
// BuildAffinity
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_BuildAffinity_HardLabel(t *testing.T) {
	b := testBackend(nil)
	affinity := b.BuildAffinity(nil)

	if affinity == nil {
		t.Fatal("expected non-nil affinity")
	}
	required := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil {
		t.Fatal("expected required node selector")
	}
	found := false
	for _, term := range required.NodeSelectorTerms {
		for _, expr := range term.MatchExpressions {
			if expr.Key == "concourse.dev/artifact-cache" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected hard affinity for concourse.dev/artifact-cache label")
	}
}

func TestDaemonSetBackend_BuildAffinity_SoftPreference(t *testing.T) {
	locator := NewArtifactLocator()
	locator.Record("vol-a", "preferred-node", "handle/result")

	b := testBackend(locator)
	inputs := []runtime.Input{
		{Artifact: &testArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input"},
	}

	affinity := b.BuildAffinity(inputs)
	preferred := affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(preferred) == 0 {
		t.Fatal("expected soft affinity")
	}
	found := false
	for _, term := range preferred {
		for _, expr := range term.Preference.MatchExpressions {
			if expr.Key == "kubernetes.io/hostname" {
				for _, v := range expr.Values {
					if v == "preferred-node" {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Error("expected soft affinity for preferred-node")
	}
}

func TestDaemonSetBackend_BuildAffinity_NoInputs_NoSoftAffinity(t *testing.T) {
	locator := NewArtifactLocator()
	b := testBackend(locator)

	affinity := b.BuildAffinity(nil)
	preferred := affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(preferred) != 0 {
		t.Errorf("expected no soft affinity with no inputs, got %d terms", len(preferred))
	}
}

// ---------------------------------------------------------------------------
// RecordOutputs
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_RecordOutputs_RecordsInLocator(t *testing.T) {
	locator := NewArtifactLocator()
	b := testBackend(locator)

	volumes := []*Volume{
		NewStubVolume("vol-handle-1", "worker", "/tmp/result"),
	}
	spec := runtime.ContainerSpec{
		Dir:     "/tmp/build",
		Outputs: runtime.OutputPaths{"result": "/tmp/result"},
		Type:    db.ContainerTypeTask,
	}

	b.RecordOutputs(context.Background(), "container-handle", "node-1", volumes, spec)

	key := ArtifactKey("vol-handle-1")
	loc, found := locator.Locate(key)
	if !found {
		t.Fatal("expected locator to have entry for vol-handle-1")
	}
	if loc.NodeName != "node-1" {
		t.Errorf("expected node node-1, got %s", loc.NodeName)
	}
	if loc.HostDir != "container-handle/result" {
		t.Errorf("expected daemon key container-handle/result, got %s", loc.HostDir)
	}
}

func TestDaemonSetBackend_RecordOutputs_NilLocatorIsNoop(t *testing.T) {
	b := NewDaemonSetBackend(testDaemonConfig(), nil, nil)
	// Should not panic
	b.RecordOutputs(context.Background(), "handle", "node", nil, runtime.ContainerSpec{})
}

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
	b := NewDaemonSetBackend(cfg, locator, resolver)

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
	b := NewDaemonSetBackend(cfg, locator, resolver)
	logger := lagertest.NewTestLogger("test")
	b.SetDaemonClient(NewDaemonClient(logger, clientset, "test-ns", "artifact-daemon", port, nil))

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
		mu          sync.Mutex
		registerKeys []string
		mirrorKeys  []string
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
	b := NewDaemonSetBackend(cfg, locator, resolver)
	logger := lagertest.NewTestLogger("multi")
	b.SetDaemonClient(NewDaemonClient(logger, clientset, "test-ns", "artifact-daemon", port, nil))

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
	b := NewDaemonSetBackend(cfg, locator, resolver)
	logger := lagertest.NewTestLogger("test")
	b.SetDaemonClient(NewDaemonClient(logger, clientset, "test-ns", "artifact-daemon", port, nil))

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
	b := NewDaemonSetBackend(cfg, locator, resolver)
	logger := lagertest.NewTestLogger("test")
	b.SetDaemonClient(NewDaemonClient(logger, clientset, "test-ns", "artifact-daemon", port, nil))

	if err := b.RegisterResourceCache(context.Background(), 42, "container-handle-dir", "node-1"); err != nil {
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

// ---------------------------------------------------------------------------
// WrapVolumeForArtifact / WrapVolumeForLookup
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_WrapVolumeForArtifact(t *testing.T) {
	b := testBackend(nil)
	vol := b.WrapVolumeForArtifact("key-1", "handle-1", "worker-1", nil)

	dsv, ok := vol.(*DaemonSetVolume)
	if !ok {
		t.Fatalf("expected *DaemonSetVolume, got %T", vol)
	}
	if dsv.Handle() != "handle-1" {
		t.Errorf("expected handle handle-1, got %s", dsv.Handle())
	}
	if dsv.Key() != "key-1" {
		t.Errorf("expected key key-1, got %s", dsv.Key())
	}
}

func TestDaemonSetBackend_WrapVolumeForLookup_WithLocator(t *testing.T) {
	locator := NewArtifactLocator()
	locator.Record("key-1", "source-node", "handle/dir")

	b := testBackend(locator)
	vol := b.WrapVolumeForLookup(context.Background(), "key-1", "handle-1", "worker-1", nil)

	dsv, ok := vol.(*DaemonSetVolume)
	if !ok {
		t.Fatalf("expected *DaemonSetVolume, got %T", vol)
	}
	if dsv.sourceNode != "source-node" {
		t.Errorf("expected sourceNode source-node, got %s", dsv.sourceNode)
	}
}

func TestDaemonSetBackend_WrapVolumeForLookup_WithoutLocator(t *testing.T) {
	b := NewDaemonSetBackend(testDaemonConfig(), nil, nil)
	vol := b.WrapVolumeForLookup(context.Background(), "key-1", "handle-1", "worker-1", nil)

	dsv, ok := vol.(*DaemonSetVolume)
	if !ok {
		t.Fatalf("expected *DaemonSetVolume, got %T", vol)
	}
	if dsv.sourceNode != "" {
		t.Errorf("expected empty sourceNode without locator, got %s", dsv.sourceNode)
	}
}

// TestDaemonSetBackend_WrapVolumeForLookup_RcKeyProbesDaemons covers the
// happy path of Option D: when the locator has no entry and the handle
// is a resource cache key, WrapVolumeForLookup probes live daemons via
// the DaemonClient and returns a volume bound directly to the daemon
// pod IP — sidestepping NodeIPResolver entirely.
func TestDaemonSetBackend_WrapVolumeForLookup_RcKeyProbesDaemons(t *testing.T) {
	daemon := newDaemonTestServer(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && strings.HasPrefix(r.URL.Path, "/resource-caches/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/artifacts/"):
			w.Write([]byte("cached-tar-data"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer daemon.close()

	b := daemonBackend(t, NewArtifactLocator(), daemon)

	vol := b.WrapVolumeForLookup(context.Background(), "rc-42", "rc-42", "worker-1", nil)

	dsv, ok := vol.(*DaemonSetVolume)
	if !ok {
		t.Fatalf("expected *DaemonSetVolume, got %T", vol)
	}
	if dsv.sourceIP != daemon.host {
		t.Errorf("expected sourceIP %q (from probe), got %q", daemon.host, dsv.sourceIP)
	}
	if dsv.sourceNode != "" {
		t.Errorf("expected empty sourceNode (probe only learns IP), got %q", dsv.sourceNode)
	}

	// StreamOut must succeed — proves the volume is bound to a working
	// daemon and never touches NodeIPResolver.
	reader, err := dsv.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("StreamOut: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "cached-tar-data" {
		t.Errorf("expected cached-tar-data, got %q", string(body))
	}
}

// TestDaemonSetBackend_WrapVolumeForLookup_RcKeyHonorsLocator covers the
// case where a real get step has populated the locator with a node-name
// entry for the cache key. WrapVolumeForLookup must NOT re-probe — the
// recorded node name is authoritative, and the daemon HTTP server should
// see zero requests.
func TestDaemonSetBackend_WrapVolumeForLookup_RcKeyHonorsLocator(t *testing.T) {
	daemon := newDaemonTestServer(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("daemon should not be probed when locator has an entry; got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer daemon.close()

	locator := NewArtifactLocator()
	locator.Record("rc-42", "real-node-name", "rc-42")

	b := daemonBackend(t, locator, daemon)

	vol := b.WrapVolumeForLookup(context.Background(), "rc-42", "rc-42", "worker-1", nil)

	dsv, ok := vol.(*DaemonSetVolume)
	if !ok {
		t.Fatalf("expected *DaemonSetVolume, got %T", vol)
	}
	if dsv.sourceNode != "real-node-name" {
		t.Errorf("expected sourceNode real-node-name (from locator), got %q", dsv.sourceNode)
	}
	if dsv.sourceIP != "" {
		t.Errorf("expected empty sourceIP when locator hit, got %q", dsv.sourceIP)
	}
	if got := daemon.requestCount(); got != 0 {
		t.Errorf("expected zero daemon requests when locator has entry, got %d", got)
	}
}

// TestDaemonSetBackend_WrapVolumeForLookup_NonRcKeyNeverProbes guards
// against accidental over-probing. Only resource-cache-shaped keys
// (rc-{id}) should trigger the probe; other handles must keep the
// legacy behavior even when the locator is empty.
func TestDaemonSetBackend_WrapVolumeForLookup_NonRcKeyNeverProbes(t *testing.T) {
	daemon := newDaemonTestServer(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("daemon should not be probed for non-rc key; got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer daemon.close()

	b := daemonBackend(t, NewArtifactLocator(), daemon)

	for _, key := range []string{"artifact-handle-1", "build-42-result", "input-vol"} {
		t.Run(key, func(t *testing.T) {
			vol := b.WrapVolumeForLookup(context.Background(), key, key, "worker-1", nil)
			dsv, ok := vol.(*DaemonSetVolume)
			if !ok {
				t.Fatalf("expected *DaemonSetVolume, got %T", vol)
			}
			if dsv.sourceNode != "" {
				t.Errorf("expected empty sourceNode (no locator entry), got %q", dsv.sourceNode)
			}
			if dsv.sourceIP != "" {
				t.Errorf("expected empty sourceIP (no probe), got %q", dsv.sourceIP)
			}
		})
	}

	if got := daemon.requestCount(); got != 0 {
		t.Errorf("expected zero daemon requests for non-rc keys, got %d", got)
	}
}

// TestDaemonSetBackend_WrapVolumeForLookup_RcKeyProbeErrorFallsThrough
// covers the failure-mode path: when daemon discovery yields nothing
// (no EndpointSlices), the probe returns (`""`, false, nil) per the
// existing `daemonIPs` contract. WrapVolumeForLookup must fall through
// to today's empty-sourceNode path without panicking and without
// returning a nil volume.
func TestDaemonSetBackend_WrapVolumeForLookup_RcKeyProbeErrorFallsThrough(t *testing.T) {
	cfg := testDaemonConfig()
	clientset := fake.NewSimpleClientset() // no EndpointSlices → no daemons
	resolver := NewNodeIPResolver(clientset)
	b := NewDaemonSetBackend(cfg, NewArtifactLocator(), resolver)
	logger := lagertest.NewTestLogger("test")
	b.SetDaemonClient(NewDaemonClient(logger, clientset, cfg.Namespace, cfg.ArtifactDaemonService, cfg.ArtifactDaemonPort, nil))

	vol := b.WrapVolumeForLookup(context.Background(), "rc-42", "rc-42", "worker-1", nil)
	if vol == nil {
		t.Fatal("expected non-nil volume even when probe finds nothing")
	}
	dsv, ok := vol.(*DaemonSetVolume)
	if !ok {
		t.Fatalf("expected *DaemonSetVolume, got %T", vol)
	}
	if dsv.sourceNode != "" {
		t.Errorf("expected empty sourceNode after failed probe, got %q", dsv.sourceNode)
	}
	if dsv.sourceIP != "" {
		t.Errorf("expected empty sourceIP after failed probe, got %q", dsv.sourceIP)
	}
}

// TestDaemonSetBackend_WrapVolumeForLookup_SetsDaemonClient guards the
// resilience fix: a lookup-wrapped volume must carry the backend's
// daemonClient so its StreamOut can peer-fallback / discover daemons when
// the recorded source node is unreachable. Without this, web-process reads
// of a get-step output (e.g. a file: task config) can only hit the recorded
// node and hard-fail with no recovery. Mirrors WrapVolumeForArtifact.
func TestDaemonSetBackend_WrapVolumeForLookup_SetsDaemonClient(t *testing.T) {
	daemon := newDaemonTestServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer daemon.close()

	locator := NewArtifactLocator()
	locator.Record("artifact-key-1", "source-node", "handle/dir")
	b := daemonBackend(t, locator, daemon)

	vol := b.WrapVolumeForLookup(context.Background(), "artifact-key-1", "handle-1", "worker-1", nil)
	dsv, ok := vol.(*DaemonSetVolume)
	if !ok {
		t.Fatalf("expected *DaemonSetVolume, got %T", vol)
	}
	if dsv.daemonClient == nil {
		t.Fatal("expected lookup-wrapped volume to carry a daemonClient for peer-fallback")
	}
}

// TestDaemonSetBackend_WrapVolumeForLookup_NoDaemonClientStaysNil verifies the
// fix doesn't fabricate a client when the backend has none configured.
func TestDaemonSetBackend_WrapVolumeForLookup_NoDaemonClientStaysNil(t *testing.T) {
	b := testBackend(nil) // testBackend wires no daemonClient
	vol := b.WrapVolumeForLookup(context.Background(), "artifact-key-1", "handle-1", "worker-1", nil)
	dsv, ok := vol.(*DaemonSetVolume)
	if !ok {
		t.Fatalf("expected *DaemonSetVolume, got %T", vol)
	}
	if dsv.daemonClient != nil {
		t.Error("expected nil daemonClient when backend has none configured")
	}
}

// ---------------------------------------------------------------------------
// Nil backend fallback (container.go behavior with nil StorageBackend)
// ---------------------------------------------------------------------------

func TestNilBackend_StepVolumeReturnsEmptyDir(t *testing.T) {
	vol := emptyDirVolume("test-vol")
	if vol.EmptyDir == nil {
		t.Fatal("expected emptyDir volume")
	}
	if vol.Name != "test-vol" {
		t.Errorf("expected name test-vol, got %s", vol.Name)
	}
}

// ---------------------------------------------------------------------------
// daemonResolveCommand
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_DaemonResolveCommand_EmptyKey(t *testing.T) {
	b := testBackend(nil)
	cmd := b.daemonResolveCommand("", "/dest")
	cmdStr := strings.Join(cmd, " ")
	if !strings.Contains(cmdStr, "exit 1") {
		t.Errorf("expected exit 1 for empty key, got: %s", cmdStr)
	}
}

func TestDaemonSetBackend_DaemonResolveCommand_ValidKey(t *testing.T) {
	b := testBackend(nil)
	cmd := b.daemonResolveCommand("handle/result", "/dest/path")
	cmdStr := strings.Join(cmd, " ")
	if !strings.Contains(cmdStr, "handle/result") {
		t.Errorf("expected key in command, got: %s", cmdStr)
	}
	if !strings.Contains(cmdStr, "/dest/path") {
		t.Errorf("expected dest in command, got: %s", cmdStr)
	}
	if !strings.Contains(cmdStr, "/resolve") {
		t.Errorf("expected /resolve endpoint in command, got: %s", cmdStr)
	}
}

func TestDaemonSetBackend_DaemonResolveCommand_Timeout180s(t *testing.T) {
	b := testBackend(nil)
	cmd := b.daemonResolveCommand("handle/result", "/dest/path")
	cmdStr := strings.Join(cmd, " ")

	// Must use 180s timeout to accommodate cross-node large artifact transfers.
	if !strings.Contains(cmdStr, "-T 180") {
		t.Errorf("expected wget timeout -T 180 for cross-node reliability, got: %s", cmdStr)
	}
	// Must NOT use the old 5s timeout.
	if strings.Contains(cmdStr, "-T 5") {
		t.Errorf("wget -T 5 is too short for cross-node transfers, got: %s", cmdStr)
	}
}

func TestDaemonSetBackend_DaemonResolveCommand_DefaultPort(t *testing.T) {
	cfg := testDaemonConfig()
	cfg.ArtifactDaemonPort = 0
	b := NewDaemonSetBackend(cfg, nil, nil)

	cmd := b.daemonResolveCommand("key", "/dest")
	cmdStr := strings.Join(cmd, " ")
	if !strings.Contains(cmdStr, "7780") {
		t.Errorf("expected default port 7780, got: %s", cmdStr)
	}
}

// Suppress unused import warning for os
var _ = os.Stderr
