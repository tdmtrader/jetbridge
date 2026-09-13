package steps

// Pause pod recreation over a capture-held source.
//
// This is the regression the whole writer-ticket rule exists to protect, and it
// is the one destructive path with no execution identity to take a ticket with:
// a pause pod that goes terminal before the step's command runs is REPLACED,
// and a replacement is a new Pod UID getting a write-capable mount over the
// step's tree. For a capture-selected step that tree is the reserved
// incarnation, and Req 16 says a held incarnation may not receive one.
//
// Everything here drives PRODUCTION code over a REAL artifact daemon. The
// refusal comes out of Container.Run consulting DaemonSetBackend.CaptureClass,
// which is an HTTPS call to the daemon's read-only classification route, which
// reads the ledger record the OUTPUT daemon wrote when it reserved and held the
// source. Three processes and one storage root, which is what a node is.

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/brine-dev/brine-go/pkg/brine"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

// PausePodReplacement is what happened when the runtime met a terminal pause
// pod: a fresh one, or a refusal.
//
// The Pod UID is what tells them apart. "A pod exists afterwards" is true in
// both cases -- the refusal leaves the terminal one in place -- so the
// assertion has to be about WHICH pod, and a UID is the only thing that says.
type PausePodReplacement struct {
	Ctx       context.Context
	Namespace string
	Handle    string

	// Before is the terminal pod's UID and After is whatever carries the pod's
	// name once the runtime has had its say. Present says whether anything
	// carries it at all.
	//
	// The UID is what tells a replacement from a refusal. "A pod exists
	// afterwards" is true either way -- a refusal leaves the terminal one in
	// place -- so the assertion has to be about WHICH pod, and the fake API
	// server assigns no UIDs, so the fixture stamps the terminal one and a
	// replacement is recognised by NOT carrying it.
	Before  types.UID
	After   types.UID
	Present bool

	// Err is the runtime's refusal, as a VALUE: "the runtime's refusal says"
	// is an outcome a scenario asserts on, and a fixture that died on it could
	// not.
	Err error
}

// hangarPausePodNode is the node both the pod and the daemon are on. The daemon
// listens on loopback, so the node's address is loopback: the ATC resolves the
// node's IP the way it does in production and reaches the real process.
const hangarPausePodNode = "node-1"

// HangarPausePodDefinitions is the pause-pod regression family.
func HangarPausePodDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The CONTROL's sentence. It is a separate sentence rather than the
		// same one over a different state because brine's registry gives one
		// pattern exactly one input type -- and it is a separate SCENARIO,
		// asserted above its twin, for the reason `Destructive cleanup is
		// permitted once the witness and the release both exist` is: "the
		// replacement was refused" passes on a runtime that has stopped
		// replacing pause pods at all.
		brine.DefineMapUsing[CaptureDraft, PausePodReplacement](
			"an ordinary step's pause pod reaches a terminal state",
			[]string{"jetbridge-db"},
			func(in CaptureDraft, _ brine.Params, _ *brine.Recorder,
				res brine.Resources) (PausePodReplacement, error) {
				return terminalPausePod(res, in.Daemon, "ordinary-step", nil)
			},
		),

		brine.DefineMapUsing[HeldSource, PausePodReplacement](
			"its pause pod reaches a terminal state",
			[]string{"jetbridge-db"},
			func(in HeldSource, _ brine.Params, _ *brine.Recorder,
				res brine.Resources) (PausePodReplacement, error) {
				control, err := capturedControl(in)
				if err != nil {
					return PausePodReplacement{}, err
				}

				return terminalPausePod(res, in.Draft.Daemon, "capture-step", control)
			},
		),

		CheckThat[PausePodReplacement]("the pause pod is recreated",
			func(in PausePodReplacement) error {
				if in.Err != nil {
					return fmt.Errorf("the runtime refused to replace an ordinary terminal "+
						"pause pod: %v", in.Err)
				}
				if !in.Present {
					return fmt.Errorf("no pod carries the step's name after the replacement")
				}
				if in.After == in.Before {
					return fmt.Errorf("the terminal pod %s is still the one there; nothing was "+
						"replaced", in.Before)
				}

				return nil
			}),

		CheckThat[PausePodReplacement]("the pause pod is not recreated",
			func(in PausePodReplacement) error {
				if in.Err == nil {
					return fmt.Errorf("the runtime replaced the pause pod of a capture-held "+
						"source: %s became %s. A new Pod UID has a write-capable mount over an "+
						"incarnation a capture is about to seal", in.Before, in.After)
				}
				if !in.Present || in.After != in.Before {
					return fmt.Errorf("the replacement was refused and the terminal pod is gone "+
						"anyway: %s became %s (present=%v)", in.Before, in.After, in.Present)
				}

				return nil
			}),

		// The ATC's refusal, not the daemon's, which is why it is its own
		// sentence: the daemon answered a classification and the runtime is
		// what turned it into a refusal.
		CheckContains[PausePodReplacement]("the runtime's refusal says {string}",
			"the runtime's refusal",
			func(in PausePodReplacement) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("the runtime refused nothing")
				}

				return in.Err.Error(), nil
			}),
	}
}

