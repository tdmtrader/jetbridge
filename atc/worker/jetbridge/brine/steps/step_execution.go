package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	execbuild "github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/policy"
	atcresource "github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
	atcworker "github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/vars"
)

// StepExecutionDefinitions migrates the parts of atc/exec that describe what a
// KIND of step promises: which version a get fetches, what a put publishes,
// what set_pipeline does to a pipeline somebody else already set, what a task
// says when it cannot find its image or its inputs, and how a retry and an
// abort differ.
//
// MOST OF THE USUAL PAYOFF WAS COLLECTED BEFORE BRINE EXISTED, AND THAT
// CHANGED WHAT GOT WRITTEN.
//
// atc/exec's suite already runs against real PostgreSQL with the real engine
// delegates, reading real build_events back out. So the bar for every scenario
// below was the sentence alone, and most of what atc/exec asserts did not
// clear it. What is here is the handful of claims a pipeline author or an
// operator would recognise as a promise: a version that was pinned is the one
// that arrives, a put that failed publishes nothing, a pipeline is not rolled
// back by an older build, on_abort means abort.
//
// A CORRECTION TO WHAT THIS COMMENT FIRST SAID. Its heading was "there is no
// recording double to replace here", and that is false. worker_pool_test.go:69
// scriptedPool records the arguments the step hands the pool;
// get_step_test.go:49 recordingGetDelegate records the ORDER of the delegate
// calls; get_step_test.go:1115 recordingLockFactory counts lock acquisitions.
// The narrower statement that IS true is the reason all three are still there:
// each records something PostgreSQL cannot show, and those are exactly the
// assertions the dispositions in the feature file decline. execStepPool below
// is one of them replaced with a working one — so this file did the thing the
// heading said there was nothing left to do.
//
// THE RESOURCE ANSWERS. The ginkgo suite scripts a container process with a
// canned reply (`runtimetest.ProcessStub{Output: ...}`): the version that comes
// back is a constant the spec supplied, it comes back whatever was asked for,
// and nothing there reads the request the step wrote on stdin. The resource
// below holds versions and answers ONLY for a version it holds, refusing
// anything else the way a real `in` script refuses a ref that is not in the
// repository.
//
// What that buys, stated exactly, because the first version of this comment
// overstated it. It does NOT buy catching a get that lost its version pin —
// ginkgo catches that on the ROW rather than on the wire. MEASURED: with the
// `getPlan.Version != nil` arm of NewVersionSourceFromPlan disabled through a
// build overlay (production untouched),
//
//	go test -overlay=/tmp/ov.json ./atc/exec/ -run TestExec -args \
//	    -ginkgo.focus="constructs the resource cache correctly"
//
// reports "Ran 1 of 563 Specs ... 1 Failed", against "1 Passed" unmutated.
// What it does buy is a resource that can say NO: "pinned to v2" fails on the
// step itself rather than only on a row read afterwards, the cache-hit
// scenario can hold nothing at all, and a resource that refuses is a failed
// build rather than an errored one.
//
// It is also what makes the put/get chain real. A put creates a version by
// adding it to the same catalogue the get reads, so "the version the put
// created is the one the get after it fetched" is a round trip through the
// resource and the run state, not two constants compared.
//
// THE POOL ANSWERS TOO. exec.Pool is asked two questions, and both answers
// here come from state a scenario described rather than from a script:
// FindOrSelectWorker hands back the one worker (or the failure the scenario
// named), and FindResourceCacheVolumeOnWorker answers by the VERSION of the
// cache it is asked about. Keying on the version is deliberate: a step that
// resolved the wrong version misses a cache that is right there, which is what
// the cache-hit scenario is really pinning.
//
// WHAT IS READ AFTERWARDS. Every assertion is a row or a returned value:
// `build_events` (the log an operator reads, the finish events the UI renders,
// the error events that colour a step), `build_resource_config_version_outputs`
// via db.Build.Resources (the versions a build published), `resource_caches`
// via `resource_cache_uses` (which version this build's cache is for),
// `pipelines` (the config, its version, and the build recorded as having set
// it), and the artifact repository the next step reads. Nothing counts calls.

// -----------------------------------------------------------------------
// The resource
// -----------------------------------------------------------------------

// resourceRequest is what a resource script reads on stdin. It mirrors
// atc/resource.Resource's own JSON shape, which is the wire the production
// code writes — decoding it here is how a scenario finds out what the step
// actually asked for.
type execResourceRequest struct {
	Source  atc.Source  `json:"source"`
	Params  atc.Params  `json:"params,omitempty"`
	Version atc.Version `json:"version,omitempty"`
}

// resourceReply is what a resource script writes on stdout.
type execResourceReply struct {
	Version  atc.Version  `json:"version"`
	Metadata atc.Metadata `json:"metadata,omitempty"`
}

// resourceScript is one run of `/opt/resource/in` or `/opt/resource/out`.
type execResourceScript func(context.Context, *runtimetest.Process, execResourceRequest) (runtime.ProcessResult, error)

// versionCatalogue is what the resource holds. A get answers from it; a put
// adds to it, which is what makes a put followed by a get a round trip rather
// than two unrelated constants.
type execVersionCatalogue struct {
	mu   sync.Mutex
	held map[string]atc.Metadata
}

func newExecVersionCatalogue() *execVersionCatalogue {
	return &execVersionCatalogue{held: map[string]atc.Metadata{}}
}

func (c *execVersionCatalogue) hold(ref string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held[ref] = atc.Metadata{{Name: "ref", Value: ref}}
}

func (c *execVersionCatalogue) metadata(ref string) (atc.Metadata, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	md, ok := c.held[ref]
	return md, ok
}

// execVersionRef is the single field every version in this file carries. Using one
// field keeps the Gherkin able to name a version as a bare word while the
// value on the wire stays a real atc.Version map.
const execVersionRef = "ref"

func execVersionOf(ref string) atc.Version { return atc.Version{execVersionRef: ref} }

// servesHeldVersions is the `in` script: it answers for a version the
// catalogue holds and refuses anything else, which is how a lost or wrong
// version pin becomes a failing step rather than a silent success.
func (c *execVersionCatalogue) servesHeldVersions() execResourceScript {
	return func(_ context.Context, p *runtimetest.Process, req execResourceRequest) (runtime.ProcessResult, error) {
		ref := req.Version[execVersionRef]
		md, ok := c.metadata(ref)
		if !ok {
			fmt.Fprintf(p.Stderr(), "no version %q in this resource\n", ref)
			return runtime.ProcessResult{ExitStatus: 1}, nil
		}
		if err := json.NewEncoder(p.Stdout()).Encode(execResourceReply{Version: execVersionOf(ref), Metadata: md}); err != nil {
			return runtime.ProcessResult{}, err
		}
		return runtime.ProcessResult{ExitStatus: 0}, nil
	}
}

// createsVersion is the `out` script: it creates whatever version the step's
// params named, and holds it afterwards so a get can fetch it.
func (c *execVersionCatalogue) createsVersion(announce string) execResourceScript {
	return func(_ context.Context, p *runtimetest.Process, req execResourceRequest) (runtime.ProcessResult, error) {
		if announce != "" {
			fmt.Fprintf(p.Stderr(), "%s\n", announce)
		}
		ref, _ := req.Params["create"].(string)
		if ref == "" {
			fmt.Fprintln(p.Stderr(), "no version to create")
			return runtime.ProcessResult{ExitStatus: 1}, nil
		}
		c.hold(ref)
		if err := json.NewEncoder(p.Stdout()).Encode(execResourceReply{
			Version:  execVersionOf(ref),
			Metadata: atc.Metadata{{Name: "published", Value: ref}},
		}); err != nil {
			return runtime.ProcessResult{}, err
		}
		return runtime.ProcessResult{ExitStatus: 0}, nil
	}
}

