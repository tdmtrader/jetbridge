package jetbridge

// RESTORED 2026-09-05 (rebase onto core for 0.3.2), from the deleted
// atc/worker/jetbridge/behavioral_permutations_test.go (merge-base aef2244a63,
// via the compile-adapted copy the port stage produced for this branch).
//
// Fourteen of the file's nineteen tests -- rows JB-behavioral_permutations-000,
// -002, -003, -004, -006, -007, -008, -009, -010, -011, -014, -016, -017 and
// -018 of DISPOSITION-jetbridge.md -- were recorded DELETED on FILE-level
// evidence only, and the rebase re-verification did not sustain them
// (twelve REFUTED, two GAP). Restoring is always acceptable; deleting on
// inference is not.
//
// The five tests whose evidence held -- NoInputsMultipleOutputs,
// AllVolumeTypes_DaemonSet, InputWithoutArtifact, LocatorHitVsMiss and
// BuildPod_InitContainerOrdering -- stay deleted. The retained tests keep their
// original domain assertions; the substring helpers now use strings.Contains.

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
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
		false,
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

// assertMountLayout keeps exact counts and independently specified paths together.
func assertMountLayout(t *testing.T, c *Container, expected int, paths ...string) ([]corev1.Volume, []corev1.VolumeMount) {
	t.Helper()
	volumes, mounts := c.buildVolumeMounts()
	if len(volumes) != expected {
		t.Fatalf("expected %d volumes, got %d", expected, len(volumes))
	}
	assertMountCount(t, mounts, expected)
	for _, path := range paths {
		findMountByPath(t, mounts, path)
	}
	return volumes, mounts
}

