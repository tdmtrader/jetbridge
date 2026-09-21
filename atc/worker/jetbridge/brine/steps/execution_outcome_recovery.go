package steps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/hangar/executioncontrol"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ExecutionOutcomeRecoveryDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[RunOutputRuntime, CancellationLeaseResult]("a real controlled command loses its completed exec response", []string{"task-workspace"}, func(in RunOutputRuntime, _ brine.Params, rec *brine.Recorder, res brine.Resources) (CancellationLeaseResult, error) {
			return CancellationLeaseResult{Err: exerciseExecutionOutcomeRecovery(in, res.Get("task-workspace").(TaskWorkspace).Dir, rec, "")}, nil
		}),
		brine.DefineMapUsing[RunOutputRuntime, CancellationLeaseResult]("a completed controlled command loses its response with {string}", []string{"task-workspace"}, func(in RunOutputRuntime, p brine.Params, rec *brine.Recorder, res brine.Resources) (CancellationLeaseResult, error) {
			fault, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseExecutionOutcomeRecovery(in, res.Get("task-workspace").(TaskWorkspace).Dir, rec, fault)}, nil
		}),
		brine.DefineMapUsing[RunOutputRuntime, CancellationLeaseResult]("a controlled command reconnects after an uncertain start delivery", []string{"task-workspace"}, func(in RunOutputRuntime, _ brine.Params, rec *brine.Recorder, res brine.Resources) (CancellationLeaseResult, error) {
			return CancellationLeaseResult{Err: exerciseExecutionOutcomeRecovery(in, res.Get("task-workspace").(TaskWorkspace).Dir, rec, "ambiguous start")}, nil
		}),
		CheckThat[CancellationLeaseResult]("its exact outcome survives without repeating the command", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("the execution stays unresolved without repeating the command", func(in CancellationLeaseResult) error { return in.Err }),
		CheckThat[CancellationLeaseResult]("the execution is closed as stopped without running the command", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

// Only the transport completion is lost. The production supervisor and command
// run as real OS processes, and the node control plane is the real TLS daemon.
// The fixture supplies kubelet status through a real Kubernetes API; it does not
// claim to exercise a live kubelet or remote SPDY transport.
type lostOutcomeExecutor struct {
	localExecutor
	fault string
}

func (e lostOutcomeExecutor) ExecInPod(ctx context.Context, namespace, pod, container string, command []string, stdin io.Reader, stdout, stderr io.Writer, tty bool, attrs jetbridge.ExecAttrs) error {
	err := e.localExecutor.ExecInPod(ctx, namespace, pod, container, command, stdin, stdout, stderr, tty, attrs)
	if attrs.Purpose == "step-command" && ctx.Err() == nil {
		if err := e.loseJournal(ctx, namespace, pod); err != nil {
			return err
		}
		return fmt.Errorf("exec completion connection closed: %w", io.ErrUnexpectedEOF)
	}
	return err
}

func (e lostOutcomeExecutor) loseJournal(ctx context.Context, namespace, name string) error {
	switch e.fault {
	case "", "ambiguous start":
		return nil
	case "missing exit", "corrupt exit":
		files, err := filepath.Glob(filepath.Join(e.supervisorRoot, supervisorStateDirectory, "*", "exit"))
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return fmt.Errorf("completed supervisor left no exit to interrupt")
		}
		for _, file := range files {
			if e.fault == "missing exit" {
				err = os.Remove(file)
			} else {
				err = os.WriteFile(file, []byte("pending\n"), 0600)
			}
			if err != nil {
				return err
			}
		}
		return nil
	case "replacement Pod":
		pod, err := e.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		zero := int64(0)
		if err = e.client.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
			return err
		}
		pod.ObjectMeta = metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: pod.Labels}
		pod.Status = corev1.PodStatus{}
		_, err = e.client.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
		return err
	default:
		return fmt.Errorf("unknown completion fault %q", e.fault)
	}
}

