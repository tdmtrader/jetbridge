package steps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

// WorkerReady is a jetbridge worker on a fake Kubernetes cluster, backed by a
// real PostgreSQL database. Every Given in worker.feature refines this state
// and rebuilds the worker from it, because the worker's collaborators are all
// constructor- or setter-injected and several of the setters replace each other
// (SetArtifactLocator swaps the whole storage backend, dropping the daemon
// client — a real ordering hazard the ginkgo suite worked around by hand).
type WorkerReady struct {
	DB        JetbridgeDB
	Namespace string
	Clientset *fake.Clientset
	Config    jetbridge.Config
	DBWorker  db.Worker
	Worker    *jetbridge.Worker
	TeamID    int
	Ctx       context.Context

	// Knobs the Given steps turn. rebuild() reads all of them.
	VolumeRepo      db.VolumeRepository
	Executor        jetbridge.PodExecutor
	DaemonClient    *jetbridge.DaemonClient
	Locator         *jetbridge.ArtifactLocator
	ContainerFault  bool
	ProducerReaped  bool
	DaemonBodyByKey map[string]string

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
// Real adapters (not spies)
// ---------------------------------------------------------------------------

// reapedPodExecutor is a real PodExecutor for a cluster whose step pods have
// already been collected. Its named behavioral difference is exactly that: the
// pods are gone. It records nothing, so the only thing a scenario can assert is
// what a consumer of the artifact sees — which is the point. Without the
// DaemonSet wrap, a downstream read execs into the producer pod and dies here;
// with it, the read never touches this adapter at all.
type reapedPodExecutor struct{}

func (reapedPodExecutor) ExecInPod(
	context.Context,
	string, string, string,
	[]string,
	io.Reader,
	io.Writer, io.Writer,
	bool,
	jetbridge.ExecAttrs,
) error {
	return fmt.Errorf("exec stream: the producer pod has been reaped")
}

// stubResourceCache is a db.ResourceCache carrying only the two fields the key
// formatters read. It mirrors resource_cache_stub_test.go, which lives in a
// _test.go file and so cannot be imported: every method the code under test
// does not use panics rather than returning a zero value, so this cannot
// quietly grow into a stand-in for a real cache.
type stubResourceCache struct {
	id         int
	durableKey string
}

func (c stubResourceCache) ID() int            { return c.id }
func (c stubResourceCache) DurableKey() string { return c.durableKey }

func (stubResourceCache) Version() atc.Version {
	panic("stubResourceCache.Version: not modelled — see the type comment")
}

func (stubResourceCache) ResourceConfig() db.ResourceConfig {
	panic("stubResourceCache.ResourceConfig: not modelled — see the type comment")
}

func (stubResourceCache) Destroy(db.Tx) (bool, error) {
	panic("stubResourceCache.Destroy: not modelled — see the type comment")
}

func (stubResourceCache) BaseResourceType() *db.UsedBaseResourceType {
	panic("stubResourceCache.BaseResourceType: not modelled — see the type comment")
}

// The decorators below wrap the real PostgreSQL-backed worker and volume
// repository so that exactly one transition in the middle of a sequence fails.
// Everything before the fault is a real row, so what the worker leaves behind
// is asserted against the database rather than against a call count. Ported
// from the bottom of worker_test.go, which cannot be imported.
type failContainerCreatedTransition struct{ db.Worker }

func (w failContainerCreatedTransition) CreateContainer(owner db.ContainerOwner, meta db.ContainerMetadata) (db.CreatingContainer, error) {
	creating, err := w.Worker.CreateContainer(owner, meta)
	if err != nil {
		return nil, err
	}
	return creatingContainerFailsCreated{creating}, nil
}

type creatingContainerFailsCreated struct{ db.CreatingContainer }

func (creatingContainerFailsCreated) Created() (db.CreatedContainer, error) {
	return nil, fmt.Errorf("db connection lost")
}

type failVolumeCreatedTransitionRepo struct{ db.VolumeRepository }

func (r failVolumeCreatedTransitionRepo) CreateVolume(teamID int, workerName string, volumeType db.VolumeType) (db.CreatingVolume, error) {
	creating, err := r.VolumeRepository.CreateVolume(teamID, workerName, volumeType)
	if err != nil {
		return nil, err
	}
	return creatingVolumeFailsCreated{creating}, nil
}

type creatingVolumeFailsCreated struct{ db.CreatingVolume }

func (creatingVolumeFailsCreated) Created() (db.CreatedVolume, error) {
	return nil, fmt.Errorf("transition error")
}

type failInitializeArtifactRepo struct{ db.VolumeRepository }

func (r failInitializeArtifactRepo) CreateVolume(teamID int, workerName string, volumeType db.VolumeType) (db.CreatingVolume, error) {
	creating, err := r.VolumeRepository.CreateVolume(teamID, workerName, volumeType)
	if err != nil {
		return nil, err
	}
	return creatingVolumeFailsArtifact{creating}, nil
}

type creatingVolumeFailsArtifact struct{ db.CreatingVolume }

func (v creatingVolumeFailsArtifact) Created() (db.CreatedVolume, error) {
	created, err := v.CreatingVolume.Created()
	if err != nil {
		return nil, err
	}
	return createdVolumeFailsArtifact{created}, nil
}

type createdVolumeFailsArtifact struct{ db.CreatedVolume }

func (createdVolumeFailsArtifact) InitializeArtifact(string, int) (db.WorkerArtifact, error) {
	return nil, fmt.Errorf("artifact init error")
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
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (WorkerReady, error) {
				name, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a worker name parameter")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return WorkerReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}

				dbWorker, err := database.PersistNamedWorker(name)
				if err != nil {
					return WorkerReady{}, err
				}
				team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
				if err != nil {
					return WorkerReady{}, fmt.Errorf("create team: %w", err)
				}

				ready := WorkerReady{
					DB:         database,
					Namespace:  "test-namespace",
					Clientset:  fake.NewSimpleClientset(),
					Config:     jetbridge.NewConfig("test-namespace", ""),
					DBWorker:   dbWorker,
					TeamID:     team.ID(),
					Ctx:        context.Background(),
					VolumeRepo: database.VolumeRepository,
				}
				return ready.rebuild(), nil
			},
		),

		brine.DefineCheck[WorkerReady](
			"the worker answers to the name {string}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a name parameter")
				}
				if in.Worker.Name() != want {
					return fmt.Errorf("expected the worker to answer to %q, got %q", want, in.Worker.Name())
				}
				return nil
			},
		),

		// RC-01. False means "do not skip the cache", i.e. a get step is
		// allowed to serve a hit from the daemon instead of downloading again.
		brine.DefineCheck[WorkerReady](
			"the worker takes part in resource caching",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) error {
				if in.Worker.SkipResourceCache() {
					return fmt.Errorf("expected the worker to take part in resource caching, " +
						"but it reports that resource caches should be skipped")
				}
				return nil
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the database cannot transition containers to created",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				in.ContainerFault = true
				return in.rebuild(), nil
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the worker can exec into pods",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				in.Executor = localShellAdapter{}
				return in.rebuild(), nil
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the worker has no volume repository configured",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				in.VolumeRepo = nil
				return in.rebuild(), nil
			},
		),

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

		brine.DefineMap[WorkerReady, WorkerReady](
			"the volume repository cannot transition volumes to created",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				in.VolumeRepo = failVolumeCreatedTransitionRepo{in.DB.VolumeRepository}
				return in.rebuild(), nil
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the volume repository cannot initialise artifacts",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				in.VolumeRepo = failInitializeArtifactRepo{in.DB.VolumeRepository}
				return in.rebuild(), nil
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the producing pod has been reaped",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				in.ProducerReaped = true
				return in, nil
			},
		),

		// The daemon steps below all stand up a REAL HTTP server and a real
		// EndpointSlice, then point a real DaemonClient at it. Nothing is
		// simulated: the worker discovers the daemon the way it does in a
		// cluster, and the bytes a scenario asserts on travel over TCP.
		brine.DefineMap[WorkerReady, WorkerReady](
			"the cluster runs an artifact daemon holding every step output",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				return in.withDaemon(map[string]string{daemonWildcardKey: stepOutputBody})
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the cluster runs an artifact daemon holding the step output {string}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				key, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a step output key parameter")
				}
				return in.withDaemon(map[string]string{key: stepOutputBody})
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the cluster runs an artifact daemon holding the resource cache {int}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				id, ok := p.GetInt(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a cache id parameter")
				}
				return in.withDaemon(map[string]string{fmt.Sprintf("rc-%d", id): cachedBody})
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the cluster runs an artifact daemon holding nothing",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				return in.withDaemon(map[string]string{})
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"the worker has no artifact daemon configured",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				in.Config.ArtifactDaemonHostPath = ""
				in.Config.ArtifactDaemonService = ""
				in.Config.ArtifactDaemonPort = 0
				in.DaemonClient = nil
				in.Locator = nil
				return in.rebuild(), nil
			},
		),

		// A node roll leaves the locator naming a node that no longer exists.
		// SetArtifactLocator replaces the storage backend, which drops the
		// daemon client, so the rebuild re-applies it in that order.
		brine.DefineMap[WorkerReady, WorkerReady](
			"the worker still remembers the resource cache {int} on a node that has been rolled away",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				id, ok := p.GetInt(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected a cache id parameter")
				}
				key := fmt.Sprintf("rc-%d", id)
				locator := jetbridge.NewArtifactLocator()
				locator.Record(key, "10.0.0.99", key)
				in.Locator = locator
				return in.rebuild(), nil
			},
		),
	}
}

func workerContainerDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

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
				return in, in.createPod(name, nil, corev1.PodRunning)
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
					&noopDelegate{},
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

		brine.DefineCheck[ContainerOutcome](
			"the container request succeeds",
			func(in ContainerOutcome, _ brine.Params, _ *brine.Recorder) error {
				return in.ok()
			},
		),

		brine.DefineCheck[ContainerOutcome](
			"the container request fails saying {string}",
			func(in ContainerOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
				}
				return expectFailure("the container request", in.Err, in.Message, want)
			},
		),

		brine.DefineCheck[ContainerOutcome](
			"the container is found",
			func(in ContainerOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Err != nil {
					return fmt.Errorf("looking the container up failed: %v", in.Err)
				}
				if !in.Found {
					return fmt.Errorf("expected the container %q to be found, it was not", in.Handle)
				}
				return nil
			},
		),

		brine.DefineCheck[ContainerOutcome](
			"the container is not found",
			func(in ContainerOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Err != nil {
					return fmt.Errorf("expected a clean miss, got an error: %v", in.Err)
				}
				if in.Found {
					return fmt.Errorf("expected the container %q not to be found, but it was", in.Handle)
				}
				return nil
			},
		),

		// `fly intercept` hands the container's database row to the hijack
		// handler, which records the hijack against it. A nil row would panic
		// there rather than here.
		brine.DefineCheck[ContainerOutcome](
			"it carries the database row for handle {string}",
			func(in ContainerOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a handle parameter")
				}
				if err := in.ok(); err != nil {
					return err
				}
				row := in.Container.DBContainer()
				if row == nil {
					return fmt.Errorf("expected the container to carry a database row, it carries none")
				}
				if row.Handle() != want {
					return fmt.Errorf("expected the database row for %q, got %q", want, row.Handle())
				}
				return nil
			},
		),

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
		brine.DefineCheck[ContainerOutcome](
			"the container {string} is left in state {string}",
			func(in ContainerOutcome, p brine.Params, _ *brine.Recorder) error {
				handle, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a handle and a state")
				}
				var state string
				if err := in.Ready.DB.Conn.QueryRow(
					`SELECT state FROM containers WHERE handle = $1`, handle,
				).Scan(&state); err != nil {
					return fmt.Errorf("read the state of container %q: %w", handle, err)
				}
				if state != want {
					return fmt.Errorf("expected container %q to be left in state %q, it is %q", handle, want, state)
				}
				return nil
			},
		),

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

		brine.DefineCheck[ContainerRun](
			"no pod existed until the container ran",
			func(in ContainerRun, _ brine.Params, _ *brine.Recorder) error {
				if in.PodsBefore != 0 {
					return fmt.Errorf("expected no pod before the container ran, found %d", in.PodsBefore)
				}
				return nil
			},
		),

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
				if err := in.createPod(name, labels, corev1.PodRunning); err != nil {
					return WorkerReady{}, err
				}
				in.StepPodName = name
				return in, nil
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"that pod has since been reaped",
			func(in WorkerReady, _ brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				err := in.Clientset.CoreV1().Pods(in.Namespace).Delete(
					in.Ctx, in.StepPodName, metav1.DeleteOptions{})
				if err != nil {
					return WorkerReady{}, fmt.Errorf("delete pod %q: %w", in.StepPodName, err)
				}
				return in, nil
			},
		),

		brine.DefineMap[WorkerReady, WorkerReady](
			"that pod has since finished with exit status {string}",
			func(in WorkerReady, p brine.Params, _ *brine.Recorder) (WorkerReady, error) {
				status, ok := p.GetString(0)
				if !ok {
					return WorkerReady{}, fmt.Errorf("expected an exit status parameter")
				}
				pods := in.Clientset.CoreV1().Pods(in.Namespace)
				pod, err := pods.Get(in.Ctx, in.StepPodName, metav1.GetOptions{})
				if err != nil {
					return WorkerReady{}, fmt.Errorf("get pod %q: %w", in.StepPodName, err)
				}
				pod.Status.Phase = corev1.PodSucceeded
				pod.Annotations = map[string]string{exitStatusAnnotation: status}
				if _, err := pods.Update(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return WorkerReady{}, fmt.Errorf("update pod %q: %w", in.StepPodName, err)
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
				return in, in.createPod(in.StepHandle, nil, corev1.PodRunning)
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

				// A real interception lands in a pod whose /tmp belongs to that
				// pod alone. The local shell adapter runs in THIS host's /tmp,
				// which outlives the run, so a second invocation of the suite
				// would find the first one's supervisor state and replay its log
				// instead of exec-ing afresh. That is the adapter's platform
				// leaking, not anything the runtime does, so it is cleared at
				// the source rather than papered over in the assertion.
				if err := clearSupervisorState(handle); err != nil {
					return InterceptOutcome{}, err
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

		brine.DefineCheck[InterceptOutcome](
			"the interception succeeds",
			func(in InterceptOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Err != nil {
					return fmt.Errorf("expected the interception to succeed, it failed: %v", in.Err)
				}
				if in.ExitStatus != 0 {
					return fmt.Errorf("expected the intercepted command to exit 0, it exited %d (log: %q)",
						in.ExitStatus, in.Log)
				}
				return nil
			},
		),

		brine.DefineCheck[InterceptOutcome](
			"the operator sees {string}",
			func(in InterceptOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a text parameter")
				}
				if !strings.Contains(in.Log, want) {
					return fmt.Errorf("expected the operator to see %q, the session printed %q", want, in.Log)
				}
				return nil
			},
		),

		brine.DefineCheck[InterceptOutcome](
			"the interception fails saying {string}",
			func(in InterceptOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
				}
				return expectFailure("the interception", in.Err, in.Message, want)
			},
		),

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
		brine.DefineCheck[InterceptOutcome](
			"the pod {string} still records exit status {string}",
			func(in InterceptOutcome, p brine.Params, _ *brine.Recorder) error {
				name, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a pod name and an exit status")
				}
				pod, err := in.Ready.Clientset.CoreV1().Pods(in.Ready.Namespace).Get(
					in.Ready.Ctx, name, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("expected the pod %q to survive: %w", name, err)
				}
				if got := pod.Annotations[exitStatusAnnotation]; got != want {
					return fmt.Errorf("expected pod %q to still record exit status %q, it records %q",
						name, want, got)
				}
				return nil
			},
		),
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
				vol, found, err := in.Worker.FindDaemonResourceCache(in.Ctx, stubResourceCache{id: id})
				out := VolumeOutcome{Ready: in, Volume: vol, Found: found, Err: err}
				if err != nil {
					out.Message = err.Error()
				}
				return out, nil
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"creating the volume succeeds",
			func(in VolumeOutcome, _ brine.Params, _ *brine.Recorder) error {
				return in.ok()
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"creating the volume fails saying {string}",
			func(in VolumeOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
				}
				return expectFailure("creating the volume", in.Err, in.Message, want)
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"looking up the volume fails saying {string}",
			func(in VolumeOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
				}
				return expectFailure("looking up the volume", in.Err, in.Message, want)
			},
		),

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

		brine.DefineCheck[VolumeOutcome](
			"the volume row points at the artifact the caller was handed",
			func(in VolumeOutcome, _ brine.Params, _ *brine.Recorder) error {
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
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"the handle the caller got is the handle the database persisted",
			func(in VolumeOutcome, _ brine.Params, _ *brine.Recorder) error {
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
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"a volume for this worker is left in state {string}",
			func(in VolumeOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a state parameter")
				}
				var state string
				if err := in.Ready.DB.Conn.QueryRow(
					`SELECT state FROM volumes WHERE team_id = $1 AND worker_name = $2`,
					in.Ready.TeamID, in.Ready.DBWorker.Name(),
				).Scan(&state); err != nil {
					return fmt.Errorf("read the half-written volume: %w", err)
				}
				if state != want {
					return fmt.Errorf("expected the volume to be left in state %q, it is %q", want, state)
				}
				return nil
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"no artifact is recorded",
			func(in VolumeOutcome, _ brine.Params, _ *brine.Recorder) error {
				var count int
				if err := in.Ready.DB.Conn.QueryRow(`SELECT count(*) FROM worker_artifacts`).Scan(&count); err != nil {
					return fmt.Errorf("count artifacts: %w", err)
				}
				if count != 0 {
					return fmt.Errorf("expected no artifact to be recorded, found %d", count)
				}
				return nil
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"the volume is found",
			func(in VolumeOutcome, _ brine.Params, _ *brine.Recorder) error {
				return in.found("volume")
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"the volume is not found",
			func(in VolumeOutcome, _ brine.Params, _ *brine.Recorder) error {
				return in.notFound("volume")
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"the cache is found",
			func(in VolumeOutcome, _ brine.Params, _ *brine.Recorder) error {
				return in.found("cache")
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"the cache is not found",
			func(in VolumeOutcome, _ brine.Params, _ *brine.Recorder) error {
				return in.notFound("cache")
			},
		),

		brine.DefineCheck[VolumeOutcome](
			"the volume's handle is {string}",
			func(in VolumeOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a handle parameter")
				}
				if err := in.ok(); err != nil {
					return err
				}
				if in.Volume.Handle() != want {
					return fmt.Errorf("expected the handle %q, got %q", want, in.Volume.Handle())
				}
				return nil
			},
		),

		// Source() is what a cross-worker stream reads to decide where the
		// bytes live. It is the worker name, never a node or a pod IP.
		brine.DefineCheck[VolumeOutcome](
			"it reports {string} as its source",
			func(in VolumeOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a source parameter")
				}
				if err := in.ok(); err != nil {
					return err
				}
				if in.Volume.Source() != want {
					return fmt.Errorf("expected the source %q, got %q", want, in.Volume.Source())
				}
				return nil
			},
		),

		// The round trip. Nothing is asserted about how the volume reads —
		// only that the bytes arrive.
		brine.DefineCheck[VolumeOutcome](
			"reading the volume yields {string}",
			func(in VolumeOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected the expected content")
				}
				if err := in.ok(); err != nil {
					return err
				}
				body, err := readArtifact(in.Ready.Ctx, in.Volume)
				if err != nil {
					return fmt.Errorf("expected to read %q from the volume, the read failed: %w", want, err)
				}
				if body != want {
					return fmt.Errorf("expected the volume to hold %q, it holds %q", want, body)
				}
				return nil
			},
		),
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
					handle, in.Worker.Name(), in.producerExecutor(),
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

		brine.DefineCheck[ArtifactOutcome](
			"the artifact's handle is {string}",
			func(in ArtifactOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a handle parameter")
				}
				if in.Artifact == nil {
					return fmt.Errorf("expected an artifact with handle %q for volume %q, got none", want, in.Handle)
				}
				if in.Artifact.Handle() != want {
					return fmt.Errorf("expected the artifact handle %q, got %q", want, in.Artifact.Handle())
				}
				return nil
			},
		),

		brine.DefineCheck[ArtifactOutcome](
			"reading the artifact yields {string}",
			func(in ArtifactOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected the expected content")
				}
				if in.Artifact == nil {
					return fmt.Errorf("expected an artifact to read, got none")
				}
				body, err := readArtifact(in.Ready.Ctx, in.Artifact)
				if err != nil {
					return fmt.Errorf("expected to read %q from the artifact, the read failed: %w", want, err)
				}
				if body != want {
					return fmt.Errorf("expected the artifact to hold %q, it holds %q", want, body)
				}
				return nil
			},
		),

		brine.DefineCheck[ArtifactOutcome](
			"reading the artifact fails saying {string}",
			func(in ArtifactOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a message parameter")
				}
				if in.Artifact == nil {
					return fmt.Errorf("expected an artifact to read, got none")
				}
				body, err := readArtifact(in.Ready.Ctx, in.Artifact)
				if err == nil {
					return fmt.Errorf("expected the read to fail mentioning %q, it returned %q", want, body)
				}
				if !strings.Contains(err.Error(), want) {
					return fmt.Errorf("expected the read to fail mentioning %q, it failed with %q", want, err.Error())
				}
				return nil
			},
		),

		brine.DefineCheck[ArtifactOutcome](
			"no artifact is handed back",
			func(in ArtifactOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Artifact != nil {
					return fmt.Errorf("expected no artifact, got one with handle %q", in.Artifact.Handle())
				}
				return nil
			},
		),
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const (
	// exitStatusAnnotation is the annotation a finished step's pod carries so a
	// restarted web can resume rather than re-run it.
	exitStatusAnnotation = "concourse.ci/exit-status"

	// daemonWildcardKey makes the test daemon answer for any step-output key.
	// Scenarios that are testing the KEY name hold exactly one instead.
	daemonWildcardKey = "*"

	stepOutputBody = "step-output-bytes"
	cachedBody     = "cached-tar-data"
)

