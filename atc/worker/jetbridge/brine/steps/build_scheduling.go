package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/creds/dummy"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/engine"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/scheduler"
	"github.com/concourse/concourse/atc/scheduler/algorithm"
	"github.com/concourse/concourse/vars"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/time/rate"
)

// BuildSchedulingDefinitions migrates the operator-facing half of
// atc/scheduler and atc/engine: which queued build starts and why not, and
// what happens to a build that is already running when something interrupts
// it.
//
// THERE IS NO DOUBLE IN THE SCHEDULING HALF. `Scheduler.Schedule` runs here
// with the real `algorithm.New(db.NewVersionsDB(...))` and the real
// `builds.NewPlanner`, over the scenario's own PostgreSQL, on a
// `db.SchedulerJob` fetched through `JobFactory.JobsToScheduleByIDs` — which
// is the production path, so Resources, ResourceTypes and Prototypes are the
// ones the ATC would pass rather than ones a test invented. That matters more
// than it sounds: the ginkgo suites reach several of their states by calling
// `job.SaveNextInputMapping(nil, false)` or by handing the starter a
// `SchedulerResources` literal, and those states are then only reachable from
// a test. Everything below is reached from a pipeline.
//
// The engine half has one, and it is a working step rather than a recording
// one: a leaf whose behaviour the scenario writes (block until cancelled,
// fail, panic), wrapped in production's own `exec.LogError` exactly as
// `coreStepFactory.LoadVarStep` wraps the real one. Nothing is asserted about
// what it was asked; every assertion reads the build row or the build log.

// -----------------------------------------------------------------------
// The queue
// -----------------------------------------------------------------------

// SchedulingJob is a job, its pipeline, and the builds queued for it, before
// the scheduler runs.
type SchedulingJob struct {
	DB         JetbridgeDB
	Scenario   *dbtest.Scenario
	JobFactory db.JobFactory
	Scheduler  *scheduler.Scheduler
	Ctx        context.Context

	JobName string

	// builds maps the scenario's name for a build to the row's id. Build names
	// in the database are ordinals ("1", "2", …) assigned in creation order,
	// which a reader of the feature file cannot place, so the mapping is kept
	// both ways and every failure message speaks the scenario's vocabulary.
	builds map[string]int
	named  map[int]string
}

// SchedulingPass is a completed scheduling pass: what the scheduler answered,
// and the queue as it found it.
type SchedulingPass struct {
	Job SchedulingJob

	// NeedsRetry is Schedule's own answer. It is not a call count: runner.go
	// skips `job.UpdateLastScheduled` when it is true, which leaves
	// `schedule_requested > last_scheduled` and so leaves the job in the set
	// `JobsToSchedule` returns on the next tick. False means the scheduler is
	// done with this job until something else requests it.
	NeedsRetry bool

	// Queue is the order GetPendingBuilds handed the starter, captured before
	// the pass ran. Head-of-line claims are meaningless without it: "the build
	// behind it still started" says nothing if the fixture put them the other
	// way round.
	Queue []string
}

func BuildSchedulingDefinitions() []brine.StepDefinition {
	defs := queueDefinitions()
	defs = append(defs, triggeringDefinitions()...)
	defs = append(defs, runningBuildDefinitions()...)
	return defs
}

func queueDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// The three job shapes. Each is a whole pipeline, because a job the
		// scheduler will look at has to be one: JobsToScheduleByIDs joins
		// through the pipeline for resources, types and prototypes, and a job
		// assembled any other way would be carrying a fixture's idea of its
		// own configuration.
		schedulingJobStep("a job that runs a task", taskJobConfig(0)),
		schedulingJobStep("a job that runs one build at a time", taskJobConfig(1)),

		// A resource with a version, checked BEFORE any build below is
		// created — which is what makes ResourcesChecked false for a build
		// created afterwards, the state a hand-triggered build starts in.
		brine.DefineMapUsing[brine.Empty, SchedulingJob](
			"a job that gets a resource, and a version of it already exists",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (SchedulingJob, error) {
				in, err := newSchedulingJob(res, getJobConfig())
				if err != nil {
					return SchedulingJob{}, err
				}
				return in, in.check(atc.Version{"ref": "v1"})
			},
		),

		// ...and the same pipeline whose resource has been checked and found
		// nothing. The check is what separates "not ready to decide yet" from
		// "decided, and there is nothing to run with".
		brine.DefineMapUsing[brine.Empty, SchedulingJob](
			"a job that gets a resource which has never produced a version",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (SchedulingJob, error) {
				in, err := newSchedulingJob(res, getJobConfig())
				if err != nil {
					return SchedulingJob{}, err
				}
				return in, in.check()
			},
		),

		brine.DefineMap[SchedulingJob, SchedulingJob](
			"the resource has been checked since",
			func(in SchedulingJob, _ brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				return in, in.check()
			},
		),

		// A no-op with a name. The check in the Given already happened before
		// the build was created, so "has not been checked since" is the state
		// the scenario is already in — but an Examples table that said nothing
		// in this column would leave a reader guessing which row was which.
		brine.DefineMap[SchedulingJob, SchedulingJob](
			"the resource has not been checked since",
			func(in SchedulingJob, _ brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				return in, nil
			},
		),

		// The two ways a build joins the queue, kept apart because the starter
		// treats them differently: a hand-triggered build re-runs the
		// algorithm and waits for its resources, a scheduler-queued one
		// adopts what the last pass already decided.
		brine.DefineMap[SchedulingJob, SchedulingJob](
			"the build {string} was triggered by hand",
			func(in SchedulingJob, p brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				name, err := paramAt("the build {string} was triggered by hand", p, 0)
				if err != nil {
					return SchedulingJob{}, err
				}
				job, err := in.job()
				if err != nil {
					return SchedulingJob{}, err
				}
				build, err := job.CreateBuild("someone")
				if err != nil {
					return SchedulingJob{}, fmt.Errorf("trigger build %q by hand: %w", name, err)
				}
				if !build.IsManuallyTriggered() {
					return SchedulingJob{}, fmt.Errorf("build %q was not recorded as manually triggered", name)
				}
				in.name(name, build.ID())
				return in, nil
			},
		),

		brine.DefineMap[SchedulingJob, SchedulingJob](
			"the build {string} was queued by the scheduler",
			func(in SchedulingJob, p brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				name, err := paramAt("the build {string} was queued by the scheduler", p, 0)
				if err != nil {
					return SchedulingJob{}, err
				}
				job, err := in.job()
				if err != nil {
					return SchedulingJob{}, err
				}
				before, err := job.GetPendingBuilds()
				if err != nil {
					return SchedulingJob{}, fmt.Errorf("read the queue: %w", err)
				}
				// EnsurePendingBuildExists inserts only when nothing is
				// pending, so a scenario that queued one this way after some
				// other build would silently get nothing at all.
				if len(before) > 0 {
					return SchedulingJob{}, fmt.Errorf(
						"the scheduler can only queue a build when the queue is empty, and %d were already waiting",
						len(before))
				}
				if err := job.EnsurePendingBuildExists(in.Ctx); err != nil {
					return SchedulingJob{}, fmt.Errorf("queue build %q: %w", name, err)
				}
				after, err := job.GetPendingBuilds()
				if err != nil {
					return SchedulingJob{}, fmt.Errorf("read the queue: %w", err)
				}
				if len(after) != 1 {
					return SchedulingJob{}, fmt.Errorf("expected the scheduler to queue one build, found %d", len(after))
				}
				in.name(name, after[0].ID())
				return in, nil
			},
		),

		// Cancelling a queued build sets the aborted flag; the row stays
		// pending until a scheduling pass reaches it. That gap is the whole
		// subject of the scenario that uses this.
		brine.DefineMap[SchedulingJob, SchedulingJob](
			"the build {string} was cancelled before the scheduler reached it",
			func(in SchedulingJob, p brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				name, err := paramAt("the build {string} was cancelled before the scheduler reached it", p, 0)
				if err != nil {
					return SchedulingJob{}, err
				}
				build, err := in.build(name)
				if err != nil {
					return SchedulingJob{}, err
				}
				if err := build.MarkAsAborted(); err != nil {
					return SchedulingJob{}, fmt.Errorf("cancel build %q: %w", name, err)
				}
				return in, nil
			},
		),

		// The build already occupying a serial job. It is started by a real
		// scheduling pass rather than by calling ScheduleBuild and Start
		// directly, because a hand-started build is not the same row: the
		// serial-group admission query requires `j.inputs_determined`, which
		// only a pass sets. Reaching this state by hand — which is what
		// `SaveNextInputMapping(nil, true)` is doing in the ginkgo suite —
		// would mean the scenario's premise was one production never
		// produces.
		brine.DefineMap[SchedulingJob, SchedulingJob](
			"the build {string} is already running",
			func(in SchedulingJob, p brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				name, err := paramAt("the build {string} is already running", p, 0)
				if err != nil {
					return SchedulingJob{}, err
				}
				job, err := in.job()
				if err != nil {
					return SchedulingJob{}, err
				}
				build, err := job.CreateBuild("someone")
				if err != nil {
					return SchedulingJob{}, fmt.Errorf("create build %q: %w", name, err)
				}
				in.name(name, build.ID())

				if _, err := in.schedule(); err != nil {
					return SchedulingJob{}, fmt.Errorf("start build %q with a scheduling pass: %w", name, err)
				}

				running, err := in.build(name)
				if err != nil {
					return SchedulingJob{}, err
				}
				if running.Status() != db.BuildStatusStarted {
					return SchedulingJob{}, fmt.Errorf(
						"expected a scheduling pass to start build %q, it is %s", name, running.Status())
				}
				return in, nil
			},
		),

		// The original of a rerun that can never resolve its inputs: it
		// finished without ever adopting any, so AdoptRerunInputsAndPipes has
		// nothing to copy.
		brine.DefineMap[SchedulingJob, SchedulingJob](
			"the build {string} failed before its inputs were ever resolved",
			func(in SchedulingJob, p brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				name, err := paramAt("the build {string} failed before its inputs were ever resolved", p, 0)
				if err != nil {
					return SchedulingJob{}, err
				}
				job, err := in.job()
				if err != nil {
					return SchedulingJob{}, err
				}
				build, err := job.CreateBuild("someone")
				if err != nil {
					return SchedulingJob{}, fmt.Errorf("create build %q: %w", name, err)
				}
				if err := build.Finish(db.BuildStatusFailed); err != nil {
					return SchedulingJob{}, fmt.Errorf("fail build %q: %w", name, err)
				}
				if build.InputsReady() {
					return SchedulingJob{}, fmt.Errorf("build %q resolved its inputs after all, so a rerun of it would not be stuck", name)
				}
				in.name(name, build.ID())
				return in, nil
			},
		),

		brine.DefineMap[SchedulingJob, SchedulingJob](
			"the build {string} is a rerun of {string}",
			func(in SchedulingJob, p brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				name, of, err := twoParams("the build {string} is a rerun of {string}", p)
				if err != nil {
					return SchedulingJob{}, err
				}
				job, err := in.job()
				if err != nil {
					return SchedulingJob{}, err
				}
				original, err := in.build(of)
				if err != nil {
					return SchedulingJob{}, err
				}
				rerun, err := job.RerunBuild(original, "someone")
				if err != nil {
					return SchedulingJob{}, fmt.Errorf("rerun build %q: %w", of, err)
				}
				in.name(name, rerun.ID())
				return in, nil
			},
		),

		// The pass. Requesting a schedule and then reading the job back
		// through JobsToScheduleByIDs is what the component runner does; it is
		// also the only way to get a SchedulerJob whose Resources are the
		// pipeline's rather than a literal.
		brine.DefineMap[SchedulingJob, SchedulingPass](
			"the scheduler runs for this job",
			func(in SchedulingJob, _ brine.Params, _ *brine.Recorder) (SchedulingPass, error) {
				return in.schedule()
			},
		),

		// Two passes. The first is setup for a scenario about the second.
		brine.DefineMap[SchedulingJob, SchedulingJob](
			"the scheduler has already run once for this job",
			func(in SchedulingJob, _ brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				_, err := in.schedule()
				return in, err
			},
		),

		// ...and the state that first pass has to have reached for a scenario
		// about CLEARING the flag to mean anything. `HasNewInputs()==false` is
		// also the state of a job the flag was never written for, so a
		// clearing scenario that only reads the end state passes on a
		// scheduler that never sets it. This is a Given-side read, stated in
		// the feature file rather than hidden in the pass above, because it is
		// a precondition a reader has to see to trust the Then.
		brine.DefineMap[SchedulingJob, SchedulingJob](
			"that pass flagged the job as having new inputs",
			func(in SchedulingJob, _ brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				job, err := in.job()
				if err != nil {
					return SchedulingJob{}, err
				}
				if _, err := job.Reload(); err != nil {
					return SchedulingJob{}, fmt.Errorf("reload the job: %w", err)
				}
				if !job.HasNewInputs() {
					return SchedulingJob{}, fmt.Errorf(
						"the first pass left the job's new-inputs flag unset, so there is nothing " +
							"for the second pass to clear and the scenario would pass on a scheduler " +
							"that never writes the flag at all")
				}
				return in, nil
			},
		),

		CheckThat[SchedulingPass]("the scheduler will come back to this job",
			func(in SchedulingPass) error {
				if !in.NeedsRetry {
					return fmt.Errorf("the scheduler reported it was finished with this job; " +
						"runner.go would advance last_scheduled and the job would drop out of " +
						"JobsToSchedule, so nothing would look at it again until something else asked")
				}
				return nil
			}),

		CheckThat[SchedulingPass]("the scheduler is finished with this job for now",
			func(in SchedulingPass) error {
				if in.NeedsRetry {
					return fmt.Errorf("the scheduler asked to come back; runner.go would leave " +
						"schedule_requested ahead of last_scheduled and the job would be " +
						"rescheduled on every tick")
				}
				return nil
			}),

		CheckString[SchedulingPass]("the scheduler found {string} at the head of the queue",
			"the build at the head of the queue",
			func(in SchedulingPass) (string, error) { return in.head() },
			func(in SchedulingPass) string { return "the queue was " + strings.Join(in.Queue, ", ") }),

		CheckMember[SchedulingPass]("the build {string} is running",
			"the builds that are running",
			func(in SchedulingPass) ([]string, error) { return in.buildsWith(db.BuildStatusStarted) },
			func(in SchedulingPass) string { return in.summary() }),

		CheckMember[SchedulingPass]("the build {string} is still queued",
			"the builds still queued",
			func(in SchedulingPass) ([]string, error) { return in.buildsWith(db.BuildStatusPending) },
			func(in SchedulingPass) string { return in.summary() }),

		CheckMember[SchedulingPass]("the build {string} was cancelled",
			"the builds recorded as cancelled",
			func(in SchedulingPass) ([]string, error) { return in.buildsWith(db.BuildStatusAborted) },
			func(in SchedulingPass) string { return in.summary() }),

		// A build that never ran has no plan on its row. That is what a web
		// restarting mid-pass reads, and it is the difference between "the
		// scheduler declined to run this" and "the scheduler ran it and the
		// work is somewhere".
		CheckNotMember[SchedulingPass]("the build {string} never got a plan",
			"the builds carrying a plan",
			func(in SchedulingPass) ([]string, error) { return in.buildsWithAPlan() },
			func(in SchedulingPass) string { return in.summary() }),

		CheckMember[SchedulingPass]("the build {string} carries the plan it will run",
			"the builds carrying a plan",
			func(in SchedulingPass) ([]string, error) { return in.buildsWithAPlan() },
			func(in SchedulingPass) string { return in.summary() }),

		// `builds.scheduled` is the column job.ScheduleBuild sets when the
		// starter ADMITS a build (db/job.go), and nothing ever clears it —
		// Finish updates status, end_time, completed, private_plan, nonce and
		// interceptible, and leaves this one alone (db/build.go).
		//
		// That is what makes it the discriminator for a cancelled build. A
		// build the starter refused before admitting it and a build it
		// admitted, planned, failed to start and then finished as aborted are
		// the same row in status, completion and plan; they differ here and
		// nowhere else the operator can see.
		CheckNotMember[SchedulingPass]("the build {string} was never even scheduled",
			"the builds the scheduler admitted",
			func(in SchedulingPass) ([]string, error) { return in.buildsScheduled() },
			func(in SchedulingPass) string { return in.summary() }),

		CheckStringFor[SchedulingPass]("the build {string} will run the task {string}",
			"the task the build's plan names",
			func(in SchedulingPass, name string) (string, error) { return in.plannedTask(name) }),
	}
}

