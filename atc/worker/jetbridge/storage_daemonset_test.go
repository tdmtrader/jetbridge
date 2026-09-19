package jetbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	corev1 "k8s.io/api/core/v1"
)

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

// These callers inspect layout, wrapping and locator state, not daemon I/O.
// Real registration and mirroring are exercised in artifact-recording.feature.
func testBackend(locator *ArtifactLocator) *DaemonSetBackend {
	return NewDaemonSetBackend(testDaemonConfig(), locator, nil)
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
	vol := b.CacheVolume("cache-0", atc.TaskCacheIdentity{JobID: 42}, "my-step", "/cache/path")

	if vol.HostPath == nil {
		t.Fatal("expected hostPath volume")
	}
	// This literal is the pre-run cache key and must remain compatible.
	expected := filepath.Join("/artifact-store/caches", "job-42-my-step-3f84b7078395")
	if vol.HostPath.Path != expected {
		t.Errorf("expected path %q, got %q", expected, vol.HostPath.Path)
	}
}

func TestDaemonSetBackend_CacheVolume_UsesCacheHostPathWhenSet(t *testing.T) {
	cfg := testDaemonConfig()
	cfg.CacheHostPath = "/custom-cache-dir"
	b := NewDaemonSetBackend(cfg, nil, nil)

	vol := b.CacheVolume("cache-0", atc.TaskCacheIdentity{JobID: 1}, "step", "/path")
	if !strings.HasPrefix(vol.HostPath.Path, "/custom-cache-dir/") {
		t.Errorf("expected path under /custom-cache-dir, got %q", vol.HostPath.Path)
	}
}

func TestDaemonSetBackend_CacheVolume_Deterministic(t *testing.T) {
	b := testBackend(nil)
	vol1 := b.CacheVolume("c1", atc.TaskCacheIdentity{JobID: 42}, "step", "/cache")
	vol2 := b.CacheVolume("c2", atc.TaskCacheIdentity{JobID: 42}, "step", "/cache")

	if vol1.HostPath.Path != vol2.HostPath.Path {
		t.Errorf("same (jobID, step, path) should produce same hostPath, got %q and %q", vol1.HostPath.Path, vol2.HostPath.Path)
	}
}

