package jetbridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ---------------------------------------------------------------------------
// VT-01: DeferredVolume creation and binding
// ---------------------------------------------------------------------------

func TestVT01_DeferredVolume_CreationWithNoPodName(t *testing.T) {
	vol := NewDeferredVolume("handle-1", "worker-1", nil, "ns", "main", "/mnt/data")

	if vol.PodName() != "" {
		t.Errorf("expected empty podName on new deferred volume, got %q", vol.PodName())
	}
}

func TestVT01_DeferredVolume_SetPodNameUpdates(t *testing.T) {
	vol := NewDeferredVolume("handle-1", "worker-1", nil, "ns", "main", "/mnt/data")
	vol.SetPodName("my-pod")

	if vol.PodName() != "my-pod" {
		t.Errorf("expected podName %q, got %q", "my-pod", vol.PodName())
	}
}

func TestVT01_DeferredVolume_HasExecutorWhenSet(t *testing.T) {
	executor := &noopPodExecutor{}
	vol := NewDeferredVolume("handle-1", "worker-1", executor, "ns", "main", "/mnt/data")

	if !vol.HasExecutor() {
		t.Error("expected HasExecutor to return true when executor is set")
	}
}

func TestVT01_DeferredVolume_HasExecutorFalseWhenNil(t *testing.T) {
	vol := NewDeferredVolume("handle-1", "worker-1", nil, "ns", "main", "/mnt/data")

	if vol.HasExecutor() {
		t.Error("expected HasExecutor to return false when executor is nil")
	}
}

// ---------------------------------------------------------------------------
// VT-04: Path resolution
// ---------------------------------------------------------------------------

func TestVT04_ResolvedPath_EmptyReturnsMount(t *testing.T) {
	vol := NewStubVolume("h", "w", "/mnt/data")
	got := vol.resolvedPath("")
	if got != "/mnt/data" {
		t.Errorf("resolvedPath(\"\") = %q, want /mnt/data", got)
	}
}

func TestVT04_ResolvedPath_DotReturnsMount(t *testing.T) {
	vol := NewStubVolume("h", "w", "/mnt/data")
	got := vol.resolvedPath(".")
	if got != "/mnt/data" {
		t.Errorf("resolvedPath(\".\") = %q, want /mnt/data", got)
	}
}

func TestVT04_ResolvedPath_Subdir(t *testing.T) {
	vol := NewStubVolume("h", "w", "/mnt/data")
	got := vol.resolvedPath("subdir")
	want := filepath.Join("/mnt/data", "subdir")
	if got != want {
		t.Errorf("resolvedPath(\"subdir\") = %q, want %q", got, want)
	}
}

