package steps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type SidecarPlan struct {
	Database JetbridgeDB
	Routing  string
	Mode     string
}
type SidecarLogs struct {
	Routing, Mode, Stdout, Dedicated, Expected string
	namespace                                  string
	trace                                      *execObservation
}

func SidecarLogDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, SidecarPlan]("a task with {string} sidecar output in {string} mode", []string{"jetbridge-db"}, func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (SidecarPlan, error) {
			routing, ok := p.GetString(0)
			if !ok || (routing != "dedicated" && routing != "fallback") {
				return SidecarPlan{}, fmt.Errorf("sidecar routing must be dedicated or fallback")
			}
			mode, ok := p.GetString(1)
			if !ok || (mode != "exec" && mode != "direct") {
				return SidecarPlan{}, fmt.Errorf("sidecar mode must be exec or direct")
			}
			database, ok := res.Get("jetbridge-db").(JetbridgeDB)
			if !ok {
				return SidecarPlan{}, fmt.Errorf("missing real database")
			}
			return SidecarPlan{Database: database, Routing: routing, Mode: mode}, nil
		}),
		brine.DefineMap[SidecarPlan, SidecarLogs]("the task and its sidecar execute on Kubernetes", func(in SidecarPlan, _ brine.Params, rec *brine.Recorder) (SidecarLogs, error) { return in.run(rec) }),
		CheckThat[SidecarLogs]("every sidecar line reaches the intended build stream", func(in SidecarLogs) error {
			if err := in.requireRuntimeLogRequest(); err != nil {
				return err
			}
			if strings.Count(in.Stdout, "main-only\n") != 1 {
				return fmt.Errorf("main output missing or duplicated: %q", in.Stdout)
			}
			rest := strings.Replace(in.Stdout, "main-only\n", "", 1)
			if in.Routing == "dedicated" {
				if in.Dedicated != in.Expected || rest != "" {
					return fmt.Errorf("dedicated sidecar routing: own=%q main=%q, want own=%q and no sidecar bytes in main", in.Dedicated, in.Stdout, in.Expected)
				}
			} else {
				// The payload includes an empty line and an unterminated final line.
				want := "[helper] " + strings.ReplaceAll(in.Expected, "\n", "\n[helper] ")
				// The direct compatibility formatter adds a final newline; the
				// production exec formatter preserves the unterminated tail.
				if in.Mode == "direct" {
					want += "\n"
				}
				if rest != want || in.Dedicated != "" {
					return fmt.Errorf("fallback sidecar routing: main=%q, want main command plus %q", in.Stdout, want)
				}
			}
			return nil
		}),
	}
}

// Synchronization belongs to the recorder, not a replacement executor. All
// bytes originate in the real task's exec stream or the kubelet's log API.
type liveLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *liveLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *liveLogBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

