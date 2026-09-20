package steps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// WorkerDefinitions migrates worker_test.go — the object the ATC holds when it
// places a step: names, container rows, artifact volumes, and the references it
// hands downstream.
//
// Two of that suite's 37 cases are NOT here, and both dispositions are recorded
// against the case they belong to:
//
//   - "writes nothing to the ArtifactLocator for the cache key" asserts a
//     collaborator's internal state. Its observable consequence is exactly the
//     "cache hit survives being wrapped" scenario, which is migrated. See
//     locatorDisposition below.
//   - "resolves the source node from the ArtifactLocator when the locator has
//     an entry" asserts nothing of the sort — its only Expect is
//     `dsVol.Source() == "k8s-worker-1"`, the worker name, and its own comment
//     admits the source node "is observable via StreamOut behavior (tested at
//     the integration level)". See sourceNodeDisposition below.

// ---------------------------------------------------------------------------
// Domain states
// ---------------------------------------------------------------------------

// WorkerReady is a jetbridge worker using a real Kubernetes API and real
// PostgreSQL database. Most fixtures use a local API without a kubelet and
// report pod state explicitly; live tasks/interception use real kubelets.
// Each Given refines this state
// and rebuilds the worker from it, because its collaborators are all
// constructor- or setter-injected and several of the setters replace each other
// (SetArtifactLocator swaps the whole storage backend, dropping the daemon
// client — a real ordering hazard the ginkgo suite worked around by hand).
type WorkerReady struct {
	DB        JetbridgeDB
	Namespace string
	Clientset kubernetes.Interface
	Config    jetbridge.Config
	DBWorker  db.Worker
	Worker    *jetbridge.Worker
	TeamID    int
	Ctx       context.Context

	// Knobs the Given steps turn. rebuild() reads all of them.
	VolumeRepo       db.VolumeRepository
	Executor         jetbridge.PodExecutor
	DaemonClient     *jetbridge.DaemonClient
	Locator          *jetbridge.ArtifactLocator
	ProducerExecutor jetbridge.PodExecutor
	Daemon           *realDaemon
	StoreNewOutputs  bool

	// The build-step container the intercept scenarios attach to.
	StepHandle   string
	StepMetadata db.ContainerMetadata
	StepPodName  string
}

// ContainerOutcome is what a caller of FindOrCreateContainer or LookupContainer
// got back. An error is a value here so a scenario can assert on failure.
type ContainerOutcome struct {
	Ready     WorkerReady
	Container runtime.Container
	Handle    string
	Found     bool
	Err       error
	Message   string
}

// ContainerRun is what the cluster looked like before and after the container
// was run. Pod creation is deferred to Run, so both halves matter.
type ContainerRun struct {
	Outcome    ContainerOutcome
	PodsBefore int
	PodsAfter  []string
	Err        error
	Message    string
}

// InterceptOutcome is what an operator running `fly intercept` saw.
type InterceptOutcome struct {
	Ready      WorkerReady
	Log        string
	ExitStatus int
	Err        error
	Message    string
	Pods       []string
}

// VolumeOutcome is what a caller of CreateVolumeForArtifact, LookupVolume or
// FindDaemonResourceCache got back. All three return the same shape, so they
// share a state and a vocabulary of checks.
type VolumeOutcome struct {
	Ready    WorkerReady
	Volume   runtime.Volume
	Artifact db.WorkerArtifact
	Found    bool
	Err      error
	Message  string
}

// ArtifactOutcome is a volume after a downstream step turned it into an
// artifact reference. Reading it is the only thing worth asserting.
type ArtifactOutcome struct {
	Ready    WorkerReady
	Artifact runtime.Artifact
	Handle   string
}

// ---------------------------------------------------------------------------
// Database-backed cache and failure fixtures
// ---------------------------------------------------------------------------

