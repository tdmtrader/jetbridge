package steps

// Closing steps: the executable half of ../features/step-closing.feature.
//
// Three families migrate here, from three ginkgo/go suites:
//
//   1. integration_test.go            — whole-step workflows
//   2. storage_daemonset_durable_test.go — the durable resource-cache tier
//   3. artifact_locator_test.go       — the in-memory artifact index
//
// Every double below is a REAL implementation with a named behavioral
// difference, per coverage_matrix.md Addendum 2:
//
//   - closingShellAdapter is a real PodExecutor that RUNS the command, in this
//     process's shell instead of in a pod. It records nothing. It replaces the
//     `expectSupervisedExec(fakeExecutor.execCalls[0].command, ...)` family,
//     which asserted the shape of a string nothing ever executed.
//
//   - closingDaemon is a real http.Server speaking the artifact daemon's wire
//     contract, holding its artifacts in two maps — a node-local one and a
//     "durable store" — instead of on disk and in a bucket. It records
//     nothing: there is no restores counter, no gotDurableKey. The suite it
//     replaces asserted `d.restores.Load() == 0` and `got.DurableKey == key`;
//     what a consumer actually experiences is whether the bytes arrive, and
//     what the OPERATOR experiences is the four warm counters. Both of those
//     are production output, not a double's memory.
//
// Prefix note: every exported identifier here is `Closing*` because other
// migrations are landing in this package concurrently.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ClosingDefinitions is the single entry point this file exports.
func ClosingDefinitions() []brine.StepDefinition {
	defs := closingStepDefinitions()
	defs = append(defs, closingCacheDefinitions()...)
	defs = append(defs, closingLocatorDefinitions()...)
	return defs
}

// ===========================================================================
// Family 1 — a whole step, end to end (integration_test.go)
// ===========================================================================

// ClosingCluster is a jetbridge worker whose executor really runs commands.
// It is distinct from TaskCluster because these scenarios keep the container
// itself — the properties a restarted web reads back live on it, not on the
// process.
type ClosingCluster struct {
	ClosingNamespace string
	ClosingWorker    *jetbridge.Worker
	ClosingClientset *fake.Clientset
	ClosingCtx       context.Context
}

// ClosingRun is what a consumer and an operator can see after a step has been
// driven to completion: the log, the exit status, the properties the container
// carries, the pods on the cluster, and — after a restart — whether attaching
// was possible.
type ClosingRun struct {
	Cluster   ClosingCluster
	Handle    string
	Script    string
	Container runtime.Container

	Log        string
	ExitStatus int
	Err        error
	Message    string

	Props     map[string]string
	Pods      []string
	PodLabels map[string]string

	AttachErr     error
	AttachMessage string
}

// closingShellAdapter is a real PodExecutor: it runs the command it is given.
//
// Its named behavioral difference is that the command runs in this process's
// shell rather than in a pod. It records nothing, so no scenario below can
// assert what string was assembled — only what came back out.
type closingShellAdapter struct{}

func (closingShellAdapter) ExecInPod(
	ctx context.Context,
	_, _, _ string,
	command []string,
	_ io.Reader,
	stdout, stderr io.Writer,
	_ bool,
	_ jetbridge.ExecAttrs,
) error {
	if len(command) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &jetbridge.ExecExitError{ExitCode: exitErr.ExitCode()}
	}
	return err
}

func closingStepDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ClosingCluster](
			"a jetbridge worker driving a whole step from end to end",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ClosingCluster, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ClosingCluster{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}

				dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return ClosingCluster{}, err
				}

				namespace := "ci-namespace"
				clientset := fake.NewSimpleClientset()
				worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig(namespace, ""))
				worker.SetExecutor(closingShellAdapter{})

				return ClosingCluster{
					ClosingNamespace: namespace,
					ClosingWorker:    worker,
					ClosingClientset: clientset,
					ClosingCtx:       context.Background(),
				}, nil
			},
		),

		// The whole workflow the ginkgo case called "create container → run →
		// wait → exit", with the command really running.
		brine.DefineMap[ClosingCluster, ClosingRun](
			"the step {string} runs {string} and finishes",
			func(in ClosingCluster, p brine.Params, _ *brine.Recorder) (ClosingRun, error) {
				handle, _ := p.GetString(0)
				script, ok := p.GetString(1)
				if !ok {
					return ClosingRun{}, fmt.Errorf("expected a handle and a command")
				}
				return closingRunStep(in, handle, script, nil)
			},
		),

		// The reattach case: web 1 finished, but died before the exit status
		// was recorded, so the pod survives with no completion annotation.
		brine.DefineMap[ClosingRun, ClosingRun](
			"the web dies before the exit status is recorded and a new web takes over",
			func(in ClosingRun, _ brine.Params, _ *brine.Recorder) (ClosingRun, error) {
				pods := in.Cluster.ClosingClientset.CoreV1().Pods(in.Cluster.ClosingNamespace)
				pod, err := pods.Get(in.Cluster.ClosingCtx, in.Handle, metav1.GetOptions{})
				if err != nil {
					return ClosingRun{}, fmt.Errorf("get pod %q: %w", in.Handle, err)
				}
				pod.Annotations = nil
				if _, err := pods.Update(in.Cluster.ClosingCtx, pod, metav1.UpdateOptions{}); err != nil {
					return ClosingRun{}, fmt.Errorf("strip completion annotation: %w", err)
				}

				container, err := closingContainer(in.Cluster, in.Handle)
				if err != nil {
					return ClosingRun{}, err
				}

				out := in
				out.Container = container
				_, attachErr := container.Attach(in.Cluster.ClosingCtx, in.Handle, runtime.ProcessIO{})
				out.AttachErr = attachErr
				if attachErr != nil {
					out.AttachMessage = attachErr.Error()
				}
				return out, nil
			},
		),

		// ...and then runs the same command again, which must land on the pod
		// that is already there rather than scheduling a second one.
		brine.DefineMap[ClosingRun, ClosingRun](
			"the new web runs the same step again",
			func(in ClosingRun, _ brine.Params, _ *brine.Recorder) (ClosingRun, error) {
				out, err := closingRunStep(in.Cluster, in.Handle, in.Script, in.Container)
				if err != nil {
					return out, err
				}
				// The refusal the new web met on the way in is part of this
				// story, and the live state is replaced wholesale.
				out.AttachErr, out.AttachMessage = in.AttachErr, in.AttachMessage
				return out, nil
			},
		),

		// The node takes the pod away before the command can run. The ginkgo
		// case asserted this is a TYPED, retryable interruption rather than a
		// plain failure — a different build classification — and no feature
		// file says so yet.
		brine.DefineMap[ClosingCluster, ClosingRun](
			"the node evicts the step {string} before its command runs",
			func(in ClosingCluster, p brine.Params, _ *brine.Recorder) (ClosingRun, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ClosingRun{}, fmt.Errorf("expected a handle parameter")
				}
				return closingRunStep(in, handle, "echo unreachable", nil, func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodFailed
					pod.Status.Reason = "Evicted"
					pod.Status.Message = "The node was low on resource: memory."
				})
			},
		),

		// ------------------------------------------------------------------
		// Checks
		// ------------------------------------------------------------------

		brine.DefineCheck[ClosingRun](
			"the finished step left exit status {string} on its container",
			func(in ClosingRun, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an exit status parameter")
				}
				got, present := in.Props["concourse:exit-status"]
				if !present {
					return fmt.Errorf("the container carries no concourse:exit-status property (has %v)", in.Props)
				}
				if got != want {
					return fmt.Errorf("expected exit status %q on the container, got %q", want, got)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingRun](
			"the step's pod is labelled for the worker that owns it",
			func(in ClosingRun, _ brine.Params, _ *brine.Recorder) error {
				got, present := in.PodLabels["concourse.ci/worker"]
				if !present {
					return fmt.Errorf("the pod carries no concourse.ci/worker label (has %v)", in.PodLabels)
				}
				if got != "k8s-worker-1" {
					return fmt.Errorf("expected the pod to name worker %q, got %q", "k8s-worker-1", got)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingRun](
			"the finished step reported exit {int}",
			func(in ClosingRun, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected an exit status parameter")
				}
				if in.Err != nil {
					return fmt.Errorf("expected exit %d, but the step errored: %v", want, in.Err)
				}
				if in.ExitStatus != want {
					return fmt.Errorf("expected exit %d, got %d (log: %q)", want, in.ExitStatus, in.Log)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingRun](
			"the step's output reached the build log as {string}",
			func(in ClosingRun, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an output parameter")
				}
				if !strings.Contains(in.Log, want) {
					return fmt.Errorf("expected the build log to contain %q, got %q", want, in.Log)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingRun](
			"attaching was refused saying {string}",
			func(in ClosingRun, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a reason parameter")
				}
				if in.AttachErr == nil {
					return fmt.Errorf("expected attaching to be refused saying %q, but it succeeded", want)
				}
				if !strings.Contains(in.AttachMessage, want) {
					return fmt.Errorf("expected the refusal to say %q, got %q", want, in.AttachMessage)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingRun](
			"the cluster is running exactly {int} pod for the step",
			func(in ClosingRun, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected a pod count parameter")
				}
				if len(in.Pods) != want {
					return fmt.Errorf("expected %d pod on the cluster, found %d: %v", want, len(in.Pods), in.Pods)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingRun](
			"the step was interrupted rather than failed, because it was {string}",
			func(in ClosingRun, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an interruption reason parameter")
				}
				if in.Err == nil {
					return fmt.Errorf("expected an interruption saying %q, but the step succeeded", want)
				}
				var interruption runtime.InterruptionError
				if !errors.As(in.Err, &interruption) {
					return fmt.Errorf(
						"expected a retryable runtime.InterruptionError, got %T: %v — a plain error fails the build instead of retrying it",
						in.Err, in.Err)
				}
				if string(interruption.InterruptionReason()) != want {
					return fmt.Errorf("expected interruption reason %q, got %q",
						want, interruption.InterruptionReason())
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingRun](
			"the diagnostics in the build log explain {string}",
			func(in ClosingRun, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a diagnostic parameter")
				}
				if !strings.Contains(in.Log, want) {
					return fmt.Errorf("expected the build log to explain %q, got %q", want, in.Log)
				}
				return nil
			},
		),
	}
}

// closingContainer finds or creates the runtime container for a handle.
func closingContainer(in ClosingCluster, handle string) (runtime.Container, error) {
	container, _, err := in.ClosingWorker.FindOrCreateContainer(
		in.ClosingCtx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			TeamName:  "main",
			Dir:       "/tmp/build/workdir",
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///ubuntu:22.04"},
			Type:      db.ContainerTypeTask,
		},
		&noopDelegate{},
	)
	if err != nil {
		return nil, fmt.Errorf("find or create container %q: %w", handle, err)
	}
	return container, nil
}

// closingRunStep drives one step to completion and collects everything a
// consumer or an operator can see afterwards. `shape`, when given, puts the
// pod into a failure shape instead of running it.
func closingRunStep(
	in ClosingCluster,
	handle, script string,
	existing runtime.Container,
	shape ...func(*corev1.Pod),
) (ClosingRun, error) {
	container := existing
	if container == nil {
		var err error
		if container, err = closingContainer(in, handle); err != nil {
			return ClosingRun{}, err
		}
	}

	log := new(bytes.Buffer)
	process, err := container.Run(in.ClosingCtx,
		runtime.ProcessSpec{
			ID:   handle,
			Path: "/bin/sh",
			Args: []string{"-c", script},
			Dir:  "/tmp/build/workdir",
		},
		runtime.ProcessIO{Stdout: log, Stderr: log},
	)
	if err != nil {
		return ClosingRun{}, fmt.Errorf("run step %q: %w", handle, err)
	}

	pods := in.ClosingClientset.CoreV1().Pods(in.ClosingNamespace)
	pod, err := pods.Get(in.ClosingCtx, handle, metav1.GetOptions{})
	if err != nil {
		return ClosingRun{}, fmt.Errorf("get pod %q: %w", handle, err)
	}
	if len(shape) > 0 {
		shape[0](pod)
	} else {
		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		}
	}
	if _, err := pods.UpdateStatus(in.ClosingCtx, pod, metav1.UpdateOptions{}); err != nil {
		return ClosingRun{}, fmt.Errorf("update pod status: %w", err)
	}

	result, waitErr := process.Wait(in.ClosingCtx)

	out := ClosingRun{
		Cluster: in, Handle: handle, Script: script, Container: container,
		Log: log.String(), ExitStatus: result.ExitStatus, Err: waitErr,
	}
	if waitErr != nil {
		out.Message = waitErr.Error()
	}

	props, err := container.Properties()
	if err != nil {
		return ClosingRun{}, fmt.Errorf("read container properties: %w", err)
	}
	out.Props = props

	listed, err := pods.List(in.ClosingCtx, metav1.ListOptions{})
	if err != nil {
		return ClosingRun{}, fmt.Errorf("list pods: %w", err)
	}
	for _, item := range listed.Items {
		out.Pods = append(out.Pods, item.Name)
		if item.Name == handle {
			out.PodLabels = item.Labels
		}
	}

	return out, nil
}

// ===========================================================================
// Family 2 — the durable resource-cache tier (storage_daemonset_durable_test.go)
// ===========================================================================

// ClosingCachePlan is a cluster with one artifact daemon in it, under
// description. Refinement steps take ClosingCachePlan in and out, so a
// scenario may say what the node holds, what the store holds, and what the
// daemon is capable of, in any order.
//
// Nothing is wired until a When step: readiness has to be decided before the
// EndpointSlice is published.
type ClosingCachePlan struct {
	CacheCtx    context.Context
	CacheDaemon *closingDaemon
	CacheServer *httptest.Server
	CacheHost   string
	CachePort   int
	CacheReady  bool
}

// ClosingCacheLookup is what a consumer got — the bytes, or nothing — and what
// the operator's four warm counters recorded while it happened.
//
// The counters are here rather than in a spy because they are PRODUCTION
// output. `d.restores.Load()`, which the ginkgo suite asserted, is not
// observable by anyone.
type ClosingCacheLookup struct {
	CacheFound   bool
	CacheFiles   map[string]string
	CacheReadErr error
	CacheMessage string

	CacheLookups    int
	CacheLocalHits  float64
	CacheWarmHits   float64
	CacheWarmMiss   float64
	CacheSuppressed float64
}

// closingDaemon is a real artifact daemon: an http.Handler over two maps.
//
// `node` is what this node has on disk; `store` is the durable bucket behind
// the whole DaemonSet; `mirror` is what a peer holds under steps/. Its named
// behavioral difference from the deployed daemon is that all three are maps in
// this process. It records nothing.
type closingDaemon struct {
	mu     sync.Mutex
	node   map[string]string
	store  map[string]string
	mirror map[string]string

	durableCapable bool
	storeReachable bool
}

func newClosingDaemon() *closingDaemon {
	return &closingDaemon{
		node:           map[string]string{},
		store:          map[string]string{},
		mirror:         map[string]string{},
		durableCapable: true,
		storeReachable: true,
	}
}

func (d *closingDaemon) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()

		// Capability rides every response, at any status — that is how an ATC
		// learns the cluster can warm at all without probing a route that may
		// not exist.
		if d.durableCapable {
			w.Header().Set(jetbridge.DurableTierHeader, "enabled")
		}

		switch {
		case strings.HasPrefix(r.URL.Path, "/resource-caches/"):
			key := strings.TrimPrefix(r.URL.Path, "/resource-caches/")
			if _, held := d.node[key]; held {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)

		case r.Method == http.MethodPost && r.URL.Path == "/durable/restore":
			var body struct {
				Key        string `json:"key"`
				DurableKey string `json:"durable_key"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)

			if !d.storeReachable {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			content, inStore := d.store[body.DurableKey]
			if !inStore {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			// A restore makes its own answer true: the object lands on this
			// node under the local alias the ATC asked for.
			d.node[body.Key] = content
			w.Header().Set("X-Artifact-Tier", "durable")
			w.WriteHeader(http.StatusCreated)

		case r.Method == http.MethodPost && r.URL.Path == "/register":
			var body struct {
				Key        string `json:"key"`
				LocalPath  string `json:"local_path"`
				DurableKey string `json:"durable_key"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)

			// The two names are different namespaces. Only a content key gets
			// the object filed in permanent storage; a bare local alias is a
			// Postgres row id, and filing that is the defect the content key
			// exists to prevent.
			if body.DurableKey != "" {
				d.store[body.DurableKey] = d.node[body.Key]
			}
			w.WriteHeader(http.StatusCreated)

		case strings.HasPrefix(r.URL.Path, "/artifacts/steps/"):
			key := strings.TrimPrefix(r.URL.Path, "/artifacts/steps/")
			content, held := d.mirror[key]
			if !held {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			closingServeTar(w, r, content)

		case strings.HasPrefix(r.URL.Path, "/artifacts/"):
			key := strings.TrimPrefix(r.URL.Path, "/artifacts/")
			content, held := d.node[key]
			if !held {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			closingServeTar(w, r, content)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// closingServeTar answers with the one-member archive a daemon serves.
func closingServeTar(w http.ResponseWriter, r *http.Request, content string) {
	body, err := closingTar("cached.txt", content)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// closingTar builds the uncompressed archive the daemon serves. The package's
// tarOfOneFile gzips, and the daemon's /artifacts route serves raw tar.
func closingTar(name, content string) ([]byte, error) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, fmt.Errorf("write tar header: %w", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return nil, fmt.Errorf("write tar body: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar: %w", err)
	}
	return raw.Bytes(), nil
}

func closingCacheDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, ClosingCachePlan](
			"an artifact daemon with a durable store behind it",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				daemon := newClosingDaemon()
				server := httptest.NewServer(daemon.handler())

				host, port, err := hostAndPort(server)
				if err != nil {
					server.Close()
					return ClosingCachePlan{}, err
				}

				return ClosingCachePlan{
					CacheCtx:    context.Background(),
					CacheDaemon: daemon,
					CacheServer: server,
					CacheHost:   host,
					CachePort:   port,
					CacheReady:  true,
				}, nil
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCachePlan](
			"the node already holds the resource cache {string} containing {string}",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				key, _ := p.GetString(0)
				content, ok := p.GetString(1)
				if !ok {
					return in, fmt.Errorf("expected a cache key and its content")
				}
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.node[key] = content
				in.CacheDaemon.mu.Unlock()
				return in, nil
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCachePlan](
			"the durable store holds {string} containing {string}",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				key, _ := p.GetString(0)
				content, ok := p.GetString(1)
				if !ok {
					return in, fmt.Errorf("expected a content key and its content")
				}
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.store[key] = content
				in.CacheDaemon.mu.Unlock()
				return in, nil
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCachePlan](
			"a peer still holds a mirrored copy of {string} containing {string}",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				key, _ := p.GetString(0)
				content, ok := p.GetString(1)
				if !ok {
					return in, fmt.Errorf("expected a cache key and its content")
				}
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.mirror[key] = content
				in.CacheDaemon.mu.Unlock()
				return in, nil
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCachePlan](
			"the daemon predates the durable tier",
			func(in ClosingCachePlan, _ brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.durableCapable = false
				in.CacheDaemon.mu.Unlock()
				return in, nil
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCachePlan](
			"the durable store cannot be reached",
			func(in ClosingCachePlan, _ brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.storeReachable = false
				in.CacheDaemon.mu.Unlock()
				return in, nil
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCachePlan](
			"the API has marked the daemon pod not ready",
			func(in ClosingCachePlan, _ brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				in.CacheReady = false
				return in, nil
			},
		),

		// Registration is the producing half: it is what puts an object into
		// permanent storage under a content key, or does not.
		brine.DefineMap[ClosingCachePlan, ClosingCachePlan](
			"the ATC registers the resource cache {string} under content key {string}",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				key, _ := p.GetString(0)
				durableKey, ok := p.GetString(1)
				if !ok {
					return in, fmt.Errorf("expected a cache key and a content key")
				}
				return in, closingRegister(in, key, durableKey)
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCachePlan](
			"the ATC registers the resource cache {string} offering no content key",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				key, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected a cache key")
				}
				return in, closingRegister(in, key, "")
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCachePlan](
			"the node's own copy of {string} is reclaimed",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCachePlan, error) {
				key, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected a cache key")
				}
				in.CacheDaemon.mu.Lock()
				delete(in.CacheDaemon.node, key)
				in.CacheDaemon.mu.Unlock()
				return in, nil
			},
		),

		// ------------------------------------------------------------------
		// The consumer's action. Every one of these closes the daemon: the
		// resource plane cannot own an httptest server a step created, and
		// nothing after a When needs it alive.
		// ------------------------------------------------------------------

		brine.DefineMap[ClosingCachePlan, ClosingCacheLookup](
			"a get step looks up the resource cache {string} offering content key {string}",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCacheLookup, error) {
				key, _ := p.GetString(0)
				durableKey, ok := p.GetString(1)
				if !ok {
					return ClosingCacheLookup{}, fmt.Errorf("expected a cache key and a content key")
				}
				return closingLookup(in, key, durableKey, 1, false)
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCacheLookup](
			"a get step looks up the resource cache {string} offering no content key",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCacheLookup, error) {
				key, ok := p.GetString(0)
				if !ok {
					return ClosingCacheLookup{}, fmt.Errorf("expected a cache key")
				}
				return closingLookup(in, key, "", 1, false)
			},
		),

		brine.DefineMap[ClosingCachePlan, ClosingCacheLookup](
			"a get step looks up the resource cache {string} offering content key {string} {int} times over",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCacheLookup, error) {
				key, _ := p.GetString(0)
				durableKey, _ := p.GetString(1)
				times, ok := p.GetInt(2)
				if !ok {
					return ClosingCacheLookup{}, fmt.Errorf("expected a cache key, a content key and a count")
				}
				return closingLookup(in, key, durableKey, times, false)
			},
		),

		// The alias vanishes between the probe and the read — a sweeper ran,
		// or the pod rolled. The bound volume has to find the bytes anyway.
		brine.DefineMap[ClosingCachePlan, ClosingCacheLookup](
			"a get step looks up the resource cache {string} offering content key {string}, and the node's alias vanishes before the bytes are read",
			func(in ClosingCachePlan, p brine.Params, _ *brine.Recorder) (ClosingCacheLookup, error) {
				key, _ := p.GetString(0)
				durableKey, ok := p.GetString(1)
				if !ok {
					return ClosingCacheLookup{}, fmt.Errorf("expected a cache key and a content key")
				}
				return closingLookup(in, key, durableKey, 1, true)
			},
		),

		// ------------------------------------------------------------------
		// Checks
		// ------------------------------------------------------------------

		brine.DefineCheck[ClosingCacheLookup](
			"the resource cache is found",
			func(in ClosingCacheLookup, _ brine.Params, _ *brine.Recorder) error {
				if !in.CacheFound {
					return fmt.Errorf("expected the resource cache to be found, it was not")
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingCacheLookup](
			"the resource cache is not found",
			func(in ClosingCacheLookup, _ brine.Params, _ *brine.Recorder) error {
				if in.CacheFound {
					return fmt.Errorf("expected no resource cache, but one was reported found")
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingCacheLookup](
			"the cached artifact reads {string}",
			func(in ClosingCacheLookup, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected the expected content")
				}
				if in.CacheReadErr != nil {
					return fmt.Errorf("reading the cached artifact failed: %v", in.CacheReadErr)
				}
				if len(in.CacheFiles) != 1 {
					return fmt.Errorf("expected one member in the cached artifact, got %d: %v",
						len(in.CacheFiles), in.CacheFiles)
				}
				for name, got := range in.CacheFiles {
					if got != want {
						return fmt.Errorf("expected the cached artifact %q to read %q, got %q", name, want, got)
					}
				}
				return nil
			},
		),

		// The four counters must partition every lookup that reaches the
		// daemon. If they do not, no ratio computed from them means anything,
		// and answering "is this tier earning its egress?" is impossible.
		brine.DefineCheck[ClosingCacheLookup](
			"the warm counters read {int} local, {int} hit, {int} miss, {int} suppressed",
			func(in ClosingCacheLookup, p brine.Params, _ *brine.Recorder) error {
				local, _ := p.GetInt(0)
				hits, _ := p.GetInt(1)
				misses, _ := p.GetInt(2)
				suppressed, ok := p.GetInt(3)
				if !ok {
					return fmt.Errorf("expected four counter values")
				}

				if in.CacheLocalHits != float64(local) ||
					in.CacheWarmHits != float64(hits) ||
					in.CacheWarmMiss != float64(misses) ||
					in.CacheSuppressed != float64(suppressed) {
					return fmt.Errorf(
						"expected local=%d warmHits=%d warmMisses=%d suppressed=%d, got %v/%v/%v/%v",
						local, hits, misses, suppressed,
						in.CacheLocalHits, in.CacheWarmHits, in.CacheWarmMiss, in.CacheSuppressed)
				}

				total := in.CacheLocalHits + in.CacheWarmHits + in.CacheWarmMiss + in.CacheSuppressed
				if total != float64(in.CacheLookups) {
					return fmt.Errorf(
						"the counters sum to %v across %d lookups; they must partition every outcome exactly once",
						total, in.CacheLookups)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingCacheLookup](
			"the operator sees no warm activity at all",
			func(in ClosingCacheLookup, _ brine.Params, _ *brine.Recorder) error {
				total := in.CacheLocalHits + in.CacheWarmHits + in.CacheWarmMiss + in.CacheSuppressed
				if total != 0 {
					return fmt.Errorf(
						"expected the durable tier to be untouched, but the counters moved: local=%v warmHits=%v warmMisses=%v suppressed=%v",
						in.CacheLocalHits, in.CacheWarmHits, in.CacheWarmMiss, in.CacheSuppressed)
				}
				return nil
			},
		),
	}
}

// closingConfig is the ATC-side config. The warm timeout is deliberately
// short: a scenario that somehow wedged on an unanswered restore must fail
// fast rather than sit on the 90s default.
func (p ClosingCachePlan) closingConfig() jetbridge.Config {
	return jetbridge.Config{
		Namespace:                 "cicd",
		ArtifactDaemonService:     "artifact-daemon",
		ArtifactDaemonPort:        p.CachePort,
		ArtifactDaemonHostPath:    "/artifact-store",
		ArtifactDaemonWarmTimeout: 5 * time.Second,
	}
}

// closingCluster publishes the daemon in an EndpointSlice the way the
// DaemonSet's Service does — including the readiness condition the API
// reports, which is the whole subject of two scenarios.
func (p ClosingCachePlan) closingCluster() *fake.Clientset {
	ready := p.CacheReady
	return fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-brine",
			Namespace: "cicd",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{p.CacheHost},
			NodeName:   closingPtr("node-a"),
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		}},
	})
}

func (p ClosingCachePlan) closingClient(cs *fake.Clientset) *jetbridge.DaemonClient {
	return jetbridge.NewDaemonClient(
		lagertest.NewTestLogger("brine-closing"),
		cs, "cicd", "artifact-daemon", p.CachePort, nil,
	)
}

func closingPtr[T any](v T) *T { return &v }

func closingRegister(p ClosingCachePlan, key, durableKey string) error {
	cs := p.closingCluster()
	client := p.closingClient(cs)
	if err := client.RegisterAlias(p.CacheCtx, key, "/artifact-store/steps/"+key, durableKey); err != nil {
		return fmt.Errorf("register alias %q: %w", key, err)
	}
	return nil
}

// closingLookup runs the consumer's action N times and reads the artifact the
// last lookup handed back.
func closingLookup(p ClosingCachePlan, key, durableKey string, times int, sweep bool) (ClosingCacheLookup, error) {
	defer p.CacheServer.Close()

	cs := p.closingCluster()
	backend := jetbridge.NewDaemonSetBackend(p.closingConfig(), jetbridge.NewArtifactLocator(), nil)
	backend.SetDaemonClient(p.closingClient(cs))

	// Drain whatever earlier scenarios left on the process-wide counters, so
	// what follows is this scenario's own.
	closingDrainWarmCounters()

	out := ClosingCacheLookup{CacheLookups: times}
	var vol runtime.Volume
	for range times {
		v, found := backend.FindResourceCache(p.CacheCtx, key, durableKey, "k8s-worker-1")
		vol, out.CacheFound = v, found
	}

	out.CacheLocalHits = metric.Metrics.ResourceCacheLocalHits.Delta()
	out.CacheWarmHits = metric.Metrics.DurableWarmHits.Delta()
	out.CacheWarmMiss = metric.Metrics.DurableWarmMisses.Delta()
	out.CacheSuppressed = metric.Metrics.DurableWarmSuppressed.Delta()

	if !out.CacheFound || vol == nil {
		return out, nil
	}

	if sweep {
		p.CacheDaemon.mu.Lock()
		delete(p.CacheDaemon.node, key)
		p.CacheDaemon.mu.Unlock()
	}

	stream, err := vol.StreamOut(p.CacheCtx, ".", nil)
	if err != nil {
		out.CacheReadErr, out.CacheMessage = err, err.Error()
		return out, nil
	}
	raw, readErr := io.ReadAll(stream)
	_ = stream.Close()
	if readErr != nil {
		out.CacheReadErr, out.CacheMessage = readErr, readErr.Error()
		return out, nil
	}

	_, _, files, _ := decodeArchive(raw)
	out.CacheFiles = files
	return out, nil
}

func closingDrainWarmCounters() {
	metric.Metrics.ResourceCacheLocalHits.Delta()
	metric.Metrics.DurableWarmHits.Delta()
	metric.Metrics.DurableWarmMisses.Delta()
	metric.Metrics.DurableWarmSuppressed.Delta()
}

// ===========================================================================
// Family 3 — the artifact index (artifact_locator_test.go)
// ===========================================================================

// ClosingIndex is the in-memory index the ATC keeps of which node holds which
// artifact. There is no cluster, no double and no database in this family: the
// seam is a data structure, and every assertion re-queries it.
type ClosingIndex struct {
	Index     *jetbridge.ArtifactLocator
	Expecting []string
}

func closingLocatorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, ClosingIndex](
			"an empty artifact index",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (ClosingIndex, error) {
				return ClosingIndex{Index: jetbridge.NewArtifactLocator()}, nil
			},
		),

		brine.DefineMap[ClosingIndex, ClosingIndex](
			"the artifact {string} is recorded on node {string}",
			func(in ClosingIndex, p brine.Params, _ *brine.Recorder) (ClosingIndex, error) {
				key, _ := p.GetString(0)
				node, ok := p.GetString(1)
				if !ok {
					return in, fmt.Errorf("expected an artifact key and a node")
				}
				in.Index.Record(key, node, "")
				in.Expecting = append(in.Expecting, key)
				return in, nil
			},
		),

		brine.DefineMap[ClosingIndex, ClosingIndex](
			"the artifact {string} is recorded on node {string} in directory {string}",
			func(in ClosingIndex, p brine.Params, _ *brine.Recorder) (ClosingIndex, error) {
				key, _ := p.GetString(0)
				node, _ := p.GetString(1)
				dir, ok := p.GetString(2)
				if !ok {
					return in, fmt.Errorf("expected an artifact key, a node and a directory")
				}
				in.Index.Record(key, node, dir)
				in.Expecting = append(in.Expecting, key)
				return in, nil
			},
		),

		brine.DefineMap[ClosingIndex, ClosingIndex](
			"the artifact {string} is collected",
			func(in ClosingIndex, p brine.Params, _ *brine.Recorder) (ClosingIndex, error) {
				key, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected an artifact key")
				}
				in.Index.Remove(key)
				kept := in.Expecting[:0:0]
				for _, k := range in.Expecting {
					if k != key {
						kept = append(kept, k)
					}
				}
				in.Expecting = kept
				return in, nil
			},
		),

		// The ginkgo case this replaces spawned 300 goroutines and asserted
		// nothing at all — it was a race-detector probe, and `make test-unit`
		// does not run with -race, so it proved nothing there either. Distinct
		// keys make it an assertable claim: nothing recorded concurrently may
		// be lost.
		brine.DefineMap[ClosingIndex, ClosingIndex](
			"{int} artifacts are recorded at the same moment",
			func(in ClosingIndex, p brine.Params, _ *brine.Recorder) (ClosingIndex, error) {
				count, ok := p.GetInt(0)
				if !ok {
					return in, fmt.Errorf("expected a count")
				}

				var wg sync.WaitGroup
				for i := range count {
					key := fmt.Sprintf("concurrent-%d", i)
					in.Expecting = append(in.Expecting, key)
					wg.Add(2)
					go func() {
						defer wg.Done()
						in.Index.Record(key, "node-x", "dir-"+key)
					}()
					go func() {
						defer wg.Done()
						in.Index.Locate(key)
					}()
				}
				wg.Wait()
				return in, nil
			},
		),

		// ------------------------------------------------------------------
		// Checks
		// ------------------------------------------------------------------

		brine.DefineCheck[ClosingIndex](
			"the artifact {string} is held on node {string}",
			func(in ClosingIndex, p brine.Params, _ *brine.Recorder) error {
				key, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected an artifact key and a node")
				}
				node, found := in.Index.LocateNode(key)
				if !found {
					return fmt.Errorf("the index does not hold %q at all", key)
				}
				if node != want {
					return fmt.Errorf("expected %q to be held on %q, the index says %q", key, want, node)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingIndex](
			"the artifact {string} is stored in directory {string}",
			func(in ClosingIndex, p brine.Params, _ *brine.Recorder) error {
				key, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected an artifact key and a directory")
				}
				loc, found := in.Index.Locate(key)
				if !found {
					return fmt.Errorf("the index does not hold %q at all", key)
				}
				if loc.HostDir != want {
					return fmt.Errorf("expected %q to be stored in %q, the index says %q", key, want, loc.HostDir)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingIndex](
			"the artifact {string} is not held anywhere",
			func(in ClosingIndex, p brine.Params, _ *brine.Recorder) error {
				key, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an artifact key")
				}
				if loc, found := in.Index.Locate(key); found {
					return fmt.Errorf("expected %q to be unknown, the index says node %q", key, loc.NodeName)
				}
				return nil
			},
		),

		brine.DefineCheck[ClosingIndex](
			"every artifact that was recorded is still held",
			func(in ClosingIndex, _ brine.Params, _ *brine.Recorder) error {
				missing := []string{}
				for _, key := range in.Expecting {
					if _, found := in.Index.Locate(key); !found {
						missing = append(missing, key)
					}
				}
				if len(missing) > 0 {
					return fmt.Errorf("%d of %d recorded artifacts were lost from the index: %v",
						len(missing), len(in.Expecting), missing)
				}
				return nil
			},
		),
	}
}

