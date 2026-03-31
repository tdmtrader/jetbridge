package jetbridge

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// permStubArtifact is a minimal runtime.Artifact for permutation tests.
type permStubArtifact struct{ handle string }

var _ runtime.Artifact = (*permStubArtifact)(nil)

func (a *permStubArtifact) Handle() string { return a.handle }
func (a *permStubArtifact) Source() string { return "worker-1" }
func (a *permStubArtifact) StreamOut(_ context.Context, _ string, _ compression.Compression) (io.ReadCloser, error) {
	return nil, fmt.Errorf("not implemented")
}

func permDaemonSetConfig() Config {
	return Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/artifact-store",
		ArtifactDaemonPort:     7780,
		ArtifactDaemonService:  "artifact-daemon",
		ArtifactHelperImage:    "alpine:latest",
	}
}

func permEmptyDirConfig() Config {
	return Config{
		Namespace: "test-ns",
	}
}

func makeContainer(handle string, metadata db.ContainerMetadata, spec runtime.ContainerSpec, cfg Config, locator *ArtifactLocator, reused bool) *Container {
	var backend StorageBackend
	if cfg.ArtifactDaemonHostPath != "" {
		backend = NewDaemonSetBackend(cfg, locator, nil)
	}
	return newContainer(
		handle,
		metadata,
		spec,
		nil, // dbContainer
		fake.NewSimpleClientset(),
		cfg,
		"worker-1",
		nil, // executor
		nil, // volumes
		backend,
		reused,
	)
}

func taskMetadata() db.ContainerMetadata {
	return db.ContainerMetadata{
		Type:     db.ContainerTypeTask,
		StepName: "my-step",
		JobID:    42,
		BuildID:  100,
	}
}

// assertVolumeCount is a test helper for volume count assertions.
func assertVolumeCount(t *testing.T, volumes []corev1.Volume, expected int) {
	t.Helper()
	if len(volumes) != expected {
		t.Fatalf("expected %d volumes, got %d", expected, len(volumes))
	}
}

// assertMountCount is a test helper for mount count assertions.
func assertMountCount(t *testing.T, mounts []corev1.VolumeMount, expected int) {
	t.Helper()
	if len(mounts) != expected {
		t.Fatalf("expected %d mounts, got %d", expected, len(mounts))
	}
}

// findMountByPath returns the VolumeMount at the given path, or fails.
func findMountByPath(t *testing.T, mounts []corev1.VolumeMount, path string) corev1.VolumeMount {
	t.Helper()
	for _, m := range mounts {
		if m.MountPath == path {
			return m
		}
	}
	t.Fatalf("no mount found at path %q", path)
	return corev1.VolumeMount{}
}

