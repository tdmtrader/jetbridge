package jetbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/tracing"
	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// execProcess implements runtime.Process using the exec API. Instead of
// baking the command into the Pod spec, it creates a pause Pod and exec-s
// the real command with full stdin/stdout/stderr separation.
type execProcess struct {
	id             string
	podName        string
	clientset      kubernetes.Interface
	config         Config
	container      *Container
	executor       PodExecutor
	processSpec    runtime.ProcessSpec
	processIO      runtime.ProcessIO
	storageBackend StorageBackend

	// pausePodRecreated is the one-shot guard shared by both paths that
	// replace a dead pause pod (waitForRunning, and the exec retry loop).
	// One replacement per step, whichever path spends it: a pod that dies
	// twice is not losing a race, it is telling us something.
	pausePodRecreated bool

	// recreatedPodUID is the Pod UID the one replacement was created with,
	// empty until a replacement was created. An exact execution's start may
	// name a Pod other than the admitted one only when it is this one.
	recreatedPodUID executioncontrol.PodUID

	// execTransportLive records that the exec transport actually carried
	// bytes — the command's own input was read, or its output was written.
	// Past that point the step has started and must never be exec'd a
	// second time, whatever happens to the pod.
	execTransportLive atomic.Bool

	// execAccepted records that the kubelet accepted the step command's
	// exec, after which it starts the command. It is the exec runtime's
	// evidence that a cancelled command started, as main's container status
	// is the job runtime's; execTransportLive is the other, for a command
	// that has talked.
	execAccepted atomic.Bool

	// runningNodeName is retained from readiness, before teardown can remove
	// the Pod whose hostPath holds a cancelled command's partial outputs.
	runningNodeName string

	// control is the optional exact-execution envelope this step was admitted
	// under, and exact is the live control state once the daemon has admitted
	// it. Both are nil on the ordinary path, which is every step that opted
	// into nothing: this file's behaviour is unchanged for them.
	control        *runtime.ExecutionControl
	outputControls OutputControlResolver
	exact          *exactExecution

	// exactCleanupEligible is the ledger's last word on whether this
	// execution's remains may be destroyed. It starts false, which is the
	// fail-closed direction: a Pod that is not proven disposable is kept.
	exactCleanupEligible bool

	// exactOutcomeAcknowledged records that the node's ledger durably holds
	// this execution's outcome -- read back, not merely written. For a
	// capture-selected execution it is what lets Wait end the pause Pod
	// (process_control.go: releaseCapturedPod). Atomic because the stop
	// callback observes from its own goroutine.
	exactOutcomeAcknowledged atomic.Bool

	// admission is the background attempt admitting the exact identity once
	// the pod is bound (process_control.go: admitWhenScheduled).
	admission *admissionAttempt

	// relocatedFrom is every node this step's execution was admitted on and
	// moved off because its pod could not be scheduled there, and
	// relocationSpent records that a move was refused or failed, after which
	// the pod waits where it is (process_relocation.go).
	relocatedFrom   []string
	relocationSpent bool
	// relocationTried is the resourceVersion of the pod a move was last
	// attempted on and left untouched: that pod is not tried again, only a
	// newer observation of it.
	relocationTried string
}

func newExecProcess(
	id, podName string,
	clientset kubernetes.Interface,
	config Config,
	container *Container,
	executor PodExecutor,
	spec runtime.ProcessSpec,
	io runtime.ProcessIO,
	storageBackend StorageBackend,
) *execProcess {
	process := &execProcess{
		id:             id,
		podName:        podName,
		clientset:      clientset,
		config:         config,
		container:      container,
		executor:       executor,
		processSpec:    spec,
		processIO:      io,
		storageBackend: storageBackend,
	}
	if container != nil {
		process.control = container.containerSpec.ExecutionControl
		process.outputControls = container.outputControls
	}

	return process
}

func (p *execProcess) ID() string {
	return p.id
}

// supervised reports whether this process should run under the in-pod task
// supervisor. Only task steps qualify: get/put/check use the stdin/stdout
// resource protocol, which the supervisor's log-file indirection would break.
func (p *execProcess) supervised() bool {
	return p.container != nil &&
		p.container.metadata.Type == db.ContainerTypeTask &&
		p.processIO.Stdin == nil
}

// outputRecordBudget bounds output recording independently of the step's
// deadline: cancellation before or during recording must not drop later
// aliases. Storage failures remain best-effort and are logged by the backend.
const outputRecordBudget = 10 * time.Second

