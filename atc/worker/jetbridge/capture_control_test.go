package jetbridge

// The capture pod's Go half: the shell the control and cleanup init containers
// actually run, and the proof that an ordinary pipeline's pod did not move.
//
// What a capture pod SAYS is brine's -- features/hangar-capture-pod.feature
// asserts the init ordering, the credential placement, the Downward API field
// paths and the mount count over the Pod the cluster hands back. What stays
// here is what brine cannot say: generated shell text, and a byte-for-byte
// comparison of two pods built under two configurations, which is a diff rather
// than a sentence.

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

func capturePodConfig(outputPlane bool) Config {
	return Config{
		Namespace:              "test-ns",
		ArtifactDaemonHostPath: "/var/concourse/artifacts",
		ArtifactHelperImage:    "busybox:latest",
		ArtifactDaemonPort:     7780,
		OutputPlaneEnabled:     outputPlane,
		OutputDaemonPort:       7781,
	}
}

func capturingContainer(t *testing.T, cfg Config, reused bool, control *runtime.ExecutionControl) *Container {
	t.Helper()

	spec := runtime.ContainerSpec{
		Dir:              "/tmp/build/task",
		Type:             db.ContainerTypeTask,
		ImageSpec:        runtime.ImageSpec{ImageURL: "busybox"},
		Outputs:          runtime.OutputPaths{"result": "/tmp/build/result"},
		ExecutionControl: control,
	}

	return &Container{
		handle:         "capture-handle",
		podName:        "capture-pod",
		metadata:       db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec:  spec,
		config:         cfg,
		properties:     map[string]string{},
		reused:         reused,
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
	}
}

func admittedCapture() *runtime.ExecutionControl {
	control := &runtime.ExecutionControl{
		Version:         runtime.ExecutionControlVersion,
		Phase:           runtime.ControlPhaseAdmitted,
		Identity:        executioncontrol.Identity{ExecutionID: "11111111-1111-4111-8111-111111111111", Fence: 3},
		ActivationEpoch: 9,
		Endpoint:        "",
		Capability:      "base-capability",
	}
	_ = control.SelectCapture(runtime.DurableOutputCapture{
		Version:            runtime.DurableOutputCaptureVersion,
		Identity:           control.Identity,
		ActivationEpoch:    control.ActivationEpoch,
		HandoffID:          hangaroutput.HandoffID("22222222-2222-4222-8222-222222222222"),
		SourceLeaseID:      hangaroutput.SourceLeaseID("33333333-3333-4333-8333-333333333333"),
		Output:             "result",
		SourceControlGrant: "source-control-grant",
		CaptureDeadline:    time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC),
	})
	// The envelope needs an endpoint to validate; the control init falls back
	// to the Downward API host IP when it is empty, which is the deployed
	// shape, so both are exercised.
	control.Endpoint = "http://127.0.0.1:7781"

	return control
}

// A pod whose step selected no capture is the pod this runtime built before
// the output plane existed, and this is a DIFF rather than a claim about it.
//
// The two pods come from the same Container code under two configurations, so
// a field that moved shows up as a field rather than as a count somebody
// forgot to update. Req 59 and AC 20.
func TestTheOutputPlaneChangesNoOrdinaryPodWhenNothingIsCaptured(t *testing.T) {
	for _, reused := range []bool{false, true} {
		off, err := capturingContainer(t, capturePodConfig(false), reused, nil).
			buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
		if err != nil {
			t.Fatalf("reused=%v: building with the output plane off: %v", reused, err)
		}
		on, err := capturingContainer(t, capturePodConfig(true), reused, nil).
			buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
		if err != nil {
			t.Fatalf("reused=%v: building with the output plane on: %v", reused, err)
		}

		if !reused {
			// Nothing this pod does can touch a held source: there is no
			// stale workspace to remove. So the two are the same Pod.
			if !reflect.DeepEqual(off.Spec, on.Spec) {
				t.Errorf("enabling the output plane changed the pod of a step that captures "+
					"nothing:\n off: %+v\n  on: %+v", off.Spec, on.Spec)
			}

			continue
		}

		// A REUSED handle is the one ordinary pod that legitimately differs,
		// and it differs in exactly one place: the cleanup init's command,
		// which now asks the ledger before removing anything. Everything else
		// -- containers, volumes, mounts, affinity, security -- is equal, and
		// the comparison says so by zeroing only that field.
		if len(off.Spec.InitContainers) != 1 || len(on.Spec.InitContainers) != 1 {
			t.Fatalf("reused pods carry %d and %d init containers; this comparison assumes one",
				len(off.Spec.InitContainers), len(on.Spec.InitContainers))
		}
		offCleanup, onCleanup := off.Spec.InitContainers[0], on.Spec.InitContainers[0]
		if offCleanup.Name != "cleanup-stale" || onCleanup.Name != "cleanup-stale" {
			t.Fatalf("the reused pod's init container is %q / %q", offCleanup.Name, onCleanup.Name)
		}
		if strings.Join(offCleanup.Command, " ") == strings.Join(onCleanup.Command, " ") {
			t.Error("the cleanup init runs the same command with the output plane on; it is the " +
				"one destructive path in a Pod that reached no ledger guard, and enabling the " +
				"plane is what gives it one")
		}
		off.Spec.InitContainers[0].Command = nil
		on.Spec.InitContainers[0].Command = nil
		if !reflect.DeepEqual(off.Spec, on.Spec) {
			t.Errorf("enabling the output plane changed a reused ordinary pod somewhere OTHER "+
				"than the cleanup command:\n off: %+v\n  on: %+v", off.Spec, on.Spec)
		}
	}
}