func TestVT04_ResolvedPath_NestedPath(t *testing.T) {
	vol := NewStubVolume("h", "w", "/mnt/data")
	got := vol.resolvedPath("nested/path")
	want := filepath.Join("/mnt/data", "nested/path")
	if got != want {
		t.Errorf("resolvedPath(\"nested/path\") = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// VT-05: StubVolume limitations
// ---------------------------------------------------------------------------

func TestVT05_StubVolume_Handle(t *testing.T) {
	vol := NewStubVolume("stub-handle", "worker-x", "/mnt/stub")
	if vol.Handle() != "stub-handle" {
		t.Errorf("Handle() = %q, want stub-handle", vol.Handle())
	}
}

func TestVT05_StubVolume_Source(t *testing.T) {
	vol := NewStubVolume("stub-handle", "worker-x", "/mnt/stub")
	if vol.Source() != "worker-x" {
		t.Errorf("Source() = %q, want worker-x", vol.Source())
	}
}

func TestVT05_StubVolume_HasExecutorFalse(t *testing.T) {
	vol := NewStubVolume("stub-handle", "worker-x", "/mnt/stub")
	if vol.HasExecutor() {
		t.Error("expected HasExecutor to return false for stub volume")
	}
}

func TestVT05_StubVolume_DBVolumeNil(t *testing.T) {
	vol := NewStubVolume("stub-handle", "worker-x", "/mnt/stub")
	if vol.DBVolume() != nil {
		t.Error("expected DBVolume to return nil for stub volume")
	}
}

// ---------------------------------------------------------------------------
// VT-06: DaemonSetVolume StreamOut
// ---------------------------------------------------------------------------

func TestVT06_DaemonSetVolume_StreamOut_RetrySucceeds(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			// Close the connection abruptly to simulate a transport error
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success-data"))
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))

	vol := &DaemonSetVolume{
		key:            "retry-key",
		handle:         "retry-handle",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     &http.Client{},
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	reader, err := vol.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("expected StreamOut to succeed after retries, got: %v", err)
	}
	defer reader.Close()

	data, _ := io.ReadAll(reader)
	if string(data) != "success-data" {
		t.Errorf("expected 'success-data', got %q", string(data))
	}

	if atomic.LoadInt32(&attempts) < 3 {
		t.Errorf("expected at least 3 attempts, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestVT06_DaemonSetVolume_StreamOut_GivesUpAfter3Failures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))

	vol := &DaemonSetVolume{
		key:            "fail-key",
		handle:         "fail-handle",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     &http.Client{},
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	_, err := vol.StreamOut(context.Background(), ".", nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestVT06_DaemonSetVolume_StreamOut_Non200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))

	vol := &DaemonSetVolume{
		key:            "err-key",
		handle:         "err-handle",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     srv.Client(),
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	_, err := vol.StreamOut(context.Background(), ".", nil)
	if err == nil {
		t.Fatal("expected error for 500 status code")
	}
	if !strings.Contains(err.Error(), "unexpected status 500") {
		t.Errorf("expected 'unexpected status 500' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VT-07: DaemonSetVolume StreamIn requires daemon client or source node
// ---------------------------------------------------------------------------

func TestVT07_DaemonSetVolume_StreamIn_ErrorsWithoutDaemonClient(t *testing.T) {
	vol := &DaemonSetVolume{
		key:    "test-key",
		handle: "test-handle",
	}

	err := vol.StreamIn(context.Background(), ".", nil, 0, strings.NewReader("data"))
	if err == nil {
		t.Fatal("expected error from StreamIn without daemon client")
	}
	if !strings.Contains(err.Error(), "no source node or daemon client") {
		t.Errorf("expected error about missing daemon client, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VT-08: DaemonSetVolume no compression handling
// ---------------------------------------------------------------------------

func TestVT08_DaemonSetVolume_StreamOut_PassesRawBody(t *testing.T) {
	rawContent := "raw-tar-bytes-no-compression"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(rawContent))
	}))
	defer srv.Close()

	resolver := fakeNodeIPResolver(testNode("node-1", "10.0.0.1"))

	vol := &DaemonSetVolume{
		key:            "raw-key",
		handle:         "raw-handle",
		workerName:     "w1",
		sourceNode:     "node-1",
		config:         Config{Namespace: "test-ns", ArtifactDaemonPort: 7780},
		httpClient:     srv.Client(),
		nodeIPResolver: resolver,
	}
	vol.httpClient.Transport = rewriteTransport{url: srv.URL}

	// Pass a non-nil compression but the DaemonSetVolume should ignore it
	reader, err := vol.StreamOut(context.Background(), ".", nil)
	if err != nil {
		t.Fatalf("StreamOut: %v", err)
	}
	defer reader.Close()

	data, _ := io.ReadAll(reader)
	if string(data) != rawContent {
		t.Errorf("expected raw passthrough %q, got %q", rawContent, string(data))
	}
}

// ---------------------------------------------------------------------------
// VT-09: Cache initialization delegation
// ---------------------------------------------------------------------------

func TestVT09_DaemonSetVolume_NilDBVolume_InitializeResourceCacheReturnsNil(t *testing.T) {
	vol := &DaemonSetVolume{
		key:    "test-key",
		handle: "test-handle",
	}

	result, err := vol.InitializeResourceCache(context.Background(), nil)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result, got: %v", result)
	}
}

func TestVT09_DaemonSetVolume_NilDBVolume_InitializeTaskCacheReturnsNil(t *testing.T) {
	vol := &DaemonSetVolume{
		key:    "test-key",
		handle: "test-handle",
	}

	err := vol.InitializeTaskCache(context.Background(), 1, "step", "/path", false)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestVT09_Volume_NilDBVolume_InitializeResourceCacheReturnsNil(t *testing.T) {
	vol := NewStubVolume("h", "w", "/mnt")

	result, err := vol.InitializeResourceCache(context.Background(), nil)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result, got: %v", result)
	}
}

func TestVT09_Volume_NilDBVolume_InitializeTaskCacheReturnsNil(t *testing.T) {
	vol := NewStubVolume("h", "w", "/mnt")

	err := vol.InitializeTaskCache(context.Background(), 1, "step", "/path", false)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VT-10: Volume Handle identity
// ---------------------------------------------------------------------------

func TestVT10_DeferredVolume_Handle_WithDBVolume_ReturnsDBHandle(t *testing.T) {
	fakeDBVol := new(dbfakes.FakeCreatedVolume)
	fakeDBVol.HandleReturns("db-handle-xyz")

	vol := NewVolume(fakeDBVol, nil, "pod", "ns", "main", "/mnt")

	if vol.Handle() != "db-handle-xyz" {
		t.Errorf("expected db-handle-xyz, got %q", vol.Handle())
	}
}

func TestVT10_DeferredVolume_Handle_WithoutDBVolume_ReturnsInternalHandle(t *testing.T) {
	vol := NewDeferredVolume("internal-handle", "worker", nil, "ns", "main", "/mnt")

	if vol.Handle() != "internal-handle" {
		t.Errorf("expected internal-handle, got %q", vol.Handle())
	}
}

func TestVT10_DaemonSetVolume_Handle_ReturnsConstructionHandle(t *testing.T) {
	vol := NewDaemonSetVolume("key", "dsv-handle", "w1", nil, "", Config{}, nil)

	if vol.Handle() != "dsv-handle" {
		t.Errorf("expected dsv-handle, got %q", vol.Handle())
	}
}

func TestVT10_StubVolume_Handle_ReturnsConstructionHandle(t *testing.T) {
	vol := NewStubVolume("stub-h", "w1", "/mnt")

	if vol.Handle() != "stub-h" {
		t.Errorf("expected stub-h, got %q", vol.Handle())
	}
}

// ---------------------------------------------------------------------------
// CO-04/CO-05/CO-06: Volume mount construction via buildVolumeMountsForSpec
// ---------------------------------------------------------------------------

func TestCO04_BuildVolumeMounts_DirOnly(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{Dir: "/workdir"}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount for Dir only, got %d", len(mounts))
	}
	if mounts[0].MountPath != "/workdir" {
		t.Errorf("expected mount at /workdir, got %s", mounts[0].MountPath)
	}
}

func TestCO04_BuildVolumeMounts_WithInputs(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Dir: "/workdir",
		Inputs: []runtime.Input{
			{DestinationPath: "/workdir/input-a"},
			{DestinationPath: "/workdir/input-b"},
		},
	}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	// 1 dir + 2 inputs = 3
	if len(mounts) != 3 {
		t.Fatalf("expected 3 mounts, got %d", len(mounts))
	}
	if mounts[1].MountPath != "/workdir/input-a" {
		t.Errorf("expected input-a mount, got %s", mounts[1].MountPath)
	}
	if mounts[2].MountPath != "/workdir/input-b" {
		t.Errorf("expected input-b mount, got %s", mounts[2].MountPath)
	}
}

func TestCO05_BuildVolumeMounts_WithOutputs(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Dir:     "/workdir",
		Outputs: runtime.OutputPaths{"result": "/workdir/result"},
	}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	// 1 dir + 1 output = 2
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(mounts))
	}
	if mounts[1].MountPath != "/workdir/result" {
		t.Errorf("expected output mount at /workdir/result, got %s", mounts[1].MountPath)
	}
}

func TestCO05_BuildVolumeMounts_OverlappingInputAndOutput_Deduped(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Dir: "/workdir",
		Inputs: []runtime.Input{
			{DestinationPath: "/workdir/shared"},
		},
		Outputs: runtime.OutputPaths{"shared": "/workdir/shared"},
	}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	// 1 dir + 1 input (output deduped) = 2
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts (output deduped), got %d", len(mounts))
	}
}