// Wait waits for the pause Pod to reach Running state, streams input
// artifacts into the pod, then exec-s the actual command with ProcessIO
// piped through.
func (p *execProcess) Wait(ctx context.Context) (result runtime.ProcessResult, retErr error) {
	logger := lagerctx.FromContext(ctx).Session("exec-process-wait", lager.Data{
		"pod":        p.podName,
		"process-id": p.id,
	})

	ctx, span := tracing.StartSpan(ctx, "k8s.exec-process.wait", tracing.Attrs{
		"pod-name":   p.podName,
		"process-id": p.id,
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	// A capture-selected execution whose outcome the node has acknowledged
	// gives its pause Pod back, gracefully, on whichever path Wait returns.
	// Registered first so it runs last: after the source-preserving stop
	// below has had its say, and after the exit status is annotated.
	defer p.releaseCapturedPod(logger)

	// The pause Pod is normally left behind when Wait returns: the reaper
	// collects it, and until it does, fly hijack can exec into it.
	//
	// A supervised step whose context has ended is the exception. Its
	// command was started in a session of its own, so that a web restart
	// cannot kill it (see supervisor.go) — which means the teardown of the
	// exec stream does not kill it either. When the
	// context ends the step has been abandoned (abort) or has run out of
	// time (timeout), nobody is coming back for it, and the reaper's fast
	// path skips it because it never recorded an exit status. Left alone it
	// runs until the pause pod's own 24h sleep is up. So delete it, on
	// whichever path Wait returns.
	//
	// Only supervised steps: get/put/check commands are stopped by their
	// invocation-scoped process group below (resource_process.go), retaining
	// the pod for fly hijack into an aborted build's resource containers.
	// And never for a looked-up Container — that is a fly hijack session on somebody else's
	// pod, ending when the operator closes the window.
	recordAbandonedOutputs := false
	defer func() {
		if ctx.Err() != nil && p.supervised() && !p.container.lookedUp {
			p.deleteAbandonedPod(logger)
		}
		// Only a task's outputs are filed: a get or put counts its Dir as an
		// output once it completes, and an abandoned one holds a partial fetch.
		if recordAbandonedOutputs && p.runningNodeName != "" && p.container != nil && p.container.metadata.Type == db.ContainerTypeTask &&
			!p.container.lookedUp && p.storageBackend != nil {
			recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outputRecordBudget)
			defer cancel()
			// Resource commands have already joined their stop defer; a
			// supervised task has had its pause Pod deleted above. Pod deletion
			// can still be reaching the kubelet. Recording is safe during that
			// interval: it files an alias to the live hostPath, not a snapshot
			// of its bytes. The path survives deletion and includes any final
			// writes; consumers of a cancelled task expect partial output.
			// RecordOutputs has no error result: the backend logs failures.
			p.storageBackend.RecordOutputs(recordCtx, p.container.handle, p.runningNodeName, p.container.volumes, p.container.containerSpec)
		}
	}()

	// Deleting an abandoned Pod is destructive quiescence, and for a
	// controlled execution nothing may be destroyed until the ledger says so.
	// The stop is source-preserving and the delete waits on
	// DestructiveCleanupEligible, which for a capture-selected execution stays
	// false while its hold is open -- so an aborted producer's source survives
	// its Pod being reaped rather than going with it.
	defer func() {
		if ctx.Err() == nil || p.control == nil || p.exact == nil {
			return
		}
		quiesceCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		eligible, err := p.stopPreservingSource(lagerctx.NewContext(quiesceCtx, logger))
		if err != nil {
			logger.Error("failed-to-stop-preserving-source", err)
		}
		p.exactCleanupEligible = eligible
	}()

	// A capture-selected task whose acknowledged outcome already released its
	// Pod is answered from its node's ledger, never by waiting for that Pod.
	if recovered, ok, err := p.recoverReleasedCapture(ctx); ok {
		spanErr = err
		return recovered, err
	}

	// Admit the exact identity as soon as the scheduler binds the Pod, which
	// is BEFORE it is running: the capture control init is already retrying
	// its hold inside that Pod, and the daemon refuses a hold for an execution
	// it was never told about. Waiting for Running here would be waiting for
	// the container that is waiting for this.
	joinAdmission := p.admitWhenScheduled(ctx)

	// Wait for the Pod to be running before exec-ing.
	waitCtx, waitSpan := tracing.StartSpan(ctx, "k8s.exec-process.wait-for-running", tracing.Attrs{
		"pod-name": p.podName,
	})
	if err := p.waitForRunning(waitCtx); err != nil {
		tracing.End(waitSpan, err)
		logger.Error("failed-to-wait-for-pod-running", err)
		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(fmt.Errorf("waiting for pod running: %w", err))
	}
	tracing.End(waitSpan, nil)

	// Inputs are already in the pod: the fetch init containers staged them
	// before the main container started.

	// Stream sidecar container logs in parallel with the exec command. Each
	// sidecar streams to its dedicated per-sidecar event writer, so fly watch
	// renders it on its own; the engine makes one per declared sidecar, and a
	// step that wants output (has a Stdout) but lacks one is refused here
	// rather than given a second place for the lines to go. The WaitGroup
	// ensures all sidecar log streams finish before we return (preventing
	// log loss); the cancel ends any still following when we do.
	var stdout io.Writer = p.processIO.Stdout
	var sidecarWriters map[string]io.Writer
	if stdout != nil && p.container != nil {
		sidecarWriters, retErr = sidecarLogWriters(p.container.containerSpec.Sidecars, p.processIO)
		if retErr != nil {
			spanErr = retErr
			return runtime.ProcessResult{}, retErr
		}
	}
	sidecarCtx, cancelSidecars := context.WithCancel(ctx)
	defer cancelSidecars()
	var sidecarWg sync.WaitGroup
	for name, writer := range sidecarWriters {
		sidecarWg.Add(1)
		go func(name string, writer io.Writer) {
			defer sidecarWg.Done()
			defer closeSidecarWriter(writer)
			p.streamSidecarLogs(sidecarCtx, name, writer)
		}(name, writer)
	}

	// Build the command: [path, arg1, arg2, ...]
	command := append([]string{p.processSpec.Path}, p.processSpec.Args...)
	var stopCommand func(context.Context) error

	// Task steps run under an in-pod supervisor so that a web restart can
	// re-exec the same command and take over the still-running process
	// instead of starting a second copy (see supervisor.go).
	//
	// A controlled execution runs under the EXACT supervisor instead, which
	// records its start before the child and refuses to re-run a command whose
	// start it finds with no outcome. The two scripts are two contracts and the
	// call site says which one it chose.
	if p.supervised() {
		if p.control != nil {
			command = exactSupervisorCommand(p.id, p.processSpec)
			stopCommand = func(stopCtx context.Context) error {
				_, err := p.stopPreservingSource(stopCtx)
				return err
			}
		} else {
			command = supervisorCommand(p.id, p.processSpec)
		}
	}

	// A resource command gets its own process group so that cancelling the
	// step can stop it: closing the SPDY stream does not (resource_process.go).
	if p.resourceCommand() {
		var state string
		if p.control != nil {
			// An exact resource command journals its exit where its recorded
			// start says, so the node's ledger has an outcome writer that
			// outlives this ATC (resource_process.go).
			state = exactResourceStateDir(p.control.Identity)
			command = journaledResourceCommand(command, state)
		} else {
			command, state = cancellableResourceCommand(command)
		}
		var stopErr error
		stopCommand = func(stopCtx context.Context) error {
			stopErr = p.cancelResourceCommand(stopCtx, state)
			return stopErr
		}
		defer func() {
			if ctx.Err() != nil {
				// Exact execution joins its stop callback before this defer.
				// Ordinary execution retains its existing cleanup on return.
				if p.control == nil {
					stopErr = p.cancelResourceCommand(context.Background(), state)
					if stopErr != nil {
						logger.Error("failed-to-stop-resource-command", stopErr)
					}
				}
				result = runtime.ProcessResult{}
				retErr = errors.Join(retErr, ctx.Err(), stopErr)
			}
		}()
	}

	// An exact resource command's answer -- its stdout -- is exposed only with
	// an outcome: held here while the transport carries it, and handed to the
	// step once the transport has reported the command's own exit and the
	// node has recorded it. A transport that loses the answer hands the step
	// nothing, and recovery reads the journaled answer instead, so a step
	// never sees half of one and then all of it.
	var exactAnswer *bytes.Buffer
	if p.control != nil && p.resourceCommand() {
		exactAnswer = &bytes.Buffer{}
		stdout = exactAnswer
	}

	// A cancelled or timed-out step reports its context's error on every exact
	// path out, as a resource step does, so the engine aborts a task whose
	// ledger answer was lost instead of erroring it. The ledger calls
	// themselves run detached (process_control.go), so their own errors never
	// carry the step's cancellation.
	if p.control != nil {
		defer func() {
			if retErr != nil && ctx.Err() != nil && !errors.Is(retErr, ctx.Err()) {
				retErr = errors.Join(retErr, ctx.Err())
			}
		}()
	}

	// Everything that must be durable before the command runs: the admission,
	// the revalidated hold, every writer's ticket, and the start record. A
	// refusal here happens before the process and before its mounts.
	var startWitnessErr error
	if p.control != nil {
		if err := joinAdmission(); err != nil {
			spanErr = err
			return runtime.ProcessResult{}, fmt.Errorf("exact execution admission: %w", err)
		}
		if err := p.beginExactCommand(ctx); err != nil {
			if errors.Is(err, errExactReconcile) {
				// A recorded start closes admission even when the original
				// command delivery is uncertain. Read its existing outcome;
				// never use absence of a local journal to launch it again.
				return p.reportExactOutcomeWithoutRerunning(ctx)
			}
			var unretained *startWitnessError
			if !errors.As(err, &unretained) {
				spanErr = err
				return runtime.ProcessResult{}, err
			}
			// The node committed the start and the Run did not retain it.
			// The command is still delivered -- stopped first, below -- so
			// that the node's journal gets an outcome writer; the step then
			// errors with the witness failure whatever the command reported.
			logger.Error("run-start-witness-unretained", err)
			startWitnessErr = err
			defer func() {
				result = runtime.ProcessResult{}
				retErr = errors.Join(startWitnessErr, retErr)
				spanErr = retErr
			}()
		}
	}
	commandCtx := ctx
	if p.control != nil && stopCommand != nil {
		var closeCommand func()
		commandCtx, closeCommand = exactCommandContext(ctx, stopCommand)
		defer closeCommand()
		if startWitnessErr != nil {
			// Stopped delivery. The stop is not a separate request whose
			// failure could let the producer through: the one exec sent in
			// place of the wrapper writes the stop, claims the start and
			// journals the stopped exit -- 143 for a task, 130 for a resource
			// command -- and never names the producer (process_outcome_recovery.go).
			state, resource, _ := p.exactJournal()
			command = exactStoppedDelivery(state, resource)
		}
	}

	// Exec with retry: if the SPDY dial fails because the pause pod was
	// terminated (the GC reaper collecting a previous check's container, a
	// drain, an eviction), replace the pod and retry. This handles the race
	// where the pod goes terminal between waitForRunning and the exec, in
	// whichever phase it lands.
	const maxExecRetries = 2
	var err error
	// The transport is handed watched copies of the step's own streams, so
	// that a retry can tell "the dial never connected" from "the command
	// has started talking". Only the first is safe to run again.
	execStdin := p.watchExecReader(p.processIO.Stdin)
	execStdout := p.watchExecWriter(stdout)
	execStderr := p.watchExecWriter(p.processIO.Stderr)
	for attempt := 0; attempt <= maxExecRetries; attempt++ {
		execCtx, execSpan := tracing.StartSpan(commandCtx, "k8s.exec-process.exec", tracing.Attrs{
			"pod-name": p.podName,
			"attempt":  fmt.Sprintf("%d", attempt),
		})
		err = p.executor.ExecInPod(
			execCtx,
			p.config.Namespace,
			p.podName,
			mainContainerName,
			command,
			execStdin,
			execStdout,
			execStderr,
			p.processSpec.TTY != nil,
			ExecAttrs{Purpose: "step-command", Accepted: func() { p.execAccepted.Store(true) }},
		)
		tracing.End(execSpan, err)
		// A cancelled step reports its cancellation, whatever the torn-down
		// transport made of it -- but an exit the command actually reached
		// before the tear-down (nil, or an ExecExitError) is still its exit.
		// StreamWithContext selects between a completed stream and a done
		// context, so both can be true at once.
		if ctx.Err() != nil {
			var exitErr *ExecExitError
			if err != nil && !errors.As(err, &exitErr) {
				err = ctx.Err()
			}
			break
		}

		if err == nil {
			break
		}
		if p.control != nil {
			// Start was durably recorded before this exec. Even a failed
			// dial is reconciled against that identity, never retried on a
			// fresh Pod or by sending the launch command again.
			break
		}
		// Only retry on transient SPDY exec errors: the pod's container
		// terminated between our readiness check and the exec attempt.
		if !isTransientExecError(err) {
			break
		}
		if attempt == maxExecRetries {
			break
		}
		logger.Info("exec-retry-on-terminated-pod", lager.Data{
			"attempt": attempt + 1,
			"error":   err.Error(),
		})
		// The dial never connected, so nothing of the step has run: replace
		// the dead pause pod and try again.
		if recreateErr := p.recreatePausePod(ctx, nil); recreateErr != nil {
			logger.Info("not-recreating-pause-pod", lager.Data{
				"pod": p.podName,
				"why": recreateErr.Error(),
			})
			break
		}
		// Wait for the new pod to be Running before retrying exec.
		if waitErr := p.waitForRunning(ctx); waitErr != nil {
			logger.Error("failed-to-wait-for-recreated-pod", waitErr)
			break
		}
	}

	// Wait for sidecar log streams to finish (bounded to avoid blocking
	// indefinitely if a sidecar hangs).
	//
	// A long-lived sidecar outlives the command, so its stream ends only when
	// it is cancelled. Cancel it here and join it briefly rather than leaving
	// it to the deferred cancel: closing the stream's writer saves its last
	// line, and that must land inside Wait, ahead of the step's finish event.
	sidecarDone := make(chan struct{})
	go func() {
		sidecarWg.Wait()
		close(sidecarDone)
	}()
	select {
	case <-sidecarDone:
	case <-time.After(execSidecarLogGrace):
		cancelSidecars()
		select {
		case <-sidecarDone:
		case <-time.After(sidecarFlushGrace):
		}
	}

	if err != nil {
		var exitErr *ExecExitError
		if errors.As(err, &exitErr) {
			exitCode := exitErr.ExitCode

			// The exact supervisor's own refusal: it found a recorded start,
			// no outcome and nothing alive. The command may have run, so the
			// ATC asks the ledger rather than deciding, and never re-invokes.
			if p.control != nil && exitCode == ExactUnresolvedExitCode {
				return p.reportExactOutcomeWithoutRerunning(commandCtx)
			}

			// A non-zero exit is not evidence that the command chose it. A Pod
			// destroyed under a running command kills that command, and the
			// exit the transport carries out is the signal's -- 137 for a
			// SIGKILL -- which reads as a step that failed on its own terms.
			// Ask the Pod before the code is believed. This is the same
			// question the no-error path asks below, on the narrower evidence
			// a non-zero exit may use.
			if destroyed := p.confirmPodSurvivedExec(commandCtx, exitCode); destroyed != nil {
				if p.control != nil {
					return p.reportExactOutcomeWithoutRerunning(commandCtx)
				}
				logger.Error("pod-destroyed-during-exec", destroyed)
				spanErr = destroyed
				return runtime.ProcessResult{}, destroyed
			}

			// The outcome is durable before the result is exposed. An exit
			// code the engine has seen and the ledger has not is a terminal
			// answer nothing can corroborate.
			if p.control != nil {
				if recordErr := p.finishExactCommand(commandCtx,
					executioncontrol.ExitOutcome{ExitCode: exitCode},
					executioncontrol.AcknowledgementFinish); recordErr != nil {
					spanErr = recordErr
					return runtime.ProcessResult{}, recordErr
				}
				if err := p.exposeLiveAnswer(exactAnswer); err != nil {
					return runtime.ProcessResult{}, err
				}
			}

			// Upload outputs even on non-zero exit (some steps produce
			// useful artifacts on failure).
			if uploadErr := p.uploadOutputsToArtifactStore(ctx); uploadErr != nil {
				fetchPodFailureContext(ctx, p.clientset, p.config.Namespace, p.podName, p.processIO.Stderr)
				return runtime.ProcessResult{}, fmt.Errorf("uploading artifacts: %w", uploadErr)
			}

			if p.container != nil {
				p.container.SetProperty(exitStatusPropertyName, strconv.Itoa(exitCode))
			}
			p.annotateExitStatus(ctx, exitCode)
			span.SetAttributes(attribute.String("exit-code", strconv.Itoa(exitCode)))
			return runtime.ProcessResult{ExitStatus: exitCode}, nil
		}
		logger.Error("failed-to-exec-in-pod", err)

		// The transport itself has said the error stream closed with no
		// status on it (exec_status.go). Before that check this case came
		// back as a nil error, and the Pod was asked whether it survived;
		// it is asked the same question now, so that a Pod destroyed under
		// its running command is still named as such and is still not
		// retried. A Pod that survived means the stream broke somewhere
		// between here and the kubelet, and the generic path below says so.
		if isExecStatusMissing(err) {
			if destroyed := p.confirmPodSurvivedExec(commandCtx, execExitStatusUnknown); destroyed != nil {
				if p.control != nil {
					return p.reportExactOutcomeWithoutRerunning(commandCtx)
				}
				logger.Error("pod-destroyed-during-exec", destroyed)
				spanErr = destroyed
				return runtime.ProcessResult{}, fmt.Errorf("exec in pod: %w", destroyed)
			}
		}

		// The transport lost its answer for a controlled execution whose
		// command had already started talking. It is not run again; the ledger
		// is asked what is durably known, and "we do not know" stays unresolved
		// rather than becoming a failure.
		if p.control != nil {
			return p.reportExactOutcomeWithoutRerunning(commandCtx)
		}

		// Only a command with evidence it started has outputs to file: an exec
		// still dialling when the step was cancelled ran nothing.
		recordAbandonedOutputs = ctx.Err() != nil && (p.execAccepted.Load() || p.execTransportLive.Load())
		fetchPodFailureContext(ctx, p.clientset, p.config.Namespace, p.podName, p.processIO.Stderr)
		spanErr = err
		return runtime.ProcessResult{}, wrapIfTransient(fmt.Errorf("exec in pod: %w", err))
	}

	// An exec stream that ended without an error is not by itself an exit 0.
	// The command's exit status arrives on the SPDY error stream, so a Pod
	// destroyed mid-exec closes that stream with nothing on it and the
	// transport reports the same nil a clean exit does. The absence of an
	// error is only evidence of success if the container that ran the command
	// is still there to have exited.
	if destroyed := p.confirmPodSurvivedExec(commandCtx, 0); destroyed != nil {
		// Same rule the lost-transport path above follows: for a controlled
		// execution the ledger says what happened, and "we do not know" stays
		// unresolved rather than becoming a success or a failure.
		if p.control != nil {
			return p.reportExactOutcomeWithoutRerunning(commandCtx)
		}
		logger.Error("pod-destroyed-during-exec", destroyed)
		spanErr = destroyed
		return runtime.ProcessResult{}, destroyed
	}

	if p.control != nil {
		if recordErr := p.finishExactCommand(commandCtx,
			executioncontrol.ExitOutcome{ExitCode: 0},
			executioncontrol.AcknowledgementFinish); recordErr != nil {
			spanErr = recordErr
			return runtime.ProcessResult{}, recordErr
		}
		if err := p.exposeLiveAnswer(exactAnswer); err != nil {
			return runtime.ProcessResult{}, err
		}
	}

	// Hand step outputs to the artifact daemon so later steps -- possibly on
	// other nodes -- can fetch them.
	if err := p.uploadOutputsToArtifactStore(ctx); err != nil {
		fetchPodFailureContext(ctx, p.clientset, p.config.Namespace, p.podName, p.processIO.Stderr)
		return runtime.ProcessResult{}, fmt.Errorf("uploading artifacts: %w", err)
	}

	if p.container != nil {
		p.container.SetProperty(exitStatusPropertyName, "0")
	}
	p.annotateExitStatus(ctx, 0)
	span.SetAttributes(attribute.String("exit-code", "0"))
	return runtime.ProcessResult{ExitStatus: 0}, nil
}

// exposeLiveAnswer hands the step the answer the transport carried, once its
// outcome is the node's.
func (p *execProcess) exposeLiveAnswer(answer *bytes.Buffer) error {
	if answer == nil || p.processIO.Stdout == nil {
		return nil
	}
	_, err := p.processIO.Stdout.Write(answer.Bytes())
	return err
}

// confirmPodSurvivedExec reports the destruction of the pause Pod that
// happened while its step's command was running, or nil when the Pod survived
// and the exit the transport reported is the command's own.
//
// Both exit paths ask it, because neither can tell on its own. A Pod deleted,
// evicted, or taken with its node ends the exec stream exactly the way a
// command exiting 0 does, and kills a running command with a signal whose 137
// is exactly the way a command failing on its own terms looks. exitCode is
// what the transport reported -- 0 on the no-error path,
// execExitStatusUnknown when the transport said the error stream closed with
// no status on it -- and it decides which evidence about the Pod is
// admissible.
func (p *execProcess) confirmPodSurvivedExec(ctx context.Context, exitCode int) error {
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(fetchCtx, p.podName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// There is no Pod to write diagnostics for, so the build log
			// gets the one fact there is, in the words the generic
			// exec-error path (fetchPodFailureContext) uses for it.
			if p.processIO.Stderr != nil {
				fmt.Fprintf(p.processIO.Stderr, "\n--- Pod Failure Context ---\nPod %s/%s: pod no longer exists (deleted while the step's command was running)\n",
					p.config.Namespace, p.podName)
			}
			return destroyedDuringExec(runtime.InterruptionPodDeleted,
				fmt.Sprintf("pod %s no longer exists", p.podName), exitCode)
		}
		// An API server we could not reach is not evidence that the step
		// died, and a command that reported its own exit must not lose it.
		return nil
	}

	// Deletion is already in flight: the kubelet is killing the container the
	// command was running in, whatever phase the Pod still reports.
	if pod.DeletionTimestamp != nil {
		return destroyedDuringExec(runtime.InterruptionPodDeleted,
			fmt.Sprintf("pod %s is being deleted", p.podName), exitCode)
	}

	// A classified lifecycle event -- evicted, preempted, node lost -- names
	// what happened, so it outranks the generic diagnosis below.
	if reason, ok := interruptionReasonForPod(pod, false); ok {
		writePodDiagnostics(pod, p.processIO.Stderr)
		writeNodeDiagnostics(ctx, p.clientset, pod, p.processIO.Stderr)
		return destroyedDuringExec(reason,
			fmt.Sprintf("pod %s is %s", p.podName, pod.Status.Phase), exitCode)
	}

	// Past here only a clean exit may go. The pause container outlives every
	// exec -- it sleeps for the life of the step -- so finding it terminated
	// says the Pod died whatever the transport reported. But after a NON-zero
	// exit that reading has nothing to add and one way to be wrong: the
	// command's own code is already a truthful answer, and a destruction that
	// could have overwritten it leaves a DeletionTimestamp or a lifecycle
	// reason, both of which the checks above have already had. Keeping the
	// exit the command gave is the smaller claim, so that is what a non-zero
	// exit gets. An unknown status is not a non-zero exit: nothing was
	// reported, so every reading above and below is admissible.
	if exitCode > 0 {
		return nil
	}

	// A plain `kubectl delete pod` leaves exactly this and sets no Reason the
	// classification above could use: phase Failed, main container
	// ContainerStatusUnknown, "the container could not be located when the pod
	// was terminated".
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == mainContainerName && cs.State.Terminated != nil {
			writePodDiagnostics(pod, p.processIO.Stderr)
			return destroyedDuringExec(runtime.InterruptionPodDeleted, fmt.Sprintf(
				"container %q of pod %s terminated: %s (exit code %d)",
				mainContainerName, p.podName, cs.State.Terminated.Reason,
				cs.State.Terminated.ExitCode), exitCode)
		}
	}

	return nil
}

