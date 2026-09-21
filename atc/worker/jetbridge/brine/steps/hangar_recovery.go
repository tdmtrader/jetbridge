package steps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type WriterLookup struct {
	Issued, Closed output.CaptureAcknowledgement
	Before, After  controlAnswer
	Foreign        controlAnswer
}

func HangarRecoveryDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, HangarDaemon]("a Hangar output daemon accepting authenticated TLS connections",
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder) (HangarDaemon, error) {
				return startHangarDaemon(rec, true)
			}),
		brine.DefineMapUsing[CaptureDraft, DrainAttempt]("the controller seals a capture with {string} pod evidence",
			[]string{"real-cluster", "jetbridge-db"},
			func(in CaptureDraft, p brine.Params, rec *brine.Recorder, res brine.Resources) (DrainAttempt, error) {
				state, _ := p.GetString(0)
				cluster, err := getRealCluster(res)
				if err != nil {
					return DrainAttempt{}, err
				}
				return attemptRealDrain(in, state, cluster.Clientset, rec, res)
			}),
		CheckThat[DrainAttempt]("the controller proves every captured writer has terminated",
			func(in DrainAttempt) error {
				if in.Err != nil {
					return in.Err
				}
				if !in.Graceful || len(in.Drained) != 1 || !in.Drained[0].ContainersTerminated || in.Drained[0].PodUID != executioncontrol.PodUID(in.Pod.UID) {
					return fmt.Errorf("the exact pod and its captured writer were not accounted for")
				}
				return in.Drained[0].Validate()
			}),
		CheckThat[DrainAttempt]("the seal remains unconfirmed",
			func(in DrainAttempt) error {
				if !errors.Is(in.Err, output.ErrSealUnconfirmed) {
					return fmt.Errorf("expected unconfirmed seal, got %v", in.Err)
				}
				return nil
			}),
		CheckThat[DrainAttempt]("a fresh controller publishes the receipt and releases only its own pod pin",
			func(in DrainAttempt) error {
				if in.Err != nil {
					return in.Err
				}
				if !in.Graceful || in.Final.Receipt == nil || !in.Final.ReleaseAcknowledged {
					return fmt.Errorf("recovery did not gracefully terminate, publish and acknowledge release")
				}
				if len(in.RemainingFinalizers) != 1 || in.RemainingFinalizers[0] != "brine.test/other-owner" {
					return fmt.Errorf("release changed another owner's pin or retained its own: %v", in.RemainingFinalizers)
				}
				return in.Final.Ref.Validate()
			}),
		brine.DefineMap[HeldSource, WriterLookup]("a controller reads an admitted writer before and after closing it",
			func(in HeldSource, _ brine.Params, _ *brine.Recorder) (WriterLookup, error) {
				admission := in.writerAdmission(freshUUID())
				daemon := in.Draft.Daemon
				issued, err := decodeControl[output.CaptureAcknowledgement](daemon.capture("issue-writer-ticket", "/capture/v1/writer-ticket", in.Execution, admission))
				if err != nil {
					return WriterLookup{}, err
				}
				query := map[string]any{"execution": in.Execution, "handoff_id": admission.HandoffID, "writer_ticket_id": admission.WriterTicketID}
				before := daemon.capture("inspect-writer-ticket", "/capture/v1/writer-ticket/inspect", in.Execution, query)
				closed, err := decodeControl[output.CaptureAcknowledgement](daemon.capture("close-writer-ticket", "/capture/v1/writer-ticket/close", in.Execution, admission))
				if err != nil {
					return WriterLookup{}, err
				}
				after := daemon.capture("inspect-writer-ticket", "/capture/v1/writer-ticket/inspect", in.Execution, query)
				foreign := in.Execution
				foreign.ExecutionID = executioncontrol.ExecutionID(freshUUID())
				query["execution"] = foreign
				denied := daemon.capture("inspect-writer-ticket", "/capture/v1/writer-ticket/inspect", foreign, query)
				return WriterLookup{Issued: issued, Closed: closed, Before: before, After: after, Foreign: denied}, nil
			}),
		CheckThat[WriterLookup]("the writer lookup preserves the issued identity and the exact closed statement",
			func(in WriterLookup) error {
				type inspection struct {
					Issued output.CaptureAcknowledgement  `json:"issued"`
					Closed *output.CaptureAcknowledgement `json:"closed"`
				}
				before, err := decodeControl[inspection](in.Before)
				if err != nil {
					return err
				}
				after, err := decodeControl[inspection](in.After)
				if err != nil {
					return err
				}
				if before.Issued != in.Issued || before.Closed != nil || after.Issued != in.Issued || after.Closed == nil || *after.Closed != in.Closed {
					return fmt.Errorf("writer lookup changed an issued or closed identity")
				}
				return nil
			}),
		CheckThat[WriterLookup]("a writer lookup for another execution is refused",
			func(in WriterLookup) error {
				if in.Foreign.Err != nil || in.Foreign.Status != http.StatusForbidden {
					return fmt.Errorf("foreign lookup: status=%d, error=%v", in.Foreign.Status, in.Foreign.Err)
				}
				return nil
			}),
	}
}