// refuses is a resource script that runs and reports a non-zero exit. That is
// a resource saying no, which is a FAILED step; it is not the same as the
// script being unreachable, which is an ERRORED one, and the two scenarios
// that use these are about exactly that difference.
func scriptRefuses(announce string, status int) execResourceScript {
	return func(_ context.Context, p *runtimetest.Process, _ execResourceRequest) (runtime.ProcessResult, error) {
		if announce != "" {
			fmt.Fprintf(p.Stderr(), "%s\n", announce)
		}
		fmt.Fprintln(p.Stderr(), "the resource refused")
		return runtime.ProcessResult{ExitStatus: status}, nil
	}
}

// namesAVersionThenFails is the `out` script that got HALFWAY: it prints the
// version it was creating and then exits non-zero, which is what a push that
// wrote a tag and then lost the registry looks like.
//
// It has to print the version, because "a failed put publishes nothing" is
// only a claim about the step when there is something it COULD have
// published. Without this, no change to put_step.go can make that assertion
// fail, and it would be a sentence with nothing behind it.
func scriptNamesAVersionThenFails(status int) execResourceScript {
	return func(_ context.Context, p *runtimetest.Process, req execResourceRequest) (runtime.ProcessResult, error) {
		if ref, ok := req.Params["create"].(string); ok && ref != "" {
			if err := json.NewEncoder(p.Stdout()).Encode(execResourceReply{Version: execVersionOf(ref)}); err != nil {
				return runtime.ProcessResult{}, err
			}
		}
		fmt.Fprintln(p.Stderr(), "the resource got halfway and then failed")
		return runtime.ProcessResult{ExitStatus: status}, nil
	}
}

// unreachable is a resource script whose HOST goes away mid-run: the process
// returns an error rather than an exit status.
func scriptUnreachable(announce string) execResourceScript {
	return func(_ context.Context, p *runtimetest.Process, _ execResourceRequest) (runtime.ProcessResult, error) {
		if announce != "" {
			fmt.Fprintf(p.Stderr(), "%s\n", announce)
		}
		return runtime.ProcessResult{}, errors.New("the resource host went away")
	}
}

// stalls never answers, so a step with a timeout hits its deadline.
func scriptStalls() execResourceScript {
	return func(ctx context.Context, _ *runtimetest.Process, _ execResourceRequest) (runtime.ProcessResult, error) {
		<-ctx.Done()
		return runtime.ProcessResult{}, ctx.Err()
	}
}

// abortsTheBuild announces itself, cancels the build the way the abort button
// does, and then waits for the cancellation to reach it.
func scriptAbortsTheBuild(announce string, cancel func()) execResourceScript {
	return func(ctx context.Context, p *runtimetest.Process, _ execResourceRequest) (runtime.ProcessResult, error) {
		if announce != "" {
			fmt.Fprintf(p.Stderr(), "%s\n", announce)
		}
		cancel()
		<-ctx.Done()
		return runtime.ProcessResult{}, ctx.Err()
	}
}

// resourceStub adapts a resourceScript to the runtime's process contract,
// decoding the request the step wrote on stdin.
func execResourceStub(script execResourceScript) runtimetest.ProcessStub {
	return runtimetest.ProcessStub{
		Call: func(ctx context.Context, p *runtimetest.Process) (runtime.ProcessResult, error) {
			raw, err := io.ReadAll(p.Stdin())
			if err != nil {
				return runtime.ProcessResult{}, fmt.Errorf("read the resource request: %w", err)
			}
			var req execResourceRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return runtime.ProcessResult{}, fmt.Errorf("decode the resource request %q: %w", string(raw), err)
			}
			return script(ctx, p, req)
		},
	}
}

// -----------------------------------------------------------------------
// The pool
// -----------------------------------------------------------------------

// execPool is exec.Pool answering from what a scenario said is true, rather
// than from a script. There is one worker, so which one gets chosen is not
// what any scenario here is about; what IS answered from state is the cache
// lookup, keyed by the version of the cache being asked about.
type execStepPool struct {
	worker    runtime.Worker
	selectErr error

	mu     sync.Mutex
	cached map[string]runtime.Volume
}

func (p *execStepPool) FindOrSelectWorker(context.Context, db.ContainerOwner, runtime.ContainerSpec, atcworker.Spec) (runtime.Worker, error) {
	if p.selectErr != nil {
		return nil, p.selectErr
	}
	return p.worker, nil
}

func (p *execStepPool) FindResourceCacheVolumeOnWorker(_ context.Context, cache db.ResourceCache, _ atcworker.Spec, _ string, _ time.Time) (runtime.Volume, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	volume, found := p.cached[cache.Version()[execVersionRef]]
	if !found {
		return nil, false, nil
	}
	return volume, true, nil
}

func (p *execStepPool) LocateVolume(context.Context, int, string) (runtime.Volume, runtime.Worker, bool, error) {
	return nil, nil, false, nil
}

func (p *execStepPool) holdsCacheOf(ref string, volume runtime.Volume) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cached[ref] = volume
}

// -----------------------------------------------------------------------
// Delegate factories
// -----------------------------------------------------------------------

type execGetDelegates func(exec.RunState) exec.GetDelegate

func (f execGetDelegates) GetDelegate(state exec.RunState) exec.GetDelegate { return f(state) }

type execPutDelegates func(exec.RunState) exec.PutDelegate

func (f execPutDelegates) PutDelegate(state exec.RunState) exec.PutDelegate { return f(state) }

type execTaskDelegates func(exec.RunState) exec.TaskDelegate

func (f execTaskDelegates) TaskDelegate(state exec.RunState) exec.TaskDelegate { return f(state) }

type execSetPipelineDelegates func(exec.RunState) exec.SetPipelineStepDelegate

func (f execSetPipelineDelegates) SetPipelineStepDelegate(state exec.RunState) exec.SetPipelineStepDelegate {
	return f(state)
}

type execBuildStepDelegates func(exec.RunState) exec.BuildStepDelegate

func (f execBuildStepDelegates) BuildStepDelegate(state exec.RunState) exec.BuildStepDelegate {
	return f(state)
}

// -----------------------------------------------------------------------
// State
// -----------------------------------------------------------------------

// execCore is the build every scenario runs a step inside, and the handles the
// checks read afterwards. It is shared by all three Given types because the
// outcome type is shared: what a check wants to know is always "what did this
// build end up holding".
type execCore struct {
	DB     JetbridgeDB
	Ctx    context.Context
	Cancel context.CancelFunc

	Team     db.Team
	Pipeline db.Pipeline
	Job      db.Job
	Build    db.Build

	Caches db.ResourceCacheFactory
	State  exec.RunState

	// set_pipeline only. targetTeam is the OTHER team, and priorVersion is the
	// config version the target pipeline had before the step ran — read up
	// front so "was not written again" is an equality rather than an absence.
	targetTeam   db.Team
	targetRef    atc.PipelineRef
	priorVersion db.ConfigVersion
}

// ExecBuild is a build whose pipeline has a resource, ready to run get, put
// and the step combinators against it.
type ExecBuild struct {
	core *execCore

	Catalogue *execVersionCatalogue
	Pool      *execStepPool
	Worker    *runtimetest.Worker

	getTimeout     string
	stall          bool
	putGetsHalfway bool

	attempts []execAttempt
	hook     string

	stepFate  string
	selectErr error
	aborted   bool
}

// execAttempt is one attempt of a retried step. Each attempt is a real put
// step, so what an attempt leaves behind when it runs is a version in the
// database rather than a mark in a counter.
type execAttempt struct {
	number  int
	creates string
	abort   bool
}

// ExecPipelineBuild is a build whose step sets pipelines.
type ExecPipelineBuild struct {
	core *execCore
}