// -----------------------------------------------------------------------
// Triggering
// -----------------------------------------------------------------------

func triggeringDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// One resource, two pipelines differing in one word. That is the
		// discriminator these scenarios exist for, and it is also exactly the
		// line a pipeline author writes or does not write.
		schedulingJobStep(`a job that gets "code" on every new version`, triggerJobConfig(true)),
		schedulingJobStep(`a job that gets "code" but is not triggered by it`, triggerJobConfig(false)),

		brine.DefineMap[SchedulingJob, SchedulingJob](
			"a new version {string} of {string} appears",
			func(in SchedulingJob, p brine.Params, _ *brine.Recorder) (SchedulingJob, error) {
				ref, resource, err := twoParams("a new version {string} of {string} appears", p)
				if err != nil {
					return SchedulingJob{}, err
				}
				return in, in.checkResource(resource, atc.Version{"ref": ref})
			},
		),

		// Two sentences rather than one parameterised count, because the two
		// things they say are different: one is "the pass queued a build" and
		// the other is "the pass queued nothing". A single "{int} builds"
		// reads as "1 builds" and hides which claim is being made.
		CheckThat[SchedulingPass]("the job has exactly one build",
			func(in SchedulingPass) error { return in.buildCountIs(1) }),

		CheckThat[SchedulingPass]("the job has no builds at all",
			func(in SchedulingPass) error { return in.buildCountIs(0) }),

		CheckThat[SchedulingPass]("the build the scheduler queued is running",
			func(in SchedulingPass) error { return in.latestBuildIs(db.BuildStatusStarted) }),

		CheckStringFor[SchedulingPass]("the build the scheduler queued will get {string} at version {string}",
			"the version the build's plan pins",
			func(in SchedulingPass, resource string) (string, error) { return in.plannedVersion(resource) }),

		CheckThat[SchedulingPass]("the job is flagged as having new inputs",
			func(in SchedulingPass) error { return in.hasNewInputs(true) }),

		CheckThat[SchedulingPass]("the job is no longer flagged as having new inputs",
			func(in SchedulingPass) error { return in.hasNewInputs(false) }),
	}
}

