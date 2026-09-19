package jetbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestDaemonSetMode_PodHasHostPathVolume verifies that in DaemonSet mode,
// the pod spec includes a hostPath volume instead of a PVC volume.
func TestDaemonSetMode_PodHasHostPathVolume(t *testing.T) {
	cfg := Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactDaemonPort:     7780,
		ArtifactDaemonService:  "artifact-daemon",
		ArtifactHelperImage:    "alpine:latest",
	}

	backend := NewDaemonSetBackend(cfg, nil, nil)
	c := &Container{
		handle:         "test-handle",
		podName:        "test-pod",
		metadata:       db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec:  runtime.ContainerSpec{Dir: "/tmp/build", Type: db.ContainerTypeTask},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: backend,
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
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
	}

	c := &Container{
		handle:         "test-handle",
		podName:        "test-pod",
		metadata:       db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec:  runtime.ContainerSpec{Dir: "/tmp/build", Type: db.ContainerTypeTask},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
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

func TestDaemonSetMode_StrictInputValidationFailsClosed(t *testing.T) {
	ref := hangar.TreeRef{
		Scope:      "builds",
		Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Generation: 1,
	}
	ordinary := constructionArtifact("ordinary", "test-worker")

	tests := map[string]struct {
		cfg     Config
		inputs  []runtime.Input
		outputs runtime.OutputPaths
		want    string
	}{
		"missing source": {
			cfg: daemonSetConfig(), inputs: []runtime.Input{{DestinationPath: "/work/input"}}, want: "exactly one",
		},
		"two sources": {
			cfg: daemonSetConfig(), inputs: []runtime.Input{{Artifact: ordinary, HangarTree: &ref, DestinationPath: "/work/input"}}, want: "exactly one",
		},
		"disabled": {
			cfg: daemonSetConfig(), inputs: []runtime.Input{{HangarTree: &ref, DestinationPath: "/work/input"}}, want: "disabled",
		},
		"strict input output overlap": {
			cfg:     func() Config { cfg := daemonSetConfig(); cfg.HangarEnabled = true; return cfg }(),
			inputs:  []runtime.Input{{HangarTree: &ref, DestinationPath: "/work/input"}},
			outputs: runtime.OutputPaths{"result": "/work/input/"}, want: "overlap",
		},
		"strict input contains output": {
			cfg:     func() Config { cfg := daemonSetConfig(); cfg.HangarEnabled = true; return cfg }(),
			inputs:  []runtime.Input{{HangarTree: &ref, DestinationPath: "/work/exact"}},
			outputs: runtime.OutputPaths{"result": "/work/exact/result"}, want: "overlap",
		},
		"output contains strict input": {
			cfg:     func() Config { cfg := daemonSetConfig(); cfg.HangarEnabled = true; return cfg }(),
			inputs:  []runtime.Input{{HangarTree: &ref, DestinationPath: "/work/exact/input"}},
			outputs: runtime.OutputPaths{"result": "/work/exact"}, want: "overlap",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			container := &Container{
				handle: "task-handle", podName: "test-pod",
				metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
				containerSpec: runtime.ContainerSpec{
					Dir: "/work", Type: db.ContainerTypeTask,
					ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}, Inputs: test.inputs, Outputs: test.outputs,
				},
				config: test.cfg, storageBackend: NewDaemonSetBackend(test.cfg, nil, nil),
			}
			_, err := container.buildPod(runtime.ProcessSpec{}, []string{"sh", "-c", "true"}, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestDaemonSetMode_StrictInputAllowsSiblingOutput(t *testing.T) {
	ref := hangar.TreeRef{
		Scope:      "builds",
		Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Generation: 1,
	}
	cfg := daemonSetConfig()
	cfg.HangarEnabled = true
	container := &Container{
		containerSpec: runtime.ContainerSpec{
			Inputs:  []runtime.Input{{HangarTree: &ref, DestinationPath: "/work/exact"}},
			Outputs: runtime.OutputPaths{"result": "/work/exact2"},
		},
		config: cfg, storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}
	if err := container.validateInputs(); err != nil {
		t.Fatalf("sibling output must not overlap strict input: %v", err)
	}
}

func TestDaemonSetMode_StrictInputsAreReadOnlyEverywhereAndPodMountsResolve(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	signer, err := hangar.NewWarrantSigner(key, hangar.MaxWarrantTTL, func() time.Time {
		return time.Unix(1_800_000_000, 0).UTC()
	})
	if err != nil {
		t.Fatalf("new warrant signer: %v", err)
	}
	cfg := daemonSetConfig()
	cfg.HangarEnabled = true
	cfg.HangarWarrantSigner = signer
	ref := hangar.TreeRef{
		Scope:      "builds",
		Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Generation: 9,
	}
	container := &Container{
		handle: "task-handle", podName: "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir: "/work", Type: db.ContainerTypeTask,
			ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
			Inputs:    []runtime.Input{{HangarTree: &ref, DestinationPath: "/work/exact"}},
			Sidecars:  []atc.SidecarConfig{{Name: "observer", Image: "busybox"}},
		},
		config: cfg, storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}

	pod, err := container.buildPod(runtime.ProcessSpec{}, []string{"sh", "-c", "true"}, nil)
	if err != nil {
		t.Fatalf("build strict input Pod: %v", err)
	}
	if len(pod.Spec.Containers) != 2 {
		t.Fatalf("expected task plus sidecar, got %d containers", len(pod.Spec.Containers))
	}
	for _, taskContainer := range pod.Spec.Containers {
		mount := mountAtPath(t, taskContainer.VolumeMounts, "/work/exact")
		if !mount.ReadOnly {
			t.Errorf("container %q strict input mount is writable", taskContainer.Name)
		}
	}
	if len(pod.Spec.InitContainers) != 1 || pod.Spec.InitContainers[0].Name != "materialize-hangar-inputs" {
		t.Fatalf("unexpected strict init containers: %+v", pod.Spec.InitContainers)
	}
	strictInitMount := mountAtPath(t, pod.Spec.InitContainers[0].VolumeMounts, "/hangar-inputs/input-0")
	if !strictInitMount.ReadOnly {
		t.Fatal("materialization init must verify through a read-only input mount")
	}
	for _, mount := range pod.Spec.InitContainers[0].VolumeMounts {
		if mount.MountPath == "/work/exact" {
			t.Fatal("materialization init must not mount at the user-controlled destination")
		}
	}
	assertPodMountsResolve(t, pod)

	required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) != 1 {
		t.Fatalf("expected one required selector term, got %+v", required)
	}
	want := map[string]bool{
		"concourse.dev/artifact-cache": false,
		"concourse.dev/hangar-v1":      false,
	}
	for _, expression := range required.NodeSelectorTerms[0].MatchExpressions {
		if _, found := want[expression.Key]; found && expression.Operator == corev1.NodeSelectorOpIn && len(expression.Values) == 1 && expression.Values[0] == "ready" {
			want[expression.Key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("required selector term is missing %s In [ready]", key)
		}
	}
}

func TestDaemonSetMode_OrdinaryOverlappingInputRemainsWritable(t *testing.T) {
	cfg := daemonSetConfig()
	container := &Container{
		handle: "task-handle", podName: "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir: "/work", Type: db.ContainerTypeTask,
			ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
			Inputs:    []runtime.Input{{Artifact: constructionArtifact("ordinary", "test-worker"), DestinationPath: "/work/shared"}},
			Outputs:   runtime.OutputPaths{"result": "/work/shared/"},
		},
		config: cfg, storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}
	pod, err := container.buildPod(runtime.ProcessSpec{}, []string{"sh", "-c", "true"}, nil)
	if err != nil {
		t.Fatalf("ordinary input/output overlap changed behavior: %v", err)
	}
	if mountAtPath(t, pod.Spec.Containers[0].VolumeMounts, "/work/shared").ReadOnly {
		t.Fatal("ordinary overlapping input unexpectedly became read-only")
	}
	assertPodMountsResolve(t, pod)
}

type unresolvedMountBackend struct{ *DaemonSetBackend }

func (backend unresolvedMountBackend) BuildFetchInitContainers(string, []runtime.Input, []corev1.Volume, []corev1.VolumeMount) ([]corev1.Container, error) {
	return []corev1.Container{{Name: "bad-init", Image: "busybox", VolumeMounts: []corev1.VolumeMount{{Name: "missing", MountPath: "/missing"}}}}, nil
}

func TestDaemonSetMode_BuildPodRejectsUnresolvedMounts(t *testing.T) {
	cfg := daemonSetConfig()
	container := &Container{
		handle: "task-handle", podName: "test-pod",
		metadata:      db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{Dir: "/work", Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}},
		config:        cfg, storageBackend: unresolvedMountBackend{NewDaemonSetBackend(cfg, nil, nil)},
	}
	if _, err := container.buildPod(runtime.ProcessSpec{}, []string{"sh", "-c", "true"}, nil); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected unresolved init mount to reject Pod construction, got %v", err)
	}
}