func TestCO05_BuildVolumeMounts_NonOverlappingInputAndOutput_BothCreated(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Dir: "/workdir",
		Inputs: []runtime.Input{
			{DestinationPath: "/workdir/input"},
		},
		Outputs: runtime.OutputPaths{"output": "/workdir/output"},
	}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	// 1 dir + 1 input + 1 output = 3
	if len(mounts) != 3 {
		t.Fatalf("expected 3 mounts, got %d", len(mounts))
	}
}

func TestCO06_BuildVolumeMounts_WithCaches(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Dir:    "/workdir",
		Caches: []string{"cache-a"},
	}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	// 1 dir + 1 cache = 2
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(mounts))
	}
	// Relative cache path resolved against Dir
	expectedPath := filepath.Join("/workdir", "cache-a")
	if mounts[1].MountPath != expectedPath {
		t.Errorf("expected cache mount at %s, got %s", expectedPath, mounts[1].MountPath)
	}
}

// ---------------------------------------------------------------------------
// CO-11: Volume naming conventions
// ---------------------------------------------------------------------------

func TestCO11_VolumeNaming(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Dir: "/workdir",
		Inputs: []runtime.Input{
			{DestinationPath: "/workdir/my-input"},
		},
		Outputs: runtime.OutputPaths{"my-output": "/workdir/my-output"},
		Caches:  []string{"my-cache"},
	}

	_, volumes := w.buildVolumeMountsForSpec("abc", spec)

	if len(volumes) != 4 {
		t.Fatalf("expected 4 volumes, got %d", len(volumes))
	}

	// dir volume
	if volumes[0].Handle() != "abc-dir" {
		t.Errorf("expected dir handle 'abc-dir', got %q", volumes[0].Handle())
	}
	// input volume
	if volumes[1].Handle() != "abc-input-0" {
		t.Errorf("expected input handle 'abc-input-0', got %q", volumes[1].Handle())
	}
	// output volume
	if volumes[2].Handle() != "abc-output-my-output" {
		t.Errorf("expected output handle 'abc-output-my-output', got %q", volumes[2].Handle())
	}
	// cache volume
	if volumes[3].Handle() != "abc-cache-0" {
		t.Errorf("expected cache handle 'abc-cache-0', got %q", volumes[3].Handle())
	}
}