// -----------------------------------------------------------------------
// SchedulingJob / SchedulingPass mechanics
// -----------------------------------------------------------------------

func newSchedulingJob(res brine.Resources, config atc.Config) (SchedulingJob, error) {
	database, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return SchedulingJob{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
	}

	scenario := &dbtest.Scenario{}
	if err := database.Builder.WithTeam("scheduling-team")(scenario); err != nil {
		return SchedulingJob{}, fmt.Errorf("create team: %w", err)
	}
	if err := database.Builder.WithPipeline(config)(scenario); err != nil {
		return SchedulingJob{}, fmt.Errorf("save pipeline: %w", err)
	}

	versions := db.NewVersionsDB(database.Conn, 100, gocache.New(time.Minute, time.Minute))
	real := algorithm.New(versions)

	return SchedulingJob{
		DB:         database,
		Scenario:   scenario,
		JobFactory: db.NewJobFactory(database.Conn, database.LockFactory),
		Scheduler: &scheduler.Scheduler{
			Algorithm:    real,
			BuildStarter: scheduler.NewBuildStarter(builds.NewPlanner(atc.NewPlanFactory(0)), real),
		},
		Ctx:     context.Background(),
		JobName: config.Jobs[0].Name,
		builds:  map[string]int{},
		named:   map[int]string{},
	}, nil
}

func schedulingJobStep(pattern string, config atc.Config) brine.StepDefinition {
	return brine.DefineMapUsing[brine.Empty, SchedulingJob](
		pattern,
		[]string{"jetbridge-db"},
		func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (SchedulingJob, error) {
			return newSchedulingJob(res, config)
		},
	)
}

func taskJobConfig(maxInFlight int) atc.Config {
	return atc.Config{
		Jobs: atc.JobConfigs{{
			Name:           "some-job",
			RawMaxInFlight: maxInFlight,
			PlanSequence: []atc.Step{
				{Config: &atc.TaskStep{Name: "some-task", ConfigPath: "some/config/path.yml"}},
			},
		}},
	}
}

func getJobConfig() atc.Config {
	return atc.Config{
		Resources: atc.ResourceConfigs{
			{Name: "some-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "source"}},
		},
		Jobs: atc.JobConfigs{{
			Name:         "some-job",
			PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "some-resource"}}},
		}},
	}
}

func triggerJobConfig(trigger bool) atc.Config {
	return atc.Config{
		Resources: atc.ResourceConfigs{
			{Name: "code", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "source"}},
		},
		Jobs: atc.JobConfigs{{
			Name:         "some-job",
			PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "code", Trigger: trigger}}},
		}},
	}
}

func (s SchedulingJob) name(name string, id int) {
	s.builds[name] = id
	s.named[id] = name
}

func (s SchedulingJob) job() (db.Job, error) {
	job, found, err := s.Scenario.Pipeline.Job(s.JobName)
	if err != nil {
		return nil, fmt.Errorf("find job %q: %w", s.JobName, err)
	}
	if !found {
		return nil, fmt.Errorf("job %q is not in the pipeline", s.JobName)
	}
	return job, nil
}

