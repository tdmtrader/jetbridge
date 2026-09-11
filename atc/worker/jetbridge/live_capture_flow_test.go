// hangar_live only, never live: like the generated-Pod contract beside it, this
// one needs a disposable cluster. It labels a node into the output-plane ready
// cohort (concourse.dev/hangar-execution-control-v1, concourse.dev/hangar-output-v1),
// runs a node-local output daemon, and pins a Pod to that node -- all
// cluster-scoped work a namespaced live-tier account cannot do and must not do
// against the deployed cluster. Its CI home is a job in
// deploy/k8s-e2e-pipeline.yml beside hangar-generated-pod-contract;
// build-and-vet only compiles it.
//go:build hangar_live

package jetbridge

// The one thing no unit tier can say: a real kubelet ran the Pod this runtime
// generated, and the hold the capture control init took inside it is the hold
// the producer's own start revalidated.
//
// WHAT THIS FILE COVERS, and what it deliberately does not.
//
// The Phase 4 round-1 review is why it exists: F1 and F2 were both defects only
// a real node reveals, and both were invisible to a phase whose only coverage of
// the generated script was `sh -n`. Phase 4 closed them with a Go test that
// EXECUTES the script against a real daemon, which is as far as a unit tier can
// go. What that test still cannot say is the three things below, and those are
// what this test asserts:
//
//  1. A kubelet ran the control init TO COMPLETION before the producer started.
//     The daemon fixture writes a marker at the moment it acknowledges the hold
//     and the producer refuses to run without it, so this is the hold's effect
//     rather than its position in a list.
//
//     NARROWED, deliberately, and the narrowing is the honest version. The
//     header used to say "before every writer". This Pod HAS no other writer:
//     `cleanup-stale` is emitted only for a reused handle
//     (`storage_daemonset.go:562`) and `artifact-fetch` only for declared
//     inputs, so its init list has exactly one element and `InitContainers[0]`
//     is trivially the control init. Ordering against SIBLING inits is asserted
//     where a multi-init Pod exists: `features/hangar-capture-pod.feature`'s
//     `Selecting capture for a declared output puts the hold init container
//     before every writer`, and `capture_control_test.go`. Making it real here
//     would mean a second fixture listening on the artifact daemon's port to
//     answer the cleanup init's `GET /capture-held/steps/<handle>`, which is
//     scaffolding this contract does not need for what it does say.
//  2. The Downward API really supplied `metadata.uid`, and the value the API
//     server assigned is the value that reached the daemon. No fixture can
//     honestly invent a UID the API server has not yet issued; this test reads
//     the request the daemon received back out of the producer's own logs and
//     compares it against the Pod's `metadata.uid` as the API server reports it.
//  3. The hostPath the ATC mounted is the directory the daemon reserved on that
//     node's disk, and the node affinity put the Pod on the node holding the
//     reservation. The producer writes into the reserved incarnation and the
//     bytes are read back from the fixture's own view of the host path, so a
//     builder that composed a path of its own fails here even though every unit
//     assertion about the Pod spec still passes.
//
// NOT covered here, and named rather than implied. The Phase 9 plan's list for
// this box also includes cancellation before hold / after hold / during
// execution / after Stage 2, pre_reservation_cancel release behaviour,
// sidecar and hijack termination, seal timeout, web and daemon restart, pod
// disappearance, node loss, and the materialization read lease. Every one of
// those needs a real output daemon binary and a real control plane on the
// cluster, not the BusyBox stand-in below, and they are the sibling of the
// Phase 9 activation/downgrade cluster tests rather than of this contract.
// phase-9-demonstrations.md records them as written-up-but-unwritten with this
// reason; nothing in this file claims them.
//
// THE DAEMON HERE IS A STAND-IN, and the file says so where it matters. It is
// BusyBox `nc` answering one route, exactly as the strict-tree contract beside
// it uses BusyBox to answer the materialization route. What is under test is
// the POD and the KUBELET -- ordering, identity, placement, the host path --
// and none of those become more true with a real daemon behind them. What the
// stand-in cannot say is anything about the daemon's own answer, and this test
// asserts nothing about it beyond the shape the init script switches on.

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	atcruntime "github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The identities a capture-selected execution is admitted with. They are
// constants because the assertions name them, and because the whole point of
// Req 7 is that a task cannot choose one.
const (
	liveCaptureExecution = "11111111-1111-4111-8111-111111111111"
	liveCaptureHandoff   = "22222222-2222-4222-8222-222222222222"
	liveCaptureLease     = "33333333-3333-4333-8333-333333333333"
	liveCaptureOutput    = "result"
	liveCaptureNodeUID   = "live-node-uid"
	liveCaptureGrant     = "live-source-control-grant"
	liveCapturePort      = 31781
)

