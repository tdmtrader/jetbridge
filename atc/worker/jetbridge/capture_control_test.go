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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
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

// capturingSpec is the one description of a capture-selected step, so that a
// spec built here and a spec built in the ginkgo file cannot drift into two
// different meanings of "capture-selected".
func capturingSpec(control *runtime.ExecutionControl) runtime.ContainerSpec {
	return runtime.ContainerSpec{
		Dir:              "/tmp/build/task",
		Type:             db.ContainerTypeTask,
		ImageSpec:        runtime.ImageSpec{ImageURL: "busybox"},
		Outputs:          runtime.OutputPaths{"result": "/tmp/build/result"},
		ExecutionControl: control,
	}
}

func capturingContainer(t *testing.T, cfg Config, reused bool, control *runtime.ExecutionControl) *Container {
	t.Helper()

	spec := capturingSpec(control)

	return &Container{
		handle:         "capture-handle",
		podName:        "capture-pod",
		metadata:       db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec:  spec,
		config:         cfg,
		properties:     map[string]string{},
		reused:         reused,
		storageBackend: NewDaemonSetBackend(cfg, nil, nil, nil),
	}
}

// testReservingNode is the Kubernetes node whose daemon issued the reservation
// below. It is the node NAME; testReservedIncarnation carries that node's UID.
const testReservingNode = "kube-node-a"

