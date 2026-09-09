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
//   - localExecutor is a real PodExecutor that RUNS the command, in this
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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

func closingStepDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The reattach case: web 1 finished, but died before the exit status
		// was recorded, so the pod survives with no completion annotation.
		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the web dies before the exit status is recorded and a new web takes over",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				pods := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace)
				pod, err := pods.Get(in.Cluster.Ctx, in.Handle, metav1.GetOptions{})
				if err != nil {
					return TaskOutcome{}, fmt.Errorf("get pod %q: %w", in.Handle, err)
				}
				pod.Annotations = nil
				if _, err := pods.Update(in.Cluster.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return TaskOutcome{}, fmt.Errorf("strip completion annotation: %w", err)
				}

				container, err := findTaskContainer(in.Cluster, in.Handle)
				if err != nil {
					return TaskOutcome{}, err
				}

				out := in
				out.Container = container
				_, attachErr := container.Attach(in.Cluster.Ctx, in.Handle, runtime.ProcessIO{})
				out.AttachErr = attachErr
				if attachErr != nil {
					out.AttachMessage = attachErr.Error()
				}
				return out, nil
			},
		),

		// The other side of that story, and the one that pins the WRITE.
		//
		// Above, the annotation is stripped to reach the no-record branch. Here
		// it is left exactly as the finished step wrote it, and a new web
		// attaches. Until this existed, only the READ was covered: the scenario
		// for it hand-builds a pod and spells "concourse.ci/exit-status" itself,
		// so production's annotateExitStatus could write the wrong number and
		// nothing noticed. Measured — writing exitCode+1 left all 328 scenarios
		// green.
		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the web dies after the step finished and a new web takes over",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				// A new container object, which is what a restarted web has:
				// the exit status it held in memory is gone, so the only
				// surviving record is the one the step left on the pod.
				container, err := findTaskContainer(in.Cluster, in.Handle)
				if err != nil {
					return TaskOutcome{}, err
				}

				out := in
				out.Container = container
				out.ExitStatus, out.Err, out.Message = -1, nil, ""

				process, attachErr := container.Attach(in.Cluster.Ctx, in.Handle, runtime.ProcessIO{})
				if attachErr != nil {
					out.AttachErr, out.AttachMessage = attachErr, attachErr.Error()
					out.Err, out.Message = attachErr, attachErr.Error()
					return out, nil
				}
				result, waitErr := process.Wait(in.Cluster.Ctx)
				if waitErr != nil {
					out.Err, out.Message = waitErr, waitErr.Error()
					return out, nil
				}
				out.ExitStatus = result.ExitStatus
				return out, nil
			},
		),

		// ...and then runs the same command again, which must land on the pod
		// that is already there rather than scheduling a second one.
		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the new web runs the same step again",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				out, err := runTask(in.Cluster, in.Handle, in.Script, in.Container)
				if err != nil {
					return out, err
				}
				// The refusal the new web met on the way in is part of this
				// story, and the live state is replaced wholesale.
				out.AttachErr, out.AttachMessage = in.AttachErr, in.AttachMessage
				return out, nil
			},
		),

		// The node takes the pod away before the command can run, and keeps
		// doing it. The ginkgo case asserted this is a TYPED, retryable
		// interruption rather than a plain failure — a different build
		// classification — and no feature file says so yet.
		//
		// It has to keep doing it: a single eviction before the command runs
		// is now absorbed by the one pause-pod replacement the runtime is
		// allowed, so a node that evicts once no longer reaches the build at
		// all. The classification is what the scenario is about, and it is
		// the SECOND eviction that carries it.
		Transform[TaskCluster, TaskOutcome](
			"the node keeps evicting the step {string} before its command runs",
			func(in TaskCluster, a Args) (TaskOutcome, error) {
				evict := func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodFailed
					pod.Status.Reason = "Evicted"
					pod.Status.Message = "The node was low on resource: memory."
				}
				// Every pod this node is given, including the replacement.
				in.Clientset.PrependReactor("create", "pods",
					func(action k8stesting.Action) (bool, apiruntime.Object, error) {
						if pod, ok := action.(k8stesting.CreateActionImpl).GetObject().(*corev1.Pod); ok {
							evict(pod)
						}
						return false, nil, nil
					})
				return runTask(in, a.String(0), "echo unreachable", nil, evict)
			},
		),

		// Removal under concurrency, which the ginkgo probe interleaved and
		// could not assert. Its Remove ran against the same colliding keys as
		// its Record, so "was it removed" had no answer even in principle —
		// another goroutine may have re-recorded it. A separate key set makes
		// it a claim: everything collected concurrently is gone, and nothing
		// collected takes a neighbour with it.
		Refine[ClosingIndex]("{int} more are recorded and collected at the same moment",
			func(in ClosingIndex, a Args) ClosingIndex {
				count := a.Int(0)

				var wg sync.WaitGroup
				for i := range count {
					key := fmt.Sprintf("collected-%d", i)
					in.Collected = append(in.Collected, key)
					in.Index.Record(key, "node-x", "dir-"+key)
					wg.Add(2)
					go func() {
						defer wg.Done()
						in.Index.Remove(key)
					}()
					go func() {
						defer wg.Done()
						in.Index.Locate(key)
					}()
				}
				wg.Wait()
				return in
			}),

		// ------------------------------------------------------------------
		// Checks
		// ------------------------------------------------------------------

		CheckString[TaskOutcome]("the finished step left exit status {string} on its container",
			"the exit status on the container",
			func(in TaskOutcome) (string, error) {
				got, present := in.Props["concourse:exit-status"]
				if !present {
					return "", fmt.Errorf("the container carries no concourse:exit-status property (has %v)", in.Props)
				}
				return got, nil
			}),

		CheckThat[TaskOutcome]("the step's pod is labelled for the worker that owns it",
			func(in TaskOutcome) error {
				got, present := in.PodLabels["concourse.ci/worker"]
				if !present {
					return fmt.Errorf("the pod carries no concourse.ci/worker label (has %v)", in.PodLabels)
				}
				if got != "k8s-worker-1" {
					return fmt.Errorf("expected the pod to name worker %q, got %q", "k8s-worker-1", got)
				}
				return nil
			}),

		// Diagnostics are inspected on a failed Wait, separately from successful
		// command output. The ordinary task log check continues to reject errors.
		CheckContains[TaskOutcome]("the interrupted task's diagnostic log contains {string}",
			"the task diagnostics",
			func(in TaskOutcome) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("expected task diagnostics after an interrupted Wait, but it succeeded")
				}
				return in.Log, nil
			}),

		// The refusal has to have happened at all before its wording means
		// anything, so "it succeeded" is reported from the getter.
		CheckContains[TaskOutcome]("attaching was refused saying {string}",
			"the refusal",
			func(in TaskOutcome) (string, error) {
				if in.AttachErr == nil {
					return "", fmt.Errorf("expected attaching to be refused, but it succeeded")
				}
				return in.AttachMessage, nil
			}),

		// On a mismatch the failure names every pod on the cluster, which is how
		// the duplicate that should not have been scheduled is identified.
		CheckCount[TaskOutcome]("the cluster is running exactly {int} pod for the step",
			"pods on the cluster",
			func(in TaskOutcome) ([]string, error) { return in.Pods, nil }),

		// Keeps its own body: it asserts a TYPE as well as a reason, and the
		// message on the wrong type explains the classification rule — a plain
		// error fails the build where an InterruptionError retries it.
		Assert[TaskOutcome](
			"the step was interrupted rather than failed, because it was {string}",
			func(in TaskOutcome, args Args) error {
				want := args.String(0)

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
	}
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
	body, err := plainTarOfOneFile("cached.txt", content)
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

		Refine[ClosingCachePlan]("the node already holds the resource cache {string} containing {string}",
			func(in ClosingCachePlan, a Args) ClosingCachePlan {
				key, content := a.String(0), a.String(1)
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.node[key] = content
				in.CacheDaemon.mu.Unlock()
				return in
			}),

		Refine[ClosingCachePlan]("the durable store holds {string} containing {string}",
			func(in ClosingCachePlan, a Args) ClosingCachePlan {
				key, content := a.String(0), a.String(1)
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.store[key] = content
				in.CacheDaemon.mu.Unlock()
				return in
			}),

		Refine[ClosingCachePlan]("a peer still holds a mirrored copy of {string} containing {string}",
			func(in ClosingCachePlan, a Args) ClosingCachePlan {
				key, content := a.String(0), a.String(1)
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.mirror[key] = content
				in.CacheDaemon.mu.Unlock()
				return in
			}),

		Refine[ClosingCachePlan]("the daemon predates the durable tier",
			func(in ClosingCachePlan, _ Args) ClosingCachePlan {
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.durableCapable = false
				in.CacheDaemon.mu.Unlock()
				return in
			}),

		Refine[ClosingCachePlan]("the durable store cannot be reached",
			func(in ClosingCachePlan, _ Args) ClosingCachePlan {
				in.CacheDaemon.mu.Lock()
				in.CacheDaemon.storeReachable = false
				in.CacheDaemon.mu.Unlock()
				return in
			}),

		Refine[ClosingCachePlan]("the API has marked the daemon pod not ready",
			func(in ClosingCachePlan, _ Args) ClosingCachePlan {
				in.CacheReady = false
				return in
			}),

		// Registration is the producing half: it is what puts an object into
		// permanent storage under a content key, or does not.
		Transform[ClosingCachePlan, ClosingCachePlan](
			"the ATC registers the resource cache {string} under content key {string}",
			func(in ClosingCachePlan, a Args) (ClosingCachePlan, error) {
				return in, closingRegister(in, a.String(0), a.String(1))
			},
		),

		Transform[ClosingCachePlan, ClosingCachePlan](
			"the ATC registers the resource cache {string} offering no content key",
			func(in ClosingCachePlan, a Args) (ClosingCachePlan, error) {
				return in, closingRegister(in, a.String(0), "")
			},
		),

		Refine[ClosingCachePlan]("the node's own copy of {string} is reclaimed",
			func(in ClosingCachePlan, a Args) ClosingCachePlan {
				key := a.String(0)
				in.CacheDaemon.mu.Lock()
				delete(in.CacheDaemon.node, key)
				in.CacheDaemon.mu.Unlock()
				return in
			}),

		// ------------------------------------------------------------------
		// The consumer's action. Every one of these closes the daemon: the
		// resource plane cannot own an httptest server a step created, and
		// nothing after a When needs it alive.
		// ------------------------------------------------------------------

		Transform[ClosingCachePlan, ClosingCacheLookup](
			"a get step looks up the resource cache {string} offering content key {string}",
			func(in ClosingCachePlan, a Args) (ClosingCacheLookup, error) {
				return closingLookup(in, a.String(0), a.String(1), 1, false)
			},
		),

		Transform[ClosingCachePlan, ClosingCacheLookup](
			"a get step looks up the resource cache {string} offering no content key",
			func(in ClosingCachePlan, a Args) (ClosingCacheLookup, error) {
				return closingLookup(in, a.String(0), "", 1, false)
			},
		),

		Transform[ClosingCachePlan, ClosingCacheLookup](
			"a get step looks up the resource cache {string} offering content key {string} {int} times over",
			func(in ClosingCachePlan, a Args) (ClosingCacheLookup, error) {
				return closingLookup(in, a.String(0), a.String(1), a.Int(2), false)
			},
		),

		// The alias vanishes between the probe and the read — a sweeper ran,
		// or the pod rolled. The bound volume has to find the bytes anyway.
		Transform[ClosingCachePlan, ClosingCacheLookup](
			"a get step looks up the resource cache {string} offering content key {string}, and the node's alias vanishes before the bytes are read",
			func(in ClosingCachePlan, a Args) (ClosingCacheLookup, error) {
				return closingLookup(in, a.String(0), a.String(1), 1, true)
			},
		),

		// ------------------------------------------------------------------
		// Checks
		// ------------------------------------------------------------------

		CheckThat[ClosingCacheLookup]("the resource cache is found",
			func(in ClosingCacheLookup) error {
				if !in.CacheFound {
					return fmt.Errorf("expected the resource cache to be found, it was not")
				}
				return nil
			}),

		CheckThat[ClosingCacheLookup]("the resource cache is not found",
			func(in ClosingCacheLookup) error {
				if in.CacheFound {
					return fmt.Errorf("expected no resource cache, but one was reported found")
				}
				return nil
			}),

		// Keeps its own body: it asserts the archive holds exactly ONE member as
		// well as what that member says. Two independent assertions in one
		// sentence fit no combinator — folding the count into a getter error
		// would turn an assertion into a presumption.
		Assert[ClosingCacheLookup](
			"the cached artifact reads {string}",
			func(in ClosingCacheLookup, args Args) error {
				want := args.String(0)

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
		// Keeps its own body: four expectations in one sentence, plus that
		// partition invariant, is a shape no combinator has.
		Assert[ClosingCacheLookup](
			"the warm counters read {int} local, {int} hit, {int} miss, {int} suppressed",
			func(in ClosingCacheLookup, args Args) error {
				local := args.Int(0)
				hits := args.Int(1)
				misses := args.Int(2)
				suppressed := args.Int(3)

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

		CheckThat[ClosingCacheLookup]("the operator sees no warm activity at all",
			func(in ClosingCacheLookup) error {
				total := in.CacheLocalHits + in.CacheWarmHits + in.CacheWarmMiss + in.CacheSuppressed
				if total != 0 {
					return fmt.Errorf(
						"expected the durable tier to be untouched, but the counters moved: local=%v warmHits=%v warmMisses=%v suppressed=%v",
						in.CacheLocalHits, in.CacheWarmHits, in.CacheWarmMiss, in.CacheSuppressed)
				}
				return nil
			}),
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
	// Collected are keys recorded and then removed concurrently. They are
	// tracked apart from Expecting because the claim about them is the
	// opposite one: none of them may survive.
	Collected []string
}

func closingLocatorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[brine.Empty, ClosingIndex](
			"an empty artifact index",
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder) (ClosingIndex, error) {
				return ClosingIndex{Index: jetbridge.NewArtifactLocator()}, nil
			},
		),

		Refine[ClosingIndex]("the artifact {string} is recorded on node {string}",
			func(in ClosingIndex, a Args) ClosingIndex {
				key, node := a.String(0), a.String(1)
				in.Index.Record(key, node, "")
				in.Expecting = append(in.Expecting, key)
				return in
			}),

		Refine[ClosingIndex]("the artifact {string} is recorded on node {string} in directory {string}",
			func(in ClosingIndex, a Args) ClosingIndex {
				key, node, dir := a.String(0), a.String(1), a.String(2)
				in.Index.Record(key, node, dir)
				in.Expecting = append(in.Expecting, key)
				return in
			}),

		Refine[ClosingIndex]("the artifact {string} is collected",
			func(in ClosingIndex, a Args) ClosingIndex {
				key := a.String(0)
				in.Index.Remove(key)
				kept := in.Expecting[:0:0]
				for _, k := range in.Expecting {
					if k != key {
						kept = append(kept, k)
					}
				}
				in.Expecting = kept
				return in
			}),

		// The ginkgo case this replaces spawned 300 goroutines and asserted
		// nothing at all — it was a race-detector probe, and `make test-unit`
		// does not run with -race, so it proved nothing there either. Distinct
		// keys make it an assertable claim: nothing recorded concurrently may
		// be lost.
		Refine[ClosingIndex]("{int} artifacts are recorded at the same moment",
			func(in ClosingIndex, a Args) ClosingIndex {
				count := a.Int(0)

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
				return in
			}),

		// ------------------------------------------------------------------
		// Checks
		// ------------------------------------------------------------------

		// "not held at all" is how these two say the sentence's premise fails:
		// the index was never asked about a node or a directory for a key it
		// does not carry.
		CheckStringFor[ClosingIndex]("the artifact {string} is held on node {string}",
			"the holding node",
			func(in ClosingIndex, key string) (string, error) {
				node, found := in.Index.LocateNode(key)
				if !found {
					return "", fmt.Errorf("the index does not hold %q at all", key)
				}
				return node, nil
			}),

		CheckStringFor[ClosingIndex]("the artifact {string} is stored in directory {string}",
			"the storage directory",
			func(in ClosingIndex, key string) (string, error) {
				loc, found := in.Index.Locate(key)
				if !found {
					return "", fmt.Errorf("the index does not hold %q at all", key)
				}
				return loc.HostDir, nil
			}),

		// Keeps its own body: CheckNotMember is the shape for a negative that
		// takes a parameter, but it searches a collection, and this index
		// exposes no listing — only a lookup per key — so there is nothing for
		// it to search. The failure names the node the index does claim.
		Assert[ClosingIndex](
			"the artifact {string} is not held anywhere",
			func(in ClosingIndex, args Args) error {
				key := args.String(0)

				if loc, found := in.Index.Locate(key); found {
					return fmt.Errorf("expected %q to be unknown, the index says node %q", key, loc.NodeName)
				}
				return nil
			},
		),

		// The same negative asked through the OTHER lookup, and it is not a
		// restatement. Every negative above goes through Locate; the node
		// lookup is a second entry point with its own found flag, and it is
		// the one production calls — the reaper branches on it to decide
		// whether a destroyed handle can be placed at all, and the input
		// affinity chooser branches on it to decide whether an input counts
		// towards a node. A flag that says yes for a key the index does not
		// hold is invisible to Locate and to every step above: the node it
		// hands back is the zero value, so the only way to see it is to ask
		// the node lookup about a key that was never recorded, or that was
		// collected.
		//
		// The failure prints what the lookup answered, because "yes, node
		// \"\"" is the whole diagnosis.
		Assert[ClosingIndex](
			"no node is named for the artifact {string}",
			func(in ClosingIndex, args Args) error {
				key := args.String(0)

				if node, found := in.Index.LocateNode(key); found {
					return fmt.Errorf("expected the node lookup to name nobody for %q, it answered yes with node %q", key, node)
				}
				return nil
			},
		),

		CheckThat[ClosingIndex]("every artifact that was collected is gone",
			func(in ClosingIndex) error {
				var survived []string
				for _, key := range in.Collected {
					if _, found := in.Index.Locate(key); found {
						survived = append(survived, key)
					}
				}
				if len(survived) > 0 {
					return fmt.Errorf(
						"%d of %d artifacts collected concurrently are still held (%v) — a reaper that "+
							"cannot remove under load leaves the index growing against a disk that is not",
						len(survived), len(in.Collected), abbrev(fmt.Sprint(survived)))
				}
				return nil
			}),

		CheckThat[ClosingIndex]("every artifact that was recorded is still held",
			func(in ClosingIndex) error {
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
			}),
	}
}

// Cancellation keeps resource pods available for hijack, but must delete a
// supervised task's pod: its background command survives loss of the stream.
type CancelledExecOutcome struct {
	Cluster   Cluster
	Workspace TaskWorkspace
	Kind      string
	Handle    string
	PID       string
	Err       error
}

func CancelledExecDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		TransformUsing[brine.Empty, CancelledExecOutcome](
			"an exec-mode {string} step {string} is cancelled {string}",
			[]string{"jetbridge-db", "task-workspace"},
			func(_ brine.Empty, a Args, res brine.Resources) (CancelledExecOutcome, error) {
				workspace, ok := res.Get("task-workspace").(TaskWorkspace)
				if !ok {
					return CancelledExecOutcome{}, fmt.Errorf("missing task workspace")
				}
				cluster, err := NewCluster(res)
				if err != nil {
					return CancelledExecOutcome{}, err
				}
				cluster.Worker.SetExecutor(localExecutor{client: cluster.Clientset, supervisorRoot: workspace.Dir})
				return cancelExec(cluster, workspace, a.String(0), a.String(1), a.String(2))
			}),
		CheckThat[CancelledExecOutcome]("the cancelled command stops and reports cancellation",
			func(in CancelledExecOutcome) error {
				if !errors.Is(in.Err, context.Canceled) {
					return fmt.Errorf("expected context cancellation, got %v", in.Err)
				}
				if in.PID != "" {
					if err := processStopped(in.PID); err != nil {
						return err
					}
					if in.Kind == "task" {
						return in.Workspace.requireSupervisorState()
					}
				}
				return nil
			}),
		CheckString[CancelledExecOutcome]("the cancelled step's pod is {string}",
			"the cancelled step's pod",
			func(in CancelledExecOutcome) (string, error) {
				_, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).Get(in.Cluster.Ctx, in.Handle, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return "removed", nil
				}
				if err != nil {
					return "", err
				}
				return "retained", nil
			}),
	}
}

