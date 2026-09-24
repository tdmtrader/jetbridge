package steps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// The barrier changes timing, never request/response contents: a separate real
// exec terminates this owned pod's pause process before forwarding the dial.
type pauseDialBarrier struct {
	next   http.RoundTripper
	before func() error
}

func (t *pauseDialBarrier) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/exec") {
		if err := t.before(); err != nil {
			return nil, err
		}
	}
	return t.next.RoundTrip(req)
}

// A successful CREATE has already reached the real API. Delay its delivery
// until the owned replacement has actually terminated; return its bytes intact.
type pauseCreateBarrier struct {
	next  http.RoundTripper
	path  string
	after func() error
}

func (t *pauseCreateBarrier) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.next.RoundTrip(req)
	if err == nil && req.Method == http.MethodPost && req.URL.Path == t.path && res.StatusCode == http.StatusCreated {
		if err := t.after(); err != nil {
			res.Body.Close()
			return nil, err
		}
	}
	return res, err
}

type PauseRecovery struct {
	worker         WorkerReady
	handle         string
	initial        types.UID
	api, exec      *execObservation
	result         runtime.ProcessResult
	err            error
	stdout, stderr string
	runtimeLog     string
}

func PauseRecoveryDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[LiveTaskPlan, PauseRecovery]("the pause pod stops {string} from {string} before the task can execute",
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (PauseRecovery, error) {
				when, ok := p.GetString(0)
				if !ok {
					return PauseRecovery{}, fmt.Errorf("expected pause timing")
				}
				cause, ok := p.GetString(1)
				if !ok {
					return PauseRecovery{}, fmt.Errorf("expected a pause failure cause")
				}
				return runPauseRecovery(in, rec, when, cause)
			}),
		Assert[PauseRecovery]("the task reports {string} after {int} pod creations and {int} exec attempts",
			func(in PauseRecovery, a Args) error {

				in.api.mu.Lock()
				creates := 0
				for _, r := range in.api.requests {
					if r.method == http.MethodPost && r.path == "/api/v1/namespaces/"+in.worker.Namespace+"/pods" && r.status == http.StatusCreated && r.err == nil {
						creates++
					}
				}
				in.api.mu.Unlock()
				if creates != a.Int(1) {
					return fmt.Errorf("observed %d successful pod creations, want %d", creates, a.Int(1))
				}
				in.exec.mu.Lock()
				defer in.exec.mu.Unlock()
				if len(in.exec.requests) != a.Int(2) {
					return fmt.Errorf("observed %d runtime exec attempts, want %d", len(in.exec.requests), a.Int(2))
				}
				switch a.String(0) {
				case "success", "success with OOM diagnostics", "success with eviction log":
					if in.err != nil || in.result.ExitStatus != 0 || in.stdout != "hello\n" {
						return fmt.Errorf("task did not recover: exit=%d error=%v stdout=%q", in.result.ExitStatus, in.err, in.stdout)
					}
					if a.String(0) == "success with OOM diagnostics" && (!strings.Contains(in.stderr, "Pod Failure Diagnostics") || !strings.Contains(in.stderr, "OOMKilled")) {
						return fmt.Errorf("replacement lost the dead pod diagnostics: %q", in.stderr)
					}
					if a.String(0) == "success with eviction log" {
						if err := requirePauseReplacementLog(in.runtimeLog, in.handle, "Failed", "Evicted"); err != nil {
							return err
						}
					}
				case "startup failure":
					if in.err == nil || !strings.Contains(in.err.Error(), "pod terminated before exec could run") {
						return fmt.Errorf("expected startup failure after replacement was spent, got %v", in.err)
					}
				case "exec failure":
					if in.err == nil || !strings.Contains(in.err.Error(), "exec in pod") {
						return fmt.Errorf("expected exec failure after replacement was spent, got %v", in.err)
					}
				default:
					return fmt.Errorf("unknown expected outcome %q", a.String(0))
				}
				for _, r := range in.exec.requests {
					if r.method != http.MethodPost || r.path != "/api/v1/namespaces/"+in.worker.Namespace+"/pods/"+in.handle+"/exec" || r.query.Get("container") != "main" || r.query.Get("stdin") != "" || r.query.Get("tty") != "" {
						return fmt.Errorf("unexpected runtime exec route/options: %+v", r)
					}
					argv := r.query["command"]
					if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" || !strings.Contains(argv[2], "'/bin/sh' '-c' 'echo hello'") {
						return fmt.Errorf("runtime changed supervised command: %q", argv)
					}
				}
				pods, err := in.worker.Clientset.CoreV1().Pods(in.worker.Namespace).List(in.worker.Ctx, metav1.ListOptions{})
				if err != nil {
					return err
				}
				if len(pods.Items) != 1 || pods.Items[0].Name != in.handle || pods.Items[0].UID == "" || pods.Items[0].UID == in.initial {
					return fmt.Errorf("expected exactly one replacement of the original pod UID %s", in.initial)
				}
				return nil
			}),
	}
}