// ExecTaskBuild is a build whose step is a task.
type ExecTaskBuild struct {
	core *execCore

	config        atc.TaskConfig
	imageArtifact string
	inputMapping  map[string]string
}

// ExecRun is a step that has run. Ok and Err are the two values a Step
// returns, and they are not the same thing: Err makes a build ERRORED and
// !Ok makes it FAILED, which is the distinction three scenarios turn on.
type ExecRun struct {
	core *execCore

	Ok  bool
	Err error
}

// -----------------------------------------------------------------------
// Reading what the build ended up holding
// -----------------------------------------------------------------------

// payloadsOfType reads the raw persisted payloads of one event type. Raw,
// because the event parser maps several of these onto shapes that drop the
// fields the scenarios care about — the exit status on a finish, for one.
func (c *execCore) payloadsOfType(eventType atc.EventType) ([]json.RawMessage, error) {
	rows, err := c.DB.Conn.Query(`
		SELECT payload
		FROM build_events
		WHERE build_id = $1 AND type = $2
		ORDER BY event_id ASC
	`, c.Build.ID(), string(eventType))
	if err != nil {
		return nil, fmt.Errorf("read %s events: %w", eventType, err)
	}
	defer rows.Close()

	var payloads []json.RawMessage
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan a %s event: %w", eventType, err)
		}
		payloads = append(payloads, json.RawMessage(payload))
	}
	return payloads, rows.Err()
}

// log is everything the step wrote to the build, stdout and stderr together —
// which is what the build page shows as one stream.
func (c *execCore) log() (string, error) {
	payloads, err := c.payloadsOfType(event.EventTypeLog)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, payload := range payloads {
		var logged event.Log
		if err := json.Unmarshal(payload, &logged); err != nil {
			return "", fmt.Errorf("decode a log event: %w", err)
		}
		out.WriteString(logged.Payload)
	}
	return out.String(), nil
}

// errorMessages are the error events — the text an operator sees on a step
// that did not merely fail.
func (c *execCore) errorMessages() ([]string, error) {
	payloads, err := c.payloadsOfType(event.EventTypeError)
	if err != nil {
		return nil, err
	}
	var messages []string
	for _, payload := range payloads {
		var errored event.Error
		if err := json.Unmarshal(payload, &errored); err != nil {
			return nil, fmt.Errorf("decode an error event: %w", err)
		}
		messages = append(messages, errored.Message)
	}
	return messages, nil
}

type execFinish struct {
	ExitStatus int         `json:"exit_status"`
	Version    atc.Version `json:"version"`
}

func (c *execCore) finishes(eventType atc.EventType) ([]execFinish, error) {
	payloads, err := c.payloadsOfType(eventType)
	if err != nil {
		return nil, err
	}
	finishes := make([]execFinish, 0, len(payloads))
	for _, payload := range payloads {
		var finish execFinish
		if err := json.Unmarshal(payload, &finish); err != nil {
			return nil, fmt.Errorf("decode a %s event: %w", eventType, err)
		}
		finishes = append(finishes, finish)
	}
	return finishes, nil
}

// publishedVersions are the versions this build put onto its resources — the
// rows the resource page and the build's own output list are drawn from.
func (c *execCore) publishedVersions() ([]string, error) {
	_, outputs, err := c.Build.Resources()
	if err != nil {
		return nil, fmt.Errorf("read the build's outputs: %w", err)
	}
	refs := make([]string, 0, len(outputs))
	for _, output := range outputs {
		refs = append(refs, output.Version[execVersionRef])
	}
	return refs, nil
}

// cachedVersions are the versions of every resource cache this build holds a
// use on.
func (c *execCore) cachedVersions() ([]string, error) {
	rows, err := c.DB.Conn.Query(
		`SELECT resource_cache_id FROM resource_cache_uses WHERE build_id = $1`,
		c.Build.ID(),
	)
	if err != nil {
		return nil, fmt.Errorf("read the build's cache uses: %w", err)
	}
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan a cache use: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		cache, found, err := c.Caches.FindResourceCacheByID(id)
		if err != nil {
			return nil, fmt.Errorf("look up resource cache %d: %w", id, err)
		}
		if !found {
			return nil, fmt.Errorf("the build uses resource cache %d, which does not exist", id)
		}
		refs = append(refs, cache.Version()[execVersionRef])
	}
	return refs, nil
}

// artifactNames are what the next step in the build would be able to see.
func (c *execCore) artifactNames() []string {
	var names []string
	for name := range c.State.ArtifactRepository().AsMap() {
		names = append(names, string(name))
	}
	return names
}

// -----------------------------------------------------------------------
// Building the fixture
// -----------------------------------------------------------------------

func execLogger(session string) context.Context {
	return lagerctx.NewContext(context.Background(), lagertest.NewTestLogger(session))
}

func newExecCore(res brine.Resources, teamName, pipelineName string, config atc.Config) (*execCore, error) {
	database, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return nil, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
	}

	var team db.Team
	var err error
	if teamName == atc.DefaultTeamName {
		team, err = database.TeamFactory.CreateDefaultTeamIfNotExists()
	} else {
		team, err = database.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	}
	if err != nil {
		return nil, fmt.Errorf("create the team %q: %w", teamName, err)
	}

	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: pipelineName}, config, 0, false)
	if err != nil {
		return nil, fmt.Errorf("save the pipeline %q: %w", pipelineName, err)
	}

	job, found, err := pipeline.Job("some-job")
	if err != nil {
		return nil, fmt.Errorf("look up the job: %w", err)
	}
	if !found {
		return nil, errors.New("the pipeline has no job \"some-job\"")
	}

	build, err := job.CreateBuild("someone")
	if err != nil {
		return nil, fmt.Errorf("create the build: %w", err)
	}

	ctx, cancel := context.WithCancel(execLogger("step-execution"))

	return &execCore{
		DB:       database,
		Ctx:      ctx,
		Cancel:   cancel,
		Team:     team,
		Pipeline: pipeline,
		Job:      job,
		Build:    build,
		Caches:   db.NewResourceCacheFactory(database.Conn, database.LockFactory),
		State: exec.NewRunState(func(atc.Plan) exec.Step {
			return execUnbuildableStep{}
		}, vars.StaticVariables{}),
	}, nil
}

// execUnbuildableStep is the stepper. No scenario here runs a substep, and a
// stepper that quietly returned a no-op step would let one do so unnoticed.
type execUnbuildableStep struct{}

func (execUnbuildableStep) Run(context.Context, exec.RunState) (bool, error) {
	return false, errors.New("this scenario builds no substeps")
}

func (b ExecBuild) stepMetadata() exec.StepMetadata {
	return exec.StepMetadata{
		BuildID:      b.core.Build.ID(),
		BuildName:    b.core.Build.Name(),
		TeamID:       b.core.Team.ID(),
		TeamName:     b.core.Team.Name(),
		JobID:        b.core.Job.ID(),
		JobName:      b.core.Job.Name(),
		PipelineID:   b.core.Pipeline.ID(),
		PipelineName: b.core.Pipeline.Name(),
	}
}

// mountedContainer registers the container the step will find on the worker,
// with the resource process the scenario armed and a volume where the step
// looks for its result.
func (b ExecBuild) mountedContainer(planID atc.PlanID, spec runtime.ProcessSpec, script execResourceScript, mountPath string) {
	owner := db.NewBuildStepContainerOwner(b.core.Build.ID(), planID, b.core.Team.ID())
	container := runtimetest.NewContainer().WithProcess(spec, execResourceStub(script))
	volume := runtimetest.NewVolume("volume-" + string(planID))
	b.Worker.AddContainer(owner, container, []runtime.VolumeMount{
		{Volume: volume, MountPath: mountPath},
	})
}