// destroyedDuringExec names what took the Pod and what the transport made of
// it, and is deliberately NOT a runtime.InterruptionError.
//
// An interruption is retryable, and a retryable step error leaves the build
// started for the tracker to resume, which re-runs the whole plan. That is the
// right answer for a Pod destroyed BEFORE its command ran -- the case
// waitForRunning classifies, recreatePausePod absorbs, and step-closing.feature
// claims -- and the wrong one here: the command has already run, and running it
// again is the second invocation recreatePausePod refuses once the exec
// transport has carried a byte. The reason is still in the message, because an
// operator needs to tell an eviction from a deletion, and so is the exit code,
// because the build log will be showing it.
func destroyedDuringExec(reason runtime.InterruptionReason, evidence string, exitCode int) error {
	transport := fmt.Sprintf("the exec transport reported exit code %d", exitCode)
	if exitCode == execExitStatusUnknown {
		transport = "the exec transport reported no exit status"
	}
	return fmt.Errorf(
		"the step's command was running when its pod was destroyed (%s): %s; %s",
		reason, evidence, transport)
}

// execExitStatusUnknown stands in for an exit code when the transport
// reported none at all (exec_status.go). Exit codes are 0..255, so a negative
// value cannot be mistaken for one.
const execExitStatusUnknown = -1

