package jetbridge

import (
	"context"
	"io"
	"path/filepath"
	"strings"
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
	locator.Record(ArtifactKey("input-vol-1"), "node-42", "")

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
	locator.Record(ArtifactKey("vol-1"), "node-a", "/var/concourse/artifacts/steps/test-handle/result")

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

	inits, err := c.buildArtifactInitContainers(mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
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

	locator.Record("artifacts/build-1.tar", "node-a", "")
	locator.Record("artifacts/build-2.tar", "node-b", "")
	locator.Record("artifacts/build-3.tar", "node-a", "")

	// Verify lookup
	node, ok := locator.LocateNode("artifacts/build-1.tar")
	if !ok || node != "node-a" {
		t.Errorf("expected node-a, got %s", node)
	}

	// Simulate GC cleanup
	locator.Remove("artifacts/build-1.tar")
	_, ok = locator.LocateNode("artifacts/build-1.tar")
	if ok {
		t.Error("expected not found after Remove")
	}

	// Other entries unaffected
	node, ok = locator.LocateNode("artifacts/build-2.tar")
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
	node, found := locator.LocateNode(key)
	if !found {
		t.Fatalf("expected locator to have key %s, but not found", key)
	}
	if node != "test-node-1" {
		t.Errorf("expected node test-node-1, got %s", node)
	}
}

// =======================================================================
// Phase 2: HostPath output and dir volumes
// =======================================================================

func daemonSetConfig() Config {
	return Config{
		Namespace:              "test-ns",
		ArtifactBackend:        ArtifactBackendDaemonSet,
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactDaemonPort:     8080,
		ArtifactDaemonService:  "artifact-daemon",
		ArtifactHelperImage:    "alpine:latest",
	}
}

func TestDaemonSetMode_OutputVolumesAreHostPath(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "build-42",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:     "/tmp/build",
			Type:    db.ContainerTypeTask,
			Outputs: runtime.OutputPaths{"result": "/tmp/build/result"},
		},
		config:     cfg,
		properties: make(map[string]string),
	}

	volumes, _ := c.buildVolumeMounts()

	// Find the output volume
	for _, vol := range volumes {
		if strings.HasPrefix(vol.Name, "output-") {
			if vol.HostPath == nil {
				t.Fatal("output volume should be hostPath in DaemonSet mode, got emptyDir")
			}
			expectedPath := filepath.Join(cfg.ArtifactDaemonHostPath, "steps", "build-42", "result")
			if vol.HostPath.Path != expectedPath {
				t.Errorf("expected hostPath %s, got %s", expectedPath, vol.HostPath.Path)
			}
			return
		}
	}
	t.Fatal("no output volume found")
}

func TestDaemonSetMode_DirVolumeIsHostPath(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "build-42",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
		},
		config:     cfg,
		properties: make(map[string]string),
	}

	volumes, _ := c.buildVolumeMounts()

	for _, vol := range volumes {
		if strings.HasPrefix(vol.Name, "dir-") {
			if vol.HostPath == nil {
				t.Fatal("dir volume should be hostPath in DaemonSet mode, got emptyDir")
			}
			expectedPath := filepath.Join(cfg.ArtifactDaemonHostPath, "steps", "build-42", "dir")
			if vol.HostPath.Path != expectedPath {
				t.Errorf("expected hostPath %s, got %s", expectedPath, vol.HostPath.Path)
			}
			return
		}
	}
	t.Fatal("no dir volume found")
}

func TestDaemonSetMode_InputVolumesAreHostPath(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "build-43",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        &stubArtifact{handle: "src-vol"},
					DestinationPath: "/tmp/build/src",
				},
			},
		},
		config:     cfg,
		properties: make(map[string]string),
	}

	volumes, _ := c.buildVolumeMounts()

	for _, vol := range volumes {
		if strings.HasPrefix(vol.Name, "input-") {
			if vol.HostPath == nil {
				t.Fatal("input volume should be hostPath in DaemonSet mode, got emptyDir")
			}
			expectedPath := filepath.Join(cfg.ArtifactDaemonHostPath, "steps", "build-43", "input-1")
			if vol.HostPath.Path != expectedPath {
				t.Errorf("expected hostPath %s, got %s", expectedPath, vol.HostPath.Path)
			}
			return
		}
	}
	t.Fatal("no input volume found")
}

func TestPVCMode_VolumesStillEmptyDir(t *testing.T) {
	cfg := Config{
		Namespace:          "test-ns",
		ArtifactStoreClaim: "artifacts-pvc",
		ArtifactHelperImage: "alpine:latest",
	}
	c := &Container{
		handle:   "build-99",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:     "/tmp/build",
			Type:    db.ContainerTypeTask,
			Outputs: runtime.OutputPaths{"out": "/tmp/build/out"},
		},
		config:     cfg,
		properties: make(map[string]string),
	}

	volumes, _ := c.buildVolumeMounts()

	for _, vol := range volumes {
		if vol.HostPath != nil {
			t.Errorf("PVC mode should not use hostPath volumes, but %s does", vol.Name)
		}
	}
}

// =======================================================================
// Phase 4: Direct cache hostPath mounts
// =======================================================================