func (s SchedulingJob) build(name string) (db.Build, error) {
	id, ok := s.builds[name]
	if !ok {
		return nil, fmt.Errorf("no build named %q was queued by this scenario", name)
	}
	build, found, err := s.DB.BuildFactory.Build(id)
	if err != nil {
		return nil, fmt.Errorf("read build %q: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("build %q is no longer in the database", name)
	}
	return build, nil
}

// check runs a check of "some-resource"; checkResource names the resource.
func (s SchedulingJob) check(versions ...atc.Version) error {
	return s.checkResource("some-resource", versions...)
}

func (s SchedulingJob) checkResource(resource string, versions ...atc.Version) error {
	if err := s.DB.Builder.WithResourceVersions(resource, versions...)(s.Scenario); err != nil {
		return fmt.Errorf("check %q: %w", resource, err)
	}
	return nil
}

// schedule runs one pass the way runner.go does: request a schedule, read the
// job back as the scheduler would see it, and run it.
//
// The error is returned rather than kept, so no scenario can pass on a pass
// that never happened — every "still queued" assertion below would otherwise
// hold trivially against a scheduler that fell over on its first statement.
func (s SchedulingJob) schedule() (SchedulingPass, error) {
	job, err := s.job()
	if err != nil {
		return SchedulingPass{}, err
	}
	if err := job.RequestSchedule(); err != nil {
		return SchedulingPass{}, fmt.Errorf("request a schedule: %w", err)
	}

	queued, err := job.GetPendingBuilds()
	if err != nil {
		return SchedulingPass{}, fmt.Errorf("read the queue: %w", err)
	}
	queue := make([]string, 0, len(queued))
	for _, build := range queued {
		if named, ok := s.named[build.ID()]; ok {
			queue = append(queue, named)
			continue
		}
		queue = append(queue, fmt.Sprintf("an unnamed build (id %d)", build.ID()))
	}

	toSchedule, err := s.JobFactory.JobsToScheduleByIDs([]int{job.ID()})
	if err != nil {
		return SchedulingPass{}, fmt.Errorf("read the jobs waiting to be scheduled: %w", err)
	}
	if len(toSchedule) != 1 {
		return SchedulingPass{}, fmt.Errorf(
			"expected this job to be waiting to be scheduled, the scheduler found %d jobs", len(toSchedule))
	}

	needsRetry, err := s.Scheduler.Schedule(s.Ctx, lagertest.NewTestLogger("scheduler"), toSchedule[0])
	if err != nil {
		return SchedulingPass{}, fmt.Errorf("the scheduling pass failed: %w", err)
	}

	return SchedulingPass{Job: s, NeedsRetry: needsRetry, Queue: queue}, nil
}

func (p SchedulingPass) head() (string, error) {
	if len(p.Queue) == 0 {
		return "", fmt.Errorf("the queue was empty when the scheduler ran, so nothing was at its head")
	}
	return p.Queue[0], nil
}

// allBuilds reads every build of the job, newest last, named as the scenario
// named it. A build the scenario never named is shown by its row id: an
// unexplained build is exactly what a reader needs to see.
func (p SchedulingPass) allBuilds() ([]db.Build, error) {
	job, err := p.Job.job()
	if err != nil {
		return nil, err
	}
	rows, err := p.Job.DB.Conn.Query(`SELECT id FROM builds WHERE job_id = $1 ORDER BY id ASC`, job.ID())
	if err != nil {
		return nil, fmt.Errorf("read the job's builds: %w", err)
	}
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read a build id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read the job's builds: %w", err)
	}
	rows.Close()

	all := make([]db.Build, 0, len(ids))
	for _, id := range ids {
		build, found, err := p.Job.DB.BuildFactory.Build(id)
		if err != nil {
			return nil, fmt.Errorf("read build %d: %w", id, err)
		}
		if !found {
			return nil, fmt.Errorf("build %d vanished while it was being read", id)
		}
		all = append(all, build)
	}
	return all, nil
}

func (p SchedulingPass) buildName(build db.Build) string {
	if name, ok := p.Job.named[build.ID()]; ok {
		return name
	}
	return fmt.Sprintf("build %s (id %d)", build.Name(), build.ID())
}

func (p SchedulingPass) buildsWith(status db.BuildStatus) ([]string, error) {
	all, err := p.allBuilds()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, build := range all {
		if build.Status() == status {
			names = append(names, p.buildName(build))
		}
	}
	return names, nil
}

func (p SchedulingPass) buildsWithAPlan() ([]string, error) {
	all, err := p.allBuilds()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, build := range all {
		if build.HasPlan() {
			names = append(names, p.buildName(build))
		}
	}
	return names, nil
}

func (p SchedulingPass) buildsScheduled() ([]string, error) {
	all, err := p.allBuilds()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, build := range all {
		if build.IsScheduled() {
			names = append(names, p.buildName(build))
		}
	}
	return names, nil
}

func (p SchedulingPass) buildCountIs(want int) error {
	all, err := p.allBuilds()
	if err != nil {
		return err
	}
	if len(all) != want {
		return fmt.Errorf("expected the job to have %d builds, it has %d (%s)", want, len(all), p.summary())
	}
	return nil
}

func (p SchedulingPass) summary() string {
	all, err := p.allBuilds()
	if err != nil {
		return "could not read the job's builds: " + err.Error()
	}
	if len(all) == 0 {
		return "the job has no builds at all"
	}
	parts := make([]string, 0, len(all))
	for _, build := range all {
		parts = append(parts, fmt.Sprintf("%s=%s", p.buildName(build), build.Status()))
	}
	return "the job's builds: " + strings.Join(parts, ", ")
}

// latest is the newest build of the job — the one a scenario means by "the
// build the scheduler queued", in a scenario where the scheduler queued it and
// so the feature file has no name for it.
func (p SchedulingPass) latest() (db.Build, error) {
	all, err := p.allBuilds()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("the scheduler queued no build at all")
	}
	return all[len(all)-1], nil
}

func (p SchedulingPass) latestBuildIs(status db.BuildStatus) error {
	build, err := p.latest()
	if err != nil {
		return err
	}
	if build.Status() != status {
		return fmt.Errorf("expected the build the scheduler queued to be %s, it is %s (%s)",
			status, build.Status(), p.summary())
	}
	return nil
}

func (p SchedulingPass) plannedTask(name string) (string, error) {
	build, err := p.Job.build(name)
	if err != nil {
		return "", err
	}
	if !build.HasPlan() {
		return "", fmt.Errorf("build %q holds no plan at all, so it names no task (%s)", name, p.summary())
	}
	var tasks []string
	plan := build.PrivatePlan()
	plan.Each(func(sub *atc.Plan) {
		if sub.Task != nil {
			tasks = append(tasks, sub.Task.Name)
		}
	})
	if len(tasks) != 1 {
		return "", fmt.Errorf("expected build %q to plan exactly one task, it plans %v", name, tasks)
	}
	return tasks[0], nil
}

func (p SchedulingPass) plannedVersion(resource string) (string, error) {
	build, err := p.latest()
	if err != nil {
		return "", err
	}
	if !build.HasPlan() {
		return "", fmt.Errorf("the build the scheduler queued holds no plan, so it pins no version (%s)", p.summary())
	}
	var refs []string
	plan := build.PrivatePlan()
	plan.Each(func(sub *atc.Plan) {
		if sub.Get == nil || sub.Get.Resource != resource {
			return
		}
		if sub.Get.Version == nil {
			refs = append(refs, "<no version>")
			return
		}
		refs = append(refs, (*sub.Get.Version)["ref"])
	})
	if len(refs) != 1 {
		return "", fmt.Errorf("expected the plan to get %q exactly once, it gets it %d times %v", resource, len(refs), refs)
	}
	return refs[0], nil
}