// rebuild reconstructs the worker from the current knobs. The order matters and
// is the reason this exists: SetArtifactLocator replaces the whole storage
// backend, which silently drops a daemon client set before it.
func (w WorkerReady) rebuild() WorkerReady {
	dbWorker := w.DBWorker
	if w.ContainerFault {
		dbWorker = failContainerCreatedTransition{w.DBWorker}
	}
	worker := jetbridge.NewWorker(dbWorker, w.Clientset, w.Config)
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

// withDaemon stands up a real HTTP artifact daemon, publishes it as an
// EndpointSlice the way the DaemonSet does, and points the worker at it.
//
// The server is deliberately not closed: brine's disposal hooks live on the
// resource plane, and this file may not add a resource. One listener per
// daemon-using scenario lives until the adapter process exits, which is the
// same lifetime the ginkgo suite's `defer daemon.Close()` effectively gave it
// within a spec.
func (w WorkerReady) withDaemon(bodies map[string]string) (WorkerReady, error) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, ok := daemonBodyFor(bodies, r.URL.Path)
		if !ok {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodHead {
			rw.WriteHeader(http.StatusOK)
			return
		}
		_, _ = rw.Write([]byte(body))
	}))

	addr := server.Listener.Addr().String()
	colon := strings.LastIndex(addr, ":")
	if colon < 0 {
		return WorkerReady{}, fmt.Errorf("daemon listening on %q, which has no port", addr)
	}
	host := addr[:colon]
	port, err := strconv.Atoi(addr[colon+1:])
	if err != nil {
		return WorkerReady{}, fmt.Errorf("daemon port %q: %w", addr[colon+1:], err)
	}

	_, err = w.Clientset.DiscoveryV1().EndpointSlices(w.Namespace).Create(w.Ctx, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-abc",
			Namespace: w.Namespace,
			Labels:    map[string]string{discoveryv1.LabelServiceName: "artifact-daemon"},
		},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{host}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return WorkerReady{}, fmt.Errorf("publish the daemon endpoint slice: %w", err)
	}

	w.Config.ArtifactDaemonHostPath = "/var/artifacts"
	w.Config.ArtifactDaemonService = "artifact-daemon"
	w.Config.ArtifactDaemonPort = port
	w.DaemonBodyByKey = bodies
	w.DaemonClient = jetbridge.NewDaemonClient(
		lagertest.NewTestLogger("daemon"), w.Clientset, w.Namespace, "artifact-daemon", port, nil)
	return w.rebuild(), nil
}