// deleteAbandonedPod deletes the pause Pod of a step whose context has ended.
// The deletion runs on its own bounded context because the step's context is
// already done, and with no grace period: a task that ignores SIGTERM — a
// privileged nested dockerd, say — would otherwise sit out the pod's
// termination grace period before anything actually stopped.
func (p *execProcess) deleteAbandonedPod(logger lager.Logger) {
	// A controlled execution's Pod is deleted only once the ledger has said
	// its remains may be destroyed. Missing or unprovable truth withholds
	// cleanup, so the Pod stays and an operator can see why.
	if p.control != nil && !p.exactCleanupEligible {
		logger.Info("not-deleting-abandoned-pod", lager.Data{
			"pod": p.podName,
			"why": "the exact execution is not cleanup-eligible; its source may still be held",
		})

		return
	}

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()

	noGrace := int64(0)
	err := p.clientset.CoreV1().Pods(p.config.Namespace).Delete(
		cleanupCtx, p.podName, metav1.DeleteOptions{GracePeriodSeconds: &noGrace},
	)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error("failed-to-delete-abandoned-pod", err)
	}
}

// uploadOutputsToArtifactStore records artifact locations via the storage
// backend. Outputs are already on hostPath — no upload needed.
func (p *execProcess) uploadOutputsToArtifactStore(ctx context.Context) error {
	if p.container == nil || p.storageBackend == nil {
		return nil
	}

	// A completed command owns its outputs even if cancellation arrives
	// between alias registrations rather than before recording starts.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outputRecordBudget)
	defer cancel()
	nodeName := p.fetchPodNodeName(ctx)
	logger := lagerctx.FromContext(ctx).Session("record-output-locations", lager.Data{
		"handle":  p.container.handle,
		"pod":     p.podName,
		"node":    nodeName,
		"volumes": len(p.container.volumes),
		"type":    string(p.container.containerSpec.Type),
	})
	logger.Info("recording-daemonset-artifacts")
	p.storageBackend.RecordOutputs(ctx, p.container.handle, nodeName, p.container.volumes, p.container.containerSpec)
	return nil
}