func (p SchedulingPass) hasNewInputs(want bool) error {
	job, err := p.Job.job()
	if err != nil {
		return err
	}
	if _, err := job.Reload(); err != nil {
		return fmt.Errorf("reload the job: %w", err)
	}
	if job.HasNewInputs() != want {
		if want {
			return fmt.Errorf("expected the job to be flagged as having new inputs, it is not")
		}
		return fmt.Errorf("expected the job's new-inputs flag to have been cleared, it is still set")
	}
	return nil
}

// -----------------------------------------------------------------------
// A build that is already running
// -----------------------------------------------------------------------

// RunningBuild is a build the engine is about to run, and the step it will
// run. The step's behaviour is set by the When, so the Given can be shared.
type RunningBuild struct {
	DB    JetbridgeDB
	Build db.Build

	Engine  builds.Runnable
	Step    *scriptedBuildStep
	Release chan bool
	Group   *sync.WaitGroup
	Ctx     context.Context

	// stop releases a step that is blocking, once the engine has stopped
	// caring what it returns.
	stop chan struct{}
}

// FinishedBuild is what the engine left behind.
type FinishedBuild struct {
	Running RunningBuild
}

func runningBuildDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, RunningBuild](
			"a build that is running a step",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (RunningBuild, error) {
				return newRunningBuild(res, false)
			},
		),

		brine.DefineMapUsing[brine.Empty, RunningBuild](
			"a resource check that is running",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (RunningBuild, error) {
				return newRunningBuild(res, true)
			},
		),

		// Cancelling. The build is marked aborted through a second handle, so
		// the notification travels the way it does from the API: through
		// PostgreSQL, to the listener the engine registered before the step
		// began. Nothing here touches the step's context directly.
		brine.DefineMap[RunningBuild, FinishedBuild](
			"the build is cancelled while its step is still running",
			func(in RunningBuild, _ brine.Params, _ *brine.Recorder) (FinishedBuild, error) {
				in.Step.blockUntilInterrupted(in.stop)

				done := in.run()
				if err := in.awaitStep(); err != nil {
					return FinishedBuild{}, err
				}

				handle, found, err := in.DB.BuildFactory.Build(in.Build.ID())
				if err != nil {
					return FinishedBuild{}, fmt.Errorf("open a second handle on the build: %w", err)
				}
				if !found {
					return FinishedBuild{}, fmt.Errorf("the build is no longer in the database")
				}
				if err := handle.MarkAsAborted(); err != nil {
					return FinishedBuild{}, fmt.Errorf("cancel the build: %w", err)
				}

				if err := await(done, 30*time.Second, "the engine to stop running the build"); err != nil {
					return FinishedBuild{}, err
				}
				return FinishedBuild{Running: in}, nil
			},
		),

		// Draining. `release` is what atccmd closes when the web is shutting
		// down; the engine's Run selects on it against the step finishing.
		brine.DefineMap[RunningBuild, FinishedBuild](
			"the web shuts down while the step is still running",
			func(in RunningBuild, _ brine.Params, _ *brine.Recorder) (FinishedBuild, error) {
				in.Step.blockUntilInterrupted(in.stop)

				done := in.run()
				if err := in.awaitStep(); err != nil {
					return FinishedBuild{}, err
				}

				select {
				case in.Release <- true:
				case <-time.After(30 * time.Second):
					return FinishedBuild{}, fmt.Errorf("the engine never noticed the web shutting down")
				}

				if err := await(done, 30*time.Second, "the engine to release the build"); err != nil {
					return FinishedBuild{}, err
				}
				// The engine has returned and no longer reads what the step
				// says, so letting it go changes nothing and leaks nothing.
				close(in.stop)
				return FinishedBuild{Running: in}, nil
			},
		),

		brine.DefineMap[RunningBuild, FinishedBuild](
			"the step fails, saying {string}",
			func(in RunningBuild, p brine.Params, _ *brine.Recorder) (FinishedBuild, error) {
				message, err := paramAt("the step fails, saying {string}", p, 0)
				if err != nil {
					return FinishedBuild{}, err
				}
				in.Step.fail(fmt.Errorf("%s", message))
				return in.runToCompletion()
			},
		),

		// exec.Retriable is what a step returns when the worker running it
		// disappeared — the case the engine deliberately does not finish.
		brine.DefineMap[RunningBuild, FinishedBuild](
			"the step fails because the worker running it went away",
			func(in RunningBuild, _ brine.Params, _ *brine.Recorder) (FinishedBuild, error) {
				in.Step.fail(exec.Retriable{Cause: fmt.Errorf("worker disappeared")})
				return in.runToCompletion()
			},
		),

		brine.DefineMap[RunningBuild, FinishedBuild](
			"the step panics",
			func(in RunningBuild, _ brine.Params, _ *brine.Recorder) (FinishedBuild, error) {
				in.Step.panics("something went wrong")
				return in.runToCompletion()
			},
		),

		CheckString[FinishedBuild]("the build finished as {string}",
			"the build's recorded status",
			func(in FinishedBuild) (string, error) { return in.status() },
			func(in FinishedBuild) string { return in.log() }),

		CheckThat[FinishedBuild]("the build is still recorded as running",
			func(in FinishedBuild) error {
				status, err := in.status()
				if err != nil {
					return err
				}
				if status != string(db.BuildStatusStarted) {
					return fmt.Errorf(
						"expected the build to be left running for the next web to pick up, it is %q", status)
				}
				return nil
			}),

		// The witness that the engine got as far as the step at all.
		//
		// Every other scenario here gets that for free: a build the engine
		// finished, and a log line the step's wrapper wrote, are both proof it
		// ran. The retriable one has neither — its whole claim is that the row
		// is UNCHANGED — and `started` is the state the Given already put the
		// row in, so without this the scenario passes on any of the four early
		// returns in engineBuild.Run (lock not acquired, Reload error, build
		// not found, build not running) as well as on the branch it names.
		CheckThat[FinishedBuild]("the build's step really ran",
			func(in FinishedBuild) error {
				if !in.Running.stepRan() {
					return fmt.Errorf(
						"the engine returned without ever running the build's step, so the build " +
							"being left at \"started\" says nothing about how a retriable failure " +
							"is classified — it is the state the build was already in")
				}
				return nil
			}),

		CheckContains[FinishedBuild]("the finished build's log explains {string}",
			"the build log",
			func(in FinishedBuild) (string, error) { return in.completedLog() }),
	}
}

