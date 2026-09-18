package steps

// Closing steps: the executable half of ../features/step-closing.feature.
//
// Three families migrate here, from three ginkgo/go suites:
//
//   1. integration_test.go            — whole-step workflows
//   2. storage_daemonset_durable_test.go — the durable resource-cache tier
//   3. artifact_locator_test.go       — the in-memory artifact index
//
// Cache scenarios use production daemons and a real filesystem durable store.
// Whole-step execution and typed eviction are covered by the live features.
//
// Prefix note: every exported identifier here is `Closing*` because other
// migrations are landing in this package concurrently.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// Capture business failures for the scenario's exit-status assertion.
func attachTaskResult(in TaskOutcome, container runtime.Container) TaskOutcome {
	out := in
	out.Container = container
	out.ExitStatus, out.Err, out.Message = -1, nil, ""
	out.AttachErr, out.AttachMessage = nil, ""
	process, err := container.Attach(in.Cluster.Ctx, in.Handle, runtime.ProcessIO{})
	if err != nil {
		out.AttachErr, out.AttachMessage = err, err.Error()
		out.Err, out.Message = err, err.Error()
		return out
	}
	result, err := process.Wait(in.Cluster.Ctx)
	if err != nil {
		out.Err, out.Message = err, err.Error()
		return out
	}
	out.ExitStatus = result.ExitStatus
	return out
}

func attachFinishedTask(in TaskOutcome, container runtime.Container) (TaskOutcome, error) {
	out := attachTaskResult(in, container)
	if out.Err != nil {
		return out, nil
	}
	pod, err := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace).
		Get(in.Cluster.Ctx, in.podName(), metav1.GetOptions{})
	if err != nil {
		return TaskOutcome{}, fmt.Errorf("read recovered task pod: %w", err)
	}
	fmt.Printf("live task reattach %s/%s UID %s exited %d\n",
		in.Cluster.Namespace, pod.Name, pod.UID, out.ExitStatus)
	return out, nil
}