func mountAtPath(t *testing.T, mounts []corev1.VolumeMount, path string) corev1.VolumeMount {
	t.Helper()
	for _, mount := range mounts {
		if filepath.Clean(mount.MountPath) == filepath.Clean(path) {
			return mount
		}
	}
	t.Fatalf("no mount at %q in %+v", path, mounts)
	return corev1.VolumeMount{}
}

// TestDaemonSetMode_SoftAffinity verifies soft scheduling toward input source node.
func TestDaemonSetMode_SoftAffinity(t *testing.T) {
	cfg := Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
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
					Artifact:        constructionArtifact("input-vol-1", "test-worker"),
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
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
		Namespace: "test-ns",
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

// TestDaemonSetMode_InitContainerResolveCommand verifies the daemon resolve command.
func TestDaemonSetMode_InitContainerResolveCommand(t *testing.T) {
	cfg := Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactDaemonPort:     7780,
		ArtifactDaemonService:  "artifact-daemon",
		ArtifactHelperImage:    "alpine:latest",
	}

	locator := NewArtifactLocator()
	locator.Record(ArtifactKey("vol-1"), "node-a", "test-handle/result")

	c := &Container{
		handle:   "test-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact("vol-1", "test-worker"),
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	// Build mounts to get volumeName
	volumes, mounts := c.buildVolumeMounts()

	inits, err := c.buildArtifactInitContainers(volumes, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected at least one init container")
	}

	init := inits[0]
	// Should NOT have MY_NODE_NAME or SOURCE_NODE env vars (removed in daemon resolve mode)
	for _, env := range init.Env {
		if env.Name == "MY_NODE_NAME" || env.Name == "SOURCE_NODE" || env.Name == "SOURCE_DAEMON_IP" {
			t.Errorf("unexpected env var %s — these were removed in daemon resolve mode", env.Name)
		}
	}

	// Command should use wget to /resolve endpoint
	cmd := strings.Join(init.Command, " ")
	if !strings.Contains(cmd, "/resolve") {
		t.Errorf("expected /resolve endpoint in command, got: %s", cmd)
	}
	if !strings.Contains(cmd, "test-handle/result") {
		t.Errorf("expected daemon key in command, got: %s", cmd)
	}
}

// TestArtifactLocator_RecordAndCleanup verifies the full lifecycle.
func TestDaemonSetMode_LocatorRecordLookupCleanup(t *testing.T) {
	locator := NewArtifactLocator()

	locator.Record("build-1", "node-a", "")
	locator.Record("build-2", "node-b", "")
	locator.Record("build-3", "node-a", "")

	// Verify lookup
	node, ok := locator.LocateNode("build-1")
	if !ok || node != "node-a" {
		t.Errorf("expected node-a, got %s", node)
	}

	// Simulate GC cleanup
	locator.Remove("build-1")
	_, ok = locator.LocateNode("build-1")
	if ok {
		t.Error("expected not found after Remove")
	}

	// Other entries unaffected
	node, ok = locator.LocateNode("build-2")
	if !ok || node != "node-b" {
		t.Errorf("expected node-b, got %s", node)
	}
}

// --- Gap #8: Uploads must be skipped in DaemonSet mode ---

func TestDaemonSetMode_UploadOutputsIsNoop(t *testing.T) {
	cfg := Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
	}

	c := &Container{
		handle:   "test-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:     "/tmp/build",
			Type:    db.ContainerTypeTask,
			Outputs: runtime.OutputPaths{"out": "/tmp/build/out"},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}

	p := &execProcess{
		id:             "test",
		podName:        "test-pod",
		config:         cfg,
		container:      c,
		storageBackend: c.storageBackend,
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
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
	}

	vol := NewStubVolume("output-vol", "test-worker", "/tmp/build/out")

	c := &Container{
		handle:   "test-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:     "/tmp/build",
			Type:    db.ContainerTypeTask,
			Outputs: runtime.OutputPaths{"out": "/tmp/build/out"},
		},
		config:         cfg,
		properties:     make(map[string]string),
		volumes:        []*Volume{vol},
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	p := &execProcess{
		id:             "test",
		podName:        "test-pod",
		config:         cfg,
		container:      c,
		storageBackend: c.storageBackend,
	}

	// recordOutputLocations should exist and record each output volume's
	// artifact key → node name in the locator.
	p.storageBackend.RecordOutputs(context.Background(), p.container.handle, "test-node-1", p.container.volumes, p.container.containerSpec)

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

// TestDaemonSetMode_RecordOutputLocationsWithEmptyNodeName verifies that
// recordOutputLocations still records the HostDir even when the node name
// is empty (e.g. pod not found). This prevents downstream steps from failing
// with "artifact location unknown" when the only issue is unknown node name.
func TestDaemonSetMode_RecordOutputLocationsWithEmptyNodeName(t *testing.T) {
	locator := NewArtifactLocator()

	cfg := Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
	}

	vol := NewStubVolume("output-vol", "test-worker", "/tmp/build/out")

	c := &Container{
		handle:   "test-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:     "/tmp/build",
			Type:    db.ContainerTypeTask,
			Outputs: runtime.OutputPaths{"out": "/tmp/build/out"},
		},
		config:         cfg,
		properties:     make(map[string]string),
		volumes:        []*Volume{vol},
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	p := &execProcess{
		id:             "test",
		podName:        "test-pod",
		config:         cfg,
		container:      c,
		storageBackend: c.storageBackend,
	}

	// Record with empty node name (simulates fetchPodNodeName failure).
	p.storageBackend.RecordOutputs(context.Background(), p.container.handle, "", p.container.volumes, p.container.containerSpec)

	// Should still record the hostDir so downstream steps can locate it.
	key := ArtifactKey(vol.Handle())
	loc, found := locator.Locate(key)
	if !found {
		t.Fatalf("expected locator to have key %s even with empty nodeName, but not found", key)
	}
	if loc.HostDir != "test-handle/out" {
		t.Errorf("expected hostDir test-handle/out, got %s", loc.HostDir)
	}
	if loc.NodeName != "" {
		t.Errorf("expected empty node name, got %s", loc.NodeName)
	}
}