// annotateExitStatus persists the exit status as a pod annotation so that
// Attach() can recover the result after a web restart (when in-memory
// container properties are lost).
func (p *execProcess) annotateExitStatus(ctx context.Context, exitCode int) {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%d"}}}`, exitStatusAnnotationKey, exitCode)
	_, err := p.clientset.CoreV1().Pods(p.config.Namespace).Patch(
		ctx, p.podName, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		logger := lagerctx.FromContext(ctx).Session("annotate-exit-status")
		logger.Error("failed-to-annotate-exit-status", err, lager.Data{
			"pod":       p.podName,
			"exit-code": exitCode,
		})
	}
}

func (p *execProcess) SetTTY(_ runtime.TTYSpec) error {
	return nil
}

// watchExecReader and watchExecWriter wrap the step's own streams so that the
// first byte the exec transport carries in either direction marks the command
// as started. A nil stream stays nil: the executors check for it.
func (p *execProcess) watchExecReader(r io.Reader) io.Reader {
	if r == nil {
		return nil
	}
	return &liveExecReader{r: r, live: &p.execTransportLive}
}

func (p *execProcess) watchExecWriter(w io.Writer) io.Writer {
	if w == nil {
		return nil
	}
	return &liveExecWriter{w: w, live: &p.execTransportLive}
}