// recoverTaskWithoutPod keeps the original runtime object, not a completion
// value supplied by the fixture. The command's real Wait populated its memory.
func recoverTaskWithoutPod(in TaskOutcome) (TaskOutcome, error) {
	if in.Err != nil || in.completedContainer == nil || in.completedPodUID == "" {
		return TaskOutcome{}, fmt.Errorf("memory recovery requires a completed real task")
	}
	ctx, cancel := context.WithTimeout(in.Cluster.Ctx, 20*time.Second)
	defer cancel()
	pods := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace)
	pod, err := pods.Get(ctx, in.podName(), metav1.GetOptions{})
	if err != nil {
		return TaskOutcome{}, err
	}
	if pod.UID != in.completedPodUID {
		return TaskOutcome{}, fmt.Errorf("refuse to delete a replacement task pod")
	}
	grace := int64(1)
	if err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &grace, Preconditions: &metav1.Preconditions{UID: &pod.UID},
	}); err != nil {
		return TaskOutcome{}, err
	}
	for {
		current, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		if err != nil {
			return TaskOutcome{}, err
		}
		if current.UID != pod.UID {
			return TaskOutcome{}, fmt.Errorf("task pod was replaced during deletion")
		}
		select {
		case <-ctx.Done():
			return TaskOutcome{}, fmt.Errorf("task pod must be absent before memory recovery: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	fmt.Printf("actual completed task pod %s/%s UID %s is absent before original-container recovery\n", in.Cluster.Namespace, pod.Name, pod.UID)
	out := attachTaskResult(in, in.completedContainer)
	// A successful cached Attach must not create a replacement pod either.
	if _, err := pods.Get(ctx, pod.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return TaskOutcome{}, fmt.Errorf("task pod must remain absent after memory recovery: %v", err)
	}
	fmt.Printf("live task memory-only recovery %s/%s UID %s exited %d error=%v\n", in.Cluster.Namespace, pod.Name, pod.UID, out.ExitStatus, out.Err)
	return out, nil
}

func closingStepDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the original web recovers the finished task after its pod is removed",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				return recoverTaskWithoutPod(in)
			},
		),

		// The reattach case: web 1 finished, but died before the exit status
		// was recorded, so the pod survives with no completion annotation.
		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the web dies before the exit status is recorded and a new web takes over",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				pods := in.Cluster.Clientset.CoreV1().Pods(in.Cluster.Namespace)
				pod, err := pods.Get(in.Cluster.Ctx, in.podName(), metav1.GetOptions{})
				if err != nil {
					return TaskOutcome{}, fmt.Errorf("get pod %q: %w", in.podName(), err)
				}
				pod.Annotations = nil
				if _, err := pods.Update(in.Cluster.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return TaskOutcome{}, fmt.Errorf("strip completion annotation: %w", err)
				}

				container, _, err := findTaskContainer(in.Cluster, in.Handle)
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

		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the current web reattaches to the finished task",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				return attachFinishedTask(in, in.Container)
			},
		),

		// Read the status production wrote to the real pod. A fresh runtime
		// container cannot use the previous object's in-memory completion.
		brine.DefineMap[TaskOutcome, TaskOutcome](
			"the web dies after the step finished and a new web takes over",
			func(in TaskOutcome, _ brine.Params, _ *brine.Recorder) (TaskOutcome, error) {
				// A new container object, which is what a restarted web has:
				// the exit status it held in memory is gone, so the only
				// surviving record is the one the step left on the pod.
				container, _, err := findTaskContainer(in.Cluster, in.Handle)
				if err != nil {
					return TaskOutcome{}, err
				}

				return attachFinishedTask(in, container)
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
		Assert[StepOutcome](
			"the step was interrupted rather than failed, because it was {string}",
			func(in StepOutcome, args Args) error {
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
	CacheCtx       context.Context
	CacheRuntime   *closingCacheRuntime
	CacheLocal     map[string]string
	CacheDurable   map[string]string
	CachePeer      map[string]string
	CacheReady     bool
	CacheCapable   bool
	CacheReachable bool
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

func closingCacheDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ClosingCachePlan](
			"an artifact daemon with a durable store behind it",
			[]string{"real-cluster"},
			func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ClosingCachePlan, error) {
				api, ok := res.Get("real-cluster").(*realCluster)
				if !ok {
					return ClosingCachePlan{}, fmt.Errorf("real-cluster resource is %T", res.Get("real-cluster"))
				}
				return ClosingCachePlan{CacheCtx: context.Background(), CacheRuntime: &closingCacheRuntime{api: api, rec: rec},
					CacheLocal: map[string]string{}, CacheDurable: map[string]string{}, CachePeer: map[string]string{},
					CacheReady: true, CacheCapable: true, CacheReachable: true}, nil
			}),

		Refine[ClosingCachePlan]("the node already holds the resource cache {string} containing {string}",
			func(in ClosingCachePlan, a Args) ClosingCachePlan {
				key, content := a.String(0), a.String(1)
				in.CacheLocal[key] = content
				return in
			}),

		Refine[ClosingCachePlan]("the durable store holds {string} containing {string}",
			func(in ClosingCachePlan, a Args) ClosingCachePlan {
				key, content := a.String(0), a.String(1)
				in.CacheDurable[key] = content
				return in
			}),

		Refine[ClosingCachePlan]("a peer still holds a mirrored copy of {string} containing {string}",
			func(in ClosingCachePlan, a Args) ClosingCachePlan {
				key, content := a.String(0), a.String(1)
				in.CachePeer[key] = content
				return in
			}),

		Refine[ClosingCachePlan]("the daemon predates the durable tier",
			func(in ClosingCachePlan, _ Args) ClosingCachePlan {
				in.CacheCapable = false
				return in
			}),

		Refine[ClosingCachePlan]("the durable store cannot be reached",
			func(in ClosingCachePlan, _ Args) ClosingCachePlan {
				in.CacheReachable = false
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

		Transform[ClosingCachePlan, ClosingCachePlan]("the node's own copy of {string} is reclaimed",
			func(in ClosingCachePlan, a Args) (ClosingCachePlan, error) { return in, in.reclaimCache(a.String(0)) }),

		// ------------------------------------------------------------------
		// The consumer's action. Recorder disposal owns all real processes,
		// including failures before the final lookup.
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

// closingLookup runs the consumer's action N times and reads the artifact the
// last lookup handed back.
func closingLookup(p ClosingCachePlan, key, durableKey string, times int, sweep bool) (ClosingCacheLookup, error) {
	if err := p.ensureCache(); err != nil {
		return ClosingCacheLookup{}, err
	}
	backend := jetbridge.NewDaemonSetBackend(p.closingConfig(), jetbridge.NewArtifactLocator(), nil)
	backend.SetDaemonClient(p.CacheRuntime.client)

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
		if err := p.reclaimCache(key); err != nil {
			return out, err
		}
		if err := p.revealCachePeer(); err != nil {
			return out, err
		}
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