func TestLiveCaptureSelectedProducerHoldsAndWrites(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("real BusyBox/Linux and K3s execution is CI-only on macOS")
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	namespace := os.Getenv("K8S_TEST_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}
	cfg := NewConfig(namespace, kubeconfig)
	client, err := NewClientset(cfg)
	if err != nil {
		t.Fatalf("create live Kubernetes client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The node this execution's reservation belongs to. A reservation is a
	// directory on ONE node's disk, so the Pod has to land on that node and
	// nowhere else -- which is contract 3, and which one node cannot disprove.
	// The job that runs this stands up ONE K3s container, so the placement
	// assertion below cannot currently fail: the affinity is asserted on the
	// spec and the placement on the result, and only a second node would make
	// the second of those falsifiable. Stated rather than hedged.
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		t.Fatalf("list K3s nodes: count=%d err=%v", len(nodes.Items), err)
	}
	node := nodes.Items[0]
	// The runtime pins by kubernetes.io/hostname, not by object name, so the
	// reservation names the value of that label rather than the node's name.
	// They are the same string on K3s and the test would be asserting a
	// coincidence if it did not say which one it means.
	reservingNode := node.Labels[corev1.LabelHostname]
	if reservingNode == "" {
		t.Fatalf("node %s carries no %s label; the capture pod is pinned by that label and "+
			"a node without one can hold no reservation this runtime can schedule to",
			node.Name, corev1.LabelHostname)
	}
	// THREE labels, not two. `BuildAffinity` seeds the required expressions with
	// `concourse.dev/artifact-cache` unconditionally
	// (`storage_daemonset.go:689-693`) before it appends the two output-plane
	// ones, so a node carrying only the output pair schedules nothing and the
	// Pod sits Pending until the context expires -- a failure whose message
	// would name a deadline rather than a label.
	labelLiveNode(t, ctx, client, node.Name,
		"concourse.dev/artifact-cache",
		"concourse.dev/hangar-execution-control-v1",
		"concourse.dev/hangar-output-v1")

	unique := fmt.Sprintf("hangar-capture-%d", time.Now().UnixNano())
	hostRoot := "/tmp/" + unique

	cfg.Namespace = namespace
	cfg.ArtifactDaemonHostPath = hostRoot
	// The artifact daemon's port. Nothing listens on it and nothing in this Pod
	// dials it -- cleanup-stale, its only consumer, is not emitted for a fresh
	// handle -- but the config field is what the pod builder reads, so it is
	// set to something that is not the output daemon's.
	cfg.ArtifactDaemonPort = 31782
	cfg.ArtifactHelperImage = "busybox:latest"
	cfg.OutputPlaneEnabled = true
	cfg.OutputDaemonPort = liveCapturePort
	cfg.OutputActivationEpoch = 9

	// The reservation, as the daemon would have answered it. Nothing here
	// composes a path: the incarnation's own Directory() is what the ATC
	// repeats into the Pod, and the fixture below derives the same string the
	// same way rather than being told one.
	incarnation := hangaroutput.SourceIncarnation{
		ExecutionID:      liveCaptureExecution,
		NodeUID:          liveCaptureNodeUID,
		HandleGeneration: 4,
		Output:           liveCaptureOutput,
	}
	reservedDirectory := incarnation.Directory()

	// The control endpoint is the NODE's, and it is read off the API server
	// rather than composed.
	//
	// `buildPod` validates the envelope (`container.go:465` ->
	// `runtime.ExecutionControl.Validate`), and an empty endpoint is refused:
	// "an envelope nobody can ask about is not control". The init script does
	// have a Downward-API fallback for an empty one, but reaching it here would
	// mean shipping an envelope production refuses, so the deployed shape is
	// what is exercised: the ATC knows which node reserved the incarnation, so
	// it knows where that node's daemon answers.
	nodeAddress := ""
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP {
			nodeAddress = address.Address
		}
	}
	if nodeAddress == "" {
		t.Fatalf("node %s reports no InternalIP; the control init dials its node's daemon by "+
			"address and there is none to dial", node.Name)
	}

	control := &atcruntime.ExecutionControl{
		Version:         atcruntime.ExecutionControlVersion,
		Phase:           atcruntime.ControlPhaseAdmitted,
		Identity:        executioncontrol.Identity{ExecutionID: liveCaptureExecution, Fence: 3},
		ActivationEpoch: 9,
		Endpoint:        fmt.Sprintf("http://%s:%d", nodeAddress, liveCapturePort),
		Capability:      "live-base-capability",
	}
	if err := control.SelectCapture(atcruntime.DurableOutputCapture{
		Version:             atcruntime.DurableOutputCaptureVersion,
		Identity:            control.Identity,
		ActivationEpoch:     control.ActivationEpoch,
		HandoffID:           hangaroutput.HandoffID(liveCaptureHandoff),
		SourceLeaseID:       hangaroutput.SourceLeaseID(liveCaptureLease),
		Output:              liveCaptureOutput,
		SourceControlGrant:  liveCaptureGrant,
		CaptureDeadline:     time.Now().Add(time.Hour).UTC(),
		ReservedIncarnation: incarnation,
		ReservedDirectory:   reservedDirectory,
		ReservingNode:       reservingNode,
	}); err != nil {
		t.Fatalf("select capture: %v", err)
	}

	// The producer. Every line of it is a contract:
	//
	//   - the hold marker EXISTS, which is contract 1: the kubelet ran the
	//     control init to completion before this container started, and the
	//     fixture only writes the marker when it answers a hold;
	//   - the reserved incarnation is the directory this container's declared
	//     output is mounted at, and it is WRITABLE, which is contract 3 and
	//     Req 3's "the hold prevents cleanup ... while allowing the admitted
	//     producer to write";
	//   - the request the daemon received is echoed to stdout, so the test can
	//     compare the pod_uid in it against the one the API server assigned,
	//     which is contract 2.
	mainScript := `set -eu
test -f /hold/.hold-acknowledged
printf 'produced by the capture-selected step\n' > /tmp/build/result/artifact
test -s /tmp/build/result/artifact
echo '--- the hold request the daemon received ---'
cat /hold/.hold-request
`

	container := &Container{
		handle:   "live-capture-handle",
		podName:  unique + "-step",
		metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
		containerSpec: atcruntime.ContainerSpec{
			Dir:              "/tmp/build/task",
			Type:             db.ContainerTypeTask,
			ImageSpec:        atcruntime.ImageSpec{ImageURL: "busybox:latest"},
			Outputs:          atcruntime.OutputPaths{liveCaptureOutput: "/tmp/build/result"},
			ExecutionControl: control,
		},
		config:         cfg,
		storageBackend: NewDaemonSetBackend(cfg, nil, nil),
		properties:     map[string]string{},
	}

	pod, err := container.buildPod(atcruntime.ProcessSpec{}, []string{"sh", "-c", mainScript}, nil)
	if err != nil {
		t.Fatalf("generate capture-selected task Pod: %v", err)
	}

	// Read off the spec BEFORE anything runs, because a Pod that never
	// scheduled would make every runtime assertion below vacuous and these are
	// the ones that say what shape it is.
	assertLivePodMountsResolve(t, pod)
	if len(pod.Spec.InitContainers) == 0 {
		t.Fatal("the capture-selected Pod has no init containers at all")
	}
	names := make([]string, 0, len(pod.Spec.InitContainers))
	for _, init := range pod.Spec.InitContainers {
		names = append(names, init.Name)
	}
	if pod.Spec.InitContainers[0].Name != captureControlInitName {
		t.Fatalf("the capture control init is not first: %v", names)
	}
	// And it is the ONLY one, which this test says out loud rather than letting
	// the line above read as an ordering proof. A step with no inputs on a
	// fresh handle emits no cleanup-stale and no artifact-fetch; if that ever
	// changes, the ordering claim here becomes real and the header's narrowing
	// is what needs revisiting.
	if len(names) != 1 {
		t.Fatalf("this Pod has %d init containers (%v); the ordering assertion above is about "+
			"a list of one, and with siblings present it would need to say more", len(names), names)
	}
	assertLiveCaptureAffinity(t, pod, reservingNode)

	// The producer's declared output is mounted at the incarnation the daemon
	// reserved, and the host path under it is the reserved directory rather
	// than a path this runtime composed.
	outputMount := liveMountAt(t, pod.Spec.Containers[0].VolumeMounts, "/tmp/build/result")
	if outputMount.ReadOnly {
		t.Fatal("the captured output is mounted read-only; the admitted producer must be able " +
			"to write into the source it holds")
	}
	hostPath := liveHostPathForVolume(t, pod, outputMount.Name)
	if !strings.HasSuffix(hostPath, reservedDirectory) {
		t.Fatalf("the captured output's host path is %q and the daemon reserved %q; the pod "+
			"builder composed a path of its own", hostPath, reservedDirectory)
	}

	// A second volume for the hold marker, so the producer can see what the
	// control init's daemon did. It is test scaffolding and it is added AFTER
	// every assertion about the generated shape, so nothing above is asserting
	// this file's own edit.
	hostPathType := corev1.HostPathDirectoryOrCreate
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: "hold-evidence",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: hostRoot + "/hold", Type: &hostPathType},
		},
	})
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts,
		corev1.VolumeMount{Name: "hold-evidence", MountPath: "/hold"})

	// The daemon stand-in. It writes the WHOLE request to the host before it
	// answers, and writes the marker in the same breath, so "the marker exists"
	// and "the request arrived" are one fact rather than two.
	//
	// It also widens the reserved incarnation directory, which lives under
	// `steps/` because that is where `ReservedIncarnationVolume` roots it
	// (`storage_daemonset.go:110`).
	//
	// The claim this comment used to make -- that creating it here is what the
	// daemon does at reservation time, so the test is not asserting the
	// kubelet's mkdir -- was false twice over: the path was missing its
	// `steps/` segment, and the handler runs when `nc` accepts a connection,
	// which is inside the control init, long after the kubelet did its
	// DirectoryOrCreate during volume setup. The honest statement is the
	// narrow one: the producer runs as root in this Pod, so the chmod is
	// belt-and-braces and the directory it writes into is the kubelet's.
	fixtureScript := fmt.Sprintf(`set -eu
mkdir -p '/host/hold' '/host/steps/%s'
chmod 777 '/host/steps/%s'
REQ=/host/hold/.hold-request
: >"$REQ"
LEN=0
while IFS= read -r line; do
  printf '%%s\n' "$line" >>"$REQ"
  case "$line" in
    Content-Length:*|content-length:*) LEN=$(echo "$line" | tr -d '\r' | cut -d' ' -f2) ;;
  esac
  [ "$line" = "$(printf '\r')" ] && break
done
if [ "$LEN" -gt 0 ]; then
  dd bs=1 count="$LEN" 2>/dev/null >>"$REQ"
fi
: >/host/hold/.hold-acknowledged
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 34\r\nConnection: close\r\n\r\n{"kind":"hold_acknowledged","x":0}'
`, reservedDirectory, reservedDirectory)

	fixture := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: unique + "-daemon", Namespace: namespace},
		Spec: corev1.PodSpec{
			NodeName:      node.Name,
			HostNetwork:   true,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "output-daemon-fixture",
				Image:   "busybox:latest",
				Command: []string{"sh", "-c", fmt.Sprintf("printf '%%s' \"$HANDLER\" >/tmp/handler; chmod 700 /tmp/handler; exec nc -ll -p %d -e /tmp/handler", liveCapturePort)},
				// PullIfNotPresent for the reason the strict-tree fixture
				// gives: a `:latest` tag defaults to Always, so scaffolding
				// that proves nothing on its own would fail the contract on a
				// registry timeout inside a nested CI cluster.
				ImagePullPolicy: corev1.PullIfNotPresent,
				Env:             []corev1.EnvVar{{Name: "HANDLER", Value: "#!/bin/sh\n" + fixtureScript}},
				SecurityContext: &corev1.SecurityContext{RunAsUser: int64Ptr(0)},
				VolumeMounts:    []corev1.VolumeMount{{Name: "host", MountPath: "/host"}},
			}},
			Volumes: []corev1.Volume{{
				Name: "host",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: hostRoot, Type: &hostPathType},
				},
			}},
		},
	}

	createLivePod(t, ctx, client, fixture)
	waitLivePodRunning(t, ctx, client, namespace, fixture.Name)
	time.Sleep(time.Second)

	createLivePod(t, ctx, client, pod)
	waitLivePodSucceeded(t, ctx, client, namespace, pod.Name)

	// Contract 3's placement half, from the API server rather than from the
	// spec: whatever the scheduler decided, it decided the reserving node.
	scheduled, err := client.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get the scheduled capture Pod: %v", err)
	}
	if scheduled.Spec.NodeName != node.Name {
		t.Errorf("the capture Pod ran on %q and its reservation is on %q; the directory the "+
			"hold protects is on one node's disk and DirectoryOrCreate would have made an "+
			"empty unheld one here", scheduled.Spec.NodeName, node.Name)
	}

	// Contract 2. The producer echoed the request the daemon received; the UID
	// in it is the one the API server assigned to THIS Pod, which no fixture
	// could have invented because it did not exist when the Pod was composed.
	logs, err := client.CoreV1().Pods(namespace).
		GetLogs(pod.Name, &corev1.PodLogOptions{Container: pod.Spec.Containers[0].Name}).
		DoRaw(ctx)
	if err != nil {
		t.Fatalf("read the producer's logs: %v", err)
	}
	body := string(logs)
	assigned := string(scheduled.UID)
	if assigned == "" {
		t.Fatal("the API server reported no UID for the capture Pod")
	}
	if !strings.Contains(body, fmt.Sprintf(`"pod_uid":"%s"`, assigned)) {
		t.Errorf("the hold request does not carry the Pod UID the API server assigned (%s). "+
			"The hold binds to the init container's own Downward API metadata.uid, and a hold "+
			"bound to anything else is a hold for a Pod that may not be this one.\n%s",
			assigned, body)
	}
	// And the rest of the identity, so a request that carried a UID and nothing
	// else would not pass.
	for _, want := range []string{
		fmt.Sprintf(`"handoff_id":"%s"`, liveCaptureHandoff),
		fmt.Sprintf(`"source_lease_id":"%s"`, liveCaptureLease),
		fmt.Sprintf(`"execution_id":"%s"`, liveCaptureExecution),
		fmt.Sprintf(`"output":"%s"`, liveCaptureOutput),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the hold request does not carry %s:\n%s", want, body)
		}
	}

	// Req 24, on a Pod a kubelet actually ran: the source-control grant is in
	// the control init and in nothing else.
	for _, container := range append(
		append([]corev1.Container{}, scheduled.Spec.InitContainers...),
		scheduled.Spec.Containers...,
	) {
		carries := false
		for _, variable := range container.Env {
			if variable.Value == liveCaptureGrant {
				carries = true
			}
		}
		if carries && container.Name != captureControlInitName {
			t.Errorf("container %q carries the source-control grant; only %q may",
				container.Name, captureControlInitName)
		}
		if !carries && container.Name == captureControlInitName {
			t.Errorf("%q does not carry the source-control grant, so the check above proves "+
				"nothing", captureControlInitName)
		}
	}
}