var execGetProcess = runtime.ProcessSpec{
	ID:   "resource",
	Path: "/opt/resource/in",
	Args: []string{atcresource.ResourcesDir("get")},
}

var execPutProcess = runtime.ProcessSpec{
	ID:   "resource",
	Path: "/opt/resource/out",
	Args: []string{atcresource.ResourcesDir("put")},
}

func (b ExecBuild) getStep(planID atc.PlanID, plan atc.GetPlan, script execResourceScript) exec.Step {
	b.mountedContainer(planID, execGetProcess, script, atcresource.ResourcesDir("get"))
	return exec.NewGetStep(
		planID,
		plan,
		b.stepMetadata(),
		db.ContainerMetadata{
			WorkingDirectory: atcresource.ResourcesDir("get"),
			PipelineID:       b.core.Pipeline.ID(),
			Type:             db.ContainerTypeGet,
			StepName:         plan.Name,
		},
		b.core.DB.LockFactory,
		b.core.Caches,
		execGetDelegates(func(state exec.RunState) exec.GetDelegate {
			return engine.NewGetDelegate(b.core.Build, planID, state, clock.NewClock(), policy.NoopChecker{})
		}),
		b.Pool,
		0,
	)
}

func (b ExecBuild) putStep(planID atc.PlanID, plan atc.PutPlan, script execResourceScript) exec.Step {
	b.mountedContainer(planID, execPutProcess, script, atcresource.ResourcesDir("put"))
	return exec.NewPutStep(
		planID,
		plan,
		b.stepMetadata(),
		db.ContainerMetadata{
			WorkingDirectory: atcresource.ResourcesDir("put"),
			PipelineID:       b.core.Pipeline.ID(),
			Type:             db.ContainerTypePut,
			StepName:         plan.Name,
		},
		b.Pool,
		execPutDelegates(func(state exec.RunState) exec.PutDelegate {
			return engine.NewPutDelegate(b.core.Build, planID, state, clock.NewClock(), policy.NoopChecker{})
		}),
		0,
	)
}

// publishing builds the put plan a scenario means when it says a step
// publishes a version.
func execPutPlan(name, create string) atc.PutPlan {
	return atc.PutPlan{
		Name:      name,
		Type:      "some-base-type",
		TypeImage: atc.TypeImage{BaseType: "some-base-type"},
		Source:    atc.Source{"some": "source"},
		Params:    atc.Params{"create": create},
		Resource:  "some-resource",
		Inputs:    &atc.InputsConfig{Specified: []string{}},
	}
}

func execGetPlan(name string) atc.GetPlan {
	return atc.GetPlan{
		Name:      name,
		Type:      "some-base-type",
		TypeImage: atc.TypeImage{BaseType: "some-base-type"},
		Source:    atc.Source{"some": "source"},
		Resource:  "some-resource",
	}
}

// execResourcePipeline is the pipeline a resource scenario runs inside. The
// resource has to exist for a put to publish onto it.
func execResourcePipeline() atc.Config {
	return atc.Config{
		Resources: atc.ResourceConfigs{
			{Name: "some-resource", Type: "some-base-type", Source: atc.Source{"some": "source"}},
		},
		Jobs: atc.JobConfigs{{Name: "some-job"}},
	}
}

func execPlainPipeline() atc.Config {
	return atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}
}

// -----------------------------------------------------------------------
// Definitions
// -----------------------------------------------------------------------

// StepExecutionDefinitions is registered from registry.go.
func StepExecutionDefinitions() []brine.StepDefinition {
	defs := execResourceStepDefinitions()
	defs = append(defs, execSetPipelineDefinitions()...)
	defs = append(defs, execTaskDefinitions()...)
	defs = append(defs, execOutcomeDefinitions()...)
	return defs
}

// -----------------------------------------------------------------------
// Get, put, retry and abort
// -----------------------------------------------------------------------

func execResourceStepDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ExecBuild](
			"a build of a job whose pipeline has the resource \"some-resource\"",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ExecBuild, error) {
				core, err := newExecCore(res, "some-team", "some-pipeline", execResourcePipeline())
				if err != nil {
					return ExecBuild{}, err
				}
				worker := runtimetest.NewWorker("some-worker")
				return ExecBuild{
					core:      core,
					Catalogue: newExecVersionCatalogue(),
					Worker:    worker,
					Pool:      &execStepPool{worker: worker, cached: map[string]runtime.Volume{}},
				}, nil
			},
		),

		Refine[ExecBuild]("the resource holds version {string}",
			func(in ExecBuild, a Args) ExecBuild {
				in.Catalogue.hold(a.String(0))
				return in
			}),

		Refine[ExecBuild]("the chosen worker already holds a cache of version {string}",
			func(in ExecBuild, a Args) ExecBuild {
				ref := a.String(0)
				in.Pool.holdsCacheOf(ref, runtimetest.NewVolume("cache-of-"+ref))
				return in
			}),

		Refine[ExecBuild]("the resource script never answers",
			func(in ExecBuild, _ Args) ExecBuild {
				in.stall = true
				return in
			}),

		Refine[ExecBuild]("the get step is allowed {string} to finish",
			func(in ExecBuild, a Args) ExecBuild {
				in.getTimeout = a.String(0)
				return in
			}),

		Refine[ExecBuild]("the resource names a version and then fails",
			func(in ExecBuild, _ Args) ExecBuild {
				in.putGetsHalfway = true
				return in
			}),

		Refine[ExecBuild]("attempt {int} of the retried step fails",
			func(in ExecBuild, a Args) ExecBuild {
				in.attempts = append(in.attempts, execAttempt{number: a.Int(0)})
				return in
			}),

		Refine[ExecBuild]("attempt {int} of the retried step publishes version {string}",
			func(in ExecBuild, a Args) ExecBuild {
				in.attempts = append(in.attempts, execAttempt{number: a.Int(0), creates: a.String(1)})
				return in
			}),

		Refine[ExecBuild]("attempt {int} of the retried step fails, and the build is aborted while it runs",
			func(in ExecBuild, a Args) ExecBuild {
				in.attempts = append(in.attempts, execAttempt{number: a.Int(0), abort: true})
				return in
			}),

		Refine[ExecBuild]("the step is aborted while it runs",
			func(in ExecBuild, _ Args) ExecBuild {
				in.stepFate = "abort"
				return in
			}),

		Refine[ExecBuild]("the step cannot reach its resource host",
			func(in ExecBuild, _ Args) ExecBuild {
				in.stepFate = "unreachable"
				return in
			}),

		// The two fates the on_abort outline was missing. A resource that
		// refuses is a step that FAILED without erroring, which is the arm
		// on_abort.go returns from before it ever tests for cancellation; a
		// resource that answers is a step that succeeded. Neither may run the
		// hook, and neither had a row until the audit asked which one covered
		// "not for failures".
		Refine[ExecBuild]("the resource refuses the step",
			func(in ExecBuild, _ Args) ExecBuild {
				in.stepFate = "refused"
				return in
			}),

		Refine[ExecBuild]("the step does what it was asked",
			func(in ExecBuild, _ Args) ExecBuild {
				in.stepFate = "succeeds"
				return in
			}),

		Refine[ExecBuild]("the on_abort hook publishes version {string}",
			func(in ExecBuild, a Args) ExecBuild {
				in.hook = a.String(0)
				return in
			}),

		Refine[ExecBuild]("the step fails with an unreachable Kubernetes API",
			func(in ExecBuild, _ Args) ExecBuild {
				in.selectErr = &url.Error{
					Op:  "Get",
					URL: "https://10.96.0.1:443/api/v1/namespaces/concourse/pods",
					Err: errors.New("dial tcp 10.96.0.1:443: connect: connection refused"),
				}
				return in
			}),

		Refine[ExecBuild]("the step fails with an unknown resource type",
			func(in ExecBuild, _ Args) ExecBuild {
				in.selectErr = errors.New("unknown resource type: some-made-up-type")
				return in
			}),

		Refine[ExecBuild]("the build has already been aborted",
			func(in ExecBuild, _ Args) ExecBuild {
				in.aborted = true
				return in
			}),

		// ---------------------------------------------------------------
		// Running a step
		// ---------------------------------------------------------------

		brine.DefineMap[ExecBuild, ExecRun](
			"the get step runs, pinned to version {string}",
			func(in ExecBuild, p brine.Params, _ *brine.Recorder) (ExecRun, error) {
				ref, err := paramAt("the get step runs, pinned to version {string}", p, 0)
				if err != nil {
					return ExecRun{}, err
				}
				plan := execGetPlan("some-resource")
				pinned := execVersionOf(ref)
				plan.Version = &pinned
				plan.Timeout = in.getTimeout

				script := in.Catalogue.servesHeldVersions()
				if in.stall {
					script = scriptStalls()
				}

				ok, runErr := in.getStep("get-1", plan, script).Run(in.core.Ctx, in.core.State)
				return ExecRun{core: in.core, Ok: ok, Err: runErr}, nil
			},
		),

		brine.DefineMap[ExecBuild, ExecRun](
			"the put step runs, publishing version {string}",
			func(in ExecBuild, p brine.Params, _ *brine.Recorder) (ExecRun, error) {
				ref, err := paramAt("the put step runs, publishing version {string}", p, 0)
				if err != nil {
					return ExecRun{}, err
				}
				script := in.Catalogue.createsVersion("")
				if in.putGetsHalfway {
					script = scriptNamesAVersionThenFails(4)
				}
				ok, runErr := in.putStep("put-1", execPutPlan("some-resource", ref), script).Run(in.core.Ctx, in.core.State)
				return ExecRun{core: in.core, Ok: ok, Err: runErr}, nil
			},
		),

		// The put/get chain. This is one When rather than two because the
		// whole claim is that the SECOND step reads what the FIRST one left:
		// the get names no version of its own, only the plan the put ran under.
		brine.DefineMap[ExecBuild, ExecRun](
			"the build puts version {string} and then gets what the put created",
			func(in ExecBuild, p brine.Params, _ *brine.Recorder) (ExecRun, error) {
				ref, err := paramAt("the build puts version {string} and then gets what the put created", p, 0)
				if err != nil {
					return ExecRun{}, err
				}

				const putPlanID = atc.PlanID("put-1")
				putOk, putErr := in.putStep(putPlanID, execPutPlan("some-resource", ref), in.Catalogue.createsVersion("")).
					Run(in.core.Ctx, in.core.State)
				if putErr != nil {
					return ExecRun{core: in.core, Ok: putOk, Err: putErr}, nil
				}
				if !putOk {
					return ExecRun{core: in.core, Ok: false, Err: nil}, nil
				}

				getPlan := execGetPlan("some-resource")
				from := putPlanID
				getPlan.VersionFrom = &from

				ok, runErr := in.getStep("get-1", getPlan, in.Catalogue.servesHeldVersions()).
					Run(in.core.Ctx, in.core.State)
				return ExecRun{core: in.core, Ok: ok, Err: runErr}, nil
			},
		),

		brine.DefineMap[ExecBuild, ExecRun](
			"the retried step runs",
			func(in ExecBuild, _ brine.Params, _ *brine.Recorder) (ExecRun, error) {
				if len(in.attempts) == 0 {
					return ExecRun{}, errors.New("no attempts were described for the retried step")
				}
				// The number in "attempt 2 of the retried step ..." is for the
				// reader; the ORDER of the Givens is what decides which attempt
				// is which. This check keeps those two from disagreeing. It
				// guards the sentence, not production — deleting it changes no
				// scenario's verdict, only the message when one is misnumbered.
				steps := make([]exec.Step, 0, len(in.attempts))
				for i, attempt := range in.attempts {
					if attempt.number != i+1 {
						return ExecRun{}, fmt.Errorf(
							"the scenario describes attempt %d in position %d; attempts are run in the order they are written",
							attempt.number, i+1)
					}
					planID := atc.PlanID(fmt.Sprintf("attempt-%d", attempt.number))
					announce := fmt.Sprintf("attempt %d", attempt.number)

					var script execResourceScript
					switch {
					case attempt.abort:
						script = scriptAbortsTheBuild(announce, in.core.Cancel)
					case attempt.creates == "":
						script = scriptRefuses(announce, 3)
					default:
						script = in.Catalogue.createsVersion(announce)
					}

					steps = append(steps, in.putStep(planID, execPutPlan("some-resource", attempt.creates), script))
				}

				ok, runErr := exec.Retry(steps...).Run(in.core.Ctx, in.core.State)
				return ExecRun{core: in.core, Ok: ok, Err: runErr}, nil
			},
		),

		brine.DefineMap[ExecBuild, ExecRun](
			"the step runs with its on_abort hook",
			func(in ExecBuild, _ brine.Params, _ *brine.Recorder) (ExecRun, error) {
				if in.hook == "" {
					return ExecRun{}, errors.New("no on_abort hook was described")
				}

				var script execResourceScript
				switch in.stepFate {
				case "abort":
					script = scriptAbortsTheBuild("the step ran", in.core.Cancel)
				case "unreachable":
					script = scriptUnreachable("the step ran")
				case "refused":
					script = scriptRefuses("the step ran", 1)
				case "succeeds":
					script = in.Catalogue.createsVersion("the step ran")
				default:
					return ExecRun{}, fmt.Errorf("no fate was described for the step under the hook")
				}

				step := in.putStep("guarded", execPutPlan("guarded", "guarded"), script)
				hook := in.putStep("hook", execPutPlan("hook", in.hook), in.Catalogue.createsVersion("the hook ran"))

				ok, runErr := exec.OnAbort(step, hook).Run(in.core.Ctx, in.core.State)
				return ExecRun{core: in.core, Ok: ok, Err: runErr}, nil
			},
		),

		brine.DefineMap[ExecBuild, ExecRun](
			"the step runs, with its failures classified for retry",
			func(in ExecBuild, _ brine.Params, _ *brine.Recorder) (ExecRun, error) {
				if in.selectErr == nil {
					return ExecRun{}, errors.New("no failure was described for the step")
				}
				in.Pool.selectErr = in.selectErr

				ctx := in.core.Ctx
				if in.aborted {
					in.core.Cancel()
				}

				step := in.putStep("classified", execPutPlan("classified", "unreachable"), in.Catalogue.createsVersion(""))
				classified := exec.RetryError(step, execBuildStepDelegates(func(state exec.RunState) exec.BuildStepDelegate {
					return engine.NewBuildStepDelegate(in.core.Build, "classified", state, clock.NewClock(), policy.NoopChecker{}, false)
				}))

				ok, runErr := classified.Run(ctx, in.core.State)
				return ExecRun{core: in.core, Ok: ok, Err: runErr}, nil
			},
		),
	}
}

// -----------------------------------------------------------------------
// set_pipeline
// -----------------------------------------------------------------------

// execPipelineYAML is the file the set_pipeline step reads. It is written out
// rather than marshalled from a struct so that the config the step parses and
// the config a Given pre-seeds are byte-for-byte the same document — which is
// what makes "no changes to apply" a statement about the diff rather than
// about two encodings of the same thing.
func execPipelineYAML(jobName string) string {
	return fmt.Sprintf(`---
jobs:
- name: %s
  plan:
  - task: some-task
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      run:
        path: echo
        args: [hello]
`, jobName)
}

// execInvalidPipelineYAML parses cleanly and fails validation: a pipeline with
// no jobs at all, which is what an author is left with after deleting the last
// one.
const execInvalidPipelineYAML = `---
resources:
- name: orphaned
  type: git
  source: {uri: https://example.com/repo.git}
`