// capturedControl rebuilds the envelope the control plane put on the step's
// spec, carrying the reservation the daemon issued. Nothing here composes a
// path: the incarnation and its directory are the daemon's own answer, carried
// forward from `the daemon holds the source`.
func capturedControl(in HeldSource) (*runtime.ExecutionControl, error) {
	if in.Reserved.Directory == "" {
		return nil, fmt.Errorf("this chain holds a source with no reservation behind it")
	}
	control := &runtime.ExecutionControl{
		Version:         runtime.ExecutionControlVersion,
		Phase:           runtime.ControlPhaseAdmitted,
		Identity:        in.Execution,
		ActivationEpoch: in.Admission.ActivationEpoch,
		Endpoint:        in.DaemonURL,
		Capability:      "brine-base-capability",
	}
	if err := control.SelectCapture(runtime.DurableOutputCapture{
		Version:             runtime.DurableOutputCaptureVersion,
		Identity:            in.Execution,
		ActivationEpoch:     in.Admission.ActivationEpoch,
		HandoffID:           in.Admission.HandoffID,
		SourceLeaseID:       in.Admission.SourceLeaseID,
		Output:              string(in.Admission.Output),
		SourceControlGrant:  captureGrantForScenario,
		CaptureDeadline:     in.Admission.CaptureDeadline.Time,
		ReservedIncarnation: in.Reserved.Incarnation,
		ReservedDirectory:   in.Reserved.Directory,
		ReservingNode:       scenarioReservingNode,
	}); err != nil {
		return nil, err
	}

	return control, nil
}

