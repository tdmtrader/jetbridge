package jetbridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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
	executor := volumeConstructionExecutor()
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

	err := vol.InitializeTaskCache(context.Background(), atc.TaskCacheIdentity{JobID: 1}, "step", "/path", false)
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

	err := vol.InitializeTaskCache(context.Background(), atc.TaskCacheIdentity{JobID: 1}, "step", "/path", false)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// VT-10: Volume Handle identity
// ---------------------------------------------------------------------------

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
		{Artifact: constructionArtifact("vol-a", "test-worker"), DestinationPath: "/in/a"},
		{Artifact: constructionArtifact("vol-b", "test-worker"), DestinationPath: "/in/b"},
		{Artifact: constructionArtifact("vol-c", "test-worker"), DestinationPath: "/in/c"},
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
		{Artifact: constructionArtifact("vol-a", "test-worker"), DestinationPath: "/in/a"},
	}

	node := backend.preferredInputNode(inputs)
	if node != "" {
		t.Errorf("expected empty string with nil locator, got %q", node)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// These tests inspect construction only: use the actual client/executor
// types without supplying any API response or pretending a command ran.
func volumeConstructionExecutor() *SPDYExecutor {
	config := &rest.Config{Host: "https://127.0.0.1:1"}
	return NewSPDYExecutor(kubernetes.NewForConfigOrDie(config), config)
}

// constructionArtifact uses the production volume and executor. Its callers
// inspect pod layout, affinity and fetch commands; they never stream data.
func constructionArtifact(handle, workerName string) *Volume {
	return NewDeferredVolume(handle, workerName, volumeConstructionExecutor(), "test-ns", mainContainerName, "/artifact")
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

// Verify ArtifactKey is identity function.
func TestArtifactKey_IdentityFunction(t *testing.T) {
	handle := "vol-handle-abc-123"
	key := ArtifactKey(handle)
	if key != handle {
		t.Errorf("ArtifactKey should be identity, got %q for input %q", key, handle)
	}
}

// Verify DaemonSetVolume DBVolume returns nil when constructed with nil.
func TestVT10_DaemonSetVolume_DBVolume_Nil(t *testing.T) {
	vol := NewDaemonSetVolume("key", "handle", "w1", nil, "", Config{}, nil)

	if vol.DBVolume() != nil {
		t.Error("expected DBVolume() to return nil")
	}
}