// =======================================================================
// Phase 2: HostPath output and dir volumes
// =======================================================================

func daemonSetConfig() Config {
	return Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactDaemonPort:     7780,
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
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
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
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
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
					Artifact:        constructionArtifact("src-vol", "test-worker"),
					DestinationPath: "/tmp/build/src",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
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
		Namespace:           "test-ns",
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
	locator := NewArtifactLocator()
	locator.Record(ArtifactKey("src-vol"), "node-a", "source/dir")
	c := &Container{
		handle:   "build-50",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask, JobID: 7, StepName: "build"},
		containerSpec: runtime.ContainerSpec{
			TaskCacheIdentity: &atc.TaskCacheIdentity{TeamID: 17, TemplatePipelineID: 23, RunJobName: "deploy-staging"},
			StepName:          "ignored-by-cache-renderer",
			Dir:               "/tmp/build",
			Type:              db.ContainerTypeTask,
			ImageSpec:         runtime.ImageSpec{ImageURL: "docker:///busybox"},
			Caches:            []string{"/tmp/build/.cache"},
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact("src-vol", "test-worker"),
					DestinationPath: "/tmp/build/src",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	pod, err := c.buildPod(runtime.ProcessSpec{}, []string{"/bin/sh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	volumes := pod.Spec.Volumes

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
			if vol.HostPath.Path != "/var/concourse/artifacts/caches/run-17-23-build-f07d9dac9385" {
				t.Errorf("expected exact run cache key, got %s", vol.HostPath.Path)
			}
		}
	}
	if !foundCache {
		t.Fatal("no cache volume found")
	}
	if len(pod.Spec.InitContainers) == 0 || pod.Spec.InitContainers[0].Name != "fetch-inputs" {
		t.Fatalf("expected the full pod to include the fetch-inputs init container, got %#v", pod.Spec.InitContainers)
	}
	assertPodMountsResolve(t, pod)

}

func assertPodMountsResolve(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	volumeNames := map[string]int{}
	for _, volume := range pod.Spec.Volumes {
		volumeNames[volume.Name]++
	}
	containers := append([]corev1.Container{}, pod.Spec.InitContainers...)
	containers = append(containers, pod.Spec.Containers...)
	for _, container := range containers {
		for _, mount := range container.VolumeMounts {
			if volumeNames[mount.Name] != 1 {
				t.Fatalf("container %q mount %q resolves to %d pod volumes", container.Name, mount.Name, volumeNames[mount.Name])
			}
		}
	}
}

// =======================================================================
// Phase 3: cp -a init containers
// =======================================================================

func TestDaemonSetMode_InitContainerUsesDaemonResolve(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator()
	locator.Record(ArtifactKey("src-vol"), "this-node", "source-handle/src")

	c := &Container{
		handle:   "build-60",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact("src-vol", "test-worker"),
					DestinationPath: "/tmp/build/src",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	vols, mounts := c.buildVolumeMounts()
	inits, err := c.buildArtifactInitContainers(vols, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}

	if len(inits) == 0 {
		t.Fatal("expected at least one init container")
	}

	cmd := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmd, "/resolve") {
		t.Errorf("init container should use daemon /resolve endpoint, got command: %s", cmd)
	}
	if !strings.Contains(cmd, "source-handle/src") {
		t.Errorf("init container should reference daemon key, got command: %s", cmd)
	}
}

// --- Phase: Fail-fast tests ---

// TestDaemonSetMode_MissingLocatorFallsBackToVolumeHandle verifies that
// buildArtifactInitContainers uses the volume handle as daemon key when
// the locator has no entry (e.g., resource cache hit).
func TestDaemonSetMode_MissingLocatorFallsBackToVolumeHandle(t *testing.T) {
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
					Artifact:        constructionArtifact("cached-vol", "test-worker"),
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	vols, mounts := c.buildVolumeMounts()
	inits, err := c.buildArtifactInitContainers(vols, mounts)
	if err != nil {
		t.Fatalf("expected no error (graceful fallback), got: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected init container")
	}

	// The init container should use the volume handle as the daemon key.
	cmd := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmd, "cached-vol") {
		t.Errorf("expected init container to use volume handle as key, got: %s", cmd)
	}
}

// TestDaemonSetMode_EmptyKeyFailsFast verifies that
// daemonResolveCommand generates an exit-1 script when the key is empty.
func TestDaemonSetMode_EmptyKeyFailsFast(t *testing.T) {
	cfg := daemonSetConfig()
	backend := NewDaemonSetBackend(cfg, nil, nil)
	_ = &Container{config: cfg, storageBackend: backend}

	cmd := backend.daemonResolveCommand("", "/tmp/build/input")
	script := strings.Join(cmd, " ")
	if !strings.Contains(script, "exit 1") {
		t.Errorf("expected exit 1 for empty key, got: %s", script)
	}
	if strings.Contains(script, "wget") {
		t.Errorf("empty key should NOT generate wget command, got: %s", script)
	}
}