// CancelledExecOutcome is what survives after an exec-mode step is cancelled.
type CancelledExecOutcome struct {
	Namespace string
	Clientset *fake.Clientset
	Ctx       context.Context
	Handle    string
	Err       error
	Message   string
}

// CancelledExecDefinitions covers PE-10's exec-mode half: a cancelled step
// KEEPS its pause pod, because `fly hijack` into that pod is how an operator
// finds out why a build was cancelled. The direct-mode rule is the opposite,
// and applying it here would delete the evidence.
func CancelledExecDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, CancelledExecOutcome](
			"an exec-mode step {string} that is waiting for its pod",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (CancelledExecOutcome, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return CancelledExecOutcome{}, fmt.Errorf("expected a handle parameter")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return CancelledExecOutcome{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return CancelledExecOutcome{}, err
				}
				ctx := context.Background()
				namespace := "test-namespace"
				clientset := fake.NewSimpleClientset()
				worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig(namespace, ""))
				worker.SetExecutor(execStub{})

				container, _, err := worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeGet},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/get",
						ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
						Type:      db.ContainerTypeGet,
					},
					&noopDelegate{},
				)
				if err != nil {
					return CancelledExecOutcome{}, fmt.Errorf("find or create container: %w", err)
				}
				process, err := container.Run(ctx,
					runtime.ProcessSpec{Path: "/opt/resource/in", Args: []string{"/tmp/build/get"}},
					runtime.ProcessIO{
						Stdin:  strings.NewReader("{}"),
						Stdout: new(bytes.Buffer),
						Stderr: new(bytes.Buffer),
					},
				)
				if err != nil {
					return CancelledExecOutcome{}, fmt.Errorf("run step: %w", err)
				}

				// The pod never reaches Running; the build is cancelled while
				// the step is still waiting for it.
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				_, waitErr := process.Wait(cancelCtx)
				msg := ""
				if waitErr != nil {
					msg = waitErr.Error()
				}
				return CancelledExecOutcome{
					Namespace: namespace, Clientset: clientset, Ctx: ctx,
					Handle: handle, Err: waitErr, Message: msg,
				}, nil
			},
		),

		brine.DefineMap[CancelledExecOutcome, CancelledExecOutcome](
			"the build is cancelled before the pod ever starts",
			func(in CancelledExecOutcome, _ brine.Params, _ *brine.Recorder) (CancelledExecOutcome, error) {
				return in, nil
			},
		),

		brine.DefineCheck[CancelledExecOutcome](
			"the step reports the cancellation",
			func(in CancelledExecOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Err == nil {
					return fmt.Errorf("expected the cancelled step to report an error; it reported success")
				}
				return nil
			},
		),

		brine.DefineCheck[CancelledExecOutcome](
			"the pod {string} is still there for the operator",
			func(in CancelledExecOutcome, p brine.Params, _ *brine.Recorder) error {
				name, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pod name parameter")
				}
				_, err := in.Clientset.CoreV1().Pods(in.Namespace).
					Get(in.Ctx, name, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf(
						"expected the pause pod %q to survive cancellation so `fly hijack` can reach it; "+
							"it is gone (%v) — the direct-mode delete rule must not be applied to exec mode",
						name, err)
				}
				return nil
			},
		),
	}
}