func int64Ptr(value int64) *int64 { return &value }

// labelLiveNode puts a node into a ready cohort and puts its labels back.
func labelLiveNode(t *testing.T, ctx context.Context, client kubernetes.Interface,
	name string, keys ...string) {
	t.Helper()

	node, err := client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node %s: %v", name, err)
	}
	restore := map[string]*string{}
	for _, key := range keys {
		if value, found := node.Labels[key]; found {
			existing := value
			restore[key] = &existing
		} else {
			restore[key] = nil
		}
		node.Labels[key] = "ready"
	}
	if _, err := client.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("label node %s: %v", name, err)
	}
	t.Cleanup(func() {
		latest, getErr := client.CoreV1().Nodes().Get(context.Background(), name, metav1.GetOptions{})
		if getErr != nil {
			t.Errorf("restore node labels: %v", getErr)

			return
		}
		for key, value := range restore {
			if value == nil {
				delete(latest.Labels, key)
			} else {
				latest.Labels[key] = *value
			}
		}
		if _, updateErr := client.CoreV1().Nodes().Update(context.Background(), latest,
			metav1.UpdateOptions{}); updateErr != nil {
			t.Errorf("restore node labels: %v", updateErr)
		}
	})
}

// assertLiveCaptureAffinity is the output plane's cohort, both labels, plus the
// reserving node.
func assertLiveCaptureAffinity(t *testing.T, pod *corev1.Pod, reservingNode string) {
	t.Helper()

	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil {
		t.Fatal("the capture-selected Pod has no node affinity at all")
	}
	required := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if required == nil || len(required.NodeSelectorTerms) == 0 {
		t.Fatal("the capture-selected Pod has no REQUIRED node affinity; a preference would let " +
			"it schedule away from the node holding its reservation")
	}

	// Every label the scheduler will actually demand, including the
	// artifact-cache one BuildAffinity seeds for every pod. An assertion that
	// covered only the output pair would pass while the Pod was unschedulable.
	want := map[string]bool{
		"concourse.dev/artifact-cache":              false,
		"concourse.dev/hangar-execution-control-v1": false,
		"concourse.dev/hangar-output-v1":            false,
	}
	pinned := false
	for _, term := range required.NodeSelectorTerms {
		for _, expression := range term.MatchExpressions {
			if _, tracked := want[expression.Key]; tracked &&
				expression.Operator == corev1.NodeSelectorOpIn &&
				len(expression.Values) == 1 && expression.Values[0] == "ready" {
				want[expression.Key] = true
			}
		}
		for _, expression := range term.MatchExpressions {
			if expression.Key == corev1.LabelHostname &&
				expression.Operator == corev1.NodeSelectorOpIn &&
				len(expression.Values) == 1 && expression.Values[0] == reservingNode {
				pinned = true
			}
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("the capture Pod does not require %s In [ready]", key)
		}
	}
	if !pinned {
		t.Errorf("the capture Pod is not pinned to the reserving node %q. A reservation is a "+
			"directory on ONE node's disk: a Pod that schedules elsewhere gets an empty unheld "+
			"directory from DirectoryOrCreate and its hold is refused", reservingNode)
	}
}

func liveHostPathForVolume(t *testing.T, pod *corev1.Pod, name string) string {
	t.Helper()

	for _, volume := range pod.Spec.Volumes {
		if volume.Name == name && volume.HostPath != nil {
			return volume.HostPath.Path
		}
	}
	t.Fatalf("volume %q has no host path; the captured output must live on the node the "+
		"reservation was issued by", name)

	return ""
}