// testReservedIncarnation is what `reserve-incarnation` answered for this
// execution, as it would come off the wire.
func testReservedIncarnation() hangaroutput.SourceIncarnation {
	return hangaroutput.SourceIncarnation{
		ExecutionID:      "11111111-1111-4111-8111-111111111111",
		NodeUID:          "node-1",
		HandleGeneration: 4,
		Output:           "result",
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
		SourceHoldID:       hangaroutput.SourceHoldID("33333333-3333-4333-8333-333333333333"),
		Output:             "result",
		SourceControlGrant: "source-control-grant",
		CaptureDeadline:    time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC),

		// The daemon's answer, repeated. Nothing here composes it: the
		// generation is that node's monotonic ledger sequence and no caller can
		// choose one.
		ReservedIncarnation: testReservedIncarnation(),
		ReservedDirectory:   testReservedIncarnation().Directory(),

		// The node whose daemon answered. A reservation is a directory on that
		// node's disk, so the producing Pod is pinned to it.
		ReservingNode: testReservingNode,
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
	backend := NewDaemonSetBackend(capturePodConfig(true), nil, nil, nil)
	cleanup, err := backend.BuildCleanupInitContainer("reused-handle", db.ContainerTypeTask, true)
	if err != nil {
		t.Fatalf("building the cleanup init: %v", err)
	}
	if cleanup == nil {
		t.Fatal("a reused handle got no cleanup init container")
	}
	script := strings.Join(cleanup.Command, " ")

	for _, want := range []string{
		// It asks, about THIS handle. The handle reaches the URL through a
		// shell variable rather than by interpolation, because it used to be
		// interpolated into `rm -rf`, into this URL and into the messages
		// below with nothing between it and the shell's parser -- see
		// TestTheCleanupScriptTreatsAHandleAsOneWord, which runs the script.
		// So the two halves are asserted separately: the handle is bound once,
		// as one quoted word, and the question is asked about what it is bound
		// to.
		"HANDLE='reused-handle'",
		"/capture-held/steps/${HANDLE}",
		`"class":"unmanaged"`, // and only then removes
		`"class":"held"`,      // a held source is a refusal
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
	plain, err := NewDaemonSetBackend(capturePodConfig(false), nil, nil, nil).
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
	backend := NewDaemonSetBackend(capturePodConfig(true), nil, nil, nil)
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

// The captured output's volume IS the reserved incarnation.
//
// This is the pass's whole point stated where it can fail. Before it, the
// selected output mounted `steps/<handle>/result` -- the ordinary step volume
// -- while the daemon's hold protected `steps/<execution>.<generation>/result`,
// a sibling directory. The producer wrote into one and the capture sealed the
// other, so every path-keyed guard was answering about bytes nobody wrote.
//
// The controls are in the same test and asserted first: the step's OTHER
// volumes are unchanged, so this cannot pass on a builder that has started
// pointing everything at the incarnation.
func TestTheCaptureSelectedOutputMountsTheReservedIncarnation(t *testing.T) {
	control := admittedCapture()
	control.Capture.Output = "result"

	container := capturingContainer(t, capturePodConfig(true), false, control)
	container.containerSpec.Outputs = runtime.OutputPaths{
		"result": "/tmp/build/result",
		"report": "/tmp/build/report",
	}

	pod, err := container.buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
	if err != nil {
		t.Fatalf("building the capture pod: %v", err)
	}

	byMountPath := map[string]string{}
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		byMountPath[mount.MountPath] = mount.Name
	}
	hostPathOf := func(name string) string {
		for _, volume := range pod.Spec.Volumes {
			if volume.Name == name {
				if volume.HostPath == nil {
					return "(not a hostPath)"
				}

				return volume.HostPath.Path
			}
		}

		return "(no such volume)"
	}

	// The controls, first. The working directory and the output this step did
	// NOT select still resolve under the step's own handle.
	for path, want := range map[string]string{
		"/tmp/build/task":   "/var/concourse/artifacts/steps/capture-handle/dir",
		"/tmp/build/report": "/var/concourse/artifacts/steps/capture-handle/report",
	} {
		name, mounted := byMountPath[path]
		if !mounted {
			t.Fatalf("the pod mounts nothing at %s", path)
		}
		if got := hostPathOf(name); got != want {
			t.Errorf("%s resolves to %s and an unselected step volume is unchanged at %s",
				path, got, want)
		}
	}

	// And the selected output, which is the reservation.
	name, mounted := byMountPath["/tmp/build/result"]
	if !mounted {
		t.Fatal("the pod mounts nothing at the captured output's path")
	}
	want := "/var/concourse/artifacts/steps/" + testReservedIncarnation().Directory()
	if got := hostPathOf(name); got != want {
		t.Errorf("the captured output resolves to %s; the daemon reserved %s, and a producer "+
			"writing anywhere else is a hold over bytes nobody wrote", got, want)
	}

	// The control init's hold names the same location, because a hold over an
	// incarnation the Pod did not mount protects nothing.
	var initEnv map[string]string
	for _, container := range pod.Spec.InitContainers {
		if container.Name != captureControlInitName {
			continue
		}
		initEnv = map[string]string{}
		for _, env := range container.Env {
			initEnv[env.Name] = env.Value
		}
	}
	if initEnv == nil {
		t.Fatal("the capture pod has no control init")
	}
	if initEnv[captureEnvIncarnationGeneration] != "4" ||
		initEnv[captureEnvIncarnationNode] != "node-1" {
		t.Errorf("the control init would hold generation %q on node %q; the reservation is "+
			"generation 4 on node-1", initEnv[captureEnvIncarnationGeneration],
			initEnv[captureEnvIncarnationNode])
	}
}

// No file under atc/ composes a source incarnation's name.
//
// The ATC repeats `ReservedIncarnation.Directory`, which came off the wire. It
// must not derive one, because the handle generation is the daemon's monotonic
// ledger sequence: a control plane that could spell the directory could spell a
// STALE one, and a stale generation pointing at a live source is the exact
// confusion SourceIncarnation exists to prevent -- Req 7 as a scan rather than
// as a comment.
//
// Two things make it non-vacuous. The exemptions are PINNED with a reason and
// the pin fails if the reason's file stops containing the pattern, so a rename
// cannot quietly empty the guard; and the counter-check asserts the derivation
// exists in the contract package, so the scan is looking for a spelling
// something still uses.
func TestNoATCCodeComposesAnIncarnationName(t *testing.T) {
	// The two shapes that DERIVE one: the daemon's format string, and a call
	// to the contract package's derivation. Reading a field of a reservation --
	// its generation, its node -- is repeating, not composing, and is not here.
	composers := map[string]*regexp.Regexp{
		"the daemon's own format string":    regexp.MustCompile(`%s\s*\.\s*%d`),
		"the contract package's derivation": regexp.MustCompile(`\.Directory\(\)`),
	}

	// Pinned, with the reason. Each entry is a file that may derive a
	// directory, and why -- and each one is checked to still contain what it
	// was pinned for.
	pinned := map[string]string{
		"atc/runtime/executioncontrol.go": "it derives the directory only to REFUSE one that " +
			"does not match: a ReservedDirectory that does not derive from the incarnation " +
			"beside it is the ATC having composed a path, and this is where that is caught",
	}

	// The brine module is the behavioural harness, not the ATC. Its fixture
	// spells the layout because it asserts on what is on disk afterwards --
	// which is the one thing a request contract cannot say.
	const harness = "atc/worker/jetbridge/brine/"

	root := filepath.Join("..", "..", "..", "atc")
	scanned := 0
	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative := filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(path), "../../../"))
		// Tests may name the directory: they are the things asserting what
		// production repeated, and a test that could not spell the expected
		// value could not assert on it.
		if strings.HasSuffix(path, "_test.go") || strings.HasPrefix(relative, harness) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for why, composer := range composers {
			if !composer.Match(body) {
				continue
			}
			found[relative] = true
			if _, allowed := pinned[relative]; allowed {
				continue
			}
			t.Errorf("%s composes a source incarnation's name (%s). The ATC repeats the daemon's "+
				"answer -- ReservedIncarnation.Directory -- and never derives one; the handle "+
				"generation is that node's ledger sequence, and a stale one points at somebody "+
				"else's live source.", relative, why)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	if scanned < 100 {
		t.Fatalf("the scan read only %d production files under %s; this guard would pass "+
			"vacuously", scanned, root)
	}

	// Every pin must still be earning its exemption. A pin on a file that no
	// longer derives anything is an exemption nobody is watching.
	for file, reason := range pinned {
		if !found[file] {
			t.Errorf("%s is pinned as allowed to derive an incarnation directory (%s) and no "+
				"longer does. Drop the pin, or the scan has stopped seeing the shape.",
				file, reason)
		}
	}

	// The counter-check: the derivation exists in the contract package, so the
	// patterns above are not looking for a spelling that was renamed away.
	contract, err := os.ReadFile(filepath.Join("..", "..", "..", "hangar", "output",
		"reservation.go"))
	if err != nil {
		t.Fatalf("reading the contract package's derivation: %v", err)
	}
	if !composers["the daemon's own format string"].Match(contract) {
		t.Error("hangar/output/reservation.go does not compose an incarnation directory either, " +
			"so this scan is looking for a spelling that no longer exists")
	}
}