func execPipelineConfigFor(jobName string) (atc.Config, error) {
	var config atc.Config
	if err := atc.UnmarshalConfig([]byte(execPipelineYAML(jobName)), &config); err != nil {
		return atc.Config{}, fmt.Errorf("parse the pipeline for job %q: %w", jobName, err)
	}
	return config, nil
}

var execTargetRef = atc.PipelineRef{Name: "some-pipeline"}

func execSetPipelineDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ExecPipelineBuild](
			"a build of a job in the {string} team that sets pipelines",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (ExecPipelineBuild, error) {
				teamName, err := paramAt("a build of a job in the {string} team that sets pipelines", p, 0)
				if err != nil {
					return ExecPipelineBuild{}, err
				}
				core, err := newExecCore(res, teamName, "parent-pipeline", execPlainPipeline())
				if err != nil {
					return ExecPipelineBuild{}, err
				}
				core.targetRef = execTargetRef
				return ExecPipelineBuild{core: core}, nil
			},
		),

		// Seeded through the TEAM rather than through a build, so the target
		// pipeline has no parent build. A pipeline whose parent build is set
		// cannot be re-parented by an older one, and that rule is the subject
		// of its own scenario — it must not silently decide this one.
		brine.DefineMap[ExecPipelineBuild, ExecPipelineBuild](
			"the pipeline \"some-pipeline\" already has the job {string}",
			func(in ExecPipelineBuild, p brine.Params, _ *brine.Recorder) (ExecPipelineBuild, error) {
				jobName, err := paramAt("the pipeline \"some-pipeline\" already has the job {string}", p, 0)
				if err != nil {
					return in, err
				}
				config, err := execPipelineConfigFor(jobName)
				if err != nil {
					return in, err
				}
				pipeline, _, err := in.core.Team.SavePipeline(in.core.targetRef, config, 0, false)
				if err != nil {
					return in, fmt.Errorf("seed the target pipeline: %w", err)
				}
				in.core.priorVersion = pipeline.ConfigVersion()
				return in, nil
			},
		),

		// The real thing, not an injected sentinel: a SECOND build of the same
		// job — so its id is higher — sets the pipeline first. Everything the
		// step then meets, including db.ErrSetByNewerBuild, is produced by the
		// parent_build_id predicate in atc/db.
		brine.DefineMap[ExecPipelineBuild, ExecPipelineBuild](
			"a newer build of the same job already set \"some-pipeline\" to the job {string}",
			func(in ExecPipelineBuild, p brine.Params, _ *brine.Recorder) (ExecPipelineBuild, error) {
				jobName, err := paramAt("a newer build of the same job already set \"some-pipeline\" to the job {string}", p, 0)
				if err != nil {
					return in, err
				}
				newer, err := in.core.Job.CreateBuild("someone")
				if err != nil {
					return in, fmt.Errorf("create the newer build: %w", err)
				}
				if newer.ID() <= in.core.Build.ID() {
					return in, fmt.Errorf("the newer build has id %d, which is not newer than %d",
						newer.ID(), in.core.Build.ID())
				}
				config, err := execPipelineConfigFor(jobName)
				if err != nil {
					return in, err
				}
				pipeline, _, err := newer.SavePipeline(in.core.targetRef, in.core.Team.ID(), config, 0, false)
				if err != nil {
					return in, fmt.Errorf("let the newer build set the pipeline: %w", err)
				}
				in.core.priorVersion = pipeline.ConfigVersion()
				return in, nil
			},
		),

		brine.DefineMap[ExecPipelineBuild, ExecPipelineBuild](
			"the team \"other-team\" already has the pipeline \"some-pipeline\" with the job {string}",
			func(in ExecPipelineBuild, p brine.Params, _ *brine.Recorder) (ExecPipelineBuild, error) {
				jobName, err := paramAt("the team \"other-team\" already has the pipeline \"some-pipeline\" with the job {string}", p, 0)
				if err != nil {
					return in, err
				}
				other, err := in.core.DB.TeamFactory.CreateTeam(atc.Team{Name: "other-team"})
				if err != nil {
					return in, fmt.Errorf("create the other team: %w", err)
				}
				config, err := execPipelineConfigFor(jobName)
				if err != nil {
					return in, err
				}
				pipeline, _, err := other.SavePipeline(in.core.targetRef, config, 0, false)
				if err != nil {
					return in, fmt.Errorf("seed the other team's pipeline: %w", err)
				}
				in.core.targetTeam = other
				in.core.priorVersion = pipeline.ConfigVersion()
				return in, nil
			},
		),

		brine.DefineMap[ExecPipelineBuild, ExecRun](
			"the step sets \"some-pipeline\" to the job {string}",
			func(in ExecPipelineBuild, p brine.Params, _ *brine.Recorder) (ExecRun, error) {
				jobName, err := paramAt("the step sets \"some-pipeline\" to the job {string}", p, 0)
				if err != nil {
					return ExecRun{}, err
				}
				return in.runSetPipeline(execPipelineYAML(jobName), "")
			},
		),

		brine.DefineMap[ExecPipelineBuild, ExecRun](
			"the step sets the \"other-team\" pipeline \"some-pipeline\" to the job {string}",
			func(in ExecPipelineBuild, p brine.Params, _ *brine.Recorder) (ExecRun, error) {
				jobName, err := paramAt("the step sets the \"other-team\" pipeline \"some-pipeline\" to the job {string}", p, 0)
				if err != nil {
					return ExecRun{}, err
				}
				return in.runSetPipeline(execPipelineYAML(jobName), "other-team")
			},
		),

		brine.DefineMap[ExecPipelineBuild, ExecRun](
			"the step sets \"some-pipeline\" from a file with no jobs in it",
			func(in ExecPipelineBuild, _ brine.Params, _ *brine.Recorder) (ExecRun, error) {
				return in.runSetPipeline(execInvalidPipelineYAML, "")
			},
		),

		// ---------------------------------------------------------------
		// What the pipeline says afterwards
		// ---------------------------------------------------------------

		CheckString[ExecRun]("the pipeline now has the job {string}",
			"the job in the pipeline the step set",
			func(in ExecRun) (string, error) { return in.core.jobNameOf(in.core.Team) }),

		CheckString[ExecRun]("the other team's pipeline has the job {string}",
			"the job in the other team's pipeline",
			func(in ExecRun) (string, error) {
				if in.core.targetTeam == nil {
					return "", errors.New("this scenario described no other team")
				}
				return in.core.jobNameOf(in.core.targetTeam)
			}),

		CheckThat[ExecRun]("the pipeline was not written again",
			func(in ExecRun) error {
				pipeline, err := in.core.targetPipeline(in.core.Team)
				if err != nil {
					return err
				}
				if in.core.priorVersion == 0 {
					return errors.New("this scenario never recorded a config version to compare against")
				}
				if pipeline.ConfigVersion() != in.core.priorVersion {
					return fmt.Errorf("expected the pipeline to still be at config version %d, but it is at %d",
						in.core.priorVersion, pipeline.ConfigVersion())
				}
				return nil
			}),

		CheckThat[ExecRun]("the pipeline records this build as the one that set it",
			func(in ExecRun) error {
				pipeline, err := in.core.targetPipeline(in.core.Team)
				if err != nil {
					return err
				}
				if pipeline.ParentBuildID() != in.core.Build.ID() {
					return fmt.Errorf("expected the pipeline to name build %d as its parent, but it names %d",
						in.core.Build.ID(), pipeline.ParentBuildID())
				}
				if pipeline.ParentJobID() != in.core.Job.ID() {
					return fmt.Errorf("expected the pipeline to name job %d as its parent, but it names %d",
						in.core.Job.ID(), pipeline.ParentJobID())
				}
				return nil
			}),
	}
}