// daemonBodyFor mirrors the three URL shapes the runtime asks a daemon for:
// the resource-cache probe, the alias fetch, and the on-disk step fetch.
func daemonBodyFor(bodies map[string]string, path string) (string, bool) {
	trimmed := strings.TrimPrefix(path, "/")
	var key string
	switch {
	case strings.HasPrefix(trimmed, "resource-caches/"):
		key = strings.TrimPrefix(trimmed, "resource-caches/")
	case strings.HasPrefix(trimmed, "artifacts/steps/"):
		key = strings.TrimPrefix(trimmed, "artifacts/steps/")
	case strings.HasPrefix(trimmed, "artifacts/"):
		key = strings.TrimPrefix(trimmed, "artifacts/")
	default:
		return "", false
	}
	if body, ok := bodies[key]; ok {
		return body, true
	}
	if body, ok := bodies[daemonWildcardKey]; ok {
		return body, true
	}
	return "", false
}

// producerExecutor is what a container-mount volume would use to read itself:
// an exec into the pod that produced it. In these scenarios that pod is gone,
// which is the whole reason ArtifactFromVolume wraps the volume at all.
func (w WorkerReady) producerExecutor() jetbridge.PodExecutor {
	if w.ProducerReaped {
		return reapedPodExecutor{}
	}
	return localShellAdapter{}
}