func TestDaemonSetBackend_CacheVolume_UsesRunIdentityInsteadOfEphemeralIDs(t *testing.T) {
	b := testBackend(nil)
	shared := atc.TaskCacheIdentity{TeamID: 17, TemplatePipelineID: 23, RunJobName: "deploy-staging"}

	first := b.CacheVolume("cache-0", shared, "build.assets", "/work/cache")
	second := b.CacheVolume("cache-1", shared, "build.assets", "/work/cache")
	differentJob := b.CacheVolume("cache-2", atc.TaskCacheIdentity{TeamID: 17, TemplatePipelineID: 23, RunJobName: "deploy-production"}, "build.assets", "/work/cache")

	if first.HostPath.Path != "/artifact-store/caches/run-17-23-build-assets-34a6ec221a61" {
		t.Fatalf("expected exact run cache key, got %q", first.HostPath.Path)
	}
	if second.HostPath.Path != first.HostPath.Path {
		t.Fatalf("same run scope should converge: %q != %q", second.HostPath.Path, first.HostPath.Path)
	}
	if differentJob.HostPath.Path == first.HostPath.Path {
		t.Fatal("different materialized job names must use different run cache keys")
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
		{Artifact: constructionArtifact("vol-a", "worker-1"), DestinationPath: "/tmp/input-a"},
		{Artifact: constructionArtifact("vol-b", "worker-1"), DestinationPath: "/tmp/input-b"},
		{Artifact: constructionArtifact("vol-c", "worker-1"), DestinationPath: "/tmp/input-c"},
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

	inits, err := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)
	if err != nil {
		t.Fatalf("BuildFetchInitContainers: %v", err)
	}

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

func TestDaemonSetBackend_BuildFetchInitContainers_RefusesNilArtifact(t *testing.T) {
	b := testBackend(nil)
	inputs := []runtime.Input{
		{Artifact: constructionArtifact("vol-a", "worker-1"), DestinationPath: "/tmp/input-a"},
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

	// No pipeline can emit an input without an artifact, and the pod builder
	// refuses one before it gets here; the backend refuses too rather than
	// quietly fetching everything but that input.
	inits, err := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)
	if err == nil {
		t.Fatalf("expected an artifact-less input to be refused, got %d init containers", len(inits))
	}
	if !strings.Contains(err.Error(), `"/tmp/input-b"`) || !strings.Contains(err.Error(), "no artifact") {
		t.Errorf("expected the error to name the artifact-less input, got %q", err)
	}
	if inits != nil {
		t.Errorf("expected no init containers alongside the refusal, got %d", len(inits))
	}
}

func TestDaemonSetBackend_BuildFetchInitContainers_LocatorHit(t *testing.T) {
	locator := NewArtifactLocator()
	locator.Record("vol-a", "node-1", "producer-handle/result")

	b := testBackend(locator)
	inputs := []runtime.Input{
		{Artifact: constructionArtifact("vol-a", "worker-1"), DestinationPath: "/tmp/input"},
	}
	mounts := []corev1.VolumeMount{
		{Name: "input-0", MountPath: "/tmp/input"},
	}
	volumes := []corev1.Volume{
		b.StepVolume("input-0", "handle", "input-0"),
	}

	inits, err := b.BuildFetchInitContainers("handle", inputs, volumes, mounts)
	if err != nil {
		t.Fatalf("BuildFetchInitContainers: %v", err)
	}
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
	inits, err := b.BuildFetchInitContainers("handle", nil, nil, nil)
	if err != nil {
		t.Fatalf("BuildFetchInitContainers: %v", err)
	}
	if len(inits) != 0 {
		t.Errorf("expected 0 init containers for nil inputs, got %d", len(inits))
	}
}

func TestDaemonSetBackend_BuildFetchInitContainers_AppendsExactHangarBatch(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	signer, err := hangar.NewWarrantSigner(key, hangar.MaxWarrantTTL, func() time.Time {
		return time.Unix(1_800_000_000, 0).UTC()
	})
	if err != nil {
		t.Fatalf("new warrant signer: %v", err)
	}
	verifier, err := hangar.NewWarrantVerifier(key, hangar.MaxWarrantTTL, func() time.Time {
		return time.Unix(1_800_000_001, 0).UTC()
	})
	if err != nil {
		t.Fatalf("new warrant verifier: %v", err)
	}

	cfg := testDaemonConfig()
	cfg.HangarEnabled = true
	cfg.HangarWarrantSigner = signer
	b := NewDaemonSetBackend(cfg, nil, nil)
	ref := hangar.TreeRef{
		Scope:      "builds",
		Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Generation: 7,
	}
	inputs := []runtime.Input{
		{Artifact: constructionArtifact("ordinary", "worker-1"), DestinationPath: "/work/ordinary"},
		{HangarTree: &ref, DestinationPath: "/work/exact"},
	}
	mounts := []corev1.VolumeMount{
		{Name: "input-0", MountPath: "/work/ordinary"},
		{Name: "input-1", MountPath: "/work/exact", ReadOnly: true},
	}
	volumes := []corev1.Volume{
		b.StepVolume("input-0", "task-handle", "input-0"),
		b.StepVolume("input-1", "task-handle", "input-1"),
	}
	volumes = append(volumes, *b.ArtifactStoreVolume(db.ContainerTypeTask))

	inits, err := b.BuildFetchInitContainers("task-handle", inputs, volumes, mounts)
	if err != nil {
		t.Fatalf("build fetch init containers: %v", err)
	}
	if len(inits) != 2 {
		t.Fatalf("expected ordered ordinary and Hangar init containers, got %d", len(inits))
	}
	if inits[0].Name != "fetch-inputs" || inits[1].Name != "materialize-hangar-inputs" {
		t.Fatalf("unexpected init order: %q then %q", inits[0].Name, inits[1].Name)
	}

	command := strings.Join(inits[1].Command, " ")
	const prefix = "REQUEST_B64='"
	start := strings.Index(command, prefix)
	if start < 0 {
		t.Fatalf("Hangar command has no base64 request marker")
	}
	start += len(prefix)
	end := strings.Index(command[start:], "'")
	if end < 0 {
		t.Fatalf("Hangar command has an unterminated base64 request")
	}
	payload, err := base64.StdEncoding.DecodeString(command[start : start+end])
	if err != nil {
		t.Fatalf("decode Hangar request: %v", err)
	}
	var request struct {
		Items []struct {
			Ref     hangar.TreeRef `json:"ref"`
			Handle  string         `json:"handle"`
			Volume  string         `json:"volume"`
			Warrant string         `json:"warrant"`
		} `json:"items"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		t.Fatalf("decode Hangar request JSON: %v", err)
	}
	if len(request.Items) != 1 {
		t.Fatalf("expected one Hangar request item, got %d", len(request.Items))
	}
	item := request.Items[0]
	if item.Ref != ref || item.Handle != "task-handle" || item.Volume != "input-1" {
		t.Fatalf("unexpected exact request binding: %+v", item)
	}
	token := strings.TrimPrefix(item.Warrant, "Bearer ")
	if token == item.Warrant || verifier.Verify(token, ref, "task-handle", "input-1") != nil {
		t.Fatal("Hangar item did not carry a valid exact bearer warrant")
	}
	if strings.Contains(command, string(key)) {
		t.Fatal("capability signing key entered the init command")
	}
	for _, env := range inits[1].Env {
		if strings.Contains(env.Value, string(key)) {
			t.Fatal("capability signing key entered the init environment")
		}
	}
	encodedPodSurface, err := json.Marshal(struct {
		Init    corev1.Container `json:"init"`
		Volumes []corev1.Volume  `json:"volumes"`
	}{Init: inits[1], Volumes: volumes})
	if err != nil {
		t.Fatalf("marshal strict init Pod surface: %v", err)
	}
	if strings.Contains(string(encodedPodSurface), string(key)) || strings.Contains(string(encodedPodSurface), base64.StdEncoding.EncodeToString(key)) {
		t.Fatal("long-lived capability key entered the strict init Pod spec")
	}
	if len(inits[1].VolumeMounts) != 1 || inits[1].VolumeMounts[0].Name != "input-1" {
		t.Fatalf("strict init must reuse its input volume, got %+v", inits[1].VolumeMounts)
	}
	if inits[1].VolumeMounts[0].MountPath != "/hangar-inputs/input-0" || !inits[1].VolumeMounts[0].ReadOnly {
		t.Fatalf("strict init must use a fixed read-only verification mount, got %+v", inits[1].VolumeMounts[0])
	}
	assertContainerMountsResolve(t, inits, volumes)
}

func TestDaemonSetBackend_HangarInitRequiresExactResponseAndReceipt(t *testing.T) {
	ref := hangar.TreeRef{
		Scope:      "builds",
		Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Generation: 7,
	}
	wrongRef := ref
	wrongRef.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	wrongGeneration := ref
	wrongGeneration.Generation++
	exactReceipt, _ := json.Marshal(ref)
	wrongRefReceipt, _ := json.Marshal(wrongRef)
	wrongGenerationReceipt, _ := json.Marshal(wrongGeneration)

	tests := []struct {
		name         string
		sequence     string
		body         string
		wgetExit     string
		receipt      []byte
		receiptKind  string
		rootKind     string
		rootMode     os.FileMode
		receiptMode  os.FileMode
		wantSuccess  bool
		wantAttempts int
	}{
		{name: "exact 204 and exact receipt", sequence: "204", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantSuccess: true, wantAttempts: 1},
		{name: "false 204 missing receipt", sequence: "204", receiptKind: "missing", rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "false 204 wrong ref", sequence: "204", receipt: wrongRefReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "false 204 wrong generation", sequence: "204", receipt: wrongGenerationReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "false 204 malformed receipt", sequence: "204", receipt: []byte(`{"scope":`), rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "false 204 trailing receipt bytes", sequence: "204", receipt: append(append([]byte{}, exactReceipt...), '\n'), rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "false 204 symlink receipt", sequence: "204", receipt: exactReceipt, receiptKind: "symlink", rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "false 204 symlink root", sequence: "204", receipt: exactReceipt, rootKind: "symlink", rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "false 204 wrong receipt mode", sequence: "204", receipt: exactReceipt, rootMode: 0555, receiptMode: 0644, wantAttempts: 1},
		{name: "false 204 wrong root mode", sequence: "204", receipt: exactReceipt, rootMode: 0755, receiptMode: 0444, wantAttempts: 1},
		{name: "status 200", sequence: "200", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "status 201", sequence: "201", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "204 whitespace body", sequence: "204", body: " ", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "204 truncated body", sequence: "204", body: "{", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "204 HTML body", sequence: "204", body: "<html></html>", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "wget failure with 204", sequence: "204", wgetExit: "1", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
		{name: "retry no status only", sequence: "none,204", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantSuccess: true, wantAttempts: 2},
		{name: "retry exact 503 only", sequence: "503,204", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantSuccess: true, wantAttempts: 2},
		{name: "no status retry is bounded", sequence: "none", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 5},
		{name: "503 retry is bounded", sequence: "503,503,503,503,503", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 5},
		{name: "other status is terminal", sequence: "500,204", receipt: exactReceipt, rootMode: 0555, receiptMode: 0444, wantAttempts: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runHangarInitShell(t, ref, hangarShellFixture{
				sequence: test.sequence, body: test.body, wgetExit: test.wgetExit,
				receipt: test.receipt, receiptKind: test.receiptKind, rootKind: test.rootKind,
				rootMode: test.rootMode, receiptMode: test.receiptMode,
			})
			if (result.err == nil) != test.wantSuccess {
				t.Fatalf("exit error = %v, want success %t; output=%q", result.err, test.wantSuccess, result.output)
			}
			if result.attempts != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", result.attempts, test.wantAttempts)
			}
		})
	}
}

// TestDaemonSetBackend_HangarInitUsesOneFixedReadOnlyVerificationMountPerTree
// moved to brine.
//
// Its three assertions -- one fixed read-only verification mount per tree, the
// exact expected receipt in the command, and no user-controlled destination in
// it -- are what a consumer's Pod SAYS, which is the learning's own brine-shaped
// row. They live in features/hangar-consumer-pod.feature now, over a pod built
// from a tree a real capture really published rather than from a ref this file
// wrote down. See atc/worker/jetbridge/brine/steps/hangar_consumer_pod.go.
//
// Its sibling at TestDaemonSetBackend_HangarInitRequiresExactResponseAndReceipt
// did NOT move, and the plan's own reason for keeping
// TestDaemonSetBackend_HangarInitSignalsCleanUpAndFail is why: it runs the
// generated shell with a fake wget on PATH, and script text is not spec. Its
// twenty-one rows are about retry bounds, symlinked receipts, file modes and
// false 204s -- behaviour brine cannot express and nothing else covers.

func TestDaemonSetBackend_HangarInitSignalsCleanUpAndFail(t *testing.T) {
	ref := hangar.TreeRef{
		Scope:      "builds",
		Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Generation: 7,
	}
	receipt, _ := json.Marshal(ref)
	for _, signal := range []string{"HUP", "INT", "TERM"} {
		for _, phase := range []string{"wget", "retry"} {
			t.Run(signal+" during "+phase, func(t *testing.T) {
				fixture := hangarShellFixture{sequence: "signal", signal: signal, receipt: receipt, rootMode: 0555, receiptMode: 0444}
				if phase == "retry" {
					fixture.sequence = "none,204"
					fixture.signalSleep = true
				}
				result := runHangarInitShell(t, ref, fixture)
				if result.err == nil {
					t.Fatal("signal must terminate the init script nonzero")
				}
				if len(result.leftovers) != 0 {
					t.Fatalf("signal left private temporary directories: %v", result.leftovers)
				}
			})
		}
	}
}

type hangarShellFixture struct {
	sequence    string
	body        string
	wgetExit    string
	receipt     []byte
	receiptKind string
	rootKind    string
	rootMode    os.FileMode
	receiptMode os.FileMode
	signalSleep bool
	signal      string
}

type hangarShellResult struct {
	err       error
	output    string
	attempts  int
	leftovers []string
}

func runHangarInitShell(t *testing.T, ref hangar.TreeRef, fixture hangarShellFixture) hangarShellResult {
	t.Helper()
	key := []byte("0123456789abcdef0123456789abcdef")
	signer, err := hangar.NewWarrantSigner(key, hangar.MaxWarrantTTL, func() time.Time {
		return time.Unix(1_800_000_000, 0).UTC()
	})
	if err != nil {
		t.Fatalf("new warrant signer: %v", err)
	}
	cfg := testDaemonConfig()
	cfg.HangarEnabled = true
	cfg.HangarWarrantSigner = signer
	b := NewDaemonSetBackend(cfg, nil, nil)
	inputs := []runtime.Input{{HangarTree: &ref, DestinationPath: "/work/exact"}}
	mounts := []corev1.VolumeMount{{Name: "input-0", MountPath: "/work/exact", ReadOnly: true}}
	volumes := []corev1.Volume{b.StepVolume("input-0", "task-handle", "input-0")}
	inits, err := b.BuildFetchInitContainers("task-handle", inputs, volumes, mounts)
	if err != nil || len(inits) != 1 {
		t.Fatalf("build strict init: containers=%d err=%v", len(inits), err)
	}

	mountBase := filepath.Join(t.TempDir(), "hangar-inputs")
	root := filepath.Join(mountBase, "input-0")
	rootTarget := root
	if fixture.rootKind == "symlink" {
		rootTarget = t.TempDir()
		if err := os.MkdirAll(mountBase, 0755); err != nil {
			t.Fatalf("make receipt mount base: %v", err)
		}
		if err := os.Symlink(rootTarget, root); err != nil {
			t.Fatalf("symlink receipt root: %v", err)
		}
	} else if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatalf("make receipt root: %v", err)
	}
	receiptPath := filepath.Join(root, ".hangar-materialized")
	switch fixture.receiptKind {
	case "missing":
	case "symlink":
		target := filepath.Join(t.TempDir(), "receipt-target")
		if err := os.WriteFile(target, fixture.receipt, 0444); err != nil {
			t.Fatalf("write receipt target: %v", err)
		}
		if err := os.Symlink(target, receiptPath); err != nil {
			t.Fatalf("symlink receipt: %v", err)
		}
	default:
		if err := os.WriteFile(receiptPath, fixture.receipt, fixture.receiptMode); err != nil {
			t.Fatalf("write receipt: %v", err)
		}
		if err := os.Chmod(receiptPath, fixture.receiptMode); err != nil {
			t.Fatalf("chmod receipt: %v", err)
		}
	}
	if err := os.Chmod(rootTarget, fixture.rootMode); err != nil {
		t.Fatalf("chmod receipt root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(rootTarget, 0755) })

	stateDir := t.TempDir()
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "wget"), `#!/bin/sh
OUTPUT=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -O) OUTPUT=$2; shift 2 ;;
    *) shift ;;
  esac
done
ATTEMPTS_FILE="$TEST_STATE/attempts"
ATTEMPT=0
if [ -f "$ATTEMPTS_FILE" ]; then ATTEMPT=$(cat "$ATTEMPTS_FILE"); fi
ATTEMPT=$((ATTEMPT + 1))
printf '%s' "$ATTEMPT" >"$ATTEMPTS_FILE"
OUTCOME=$(printf '%s' "$TEST_SEQUENCE" | cut -d, -f"$ATTEMPT")
case "$OUTCOME" in
  none) : >"$OUTPUT"; exit 1 ;;
  signal) kill -"$TEST_SIGNAL" "$PPID"; /bin/sleep 0.1; exit 1 ;;
  503) printf '  HTTP/1.1 503 Service Unavailable\n' >&2; : >"$OUTPUT"; exit 1 ;;
  *) printf '  HTTP/1.1 %s Result\n' "$OUTCOME" >&2; printf '%s' "$TEST_BODY" >"$OUTPUT"; exit "${TEST_WGET_EXIT:-0}" ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "sleep"), `#!/bin/sh
if [ "${TEST_SIGNAL_SLEEP:-}" = 1 ]; then kill -"$TEST_SIGNAL" "$PPID"; /bin/sleep 0.1; fi
exit 0
`)

	scriptTmp := t.TempDir()
	script := strings.ReplaceAll(inits[0].Command[2], "/hangar-inputs", mountBase)
	// A non-interactive POSIX shell cannot trap a signal that was ignored when
	// it started, and the CI task tree starts with SIGHUP ignored (reproducible
	// locally with `trap '' HUP` around go test). Catching it here for the
	// duration means the child is exec'd with the default disposition, so the
	// script's `trap on_signal 1` is honoured the way it is in a real pod.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	cmd := exec.Command("/bin/sh", "-c", script)
	signalSleep := ""
	if fixture.signalSleep {
		signalSleep = "1"
	}
	cmd.Env = append(os.Environ(),
		"HOST_IP=127.0.0.1",
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"TMPDIR="+scriptTmp,
		"TEST_STATE="+stateDir,
		"TEST_SEQUENCE="+fixture.sequence,
		"TEST_BODY="+fixture.body,
		"TEST_WGET_EXIT="+fixture.wgetExit,
		"TEST_SIGNAL_SLEEP="+signalSleep,
		"TEST_SIGNAL="+fixture.signal,
	)
	output, runErr := cmd.CombinedOutput()
	attemptBytes, _ := os.ReadFile(filepath.Join(stateDir, "attempts"))
	attempts, _ := strconv.Atoi(string(attemptBytes))
	leftovers, _ := filepath.Glob(filepath.Join(scriptTmp, "hangar-materialize.*"))
	return hangarShellResult{err: runErr, output: string(output), attempts: attempts, leftovers: leftovers}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func TestDaemonSetBackend_BuildFetchInitContainers_RejectsDisabledOrUnsignableHangar(t *testing.T) {
	ref := hangar.TreeRef{
		Scope:      "builds",
		Digest:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Generation: 1,
	}
	inputs := []runtime.Input{{HangarTree: &ref, DestinationPath: "/work/exact"}}
	mounts := []corev1.VolumeMount{{Name: "input-0", MountPath: "/work/exact", ReadOnly: true}}

	for name, cfg := range map[string]Config{
		"disabled":       testDaemonConfig(),
		"missing signer": func() Config { c := testDaemonConfig(); c.HangarEnabled = true; return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			b := NewDaemonSetBackend(cfg, nil, nil)
			volumes := []corev1.Volume{b.StepVolume("input-0", "task-handle", "input-0")}
			if _, err := b.BuildFetchInitContainers("task-handle", inputs, volumes, mounts); err == nil {
				t.Fatal("expected strict input construction to fail closed")
			}
		})
	}
}

