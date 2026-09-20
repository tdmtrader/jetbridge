package steps

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Observations come from the kubelet or the retained pod's /proc, never the
// host process table. The finalizer delays API removal, not process termination.
type CancelledExecOutcome struct {
	Cluster      WorkerReady
	Kind         string
	Handle       string
	PID          string
	Err          error
	stopErr      error
	isolationErr error
	podUID       types.UID
	deletion     *podDeleteObservation
}

func CancelledExecDefinitions() []brine.StepDefinition {
	const cancellation = "an exec-mode {string} step {string} is cancelled {string}"
	return []brine.StepDefinition{
		brine.DefineMap[WorkerReady, CancelledExecOutcome](cancellation,
			func(in WorkerReady, p brine.Params, rec *brine.Recorder) (CancelledExecOutcome, error) {
				return applyAction(cancellation, in, p, func(in WorkerReady, a Args) (CancelledExecOutcome, error) {
					return cancelExec(in, rec, a.String(0), a.String(1), a.String(2))
				})
			}),
		CheckThat[CancelledExecOutcome]("the cancelled command stops and reports cancellation",
			func(in CancelledExecOutcome) error {
				if !errors.Is(in.Err, context.Canceled) {
					return fmt.Errorf("expected context cancellation, got %v", in.Err)
				}
				return errors.Join(in.stopErr, in.isolationErr)
			}),
		CheckString[CancelledExecOutcome]("the cancelled step's pod is {string}", "the cancelled step's pod",
			func(in CancelledExecOutcome) (string, error) {
				if in.Kind == "task" {
					if in.deletion == nil {
						return "", fmt.Errorf("task cancellation has no DELETE observation")
					}
					if err := in.deletion.requireOneImmediateDelete(); err != nil {
						return "", err
					}
				}
				pod, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).Get(in.Cluster.Ctx, in.Handle, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					if in.Kind == "task" {
						pods, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).List(in.Cluster.Ctx, metav1.ListOptions{})
						if err != nil {
							return "", fmt.Errorf("list cancelled task namespace: %w", err)
						}
						if len(pods.Items) != 0 {
							return "", fmt.Errorf("cancelled task namespace still contains %d pods", len(pods.Items))
						}
					}
					return "removed", nil
				}
				if err != nil {
					return "", err
				}
				if pod.UID != in.podUID {
					return "", fmt.Errorf("cancelled pod was replaced")
				}
				return "retained", nil
			}),
	}
}

func cancelExec(w WorkerReady, rec *brine.Recorder, kind, handle, when string) (CancelledExecOutcome, error) {
	out := CancelledExecOutcome{Cluster: w, Kind: kind, Handle: handle}
	if kind != "task" && kind != "get" && kind != "put" && kind != "check" || when != "before-start" && when != "running" {
		return out, fmt.Errorf("unknown cancellation case: %q %q", kind, when)
	}
	if kind == "task" {
		var err error
		w, out.deletion, err = observePodDeletes(w, handle)
		if err != nil {
			return out, err
		}
		out.Cluster = w
	}
	ctx, cancel := context.WithCancel(w.Ctx)
	defer cancel()
	cpu, memory := uint64(250), uint64(64*1024*1024)
	container, _, err := w.Worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerType(kind)}, runtime.ContainerSpec{
			Type: db.ContainerType(kind), Dir: "/tmp", ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"},
			Limits: runtime.ContainerLimits{CPU: &cpu, Memory: &memory}}, nil)
	if err != nil {
		return out, err
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var stdin io.Reader
	if kind != "task" {
		stdin = strings.NewReader("{}")
	}
	process, err := container.Run(ctx, runtime.ProcessSpec{ID: handle, Path: "sh",
		Args: []string{"-c", "trap '' TERM; sleep 60 & child=$!; printf '%s\\n' \"$child\" > /tmp/brine-cancel-child; printf '%s\\n' \"$child\"; wait"}},
		runtime.ProcessIO{Stdin: stdin, Stdout: writer, Stderr: io.Discard})
	if err != nil {
		return out, err
	}
	pod, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(w.Ctx, handle, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	if pod.UID == "" {
		return out, fmt.Errorf("cancellation pod has no UID")
	}
	out.podUID = pod.UID
	if when == "before-start" {
		cancel()
		_, out.Err = process.Wait(ctx)
		if kind != "task" {
			// Absence is an outcome for the retention assertion, not a setup error.
			_, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(w.Ctx, handle, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return out, nil
			}
			if err != nil {
				return out, err
			}
			if _, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, handle); err != nil {
				return out, err
			}
			out.stopErr = w.Executor.ExecInPod(w.Ctx, w.Namespace, handle, "main", []string{"sh", "-c", "test ! -e /tmp/brine-cancel-child"},
				nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "cancel-before-start"})
		}
		return out, nil
	}
	var release func() error
	if kind == "task" {
		release, err = holdLivePodDeletion(w, pod, rec)
		if err != nil {
			return out, err
		}
	}
	done := make(chan error, 1)
	waited := false
	defer func() {
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		if !waited {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				panic("cancelled exec failed to drain")
			}
		}
	}()
	go func() { _, err := process.Wait(ctx); _ = writer.CloseWithError(err); done <- err }()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		return out, fmt.Errorf("wait for real child PID: %w", err)
	}
	out.PID = strings.TrimSpace(line)
	pid, err := strconv.Atoi(out.PID)
	if err != nil || pid <= 1 {
		return out, fmt.Errorf("invalid actual child PID %q", out.PID)
	}
	actual, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, handle)
	if err != nil {
		return out, err
	}
	if actual.UID != pod.UID {
		return out, fmt.Errorf("cancellation pod identity changed")
	}
	state, err := childState(w.Ctx, w, handle, out.PID)
	if err != nil || state == "gone" || state == "Z" || state == "X" {
		return out, fmt.Errorf("child %s was not alive before cancellation: state=%q error=%v", out.PID, state, err)
	}
	if kind == "task" {
		command := "for d in /tmp/concourse-task-*; do [ -f \"$d/pid\" ] && [ -f \"$d/log\" ] && kill -0 \"$(cat \"$d/pid\")\" && exit 0; done; exit 1"
		if err := w.Executor.ExecInPod(w.Ctx, w.Namespace, handle, "main", []string{"sh", "-c", command}, nil, nil, nil, false,
			jetbridge.ExecAttrs{Purpose: "cancel-supervisor-premise"}); err != nil {
			return out, fmt.Errorf("running supervisor has no live state: %w", err)
		}
	}
	fmt.Printf("real cancellation armed %s pod %s/%s UID %s node %s child %s state %s\n", kind, w.Namespace, handle, pod.UID, actual.Spec.NodeName, out.PID, state)
	var sentinel string
	if kind != "task" {
		// An independent exec in the SAME pod must survive resource cancellation.
		var output bytes.Buffer
		if err := w.Executor.ExecInPod(w.Ctx, w.Namespace, handle, "main",
			[]string{"sh", "-c", "sleep 60 </dev/null >/dev/null 2>&1 & printf '%s\\n' \"$!\""},
			nil, &output, nil, false, jetbridge.ExecAttrs{Purpose: "cancel-isolation-premise"}); err != nil {
			return out, err
		}
		sentinel = strings.TrimSpace(output.String())
		pid, err := strconv.Atoi(sentinel)
		if err != nil || pid <= 1 {
			return out, fmt.Errorf("invalid independent process PID %q", sentinel)
		}
		state, err := childState(w.Ctx, w, handle, sentinel)
		if err != nil || state == "gone" || state == "Z" || state == "X" {
			return out, fmt.Errorf("independent process was not alive: state=%s error=%v", state, err)
		}
	}
	cancel()
	select {
	case out.Err = <-done:
		waited = true
	case <-time.After(10 * time.Second):
		return out, fmt.Errorf("cancelled real exec did not finish")
	}
	if kind == "task" {
		out.stopErr = observeTaskTermination(w, handle, pod.UID)
		if out.stopErr == nil {
			if err := release(); err != nil {
				return out, err
			}
			out.stopErr = awaitCancelledPodRemoval(w, handle, pod.UID)
		}
	} else {
		out.stopErr = observeChildStop(w, handle, out.PID)
		state, err := childState(w.Ctx, w, handle, sentinel)
		if err != nil || state == "gone" || state == "Z" || state == "X" {
			out.isolationErr = fmt.Errorf("resource cancellation stopped unrelated process %s: state=%s error=%v", sentinel, state, err)
		} else {
			fmt.Printf("independent process survived cancellation pod %s/%s UID %s PID %s state %s\n", w.Namespace, handle, pod.UID, sentinel, state)
		}
	}
	return out, nil
}