func (w WorkerReady) wrapArtifact(vol runtime.Volume, handle string) ArtifactOutcome {
	return ArtifactOutcome{Ready: w, Artifact: w.Worker.ArtifactFromVolume(vol), Handle: handle}
}

func (w WorkerReady) createPod(name string, labels map[string]string, phase corev1.PodPhase) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: w.Namespace, Labels: labels},
		Status: corev1.PodStatus{
			Phase:      phase,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	if _, err := w.Clientset.CoreV1().Pods(w.Namespace).Create(w.Ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create pod %q: %w", name, err)
	}
	return nil
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

// clearSupervisorState removes any in-pod task-supervisor state left under the
// host's /tmp by an earlier invocation of the suite. The supervisor derives its
// state directory from the process ID and a hash of the command, so the glob is
// narrowed to the leading alphanumeric run of this scenario's handle — never a
// blanket sweep, which would take another scenario's state with it.
func clearSupervisorState(handle string) error {
	prefix := handle
	if i := strings.IndexFunc(handle, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}); i > 0 {
		prefix = handle[:i]
	}
	if len(prefix) < 4 {
		return fmt.Errorf("handle %q has no distinctive prefix to scope supervisor cleanup to", handle)
	}
	matches, err := filepath.Glob("/tmp/concourse-task-" + prefix + "*")
	if err != nil {
		return fmt.Errorf("look for stale supervisor state: %w", err)
	}
	for _, dir := range matches {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("clear stale supervisor state %q: %w", dir, err)
		}
	}
	return nil
}

func expectFailure(what string, err error, message, want string) error {
	if err == nil {
		return fmt.Errorf("expected %s to fail mentioning %q, it succeeded", what, want)
	}
	if !strings.Contains(message, want) {
		return fmt.Errorf("expected %s to fail mentioning %q, it failed with %q", what, want, message)
	}
	return nil
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