// TestDaemonSetMode_RecordAndLocateRoundTrip verifies that recording
// an artifact location and looking it up produces correct init containers.
func TestDaemonSetMode_RecordAndLocateRoundTrip(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator()

	// Simulate the producing step recording its output location.
	artifactHandle := "producer-handle-output-result"
	locator.Record(ArtifactKey(artifactHandle), "node-a", "producer-handle/result")

	c := &Container{
		handle:   "consumer-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact(artifactHandle, "test-worker"),
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	vols, mounts := c.buildVolumeMounts()
	inits, err := c.buildArtifactInitContainers(vols, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected at least one init container")
	}

	cmd := strings.Join(inits[0].Command, " ")
	// Should contain the daemon key in the /resolve call
	if !strings.Contains(cmd, "producer-handle/result") {
		t.Errorf("expected daemon key in command, got: %s", cmd)
	}
	if !strings.Contains(cmd, "/resolve") {
		t.Errorf("expected /resolve endpoint in command, got: %s", cmd)
	}
}

// --- Daemon resolve command tests ---

// TestDaemonSetMode_DaemonResolveCommand verifies that daemonResolveCommand
// generates a wget-based script that calls the local daemon's /resolve endpoint.
func TestDaemonSetMode_DaemonResolveCommand(t *testing.T) {
	cfg := daemonSetConfig()
	backend := NewDaemonSetBackend(cfg, nil, nil)
	_ = &Container{config: cfg, storageBackend: backend}

	cmd := backend.daemonResolveCommand("producer-handle/result", "/var/concourse/artifacts/steps/consumer/input-0")
	script := strings.Join(cmd, " ")

	if !strings.Contains(script, "wget") {
		t.Errorf("expected wget in resolve command, got: %s", script)
	}
	if !strings.Contains(script, "HOST_IP") {
		t.Errorf("expected HOST_IP reference in resolve command, got: %s", script)
	}
	if !strings.Contains(script, "/resolve") {
		t.Errorf("expected /resolve endpoint in command, got: %s", script)
	}
	if !strings.Contains(script, "producer-handle/result") {
		t.Errorf("expected daemon key in command, got: %s", script)
	}
}

// TestDaemonSetMode_InitContainerUsesResolveCommand verifies that init containers
// use the daemon /resolve endpoint instead of cp -a or remote wget.
func TestDaemonSetMode_InitContainerUsesResolveCommand(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator()
	locator.Record(ArtifactKey("remote-vol"), "node-b", "source-handle/out")

	c := &Container{
		handle:   "consumer",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact("remote-vol", "test-worker"),
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	vols, mounts := c.buildVolumeMounts()
	inits, err := c.buildArtifactInitContainers(vols, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected at least one init container")
	}

	// Init container should NOT have MY_NODE_NAME, SOURCE_NODE, or SOURCE_DAEMON_IP env vars
	for _, env := range inits[0].Env {
		if env.Name == "MY_NODE_NAME" || env.Name == "SOURCE_NODE" || env.Name == "SOURCE_DAEMON_IP" {
			t.Errorf("unexpected env var %s — these were removed in daemon resolve mode", env.Name)
		}
	}

	// Command should use wget to localhost /resolve, not cp -a
	cmd := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmd, "/resolve") {
		t.Errorf("expected /resolve endpoint in command, got: %s", cmd)
	}
	if strings.Contains(cmd, "cp -a") {
		t.Errorf("should NOT use cp -a in daemon resolve mode, got: %s", cmd)
	}
}

// =======================================================================
// Phase: hostPath cleanup on container reuse
// =======================================================================

// TestDaemonSetMode_CleanupInitContainerOnReuse verifies that when a container
// handle is reused (crash recovery), a cleanup-stale init container is added
// to the pod spec that removes stale hostPath data.
func TestDaemonSetMode_CleanupInitContainerOnReuse(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "reused-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeGet},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build/get",
			Type: db.ContainerTypeGet,
		},
		config:         cfg,
		properties:     make(map[string]string),
		reused:         true,
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}

	cleanup, _ := c.buildCleanupInitContainer()
	if cleanup == nil {
		t.Fatal("expected cleanup init container for reused container, got nil")
	}
	if cleanup.Name != "cleanup-stale" {
		t.Errorf("expected name cleanup-stale, got %s", cleanup.Name)
	}
	if cleanup.Image != cfg.ArtifactHelperImage {
		t.Errorf("expected image %s, got %s", cfg.ArtifactHelperImage, cleanup.Image)
	}

	// Command should remove the stale steps directory.
	cmd := strings.Join(cleanup.Command, " ")
	if !strings.Contains(cmd, "rm -rf") {
		t.Errorf("expected rm -rf in cleanup command, got: %s", cmd)
	}
	if !strings.Contains(cmd, "reused-handle") {
		t.Errorf("expected container handle in cleanup path, got: %s", cmd)
	}

	// Should mount the artifact hostPath volume (writable, not read-only).
	if len(cleanup.VolumeMounts) == 0 {
		t.Fatal("expected volume mounts on cleanup container")
	}
	found := false
	for _, m := range cleanup.VolumeMounts {
		if m.Name == artifactDaemonHostPathVolumeName {
			found = true
			if m.ReadOnly {
				t.Error("cleanup init container should mount artifact hostPath writable")
			}
		}
	}
	if !found {
		t.Errorf("expected artifact hostPath volume mount, got: %v", cleanup.VolumeMounts)
	}
}

// TestDaemonSetMode_NoCleanupOnFreshContainer verifies that fresh containers
// (not reused) do NOT get a cleanup init container.
func TestDaemonSetMode_NoCleanupOnFreshContainer(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "fresh-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeGet},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build/get",
			Type: db.ContainerTypeGet,
		},
		config:         cfg,
		properties:     make(map[string]string),
		reused:         false,
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}

	cleanup, _ := c.buildCleanupInitContainer()
	if cleanup != nil {
		t.Errorf("expected no cleanup init container for fresh container, got: %+v", cleanup)
	}
}

// TestDaemonSetMode_NoCleanupInPVCMode verifies that reused containers in PVC
// mode (no ArtifactDaemonHostPath) don't get a cleanup init container.
func TestDaemonSetMode_NoCleanupInPVCMode(t *testing.T) {
	cfg := Config{Namespace: "test-ns"}
	c := &Container{
		handle:   "reused-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeGet},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build/get",
			Type: db.ContainerTypeGet,
		},
		config:     cfg,
		properties: make(map[string]string),
		reused:     true,
	}

	cleanup, _ := c.buildCleanupInitContainer()
	if cleanup != nil {
		t.Errorf("expected no cleanup in PVC mode, got: %+v", cleanup)
	}
}

// TestDaemonSetMode_NoCleanupForCheckContainers verifies that check containers
// don't get cleanup init containers — they use emptyDir (not hostPath), so
// there is no stale data to clean up between runs.
func TestDaemonSetMode_NoCleanupForCheckContainers(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "check-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeCheck},
		containerSpec: runtime.ContainerSpec{
			Type: db.ContainerTypeCheck,
		},
		config:         cfg,
		properties:     make(map[string]string),
		reused:         true,
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}

	cleanup, _ := c.buildCleanupInitContainer()
	if cleanup != nil {
		t.Errorf("expected no cleanup for check containers, got: %+v", cleanup)
	}
}

// TestDaemonSetMode_CleanupPrecedesArtifactInits verifies that the cleanup
// init container runs BEFORE any artifact fetch init containers.
func TestDaemonSetMode_CleanupPrecedesArtifactInits(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator()
	locator.Record(ArtifactKey("src-vol"), "node-a", "source/dir")

	c := &Container{
		handle:   "reused-task",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:       "/tmp/build",
			Type:      db.ContainerTypeTask,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact("src-vol", "test-worker"),
					DestinationPath: "/tmp/build/src",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
		reused:         true,
	}

	spec := runtime.ProcessSpec{
		Path: "/bin/sh",
		Args: []string{"-c", "echo test"},
		Dir:  "/tmp/build",
	}

	pod, err := c.buildPod(spec, []string{"sh", "-c", "sleep 86400"}, nil)
	if err != nil {
		t.Fatalf("buildPod: %v", err)
	}

	if len(pod.Spec.InitContainers) < 2 {
		t.Fatalf("expected at least 2 init containers (cleanup + fetch), got %d", len(pod.Spec.InitContainers))
	}
	if pod.Spec.InitContainers[0].Name != "cleanup-stale" {
		t.Errorf("expected first init container to be cleanup-stale, got %s", pod.Spec.InitContainers[0].Name)
	}
	if pod.Spec.InitContainers[1].Name != "fetch-inputs" {
		t.Errorf("expected second init container to be fetch-inputs, got %s", pod.Spec.InitContainers[1].Name)
	}
}

// =======================================================================
// Phase: Daemon alias registration
// =======================================================================

