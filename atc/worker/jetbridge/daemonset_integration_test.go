package jetbridge

import (
	"context"
	"io"
	"testing"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
)

// TestDaemonSetMode_PodHasHostPathVolume verifies that in DaemonSet mode,
// the pod spec includes a hostPath volume instead of a PVC volume.
func TestDaemonSetMode_PodHasHostPathVolume(t *testing.T) {
	cfg := Config{
		Namespace:              "test-ns",
		ArtifactBackend:        ArtifactBackendDaemonSet,
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactDaemonPort:     8080,
		ArtifactDaemonService:  "artifact-daemon",
		ArtifactHelperImage:    "alpine:latest",
	}

	c := &Container{
		handle:        "test-handle",
		podName:       "test-pod",
		metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{Dir: "/tmp/build", Type: db.ContainerTypeTask},
		config:        cfg,
		properties:    make(map[string]string),
	}

	vol := c.buildArtifactStoreVolume()
	if vol == nil {
		t.Fatal("expected artifact store volume, got nil")
	}
	if vol.Name != artifactDaemonHostPathVolumeName {
		t.Errorf("expected volume name %q, got %q", artifactDaemonHostPathVolumeName, vol.Name)
	}
	if vol.HostPath == nil {
		t.Fatal("expected HostPath volume source")
	}
	if vol.HostPath.Path != "/var/concourse/artifacts" {
		t.Errorf("expected hostPath /var/concourse/artifacts, got %s", vol.HostPath.Path)
	}
}

// TestDaemonSetMode_HardAffinity verifies the required node affinity.
func TestDaemonSetMode_HardAffinity(t *testing.T) {
	cfg := Config{
		Namespace:       "test-ns",
		ArtifactBackend: ArtifactBackendDaemonSet,
	}

	c := &Container{
		handle:        "test-handle",
		podName:       "test-pod",
		metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{Dir: "/tmp/build", Type: db.ContainerTypeTask},
		config:        cfg,
		properties:    make(map[string]string),
	}

	affinity := c.buildAffinity()
	if affinity == nil {
		t.Fatal("expected affinity, got nil")
	}
	if affinity.NodeAffinity == nil {
		t.Fatal("expected NodeAffinity")
	}
	required := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) == 0 {
		t.Fatal("expected required node selector terms")
	}
	expr := required.NodeSelectorTerms[0].MatchExpressions[0]
	if expr.Key != "concourse.dev/artifact-cache" {
		t.Errorf("expected label key concourse.dev/artifact-cache, got %s", expr.Key)
	}
	if expr.Operator != corev1.NodeSelectorOpIn {
		t.Errorf("expected In operator, got %v", expr.Operator)
	}
	if len(expr.Values) != 1 || expr.Values[0] != "ready" {
		t.Errorf("expected values [ready], got %v", expr.Values)
	}
}

// TestDaemonSetMode_SoftAffinity verifies soft scheduling toward input source node.
func TestDaemonSetMode_SoftAffinity(t *testing.T) {
	cfg := Config{
		Namespace:       "test-ns",
		ArtifactBackend: ArtifactBackendDaemonSet,
	}

	locator := NewArtifactLocator()
	locator.Record(ArtifactKey("input-vol-1"), "node-42")

	c := &Container{
		handle:   "test-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        &stubArtifact{handle: "input-vol-1"},
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:          cfg,
		properties:      make(map[string]string),
		artifactLocator: locator,
	}

	affinity := c.buildAffinity()
	if affinity == nil {
		t.Fatal("expected affinity, got nil")
	}

	preferred := affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(preferred) == 0 {
		t.Fatal("expected preferred scheduling terms")
	}
	if preferred[0].Weight != 100 {
		t.Errorf("expected weight 100, got %d", preferred[0].Weight)
	}
	expr := preferred[0].Preference.MatchExpressions[0]
	if expr.Key != "kubernetes.io/hostname" {
		t.Errorf("expected kubernetes.io/hostname, got %s", expr.Key)
	}
	if len(expr.Values) != 1 || expr.Values[0] != "node-42" {
		t.Errorf("expected values [node-42], got %v", expr.Values)
	}
}