func runPauseRecovery(in LiveTaskPlan, rec *brine.Recorder, when, cause string) (PauseRecovery, error) {
	secondDeath := cause == "Evicted then TERM"
	if secondDeath {
		if when != "before startup" {
			return PauseRecovery{}, fmt.Errorf("replacement death requires before startup")
		}
		cause = "Evicted"
	}
	if cause != "TERM" && cause != "OOMKilled" && cause != "Evicted" || cause != "TERM" && when == "on both dials" {
		return PauseRecovery{}, fmt.Errorf("unsupported pause death %q at %q", cause, when)
	}
	handle, deaths := "", 0
	switch when {
	case "before startup":
		handle = "pre-running-handle"
	case "on the first dial":
		handle, deaths = "dial-failed-handle", 1
	case "on both dials":
		handle, deaths = "dial-twice-handle", 2
	default:
		return PauseRecovery{}, fmt.Errorf("unknown pause timing %q", when)
	}
	if secondDeath {
		handle = "twice-dead-handle"
	}
	w, err := newLiveRuntimeWorker(in.Database, rec, 1)
	if err != nil {
		return PauseRecovery{}, err
	}
	var runtimeLog liveLogBuffer
	logger := lager.NewLogger("pause-pod")
	logger.RegisterSink(lager.NewWriterSink(&runtimeLog, lager.DEBUG))
	w.Ctx = lagerctx.NewContext(w.Ctx, logger)
	config, err := liveKubernetesConfig()
	if err != nil {
		return PauseRecovery{}, err
	}
	api, execTrace := new(execObservation), new(execObservation)
	raw := w.Executor
	var fixtureErr error
	apiConfig := api.config(config)
	if secondDeath {
		creates := 0
		apiConfig.Wrap(func(next http.RoundTripper) http.RoundTripper {
			return &pauseCreateBarrier{next: next, path: "/api/v1/namespaces/" + w.Namespace + "/pods", after: func() error {
				creates++
				if creates == 2 {
					fixtureErr = stopPausePod(w, raw, handle, "TERM")
				}
				return fixtureErr
			}}
		})
	}
	client, err := kubernetes.NewForConfig(apiConfig)
	if err != nil {
		return PauseRecovery{}, err
	}
	// API creation and exec observations use separate clients. The signal exec
	// retains the original transport and cannot inflate the runtime call count.
	w.Clientset = client
	w = w.rebuild()
	execConfig := execTrace.config(config)
	dials := 0
	execConfig.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return &pauseDialBarrier{next: next, before: func() error {
			dials++
			if dials > deaths {
				return nil
			}
			fixtureErr = stopPausePod(w, raw, handle, cause)
			return fixtureErr
		}}
	})
	w = w.rebuildWith(jetbridge.NewSPDYExecutor(client, execConfig))
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{TeamID: w.TeamID, Dir: "/workdir", ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"}}, nil)
	if err != nil {
		return PauseRecovery{}, err
	}
	// This explicit pre-existing pod gives PID 1 the bounded allocation.
	// Omit the keep-running helper: recovery needs Failed, not Running with
	// a dead main container as the interruption diagnostic fixture does.
	if cause == "OOMKilled" {
		pod := liveOOMPod(handle, corev1.RestartPolicyNever)
		if len(pod.Spec.Containers) != 2 || pod.Spec.Containers[0].Name != "main" || pod.Spec.Containers[1].Name != "keep-running" {
			return PauseRecovery{}, fmt.Errorf("OOM fixture lost main/helper identity")
		}
		pod.Spec.Containers = pod.Spec.Containers[:1]
		pod.Labels = map[string]string{"concourse.ci/worker": w.Worker.Name(), "concourse.ci/type": "task"}
		if _, err := client.CoreV1().Pods(w.Namespace).Create(w.Ctx, pod, metav1.CreateOptions{}); err != nil {
			return PauseRecovery{}, err
		}
	}
	if cause == "Evicted" {
		pod := liveVolumeEvictionPod(handle, true)
		pod.Labels = map[string]string{"concourse.ci/worker": w.Worker.Name(), "concourse.ci/type": "task"}
		if _, err := client.CoreV1().Pods(w.Namespace).Create(w.Ctx, pod, metav1.CreateOptions{}); err != nil {
			return PauseRecovery{}, err
		}
	}
	var stdout, stderr bytes.Buffer
	process, err := container.Run(w.Ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "echo hello"}}, runtime.ProcessIO{Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		return PauseRecovery{}, err
	}
	initial, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: client, Namespace: w.Namespace}, handle)
	if err != nil {
		return PauseRecovery{}, err
	}
	if when == "before startup" {
		if err := stopPausePod(w, raw, handle, cause); err != nil {
			return PauseRecovery{}, err
		}
	}
	result, waitErr := process.Wait(w.Ctx)
	if fixtureErr != nil {
		return PauseRecovery{}, fmt.Errorf("pause-death premise failed: %w", fixtureErr)
	}
	fmt.Printf("real pause recovery %s: initial UID %s, result=%d error=%v stdout=%q\n", when, initial.UID, result.ExitStatus, waitErr, stdout.String())
	return PauseRecovery{worker: w, handle: handle, initial: initial.UID, api: api, exec: execTrace, result: result, err: waitErr, stdout: stdout.String(), stderr: stderr.String(), runtimeLog: runtimeLog.String()}, nil
}