// TestDaemonSetMode_NoAliasRegistrationWithoutNodeName verifies that
// registerDaemonAlias is not called when nodeName is empty.
func TestDaemonSetMode_NoAliasRegistrationWithoutNodeName(t *testing.T) {
	locator := NewArtifactLocator()
	cfg := daemonSetConfig()

	vol := NewStubVolume("output-vol", "test-worker", "/tmp/build/out")

	c := &Container{
		handle:   "producer",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:     "/tmp/build",
			Type:    db.ContainerTypeTask,
			Outputs: runtime.OutputPaths{"out": "/tmp/build/out"},
		},
		config:         cfg,
		properties:     make(map[string]string),
		volumes:        []*Volume{vol},
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	p := &execProcess{
		id:             "test",
		podName:        "test-pod",
		config:         cfg,
		container:      c,
		storageBackend: c.storageBackend,
	}

	// Record with empty node name — should NOT attempt daemon registration
	// (would fail with DNS error). This just verifies no panic.
	p.storageBackend.RecordOutputs(context.Background(), p.container.handle, "", p.container.volumes, p.container.containerSpec)

	// Locator should still be populated.
	key := ArtifactKey(vol.Handle())
	_, found := locator.Locate(key)
	if !found {
		t.Fatalf("expected locator entry for %s even with empty node", key)
	}
}

// =======================================================================
// Phase 3: Resource caching flow tests
// =======================================================================

// TestDaemonSetMode_CacheHitFlow verifies that when a resource cache hit
// occurs (locator has no entry for the volume handle), the downstream task's
// init container uses the volume handle as the daemon key. The daemon resolves
// it via the registered alias from Phase 2.
func TestDaemonSetMode_CacheHitFlow(t *testing.T) {
	cfg := daemonSetConfig()
	// Empty locator simulates a cache hit: the original get step's locator
	// entry was recorded in a different ATC instance or has been lost.
	locator := NewArtifactLocator()

	// The cached volume handle — this is what the DB returns on cache hit.
	cachedVolHandle := "cached-resource-vol-abc123"

	c := &Container{
		handle:   "task-consumer",
		podName:  "consumer-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:       "/tmp/build",
			Type:      db.ContainerTypeTask,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact(cachedVolHandle, "test-worker"),
					DestinationPath: "/tmp/build/resource",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	vols, mounts := c.buildVolumeMounts()
	inits, err := c.buildArtifactInitContainers(vols, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected init container for cached input")
	}

	// The init container should use the volume handle as the daemon key
	// (since locator has no entry for this cache hit).
	cmd := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmd, cachedVolHandle) {
		t.Errorf("expected volume handle %q in daemon resolve command, got: %s", cachedVolHandle, cmd)
	}
	if !strings.Contains(cmd, "/resolve") {
		t.Errorf("expected /resolve endpoint in command, got: %s", cmd)
	}
}

// TestDaemonSetMode_CacheMissFlow verifies the normal (non-cache) flow:
// the producing get step records output, and the consuming task uses the
// daemon key from the locator.
func TestDaemonSetMode_CacheMissFlow(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator()

	// Step 1: Simulate the producing get step recording its output.
	producerVol := NewStubVolume("get-vol-handle", "test-worker", "/tmp/build/get")
	producer := &Container{
		handle:   "get-container",
		podName:  "get-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeGet},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build/get",
			Type: db.ContainerTypeGet,
		},
		config:         cfg,
		properties:     make(map[string]string),
		volumes:        []*Volume{producerVol},
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}
	producerProcess := &execProcess{
		id:             "get",
		podName:        "get-pod",
		config:         cfg,
		container:      producer,
		storageBackend: producer.storageBackend,
	}
	producerProcess.storageBackend.RecordOutputs(context.Background(), producerProcess.container.handle, "node-a", producerProcess.container.volumes, producerProcess.container.containerSpec)

	// Verify the locator was populated.
	key := ArtifactKey(producerVol.Handle())
	loc, found := locator.Locate(key)
	if !found {
		t.Fatalf("expected locator entry for %s", key)
	}
	if loc.HostDir != "get-container/dir" {
		t.Errorf("expected hostDir get-container/dir, got %s", loc.HostDir)
	}

	// Step 2: Build the consuming task's init containers.
	consumer := &Container{
		handle:   "task-container",
		podName:  "task-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact(producerVol.Handle(), "test-worker"),
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	vols, mounts := consumer.buildVolumeMounts()
	inits, err := consumer.buildArtifactInitContainers(vols, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected init container for input")
	}

	// The init container should use the daemon key from the locator
	// (not the volume handle fallback).
	cmd := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmd, "get-container/dir") {
		t.Errorf("expected daemon key get-container/dir in command, got: %s", cmd)
	}
}

// TestDaemonSetMode_SkipResourceCacheReturnsFalse verifies the worker
// enables resource caching.
func TestDaemonSetMode_SkipResourceCacheReturnsFalse(t *testing.T) {
	cfg := daemonSetConfig()
	w := &Worker{config: cfg}
	if w.SkipResourceCache() {
		t.Error("expected SkipResourceCache to return false")
	}
}

// =======================================================================
// Phase 4: Edge cases and hardening
// =======================================================================

// TestDaemonSetMode_CacheHitATCRestart simulates a cache hit after ATC restart.
// The locator is empty (lost on restart), so the init container falls back to
// the volume handle as the daemon key. The daemon resolves it via the alias
// registered during the original get step (alias survives within daemon lifecycle).
func TestDaemonSetMode_CacheHitATCRestart(t *testing.T) {
	cfg := daemonSetConfig()
	// Fresh locator simulates ATC restart — all in-memory state lost.
	locator := NewArtifactLocator()

	cachedVolHandle := "resource-cache-vol-xyz"

	c := &Container{
		handle:   "task-after-restart",
		podName:  "task-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact(cachedVolHandle, "test-worker"),
					DestinationPath: "/tmp/build/resource",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	vols, mounts := c.buildVolumeMounts()
	inits, err := c.buildArtifactInitContainers(vols, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected init container")
	}

	// Uses volume handle as daemon key (locator empty after restart).
	cmd := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmd, cachedVolHandle) {
		t.Errorf("expected volume handle fallback, got: %s", cmd)
	}
}

// TestDaemonSetMode_CacheHitDaemonRestart simulates a cache hit where the
// daemon has also restarted. The daemon's filesystem scan at startup should
// rediscover artifacts under steps/<container-handle>/<subdir>/, but NOT
// the volume handle alias. This test verifies that the fallback to filesystem
// scan (checking steps/<key>) happens when the alias is missing.
//
// In practice, the daemon's /resolve endpoint checks:
//  1. In-memory registry (alias — lost on restart)
//  2. Filesystem fallback: does steps/<key> exist? (works for daemon keys
//     like "container-handle/dir" but NOT for raw volume handles)
//
// When both ATC and daemon restart, the volume handle key won't resolve.
// This is an accepted limitation documented in the spec as out of scope:
// "Persisting daemon registry aliases across daemon restarts"
func TestDaemonSetMode_CacheHitDaemonRestartLimitation(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator()

	// Volume handle has no matching steps/<vol-handle> directory on disk.
	// The daemon's filesystem scan won't find it. This is expected.
	cachedVolHandle := "vol-no-disk-match"

	c := &Container{
		handle:   "task-double-restart",
		podName:  "task-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact(cachedVolHandle, "test-worker"),
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	vols, mounts := c.buildVolumeMounts()
	inits, err := c.buildArtifactInitContainers(vols, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected init container")
	}

	// The init container will be created with the volume handle as key.
	// If the daemon can't resolve it, the init container fails and the
	// build retries (standard Concourse retry behavior).
	cmd := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmd, cachedVolHandle) {
		t.Errorf("expected volume handle in command, got: %s", cmd)
	}
	// Verify the command will error properly on failure (exit 1 in wget).
	if !strings.Contains(cmd, "exit 1") {
		t.Errorf("expected error exit on failure, got: %s", cmd)
	}
}