func assertContainerMountsResolve(t *testing.T, containers []corev1.Container, volumes []corev1.Volume) {
	t.Helper()
	declared := make(map[string]struct{}, len(volumes))
	for _, volume := range volumes {
		declared[volume.Name] = struct{}{}
	}
	for _, container := range containers {
		for _, mount := range container.VolumeMounts {
			if _, found := declared[mount.Name]; !found {
				t.Errorf("container %q mount %q has no Pod volume", container.Name, mount.Name)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// BuildCleanupInitContainer
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_BuildCleanupInitContainer_Reused(t *testing.T) {
	b := testBackend(nil)
	ic, _ := b.BuildCleanupInitContainer("handle-1", db.ContainerTypeTask, true)

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
	ic, _ := b.BuildCleanupInitContainer("handle-1", db.ContainerTypeTask, false)
	if ic != nil {
		t.Errorf("expected nil for fresh container, got %+v", ic)
	}
}

func TestDaemonSetBackend_BuildCleanupInitContainer_Check(t *testing.T) {
	b := testBackend(nil)
	ic, _ := b.BuildCleanupInitContainer("handle-1", db.ContainerTypeCheck, true)
	if ic != nil {
		t.Errorf("expected nil for check container, got %+v", ic)
	}
}

// ---------------------------------------------------------------------------
// BuildAffinity
// ---------------------------------------------------------------------------

func TestDaemonSetBackend_BuildAffinity_HardLabel(t *testing.T) {
	b := testBackend(nil)
	affinity := b.BuildAffinity(nil, nil)

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
		{Artifact: constructionArtifact("vol-a", "worker-1"), DestinationPath: "/tmp/input"},
	}

	affinity := b.BuildAffinity(inputs, nil)
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

	affinity := b.BuildAffinity(nil, nil)
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

// Construction-only identity check: neither configuration makes an API or
// daemon request. Runtime discovery and reads live in artifact-recording.feature.
func TestDaemonSetBackend_WrapVolumeForLookup_PreservesDaemonClient(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "unconfigured"
		if configured {
			name = "configured"
		}
		t.Run(name, func(t *testing.T) {
			cfg := testDaemonConfig()
			clientset := volumeConstructionExecutor().clientset
			var client *DaemonClient
			if configured {
				client = NewDaemonClient(lagertest.NewTestLogger("lookup-construction"), clientset,
					cfg.Namespace, cfg.ArtifactDaemonService, cfg.ArtifactDaemonPort, nil)
			}
			b := NewDaemonSetBackend(cfg, NewArtifactLocator(), NewNodeIPResolver(clientset))
			if client != nil {
				b.SetDaemonClient(client)
			}
			vol := b.WrapVolumeForLookup(context.Background(), "artifact-key-1", "handle-1", "worker-1", nil)
			dsv, ok := vol.(*DaemonSetVolume)
			if !ok || dsv == nil {
				t.Fatalf("expected daemon volume, got %T", vol)
			}
			if dsv.daemonClient != client {
				t.Fatalf("daemon client identity changed: got %p want %p", dsv.daemonClient, client)
			}
		})
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

// ---------------------------------------------------------------------------
// The cleanup init container's script, RUN
// ---------------------------------------------------------------------------

// A handle reaches `rm -rf` in the one container that mounts the whole managed
// hostPath read-write, and it used to reach it unquoted.
//
// This does not read the script for quotes; it RUNS it, with rm, mkdir and
// wget replaced by recorders on PATH, and asks what the shell actually did. A
// quoting rule asserted by grepping for apostrophes is a rule that passes for
// the wrong reason the first time somebody reformats the string.
//
// The payload is the shape that matters: it closes the word, runs a command of
// its own, and reopens it, so a script that interpolates it unquoted stays
// syntactically valid and does something extra. Under correct quoting the
// shell sees ONE word -- a path with punctuation in it -- and nothing else
// runs.
func TestTheCleanupScriptTreatsAHandleAsOneWord(t *testing.T) {
	// TWO PAYLOADS, because the two contexts break differently and a single
	// one proves only half. Bare metacharacters escape an UNQUOTED
	// interpolation; an embedded apostrophe escapes a naively quoted one. A
	// fix that only wraps the word in quotes passes the first and fails the
	// second, which is why both run against both arms.
	payloads := map[string]func(marker string) string{
		"bare metacharacters":    func(marker string) string { return "h1; touch " + marker + "; echo x" },
		"an embedded apostrophe": func(marker string) string { return "h1'; touch " + marker + "; :'x" },
		// The vector for the DOUBLE-quoted contexts -- the URL the probe
		// fetches and the messages it echoes. `;` is literal inside double
		// quotes and a command substitution is not.
		"a command substitution": func(marker string) string { return "h1$(touch " + marker + ")x" },
	}

	cases := map[string]bool{"with no output plane": false, "with the output plane": true}
	for name, plane := range cases {
		for payloadName, payload := range payloads {
			t.Run(name+", "+payloadName, func(t *testing.T) {
				runOneCleanupScriptCase(t, plane, payload)
			})
		}
	}
}

func runOneCleanupScriptCase(t *testing.T, plane bool, payload func(marker string) string) {
	t.Helper()

	{
		{
			marker := filepath.Join(t.TempDir(), "executed")
			handle := payload(marker)

			backend := testBackend(nil)
			backend.config.OutputPlaneEnabled = plane
			container, err := backend.BuildCleanupInitContainer(handle, db.ContainerTypeTask, true)
			if err != nil {
				t.Fatalf("building the cleanup init container: %v", err)
			}
			if container == nil {
				t.Fatal("no cleanup init container for a reused task")
			}

			calls := runScriptWithRecordedTools(t, container.Command[2])

			if _, err := os.Stat(marker); err == nil {
				t.Errorf("a handle carrying a shell command RAN it, in the one container that "+
					"mounts the managed hostPath read-write:\n%s", container.Command[2])
			}
			removals := calls["rm"]
			if plane {
				// The ledger stub answers nothing, which is the refusal arm:
				// an unreadable ledger is not an empty one, so nothing is
				// removed at all.
				if len(removals) != 0 {
					t.Errorf("the ledger said nothing and %d removal(s) happened anyway: %v",
						len(removals), removals)
				}

				return
			}
			if len(removals) != 1 {
				t.Fatalf("expected exactly one rm, got %d: %v", len(removals), removals)
			}
			if want := []string{"-rf", filepath.Join(ArtifactMountPath, "steps", handle)}; !slicesEqual(removals[0], want) {
				t.Errorf("rm was called with %q; the target is one word, the step directory "+
					"whose name is the handle verbatim, and nothing else", removals[0])
			}
		}
	}
}

// runScriptWithRecordedTools runs one `sh -c` script with rm, mkdir and wget
// replaced by recorders, and returns each tool's argv per invocation.
//
// The recorders are what make this safe to run: the script's real target is
// /artifacts/steps/<handle> on a node, and nothing here touches it.
func runScriptWithRecordedTools(t *testing.T, script string) map[string][][]string {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	for _, tool := range []string{"rm", "mkdir", "wget"} {
		recorder := "#!/bin/sh\nprintf '%s' \"" + tool + "\" >> " + log +
			"\nfor argument in \"$@\"; do printf '\\037%s' \"$argument\" >> " + log +
			"; done\nprintf '\\036' >> " + log + "\nexit 0\n"
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(recorder), 0o755); err != nil {
			t.Fatalf("writing the %s recorder: %v", tool, err)
		}
	}

	command := exec.Command("sh", "-c", script)
	command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "HOST_IP=127.0.0.1")
	output, err := command.CombinedOutput()
	if err != nil {
		// A refusal arm exits non-zero on purpose; the assertions are about
		// what ran, not about the exit code.
		t.Logf("the script exited with %v; output: %s", err, output)
	}

	recorded, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading the recorded calls: %v", err)
	}

	calls := map[string][][]string{}
	for _, line := range strings.Split(string(recorded), "\036") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\037")
		calls[fields[0]] = append(calls[fields[0]], fields[1:])
	}

	return calls
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// ---------------------------------------------------------------------------
// What the fetch-inputs script puts in a build log when the daemon says no
// ---------------------------------------------------------------------------

// runFetchScript runs the emitted init-container script with wget and sleep
// stubbed, the way the node would run it, and returns its combined output.
//
// The wget stub is BusyBox's behaviour on a non-2xx: it prints the status LINE
// and discards the body. That is not a simplification — it is the constraint
// the whole diagnosis has to live inside, and a stub that handed the script a
// body would test a daemon reply the pod can never see.
func runFetchScript(t *testing.T, script, status, body string) string {
	t.Helper()

	rc := "1"
	if strings.HasPrefix(status, "2") {
		rc = "0"
	}
	prelude := `
ATTEMPTS=0
wget() {
  ATTEMPTS=$((ATTEMPTS + 1))
  echo "wget-attempts=${ATTEMPTS}" >&2
  if [ "${STUB_RC}" = "0" ]; then printf '%s' "${STUB_BODY}"; return 0; fi
  echo "wget: server returned error: HTTP/1.1 ${STUB_STATUS}" >&2
  return 1
}
sleep() { :; }
`
	cmd := exec.Command("sh", "-c", prelude+script)
	cmd.Env = append(os.Environ(),
		"HOST_IP=10.0.0.1",
		"STUB_RC="+rc,
		"STUB_STATUS="+status,
		"STUB_BODY="+body,
	)
	out, err := cmd.CombinedOutput()
	if err == nil && rc != "0" {
		t.Fatalf("script succeeded although the daemon refused: %s", out)
	}
	return string(out)
}

// testArtifact is the least runtime.Artifact an input can name: the fetch
// script only reads its handle.
type testArtifact struct {
	handle string
}

func (a *testArtifact) Handle() string { return a.handle }
func (a *testArtifact) Source() string { return "worker-1" }
func (a *testArtifact) StreamOut(context.Context, string, compression.Compression) (io.ReadCloser, error) {
	return nil, nil
}

func fetchInputsScript(t *testing.T, keys ...string) string {
	t.Helper()

	b := testBackend(nil)
	var inputs []runtime.Input
	var mounts []corev1.VolumeMount
	var volumes []corev1.Volume
	for i, key := range keys {
		dest := "/tmp/input-" + strconv.Itoa(i)
		name := "input-" + strconv.Itoa(i)
		inputs = append(inputs, runtime.Input{Artifact: &testArtifact{handle: key}, DestinationPath: dest})
		mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: dest})
		volumes = append(volumes, b.StepVolume(name, "consumer-handle", name))
	}

	inits, err := b.BuildFetchInitContainers("consumer-handle", inputs, volumes, mounts)
	if err != nil {
		t.Fatalf("BuildFetchInitContainers: %v", err)
	}
	if len(inits) != 1 {
		t.Fatalf("expected 1 fetch init container, got %d", len(inits))
	}
	argv := inits[0].Command
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		t.Fatalf("fetch init container is not a shell script: %v", argv)
	}
	return argv[2]
}