// findVolumeByName returns the Volume with the given name, or fails.
func findVolumeByName(t *testing.T, volumes []corev1.Volume, name string) corev1.Volume {
	t.Helper()
	for _, v := range volumes {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no volume found with name %q", name)
	return corev1.Volume{}
}

// assertHostPath checks that a volume is hostPath with the expected path prefix.
func assertHostPath(t *testing.T, vol corev1.Volume, expectedSuffix string) {
	t.Helper()
	if vol.HostPath == nil {
		t.Errorf("volume %q: expected hostPath, got emptyDir or nil", vol.Name)
		return
	}
	if !contains(vol.HostPath.Path, expectedSuffix) {
		t.Errorf("volume %q: hostPath %q does not contain expected suffix %q", vol.Name, vol.HostPath.Path, expectedSuffix)
	}
}

// assertEmptyDir checks that a volume is emptyDir.
func assertEmptyDir(t *testing.T, vol corev1.Volume) {
	t.Helper()
	if vol.EmptyDir == nil {
		t.Errorf("volume %q: expected emptyDir, got something else", vol.Name)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Test 1: Multiple inputs, no outputs
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_MultipleInputsNoOutputs(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/input-a"},
			{DestinationPath: "/tmp/input-b"},
			{DestinationPath: "/tmp/input-c"},
		},
	}

	c := makeContainer("handle-1", taskMetadata(), spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	// 1 dir + 3 inputs = 4
	assertVolumeCount(t, volumes, 4)
	assertMountCount(t, mounts, 4)

	// Dir mount
	findMountByPath(t, mounts, "/tmp/build")

	// Input mounts
	findMountByPath(t, mounts, "/tmp/input-a")
	findMountByPath(t, mounts, "/tmp/input-b")
	findMountByPath(t, mounts, "/tmp/input-c")

	// In hostPath mode, inputs use input-N as subdirs (no overlapping outputs).
	for _, vol := range volumes[1:4] {
		assertHostPath(t, vol, "steps/handle-1/")
	}
}

// ---------------------------------------------------------------------------
// Test 2: No inputs, multiple outputs
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_NoInputsMultipleOutputs(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Outputs: runtime.OutputPaths{
			"result": "/tmp/result",
			"logs":   "/tmp/logs",
			"report": "/tmp/report",
		},
	}

	c := makeContainer("handle-2", taskMetadata(), spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	// 1 dir + 3 outputs = 4
	assertVolumeCount(t, volumes, 4)
	assertMountCount(t, mounts, 4)

	// Dir
	findMountByPath(t, mounts, "/tmp/build")

	// Outputs
	findMountByPath(t, mounts, "/tmp/result")
	findMountByPath(t, mounts, "/tmp/logs")
	findMountByPath(t, mounts, "/tmp/report")

	// Verify output subdirs use output names and are deterministically sorted.
	// Sorted order: logs, report, result
	logsMount := findMountByPath(t, mounts, "/tmp/logs")
	logsVol := findVolumeByName(t, volumes, logsMount.Name)
	assertHostPath(t, logsVol, "steps/handle-2/logs")

	reportMount := findMountByPath(t, mounts, "/tmp/report")
	reportVol := findVolumeByName(t, volumes, reportMount.Name)
	assertHostPath(t, reportVol, "steps/handle-2/report")

	resultMount := findMountByPath(t, mounts, "/tmp/result")
	resultVol := findVolumeByName(t, volumes, resultMount.Name)
	assertHostPath(t, resultVol, "steps/handle-2/result")
}

// ---------------------------------------------------------------------------
// Test 3: Multiple inputs, multiple outputs, mixed overlap
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_MixedOverlap(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/code"},
			{DestinationPath: "/tmp/shared"},
			{DestinationPath: "/tmp/data"},
		},
		Outputs: runtime.OutputPaths{
			"modified-code": "/tmp/code",   // overlaps input
			"result":        "/tmp/result",  // non-overlapping
			"shared-out":    "/tmp/shared",  // overlaps input
		},
	}

	c := makeContainer("handle-3", taskMetadata(), spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	// 1 dir + 3 inputs + 1 non-overlapping output = 5
	assertVolumeCount(t, volumes, 5)
	assertMountCount(t, mounts, 5)

	// Input at /tmp/code should use output name "modified-code" as subdir.
	codeMount := findMountByPath(t, mounts, "/tmp/code")
	codeVol := findVolumeByName(t, volumes, codeMount.Name)
	assertHostPath(t, codeVol, "steps/handle-3/modified-code")

	// Input at /tmp/shared should use output name "shared-out" as subdir.
	sharedMount := findMountByPath(t, mounts, "/tmp/shared")
	sharedVol := findVolumeByName(t, volumes, sharedMount.Name)
	assertHostPath(t, sharedVol, "steps/handle-3/shared-out")

	// Input at /tmp/data has no overlapping output, uses default subdir.
	findMountByPath(t, mounts, "/tmp/data")

	// Non-overlapping output "result" has its own volume.
	resultMount := findMountByPath(t, mounts, "/tmp/result")
	resultVol := findVolumeByName(t, volumes, resultMount.Name)
	assertHostPath(t, resultVol, "steps/handle-3/result")

	// Verify no separate volumes were created for overlapping outputs.
	for _, vol := range volumes {
		if vol.HostPath != nil {
			path := vol.HostPath.Path
			// "modified-code" and "shared-out" should only appear as input subdirs,
			// not as separate output-N volumes.
			if contains(path, "output-") && (contains(path, "modified-code") || contains(path, "shared-out")) {
				t.Errorf("unexpected separate output volume for overlapping output: %s", path)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Test 4: All inputs overlap all outputs
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_AllOverlapping(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/code"},
			{DestinationPath: "/tmp/data"},
		},
		Outputs: runtime.OutputPaths{
			"code-out": "/tmp/code",
			"data-out": "/tmp/data",
		},
	}

	c := makeContainer("handle-4", taskMetadata(), spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	// 1 dir + 2 inputs + 0 outputs = 3
	assertVolumeCount(t, volumes, 3)
	assertMountCount(t, mounts, 3)

	// Input subdirs should use output names.
	codeMount := findMountByPath(t, mounts, "/tmp/code")
	codeVol := findVolumeByName(t, volumes, codeMount.Name)
	assertHostPath(t, codeVol, "steps/handle-4/code-out")

	dataMount := findMountByPath(t, mounts, "/tmp/data")
	dataVol := findVolumeByName(t, volumes, dataMount.Name)
	assertHostPath(t, dataVol, "steps/handle-4/data-out")
}

// ---------------------------------------------------------------------------
// Test 5: No overlap between inputs and outputs
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_NoOverlap(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/source"},
			{DestinationPath: "/tmp/deps"},
		},
		Outputs: runtime.OutputPaths{
			"binary": "/tmp/binary",
			"docs":   "/tmp/docs",
		},
	}

	c := makeContainer("handle-5", taskMetadata(), spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	// 1 dir + 2 inputs + 2 outputs = 5
	assertVolumeCount(t, volumes, 5)
	assertMountCount(t, mounts, 5)

	// All paths should be present.
	findMountByPath(t, mounts, "/tmp/build")
	findMountByPath(t, mounts, "/tmp/source")
	findMountByPath(t, mounts, "/tmp/deps")
	findMountByPath(t, mounts, "/tmp/binary")
	findMountByPath(t, mounts, "/tmp/docs")
}

// ---------------------------------------------------------------------------
// Test 6: All volume types in DaemonSet mode
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_AllVolumeTypes_DaemonSet(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/input-a"},
			{DestinationPath: "/tmp/input-b"},
		},
		Outputs: runtime.OutputPaths{
			"result": "/tmp/result", // non-overlapping
		},
		Caches:       []string{"/cache/path"},
		ScratchPaths: []string{"/scratch"},
	}

	meta := db.ContainerMetadata{
		Type:     db.ContainerTypeTask,
		StepName: "my-step",
		JobID:    42,
		BuildID:  100,
	}
	c := makeContainer("handle-6", meta, spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	// 1 dir + 2 inputs + 1 output + 1 cache + 1 scratch = 6
	assertVolumeCount(t, volumes, 6)
	assertMountCount(t, mounts, 6)

	// Dir: hostPath
	dirMount := findMountByPath(t, mounts, "/tmp/build")
	dirVol := findVolumeByName(t, volumes, dirMount.Name)
	assertHostPath(t, dirVol, "steps/handle-6/dir")

	// Inputs: hostPath
	inputAMount := findMountByPath(t, mounts, "/tmp/input-a")
	inputAVol := findVolumeByName(t, volumes, inputAMount.Name)
	if inputAVol.HostPath == nil {
		t.Error("expected input-a to be hostPath in DaemonSet mode")
	}

	inputBMount := findMountByPath(t, mounts, "/tmp/input-b")
	inputBVol := findVolumeByName(t, volumes, inputBMount.Name)
	if inputBVol.HostPath == nil {
		t.Error("expected input-b to be hostPath in DaemonSet mode")
	}

	// Output: hostPath
	resultMount := findMountByPath(t, mounts, "/tmp/result")
	resultVol := findVolumeByName(t, volumes, resultMount.Name)
	assertHostPath(t, resultVol, "steps/handle-6/result")

	// Cache: hostPath (auto-detected from ArtifactDaemonHostPath)
	cacheMount := findMountByPath(t, mounts, "/cache/path")
	cacheVol := findVolumeByName(t, volumes, cacheMount.Name)
	if cacheVol.HostPath == nil {
		t.Error("expected cache to be hostPath in DaemonSet mode")
	}
	assertHostPath(t, cacheVol, "caches/")

	// Scratch: always emptyDir
	scratchMount := findMountByPath(t, mounts, "/scratch")
	scratchVol := findVolumeByName(t, volumes, scratchMount.Name)
	assertEmptyDir(t, scratchVol)
}

// ---------------------------------------------------------------------------
// Test 7: Put container with inputs
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_PutContainer(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypePut,
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/resource-a"},
			{DestinationPath: "/tmp/resource-b"},
			{DestinationPath: "/tmp/resource-c"},
		},
	}

	meta := db.ContainerMetadata{
		Type:     db.ContainerTypePut,
		StepName: "put-step",
		JobID:    10,
		BuildID:  200,
	}
	c := makeContainer("put-handle", meta, spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	// 1 dir + 3 inputs = 4
	assertVolumeCount(t, volumes, 4)
	assertMountCount(t, mounts, 4)

	findMountByPath(t, mounts, "/tmp/build")
	findMountByPath(t, mounts, "/tmp/resource-a")
	findMountByPath(t, mounts, "/tmp/resource-b")
	findMountByPath(t, mounts, "/tmp/resource-c")
}

// ---------------------------------------------------------------------------
// Test 8: Get container producing output
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_GetContainer(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/resource",
		Type: db.ContainerTypeGet,
	}

	meta := db.ContainerMetadata{
		Type:     db.ContainerTypeGet,
		StepName: "get-step",
		JobID:    10,
		BuildID:  201,
	}
	c := makeContainer("get-handle", meta, spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	// 1 dir only
	assertVolumeCount(t, volumes, 1)
	assertMountCount(t, mounts, 1)

	findMountByPath(t, mounts, "/tmp/resource")
}

// ---------------------------------------------------------------------------
// Test 9: Check container
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_CheckContainer(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/check",
		Type: db.ContainerTypeCheck,
	}

	meta := db.ContainerMetadata{
		Type:     db.ContainerTypeCheck,
		StepName: "check-step",
	}
	c := makeContainer("check-handle", meta, spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	// 1 dir only (check containers don't have inputs/outputs)
	assertVolumeCount(t, volumes, 1)
	assertMountCount(t, mounts, 1)

	findMountByPath(t, mounts, "/tmp/check")

	// Check containers: stepVolume without ArtifactDaemonHostPath uses emptyDir
	// because check type returns nil from buildArtifactStoreVolume.
	// But stepVolume itself uses ArtifactDaemonHostPath if configured.
	// The key point: buildCleanupInitContainer returns nil for check.
	cleanup := c.buildCleanupInitContainer()
	if cleanup != nil {
		t.Error("expected nil cleanup init container for check type, even when reused")
	}

	// Also verify with reused=true
	c2 := makeContainer("check-handle-2", meta, spec, cfg, nil, true)
	cleanup2 := c2.buildCleanupInitContainer()
	if cleanup2 != nil {
		t.Error("expected nil cleanup init container for check type, even when reused")
	}
}

// ---------------------------------------------------------------------------
// Test 10: Sidecar with caches
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_SidecarWithCaches(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/input"},
		},
		Caches: []string{"/cache/my-cache"},
		Sidecars: []atc.SidecarConfig{
			{Name: "helper", Image: "busybox"},
		},
	}

	meta := db.ContainerMetadata{
		Type:     db.ContainerTypeTask,
		StepName: "sidecar-step",
		JobID:    42,
		BuildID:  300,
	}
	c := makeContainer("sidecar-handle", meta, spec, cfg, nil, false)
	_, mounts := c.buildVolumeMounts()

	sidecars := buildSidecarContainers(spec.Sidecars, mounts, spec.Dir)
	if len(sidecars) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(sidecars))
	}

	sidecar := sidecars[0]
	if sidecar.Name != "helper" {
		t.Errorf("expected sidecar name 'helper', got %q", sidecar.Name)
	}

	// Sidecar should have same mounts as main container.
	if len(sidecar.VolumeMounts) != len(mounts) {
		t.Fatalf("expected sidecar to have %d mounts (same as main), got %d", len(mounts), len(sidecar.VolumeMounts))
	}

	// Verify cache mount is present in sidecar.
	found := false
	for _, m := range sidecar.VolumeMounts {
		if m.MountPath == "/cache/my-cache" {
			found = true
			break
		}
	}
	if !found {
		t.Error("sidecar is missing cache mount at /cache/my-cache")
	}
}