func (in ExecPipelineBuild) runSetPipeline(fileContent, targetTeam string) (ExecRun, error) {
	const planID = atc.PlanID("set-1")

	volume := runtimetest.NewVolume("pipeline-bits").WithContent(runtimetest.VolumeContent{
		"pipeline.yml": {Data: []byte(fileContent)},
	})
	in.core.State.ArtifactRepository().RegisterArtifact(execbuild.ArtifactName("some-source"), volume, false)

	step := exec.NewSetPipelineStep(
		planID,
		atc.SetPipelinePlan{
			Name: execTargetRef.Name,
			File: "some-source/pipeline.yml",
			Team: targetTeam,
		},
		exec.StepMetadata{
			BuildID:      in.core.Build.ID(),
			BuildName:    in.core.Build.Name(),
			TeamID:       in.core.Team.ID(),
			TeamName:     in.core.Team.Name(),
			JobID:        in.core.Job.ID(),
			JobName:      in.core.Job.Name(),
			PipelineID:   in.core.Pipeline.ID(),
			PipelineName: in.core.Pipeline.Name(),
		},
		execSetPipelineDelegates(func(state exec.RunState) exec.SetPipelineStepDelegate {
			return engine.NewSetPipelineStepDelegate(in.core.Build, planID, state, clock.NewClock(), policy.NoopChecker{})
		}),
		in.core.DB.TeamFactory,
		in.core.DB.BuildFactory,
		atcworker.NewStreamer(compression.NewGzipCompression()),
	)

	ok, err := step.Run(in.core.Ctx, in.core.State)
	return ExecRun{core: in.core, Ok: ok, Err: err}, nil
}

func (c *execCore) targetPipeline(team db.Team) (db.Pipeline, error) {
	pipeline, found, err := team.Pipeline(c.targetRef)
	if err != nil {
		return nil, fmt.Errorf("look up the pipeline %q for team %q: %w", c.targetRef.Name, team.Name(), err)
	}
	if !found {
		return nil, fmt.Errorf("the team %q has no pipeline %q", team.Name(), c.targetRef.Name)
	}
	return pipeline, nil
}

func (c *execCore) jobNameOf(team db.Team) (string, error) {
	pipeline, err := c.targetPipeline(team)
	if err != nil {
		return "", err
	}
	config, err := pipeline.Config()
	if err != nil {
		return "", fmt.Errorf("read the pipeline's config: %w", err)
	}
	if len(config.Jobs) != 1 {
		return "", fmt.Errorf("expected the pipeline to have exactly one job, it has %d", len(config.Jobs))
	}
	return config.Jobs[0].Name, nil
}

// -----------------------------------------------------------------------
// task
// -----------------------------------------------------------------------

func execTaskDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, ExecTaskBuild](
			"a build of a job running a task step",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ExecTaskBuild, error) {
				core, err := newExecCore(res, "some-team", "some-pipeline", execPlainPipeline())
				if err != nil {
					return ExecTaskBuild{}, err
				}
				return ExecTaskBuild{
					core: core,
					config: atc.TaskConfig{
						Platform:  "linux",
						RootfsURI: "raw:///busybox",
						Run:       atc.TaskRunConfig{Path: "echo", Args: []string{"hello"}},
					},
					inputMapping: map[string]string{},
				}, nil
			},
		),

		Refine[ExecTaskBuild]("the task takes its image from the artifact {string}",
			func(in ExecTaskBuild, a Args) ExecTaskBuild {
				in.imageArtifact = a.String(0)
				return in
			}),

		Refine[ExecTaskBuild]("the task requires the input {string}",
			func(in ExecTaskBuild, a Args) ExecTaskBuild {
				in.config.Inputs = append(in.config.Inputs, atc.TaskInputConfig{Name: a.String(0)})
				return in
			}),

		Refine[ExecTaskBuild]("the task requires the input {string}, supplied by the artifact {string}",
			func(in ExecTaskBuild, a Args) ExecTaskBuild {
				name, from := a.String(0), a.String(1)
				in.config.Inputs = append(in.config.Inputs, atc.TaskInputConfig{Name: name})
				in.inputMapping[name] = from
				return in
			}),

		Refine[ExecTaskBuild]("the task allows the optional input {string}",
			func(in ExecTaskBuild, a Args) ExecTaskBuild {
				in.config.Inputs = append(in.config.Inputs, atc.TaskInputConfig{Name: a.String(0), Optional: true})
				return in
			}),

		Refine[ExecTaskBuild]("the build has produced the artifact {string}",
			func(in ExecTaskBuild, a Args) ExecTaskBuild {
				name := a.String(0)
				in.core.State.ArtifactRepository().RegisterArtifact(
					execbuild.ArtifactName(name),
					runtimetest.NewVolume("artifact-"+name),
					false,
				)
				return in
			}),

		brine.DefineMap[ExecTaskBuild, ExecRun](
			"the task step runs",
			func(in ExecTaskBuild, _ brine.Params, _ *brine.Recorder) (ExecRun, error) {
				const planID = atc.PlanID("task-1")

				config := in.config
				step := exec.NewTaskStep(
					planID,
					atc.TaskPlan{
						Name:              "some-task",
						Config:            &config,
						ImageArtifactName: in.imageArtifact,
						InputMapping:      in.inputMapping,
					},
					atc.ContainerLimits{},
					atc.ContainerLimits{},
					exec.StepMetadata{
						BuildID:  in.core.Build.ID(),
						TeamID:   in.core.Team.ID(),
						TeamName: in.core.Team.Name(),
						JobID:    in.core.Job.ID(),
					},
					db.ContainerMetadata{
						WorkingDirectory: "/tmp/build/some-task",
						Type:             db.ContainerTypeTask,
						StepName:         "some-task",
					},
					&execStepPool{worker: runtimetest.NewWorker("some-worker"), cached: map[string]runtime.Volume{}},
					atcworker.NewStreamer(compression.NewGzipCompression()),
					execTaskDelegates(func(state exec.RunState) exec.TaskDelegate {
						return engine.NewTaskDelegate(
							in.core.Build, planID, state, clock.NewClock(), policy.NoopChecker{},
							in.core.DB.WorkerFactory, in.core.DB.LockFactory,
						)
					}),
					0,
				)

				ok, err := step.Run(in.core.Ctx, in.core.State)
				return ExecRun{core: in.core, Ok: ok, Err: err}, nil
			},
		),
	}
}

// -----------------------------------------------------------------------
// What the step reported, and what the build was left holding
// -----------------------------------------------------------------------

func execOutcomeDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		CheckThat[ExecRun]("the step succeeded",
			func(in ExecRun) error {
				if in.Err != nil {
					return fmt.Errorf("expected the step to succeed, but it errored: %s", in.Err)
				}
				if !in.Ok {
					return errors.New("expected the step to succeed, but it reported failure")
				}
				return nil
			}),

		// The distinction three scenarios turn on. A step that returns an
		// error makes the build ERRORED; a step that returns !ok makes it
		// FAILED. A resource that says no is the second kind, and turning it
		// into the first would tell an operator their infrastructure broke
		// when their build did.
		CheckThat[ExecRun]("the step failed rather than erroring",
			func(in ExecRun) error {
				if in.Err != nil {
					return fmt.Errorf("expected the step to fail without an error, but it errored: %s", in.Err)
				}
				if in.Ok {
					return errors.New("expected the step to fail, but it succeeded")
				}
				return nil
			}),

		CheckContains[ExecRun]("the step was refused, saying {string}",
			"the refusal",
			func(in ExecRun) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("expected the step to be refused, but it returned ok=%t and no error", in.Ok)
				}
				return in.Err.Error(), nil
			}),

		// Its own body: the claim is that the text appears NOWHERE in the
		// refusal, and it also asserts there was a refusal to look at.
		brine.DefineCheck[ExecRun](
			"the refusal does not mention {string}",
			func(in ExecRun, p brine.Params, _ *brine.Recorder) error {
				unwanted, err := paramAt("the refusal does not mention {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Err == nil {
					return fmt.Errorf("expected the step to be refused, but it returned ok=%t and no error", in.Ok)
				}
				if strings.Contains(in.Err.Error(), unwanted) {
					return fmt.Errorf("expected the refusal not to mention %q, got %q", unwanted, in.Err.Error())
				}
				return nil
			},
		),

		CheckThat[ExecRun]("the refusal is marked for retry",
			func(in ExecRun) error {
				if in.Err == nil {
					return errors.New("expected the step to be refused, but it returned no error")
				}
				var retriable exec.Retriable
				if !errors.As(in.Err, &retriable) {
					return fmt.Errorf("expected the refusal to be marked for retry, got %q", in.Err.Error())
				}
				return nil
			}),

		CheckThat[ExecRun]("the refusal is not marked for retry",
			func(in ExecRun) error {
				if in.Err == nil {
					return errors.New("expected the step to be refused, but it returned no error")
				}
				var retriable exec.Retriable
				if errors.As(in.Err, &retriable) {
					return fmt.Errorf("expected the refusal not to be marked for retry, but it was: %q", in.Err.Error())
				}
				return nil
			},
		),

		// --------------------------------------------------------------
		// The build log
		// --------------------------------------------------------------

		CheckContains[ExecRun]("the build log mentions {string}",
			"the build log",
			func(in ExecRun) (string, error) { return in.core.log() }),

		CheckMember[ExecRun]("the build log records the error {string}",
			"the errors on the build",
			func(in ExecRun) ([]string, error) { return in.core.errorMessages() }),

		// The operator-visible half of a retry. RetryErrorStep marks the error
		// for the engine AND writes "…, will retry ..." to the build, and only
		// the second one is somewhere a person can see. Contains rather than
		// equality because the cause is a url.Error whose text carries a port
		// and a path; the tail of the message is the part being claimed.
		CheckContains[ExecRun]("the build log records an error mentioning {string}",
			"the errors on the build",
			func(in ExecRun) (string, error) {
				messages, err := in.core.errorMessages()
				if err != nil {
					return "", err
				}
				return strings.Join(messages, "\n"), nil
			}),

		brine.DefineCheck[ExecRun](
			"the build log records no error at all",
			func(in ExecRun, _ brine.Params, _ *brine.Recorder) error {
				messages, err := in.core.errorMessages()
				if err != nil {
					return err
				}
				if len(messages) > 0 {
					return fmt.Errorf("expected the build to record no error, it recorded %v", messages)
				}
				return nil
			},
		),

		// --------------------------------------------------------------
		// Finish events
		// --------------------------------------------------------------

		CheckInt[ExecRun]("the build reported the get finishing with exit status {int}",
			"the exit status the get reported",
			func(in ExecRun) (int, error) {
				return in.core.soleFinish(event.EventTypeFinishGet)
			}),

		CheckInt[ExecRun]("the build reported the put finishing with exit status {int}",
			"the exit status the put reported",
			func(in ExecRun) (int, error) {
				return in.core.soleFinish(event.EventTypeFinishPut)
			}),

		brine.DefineCheck[ExecRun](
			"the build never reported the get finishing",
			func(in ExecRun, _ brine.Params, _ *brine.Recorder) error {
				finishes, err := in.core.finishes(event.EventTypeFinishGet)
				if err != nil {
					return err
				}
				if len(finishes) > 0 {
					return fmt.Errorf("expected the build to record no finish for the get, it recorded %d", len(finishes))
				}
				return nil
			},
		),

		CheckMember[ExecRun]("the build fetched version {string}",
			"the versions the build's get steps reported fetching",
			func(in ExecRun) ([]string, error) {
				finishes, err := in.core.finishes(event.EventTypeFinishGet)
				if err != nil {
					return nil, err
				}
				refs := make([]string, 0, len(finishes))
				for _, finish := range finishes {
					refs = append(refs, finish.Version[execVersionRef])
				}
				return refs, nil
			}),

		// --------------------------------------------------------------
		// Rows the build left behind
		// --------------------------------------------------------------

		CheckMember[ExecRun]("the build published version {string}",
			"the versions this build published",
			func(in ExecRun) ([]string, error) { return in.core.publishedVersions() }),

		brine.DefineCheck[ExecRun](
			"the build published nothing at all",
			func(in ExecRun, _ brine.Params, _ *brine.Recorder) error {
				published, err := in.core.publishedVersions()
				if err != nil {
					return err
				}
				if len(published) > 0 {
					return fmt.Errorf("expected the build to have published nothing, it published %v", published)
				}
				return nil
			},
		),

		CheckNotMember[ExecRun]("the build published no version {string}",
			"the versions this build published",
			func(in ExecRun) ([]string, error) { return in.core.publishedVersions() }),

		CheckMember[ExecRun]("the build holds a resource cache for version {string}",
			"the versions of the resource caches this build holds",
			func(in ExecRun) ([]string, error) { return in.core.cachedVersions() }),

		// A get registers its artifact under the PLAN's name whichever path
		// ran, so the name alone says only that something was registered —
		// which is why the two rows that used to assert it now assert the
		// flag registered with it instead.
		brine.DefineCheck[ExecRun](
			"the build's artifact {string} came from a cache on the worker",
			func(in ExecRun, p brine.Params, _ *brine.Recorder) error {
				return in.core.artifactProvenance(
					"the build's artifact {string} came from a cache on the worker", p, true)
			},
		),

		brine.DefineCheck[ExecRun](
			"the build's artifact {string} was fetched rather than taken from a cache",
			func(in ExecRun, p brine.Params, _ *brine.Recorder) error {
				return in.core.artifactProvenance(
					"the build's artifact {string} was fetched rather than taken from a cache", p, false)
			},
		),

		CheckNotMember[ExecRun]("the build's artifacts do not include {string}",
			"the artifacts the next step would see",
			func(in ExecRun) ([]string, error) { return in.core.artifactNames(), nil }),
	}
}

// artifactProvenance answers the fromCache flag the get registered alongside
// its artifact — the one thing about a get's result that separates bytes that
// were fetched from bytes the worker already had. artifactNames() cannot say
// it: the name is the plan's either way.
func (c *execCore) artifactProvenance(pattern string, p brine.Params, wantFromCache bool) error {
	name, err := paramAt(pattern, p, 0)
	if err != nil {
		return err
	}
	_, fromCache, found := c.State.ArtifactRepository().ArtifactFor(execbuild.ArtifactName(name))
	if !found {
		return fmt.Errorf("expected the build to hold an artifact %q, the next step would see %v",
			name, c.artifactNames())
	}
	if fromCache != wantFromCache {
		if wantFromCache {
			return fmt.Errorf("expected the artifact %q to have come from a cache on the worker, "+
				"it was registered as freshly fetched", name)
		}
		return fmt.Errorf("expected the artifact %q to have been fetched, "+
			"it was registered as coming from a cache on the worker", name)
	}
	return nil
}

// soleFinish reads the exit status of the single finish event of a kind,
// reporting rather than indexing when there is not exactly one — a scenario
// that produced two finishes is asking a question with no single answer.
func (c *execCore) soleFinish(eventType atc.EventType) (int, error) {
	finishes, err := c.finishes(eventType)
	if err != nil {
		return 0, err
	}
	if len(finishes) != 1 {
		return 0, fmt.Errorf("expected exactly one %s event on the build, found %d", eventType, len(finishes))
	}
	return finishes[0].ExitStatus, nil
}