func (in SidecarPlan) run(rec *brine.Recorder) (SidecarLogs, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cluster, err := newLiveKubernetes(ctx, rec)
	if err != nil {
		return SidecarLogs{}, err
	}
	dw, err := in.Database.PersistNamedWorker("live-sidecar-worker")
	if err != nil {
		return SidecarLogs{}, err
	}
	config := jetbridge.NewConfig(cluster.Namespace, "")
	config.PodStartupTimeout = time.Minute
	config.PodSchedulingTimeout = time.Minute
	trace := &execObservation{}
	client, err := kubernetes.NewForConfig(trace.config(cluster.Config))
	if err != nil {
		return SidecarLogs{}, err
	}
	worker := jetbridge.NewWorker(dw, client, config)
	if in.Mode == "exec" {
		worker.SetExecutor(jetbridge.NewSPDYExecutor(client, cluster.Config))
	}
	marker := "sidecar-" + cluster.Marker
	expected := marker + "\n\n" + marker + "-tail"
	handle := "sidecar-task"
	// No hostPath, privileged flag, test executor, or status update. Namespace
	// admission supplies sidecar limits before the pod can be scheduled.
	cpu, memory := uint64(250), uint64(64*1024*1024)
	container, _, err := worker.FindOrCreateContainer(ctx, db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: db.ContainerTypeTask}, runtime.ContainerSpec{
		ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"}, Limits: runtime.ContainerLimits{CPU: &cpu, Memory: &memory},
		Sidecars: []atc.SidecarConfig{{Name: "helper", Image: "busybox:1.37.0", Command: []string{"sh", "-c", "printf '%s\n\n%s' '" + marker + "' '" + marker + "-tail'"}}},
	}, nil)
	if err != nil {
		return SidecarLogs{}, err
	}
	stdout, stderr, sidecar := new(liveLogBuffer), new(liveLogBuffer), new(liveLogBuffer)
	pio := runtime.ProcessIO{Stdout: stdout, Stderr: stderr}
	if in.Routing == "dedicated" {
		pio.SidecarWriters = map[string]io.Writer{"helper": sidecar}
	}
	process, err := container.Run(ctx, runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "printf 'main-only\n'"}}, pio)
	if err != nil {
		return SidecarLogs{}, err
	}
	if process == nil {
		return SidecarLogs{}, fmt.Errorf("missing real process")
	}
	_, direct := process.(*jetbridge.Process)
	if direct != (in.Mode == "direct") {
		return SidecarLogs{}, fmt.Errorf("wrong execution mode: direct=%v, want %s", direct, in.Mode)
	}
	// Direct Wait deletes the pod, so establish its real source before waiting.
	pod, err := observeSidecarSource(ctx, cluster, handle, expected, direct)
	if err != nil {
		return SidecarLogs{}, err
	}
	fmt.Printf("sidecar source mode=%s routing=%s pod=%s uid=%s node=%s\n", in.Mode, in.Routing, pod.Name, pod.UID, pod.Spec.NodeName)
	result, err := process.Wait(ctx)
	if err != nil || result.ExitStatus != 0 || stderr.String() != "" {
		return SidecarLogs{}, fmt.Errorf("real main task failed: exit=%d error=%v stderr=%q", result.ExitStatus, err, stderr.String())
	}
	return SidecarLogs{Routing: in.Routing, Mode: in.Mode, Stdout: stdout.String(), Dedicated: sidecar.String(), Expected: expected, namespace: cluster.Namespace, trace: trace}, nil
}

func observeSidecarSource(ctx context.Context, cluster liveKubernetes, handle, expected string, direct bool) (*corev1.Pod, error) {
	for {
		pod, err := cluster.Clientset.CoreV1().Pods(cluster.Namespace).Get(ctx, handle, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		var helperDone, mainDone bool
		for _, c := range pod.Status.ContainerStatuses {
			if c.State.Terminated == nil {
				continue
			}
			if c.State.Terminated.ExitCode != 0 {
				return nil, fmt.Errorf("real container %s failed: %+v", c.Name, c.State.Terminated)
			}
			if c.ContainerID != "" {
				helperDone = helperDone || c.Name == "helper"
				mainDone = mainDone || c.Name == "main"
			}
		}
		if helperDone && (!direct || (mainDone && pod.Status.Phase == corev1.PodSucceeded)) {
			if pod.UID == "" || pod.Spec.NodeName == "" {
				return nil, fmt.Errorf("task has no real scheduled pod identity")
			}
			for _, c := range pod.Spec.Containers {
				if c.Resources.Limits.Cpu().IsZero() || c.Resources.Limits.Memory().IsZero() {
					return nil, fmt.Errorf("unbounded container %s", c.Name)
				}
			}
			// This independent source probe uses the unobserved client; it cannot
			// satisfy the assertion about the runtime's streaming request.
			raw, err := cluster.Clientset.CoreV1().Pods(cluster.Namespace).GetLogs(handle, &corev1.PodLogOptions{Container: "helper"}).DoRaw(ctx)
			if err != nil || string(raw) != expected {
				return nil, fmt.Errorf("kubelet source log: got %q, want %q, error %v", raw, expected, err)
			}
			return pod, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for real sidecar source: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (in SidecarLogs) requireRuntimeLogRequest() error {
	if in.trace == nil {
		return fmt.Errorf("missing runtime sidecar log observation")
	}
	in.trace.mu.Lock()
	defer in.trace.mu.Unlock()
	path := "/api/v1/namespaces/" + in.namespace + "/pods/sidecar-task/log"
	for _, req := range in.trace.requests {
		if req.method == "GET" && req.path == path && req.query.Get("container") == "helper" &&
			req.query.Get("follow") == "true" && req.status == 200 && req.err == nil {
			return nil
		}
	}
	return fmt.Errorf("missing successful runtime helper log stream in %s mode", in.Mode)
}
