package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
	atcresource "github.com/concourse/concourse/atc/resource"
	atcworker "github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/vars"
)

const execVersionRef = "ref"

func execVersionOf(ref string) atc.Version { return atc.Version{execVersionRef: ref} }

// -----------------------------------------------------------------------
// Delegate factories
// -----------------------------------------------------------------------

type execGetDelegates func(exec.RunState) exec.GetDelegate

func (f execGetDelegates) GetDelegate(state exec.RunState) exec.GetDelegate { return f(state) }

type execPutDelegates func(exec.RunState) exec.PutDelegate

func (f execPutDelegates) PutDelegate(state exec.RunState) exec.PutDelegate { return f(state) }

// -----------------------------------------------------------------------
// State
// -----------------------------------------------------------------------

// execCore holds the build and persisted outcomes shared by the live resource steps.
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
	liveGit  *execLiveGit
	liveTime *execLiveTime
	liveHook *execLiveHook
}

// ExecBuild is a build whose pipeline has a resource, ready to run get, put
// and the step combinators against it.
type ExecBuild struct {
	core *execCore

	putGetsHalfway bool
}

// ExecRun is a step that has run. Ok and Err are the two values a Step
// returns, and they are not the same thing: Err makes a build ERRORED and
// !Ok makes it FAILED, which distinguishes resource failure from infrastructure errors.
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

	ctx, cancel := context.WithCancel(execLogger("live-exec"))

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

// execPutPlan builds the put plan a scenario means when it says a step
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

// ExecHarnessDefinitions registers shared live resource setup and outcomes.
func ExecHarnessDefinitions() []brine.StepDefinition {
	defs := []brine.StepDefinition{
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

		Refine[ExecBuild]("the resource names a version and then fails",
			func(in ExecBuild, _ Args) ExecBuild {
				in.putGetsHalfway = true
				return in
			}),

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

		CheckMember[ExecRun]("the build log records the error {string}",
			"the errors on the build",
			func(in ExecRun) ([]string, error) { return in.core.errorMessages() }),

		CheckInt[ExecRun]("the build reported the put finishing with exit status {int}",
			"the exit status the put reported",
			func(in ExecRun) (int, error) {
				return in.core.soleFinish(event.EventTypeFinishPut)
			}),

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
	}
	defs = append(defs, LiveGetStepDefinitions()...)
	defs = append(defs, LiveTimeDefinitions()...)
	defs = append(defs, LiveRetryAbortDefinitions()...)
	defs = append(defs, LiveHookDefinitions()...)
	return defs
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

func newExecWorkerDB(database JetbridgeDB) atcworker.DB {
	return atcworker.NewDB(database.WorkerFactory, database.TeamFactory, database.VolumeRepository,
		db.NewTaskCacheFactory(database.Conn), db.NewWorkerTaskCacheFactory(database.Conn),
		db.NewResourceCacheFactory(database.Conn, database.LockFactory), db.NewWorkerBaseResourceTypeFactory(database.Conn), database.LockFactory)
}

func execPlainPipeline() atc.Config {
	return atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}}
}