// ---------------------------------------------------------------------------
// Test 11: Sidecar with scratch paths
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_SidecarWithScratch(t *testing.T) {
	cfg := permEmptyDirConfig()
	spec := runtime.ContainerSpec{
		Dir:          "/tmp/build",
		Type:         db.ContainerTypeTask,
		ScratchPaths: []string{"/scratch/tmp"},
		Sidecars: []atc.SidecarConfig{
			{Name: "scratch-helper", Image: "alpine"},
		},
	}

	c := makeContainer("scratch-sc-handle", taskMetadata(), spec, cfg, nil, false)
	_, mounts := c.buildVolumeMounts()

	sidecars := buildSidecarContainers(spec.Sidecars, mounts, spec.Dir)
	if len(sidecars) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(sidecars))
	}

	// Verify scratch mount is in the sidecar.
	found := false
	for _, m := range sidecars[0].VolumeMounts {
		if m.MountPath == "/scratch/tmp" {
			found = true
			break
		}
	}
	if !found {
		t.Error("sidecar is missing scratch mount at /scratch/tmp")
	}
}

// ---------------------------------------------------------------------------
// Test 12: N inputs -> N init containers
// ---------------------------------------------------------------------------

func TestBuildArtifactInitContainers_MultipleInputs(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{Artifact: &permStubArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input-a"},
			{Artifact: &permStubArtifact{handle: "vol-b"}, DestinationPath: "/tmp/input-b"},
			{Artifact: &permStubArtifact{handle: "vol-c"}, DestinationPath: "/tmp/input-c"},
		},
	}

	c := makeContainer("init-handle", taskMetadata(), spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	inits, err := c.buildArtifactInitContainers(volumes, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}

	if len(inits) != 3 {
		t.Fatalf("expected 3 init containers, got %d", len(inits))
	}

	for i, init := range inits {
		expectedName := fmt.Sprintf("fetch-input-%d", i)
		if init.Name != expectedName {
			t.Errorf("init container %d: expected name %q, got %q", i, expectedName, init.Name)
		}
		if init.Image != "alpine:latest" {
			t.Errorf("init container %d: expected image alpine:latest, got %q", i, init.Image)
		}
		// Each init container should have exactly 2 volume mounts:
		// the artifact daemon hostpath and the input volume.
		if len(init.VolumeMounts) != 2 {
			t.Errorf("init container %d: expected 2 volume mounts, got %d", i, len(init.VolumeMounts))
		}
	}
}