// A 4xx is the daemon's answer about these artifacts, not a symptom of a
// daemon that is not up yet. Retrying it ten times spent twenty seconds and
// printed nine identical lines that said nothing; and because BusyBox wget
// throws the body away, the ONLY record of which artifacts were wanted is the
// one the script writes itself.
//
// Both 4xx answers this endpoint gives are covered. 404 is "not on this node
// or any peer"; 422 is "here, and refused" — an absolute symlink target, say.
// Neither changes on a retry, and the status line is all the pod ever sees of
// either, so the script has to name the keys itself in both cases.
func TestFetchInputsScript_NamesTheArtifactsAndDoesNotRetryARefusal(t *testing.T) {
	for _, status := range []string{"404 Not Found", "422 Unprocessable Entity"} {
		t.Run(status, func(t *testing.T) {
			script := fetchInputsScript(t, "rc-deadbeef", "producer-handle/out")

			out := runFetchScript(t, script, status, "")

			if strings.Count(out, "wget-attempts=") != 1 {
				t.Errorf("a %s was retried; the daemon's answer will not change:\n%s", status, out)
			}
			for _, key := range []string{"rc-deadbeef", "producer-handle/out"} {
				if !strings.Contains(out, key) {
					t.Errorf("the failure does not name artifact %q:\n%s", key, out)
				}
			}
			if !strings.Contains(out, status) {
				t.Errorf("the failure does not carry the daemon's status:\n%s", out)
			}
		})
	}
}

// A 5xx may well be a daemon that is still coming up, so it keeps its
// retries — but every attempt now says what the daemon actually returned
// instead of "attempt N/10 failed".
func TestFetchInputsScript_RetriesAServerErrorAndSaysWhatItWas(t *testing.T) {
	script := fetchInputsScript(t, "rc-deadbeef")

	out := runFetchScript(t, script, "500 Internal Server Error", "")

	if strings.Count(out, "wget-attempts=") != 10 {
		t.Errorf("expected the 5xx to keep its 10 retries, got:\n%s", out)
	}
	if strings.Count(out, "500 Internal Server Error") < 2 {
		t.Errorf("the per-attempt lines do not carry the daemon's answer:\n%s", out)
	}
	if !strings.Contains(out, "rc-deadbeef") {
		t.Errorf("the failure does not name the artifact:\n%s", out)
	}
}