// terminalPausePod builds a step, drives its pause pod to a terminal state, and
// asks the runtime to run it again -- which is exactly where the replacement
// decision is made.
//
// The worker is configured to reach the REAL artifact daemon: its port, its
// hostPath root, and the ATC's own client certificate, because every route on
// that daemon but the node-local ones is behind mTLS. The classifier the
// refusal reads is therefore the production one over a real HTTPS call.
func terminalPausePod(res brine.Resources, daemon HangarDaemon, handle string,
	control *runtime.ExecutionControl) (PausePodReplacement, error) {
	port, err := hangarDaemonPort(daemon.Daemon.URL)
	if err != nil {
		return PausePodReplacement{}, err
	}

	cluster, err := NewCluster(res,
		WithExecutor(closingShellAdapter{}),
		WithConfig(func(cfg *jetbridge.Config) {
			cfg.OutputPlaneEnabled = true
			cfg.ArtifactDaemonHostPath = daemon.Daemon.Root
			cfg.ArtifactDaemonPort = port
			cfg.ArtifactDaemonTLSEnabled = true
			cfg.ArtifactDaemonTLSCert = filepath.Join(daemon.CertDir, "client.crt")
			cfg.ArtifactDaemonTLSKey = filepath.Join(daemon.CertDir, "client.key")
			cfg.ArtifactDaemonTLSCACert = filepath.Join(daemon.CertDir, "ca.crt")
		}),
		WithArtifactLocator(jetbridge.NewArtifactLocator()),
		WithTeam(),
	)
	if err != nil {
		return PausePodReplacement{}, err
	}

	// The node the daemon is on. The ATC resolves a node's address before it
	// asks that node's daemon anything, so without this the question could not
	// be posed at all -- and the guard fails closed, which would make the
	// control scenario refuse for the wrong reason.
	if _, err := cluster.Clientset.CoreV1().Nodes().Create(cluster.Ctx, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: hangarPausePodNode},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "127.0.0.1"},
		}},
	}, metav1.CreateOptions{}); err != nil {
		return PausePodReplacement{}, fmt.Errorf("creating the node: %w", err)
	}

	spec := runtime.ContainerSpec{
		TeamID:           cluster.TeamID,
		Dir:              "/tmp/build/task",
		ImageSpec:        runtime.ImageSpec{ImageURL: "busybox"},
		Outputs:          runtime.OutputPaths{"result": "/tmp/build/result"},
		ExecutionControl: control,
	}
	if control != nil {
		spec.Outputs = runtime.OutputPaths{control.Capture.Output: "/tmp/build/result"}
	}

	owner := db.NewFixedHandleContainerOwner(handle)
	metadata := db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: handle}
	container, _, err := cluster.Worker.FindOrCreateContainer(cluster.Ctx, owner, metadata,
		spec, &noopDelegate{})
	if err != nil {
		return PausePodReplacement{}, fmt.Errorf("creating the container: %w", err)
	}
	if _, err := container.Run(cluster.Ctx,
		runtime.ProcessSpec{Path: "/bin/sh"}, runtime.ProcessIO{}); err != nil {
		return PausePodReplacement{}, fmt.Errorf("the first run: %w", err)
	}

	pods, err := cluster.Clientset.CoreV1().Pods(cluster.Namespace).List(cluster.Ctx,
		metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 {
		return PausePodReplacement{}, fmt.Errorf("expected one pause pod, found %d (%v)",
			len(pods.Items), err)
	}

	// The pause pod dies before the step's command runs: the sleep expired, or
	// the node drained, or it was evicted. The fixture plays the kubelet here,
	// and it also binds the pod to a node -- which the scheduler does and a
	// fake clientset does not.
	terminal := pods.Items[0].DeepCopy()
	// The fake API server assigns no UIDs, so the fixture stamps one. It is the
	// only way to tell "this pod was replaced" from "this pod is still here":
	// a replacement is created by production and carries none.
	terminal.UID = types.UID("terminal-pause-pod")
	terminal.Spec.NodeName = hangarPausePodNode
	terminal.Status.Phase = corev1.PodFailed
	terminal.Status.Reason = "Evicted"
	if _, err := cluster.Clientset.CoreV1().Pods(cluster.Namespace).Update(cluster.Ctx,
		terminal, metav1.UpdateOptions{}); err != nil {
		return PausePodReplacement{}, fmt.Errorf("driving the pause pod terminal: %w", err)
	}

	replacement := PausePodReplacement{
		Ctx:       cluster.Ctx,
		Namespace: cluster.Namespace,
		Handle:    handle,
		Before:    terminal.UID,
	}

	// And the decision: run it again. Production either replaces the terminal
	// pod or refuses to.
	_, runErr := container.Run(cluster.Ctx,
		runtime.ProcessSpec{Path: "/bin/sh"}, runtime.ProcessIO{})
	replacement.Err = runErr

	after, getErr := cluster.Clientset.CoreV1().Pods(cluster.Namespace).Get(cluster.Ctx,
		terminal.Name, metav1.GetOptions{})
	if getErr == nil {
		replacement.After, replacement.Present = after.UID, true
	}

	return replacement, nil
}

// daemonPort reads the port out of a daemon's base URL. The launcher binds a
// free one, so nothing in this tree may assume a number.
func hangarDaemonPort(base string) (int, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return 0, fmt.Errorf("parsing the daemon's URL %q: %w", base, err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return 0, fmt.Errorf("the daemon's URL %q names no port: %w", base, err)
	}

	return port, nil
}