type DrainAttempt struct {
	Drained             []output.DrainedWriter
	Pod                 *corev1.Pod
	Err                 error
	Graceful            bool
	Final               output.HandoffRecord
	RemainingFinalizers []string
}

// envtest supplies actual UID, status-subresource, deletion and selector behavior.
// The fixture supplies kubelet status observations; live CI separately proves
// that a kubelet produces them when the controller requests graceful deletion.
func attemptRealDrain(in CaptureDraft, state string, client kubernetes.Interface, rec *brine.Recorder, res brine.Resources) (DrainAttempt, error) {
	ctx := context.Background()
	node, err := client.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{GenerateName: "drain-node-"}}, metav1.CreateOptions{})
	if err != nil {
		return DrainAttempt{}, err
	}
	TrackDisposer(rec, "the drain test node "+node.Name, func() error {
		return releasedIfGone(client.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{}))
	})
	node.Status.Addresses = []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "127.0.0.1"}}
	if _, err := client.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{}); err != nil {
		return DrainAttempt{}, err
	}
	always := corev1.ContainerRestartPolicyAlways
	var pins []string
	if state == "restart during drain" {
		pins = []string{"brine.test/other-owner"}
	}
	pod, err := client.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "drain-producer-", Finalizers: pins},
		Spec: corev1.PodSpec{NodeName: node.Name, RestartPolicy: corev1.RestartPolicyNever,
			Containers:     []corev1.Container{{Name: "main", Image: "busybox"}},
			InitContainers: []corev1.Container{{Name: "init", Image: "busybox"}, {Name: "sidecar", Image: "busybox", RestartPolicy: &always}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return DrainAttempt{}, err
	}
	name := pod.Name
	TrackDisposer(rec, "the drain producer pod "+name, func() error {
		p, e := client.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			return releasedIfGone(e)
		}
		if len(p.Finalizers) > 0 {
			p.Finalizers = nil
			if _, e = client.CoreV1().Pods("default").Update(ctx, p, metav1.UpdateOptions{}); e != nil {
				return e
			}
		}
		zero := int64(0)
		return releasedIfGone(client.CoreV1().Pods("default").Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &zero}))
	})
	pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug", Image: "busybox"}}}
	pod, err = client.CoreV1().Pods("default").UpdateEphemeralContainers(ctx, name, pod, metav1.UpdateOptions{})
	if err != nil {
		return DrainAttempt{}, err
	}
	terminated := func(name string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: name, Image: "busybox", ImageID: "test-image", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}
	}
	pod.Status.Phase = corev1.PodSucceeded
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{terminated("main")}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{terminated("init"), terminated("sidecar")}
	pod.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{terminated("debug")}
	switch state {
	case "terminated", "missing", "replacement":
	case "main running", "init running", "sidecar running", "ephemeral running", "last termination only", "restart during drain":
		pod.Status.Phase = corev1.PodRunning
		status := &pod.Status.ContainerStatuses[0]
		if state == "init running" {
			status = &pod.Status.InitContainerStatuses[0]
		}
		if state == "sidecar running" {
			status = &pod.Status.InitContainerStatuses[1]
		}
		if state == "ephemeral running" {
			status = &pod.Status.EphemeralContainerStatuses[0]
		}
		status.LastTerminationState = status.State
		status.State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	case "restartable snapshot":
		pod.Status.Phase = corev1.PodRunning
	case "missing container status":
		pod.Status.InitContainerStatuses = pod.Status.InitContainerStatuses[:1]
	default:
		return DrainAttempt{}, fmt.Errorf("unknown pod evidence %q", state)
	}
	pod, err = client.CoreV1().Pods("default").UpdateStatus(ctx, pod, metav1.UpdateOptions{})
	if err != nil {
		return DrainAttempt{}, err
	}
	in.PodUID = executioncontrol.PodUID(pod.UID)
	reserved, err := decodeControl[output.ReservedIncarnation](in.Daemon.capture("reserve-incarnation", "/capture/v1/reserve-incarnation", in.Admission.Execution, in.Admission))
	if err != nil {
		return DrainAttempt{}, err
	}
	in.Reserved = reserved
	answer := in.Daemon.capture("hold", "/capture/v1/hold", in.Admission.Execution, holdBody(in.Admission, reserved.Incarnation, in.PodUID))
	ack, err := decodeControl[output.CaptureAcknowledgement](answer)
	if err != nil {
		return DrainAttempt{}, err
	}
	source := heldFrom(in, ack, answer)
	if err := os.WriteFile(filepath.Join(source.incarnationRoot(), "result.txt"), []byte("retained after the producer exits\n"), 0600); err != nil {
		return DrainAttempt{}, err
	}
	if _, err := source.witness(executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		return DrainAttempt{}, err
	}
	admission := source.writerAdmission(freshUUID())
	if _, err := decodeControl[output.CaptureAcknowledgement](in.Daemon.capture("issue-writer-ticket", "/capture/v1/writer-ticket", source.Execution, admission)); err != nil {
		return DrainAttempt{}, err
	}
	started, err := decodeControl[output.SealStarted](in.Daemon.capture("begin-seal", "/capture/v1/seal", source.Execution, output.SealRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: source.Execution, ActivationEpoch: in.Admission.ActivationEpoch,
		HandoffID: in.Admission.HandoffID, Incarnation: reserved.Incarnation, CaptureFence: 1,
		DeadlineAt: output.NewTimestamp(time.Now().Add(time.Minute)),
	}))
	if err != nil {
		return DrainAttempt{}, err
	}
	if state == "missing" || state == "replacement" {
		zero := int64(0)
		if err := client.CoreV1().Pods("default").Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &pod.UID}}); err != nil {
			return DrainAttempt{}, err
		}
		if state == "replacement" {
			copy := pod.DeepCopy()
			copy.ObjectMeta = metav1.ObjectMeta{Name: name}
			copy.Spec.EphemeralContainers = nil
			copy.Status = corev1.PodStatus{}
			if _, err := client.CoreV1().Pods("default").Create(ctx, copy, metav1.CreateOptions{}); err != nil {
				return DrainAttempt{}, err
			}
		}
	}
	port, err := hangarDaemonPort(in.Daemon.Output.URL)
	if err != nil {
		return DrainAttempt{}, err
	}
	cfg := jetbridge.NewConfig("default", "")
	cfg.OutputDaemonPort = port
	cfg.OutputDaemonTLSCert = filepath.Join(in.Daemon.CertDir, "client.crt")
	cfg.OutputDaemonTLSKey = filepath.Join(in.Daemon.CertDir, "client.key")
	cfg.OutputDaemonTLSCACert = filepath.Join(in.Daemon.CertDir, "ca.crt")
	cfg.OutputDaemonTLSServerName = "artifact-daemon"
	controls := jetbridge.NewOutputControls(cfg, jetbridge.NewNodeIPResolver(client), in.Daemon.Minter, in.Admission.ActivationEpoch)
	drain := &jetbridge.OutputDrain{Client: client, Controls: controls, Namespace: "default"}
	drained, drainErr := drain.ConfirmDrain(ctx, node.Name, started)
	attempt := DrainAttempt{Drained: drained, Pod: pod, Err: drainErr}
	if state != "restart during drain" {
		if state == "terminated" {
			current, err := client.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return attempt, err
			}
			attempt.Graceful = current.UID == pod.UID && current.DeletionTimestamp != nil
		}
		return attempt, nil
	}
	if !errors.Is(drainErr, output.ErrSealUnconfirmed) {
		return attempt, fmt.Errorf("first drain should wait for termination: %v", drainErr)
	}
	current, err := client.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return attempt, err
	}
	attempt.Graceful = current.UID == pod.UID && current.DeletionTimestamp != nil && len(current.Finalizers) == 2
	current.Status.Phase = corev1.PodSucceeded
	current.Status.ContainerStatuses = []corev1.ContainerStatus{terminated("main")}
	if _, err := client.CoreV1().Pods("default").UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
		return attempt, err
	}
	// A new controller owns no in-memory evidence from the first pass.
	fresh := &jetbridge.OutputDrain{Client: client, Controls: controls, Namespace: "default"}
	plane, err := newSettlementPlane(source, res)
	if err != nil {
		return attempt, err
	}
	if err := admitControlPlane(plane, source, node.Name); err != nil {
		return attempt, err
	}
	plane.Coordinator.Drain = fresh
	plane.Coordinator.Dialer = hangaroutput.SourceDialerFunc(func(locator string) (hangaroutput.SourceControl, error) { return controls.ForNode(ctx, locator) })
	for pass := 0; pass < 12; pass++ {
		if err := plane.Recoverer.Run(ctx); err != nil {
			return attempt, err
		}
		attempt.Final = mustRead(plane, source.Admission.HandoffID)
		if attempt.Final.ReleaseAcknowledged {
			break
		}
	}
	current, err = client.CoreV1().Pods("default").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return attempt, err
	}
	attempt.RemainingFinalizers = current.Finalizers
	attempt.Err = nil
	return attempt, nil
}