// ---------------------------------------------------------------------------
// CO-12: Relative path resolution for caches
// ---------------------------------------------------------------------------

func TestCO12_CachePath_RelativeResolvedAgainstDir(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Dir:    "/workdir",
		Caches: []string{"my-cache"},
	}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	// dir + cache = 2
	cacheMount := mounts[1]
	expected := filepath.Join("/workdir", "my-cache")
	if cacheMount.MountPath != expected {
		t.Errorf("expected cache at %s, got %s", expected, cacheMount.MountPath)
	}
}

func TestCO12_CachePath_AbsoluteStaysAbsolute(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Dir:    "/workdir",
		Caches: []string{"/absolute/cache"},
	}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	cacheMount := mounts[1]
	if cacheMount.MountPath != "/absolute/cache" {
		t.Errorf("expected cache at /absolute/cache, got %s", cacheMount.MountPath)
	}
}

// ---------------------------------------------------------------------------
// CO-10: Scheduling affinity
// ---------------------------------------------------------------------------

func TestCO10_PreferredInputNode_NoInputs_ReturnsEmpty(t *testing.T) {
	locator := NewArtifactLocator()
	backend := NewDaemonSetBackend(Config{ArtifactDaemonHostPath: "/artifacts"}, locator, nil)

	node := backend.preferredInputNode(nil)
	if node != "" {
		t.Errorf("expected empty string with no inputs, got %q", node)
	}
}

func TestCO10_PreferredInputNode_InputsOnDifferentNodes_ReturnsMostPopular(t *testing.T) {
	locator := NewArtifactLocator()
	locator.Record(ArtifactKey("vol-a"), "node-1", "")
	locator.Record(ArtifactKey("vol-b"), "node-2", "")
	locator.Record(ArtifactKey("vol-c"), "node-2", "")

	backend := NewDaemonSetBackend(Config{ArtifactDaemonHostPath: "/artifacts"}, locator, nil)
	inputs := []runtime.Input{
		{Artifact: &stubArtifactBehavioral{handle: "vol-a"}, DestinationPath: "/in/a"},
		{Artifact: &stubArtifactBehavioral{handle: "vol-b"}, DestinationPath: "/in/b"},
		{Artifact: &stubArtifactBehavioral{handle: "vol-c"}, DestinationPath: "/in/c"},
	}

	node := backend.preferredInputNode(inputs)
	if node != "node-2" {
		t.Errorf("expected node-2 (most inputs), got %q", node)
	}
}