type liveExecReader struct {
	r    io.Reader
	live *atomic.Bool
}

func (l *liveExecReader) Read(b []byte) (int, error) {
	// The transport reads the command's stdin only once it is connected, and
	// a drained reader cannot be replayed into a second exec anyway.
	l.live.Store(true)
	return l.r.Read(b)
}

type liveExecWriter struct {
	w    io.Writer
	live *atomic.Bool
}

func (l *liveExecWriter) Write(b []byte) (int, error) {
	if len(b) > 0 {
		l.live.Store(true)
	}
	return l.w.Write(b)
}

// fetchPodNodeName retrieves the node name where this pod is running.
func (p *execProcess) fetchPodNodeName(ctx context.Context) string {
	if p.clientset == nil {
		return ""
	}
	pod, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
	if err != nil {
		logger := lagerctx.FromContext(ctx).Session("fetch-pod-node-name")
		logger.Error("failed-to-get-pod", err, lager.Data{"pod": p.podName, "namespace": p.config.Namespace})
		return ""
	}
	return pod.Spec.NodeName
}

// streamSidecarLogs streams logs from a sidecar container to its dedicated
// writer using the K8s log API. Retries until the container is ready or the
// context is cancelled.
func (p *execProcess) streamSidecarLogs(ctx context.Context, containerName string, w io.Writer) {
	for {
		req := p.clientset.CoreV1().Pods(p.config.Namespace).GetLogs(p.podName, &corev1.PodLogOptions{
			Follow:    true,
			Container: containerName,
		})

		stream, err := req.Stream(ctx)
		if err == nil {
			io.Copy(w, stream)
			stream.Close()
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}