// The cleanup init's three arms, read off the shell it will actually run.
//
// `rm -rf` over a step directory is the most destructive thing in a Pod, and
// until now it was the only destructive path on a node that consulted nothing:
// DELETE /artifacts, the sweeper, a stream-in replacement and a registry remap
// all go through the classifier. The arms mirror the classifier's own --
// unmanaged proceeds, held refuses, anything else refuses -- because an
// unreadable ledger is not an empty one.
func TestTheCleanupInitAsksTheLedgerBeforeRemovingAnything(t *testing.T) {
	backend := NewDaemonSetBackend(capturePodConfig(true), nil, nil)
	cleanup, err := backend.BuildCleanupInitContainer("reused-handle", db.ContainerTypeTask, true)
	if err != nil {
		t.Fatalf("building the cleanup init: %v", err)
	}
	if cleanup == nil {
		t.Fatal("a reused handle got no cleanup init container")
	}
	script := strings.Join(cleanup.Command, " ")

	for _, want := range []string{
		"/capture-held/steps/reused-handle", // it asks
		`"class":"unmanaged"`,               // and only then removes
		`"class":"held"`,                    // a held source is a refusal
		"rm -rf",
		"exit 1",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the cleanup script does not contain %q:\n%s", want, script)
		}
	}
	// The removal is INSIDE the unmanaged arm. A script that asked and then
	// removed regardless would contain every string above and mean nothing.
	unmanagedArm := script[strings.Index(script, `*'"class":"unmanaged"'*)`):]
	held := strings.Index(unmanagedArm, `*'"class":"held"'*)`)
	if held < 0 {
		t.Fatal("the held arm does not follow the unmanaged arm")
	}
	if !strings.Contains(unmanagedArm[:held], "rm -rf") {
		t.Error("the removal is not inside the unmanaged arm; the script asks the ledger and " +
			"then removes whatever the answer was")
	}
	if strings.Contains(unmanagedArm[held:], "rm -rf") {
		t.Error("the held or default arm still removes the source")
	}

	// With the plane off there is nothing to ask and the script is the one
	// this runtime has always emitted.
	plain, err := NewDaemonSetBackend(capturePodConfig(false), nil, nil).
		BuildCleanupInitContainer("reused-handle", db.ContainerTypeTask, true)
	if err != nil {
		t.Fatalf("building the plain cleanup init: %v", err)
	}
	if strings.Contains(strings.Join(plain.Command, " "), "capture-held") {
		t.Error("a deployment with no output plane emits the ledger probe; there is no ledger " +
			"to probe and Req 59 says the ordinary path is unchanged")
	}
}