func observeTaskTermination(w WorkerReady, name string, uid types.UID) error {
	ctx, cancel := context.WithTimeout(w.Ctx, 20*time.Second)
	defer cancel()
	for {
		pod, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("observe held task termination: %w", err)
		}
		if pod.UID != uid {
			return fmt.Errorf("cancelled task pod was replaced")
		}
		if pod.DeletionTimestamp != nil {
			for _, c := range pod.Status.ContainerStatuses {
				if c.Name == "main" && c.State.Terminated != nil && !c.State.Terminated.FinishedAt.IsZero() {
					fmt.Printf("kubelet terminated cancelled task %s/%s UID %s exit %d\n", w.Namespace, name, uid, c.State.Terminated.ExitCode)
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled task did not reach kubelet-confirmed termination: phase=%s deleting=%t", pod.Status.Phase, pod.DeletionTimestamp != nil)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func awaitCancelledPodRemoval(w WorkerReady, name string, uid types.UID) error {
	ctx, cancel := context.WithTimeout(w.Ctx, 10*time.Second)
	defer cancel()
	for {
		pod, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if pod.UID != uid {
			return fmt.Errorf("cancelled task replaced")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled task pod was not removed")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func childState(ctx context.Context, w WorkerReady, name, pid string) (string, error) {
	var output strings.Builder
	command := "if [ ! -d \"/proc/$1\" ]; then printf gone; else cat \"/proc/$1/stat\"; fi"
	err := w.Executor.ExecInPod(ctx, w.Namespace, name, "main", []string{"sh", "-c", command, "observe-child", pid}, nil, &output, nil, false,
		jetbridge.ExecAttrs{Purpose: "cancel-child-observation"})
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(output.String())
	if raw == "gone" {
		return raw, nil
	}
	end := strings.LastIndex(raw, ")")
	if end < 0 {
		return "", fmt.Errorf("invalid child proc state %q", raw)
	}
	fields := strings.Fields(raw[end+1:])
	if len(fields) == 0 {
		return "", fmt.Errorf("missing child process state")
	}
	return fields[0], nil
}

func observeChildStop(w WorkerReady, name, pid string) error {
	ctx, cancel := context.WithTimeout(w.Ctx, 10*time.Second)
	defer cancel()
	for {
		state, err := childState(ctx, w, name, pid)
		if err != nil {
			return fmt.Errorf("observe cancelled child: %w", err)
		}
		if state == "gone" || state == "Z" || state == "X" {
			fmt.Printf("cancelled resource child stopped in %s/%s PID %s state %s\n", w.Namespace, name, pid, state)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled resource child %s remains alive in pod %s, state %s", pid, name, state)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