func cancelExec(cluster Cluster, workspace TaskWorkspace, kind, handle, when string) (CancelledExecOutcome, error) {
	out := CancelledExecOutcome{Cluster: cluster, Workspace: workspace, Kind: kind, Handle: handle}
	if kind != "task" && kind != "get" || when != "before-start" && when != "running" {
		return out, fmt.Errorf("unknown cancellation case: %q %q", kind, when)
	}
	ctx, cancel := context.WithTimeout(cluster.Ctx, 10*time.Second)
	defer cancel()
	container, _, err := cluster.Worker.FindOrCreateContainer(ctx,
		db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: db.ContainerType(kind)},
		runtime.ContainerSpec{TeamID: 1, Type: db.ContainerType(kind),
			ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}}, &noopDelegate{})
	if err != nil {
		return out, err
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var stdin io.Reader
	if kind == "get" {
		stdin = strings.NewReader("{}")
	}
	// Nil task stdin selects the actual supervisor. The workspace argument
	// makes its command hash unique across runs without changing the script.
	process, err := container.Run(ctx, runtime.ProcessSpec{
		Path: "sh", Args: []string{"-c", "sleep 60 & echo $!; wait", workspace.Dir},
	}, runtime.ProcessIO{Stdin: stdin, Stdout: writer, Stderr: io.Discard})
	if err != nil {
		return out, err
	}
	if when == "before-start" {
		cancel()
		_, out.Err = process.Wait(ctx)
		return out, nil
	}
	if err := markPodRunning(ctx, cluster.Clientset, cluster.Namespace, handle); err != nil {
		return out, err
	}
	done := make(chan error, 1)
	go func() {
		_, err := process.Wait(ctx)
		_ = writer.CloseWithError(err)
		done <- err
	}()
	pid, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		return out, fmt.Errorf("wait for running child: %w", err)
	}
	out.PID = strings.TrimSpace(pid)
	cancel()
	select {
	case out.Err = <-done:
	case <-time.After(3 * time.Second):
		return out, fmt.Errorf("cancelled command did not finish")
	}
	return out, nil
}