// Both generated scripts parse as POSIX sh.
//
// The host's /bin/sh is not evidence that BusyBox ash will run them -- that is
// a Linux/K3s flow under a build tag -- but a script that does not PARSE is a
// pod that fails on a syntax error at the worst possible moment, and `sh -n`
// catches that here rather than in a build log.
func TestTheGeneratedCaptureScriptsAreSyntacticallyPOSIX(t *testing.T) {
	backend := NewDaemonSetBackend(capturePodConfig(true), nil, nil)
	cleanup, err := backend.BuildCleanupInitContainer("reused-handle", db.ContainerTypeTask, true)
	if err != nil {
		t.Fatalf("building the cleanup init: %v", err)
	}
	control := capturingContainer(t, capturePodConfig(true), false, admittedCapture()).
		buildCaptureControlInitContainer()
	if control == nil {
		t.Fatal("a capture-selected container built no control init")
	}

	for name, script := range map[string]string{
		"cleanup-stale":          cleanup.Command[2],
		captureControlInitName:   control.Command[2],
		"supervisor (unchanged)": supervisorCommand("proc", runtime.ProcessSpec{Path: "true"})[2],
	} {
		path := filepath.Join(t.TempDir(), "script.sh")
		if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
			t.Fatalf("%s: writing the script: %v", name, err)
		}
		if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse as POSIX sh: %v\n%s\n%s", name, err, out, script)
		}
	}
}

// The header the control init presents is the header the daemon reads.
//
// cmd/hangar-output-daemon is package main, so the constant cannot be
// imported. Reading its source is the honest alternative to writing the string
// twice and hoping: a rename on either side fails here.
func TestTheControlInitPresentsTheHeaderTheOutputDaemonReads(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "cmd", "hangar-output-daemon", "routes.go"))
	if err != nil {
		t.Fatalf("reading the daemon's route table: %v", err)
	}
	want := `const CapabilityHeader = "` + CapabilityHeaderName + `"`
	if !strings.Contains(string(source), want) {
		t.Errorf("cmd/hangar-output-daemon/routes.go does not declare %s; the control init "+
			"presents a header the daemon does not read", want)
	}

	control := capturingContainer(t, capturePodConfig(true), false, admittedCapture()).
		buildCaptureControlInitContainer()
	if !strings.Contains(control.Command[2], CapabilityHeaderName+": ") {
		t.Errorf("the control init's script never sets %s", CapabilityHeaderName)
	}
}

// A capture-selected pod is scheduled onto a node attested for BOTH facets.
//
// The scenarios that read this off a Pod are Phase 8's scheduling block, which
// is where a worker can be told its facet is off; the affinity itself lands
// here because the Phase 4 checkpoint asks that every capture-selected
// execution reach a fresh ready cohort, and a pod that landed on a node with no
// output daemon could never have its hold acknowledged.
func TestACaptureSelectedPodRequiresBothReadyLabelsAndAnOrdinaryOneRequiresNeither(t *testing.T) {
	labels := func(pod *corev1.Pod) map[string]bool {
		found := map[string]bool{}
		affinity := pod.Spec.Affinity
		if affinity == nil || affinity.NodeAffinity == nil ||
			affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			return found
		}
		for _, term := range affinity.NodeAffinity.
			RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			for _, expr := range term.MatchExpressions {
				found[expr.Key] = true
			}
		}

		return found
	}

	ordinary, err := capturingContainer(t, capturePodConfig(true), false, nil).
		buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
	if err != nil {
		t.Fatalf("building the ordinary pod: %v", err)
	}
	capture, err := capturingContainer(t, capturePodConfig(true), false, admittedCapture()).
		buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
	if err != nil {
		t.Fatalf("building the capture pod: %v", err)
	}

	// The control first: the ordinary pod requires the artifact cache and
	// neither output-plane label. Without this line, "requires both" would
	// pass on a runtime that requires them of everything.
	ordinaryLabels := labels(ordinary)
	if !ordinaryLabels["concourse.dev/artifact-cache"] {
		t.Error("the ordinary pod stopped requiring the artifact cache")
	}
	for _, label := range []string{executioncontrol.ReadyLabel, hangaroutput.ReadyLabel} {
		if ordinaryLabels[label] {
			t.Errorf("an ordinary pod requires %s; a base-only cohort is a real deployment and "+
				"an ordinary pipeline must schedule on it", label)
		}
	}

	captureLabels := labels(capture)
	for _, label := range []string{executioncontrol.ReadyLabel, hangaroutput.ReadyLabel} {
		if !captureLabels[label] {
			t.Errorf("a capture-selected pod does not require %s. The two labels are separate "+
				"because a base-only cohort is attested for exact control without having an "+
				"output bucket, and one label would make those the same claim", label)
		}
	}
}
