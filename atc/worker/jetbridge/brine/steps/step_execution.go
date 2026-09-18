package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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
	atcworker "github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/vars"
)

// StepExecutionDefinitions covers version selection/publication, pipeline
// updates, task preflight, retries and aborts. Assertions read real build events,
// resource/cache rows, pipeline state and the artifact repository.
//
// Pipeline artifact reads, task preflight and retry classification now use
// real dependencies; see exec_artifacts.go and exec_retry.go. The partial-output
// put uses an explicitly approved executable fault in a real resource pod; see
// live_partial_put.go. Live gets use live_get.go; publication and retries use
// live_time.go and live_retry_abort.go; hooks use live_hooks.go. There are no
// runtimetest workers, containers, processes or pool substitutes here.
//
// Old/new controls and mutation evidence live in V5-MIGRATION.md. The original
// Go tests retain argument/order/locking assertions not established by these
// observable outcomes; no retirement is implied by a passing Brine scenario.

// -----------------------------------------------------------------------
// The resource
// -----------------------------------------------------------------------

const execVersionRef = "ref"

func execVersionOf(ref string) atc.Version { return atc.Version{execVersionRef: ref} }

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

	Caches   db.ResourceCacheFactory
	State    exec.RunState
	cached   *execCachedResource
	liveGit  *execLiveGit
	liveTime *execLiveTime
	liveHook *execLiveHook

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

	putGetsHalfway bool

	retryFailure string
	aborted      bool
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
	Origin     event.Origin `json:"origin"`
	ExitStatus int          `json:"exit_status"`
	Version    atc.Version  `json:"version"`
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

// cachedVersions are the versions of every resource cache this build holds a
// use on.
func (c *execCore) cachedVersions() ([]string, error) {
	versions, err := c.resourceCacheVersions()
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(versions))
	for _, version := range versions {
		refs = append(refs, version[execVersionRef])
	}
	return refs, nil
}