func assertHostPathMount(t *testing.T, volumes []corev1.Volume, mounts []corev1.VolumeMount, path, suffix string) {
	t.Helper()
	mount := findMountByPath(t, mounts, path)
	assertHostPath(t, findVolumeByName(t, volumes, mount.Name), suffix)
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

// assertHostPath checks that a volume is hostPath and contains the requested fragment.
func assertHostPath(t *testing.T, vol corev1.Volume, expectedSuffix string) {
	t.Helper()
	if vol.HostPath == nil {
		t.Errorf("volume %q: expected hostPath, got emptyDir or nil", vol.Name)
		return
	}
	if !strings.Contains(vol.HostPath.Path, expectedSuffix) {
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

// assertSidecarMounts keeps the three distinct fixtures on one mount contract.
// Paths remain explicit expectations, independent of the generated mounts.
func assertSidecarMounts(t *testing.T, c *Container, spec runtime.ContainerSpec, paths ...string) corev1.Container {
	t.Helper()
	if len(paths) == 0 {
		t.Fatal("sidecar mount check requires expected paths")
	}
	_, mounts := c.buildVolumeMounts()
	sidecars := buildSidecarContainers(spec.Sidecars, mounts, spec.Dir)
	if len(sidecars) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(sidecars))
	}
	sidecar := sidecars[0]
	if len(sidecar.VolumeMounts) != len(mounts) {
		t.Fatalf("expected sidecar to have %d mounts (same as main), got %d", len(mounts), len(sidecar.VolumeMounts))
	}
	for _, path := range paths {
		findMountByPath(t, sidecar.VolumeMounts, path)
	}
	return sidecar
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
	// 1 dir + 3 inputs = 4
	// Dir mount
	// Input mounts
	volumes, _ := assertMountLayout(t, c, 4, "/tmp/build", "/tmp/input-a", "/tmp/input-b", "/tmp/input-c")

	// In hostPath mode, inputs use input-N as subdirs (no overlapping outputs).
	for _, vol := range volumes[1:4] {
		assertHostPath(t, vol, "steps/handle-1/")
	}
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
			"result":        "/tmp/result", // non-overlapping
			"shared-out":    "/tmp/shared", // overlaps input
		},
	}

	c := makeContainer("handle-3", taskMetadata(), spec, cfg, nil, false)
	// 1 dir + 3 inputs + 1 non-overlapping output = 5
	volumes, mounts := assertMountLayout(t, c, 5)

	// Input at /tmp/code should use output name "modified-code" as subdir.
	assertHostPathMount(t, volumes, mounts, "/tmp/code", "steps/handle-3/modified-code")

	// Input at /tmp/shared should use output name "shared-out" as subdir.
	assertHostPathMount(t, volumes, mounts, "/tmp/shared", "steps/handle-3/shared-out")

	// Input at /tmp/data has no overlapping output, uses default subdir.
	findMountByPath(t, mounts, "/tmp/data")

	// Non-overlapping output "result" has its own volume.
	assertHostPathMount(t, volumes, mounts, "/tmp/result", "steps/handle-3/result")

	// Verify no separate volumes were created for overlapping outputs.
	for _, vol := range volumes {
		if vol.HostPath != nil {
			path := vol.HostPath.Path
			// "modified-code" and "shared-out" should only appear as input subdirs,
			// not as separate output-N volumes.
			if strings.Contains(path, "output-") && (strings.Contains(path, "modified-code") || strings.Contains(path, "shared-out")) {
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
	// 1 dir + 2 inputs + 0 outputs = 3
	volumes, mounts := assertMountLayout(t, c, 3)

	// Input subdirs should use output names.
	assertHostPathMount(t, volumes, mounts, "/tmp/code", "steps/handle-4/code-out")

	assertHostPathMount(t, volumes, mounts, "/tmp/data", "steps/handle-4/data-out")
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
	// 1 dir + 2 inputs + 2 outputs = 5
	// All paths should be present.
	assertMountLayout(t, c, 5, "/tmp/build", "/tmp/source", "/tmp/deps", "/tmp/binary", "/tmp/docs")
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
	// 1 dir + 3 inputs = 4
	assertMountLayout(t, c, 4, "/tmp/build", "/tmp/resource-a", "/tmp/resource-b", "/tmp/resource-c")
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
	// 1 dir only
	assertMountLayout(t, c, 1, "/tmp/resource")
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
	// 1 dir only (check containers don't have inputs/outputs)
	volumes, _ := assertMountLayout(t, c, 1, "/tmp/check")

	// Check containers must use emptyDir even with a DaemonSet backend,
	// because the same container handle is reused across check runs and
	// hostPath would leave stale data.
	assertEmptyDir(t, volumes[0])

	// Check containers use emptyDir (not hostPath) so there is no stale
	// data across runs. No cleanup init container is needed, even when reused.
	cleanup := c.buildCleanupInitContainer()
	if cleanup != nil {
		t.Error("expected nil cleanup init container for check container")
	}

	// Reused check containers still don't need cleanup — emptyDir is fresh per pod.
	c2 := makeContainer("check-handle-2", meta, spec, cfg, nil, true)
	cleanup2 := c2.buildCleanupInitContainer()
	if cleanup2 != nil {
		t.Error("expected nil cleanup init container for reused check container")
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
	sidecar := assertSidecarMounts(t, c, spec, "/cache/my-cache")
	if sidecar.Name != "helper" {
		t.Errorf("expected sidecar name 'helper', got %q", sidecar.Name)
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
	assertSidecarMounts(t, c, spec, "/scratch/tmp")
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

	// Multiple inputs produce a single batch init container.
	if len(inits) != 1 {
		t.Fatalf("expected 1 batch init container, got %d", len(inits))
	}

	init := inits[0]
	if init.Name != "fetch-inputs" {
		t.Errorf("expected name fetch-inputs, got %q", init.Name)
	}
	if init.Image != "alpine:latest" {
		t.Errorf("expected image alpine:latest, got %q", init.Image)
	}
	// Batch container should have: 1 hostpath + 3 input volume mounts.
	if len(init.VolumeMounts) != 4 {
		t.Errorf("expected 4 volume mounts (1 hostpath + 3 inputs), got %d", len(init.VolumeMounts))
	}
	// Command should use /resolve-batch.
	cmdStr := strings.Join(init.Command, " ")
	if !strings.Contains(cmdStr, "/resolve-batch") {
		t.Errorf("expected /resolve-batch in command, got: %s", cmdStr)
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
	assertSidecarMounts(t, c, spec,
		"/tmp/build",
		"/tmp/input",
		"/tmp/output",
		"/cache/data",
		"/scratch",
	)
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
	volumes, _ := assertMountLayout(t, c, 3)

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