// TestDaemonSetMode_ConcurrentBuildsShareCache verifies that two builds
// with the same resource version can share the same locator entry.
func TestDaemonSetMode_ConcurrentBuildsShareCache(t *testing.T) {
	cfg := daemonSetConfig()
	locator := NewArtifactLocator()

	// Build 1 produces output and records it.
	vol1 := NewStubVolume("shared-vol", "test-worker", "/tmp/build/get")
	producer := &Container{
		handle:   "get-build-1",
		podName:  "get-pod-1",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeGet},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build/get",
			Type: db.ContainerTypeGet,
		},
		config:         cfg,
		properties:     make(map[string]string),
		volumes:        []*Volume{vol1},
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}
	p1 := &execProcess{id: "get-1", podName: "get-pod-1", config: cfg, container: producer, storageBackend: producer.storageBackend}
	p1.storageBackend.RecordOutputs(context.Background(), p1.container.handle, "node-a", p1.container.volumes, p1.container.containerSpec)

	// Build 2 consumes the same volume via cache hit.
	consumer1 := &Container{
		handle:   "task-build-2",
		podName:  "task-pod-2",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact("shared-vol", "test-worker"),
					DestinationPath: "/tmp/build/input",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	vols, mounts := consumer1.buildVolumeMounts()
	inits, err := consumer1.buildArtifactInitContainers(vols, mounts)
	if err != nil {
		t.Fatalf("buildArtifactInitContainers: %v", err)
	}
	if len(inits) == 0 {
		t.Fatal("expected init container")
	}

	// Should use the daemon key from the locator (recorded by build 1).
	cmd := strings.Join(inits[0].Command, " ")
	if !strings.Contains(cmd, "get-build-1/dir") {
		t.Errorf("expected daemon key from build 1, got: %s", cmd)
	}
}

// =======================================================================
// Bug #1: Overlapping input+output must produce correct artifact reference
// =======================================================================

// TestDaemonSetMode_OverlappingInputOutputRecordsInputVolume verifies that
// when a task declares both an input and output at the same path (e.g.
// inputs: [{name: repo}], outputs: [{name: repo}]), only the input volume
// is created by buildVolumeMountsForSpec (the output volume is skipped).
// This ensures that registerOutputs (task_step.go) and recordOutputLocations
// (process.go) both use the same volume handle, so downstream steps can
// resolve the artifact correctly.
func TestDaemonSetMode_OverlappingInputOutputRecordsInputVolume(t *testing.T) {
	locator := NewArtifactLocator()

	cfg := Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
	}

	// Only the input volume should exist — buildVolumeMountsForSpec skips
	// the output volume when it overlaps with an input path.
	inputVol := NewStubVolume("handle-input-0", "test-worker", "/tmp/build/repo")

	c := &Container{
		handle:   "test-handle",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{DestinationPath: "/tmp/build/repo"},
			},
			Outputs: runtime.OutputPaths{"repo": "/tmp/build/repo/"},
		},
		config:         cfg,
		properties:     make(map[string]string),
		volumes:        []*Volume{inputVol},
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	p := &execProcess{
		id:             "test",
		podName:        "test-pod",
		config:         cfg,
		container:      c,
		storageBackend: c.storageBackend,
	}

	p.storageBackend.RecordOutputs(context.Background(), p.container.handle, "node-a", p.container.volumes, p.container.containerSpec)

	// The input volume should be recorded since it's the one actually
	// mounted in the pod and the only volume created for this path.
	inputKey := ArtifactKey(inputVol.Handle())
	loc, found := locator.Locate(inputKey)
	if !found {
		t.Fatalf("expected input volume %q to be recorded in locator, but not found", inputVol.Handle())
	}
	if loc.NodeName != "node-a" {
		t.Errorf("expected node node-a, got %s", loc.NodeName)
	}
	// The daemonKey should use the output name as subdir since the input
	// volume's hostPath uses the output name when paths overlap.
	if loc.HostDir != "test-handle/repo" {
		t.Errorf("expected hostDir test-handle/repo, got %s", loc.HostDir)
	}
}

// TestDaemonSetMode_ProducerModifierConsumerChain verifies the full
// producer → modifier (input+output same name) → consumer flow.
// This is the e2e scenario that was failing: the consumer's fetch-input-0
// init container couldn't find the modifier's output because registerOutputs
// and recordOutputLocations disagreed on which volume handle to use.
func TestDaemonSetMode_ProducerModifierConsumerChain(t *testing.T) {
	locator := NewArtifactLocator()

	cfg := Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
	}

	// Step 1: Producer creates output "shared"
	producerVol := NewStubVolume("producer-output-shared", "test-worker", "/tmp/build/shared/")
	producerContainer := &Container{
		handle:   "producer-handle",
		podName:  "producer-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:     "/tmp/build",
			Type:    db.ContainerTypeTask,
			Outputs: runtime.OutputPaths{"shared": "/tmp/build/shared/"},
		},
		config:         cfg,
		properties:     make(map[string]string),
		volumes:        []*Volume{producerVol},
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	producerProcess := &execProcess{
		id: "producer", podName: "producer-pod", config: cfg, container: producerContainer,
		storageBackend: producerContainer.storageBackend,
	}
	producerProcess.storageBackend.RecordOutputs(context.Background(), producerProcess.container.handle, "node-a", producerProcess.container.volumes, producerProcess.container.containerSpec)

	// Verify producer recorded correctly
	producerKey := ArtifactKey(producerVol.Handle())
	producerLoc, found := locator.Locate(producerKey)
	if !found {
		t.Fatalf("producer volume not found in locator")
	}
	if producerLoc.HostDir != "producer-handle/shared" {
		t.Errorf("expected producer hostDir producer-handle/shared, got %s", producerLoc.HostDir)
	}

	// Step 2: Modifier has input "shared" + output "shared" (same path).
	// buildVolumeMountsForSpec should only create ONE volume (the input).
	// The output volume is skipped because it overlaps.
	modifierInputVol := NewStubVolume("modifier-input-0", "test-worker", "/tmp/build/shared")

	modifierContainer := &Container{
		handle:   "modifier-handle",
		podName:  "modifier-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{
					Artifact:        constructionArtifact(producerVol.Handle(), "test-worker"),
					DestinationPath: "/tmp/build/shared",
				},
			},
			Outputs: runtime.OutputPaths{"shared": "/tmp/build/shared/"},
		},
		config:         cfg,
		properties:     make(map[string]string),
		volumes:        []*Volume{modifierInputVol}, // only input vol — no output vol
		storageBackend: NewDaemonSetBackend(cfg, locator, nil),
	}

	modifierProcess := &execProcess{
		id: "modifier", podName: "modifier-pod", config: cfg, container: modifierContainer, storageBackend: modifierContainer.storageBackend,
	}
	modifierProcess.storageBackend.RecordOutputs(context.Background(), modifierProcess.container.handle, "node-a", modifierProcess.container.volumes, modifierProcess.container.containerSpec)

	// Step 3: Consumer tries to fetch modifier's output.
	// registerOutputs (task_step.go) would register modifierInputVol as the
	// artifact for "shared" — the same handle that recordOutputLocations recorded.
	consumerKey := ArtifactKey(modifierInputVol.Handle())
	consumerLoc, found := locator.Locate(consumerKey)
	if !found {
		t.Fatalf("modifier output (via input volume %q) not found in locator — this is the bug that caused fetch-input-0 to fail", modifierInputVol.Handle())
	}
	if consumerLoc.HostDir != "modifier-handle/shared" {
		t.Errorf("expected modifier hostDir modifier-handle/shared, got %s", consumerLoc.HostDir)
	}
	if consumerLoc.NodeName != "node-a" {
		t.Errorf("expected node-a, got %s", consumerLoc.NodeName)
	}
}