func newRunningBuild(res brine.Resources, check bool) (RunningBuild, error) {
	database, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return RunningBuild{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
	}

	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "engine-team"})
	if err != nil {
		return RunningBuild{}, fmt.Errorf("create team: %w", err)
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "some-pipeline"},
		atc.Config{
			Jobs: atc.JobConfigs{{Name: "some-job"}},
			Resources: atc.ResourceConfigs{
				{Name: "some-resource", Type: dbtest.BaseResourceType, Source: atc.Source{"some": "source"}},
			},
		},
		0, false,
	)
	if err != nil {
		return RunningBuild{}, fmt.Errorf("save pipeline: %w", err)
	}

	// A LoadVar plan, because it is the one step whose leaf the engine builds
	// with no worker, no resource config and no image: the smallest plan that
	// still travels the whole of Run.
	plan := atc.Plan{
		ID:      "build-plan",
		LoadVar: &atc.LoadVarPlan{Name: "some-var", File: "some-file.yml"},
	}

	var build db.Build
	if check {
		resource, found, err := pipeline.Resource("some-resource")
		if err != nil {
			return RunningBuild{}, fmt.Errorf("find the resource: %w", err)
		}
		if !found {
			return RunningBuild{}, fmt.Errorf("the resource is not in the pipeline")
		}
		build, found, err = resource.CreateBuild(context.Background(), false, plan)
		if err != nil {
			return RunningBuild{}, fmt.Errorf("create the check build: %w", err)
		}
		if !found {
			return RunningBuild{}, fmt.Errorf("a check of the resource was already running")
		}
		if build.Name() != db.CheckBuildName {
			return RunningBuild{}, fmt.Errorf("expected a check build, got a build named %q", build.Name())
		}
	} else {
		job, found, err := pipeline.Job("some-job")
		if err != nil {
			return RunningBuild{}, fmt.Errorf("find the job: %w", err)
		}
		if !found {
			return RunningBuild{}, fmt.Errorf("the job is not in the pipeline")
		}
		build, err = job.CreateBuild("someone")
		if err != nil {
			return RunningBuild{}, fmt.Errorf("create the build: %w", err)
		}
		started, err := build.Start(plan)
		if err != nil {
			return RunningBuild{}, fmt.Errorf("start the build: %w", err)
		}
		if !started {
			return RunningBuild{}, fmt.Errorf("the build did not start")
		}
		if _, err := build.Reload(); err != nil {
			return RunningBuild{}, fmt.Errorf("reload the build: %w", err)
		}
	}

	step := &scriptedBuildStep{started: make(chan struct{})}

	stepperFactory := engine.NewStepperFactory(
		leafStepFactory{step: step},
		"http://example.com",
		db.NewResourceCheckRateLimiter(rate.Inf, 0, time.Minute, nil, time.Minute, clock.NewClock()),
		nil, nil, nil, nil, nil, nil,
	)

	varSourcePool := creds.NewVarSourcePool(
		lagertest.NewTestLogger("var-source-pool"),
		creds.CredentialManagementConfig{},
		time.Minute, time.Minute, clock.NewClock(),
	)

	release := make(chan bool)
	group := new(sync.WaitGroup)

	return RunningBuild{
		DB:    database,
		Build: build,
		Engine: engine.NewBuild(
			build,
			stepperFactory,
			&dummy.Secrets{StaticVariables: vars.StaticVariables{"foo": "bar"}},
			varSourcePool,
			release,
			new(sync.Map),
			group,
		),
		Step:    step,
		Release: release,
		Group:   group,
		Ctx:     context.Background(),
		stop:    make(chan struct{}),
	}, nil
}

// run starts the engine on its own goroutine and reports when it has returned.
func (r RunningBuild) run() chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Engine.Run(lagerctx.NewContext(r.Ctx, lagertest.NewTestLogger("engine")))
	}()
	return done
}

// runToCompletion is for steps that return on their own.
func (r RunningBuild) runToCompletion() (FinishedBuild, error) {
	if err := await(r.run(), 30*time.Second, "the engine to finish the build"); err != nil {
		return FinishedBuild{}, err
	}
	return FinishedBuild{Running: r}, nil
}

// awaitStep waits until the step is actually running, which is the point after
// which the engine has registered its abort listener and is selecting on the
// drain channel.
func (r RunningBuild) awaitStep() error {
	return await(r.Step.started, 30*time.Second, "the build's step to start running")
}

// stepRan reports whether the leaf step ever began. It is only read after the
// engine has returned, at which point a step that has not started never will,
// so the non-blocking read is the whole answer rather than a race.
func (r RunningBuild) stepRan() bool {
	select {
	case <-r.Step.started:
		return true
	default:
		return false
	}
}

func await(ch chan struct{}, timeout time.Duration, what string) error {
	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timed out after %s waiting for %s", timeout, what)
	}
}

func (f FinishedBuild) status() (string, error) {
	build, found, err := f.Running.DB.BuildFactory.Build(f.Running.Build.ID())
	if err != nil {
		return "", fmt.Errorf("read the build back: %w", err)
	}
	if !found {
		return "", fmt.Errorf("the build is no longer in the database")
	}
	return string(build.Status()), nil
}