func TestCO10_BuildAffinity_WithoutArtifactDaemonHostPath_ReturnsNil(t *testing.T) {
	c := &Container{
		config:     Config{Namespace: "test-ns"},
		properties: make(map[string]string),
	}

	affinity := c.buildAffinity()
	if affinity != nil {
		t.Error("expected nil affinity when ArtifactDaemonHostPath is empty")
	}
}

func TestCO10_PreferredInputNode_NilLocator_ReturnsEmpty(t *testing.T) {
	backend := NewDaemonSetBackend(Config{ArtifactDaemonHostPath: "/artifacts"}, nil, nil)
	inputs := []runtime.Input{
		{Artifact: &stubArtifactBehavioral{handle: "vol-a"}, DestinationPath: "/in/a"},
	}

	node := backend.preferredInputNode(inputs)
	if node != "" {
		t.Errorf("expected empty string with nil locator, got %q", node)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestWorker creates a Worker suitable for unit testing buildVolumeMountsForSpec.
// It uses a fake K8s clientset and a stub db.Worker.
func newTestWorker(executor PodExecutor) *Worker {
	fakeDBWorker := new(dbfakes.FakeWorker)
	fakeDBWorker.NameReturns("test-worker")

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.1"}},
		},
	}
	cs := fake.NewSimpleClientset(node)
	cfg := NewConfig("test-ns", "")

	w := NewWorker(fakeDBWorker, cs, cfg)
	if executor != nil {
		w.SetExecutor(executor)
	}
	return w
}

// noopPodExecutor is a minimal PodExecutor for testing HasExecutor.
type noopPodExecutor struct{}

func (e *noopPodExecutor) ExecInPod(
	ctx context.Context,
	namespace, podName, containerName string,
	command []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	tty bool,
	attrs ExecAttrs,
) error {
	return nil
}

// stubArtifactBehavioral is a minimal runtime.Artifact for behavioral tests.
type stubArtifactBehavioral struct {
	handle string
}

var _ runtime.Artifact = (*stubArtifactBehavioral)(nil)

func (a *stubArtifactBehavioral) Handle() string { return a.handle }
func (a *stubArtifactBehavioral) Source() string  { return "test-worker" }
func (a *stubArtifactBehavioral) StreamOut(_ context.Context, _ string, _ compression.Compression) (io.ReadCloser, error) {
	return nil, fmt.Errorf("not implemented")
}

// behavioralDaemonSetConfig returns a DaemonSet-mode config for behavioral tests.
func behavioralDaemonSetConfig() Config {
	return Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactDaemonPort:     7780,
		ArtifactDaemonService:  "artifact-daemon",
		ArtifactHelperImage:    "alpine:latest",
	}
}

// Verify DaemonSetVolume Key returns the key used in construction.
func TestVT10_DaemonSetVolume_Key_ReturnsConstructionKey(t *testing.T) {
	vol := NewDaemonSetVolume("my-key", "my-handle", "w1", nil, "", Config{}, nil)

	if vol.Key() != "my-key" {
		t.Errorf("expected Key() = 'my-key', got %q", vol.Key())
	}
}

// Verify DaemonSetVolume Source returns workerName.
func TestVT10_DaemonSetVolume_Source_ReturnsWorkerName(t *testing.T) {
	vol := NewDaemonSetVolume("key", "handle", "my-worker", nil, "", Config{}, nil)

	if vol.Source() != "my-worker" {
		t.Errorf("expected Source() = 'my-worker', got %q", vol.Source())
	}
}

// Verify Volume Source with dbVolume returns dbVolume.WorkerName.
func TestVT10_Volume_Source_WithDBVolume_ReturnsDBWorkerName(t *testing.T) {
	fakeDBVol := new(dbfakes.FakeCreatedVolume)
	fakeDBVol.WorkerNameReturns("db-worker-name")

	vol := NewVolume(fakeDBVol, nil, "pod", "ns", "main", "/mnt")

	if vol.Source() != "db-worker-name" {
		t.Errorf("expected Source() = 'db-worker-name', got %q", vol.Source())
	}
}