// ---------------------------------------------------------------------------
// Test 13: Input without artifact -> no init container
// ---------------------------------------------------------------------------

func TestBuildArtifactInitContainers_InputWithoutArtifact(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{Artifact: &permStubArtifact{handle: "vol-a"}, DestinationPath: "/tmp/with-artifact"},
			{Artifact: nil, DestinationPath: "/tmp/without-artifact"},
		},
	}

	c := makeContainer("nil-art-handle", taskMetadata(), spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	inits, err := c.buildArtifactInitContainers(volumes, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}

	if len(inits) != 1 {
		t.Fatalf("expected 1 init container (nil artifact skipped), got %d", len(inits))
	}

	if inits[0].Name != "fetch-input-0" {
		t.Errorf("expected init container name fetch-input-0, got %q", inits[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Test 14: Init containers with locator hit vs miss
// ---------------------------------------------------------------------------

func TestBuildArtifactInitContainers_LocatorHitVsMiss(t *testing.T) {
	cfg := permDaemonSetConfig()
	locator := NewArtifactLocator()
	// Record locator entry for vol-a with a specific HostDir.
	locator.Record(ArtifactKey("vol-a"), "node-1", "producer-handle/result")

	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{Artifact: &permStubArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input-a"},
			{Artifact: &permStubArtifact{handle: "vol-b"}, DestinationPath: "/tmp/input-b"},
		},
	}

	c := makeContainer("locator-handle", taskMetadata(), spec, cfg, locator, false)
	volumes, mounts := c.buildVolumeMounts()

	inits, err := c.buildArtifactInitContainers(volumes, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}

	if len(inits) != 2 {
		t.Fatalf("expected 2 init containers, got %d", len(inits))
	}

	// First init container: locator hit. The resolve command should reference
	// the locator's HostDir ("producer-handle/result") as the daemon key.
	init0Cmd := fmt.Sprintf("%s", inits[0].Command)
	if !contains(init0Cmd, "producer-handle/result") {
		t.Errorf("init container 0: expected resolve command to use locator HostDir 'producer-handle/result', got command: %v", inits[0].Command)
	}

	// Second init container: locator miss. The resolve command should fall back
	// to the volume handle ("vol-b") as the daemon key.
	init1Cmd := fmt.Sprintf("%s", inits[1].Command)
	if !contains(init1Cmd, "vol-b") {
		t.Errorf("init container 1: expected resolve command to use volume handle 'vol-b' as fallback, got command: %v", inits[1].Command)
	}
}

// ---------------------------------------------------------------------------
// Test 17: Sidecar gets all mounts in DaemonSet mode
// ---------------------------------------------------------------------------

func TestBuildSidecarContainers_GetsAllMountsInDaemonSetMode(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/input"},
		},
		Outputs: runtime.OutputPaths{
			"output": "/tmp/output",
		},
		Caches:       []string{"/cache/data"},
		ScratchPaths: []string{"/scratch"},
		Sidecars: []atc.SidecarConfig{
			{Name: "all-mounts-sidecar", Image: "busybox"},
		},
	}

	meta := db.ContainerMetadata{
		Type:     db.ContainerTypeTask,
		StepName: "all-mounts-step",
		JobID:    42,
		BuildID:  400,
	}
	c := makeContainer("all-mounts-handle", meta, spec, cfg, nil, false)
	_, mounts := c.buildVolumeMounts()

	sidecars := buildSidecarContainers(spec.Sidecars, mounts, spec.Dir)
	if len(sidecars) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(sidecars))
	}

	sidecar := sidecars[0]

	// Sidecar should have identical volume mounts to main container.
	if len(sidecar.VolumeMounts) != len(mounts) {
		t.Fatalf("expected sidecar to have %d mounts (same as main), got %d", len(mounts), len(sidecar.VolumeMounts))
	}

	// Verify all mount paths are present.
	expectedPaths := map[string]bool{
		"/tmp/build":  true,
		"/tmp/input":  true,
		"/tmp/output": true,
		"/cache/data": true,
		"/scratch":    true,
	}
	for _, m := range sidecar.VolumeMounts {
		delete(expectedPaths, m.MountPath)
	}
	if len(expectedPaths) > 0 {
		for path := range expectedPaths {
			t.Errorf("sidecar missing mount at %s", path)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 18: Cleanup init container + artifact init containers ordering
// ---------------------------------------------------------------------------

func TestBuildPod_InitContainerOrdering(t *testing.T) {
	cfg := permDaemonSetConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{Artifact: &permStubArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input-a"},
			{Artifact: &permStubArtifact{handle: "vol-b"}, DestinationPath: "/tmp/input-b"},
		},
		ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
	}

	meta := db.ContainerMetadata{
		Type:     db.ContainerTypeTask,
		StepName: "ordering-step",
		JobID:    42,
		BuildID:  500,
	}

	// reused=true so cleanup init container is generated
	c := makeContainer("ordering-handle", meta, spec, cfg, nil, true)

	pod, err := c.buildPod(runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hello"},
	}, []string{"/bin/sh"}, []string{"-c", "echo hello"})
	if err != nil {
		t.Fatalf("buildPod: %v", err)
	}

	inits := pod.Spec.InitContainers

	// Expect: 1 cleanup + 2 fetch-input = 3 init containers.
	if len(inits) != 3 {
		t.Fatalf("expected 3 init containers, got %d", len(inits))
	}

	// First should be cleanup-stale.
	if inits[0].Name != "cleanup-stale" {
		t.Errorf("expected first init container to be 'cleanup-stale', got %q", inits[0].Name)
	}

	// Second and third should be fetch-input-0 and fetch-input-1.
	if inits[1].Name != "fetch-input-0" {
		t.Errorf("expected second init container to be 'fetch-input-0', got %q", inits[1].Name)
	}
	if inits[2].Name != "fetch-input-1" {
		t.Errorf("expected third init container to be 'fetch-input-1', got %q", inits[2].Name)
	}
}