// legacyResourceCache persists the pre-durable-key cache shape used by these
// rc-ID scenarios. Use the production factory for the config, cache and build
// use, then reload the deliberately legacy row through that same factory.
func (w WorkerReady) legacyResourceCache(id int) (db.ResourceCache, error) {
	if id <= 0 {
		return nil, fmt.Errorf("resource cache ID must be positive, got %d", id)
	}
	factory := db.NewResourceCacheFactory(w.DB.Conn, w.DB.LockFactory)
	cache, found, err := factory.FindResourceCacheByID(id)
	if err != nil || found {
		return cache, err
	}
	team, found, err := w.DB.TeamFactory.FindTeam("main")
	if err != nil {
		return nil, fmt.Errorf("find cache user's team: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("cache user's team is missing")
	}
	build, err := team.CreateOneOffBuild()
	if err != nil {
		return nil, fmt.Errorf("create cache user's build: %w", err)
	}
	// IDs are part of the scenario's input. This sequence belongs only to its
	// private database; no cache exists at the requested ID (checked above).
	if _, err := w.DB.Conn.Exec(
		"SELECT setval(pg_get_serial_sequence('resource_caches', 'id'), $1, false)", id); err != nil {
		return nil, fmt.Errorf("set fixture cache ID: %w", err)
	}
	cache, err = factory.FindOrCreateResourceCache(db.ForBuild(build.ID()), "registry-image",
		atc.Version{"digest": "sha256:" + strings.Repeat("a", 64)},
		atc.Source{"repository": "example.invalid/brine-cache", "tag": strconv.Itoa(id)}, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create resource cache: %w", err)
	}
	if cache.ID() != id {
		return nil, fmt.Errorf("created resource cache %d, want %d", cache.ID(), id)
	}
	result, err := w.DB.Conn.Exec("UPDATE resource_caches SET durable_key = NULL WHERE id = $1", id)
	if err != nil {
		return nil, fmt.Errorf("make cache a legacy row: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, fmt.Errorf("legacy cache update affected %d rows, want 1: %v", changed, err)
	}
	cache, found, err = factory.FindResourceCacheByID(id)
	if err != nil {
		return nil, fmt.Errorf("reload legacy cache: %w", err)
	}
	if !found || cache.DurableKey() != "" {
		return nil, fmt.Errorf("legacy cache %d was not reloaded without a durable key", id)
	}
	return cache, nil
}

// workerDatabaseRefusal installs a real CHECK constraint in this scenario's
// private database. The ordinary db.Worker/VolumeRepository still issue every
// SQL statement; PostgreSQL rejects only the named mutation. No method is
// replaced and no error object or message is fabricated. The scenario's DB
// resource drops the whole database, including this constraint, at disposal.
func workerDatabaseRefusal(pattern, ddl string) brine.StepDefinition {
	return Transform[WorkerReady, WorkerReady](pattern,
		func(in WorkerReady, _ Args) (WorkerReady, error) {
			if _, err := in.DB.Conn.Exec(ddl); err != nil {
				return WorkerReady{}, fmt.Errorf("install database refusal: %w", err)
			}
			return in, nil
		})
}

// ---------------------------------------------------------------------------
// Step definitions
// ---------------------------------------------------------------------------

func WorkerDefinitions() []brine.StepDefinition {
	return concatDefs(
		workerSetupDefinitions(),
		workerContainerDefinitions(),
		workerInterceptDefinitions(),
		workerVolumeDefinitions(),
		workerArtifactDefinitions(),
	)
}

func concatDefs(groups ...[]brine.StepDefinition) []brine.StepDefinition {
	var all []brine.StepDefinition
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
}

func workerSetupDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, WorkerReady](
			"a Kubernetes worker {string} with a database behind it",
			[]string{"jetbridge-db", "real-cluster"},
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder, res brine.Resources) (WorkerReady, error) {
				name, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a worker name parameter")
				}
				// "main" by name, because the cache fixtures load that team
				// back by name to own the build a resource cache is created
				// for.
				return newWorkerReady(res, rec, name, "main", nil)
			},
		),

		CheckString[WorkerReady]("the worker answers to the name {string}",
			"the name the worker answers to",
			func(in WorkerReady) (string, error) {
				return in.Worker.Name(), nil
			}),

		// RC-01. False means "do not skip the cache", i.e. a get step is
		// allowed to serve a hit from the daemon instead of downloading again.
		CheckThat[WorkerReady]("the worker takes part in resource caching",
			func(in WorkerReady) error {
				if in.Worker.SkipResourceCache() {
					return fmt.Errorf("expected the worker to take part in resource caching, " +
						"but it reports that resource caches should be skipped")
				}
				return nil
			}),

		workerDatabaseRefusal("the database cannot transition containers to created",
			"ALTER TABLE containers ADD CONSTRAINT brine_container_creation CHECK (state <> 'created')"),

		brine.DefineMap[LiveTaskPlan, WorkerReady]("the worker can exec into pods",
			func(in LiveTaskPlan, _ brine.Params, rec *brine.Recorder) (WorkerReady, error) {
				return newLiveRuntimeWorker(in.Database, rec)
			}),

		Refine[WorkerReady]("the worker has no volume repository configured",
			func(in WorkerReady, _ Args) WorkerReady {
				in.VolumeRepo = nil
				return in.rebuild()
			}),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the worker's volume repository has lost its database connection",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				closed, err := in.DB.ClosedConn()
				if err != nil {
					return WorkerReady{}, err
				}
				in.VolumeRepo = db.NewVolumeRepository(closed)
				return in.rebuild(), nil
			},
		),

		workerDatabaseRefusal("the volume repository cannot transition volumes to created",
			"ALTER TABLE volumes ADD CONSTRAINT brine_volume_creation CHECK (state <> 'created')"),

		workerDatabaseRefusal("the volume repository cannot initialise artifacts",
			"ALTER TABLE volumes ADD CONSTRAINT brine_artifact_initialization CHECK (worker_artifact_id IS NULL)"),

		Transform[WorkerReady, WorkerReady]("the producing pod has been reaped",
			func(in WorkerReady, _ Args) (WorkerReady, error) {
				// The API owns this pod's identity and deletion. No kubelet is
				// involved: the downstream fallback must meet a real API 404.
				if _, err := in.Clientset.CoreV1().Pods(in.Namespace).Create(in.Ctx, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "producer-pod"},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "busybox:1.37.0"}}},
				}, metav1.CreateOptions{}); err != nil {
					return WorkerReady{}, err
				}
				return in, in.reapPod("producer-pod")
			}),

		// Run the production daemon with real files and registration. Discovery
		// uses the real API; envtest does not execute producer containers.
		brine.DefineMap[WorkerReady, WorkerReady](
			"the cluster runs an artifact daemon holding every step output",
			func(in WorkerReady, _ brine.Params, rec *brine.Recorder) (WorkerReady, error) {
				in.StoreNewOutputs = true
				return in.withDaemon(rec, nil)
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the cluster runs an artifact daemon holding the step output {string}",
			func(in WorkerReady, p brine.Params, rec *brine.Recorder) (WorkerReady, error) {
				key, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a step output key parameter")
				}
				return in.withDaemon(rec, map[string]string{key: stepOutputBody})
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the cluster runs an artifact daemon holding the resource cache {int}",
			func(in WorkerReady, p brine.Params, rec *brine.Recorder) (WorkerReady, error) {
				id, ok := p.GetInt(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a cache id parameter")
				}
				return in.withDaemon(rec, map[string]string{fmt.Sprintf("rc-%d", id): cachedBody})
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the cluster runs an artifact daemon holding nothing",
			func(in WorkerReady, _ brine.Params, rec *brine.Recorder) (WorkerReady, error) {
				return in.withDaemon(rec, map[string]string{})
			},
		),

		Refine[WorkerReady]("the worker has no artifact daemon configured",
			func(in WorkerReady, _ Args) WorkerReady {
				in.Config.ArtifactDaemonHostPath = ""
				in.Config.ArtifactDaemonService = ""
				in.Config.ArtifactDaemonPort = 0
				in.DaemonClient = nil
				in.Locator = nil
				return in.rebuild()
			}),

		// A node roll leaves the locator naming a node that no longer exists.
		// SetArtifactLocator replaces the storage backend, which drops the
		// daemon client, so the rebuild re-applies it in that order.
		Refine[WorkerReady]("the worker still remembers the resource cache {int} on a node that has been rolled away",
			func(in WorkerReady, a Args) WorkerReady {
				key := fmt.Sprintf("rc-%d", a.Int(0))
				locator := jetbridge.NewArtifactLocator()
				locator.Record(key, "10.0.0.99", key)
				in.Locator = locator
				return in.rebuild()
			}),
	}
}

func workerContainerDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The same real worker, request and result vocabulary handles fresh
		// creation, interrupted creation and database failures.
		Transform[WorkerReady, WorkerReady]("the worker has lost its database connection",
			func(in WorkerReady, _ Args) (WorkerReady, error) {
				conn := in.DB.runner.OpenConn()
				logger := lagertest.NewTestLogger("brine-closed-worker")
				factory := db.NewWorkerFactory(conn, db.NewStaticWorkerCache(logger, conn, 0))
				worker, found, err := factory.GetWorker(in.DBWorker.Name())
				closeErr := conn.Close()
				if err != nil {
					return WorkerReady{}, fmt.Errorf("load worker before disconnecting: %w", err)
				}
				if closeErr != nil {
					return WorkerReady{}, fmt.Errorf("close worker database connection: %w", closeErr)
				}
				if !found {
					return WorkerReady{}, fmt.Errorf("worker was not found before disconnecting")
				}
				in.DBWorker = worker
				return in.rebuild(), nil
			}),

		Transform[WorkerReady, WorkerReady]("another worker already holds container {string}",
			func(in WorkerReady, a Args) (WorkerReady, error) {
				other, err := in.DB.PersistNamedWorker(in.DBWorker.Name() + "-other")
				if err != nil {
					return WorkerReady{}, err
				}
				if _, err := other.CreateContainer(db.NewFixedHandleContainerOwner(a.String(0)),
					db.ContainerMetadata{Type: db.ContainerTypeTask}); err != nil {
					return WorkerReady{}, fmt.Errorf("create the other worker's container: %w", err)
				}
				return in, nil
			}),

		Transform[WorkerReady, WorkerReady]("a task container {string} was left half-created by a crash",
			func(in WorkerReady, a Args) (WorkerReady, error) {
				if _, err := in.DBWorker.CreateContainer(db.NewFixedHandleContainerOwner(a.String(0)),
					db.ContainerMetadata{Type: db.ContainerTypeTask}); err != nil {
					return WorkerReady{}, fmt.Errorf("leave a creating container behind: %w", err)
				}
				return in, nil
			}),

		brine.DefineMap[WorkerReady, WorkerReady](
			"a task container {string} has already been created for step {string}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				handle, _ := p.GetString(0)
				step, ok := p.GetString(1)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a handle and a step name")
				}
				creating, err := in.DBWorker.CreateContainer(
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: step},
				)
				if err != nil {
					return WorkerReady{}, fmt.Errorf("create container %q: %w", handle, err)
				}
				if _, err := creating.Created(); err != nil {
					return WorkerReady{}, fmt.Errorf("mark container %q created: %w", handle, err)
				}
				return in, nil
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the cluster is running a pod {string} that no container row refers to",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				name, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a pod name parameter")
				}
				return in, createInterceptPod(in, name, nil)
			},
		),

		brine.DefineMap[WorkerReady, ContainerOutcome](
			"a task container {string} is requested for step {string}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (ContainerOutcome, error) {
				handle, _ := p.GetString(0)
				step, ok := p.GetString(1)
				if !ok {
					return ContainerOutcome{}, fmt.Errorf("expected a handle and a step name")
				}
				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask, StepName: step},
					runtime.ContainerSpec{
						TeamID:    1,
						TeamName:  "main",
						Dir:       "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox"},
					},
					nil,
				)
				return newContainerOutcome(in, handle, container, err), nil
			},
		),

		brine.DefineMap[WorkerReady, ContainerOutcome](
			"the container {string} is looked up",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (ContainerOutcome, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ContainerOutcome{}, fmt.Errorf("expected a handle parameter")
				}
				container, found, err := in.Worker.LookupContainer(in.Ctx, handle)
				out := newContainerOutcome(in, handle, container, err)
				if err == nil {
					out.Found = found
				}
				return out, nil
			},
		),

		// Pod creation is deferred to Run, so the cluster is photographed on
		// both sides of the call.
		brine.DefineMap[ContainerOutcome, ContainerRun](
			"the container is run",
			func(in ContainerOutcome, _ brine.Params, _ *brine.Recorder) (ContainerRun, error) {
				if in.Err != nil {
					return ContainerRun{}, fmt.Errorf("the container was never created: %w", in.Err)
				}
				before, err := in.Ready.podNames()
				if err != nil {
					return ContainerRun{}, err
				}
				_, runErr := in.Container.Run(in.Ready.Ctx, runtime.ProcessSpec{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello"},
				}, runtime.ProcessIO{})
				after, err := in.Ready.podNames()
				if err != nil {
					return ContainerRun{}, err
				}
				out := ContainerRun{Outcome: in, PodsBefore: len(before), PodsAfter: after, Err: runErr}
				if runErr != nil {
					out.Message = runErr.Error()
				}
				return out, nil
			},
		),

		CheckThat[ContainerOutcome]("the container request succeeds",
			func(in ContainerOutcome) error {
				return in.ok()
			}),

		CheckContains[ContainerOutcome]("the container request fails saying {string}",
			"the container request failure",
			func(in ContainerOutcome) (string, error) {
				return failureMessage("the container request", in.Err, in.Message)
			}),

		CheckThat[ContainerOutcome]("the container is found",
			func(in ContainerOutcome) error {
				if in.Err != nil {
					return fmt.Errorf("looking the container up failed: %v", in.Err)
				}
				if !in.Found {
					return fmt.Errorf("expected the container %q to be found, it was not", in.Handle)
				}
				return nil
			}),

		CheckThat[ContainerOutcome]("the container is not found",
			func(in ContainerOutcome) error {
				if in.Err != nil {
					return fmt.Errorf("expected a clean miss, got an error: %v", in.Err)
				}
				if in.Found {
					return fmt.Errorf("expected the container %q not to be found, but it was", in.Handle)
				}
				return nil
			}),

		// `fly intercept` hands the container's database row to the hijack
		// handler, which records the hijack against it. A nil row would panic
		// there rather than here.
		CheckString[ContainerOutcome]("it carries the database row for handle {string}",
			"the handle on the container's database row",
			func(in ContainerOutcome) (string, error) {
				if err := in.ok(); err != nil {
					return "", err
				}
				row := in.Container.DBContainer()
				if row == nil {
					return "", fmt.Errorf("expected the container to carry a database row, it carries none")
				}
				return row.Handle(), nil
			}),

		// Keeps its own body: the parameter is the row to look up, and what it is
		// compared against is an id read from the database, not the parameter.
		brine.DefineCheck[ContainerOutcome](
			"it returns the container already recorded as {string}",
			func(in ContainerOutcome, p brine.Params, _ *brine.Recorder) error {
				handle, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a handle parameter")
				}
				if err := in.ok(); err != nil {
					return err
				}
				var id int
				if err := in.Ready.DB.Conn.QueryRow(
					`SELECT id FROM containers WHERE handle = $1`, handle,
				).Scan(&id); err != nil {
					return fmt.Errorf("read the recorded container %q: %w", handle, err)
				}
				if got := in.Container.DBContainer().ID(); got != id {
					return fmt.Errorf("expected the container already recorded (id %d), got a different one (id %d)", id, got)
				}
				return nil
			},
		),

		// Keeps its own body: the count comes first and the handle second, so the
		// parameter the sentence expects is not the one a For combinator routes.
		// CheckCount does read the count from that position, but its getter sees
		// no parameters at all and so cannot reach the handle the query needs.
		brine.DefineCheck[ContainerOutcome](
			"exactly {int} container row carries the handle {string}",
			func(in ContainerOutcome, p brine.Params, _ *brine.Recorder) error {
				want, _ := p.GetInt(0)
				handle, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a count and a handle")
				}
				var count int
				if err := in.Ready.DB.Conn.QueryRow(
					`SELECT count(*) FROM containers WHERE handle = $1`, handle,
				).Scan(&count); err != nil {
					return fmt.Errorf("count containers for %q: %w", handle, err)
				}
				if count != want {
					return fmt.Errorf("expected %d container row(s) with handle %q, found %d", want, handle, count)
				}
				return nil
			},
		),

		// A row left in `creating` is invisible to the collector; `failed` is
		// what lets the cluster reclaim it.
		CheckStringFor[ContainerOutcome]("the container {string} is left in state {string}",
			"the state the container is left in",
			func(in ContainerOutcome, handle string) (string, error) {
				var state string
				if err := in.Ready.DB.Conn.QueryRow(
					`SELECT state FROM containers WHERE handle = $1`, handle,
				).Scan(&state); err != nil {
					return "", fmt.Errorf("read the state of container %q: %w", handle, err)
				}
				return state, nil
			}),

		// Keeps its own body: four independent assertions — the recorded state,
		// the container type, the step the row is recorded for, and the handle on
		// the container the caller was handed. A combinator compares one derived
		// value; folding the other three into getter errors would demote real
		// assertions to presumptions.
		brine.DefineCheck[ContainerRun](
			"the container {string} is recorded as a created task container for step {string}",
			func(in ContainerRun, p brine.Params, _ *brine.Recorder) error {
				handle, _ := p.GetString(0)
				step, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a handle and a step name")
				}
				if err := in.Outcome.ok(); err != nil {
					return err
				}
				var state, containerType, stepName string
				if err := in.Outcome.Ready.DB.Conn.QueryRow(`
					SELECT state, meta_type, meta_step_name FROM containers WHERE handle = $1
				`, handle).Scan(&state, &containerType, &stepName); err != nil {
					return fmt.Errorf("read the recorded container %q: %w", handle, err)
				}
				if state != string(atc.ContainerStateCreated) {
					return fmt.Errorf("expected container %q to be recorded as %q, it is %q",
						handle, atc.ContainerStateCreated, state)
				}
				if containerType != string(db.ContainerTypeTask) {
					return fmt.Errorf("expected container %q to be a task container, it is a %q", handle, containerType)
				}
				if stepName != step {
					return fmt.Errorf("expected container %q to be recorded for step %q, it is recorded for %q",
						handle, step, stepName)
				}
				if got := in.Outcome.Container.DBContainer().Handle(); got != handle {
					return fmt.Errorf("expected the returned container to carry handle %q, it carries %q", handle, got)
				}
				return nil
			},
		),

		CheckThat[ContainerRun]("no pod existed until the container ran",
			func(in ContainerRun) error {
				if in.PodsBefore != 0 {
					return fmt.Errorf("expected no pod before the container ran, found %d", in.PodsBefore)
				}
				return nil
			}),

		// Keeps its own body: the sentence is a cardinality as much as an
		// identity — exactly one pod, and it is that one. CheckMember asserts
		// only presence, so a second, unexpected pod alongside the wanted one
		// would pass it; CheckCount takes the count as its parameter, and here
		// the parameter is the name.
		brine.DefineCheck[ContainerRun](
			"the pod {string} is now on the cluster",
			func(in ContainerRun, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pod name parameter")
				}
				if in.Err != nil {
					return fmt.Errorf("running the container failed: %v", in.Err)
				}
				if len(in.PodsAfter) != 1 || in.PodsAfter[0] != want {
					return fmt.Errorf("expected exactly the pod %q on the cluster, found %v", want, in.PodsAfter)
				}
				return nil
			},
		),
	}
}

func workerInterceptDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[WorkerReady, WorkerReady](
			"a task step of build {int} of {string} was recorded under the opaque handle {string}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				build, _ := p.GetInt(0)
				job, _ := p.GetString(1)
				handle, ok := p.GetString(2)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a build number, a pipeline/job and a handle")
				}
				pipeline, jobName, found := strings.Cut(job, "/")
				if !found {
					return WorkerReady{}, fmt.Errorf("expected %q to be pipeline/job", job)
				}
				metadata := db.ContainerMetadata{
					Type:         db.ContainerTypeTask,
					PipelineName: pipeline,
					JobName:      jobName,
					BuildName:    strconv.Itoa(build),
					StepName:     jobName,
					BuildID:      653430,
				}
				creating, err := in.DBWorker.CreateContainer(db.NewFixedHandleContainerOwner(handle), metadata)
				if err != nil {
					return WorkerReady{}, fmt.Errorf("create container %q: %w", handle, err)
				}
				if _, err := creating.Created(); err != nil {
					return WorkerReady{}, fmt.Errorf("mark container %q created: %w", handle, err)
				}
				in.StepHandle = handle
				in.StepMetadata = metadata
				return in, nil
			},
		),

		// The sanity check the ginkgo BeforeEach made with a By(): the pod the
		// step creates is named from its metadata, and that name is not the
		// handle. If those two ever became the same string the intercept
		// scenarios would stop discriminating, so this fails loudly.
		brine.DefineMap[WorkerReady, WorkerReady](
			"the step created the pod {string}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				name, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a pod name parameter")
				}
				generated := jetbridge.GeneratePodName(in.StepMetadata, in.StepHandle)
				if generated != name {
					return WorkerReady{}, fmt.Errorf(
						"the step's metadata generates the pod name %q, not %q", generated, name)
				}
				if name == in.StepHandle {
					return WorkerReady{}, fmt.Errorf(
						"the generated pod name is the handle %q, so this scenario cannot tell them apart", name)
				}
				labels := map[string]string{
					"concourse.ci/worker": in.Worker.Name(),
					"concourse.ci/handle": in.StepHandle,
				}
				if err := createInterceptPod(in, name, labels); err != nil {
					return WorkerReady{}, err
				}
				in.StepPodName = name
				return in, nil
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"that pod has since been reaped",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				return in, in.reapPod(in.StepPodName)
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"that pod has since finished with exit status {string}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				status, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected an exit status parameter")
				}
				if err := finishInterceptPod(in, status); err != nil {
					return WorkerReady{}, err
				}
				return in, nil
			},
		),

		// The decoy is what makes the "pod is gone" scenario a routing test:
		// a worker that resolved the handle straight to a pod name would find
		// this and report success.
		brine.DefineMap[WorkerReady, WorkerReady](
			"a decoy pod named after the handle is running",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				return in, createInterceptPod(in, in.StepHandle, nil)
			},
		),

		brine.DefineMap[WorkerReady, InterceptOutcome](
			"the operator intercepts the container {string} and runs {string}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (InterceptOutcome, error) {
				handle, _ := p.GetString(0)
				command, ok := p.GetString(1)
				if !ok {
					return InterceptOutcome{}, fmt.Errorf("expected a handle and a command")
				}

				out := InterceptOutcome{Ready: in}
				container, found, err := in.Worker.LookupContainer(in.Ctx, handle)
				if err != nil {
					return InterceptOutcome{}, fmt.Errorf("look up container %q: %w", handle, err)
				}
				if !found {
					return InterceptOutcome{}, fmt.Errorf("container %q is not there to intercept", handle)
				}

				log := new(strings.Builder)
				process, runErr := container.Run(in.Ctx, runtime.ProcessSpec{
					ID:   handle,
					Path: "/bin/sh",
					Args: []string{"-c", command},
				}, runtime.ProcessIO{Stdout: log, Stderr: log})
				if runErr == nil {
					result, waitErr := process.Wait(in.Ctx)
					out.ExitStatus, out.Err = result.ExitStatus, waitErr
				} else {
					out.Err = runErr
				}
				// Successful exec (including a non-zero exit) must use this
				// scenario's state; execution errors keep their original message.
				if out.Err == nil {
					if err := requirePodSupervisorState(in, in.StepPodName); err != nil {
						return InterceptOutcome{}, err
					}
				}
				if out.Err != nil {
					out.Message = out.Err.Error()
				}
				out.Log = log.String()

				pods, err := in.podNames()
				if err != nil {
					return InterceptOutcome{}, err
				}
				out.Pods = pods
				return out, nil
			},
		),

		CheckThat[InterceptOutcome]("the interception succeeds",
			func(in InterceptOutcome) error {
				if in.Err != nil {
					return fmt.Errorf("expected the interception to succeed, it failed: %v", in.Err)
				}
				if in.ExitStatus != 0 {
					return fmt.Errorf("expected the intercepted command to exit 0, it exited %d (log: %q)",
						in.ExitStatus, in.Log)
				}
				return nil
			}),

		CheckContains[InterceptOutcome]("the operator sees {string}",
			"what the operator saw",
			func(in InterceptOutcome) (string, error) {
				return in.Log, nil
			}),

		CheckContains[InterceptOutcome]("the interception fails saying {string}",
			"the interception failure",
			func(in InterceptOutcome) (string, error) {
				return failureMessage("the interception", in.Err, in.Message)
			}),

		// Keeps its own body: "only" is the assertion, and membership does not
		// express it. CheckMember would pass with a leftover pod beside the
		// wanted one — which is precisely the state this sentence exists to
		// catch — and CheckCount's parameter is a count, not a name.
		brine.DefineCheck[InterceptOutcome](
			"the cluster still holds only the pod {string}",
			func(in InterceptOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a pod name parameter")
				}
				if len(in.Pods) != 1 || in.Pods[0] != want {
					return fmt.Errorf("expected the cluster to hold only the pod %q, it holds %v", want, in.Pods)
				}
				return nil
			},
		),

		// A restarted web reads this annotation to resume the step. Replacing
		// the pod would erase it, which is why interception refuses.
		CheckStringFor[InterceptOutcome]("the pod {string} still records exit status {string}",
			"the exit status the pod still records",
			func(in InterceptOutcome, name string) (string, error) {
				pod, err := in.Ready.Clientset.CoreV1().Pods(in.Ready.Namespace).Get(
					in.Ready.Ctx, name, metav1.GetOptions{})
				if err != nil {
					return "", fmt.Errorf("expected the pod %q to survive: %w", name, err)
				}
				return pod.Annotations[exitStatusAnnotation], nil
			}),
	}
}

func workerVolumeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[WorkerReady, WorkerReady](
			"a volume {string} exists on this worker",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a handle parameter")
				}
				creating, err := in.DB.VolumeRepository.CreateVolumeWithHandle(
					handle, in.TeamID, in.DBWorker.Name(), db.VolumeTypeArtifact)
				if err != nil {
					return WorkerReady{}, fmt.Errorf("create volume %q: %w", handle, err)
				}
				if _, err := creating.Created(); err != nil {
					return WorkerReady{}, fmt.Errorf("mark volume %q created: %w", handle, err)
				}
				return in, nil
			},
		),

		brine.DefineMap[WorkerReady, VolumeOutcome](
			"the worker creates a volume for an artifact",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (VolumeOutcome, error) {
				vol, artifact, err := in.Worker.CreateVolumeForArtifact(in.Ctx, in.TeamID)
				if err == nil && in.StoreNewOutputs {
					if artifact == nil {
						return VolumeOutcome{}, fmt.Errorf("created volume has no database artifact")
					}
					// Seed from persisted identity, not the returned volume's key:
					// a wrong-key production mutation must still miss this file.
					var handle string
					if err := in.DB.Conn.QueryRow(
						"SELECT handle FROM volumes WHERE worker_artifact_id = $1",
						artifact.ID()).Scan(&handle); err != nil {
						return VolumeOutcome{}, fmt.Errorf("read artifact's persisted handle: %w", err)
					}
					if err := in.storeDaemonArtifact(handle, stepOutputBody); err != nil {
						return VolumeOutcome{}, err
					}
				}
				out := VolumeOutcome{Ready: in, Volume: vol, Artifact: artifact, Found: err == nil, Err: err}
				if err != nil {
					out.Message = err.Error()
				}
				return out, nil
			},
		),

		brine.DefineMap[WorkerReady, VolumeOutcome](
			"the volume {string} is looked up",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (VolumeOutcome, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return VolumeOutcome{}, fmt.Errorf("expected a handle parameter")
				}
				vol, found, err := in.Worker.LookupVolume(in.Ctx, handle)
				out := VolumeOutcome{Ready: in, Volume: vol, Found: found, Err: err}
				if err != nil {
					out.Message = err.Error()
				}
				return out, nil
			},
		),

		brine.DefineMap[WorkerReady, VolumeOutcome](
			"a get step looks for the resource cache {int}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (VolumeOutcome, error) {
				id, ok := p.GetInt(0)
				if !ok {
					return VolumeOutcome{}, fmt.Errorf("expected a cache id parameter")
				}
				cache, err := in.legacyResourceCache(id)
				if err != nil {
					return VolumeOutcome{}, err
				}
				vol, found, err := in.Worker.FindDaemonResourceCache(in.Ctx, cache)
				out := VolumeOutcome{Ready: in, Volume: vol, Found: found, Err: err}
				if err != nil {
					out.Message = err.Error()
				}
				return out, nil
			},
		),

		CheckThat[VolumeOutcome]("creating the volume succeeds",
			func(in VolumeOutcome) error {
				return in.ok()
			}),

		CheckContains[VolumeOutcome]("creating the volume fails saying {string}",
			"the volume creation failure",
			func(in VolumeOutcome) (string, error) {
				return failureMessage("creating the volume", in.Err, in.Message)
			}),

		CheckContains[VolumeOutcome]("looking up the volume fails saying {string}",
			"the volume lookup failure",
			func(in VolumeOutcome) (string, error) {
				return failureMessage("looking up the volume", in.Err, in.Message)
			}),

		// Keeps its own body: four independent assertions — the owning team, the
		// owning worker, the recorded state and the persisted type — of which the
		// sentence names only two as parameters. The team and worker are compared
		// against the live state rather than against anything the sentence says,
		// and turning them into getter errors would make them presumptions.
		brine.DefineCheck[VolumeOutcome](
			"the volume is recorded for this worker and team in state {string} as type {string}",
			func(in VolumeOutcome, p brine.Params, _ *brine.Recorder) error {
				wantState, _ := p.GetString(0)
				wantType, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a state and a type")
				}
				if err := in.ok(); err != nil {
					return err
				}
				var teamID int
				var workerName, state string
				if err := in.Ready.DB.Conn.QueryRow(`
					SELECT team_id, worker_name, state FROM volumes WHERE handle = $1
				`, in.Volume.Handle()).Scan(&teamID, &workerName, &state); err != nil {
					return fmt.Errorf("read the persisted volume %q: %w", in.Volume.Handle(), err)
				}
				if teamID != in.Ready.TeamID {
					return fmt.Errorf("expected the volume to belong to team %d, it belongs to %d",
						in.Ready.TeamID, teamID)
				}
				if workerName != in.Ready.DBWorker.Name() {
					return fmt.Errorf("expected the volume to belong to worker %q, it belongs to %q",
						in.Ready.DBWorker.Name(), workerName)
				}
				if state != wantState {
					return fmt.Errorf("expected the volume to be in state %q, it is %q", wantState, state)
				}
				persisted, found, err := in.Ready.DB.VolumeRepository.FindVolume(in.Volume.Handle())
				if err != nil {
					return fmt.Errorf("find the persisted volume: %w", err)
				}
				if !found {
					return fmt.Errorf("expected the volume %q to be findable, it is not", in.Volume.Handle())
				}
				if string(persisted.Type()) != wantType {
					return fmt.Errorf("expected a %q volume, got %q", wantType, persisted.Type())
				}
				return nil
			},
		),

		CheckThat[VolumeOutcome]("the volume row points at the artifact the caller was handed",
			func(in VolumeOutcome) error {
				if err := in.ok(); err != nil {
					return err
				}
				if in.Artifact == nil {
					return fmt.Errorf("expected the caller to be handed an artifact, it got none")
				}
				var artifactID int
				if err := in.Ready.DB.Conn.QueryRow(
					`SELECT worker_artifact_id FROM volumes WHERE handle = $1`, in.Volume.Handle(),
				).Scan(&artifactID); err != nil {
					return fmt.Errorf("read the volume's artifact association: %w", err)
				}
				if artifactID != in.Artifact.ID() {
					return fmt.Errorf("expected the volume row to point at artifact %d, it points at %d",
						in.Artifact.ID(), artifactID)
				}
				return nil
			}),

		CheckThat[VolumeOutcome]("the handle the caller got is the handle the database persisted",
			func(in VolumeOutcome) error {
				if err := in.ok(); err != nil {
					return err
				}
				if in.Volume.Handle() == "" {
					return fmt.Errorf("expected a handle, got an empty one")
				}
				persisted, found, err := in.Ready.DB.VolumeRepository.FindVolume(in.Volume.Handle())
				if err != nil {
					return fmt.Errorf("find the persisted volume: %w", err)
				}
				if !found {
					return fmt.Errorf("the caller got handle %q, which the database does not hold",
						in.Volume.Handle())
				}
				if persisted.Handle() != in.Volume.Handle() {
					return fmt.Errorf("expected handle %q, the database holds %q",
						in.Volume.Handle(), persisted.Handle())
				}
				return nil
			}),

		CheckString[VolumeOutcome]("a volume for this worker is left in state {string}",
			"the state the volume is left in",
			func(in VolumeOutcome) (string, error) {
				var state string
				if err := in.Ready.DB.Conn.QueryRow(
					`SELECT state FROM volumes WHERE team_id = $1 AND worker_name = $2`,
					in.Ready.TeamID, in.Ready.DBWorker.Name(),
				).Scan(&state); err != nil {
					return "", fmt.Errorf("read the half-written volume: %w", err)
				}
				return state, nil
			}),

		CheckThat[VolumeOutcome]("no artifact is recorded",
			func(in VolumeOutcome) error {
				var count int
				if err := in.Ready.DB.Conn.QueryRow(`SELECT count(*) FROM worker_artifacts`).Scan(&count); err != nil {
					return fmt.Errorf("count artifacts: %w", err)
				}
				if count != 0 {
					return fmt.Errorf("expected no artifact to be recorded, found %d", count)
				}
				return nil
			}),

		CheckThat[VolumeOutcome]("the volume is found",
			func(in VolumeOutcome) error {
				return in.found("volume")
			}),

		CheckThat[VolumeOutcome]("the volume is not found",
			func(in VolumeOutcome) error {
				return in.notFound("volume")
			}),

		CheckThat[VolumeOutcome]("the cache is found",
			func(in VolumeOutcome) error {
				return in.found("cache")
			}),

		CheckThat[VolumeOutcome]("the cache is not found",
			func(in VolumeOutcome) error {
				return in.notFound("cache")
			}),

		CheckString[VolumeOutcome]("the volume's handle is {string}",
			"the volume's handle",
			func(in VolumeOutcome) (string, error) {
				if err := in.ok(); err != nil {
					return "", err
				}
				return in.Volume.Handle(), nil
			}),

		// Source() is what a cross-worker stream reads to decide where the
		// bytes live. It is the worker name, never a node or a pod IP.
		CheckString[VolumeOutcome]("it reports {string} as its source",
			"the volume's source",
			func(in VolumeOutcome) (string, error) {
				if err := in.ok(); err != nil {
					return "", err
				}
				return in.Volume.Source(), nil
			}),

		// The round trip. Nothing is asserted about how the volume reads —
		// only that the bytes arrive.
		CheckString[VolumeOutcome]("reading the volume yields {string}",
			"the volume's contents",
			func(in VolumeOutcome) (string, error) {
				if err := in.ok(); err != nil {
					return "", err
				}
				body, err := readArtifact(in.Ready.Ctx, in.Volume)
				if err != nil {
					return "", fmt.Errorf("reading the volume failed: %w", err)
				}
				return body, nil
			}),
	}
}

func workerArtifactDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[WorkerReady, ArtifactOutcome](
			"a mounted step output volume {string} is turned into an artifact",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (ArtifactOutcome, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ArtifactOutcome{}, fmt.Errorf("expected a handle parameter")
				}
				vol := jetbridge.NewDeferredVolume(
					handle, in.Worker.Name(), in.ProducerExecutor,
					in.Namespace, "main", "/mnt/data")
				vol.SetPodName("producer-pod")
				return in.wrapArtifact(vol, handle), nil
			},
		),

		brine.DefineMap[WorkerReady, ArtifactOutcome](
			"a stub step output volume {string} is turned into an artifact",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (ArtifactOutcome, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ArtifactOutcome{}, fmt.Errorf("expected a handle parameter")
				}
				return in.wrapArtifact(jetbridge.NewStubVolume(handle, in.Worker.Name(), "/mnt/stub"), handle), nil
			},
		),

		// get_step.go calls ArtifactFromVolume unconditionally, so a step with
		// nothing to publish reaches this path in production.
		brine.DefineMap[WorkerReady, ArtifactOutcome](
			"a step with no output volume asks for an artifact",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (ArtifactOutcome, error) {
				return ArtifactOutcome{Ready: in, Artifact: in.Worker.ArtifactFromVolume(nil)}, nil
			},
		),

		// This is the migration of the "probe hit poisons the locator"
		// regression: the wrap happens after the probe, on the same worker,
		// exactly as atc/exec/get_step.go does it.
		brine.DefineMap[VolumeOutcome, ArtifactOutcome](
			"a downstream step turns it into an artifact",
			func(in VolumeOutcome, _ brine.Params, _ *brine.Recorder) (ArtifactOutcome, error) {
				if err := in.ok(); err != nil {
					return ArtifactOutcome{}, err
				}
				return in.Ready.wrapArtifact(in.Volume, in.Volume.Handle()), nil
			},
		),

		CheckString[ArtifactOutcome]("the artifact's handle is {string}",
			"the artifact's handle",
			func(in ArtifactOutcome) (string, error) {
				if in.Artifact == nil {
					return "", fmt.Errorf("expected an artifact for volume %q, got none", in.Handle)
				}
				return in.Artifact.Handle(), nil
			}),

		CheckString[ArtifactOutcome]("reading the artifact yields {string}",
			"the artifact's contents",
			func(in ArtifactOutcome) (string, error) {
				if in.Artifact == nil {
					return "", fmt.Errorf("expected an artifact to read, got none")
				}
				body, err := readArtifact(in.Ready.Ctx, in.Artifact)
				if err != nil {
					return "", fmt.Errorf("reading the artifact failed: %w", err)
				}
				return body, nil
			}),

		CheckContains[ArtifactOutcome]("reading the artifact fails saying {string}",
			"the artifact read failure",
			func(in ArtifactOutcome) (string, error) {
				if in.Artifact == nil {
					return "", fmt.Errorf("expected an artifact to read, got none")
				}
				body, err := readArtifact(in.Ready.Ctx, in.Artifact)
				if err == nil {
					return "", fmt.Errorf("expected the read to fail, it returned %q", body)
				}
				return err.Error(), nil
			}),

		CheckThat[ArtifactOutcome]("no artifact is handed back",
			func(in ArtifactOutcome) error {
				if in.Artifact != nil {
					return fmt.Errorf("expected no artifact, got one with handle %q", in.Artifact.Handle())
				}
				return nil
			}),
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const (
	// exitStatusAnnotation is the annotation a finished step's pod carries so a
	// restarted web can resume rather than re-run it.
	exitStatusAnnotation = "concourse.ci/exit-status"

	stepOutputBody = "step-output-bytes"
	cachedBody     = "cached-tar-data"
)