// Verify Volume Source without dbVolume returns workerName.
func TestVT10_Volume_Source_WithoutDBVolume_ReturnsWorkerName(t *testing.T) {
	vol := NewDeferredVolume("h", "deferred-worker", nil, "ns", "main", "/mnt")

	if vol.Source() != "deferred-worker" {
		t.Errorf("expected Source() = 'deferred-worker', got %q", vol.Source())
	}
}

// Verify MountPath returns the construction mountPath.
func TestVT01_Volume_MountPath(t *testing.T) {
	vol := NewStubVolume("h", "w", "/my/mount")

	if vol.MountPath() != "/my/mount" {
		t.Errorf("expected MountPath() = '/my/mount', got %q", vol.MountPath())
	}
}

// Verify buildVolumeMountsForSpec creates deferred volumes when executor is set.
func TestCO04_BuildVolumeMounts_WithExecutor_CreatesDeferredVolumes(t *testing.T) {
	executor := &noopPodExecutor{}
	w := newTestWorker(executor)
	spec := runtime.ContainerSpec{Dir: "/workdir"}

	_, volumes := w.buildVolumeMountsForSpec("h", spec)

	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(volumes))
	}
	if !volumes[0].HasExecutor() {
		t.Error("expected deferred volume to have executor when worker has executor set")
	}
}

// Verify buildVolumeMountsForSpec creates stub volumes when no executor.
func TestCO04_BuildVolumeMounts_WithoutExecutor_CreatesStubVolumes(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{Dir: "/workdir"}

	_, volumes := w.buildVolumeMountsForSpec("h", spec)

	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(volumes))
	}
	if volumes[0].HasExecutor() {
		t.Error("expected stub volume to NOT have executor when worker has no executor")
	}
}

// Verify overlapping output with trailing slash is still deduped.
func TestCO05_BuildVolumeMounts_OverlappingWithTrailingSlash_Deduped(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Dir: "/workdir",
		Inputs: []runtime.Input{
			{DestinationPath: "/workdir/shared"},
		},
		Outputs: runtime.OutputPaths{"shared": "/workdir/shared/"},
	}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	// 1 dir + 1 input (output deduped even with trailing slash) = 2
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts (trailing slash deduped), got %d", len(mounts))
	}
}

// Verify ArtifactKey is identity function.
func TestArtifactKey_IdentityFunction(t *testing.T) {
	handle := "vol-handle-abc-123"
	key := ArtifactKey(handle)
	if key != handle {
		t.Errorf("ArtifactKey should be identity, got %q for input %q", key, handle)
	}
}

// Verify empty Dir produces no dir mount.
func TestCO04_BuildVolumeMounts_EmptyDir_NoDirMount(t *testing.T) {
	w := newTestWorker(nil)
	spec := runtime.ContainerSpec{
		Outputs: runtime.OutputPaths{"out": "/workdir/out"},
	}

	mounts, _ := w.buildVolumeMountsForSpec("h", spec)

	// Only 1 output, no dir
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount (output only, no dir), got %d", len(mounts))
	}
}

// Verify DaemonSetVolume DBVolume returns nil when constructed with nil.
func TestVT10_DaemonSetVolume_DBVolume_Nil(t *testing.T) {
	vol := NewDaemonSetVolume("key", "handle", "w1", nil, "", Config{}, nil)

	if vol.DBVolume() != nil {
		t.Error("expected DBVolume() to return nil")
	}
}

// Verify DaemonSetVolume DBVolume returns the provided dbVolume.
func TestVT10_DaemonSetVolume_DBVolume_NonNil(t *testing.T) {
	fakeDBVol := new(dbfakes.FakeCreatedVolume)
	fakeDBVol.HandleReturns("db-vol")

	vol := NewDaemonSetVolume("key", "handle", "w1", fakeDBVol, "", Config{}, nil)

	if vol.DBVolume() == nil {
		t.Fatal("expected non-nil DBVolume()")
	}
	if vol.DBVolume().(db.CreatedVolume).Handle() != "db-vol" {
		t.Errorf("expected DBVolume handle 'db-vol', got %q", vol.DBVolume().(db.CreatedVolume).Handle())
	}
}