// TestDaemonSetMode_NoAffinityForPVC verifies PVC mode returns nil affinity.
func TestDaemonSetMode_NoAffinityForPVC(t *testing.T) {
	cfg := Config{
		Namespace:          "test-ns",
		ArtifactStoreClaim: "artifacts-pvc",
	}

	c := &Container{
		handle:        "test-handle",
		podName:       "test-pod",
		metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{Dir: "/tmp/build", Type: db.ContainerTypeTask},
		config:        cfg,
		properties:    make(map[string]string),
	}

	affinity := c.buildAffinity()
	if affinity != nil {
		t.Error("expected nil affinity for PVC mode")
	}
}

// TestDaemonSetMode_InitContainerFetchCommand verifies the branching fetch command.
func TestDaemonSetMode_InitContainerFetchCommand(t *testing.T) {
	cfg := Config{
		Namespace:              "test-ns",
		ArtifactBackend:        ArtifactBackendDaemonSet,
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactDaemonPort:     8080,
		ArtifactDaemonService:  "artifact-daemon",
		ArtifactHelperImage:    "alpine:latest",
	}

	locator := NewArtifactLocator()
	locator.Record(ArtifactKey("vol-1"), "node-a")

	c := &Container{
		handle:   "test-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        &stubArtifact{handle: "vol-1"},
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:          cfg,
		properties:      make(map[string]string),
		artifactLocator: locator,
	}

	// Build mounts to get volumeName
	volumes, mounts := c.buildVolumeMounts()
	_ = volumes

	inits := c.buildArtifactInitContainers(mounts)
	if len(inits) == 0 {
		t.Fatal("expected at least one init container")
	}

	init := inits[0]
	// Check that it has MY_NODE_NAME env var (downward API)
	hasNodeName := false
	hasSourceNode := false
	for _, env := range init.Env {
		if env.Name == "MY_NODE_NAME" && env.ValueFrom != nil && env.ValueFrom.FieldRef != nil {
			hasNodeName = true
		}
		if env.Name == "SOURCE_NODE" && env.Value == "node-a" {
			hasSourceNode = true
		}
	}
	if !hasNodeName {
		t.Error("expected MY_NODE_NAME env from downward API")
	}
	if !hasSourceNode {
		t.Error("expected SOURCE_NODE=node-a env var")
	}
}

// TestArtifactLocator_RecordAndCleanup verifies the full lifecycle.
func TestDaemonSetMode_LocatorRecordLookupCleanup(t *testing.T) {
	locator := NewArtifactLocator()

	locator.Record("artifacts/build-1.tar", "node-a")
	locator.Record("artifacts/build-2.tar", "node-b")
	locator.Record("artifacts/build-3.tar", "node-a")

	// Verify lookup
	node, ok := locator.Locate("artifacts/build-1.tar")
	if !ok || node != "node-a" {
		t.Errorf("expected node-a, got %s", node)
	}

	// Simulate GC cleanup
	locator.Remove("artifacts/build-1.tar")
	_, ok = locator.Locate("artifacts/build-1.tar")
	if ok {
		t.Error("expected not found after Remove")
	}

	// Other entries unaffected
	node, ok = locator.Locate("artifacts/build-2.tar")
	if !ok || node != "node-b" {
		t.Errorf("expected node-b, got %s", node)
	}
}

// --- Gap #4: Sidecar must NOT be created in DaemonSet mode ---

func TestDaemonSetMode_NoSidecarCreated(t *testing.T) {
	cfg := Config{
		Namespace:              "test-ns",
		ArtifactBackend:        ArtifactBackendDaemonSet,
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactHelperImage:    "alpine:latest",
	}

	c := &Container{
		handle:        "test-handle",
		podName:       "test-pod",
		metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{Dir: "/tmp/build", Type: db.ContainerTypeTask},
		config:        cfg,
		properties:    make(map[string]string),
	}

	_, mounts := c.buildVolumeMounts()
	sidecar := c.buildArtifactHelperSidecar(mounts)
	if sidecar != nil {
		t.Error("expected no sidecar in DaemonSet mode, but one was created")
	}
}