// =======================================================================
// Bug #2: Output directories must be created even for non-overlapping outputs
// =======================================================================

// TestDaemonSetMode_OutputDirCreatedInPod verifies that output volumes
// produce hostPath directories in the K8s pod spec using HostPathDirectoryOrCreate,
// ensuring the output directory exists before the task runs.
func TestDaemonSetMode_OutputDirCreatedInPod(t *testing.T) {
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
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}

	volumes, mounts := c.buildVolumeMounts()

	// Find the output volume
	foundOutput := false
	for _, vol := range volumes {
		if vol.HostPath != nil && strings.Contains(vol.HostPath.Path, "/result") {
			foundOutput = true
			if vol.HostPath.Type == nil || *vol.HostPath.Type != corev1.HostPathDirectoryOrCreate {
				t.Errorf("output volume should use HostPathDirectoryOrCreate, got %v", vol.HostPath.Type)
			}
			expectedPath := filepath.Join(cfg.ArtifactDaemonHostPath, "steps", "build-42", "result")
			if vol.HostPath.Path != expectedPath {
				t.Errorf("expected hostPath %s, got %s", expectedPath, vol.HostPath.Path)
			}
		}
	}
	if !foundOutput {
		t.Fatal("expected an output volume with hostPath containing /result")
	}

	// Verify the output is actually mounted
	foundMount := false
	for _, m := range mounts {
		if m.MountPath == "/tmp/build/result" {
			foundMount = true
		}
	}
	if !foundMount {
		t.Fatal("expected a volume mount at /tmp/build/result")
	}
}

// TestDaemonSetMode_OverlappingOutputDirStillAccessible verifies that when
// an output overlaps an input, the task can still write to the output path
// because it shares the input's volume mount. The input volume's hostPath
// must have HostPathDirectoryOrCreate so the directory exists.
func TestDaemonSetMode_OverlappingOutputDirStillAccessible(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "build-42",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{DestinationPath: "/tmp/build/repo"},
			},
			Outputs: runtime.OutputPaths{"repo": "/tmp/build/repo/"},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}

	volumes, mounts := c.buildVolumeMounts()

	// Should only have 2 volumes: dir + shared input (no separate output volume)
	if len(volumes) != 2 {
		t.Fatalf("expected 2 volumes (dir + shared input), got %d", len(volumes))
	}

	// The shared input volume should have HostPathDirectoryOrCreate
	for _, vol := range volumes {
		if vol.HostPath != nil && strings.Contains(vol.HostPath.Path, "input-") {
			if vol.HostPath.Type == nil || *vol.HostPath.Type != corev1.HostPathDirectoryOrCreate {
				t.Errorf("input volume should use HostPathDirectoryOrCreate, got %v", vol.HostPath.Type)
			}
		}
	}

	// Only 2 mounts, one for dir and one for the shared path
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(mounts))
	}

	// The repo path should be mounted (via the input volume)
	foundRepo := false
	for _, m := range mounts {
		if filepath.Clean(m.MountPath) == "/tmp/build/repo" {
			foundRepo = true
		}
	}
	if !foundRepo {
		t.Fatal("expected a volume mount at /tmp/build/repo")
	}
}

// =======================================================================
// Sidecar mounts in DaemonSet mode
// =======================================================================

// TestDaemonSetMode_SidecarGetsHostPathMounts verifies that when using
// DaemonSet hostPath volumes, sidecars receive the same volume mounts
// as the main container — including the correct hostPath directories for
// inputs, outputs, and the working directory.
func TestDaemonSetMode_SidecarGetsHostPathMounts(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "build-99",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{DestinationPath: "/tmp/build/repo"},
			},
			Outputs: runtime.OutputPaths{"result": "/tmp/build/result"},
			Sidecars: []atc.SidecarConfig{
				{
					Name:       "helper",
					Image:      "helper:latest",
					Command:    []string{"/usr/bin/helper"},
					WorkingDir: "/tmp/build",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}

	volumes, mounts := c.buildVolumeMounts()
	sidecarContainers := buildSidecarContainers(c.containerSpec.Sidecars, mounts, c.containerSpec.Dir)

	if len(sidecarContainers) != 1 {
		t.Fatalf("expected 1 sidecar container, got %d", len(sidecarContainers))
	}

	sidecar := sidecarContainers[0]

	// Sidecar must have the same volume mounts as the main container
	if len(sidecar.VolumeMounts) != len(mounts) {
		t.Fatalf("sidecar has %d mounts, main has %d", len(sidecar.VolumeMounts), len(mounts))
	}

	for i, sm := range sidecar.VolumeMounts {
		if sm.Name != mounts[i].Name || sm.MountPath != mounts[i].MountPath {
			t.Errorf("sidecar mount[%d] = {%s, %s}, main = {%s, %s}", i, sm.Name, sm.MountPath, mounts[i].Name, mounts[i].MountPath)
		}
	}

	// Verify the sidecar can see all expected paths
	mountPaths := make(map[string]bool)
	for _, m := range sidecar.VolumeMounts {
		mountPaths[m.MountPath] = true
	}

	if !mountPaths["/tmp/build"] {
		t.Error("sidecar missing mount for working directory /tmp/build")
	}
	if !mountPaths["/tmp/build/repo"] {
		t.Error("sidecar missing mount for input /tmp/build/repo")
	}
	if !mountPaths["/tmp/build/result"] {
		t.Error("sidecar missing mount for output /tmp/build/result")
	}

	// Verify all volumes are hostPath with DirectoryOrCreate in DaemonSet mode
	for _, vol := range volumes {
		if vol.HostPath == nil {
			t.Errorf("volume %s should be hostPath in DaemonSet mode", vol.Name)
			continue
		}
		if vol.HostPath.Type == nil || *vol.HostPath.Type != corev1.HostPathDirectoryOrCreate {
			t.Errorf("volume %s should use HostPathDirectoryOrCreate", vol.Name)
		}
	}

	// Verify sidecar WorkingDir is set correctly
	if sidecar.WorkingDir != "/tmp/build" {
		t.Errorf("sidecar WorkingDir = %q, want /tmp/build", sidecar.WorkingDir)
	}
}