// newWorkerReady is the preamble every worker Given shares, written once: a
// namespace of this chain's own on the real API server, a worker row in the
// real database, a team for anything it persists to hang off, and the
// production exec transport. Nothing here is a double — the namespace, the
// worker and the team are all objects a real server assigned identities to.
//
// configure applies the scenario's own configuration before the worker is
// built, which is the only axis most Givens vary. A Given that needs a knob
// rebuild() reads — an executor, a locator, a daemon client — sets it on the
// returned state and rebuilds, because those setters replace each other and
// the order they run in is rebuild()'s business rather than a caller's.
//
// teamName names the team row. An empty one takes the namespace's own
// generated name, which is unique per call: the Hangar Givens build a second
// worker inside a scenario that already has one, and two teams asking for the
// same name is a fixture collision wearing the costume of a production
// refusal.
func newWorkerReady(res brine.Resources, rec *brine.Recorder, workerName, teamName string,
	configure func(*jetbridge.Config)) (WorkerReady, error) {
	database, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return WorkerReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
	}
	cluster, err := getRealCluster(res)
	if err != nil {
		return WorkerReady{}, err
	}

	ctx := context.Background()
	ns, err := cluster.Clientset.CoreV1().Namespaces().Create(ctx,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "worker-"}},
		metav1.CreateOptions{})
	if err != nil {
		return WorkerReady{}, fmt.Errorf("create worker namespace: %w", err)
	}
	registerNamespacePodCleanup(rec, cluster.Clientset, ns.Name)

	dbWorker, err := database.PersistNamedWorker(workerName)
	if err != nil {
		return WorkerReady{}, err
	}
	if teamName == "" {
		teamName = ns.Name
	}
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	if err != nil {
		return WorkerReady{}, fmt.Errorf("create team: %w", err)
	}

	config := jetbridge.NewConfig(ns.Name, "")
	if configure != nil {
		configure(&config)
	}

	ready := WorkerReady{
		DB:               database,
		Namespace:        ns.Name,
		Clientset:        cluster.Clientset,
		Config:           config,
		DBWorker:         dbWorker,
		TeamID:           team.ID(),
		Ctx:              ctx,
		VolumeRepo:       database.VolumeRepository,
		ProducerExecutor: jetbridge.NewSPDYExecutor(cluster.Clientset, cluster.RESTConfig),
	}
	return ready.rebuild(), nil
}

// rebuild reconstructs the worker from the current knobs. The order matters and
// is the reason this exists: SetArtifactLocator replaces the whole storage
// backend, which silently drops a daemon client set before it.
func (w WorkerReady) rebuild() WorkerReady {
	worker := jetbridge.NewWorker(w.DBWorker, w.Clientset, w.Config)
	if w.VolumeRepo != nil {
		worker.SetVolumeRepo(w.VolumeRepo)
	}
	if w.Executor != nil {
		worker.SetExecutor(w.Executor)
	}
	if w.Locator != nil {
		worker.SetArtifactLocator(w.Locator)
	}
	if w.DaemonClient != nil {
		worker.SetDaemonClient(w.DaemonClient)
	}
	w.Worker = worker
	return w
}