func (c *execCore) resourceCacheVersions() ([]atc.Version, error) {
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

	refs := make([]atc.Version, 0, len(ids))
	for _, id := range ids {
		cache, found, err := c.Caches.FindResourceCacheByID(id)
		if err != nil {
			return nil, fmt.Errorf("look up resource cache %d: %w", id, err)
		}
		if !found {
			return nil, fmt.Errorf("the build uses resource cache %d, which does not exist", id)
		}
		refs = append(refs, cache.Version())
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

func (b ExecBuild) getStepWithPool(planID atc.PlanID, plan atc.GetPlan, pool exec.Pool) exec.Step {
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
		pool,
		0,
	)
}

func (b ExecBuild) putStepWithPool(planID atc.PlanID, plan atc.PutPlan, pool exec.Pool) exec.Step {
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
		pool,
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
	defs = append(defs, LiveGetStepDefinitions()...)
	defs = append(defs, LiveTimeDefinitions()...)
	defs = append(defs, LiveRetryAbortDefinitions()...)
	defs = append(defs, LiveHookDefinitions()...)
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
				return ExecBuild{
					core: core,
				}, nil
			},
		),

		brine.DefineMapUsing[ExecBuild, ExecBuild]("the chosen worker already holds a cache of version {string}",
			[]string{"real-cluster"},
			func(in ExecBuild, p brine.Params, rec *brine.Recorder, res brine.Resources) (ExecBuild, error) {
				ref, err := paramAt("the chosen worker already holds a cache of version {string}", p, 0)
				if err != nil {
					return in, err
				}
				in.core.cached, err = in.prepareCachedResource(rec, res, ref)
				return in, err
			}),

		Refine[ExecBuild]("the resource names a version and then fails",
			func(in ExecBuild, _ Args) ExecBuild {
				in.putGetsHalfway = true
				return in
			}),

		Refine[ExecBuild]("the step fails with an unreachable Kubernetes API",
			func(in ExecBuild, _ Args) ExecBuild {
				in.retryFailure = "api"
				return in
			}),

		Refine[ExecBuild]("the step requires an input artifact that was never produced",
			func(in ExecBuild, _ Args) ExecBuild {
				in.retryFailure = "input"
				return in
			}),

		Refine[ExecBuild]("the build is aborted as the API request fails",
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
				return in.runCachedGet(plan)
			},
		),

		brine.DefineMap[ExecBuild, ExecRun](
			"the put step runs, publishing version {string}",
			func(in ExecBuild, p brine.Params, rec *brine.Recorder) (ExecRun, error) {
				ref, err := paramAt("the put step runs, publishing version {string}", p, 0)
				if err != nil {
					return ExecRun{}, err
				}
				return in.runPartialPut(rec, ref)
			},
		),

		brine.DefineMapUsing[ExecBuild, ExecRun](
			"the step runs, with its failures classified for retry",
			[]string{"real-cluster"},
			func(in ExecBuild, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ExecRun, error) {
				return in.classifyRealFailure(rec, res)
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
			func(in ExecPipelineBuild, p brine.Params, rec *brine.Recorder) (ExecRun, error) {
				jobName, err := paramAt("the step sets \"some-pipeline\" to the job {string}", p, 0)
				if err != nil {
					return ExecRun{}, err
				}
				return in.runSetPipeline(rec, execPipelineYAML(jobName), "")
			},
		),

		brine.DefineMap[ExecPipelineBuild, ExecRun](
			"the step sets the \"other-team\" pipeline \"some-pipeline\" to the job {string}",
			func(in ExecPipelineBuild, p brine.Params, rec *brine.Recorder) (ExecRun, error) {
				jobName, err := paramAt("the step sets the \"other-team\" pipeline \"some-pipeline\" to the job {string}", p, 0)
				if err != nil {
					return ExecRun{}, err
				}
				return in.runSetPipeline(rec, execPipelineYAML(jobName), "other-team")
			},
		),

		brine.DefineMap[ExecPipelineBuild, ExecRun](
			"the step sets \"some-pipeline\" from a file with no jobs in it",
			func(in ExecPipelineBuild, _ brine.Params, rec *brine.Recorder) (ExecRun, error) {
				return in.runSetPipeline(rec, execInvalidPipelineYAML, "")
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

func (in ExecPipelineBuild) runSetPipeline(rec *brine.Recorder, fileContent, targetTeam string) (ExecRun, error) {
	const planID = atc.PlanID("set-1")

	ctx, cancel := context.WithTimeout(in.core.Ctx, 20*time.Second)
	defer cancel()
	volume, err := serveExecArtifact(ctx, rec, "pipeline-bits", map[string]string{
		"pipeline.yml": fileContent,
		"a-decoy.yml":  execPipelineYAML("not-the-requested-job"),
	})
	if err != nil {
		return ExecRun{}, err
	}
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

	ok, err := step.Run(ctx, in.core.State)
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

		brine.DefineMap[ExecTaskBuild, ExecTaskBuild]("the build has produced the artifact {string}",
			func(in ExecTaskBuild, p brine.Params, rec *brine.Recorder) (ExecTaskBuild, error) {
				name, ok := p.GetString(0)
				if !ok {
					return in, fmt.Errorf("expected an artifact name")
				}
				ctx, cancel := context.WithTimeout(in.core.Ctx, 20*time.Second)
				defer cancel()
				artifact, err := serveExecArtifact(ctx, rec, "artifact-"+name, map[string]string{"content": "produced " + name})
				if err != nil {
					return in, err
				}
				// Verify the present input is readable, not just an entry whose
				// name happens to satisfy the repository lookup.
				stream, err := atcworker.NewStreamer(compression.NewGzipCompression()).StreamFile(ctx, artifact, "content")
				if err != nil {
					return in, err
				}
				body, readErr := io.ReadAll(stream)
				closeErr := stream.Close()
				if readErr != nil || closeErr != nil || string(body) != "produced "+name {
					return in, fmt.Errorf("real artifact read: data=%q read=%v close=%v", body, readErr, closeErr)
				}
				in.core.State.ArtifactRepository().RegisterArtifact(
					execbuild.ArtifactName(name),
					artifact,
					false,
				)
				return in, nil
			}),

		brine.DefineMapUsing[ExecTaskBuild, ExecRun](
			"the task step runs",
			[]string{"real-cluster"},
			func(in ExecTaskBuild, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ExecRun, error) {
				const planID = atc.PlanID("task-1")
				ctx, cancel := context.WithTimeout(in.core.Ctx, 20*time.Second)
				defer cancel()
				pool, err := realExecTaskPool(ctx, rec, in.core.DB, res)
				if err != nil {
					return ExecRun{}, err
				}
				owner := db.NewBuildStepContainerOwner(in.core.Build.ID(), planID, in.core.Team.ID())
				selected, err := pool.FindOrSelectWorker(ctx, owner, runtime.ContainerSpec{Type: db.ContainerTypeTask}, atcworker.Spec{TeamID: in.core.Team.ID()})
				if err != nil || selected == nil || selected.Name() != execTaskWorkerName {
					return ExecRun{}, fmt.Errorf("real pool could not select its persisted worker: %v", err)
				}
				fmt.Printf("real task preflight pool selected %s\n", selected.Name())

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
					pool,
					atcworker.NewStreamer(compression.NewGzipCompression()),
					execTaskDelegates(func(state exec.RunState) exec.TaskDelegate {
						return engine.NewTaskDelegate(
							in.core.Build, planID, state, clock.NewClock(), policy.NoopChecker{},
							in.core.DB.WorkerFactory, in.core.DB.LockFactory,
						)
					}),
					0,
				)

				ok, err := step.Run(ctx, in.core.State)
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

		brine.DefineCheck[ExecRun](
			"the build published nothing at all",
			func(in ExecRun, _ brine.Params, _ *brine.Recorder) error {
				_, published, err := in.core.Build.Resources()
				if err != nil {
					return err
				}
				if len(published) > 0 {
					return fmt.Errorf("expected the build to have published nothing, it published %v", published)
				}
				return nil
			},
		),

		// A get registers its artifact under the PLAN's name whichever path
		// ran, so the name alone says only that something was registered —
		// which is why the two rows that used to assert it now assert the
		// flag registered with it instead.
		brine.DefineCheck[ExecRun](
			"the build's artifact {string} came from a cache on the worker",
			func(in ExecRun, p brine.Params, _ *brine.Recorder) error {
				return in.core.artifactProvenance(
					"the build's artifact {string} came from a cache on the worker", p)
			},
		),
	}
}

// artifactProvenance answers the fromCache flag the get registered alongside
// its artifact — the one thing about a get's result that separates bytes that
// were fetched from bytes the worker already had. artifactNames() cannot say
// it: the name is the plan's either way.
func (c *execCore) artifactProvenance(pattern string, p brine.Params) error {
	name, err := paramAt(pattern, p, 0)
	if err != nil {
		return err
	}
	artifact, fromCache, found := c.State.ArtifactRepository().ArtifactFor(execbuild.ArtifactName(name))
	if !found {
		return fmt.Errorf("expected the build to hold an artifact %q, the next step would see %v",
			name, c.artifactNames())
	}
	if !fromCache {
		return fmt.Errorf("expected the artifact %q to have come from a cache on the worker, it was registered as freshly fetched", name)
	}
	if c.cached == nil {
		return fmt.Errorf("cache provenance requires a real cached resource")
	}
	return c.cached.verify(c.Ctx, artifact)
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