// Every Container the worker builds carries the ledger classifier.
//
// This is the finding this pass turned up on the ATC side, and it is worse than
// the wrong question: `captureClass` was never assigned by production at all.
// `refuseIfCaptureHeld` returns nil on a nil classifier -- which is right for a
// deployment with no output plane -- so the pause-pod and hijack refusals were
// dark everywhere, and every test that exercised them supplied the collaborator
// themselves.
//
// The control is asserted first and it is the deployment with no output plane:
// it must still get NO classifier, because the classifier's own refusal is fail
// closed and a worker with no daemon to ask would refuse every reused handle.
func TestEveryContainerTheWorkerBuildsCarriesTheLedgerClassifier(t *testing.T) {
	off := NewDaemonSetBackend(capturePodConfig(false), NewArtifactLocator(), nil, nil)
	ordinary := newContainer("h", db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{}, nil, nil, capturePodConfig(false), "worker", nil, nil,
		off, false, false)
	if ordinary.captureClass != nil {
		t.Error("a deployment with no output plane was given a ledger classifier; every reused " +
			"handle would then be refused by a guard that fails closed on a daemon that is " +
			"not there")
	}

	on := NewDaemonSetBackend(capturePodConfig(true), NewArtifactLocator(), nil, nil)
	container := newContainer("h", db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{}, nil, nil, capturePodConfig(true), "worker", nil, nil,
		on, false, false)
	if container.captureClass == nil {
		t.Fatal("the worker built a Container with no ledger classifier, so refuseIfCaptureHeld " +
			"returns nil on every path: pause pod recreation and hijack over a capture-held " +
			"source are not refused anywhere in production")
	}

	// A worker with no storage backend at all -- the emptyDir deployment --
	// has nothing to ask and must not pretend otherwise.
	none := newContainer("h", db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{}, nil, nil, capturePodConfig(true), "worker", nil, nil,
		nil, false, false)
	if none.captureClass != nil {
		t.Error("a worker with no storage backend was given a ledger classifier")
	}
}

// The capture pod is pinned to the node whose daemon reserved its incarnation.
//
// The two ready labels above pick a COHORT -- nodes where a hold could be
// acknowledged at all -- and the Phase 4 round-1 review's point is that a
// cohort is not a node. The reservation is a directory on one node's disk, made
// before this Pod existed; a producer the scheduler placed anywhere else in the
// cohort would mount an empty unheld directory (`DirectoryOrCreate` makes one)
// and its control init's hold would be refused by a daemon that reserved
// nothing. Requiring the node turns that outage into a Pod that stays Pending.
//
// The ordinary pod is the control and it is asserted first: it is pinned to
// nothing, because an ordinary step may run anywhere its inputs allow.
func TestACaptureSelectedPodIsPinnedToTheReservingNodeAndAnOrdinaryOneIsNot(t *testing.T) {
	hostnameValues := func(pod *corev1.Pod) []string {
		affinity := pod.Spec.Affinity
		if affinity == nil || affinity.NodeAffinity == nil ||
			affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			return nil
		}
		var values []string
		for _, term := range affinity.NodeAffinity.
			RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			for _, expr := range term.MatchExpressions {
				if expr.Key == corev1.LabelHostname {
					values = append(values, expr.Values...)
				}
			}
		}

		return values
	}

	ordinary, err := capturingContainer(t, capturePodConfig(true), false, nil).
		buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
	if err != nil {
		t.Fatalf("building the ordinary pod: %v", err)
	}
	if pinned := hostnameValues(ordinary); len(pinned) != 0 {
		t.Errorf("an ordinary pod was pinned to %v; a step with no reservation may run anywhere",
			pinned)
	}

	capture, err := capturingContainer(t, capturePodConfig(true), false, admittedCapture()).
		buildPod(runtime.ProcessSpec{Path: "/bin/sh"}, []string{"sh"}, nil)
	if err != nil {
		t.Fatalf("building the capture pod: %v", err)
	}
	pinned := hostnameValues(capture)
	if len(pinned) != 1 || pinned[0] != testReservingNode {
		t.Errorf("the capture pod is pinned to %v and its reservation was issued by %s",
			pinned, testReservingNode)
	}
}