// withDaemon runs the production artifact daemon with only the named artifacts.
// StoreNewOutputs additionally registers files when a scenario creates outputs;
// there is no wildcard HTTP response or fixture implementation of a daemon route.
func (w WorkerReady) withDaemon(rec *brine.Recorder, bodies map[string]string) (WorkerReady, error) {
	host, err := discoveryIPv4()
	if err != nil {
		return WorkerReady{}, err
	}
	d, err := startRealDaemon()
	if err != nil {
		return WorkerReady{}, err
	}
	// Recorded, not panicked: the drain's recover() discards a panic whole,
	// message included, and skips the disposers under it.
	TrackDisposer(rec, "the worker's artifact daemon", d.stop)
	_, port, err := hostPortOfURL(d.URL)
	if err != nil {
		return WorkerReady{}, err
	}
	w.Daemon = d
	for key, body := range bodies {
		if err := w.storeDaemonArtifact(key, body); err != nil {
			return WorkerReady{}, err
		}
	}

	_, err = w.Clientset.DiscoveryV1().EndpointSlices(w.Namespace).Create(w.Ctx, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-abc",
			Namespace: w.Namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{host}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return WorkerReady{}, fmt.Errorf("publish the daemon endpoint slice: %w", err)
	}

	w.Config.ArtifactDaemonHostPath = d.Root
	w.Config.ArtifactDaemonService = "artifact-daemon"
	w.Config.ArtifactDaemonPort = port
	w.DaemonClient = jetbridge.NewDaemonClient(
		lagertest.NewTestLogger("daemon"), w.Clientset, w.Namespace, "artifact-daemon", port, nil)
	return w.rebuild(), nil
}

func (w WorkerReady) storeDaemonArtifact(key, body string) error {
	if w.Daemon == nil {
		return fmt.Errorf("cannot store artifact %q without a running daemon", key)
	}
	// These cases assert exact bytes, not tar encoding. Register a real file.
	path := filepath.Join(w.Daemon.Root, "outputs", key, "data")
	if err := writeArtifactFile(path, body); err != nil {
		return err
	}
	return registerDaemonArtifact(w.Ctx, http.DefaultClient, w.Daemon.URL, key, path)
}

// reapPod deletes only the observed UID and verifies API absence. Both
// intercept and artifact-lifetime scenarios use the same real deletion path.
func (w WorkerReady) reapPod(name string) error {
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	pod, err := pods.Get(w.Ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get pod before deletion: %w", err)
	}
	zero := int64(0)
	if err := pods.Delete(w.Ctx, pod.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &zero,
		Preconditions:      &metav1.Preconditions{UID: &pod.UID},
	}); err != nil {
		return fmt.Errorf("delete pod %q: %w", pod.Name, err)
	}
	if _, err := pods.Get(w.Ctx, pod.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return fmt.Errorf("reaped pod %q still exists or cannot be observed: %v", pod.Name, err)
	}
	return nil
}

func (w WorkerReady) wrapArtifact(vol runtime.Volume, handle string) ArtifactOutcome {
	return ArtifactOutcome{Ready: w, Artifact: w.Worker.ArtifactFromVolume(vol), Handle: handle}
}

func (w WorkerReady) podNames() ([]string, error) {
	list, err := w.Clientset.CoreV1().Pods(w.Namespace).List(w.Ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	names := make([]string, 0, len(list.Items))
	for _, pod := range list.Items {
		names = append(names, pod.Name)
	}
	return names, nil
}

func newContainerOutcome(in WorkerReady, handle string, container runtime.Container, err error) ContainerOutcome {
	out := ContainerOutcome{Ready: in, Container: container, Handle: handle, Found: err == nil, Err: err}
	if err != nil {
		out.Message = err.Error()
		out.Found = false
	}
	return out
}

func (o ContainerOutcome) ok() error {
	if o.Err != nil {
		return fmt.Errorf("the container request failed: %v", o.Err)
	}
	if o.Container == nil {
		return fmt.Errorf("no container came back for handle %q", o.Handle)
	}
	return nil
}

func (o VolumeOutcome) ok() error {
	if o.Err != nil {
		return fmt.Errorf("the volume request failed: %v", o.Err)
	}
	if o.Volume == nil {
		return fmt.Errorf("no volume came back")
	}
	return nil
}

func (o VolumeOutcome) found(noun string) error {
	if o.Err != nil {
		return fmt.Errorf("looking the %s up failed: %v", noun, o.Err)
	}
	if !o.Found || o.Volume == nil {
		return fmt.Errorf("expected the %s to be found, it was not", noun)
	}
	return nil
}

func (o VolumeOutcome) notFound(noun string) error {
	if o.Err != nil {
		return fmt.Errorf("expected a clean miss, got an error: %v", o.Err)
	}
	if o.Found {
		return fmt.Errorf("expected the %s not to be found, but it was", noun)
	}
	return nil
}

// readArtifact is the consumer's view of an artifact: open it and read what
// comes out. Volume.StreamOut hands back a pipe whose error surfaces on Read,
// so both halves are folded into one error here.
func readArtifact(ctx context.Context, source interface {
	StreamOut(context.Context, string, compression.Compression) (io.ReadCloser, error)
}) (string, error) {
	stream, err := source.StreamOut(ctx, ".", nil)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// failureMessage is the half of a "fails saying …" check that a combinator
// cannot do for itself: it decides whether the operation failed at all, and
// hands back the message for the mentions-comparison.
func failureMessage(what string, err error, message string) (string, error) {
	if err == nil {
		return "", fmt.Errorf("expected %s to fail, it succeeded", what)
	}
	return message, nil
}

// ---------------------------------------------------------------------------
// Dispositions — the two cases of the 37 that are not scenarios
// ---------------------------------------------------------------------------

// locatorDisposition records worker_test.go's "when a probe hit occurs / writes
// nothing to the ArtifactLocator for the cache key".
//
// The assertion is `locator.Locate("rc-42")` being false: the state of a
// collaborator, not anything a consumer can see. Its consequence IS observable,
// and it is precisely the "A cache hit survives being wrapped by the next step"
// scenario — that worker's locator is the one NewWorker built, so a probe hit
// that recorded the daemon pod IP under NodeName would make the very next
// WrapVolumeForLookup feed the IP to NodeIPResolver and fail the read with
// `nodes "<IP>" not found`. The scenario reads the bytes; that is the same
// guard expressed as an effect. Migrating both would assert one thing twice,
// once behaviorally and once through a keyhole.
const locatorDisposition = "expressed as its effect by: A cache hit survives being wrapped by the next step"

// sourceNodeDisposition records worker_test.go's "resolves the source node from
// the ArtifactLocator when the locator has an entry".
//
// The test seeds the locator with ("located-handle", "node-17", …), wraps a
// volume, and then asserts `dsVol.Source() == "k8s-worker-1"` — the WORKER
// name, which is returned unconditionally and has nothing to do with the
// locator or with node-17. Its own comment concedes it: the source node "is
// stored internally but is observable via StreamOut behavior (tested at the
// integration level)". So the case asserts nothing its title claims, and would
// pass with the locator empty, with a different node, or with the locator
// removed altogether. It is not migrated, and it should be deleted rather than
// translated: a scenario that cannot fail is worse in Gherkin, where it reads
// like a guarantee.
const sourceNodeDisposition = "not migrated: the assertion does not test its own title — see the comment above"

var _ = [...]string{locatorDisposition, sourceNodeDisposition}