func TestDaemonSetMode_CachesAreDirectHostPath(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "build-50",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask, JobID: 7, StepName: "build"},
		containerSpec: runtime.ContainerSpec{
			Dir:    "/tmp/build",
			Type:   db.ContainerTypeTask,
			Caches: []string{"/tmp/build/.cache"},
		},
		config:     cfg,
		properties: make(map[string]string),
	}

	volumes, _ := c.buildVolumeMounts()

	foundCache := false
	for _, vol := range volumes {
		if strings.HasPrefix(vol.Name, "cache-") {
			foundCache = true
			if vol.HostPath == nil {
				t.Fatal("cache volume should be hostPath in DaemonSet mode")
			}
			if !strings.HasPrefix(vol.HostPath.Path, filepath.Join(cfg.ArtifactDaemonHostPath, "caches")) {
				t.Errorf("cache hostPath should be under <hostPath>/caches/, got %s", vol.HostPath.Path)
			}
		}
	}
	if !foundCache {
		t.Fatal("no cache volume found")
	}

	// No cacheEntries should be created (no tar save/restore needed)
	if len(c.cacheEntries) != 0 {
		t.Errorf("expected 0 cacheEntries in DaemonSet mode, got %d", len(c.cacheEntries))
	}
}

// =======================================================================
// Phase 3: cp -a init containers
// =======================================================================

func TestDaemonSetMode_InitContainerUsesCpA(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator()
	locator.Record(ArtifactKey("src-vol"), "this-node", "/var/concourse/artifacts/steps/source-handle/src")

	c := &Container{
		handle:   "build-60",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        &stubArtifact{handle: "src-vol"},
					DestinationPath: "/tmp/build/src",
				},
			},
		},
		config:          cfg,
		properties:      make(map[string]string),
		artifactLocator: locator,
	}

	_, mounts := c.buildVolumeMounts()
	inits, err := c.buildArtifactInitContainers(mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}

	if len(inits) == 0 {
		t.Fatal("expected at least one init container")
	}

	cmd := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmd, "cp -a") {
		t.Errorf("local DaemonSet init container should use 'cp -a', got command: %s", cmd)
	}
}

// --- Phase: Fail-fast tests ---

// TestDaemonSetMode_MissingArtifactLocationReturnsError verifies that
// buildArtifactInitContainers returns an error when the artifact locator
// does not have the key for a DaemonSet input.
func TestDaemonSetMode_MissingArtifactLocationReturnsError(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator() // empty — nothing recorded

	c := &Container{
		handle:   "build-99",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        &stubArtifact{handle: "missing-vol"},
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:          cfg,
		properties:      make(map[string]string),
		artifactLocator: locator,
	}

	_, mounts := c.buildVolumeMounts()
	_, err := c.buildArtifactInitContainers(mounts)
	if err == nil {
		t.Fatal("expected error for missing artifact location, got nil")
	}
	if !strings.Contains(err.Error(), "artifact location unknown") {
		t.Errorf("expected error about unknown artifact location, got: %s", err)
	}
}

// TestDaemonSetMode_EmptySourceHostDirFailsFast verifies that
// daemonSetFetchCommand generates an exit-1 script when sourceHostDir is empty.
func TestDaemonSetMode_EmptySourceHostDirFailsFast(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{config: cfg}

	cmd := c.daemonSetFetchCommand("", "/tmp/build/input")
	script := strings.Join(cmd, " ")
	if !strings.Contains(script, "exit 1") {
		t.Errorf("expected exit 1 for empty sourceHostDir, got: %s", script)
	}
	if strings.Contains(script, "cp -a") {
		t.Errorf("empty sourceHostDir should NOT generate cp -a command, got: %s", script)
	}
}

// TestDaemonSetMode_RecordAndLocateRoundTrip verifies that recording
// an artifact location and looking it up produces correct init containers.
func TestDaemonSetMode_RecordAndLocateRoundTrip(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator()

	// Simulate the producing step recording its output location.
	artifactHandle := "producer-handle-output-result"
	locator.Record(ArtifactKey(artifactHandle), "node-a", "/var/concourse/artifacts/steps/producer-handle/result")

	c := &Container{
		handle:   "consumer-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        &stubArtifact{handle: artifactHandle},
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:          cfg,
		properties:      make(map[string]string),
		artifactLocator: locator,
	}

	_, mounts := c.buildVolumeMounts()
	inits, err := c.buildArtifactInitContainers(mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected at least one init container")
	}

	cmd := strings.Join(inits[0].Command, " ")
	// Should contain cp -a with the correct source hostDir
	if !strings.Contains(cmd, "/var/concourse/artifacts/steps/producer-handle/result") {
		t.Errorf("expected source hostDir in command, got: %s", cmd)
	}
	if !strings.Contains(cmd, "cp -a") {
		t.Errorf("expected cp -a in command, got: %s", cmd)
	}

	// Should have SOURCE_NODE env var
	hasSourceNode := false
	for _, env := range inits[0].Env {
		if env.Name == "SOURCE_NODE" && env.Value == "node-a" {
			hasSourceNode = true
		}
	}
	if !hasSourceNode {
		t.Error("expected SOURCE_NODE=node-a env var")
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