// assignsCaptureClass matches an assignment TO the field and nothing else: not
// `c.captureClass == nil`, not `c.captureClass.CaptureClass(...)`, not the
// `captureClassHeld` constant beside it.
var assignsCaptureClass = regexp.MustCompile(`captureClass(\s*=[^=]|:\s)`)

// One assignment site for the ledger classifier, and it is newContainer.
//
// Reviewer's F8. `newContainer` assigns it under the output-plane predicate;
// worker.go assigned it again, unconditionally, at both FindOrCreateContainer
// returns and in LookupContainer -- all of which had already been through
// newContainer. Harmless while the two agreed, and the class of defect this
// very field produced (a nil-tolerant field nobody assigns in production, which
// made refuseIfCaptureHeld a no-op on every path) is exactly the one two
// assignment sites invite: the test above pins newContainer alone, so a second
// site could disagree with it and be pinned by nothing.
//
// The scan is over the package's own source rather than over behaviour, because
// what is being asserted is that there is nowhere ELSE to look.
func TestTheLedgerClassifierIsAssignedInExactlyOnePlace(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package: %v", err)
	}

	sites := map[string]int{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		sites[name] += len(assignsCaptureClass.FindAllIndex(source, -1))
		if sites[name] == 0 {
			delete(sites, name)
		}
	}

	if len(sites) != 1 || sites["container.go"] != 1 {
		t.Errorf("the ledger classifier is assigned in %v; it is assigned once, in "+
			"newContainer, because a second site is a second answer to \"does this container "+
			"ask the ledger\" and only the first one is pinned", sites)
	}
}

// The output plane's scheme does not move with the artifact daemon's switch.
//
// It used to. Both output-plane call sites asked `daemonURLScheme`, a predicate
// over `ArtifactDaemonTLSEnabled`, which the chart derives from
// `artifactDaemon.tls.enabled` -- a different daemon, a different bucket, a
// different identity, and false by default. The output daemon's control API has
// no plaintext branch at all, so under the chart's own documented values the
// generated hold dialed `http://` at an HTTPS listener, was answered "Client
// sent an HTTP request to an HTTPS server", and the producer never started.
//
// Both directions are asserted, because a rule that only checks the default
// would pass on a scheme that follows the wrong flag upward.
func TestTheControlInitDialsHTTPSWhateverTheArtifactDaemonsTLSSwitchSays(t *testing.T) {
	for _, artifactTLS := range []bool{false, true} {
		cfg := capturePodConfig(true)
		cfg.ArtifactDaemonTLSEnabled = artifactTLS

		script := capturingContainer(t, cfg, false, admittedCapture()).
			buildCaptureControlInitContainer().Command[2]

		fallback := "https://${" + captureEnvHostIP + "}:${" + captureEnvOutputPort + "}"
		if !strings.Contains(script, fallback) {
			t.Errorf("with ArtifactDaemonTLSEnabled=%v the control init composes no %q; the "+
				"output daemon is TLS-only and a plaintext dial is refused at the transport, "+
				"before any capability is read",
				artifactTLS, fallback)
		}
		if strings.Contains(script, "http://${"+captureEnvHostIP+"}") {
			t.Errorf("with ArtifactDaemonTLSEnabled=%v the control init composes a plaintext "+
				"dial at the output daemon", artifactTLS)
		}
		if !strings.Contains(script, `WGET_OPTS="--no-check-certificate"`) {
			t.Errorf("with ArtifactDaemonTLSEnabled=%v the control init passes no "+
				"--no-check-certificate; it dials its own node by IP, which can be no "+
				"certificate SAN, so the handshake fails however correct the deployment is",
				artifactTLS)
		}
	}

	if got := outputDaemonURLScheme(); got != "https" {
		t.Errorf("outputDaemonURLScheme is %q", got)
	}
}