func exerciseExecutionOutcomeRecovery(in RunOutputRuntime, workspace string, rec *brine.Recorder, fault string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config := in.Config
	config.OutputPlaneEnabled = true
	config.OutputActivationEpoch = int64(hangarEpoch)
	config.PodSchedulingTimeout = 3 * time.Second
	config.PodStartupTimeout = 3 * time.Second
	row, err := in.Start.DB.PersistNamedWorker("exact-recovery")
	if err != nil {
		return err
	}
	worker := jetbridge.NewWorker(row, in.Client, config)
	worker.SetExecutor(lostOutcomeExecutor{localExecutor: localExecutor{client: in.Client, supervisorRoot: workspace}, fault: fault})
	worker.SetOutputControls(jetbridge.NewOutputControls(config, jetbridge.NewNodeIPResolver(in.Client), in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch)))
	identity := executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(freshUUID()), Fence: 1}
	client := jetbridge.NewOutputControlClient(in.Start.Daemon.Output.URL, in.Start.Daemon.HTTP, in.Start.Daemon.Minter, executioncontrol.ActivationEpoch(hangarEpoch))
	grant, err := client.MintGrant(executioncontrol.BaseFacet, "observe", identity)
	if err != nil {
		return err
	}
	control := runtime.ExecutionControl{Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted, Identity: identity, ActivationEpoch: executioncontrol.ActivationEpoch(hangarEpoch), Endpoint: in.Start.Daemon.Output.URL, Capability: grant}
	handle := "outcome-" + freshUUID()
	metadata := db.ContainerMetadata{Type: db.ContainerTypeTask}
	spec := runtime.ContainerSpec{TeamID: in.Start.Creation.EntryBuilds[0].TeamID(), Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}, ExecutionControl: &control}
	marker := filepath.Join(workspace, "executed")
	command := runtime.ProcessSpec{ID: "recover-outcome", Path: "sh", Args: []string{"-c", `printf x >> "$1"; printf 'completed\n'; exit 7`, "brine", marker}}
	name := jetbridge.GeneratePodName(metadata, handle)
	TrackDisposer(rec, "the recovered outcome pod "+name, func() error {
		zero := int64(0)
		return releasedIfGone(in.Client.CoreV1().Pods(config.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{GracePeriodSeconds: &zero}))
	})
	for attempt := 0; attempt < 2; attempt++ {
		control.Phase = runtime.ControlPhaseAdmitted
		container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), metadata, spec, nil)
		if err != nil {
			return err
		}
		process, err := container.Run(ctx, command, runtime.ProcessIO{Stdout: new(strings.Builder), Stderr: new(strings.Builder)})
		if err != nil {
			return err
		}
		if attempt == 0 {
			if err = in.Client.CoreV1().Pods(config.Namespace).Bind(ctx, &corev1.Binding{ObjectMeta: metav1.ObjectMeta{Name: name}, Target: corev1.ObjectReference{Kind: "Node", Name: in.Node.Name}}, metav1.CreateOptions{}); err != nil {
				return err
			}
			pod, err := in.Client.CoreV1().Pods(config.Namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			pod.Status.Phase = corev1.PodRunning
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "main", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
			if _, err = in.Client.CoreV1().Pods(config.Namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
				return err
			}
			if fault == "ambiguous start" {
				// Interrupt at the production boundary: the original controller
				// committed its start record, then lost command delivery. A later
				// controller cannot infer from the absent journal that no command ran.
				if _, err = client.Admit(ctx, executioncontrol.Envelope{ProtocolVersion: executioncontrol.ProtocolVersion, Identity: identity, ActivationEpoch: control.ActivationEpoch, NodeUID: executioncontrol.NodeUID(in.Node.UID), Capability: grant}); err != nil {
					return err
				}
				if _, err = client.RecordStart(ctx, identity, executioncontrol.PodUID(pod.UID), executioncontrol.ProcessIdentity(command.ID)); err != nil {
					return err
				}
			}
		}
		result, err := process.Wait(ctx)
		if fault == "ambiguous start" {
			// No delivery claimed the start, so nothing in the Pod would ever
			// journal its exit. Reconnect closes it in the Pod as stopped --
			// the claim fences any delivery still in flight -- and never runs it.
			if err != nil || result.ExitStatus != 143 {
				return fmt.Errorf("an undelivered start was not closed as stopped: exit %d, %v", result.ExitStatus, err)
			}
			break
		}
		if fault != "" {
			if !errors.Is(err, jetbridge.ErrExactOutcomeUnresolved) {
				return fmt.Errorf("%s did not preserve an unresolved execution: %v", fault, err)
			}
			break
		}
		if err != nil {
			return fmt.Errorf("completed supervisor outcome was not recovered: %w", err)
		}
		if result.ExitStatus != 7 {
			return fmt.Errorf("recovery returned exit %d", result.ExitStatus)
		}
	}
	executed, err := os.ReadFile(marker)
	if fault == "ambiguous start" {
		if !os.IsNotExist(err) {
			return fmt.Errorf("reconnect launched a command whose delivery was unknown: %q (%v)", executed, err)
		}
	} else if err != nil {
		return err
	} else if string(executed) != "x" {
		return fmt.Errorf("recovery repeated the command: %q", executed)
	}
	observed, err := client.Classify(ctx, identity)
	if err != nil {
		return err
	}
	if fault == "ambiguous start" {
		if observed.Classification != executioncontrol.ClassificationAuthoritativeFinish || observed.Acknowledgement == nil || observed.Acknowledgement.Outcome.ExitCode != 143 {
			return fmt.Errorf("the closed start's journaled stop is not the node's outcome: %+v", observed)
		}
		return nil
	}
	if fault != "" {
		if observed.Classification != executioncontrol.ClassificationExecuting || observed.Acknowledgement != nil {
			return fmt.Errorf("journal loss invented an authoritative outcome")
		}
		return nil
	}
	if observed.Classification != executioncontrol.ClassificationAuthoritativeFinish || observed.Acknowledgement == nil || observed.Acknowledgement.Outcome.ExitCode != 7 {
		return fmt.Errorf("recovery exposed an outcome absent from the node ledger")
	}
	return nil
}