// TestDaemonSetMode_SidecarWithOverlappingInputOutput verifies that sidecars
// get correct mounts when inputs and outputs overlap (same name). The shared
// volume must be accessible to the sidecar at the correct path.
func TestDaemonSetMode_SidecarWithOverlappingInputOutput(t *testing.T) {
	cfg := daemonSetConfig()
	c := &Container{
		handle:   "build-100",
		podName:  "test-pod",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: runtime.ContainerSpec{
			Dir:  "/tmp/build",
			Type: db.ContainerTypeTask,
			Inputs: []runtime.Input{
				{DestinationPath: "/tmp/build/shared"},
			},
			Outputs: runtime.OutputPaths{"shared": "/tmp/build/shared/"},
			Sidecars: []atc.SidecarConfig{
				{
					Name:  "watcher",
					Image: "watcher:latest",
				},
			},
		},
		config:         cfg,
		properties:     make(map[string]string),
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}

	volumes, mounts := c.buildVolumeMounts()
	sidecarContainers := buildSidecarContainers(c.containerSpec.Sidecars, mounts, c.containerSpec.Dir)

	if len(sidecarContainers) != 1 {
		t.Fatalf("expected 1 sidecar, got %d", len(sidecarContainers))
	}

	sidecar := sidecarContainers[0]

	// Should only have 2 volumes: dir + shared (output deduped)
	if len(volumes) != 2 {
		t.Fatalf("expected 2 volumes (dir + shared), got %d", len(volumes))
	}

	// Sidecar should have exactly the same mounts as main (2 mounts)
	if len(sidecar.VolumeMounts) != 2 {
		t.Fatalf("sidecar should have 2 mounts, got %d", len(sidecar.VolumeMounts))
	}

	// The shared path must be mounted in the sidecar
	foundShared := false
	for _, m := range sidecar.VolumeMounts {
		if filepath.Clean(m.MountPath) == "/tmp/build/shared" {
			foundShared = true
			// The volume name should be input-*, not output-*
			if !strings.HasPrefix(m.Name, "input-") {
				t.Errorf("shared mount should use input volume, got %s", m.Name)
			}
		}
	}
	if !foundShared {
		t.Error("sidecar missing mount for shared path /tmp/build/shared")
	}

	// The shared volume's hostPath should use the output name as subdir
	for _, vol := range volumes {
		if vol.HostPath != nil && strings.Contains(vol.HostPath.Path, "/shared") {
			expectedPath := filepath.Join(cfg.ArtifactDaemonHostPath, "steps", "build-100", "shared")
			if vol.HostPath.Path != expectedPath {
				t.Errorf("shared volume hostPath = %s, want %s", vol.HostPath.Path, expectedPath)
			}
		}
	}
}

// The captured output stays an ordinary output.
//
// Reviewer's F4, and the ruling on it: capture is ADDITIVE. `Container.buildPod`
// now mounts the reserved incarnation as the selected output's volume, and
// RecordOutputs went on recording `steps/<handle>/<output>` for it -- a sibling
// directory the producer never writes into -- so a downstream step consuming
// that output would fetch an empty directory and nothing would say so.
//
// The control is asserted first and it is the unselected output: it still
// records `<handle>/<name>`, because Req 59 says an ordinary output's behaviour
// is unchanged.
func TestDaemonSetMode_RecordOutputsPointsTheCapturedOutputAtItsIncarnation(t *testing.T) {
	type registration struct {
		Key, LocalPath string
		ReadOnly       bool
	}
	var registrations []registration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/register" {
			http.NotFound(w, r)

			return
		}
		var req struct {
			Key       string `json:"key"`
			LocalPath string `json:"local_path"`
			ReadOnly  bool   `json:"read_only"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		registrations = append(registrations, registration{req.Key, req.LocalPath, req.ReadOnly})
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	endpoint, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parsing the test server URL: %v", err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatalf("parsing the test server port: %v", err)
	}

	locator := NewArtifactLocator()
	cfg := daemonSetConfig()
	cfg.OutputPlaneEnabled = true
	cfg.ArtifactDaemonPort = port

	captured := NewStubVolume("captured-vol", "test-worker", "/tmp/build/result")
	ordinary := NewStubVolume("ordinary-vol", "test-worker", "/tmp/build/report")

	control := admittedCapture()
	spec := runtime.ContainerSpec{
		Dir:              "/tmp/build",
		Type:             db.ContainerTypeTask,
		Outputs:          runtime.OutputPaths{"result": "/tmp/build/result", "report": "/tmp/build/report"},
		ExecutionControl: control,
	}

	// A real NodeIPResolver over a fake API server whose one Node's internal IP
	// is the test server's, so that RecordOutputs reaches the register route
	// the way it does in a cluster. Without it `registerAlias` returns at its
	// first WARNING line and the whole register body -- including the read-only
	// flag Req 16 depends on -- is never observed. That is the round-2 finding
	// R2-2: the flag was set at a site no committed test could see.
	backend := NewDaemonSetBackend(cfg, locator, nil)
	backend.nodeIPResolver = NewNodeIPResolver(fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: endpoint.Hostname()},
		}},
	}))

	backend.RecordOutputs(context.Background(), "capture-handle", "test-node",
		[]*Volume{captured, ordinary}, spec)

	// The control: the unselected output is exactly where it always was.
	ordinaryLoc, found := locator.Locate(ArtifactKey(ordinary.Handle()))
	if !found {
		t.Fatal("the unselected output was not recorded at all")
	}
	if ordinaryLoc.HostDir != "capture-handle/report" {
		t.Errorf("the unselected output moved to %q; an ordinary output is unchanged",
			ordinaryLoc.HostDir)
	}

	// And the captured one names the directory the producer actually wrote to.
	capturedLoc, found := locator.Locate(ArtifactKey(captured.Handle()))
	if !found {
		t.Fatal("the captured output was not recorded, so no downstream step can find it")
	}
	want := control.Capture.ReservedDirectory
	if capturedLoc.HostDir != want {
		t.Errorf("the captured output is recorded at %q and its producer wrote into %q; a "+
			"downstream step would fetch a directory that does not exist",
			capturedLoc.HostDir, want)
	}

	// The BODY RecordOutputs sent, for both outputs. The register guard refuses
	// an alias onto a capture-held location, and it is right to: a second
	// write-capable name for bytes a capture is about to seal hands every
	// key-taking destructive path a way to reach them under a name the capture
	// never heard of. A read is not that, so the alias declares which of the
	// two it is -- and if it declares wrong the daemon answers 409 and the
	// captured output's ordinary key resolves to nothing.
	sent := map[string]registration{}
	for _, entry := range registrations {
		sent[entry.Key] = entry
	}
	if len(sent) != 2 {
		t.Fatalf("RecordOutputs registered %d distinct aliases, not two: %+v", len(sent), registrations)
	}

	ordinaryAlias, ok := sent[string(ArtifactKey(ordinary.Handle()))]
	if !ok {
		t.Fatalf("the unselected output registered no alias: %+v", registrations)
	}
	if ordinaryAlias.ReadOnly {
		t.Error("an ordinary output's alias declared itself read-only")
	}
	if !strings.HasSuffix(ordinaryAlias.LocalPath, "capture-handle/report") {
		t.Errorf("the unselected output's alias points at %q", ordinaryAlias.LocalPath)
	}

	capturedAlias, ok := sent[string(ArtifactKey(captured.Handle()))]
	if !ok {
		t.Fatalf("the captured output registered no alias: %+v", registrations)
	}
	if !capturedAlias.ReadOnly {
		t.Error("the captured output's alias did not declare itself read-only, so the register " +
			"guard refuses it as a second write-capable name for held bytes and the captured " +
			"output's ordinary key resolves to nothing")
	}
	if !strings.HasSuffix(capturedAlias.LocalPath, want) {
		t.Errorf("the captured output's alias points at %q and the incarnation is %q",
			capturedAlias.LocalPath, want)
	}
}