// completedLog reads what the build log holds. It refuses on a build that is
// still running, because the event source blocks waiting for more events on an
// incomplete build — and a check that hangs is a check that says nothing.
func (f FinishedBuild) completedLog() (string, error) {
	build, found, err := f.Running.DB.BuildFactory.Build(f.Running.Build.ID())
	if err != nil {
		return "", fmt.Errorf("read the build back: %w", err)
	}
	if !found {
		return "", fmt.Errorf("the build is no longer in the database")
	}
	if !build.IsCompleted() {
		return "", fmt.Errorf("the build is still running, so its log is not finished and cannot be read here")
	}
	return buildLogText(build)
}

func (f FinishedBuild) log() string {
	text, err := f.completedLog()
	if err != nil {
		return "the build log could not be read: " + err.Error()
	}
	if text == "" {
		return "the build log is empty"
	}
	return "the build log: " + text
}

// buildLogText renders the build's events the way a reader of the log sees
// them: the error messages and the terminal status, in order.
func buildLogText(build db.Build) (string, error) {
	source, err := build.Events(0)
	if err != nil {
		return "", fmt.Errorf("open the build log: %w", err)
	}
	defer source.Close()

	var lines []string
	for {
		envelope, err := source.Next()
		if err == db.ErrEndOfBuildEventStream {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read the build log: %w", err)
		}
		switch envelope.Event {
		case event.EventTypeError:
			var ev event.Error
			if envelope.Data != nil {
				if err := json.Unmarshal(*envelope.Data, &ev); err != nil {
					return "", fmt.Errorf("decode an error in the build log: %w", err)
				}
			}
			lines = append(lines, "error: "+ev.Message)
		case event.EventTypeStatus:
			var ev event.Status
			if envelope.Data != nil {
				if err := json.Unmarshal(*envelope.Data, &ev); err != nil {
					return "", fmt.Errorf("decode a status in the build log: %w", err)
				}
			}
			lines = append(lines, "status: "+string(ev.Status))
		default:
			lines = append(lines, string(envelope.Event))
		}
	}
	return strings.Join(lines, " | "), nil
}

// -----------------------------------------------------------------------
// The step the engine runs
// -----------------------------------------------------------------------

// scriptedBuildStep is a working leaf step whose behaviour the scenario
// writes. It records nothing and is never asked what it was called with; the
// only thing it reports is that it began, which is how the When knows the
// engine is far enough along to be interrupted.
type scriptedBuildStep struct {
	started chan struct{}
	once    sync.Once
	run     func(context.Context) (bool, error)
}

func (s *scriptedBuildStep) Run(ctx context.Context, _ exec.RunState) (bool, error) {
	s.once.Do(func() { close(s.started) })
	if s.run == nil {
		return true, nil
	}
	return s.run(ctx)
}

// blockUntilInterrupted is the shape of a step that is doing work: it runs
// until its context is cancelled, and reports the cancellation the way a real
// step does. The bounded fall-through is what makes a scenario that loses its
// cancellation FAIL rather than hang — the build finishes successfully
// instead, which is a legible failure.
func (s *scriptedBuildStep) blockUntilInterrupted(stop chan struct{}) {
	s.run = func(ctx context.Context) (bool, error) {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-stop:
			return true, nil
		case <-time.After(10 * time.Second):
			return true, nil
		}
	}
}

func (s *scriptedBuildStep) fail(err error) {
	s.run = func(context.Context) (bool, error) { return false, err }
}

func (s *scriptedBuildStep) panics(message string) {
	s.run = func(context.Context) (bool, error) { panic(message) }
}

// leafStepFactory hands the engine the scenario's step where production hands
// it a real one. LoadVar is wrapped in exec.LogError exactly as
// coreStepFactory.LoadVarStep wraps the real load_var step, so the build log
// carries what it would carry in production; the rest fail loudly, because a
// plan reaching them means the scenario built something it did not intend.
type leafStepFactory struct {
	step exec.Step
}

func (f leafStepFactory) LoadVarStep(_ atc.Plan, _ exec.StepMetadata, delegates engine.DelegateFactory) exec.Step {
	return exec.LogError(f.step, delegates)
}

func (f leafStepFactory) GetStep(atc.Plan, exec.StepMetadata, db.ContainerMetadata, engine.DelegateFactory) exec.Step {
	return unexpectedStep("get")
}

func (f leafStepFactory) PutStep(atc.Plan, exec.StepMetadata, db.ContainerMetadata, engine.DelegateFactory) exec.Step {
	return unexpectedStep("put")
}

func (f leafStepFactory) TaskStep(atc.Plan, exec.StepMetadata, db.ContainerMetadata, engine.DelegateFactory) exec.Step {
	return unexpectedStep("task")
}

func (f leafStepFactory) RunStep(atc.Plan, exec.StepMetadata, db.ContainerMetadata, engine.DelegateFactory) exec.Step {
	return unexpectedStep("run")
}

func (f leafStepFactory) CheckStep(atc.Plan, exec.StepMetadata, db.ContainerMetadata, engine.DelegateFactory) exec.Step {
	return unexpectedStep("check")
}

func (f leafStepFactory) SetPipelineStep(atc.Plan, exec.StepMetadata, engine.DelegateFactory) exec.Step {
	return unexpectedStep("set_pipeline")
}

func (f leafStepFactory) ArtifactInputStep(atc.Plan, db.Build) exec.Step {
	return unexpectedStep("artifact input")
}

func (f leafStepFactory) ArtifactOutputStep(atc.Plan, db.Build) exec.Step {
	return unexpectedStep("artifact output")
}

func unexpectedStep(kind string) exec.Step {
	return refusingStep{kind: kind}
}

type refusingStep struct{ kind string }

func (s refusingStep) Run(context.Context, exec.RunState) (bool, error) {
	return false, fmt.Errorf("this scenario's plan reached a %s step, which it does not describe", s.kind)
}

var _ engine.CoreStepFactory = leafStepFactory{}