// --- Gap #4 corollary: Sidecar IS created in PVC mode ---

func TestPVCMode_SidecarCreated(t *testing.T) {
	cfg := Config{
		Namespace:          "test-ns",
		ArtifactStoreClaim: "artifacts-pvc",
		ArtifactHelperImage: "alpine:latest",
	}

	c := &Container{
		handle:        "test-handle",
		podName:       "test-pod",
		metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{Dir: "/tmp/build", Type: db.ContainerTypeTask},
		config:        cfg,
		properties:    make(map[string]string),
	}

	_, mounts := c.buildVolumeMounts()
	sidecar := c.buildArtifactHelperSidecar(mounts)
	if sidecar == nil {
		t.Error("expected sidecar in PVC mode, but got nil")
	}
}

// --- Gap #8: Uploads must be skipped in DaemonSet mode ---

func TestDaemonSetMode_UploadOutputsIsNoop(t *testing.T) {
	cfg := Config{
		Namespace:              "test-ns",
		ArtifactBackend:        ArtifactBackendDaemonSet,
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
	}

	c := &Container{
		handle:        "test-handle",
		podName:       "test-pod",
		metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:     "/tmp/build",
			Type:    db.ContainerTypeTask,
			Outputs: runtime.OutputPaths{"out": "/tmp/build/out"},
		},
		config:     cfg,
		properties: make(map[string]string),
	}

	p := &execProcess{
		id:        "test",
		podName:   "test-pod",
		config:    cfg,
		container: c,
	}

	// uploadOutputsToArtifactStore should return nil (no-op) in DaemonSet mode.
	// If it tries to exec in a sidecar, it will panic since executor is nil.
	err := p.uploadOutputsToArtifactStore(context.Background())
	if err != nil {
		t.Errorf("expected no-op upload in DaemonSet mode, got error: %v", err)
	}
}

// --- Gap #2 & #3: Locator.Record must be called with node name after step ---

func TestDaemonSetMode_LocatorRecordCalledAfterUpload(t *testing.T) {
	locator := NewArtifactLocator()

	cfg := Config{
		Namespace:              "test-ns",
		ArtifactBackend:        ArtifactBackendDaemonSet,
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
	}

	vol := NewStubVolume("output-vol", "test-worker", "/tmp/build/out")

	c := &Container{
		handle:        "test-handle",
		podName:       "test-pod",
		metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:     "/tmp/build",
			Type:    db.ContainerTypeTask,
			Outputs: runtime.OutputPaths{"out": "/tmp/build/out"},
		},
		config:          cfg,
		properties:      make(map[string]string),
		volumes:         []*Volume{vol},
		artifactLocator: locator,
	}

	p := &execProcess{
		id:        "test",
		podName:   "test-pod",
		config:    cfg,
		container: c,
	}

	// recordOutputLocations should exist and record each output volume's
	// artifact key → node name in the locator.
	p.recordOutputLocations("test-node-1")

	// Verify the locator was populated for the output volume.
	key := ArtifactKey(vol.Handle())
	node, found := locator.Locate(key)
	if !found {
		t.Fatalf("expected locator to have key %s, but not found", key)
	}
	if node != "test-node-1" {
		t.Errorf("expected node test-node-1, got %s", node)
	}
}

// stubArtifact is a minimal runtime.Artifact for testing.
type stubArtifact struct {
	handle string
}

var _ runtime.Artifact = (*stubArtifact)(nil)

func (a *stubArtifact) Handle() string { return a.handle }
func (a *stubArtifact) Source() string { return "test-worker" }
func (a *stubArtifact) StreamOut(_ context.Context, _ string, _ compression.Compression) (io.ReadCloser, error) {
	return nil, nil
}