// Stop only the owned pod: TERM or an independently verified 64Mi cgroup OOM.
// Require the kubelet's exact terminal phase and exit, without supplied status.
func stopPausePod(w WorkerReady, executor jetbridge.PodExecutor, handle, cause string) error {
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	before, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, handle)
	if err != nil {
		return err
	}
	if cause == "Evicted" {
		if err := executor.ExecInPod(w.Ctx, w.Namespace, handle, "main", []string{"touch", "/tmp/brine-evict-go"}, nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "arm-bounded-eviction"}); err != nil {
			return err
		}
		_, err := awaitLiveVolumeEviction(w, before)
		return err
	}
	wantPhase, wantExit := corev1.PodSucceeded, int32(0)
	if cause == "OOMKilled" {
		direct, ok := executor.(*jetbridge.SPDYExecutor)
		if !ok {
			return fmt.Errorf("OOM requires the production SPDY executor, got %T", executor)
		}
		if err := exhaustLiveTaskMemory(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, direct, before); err != nil {
			return err
		}
		wantPhase, wantExit = corev1.PodFailed, 137
	} else {
		var stderr bytes.Buffer
		if err := executor.ExecInPod(w.Ctx, w.Namespace, handle, "main", []string{"sh", "-c", "kill -TERM 1"}, nil, nil, &stderr, false, jetbridge.ExecAttrs{Purpose: "pause-signal"}); err != nil {
			return fmt.Errorf("signal owned pause process: %w: %s", err, stderr.String())
		}
	}
	for {
		pod, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.UID != before.UID {
			return fmt.Errorf("pause pod replaced before signal observation")
		}
		if pod.Status.Phase == wantPhase {
			if len(pod.Status.ContainerStatuses) != 1 || pod.Status.ContainerStatuses[0].Name != "main" || pod.Status.ContainerStatuses[0].ContainerID == "" || pod.Status.ContainerStatuses[0].State.Terminated == nil || pod.Status.ContainerStatuses[0].State.Terminated.ExitCode != wantExit || (cause == "OOMKilled" && pod.Status.ContainerStatuses[0].State.Terminated.Reason != "OOMKilled") {
				return fmt.Errorf("pause pod lacks actual %s exit %d: %+v", wantPhase, wantExit, pod.Status)
			}
			fmt.Printf("actual pause %s: pod %s/%s UID %s container %s phase %s exit %d\n", cause, w.Namespace, handle, pod.UID, pod.Status.ContainerStatuses[0].ContainerID, wantPhase, wantExit)
			return nil
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return fmt.Errorf("pause pod reached wrong terminal phase: %+v", pod.Status)
		}
		select {
		case <-w.Ctx.Done():
			return fmt.Errorf("wait for pause %s: %w", cause, w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func requirePauseReplacementLog(raw, handle, phase, reason string) error {
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		var entry struct {
			Message string
			Data    map[string]any
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return fmt.Errorf("decode actual runtime log: %w", err)
		}
		if entry.Message != "pause-pod.recreate-pause-pod.replacing-dead-pause-pod" {
			continue
		}
		if entry.Data["pod"] != handle || entry.Data["phase"] != phase || entry.Data["reason"] != reason {
			return fmt.Errorf("replacement log data %v, want pod=%s phase=%s reason=%s", entry.Data, handle, phase, reason)
		}
		return nil
	}
	return fmt.Errorf("missing actual pause-pod replacement log: %s", raw)
}