// ---------------------------------------------------------------------------
// Additional edge case: emptyDir mode (no DaemonSet) produces no init containers
// ---------------------------------------------------------------------------

func TestBuildArtifactInitContainers_NoDaemonSet_ReturnsNil(t *testing.T) {
	cfg := permEmptyDirConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{Artifact: &permStubArtifact{handle: "vol-a"}, DestinationPath: "/tmp/input-a"},
		},
	}

	c := makeContainer("no-daemon-handle", taskMetadata(), spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	inits, err := c.buildArtifactInitContainers(volumes, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}

	if inits != nil {
		t.Errorf("expected nil init containers in non-DaemonSet mode, got %d", len(inits))
	}
}

// ---------------------------------------------------------------------------
// Additional edge case: emptyDir mode volumes are emptyDir
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_EmptyDirMode_AllEmptyDir(t *testing.T) {
	cfg := permEmptyDirConfig()
	spec := runtime.ContainerSpec{
		Dir:  "/tmp/build",
		Type: db.ContainerTypeTask,
		Inputs: []runtime.Input{
			{DestinationPath: "/tmp/input"},
		},
		Outputs: runtime.OutputPaths{
			"result": "/tmp/result",
		},
	}

	c := makeContainer("emptydir-handle", taskMetadata(), spec, cfg, nil, false)
	volumes, mounts := c.buildVolumeMounts()

	assertVolumeCount(t, volumes, 3)
	assertMountCount(t, mounts, 3)

	for _, vol := range volumes {
		assertEmptyDir(t, vol)
	}
}

// ---------------------------------------------------------------------------
// Additional edge case: relative scratch path resolved against Dir
// ---------------------------------------------------------------------------

func TestBuildVolumeMounts_RelativeScratchPath(t *testing.T) {
	cfg := permEmptyDirConfig()
	spec := runtime.ContainerSpec{
		Dir:          "/tmp/build",
		Type:         db.ContainerTypeTask,
		ScratchPaths: []string{"relative-scratch"},
	}

	c := makeContainer("rel-scratch-handle", taskMetadata(), spec, cfg, nil, false)
	_, mounts := c.buildVolumeMounts()

	// 1 dir + 1 scratch = 2
	assertMountCount(t, mounts, 2)

	expectedPath := filepath.Join("/tmp/build", "relative-scratch")
	findMountByPath(t, mounts, expectedPath)
}
