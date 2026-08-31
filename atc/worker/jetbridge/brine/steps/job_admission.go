package steps

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// JobAdmissionDefinitions migrates `JobFactory.JobsToSchedule` — the query the
// scheduler's component runner issues on every tick to find out which jobs in
// the whole cluster it is allowed to look at, and what each of them is carrying
// when it does.
//
// This is the admission policy, and it is the neighbour of
// build_scheduling.go rather than a duplicate of it. That file starts AFTER a
// job has been admitted: it calls `JobsToScheduleByIDs` with an id it already
// has and asks what the scheduler does with the job. This one asks the prior
// question — whether the job is in the set at all, and with which resources,
// resource types and prototypes attached. A job that never appears here is a
// job the scheduler never runs, and no scenario in build-scheduling.feature
// can see that: passing an explicit id and then asserting on the one job that
// came back cannot distinguish "the policy admitted it" from "the policy is
// gone".
//
// THERE IS NO DOUBLE. Every scenario builds real teams, real pipelines and
// real jobs through `Team.SavePipeline` on the scenario's own PostgreSQL, and
// the answer is whatever `jobsToSchedule` returns. The four columns the policy
// is written in — `j.schedule_requested > j.last_scheduled`, `j.active`,
// `j.paused`, `p.paused` — are reached the way an operator reaches them:
// `RequestSchedule`, `UpdateLastScheduled`, `Job.Pause`, `Pipeline.Pause`, and
// re-saving the pipeline without the job.
//
// Two structural decisions worth stating.
//
// Every admission scenario carries a CONTROL job, in its own team and its own
// pipeline, which is always due. Without it, "the scheduler will not schedule
// X" is satisfied by a `jobsToSchedule` that returns nothing at all — which is
// what four of the six mutations below produce if the WHERE clause is broken
// the other way. The control line runs first, so a scheduler that has stopped
// answering reddens on the line that says so rather than on a line that reads
// like a policy claim.
//
// The control also puts a second TEAM in the set on the row where both jobs
// come back, which is the only thing the ginkgo suite's "multiple jobs with
// different times" case asserted beyond what the single-job cases already did.
//
// And every scenario names its jobs, and every assertion is keyed by name
// against the whole returned set. The set is the answer; a positional
// `jobs[0]` claim would be an accident of ordering, and `jobsToSchedule` has
// no ORDER BY.

func JobAdmissionDefinitions() []brine.StepDefinition {
	defs := []brine.StepDefinition{

		// The two ways a job joins the scenario. They differ only in what they
		// take as input — the first opens a scenario, the second adds to one —
		// and both build a whole team and pipeline, because a job that
		// `jobsToSchedule` will look at has to be inside one: the query joins
		// `pipelines p` for `p.paused`, and reads resources, resource types and
		// prototypes through the pipeline id.
		brine.DefineMapUsing[brine.Empty, JobAdmission](
			"a job {string} in its own pipeline, asking to be scheduled",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (JobAdmission, error) {
				name, err := paramAt("a job {string} in its own pipeline, asking to be scheduled", p, 0)
				if err != nil {
					return JobAdmission{}, err
				}
				in, err := newJobAdmission(res)
				if err != nil {
					return JobAdmission{}, err
				}
				return in, in.addSoloJob(name)
			},
		),

		brine.DefineMap[JobAdmission, JobAdmission](
			"another job {string} in its own pipeline, asking to be scheduled",
			func(in JobAdmission, p brine.Params, _ *brine.Recorder) (JobAdmission, error) {
				name, err := paramAt("another job {string} in its own pipeline, asking to be scheduled", p, 0)
				if err != nil {
					return JobAdmission{}, err
				}
				return in, in.addSoloJob(name)
			},
		),

		// Two pipelines that use the same resource NAME for two different
		// things. That is what makes the assertions below about scoping rather
		// than about lookup: a `jobsToSchedule` that resolved resources by name
		// instead of through `job_inputs`/`job_outputs` would hand job-3 the
		// other pipeline's `some-resource`, and the type is what shows it.
		brine.DefineMapUsing[brine.Empty, JobAdmission](
			"two pipelines that give one resource name two different types, with three jobs between them",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (JobAdmission, error) {
				in, err := newJobAdmission(res)
				if err != nil {
					return JobAdmission{}, err
				}
				if err := in.addPipeline("team-one", "pipeline-one", sharedNamePipelineOne()); err != nil {
					return JobAdmission{}, err
				}
				return in, in.addPipeline("team-two", "pipeline-two", sharedNamePipelineTwo())
			},
		),

		// The same shape one level up: custom types belong to a PIPELINE, not
		// to a job, so two jobs in one pipeline must both get all of it and a
		// job in the other must get none of it. Resource types and prototypes
		// are two separate queries in `jobsToSchedule` with two separate
		// pipeline-id memos, so one fixture defines both and the outline over
		// it asserts each in turn.
		brine.DefineMapUsing[brine.Empty, JobAdmission](
			"two pipelines that define their own custom types, with two jobs in the first and one in the second",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (JobAdmission, error) {
				in, err := newJobAdmission(res)
				if err != nil {
					return JobAdmission{}, err
				}
				if err := in.addPipeline("type-team-one", "type-pipeline-one", customTypePipeline(
					[]string{"job-1", "job-2"}, "alpha")); err != nil {
					return JobAdmission{}, err
				}
				return in, in.addPipeline("type-team-two", "type-pipeline-two", customTypePipeline(
					[]string{"job-3"}, "beta", "gamma"))
			},
		),

		// The read. `JobsToSchedule` takes no arguments: it is the whole
		// cluster's answer, which is why the control job is visible to it
		// without being mentioned here.
		brine.DefineMap[JobAdmission, SchedulerRead](
			"the scheduler reads which jobs to schedule",
			func(in JobAdmission, _ brine.Params, _ *brine.Recorder) (SchedulerRead, error) {
				jobs, err := in.JobFactory.JobsToSchedule()
				if err != nil {
					return SchedulerRead{}, fmt.Errorf("read the jobs to schedule: %w", err)
				}
				return SchedulerRead{In: in, Jobs: jobs}, nil
			},
		),

		CheckMember[SchedulerRead]("the scheduler will schedule {string}",
			"the jobs the scheduler will schedule",
			func(in SchedulerRead) ([]string, error) { return in.names(), nil },
			func(in SchedulerRead) string { return in.summary() }),

		CheckNotMember[SchedulerRead]("the scheduler will not schedule {string}",
			"the jobs the scheduler will schedule",
			func(in SchedulerRead) ([]string, error) { return in.names(), nil },
			func(in SchedulerRead) string { return in.summary() }),

		// The three payload checks. Each renders what the job is CARRYING as
		// one sorted line, so a single comparison covers presence, absence and
		// the fields the scheduler cannot do without — a resource it cannot
		// name the type of is a resource it cannot check.
		CheckStringFor[SchedulerRead]("the resources handed to {string} are {string}",
			"the resources handed to the job",
			func(in SchedulerRead, name string) (string, error) { return in.resources(name) },
			func(in SchedulerRead) string { return in.summary() }),

		CheckStringFor[SchedulerRead]("the resource types handed to {string} are {string}",
			"the resource types handed to the job",
			func(in SchedulerRead, name string) (string, error) { return in.resourceTypes(name) },
			func(in SchedulerRead) string { return in.summary() }),

		CheckStringFor[SchedulerRead]("the prototypes handed to {string} are {string}",
			"the prototypes handed to the job",
			func(in SchedulerRead, name string) (string, error) { return in.prototypes(name) },
			func(in SchedulerRead) string { return in.summary() }),
	}

	for _, change := range admissionChanges {
		defs = append(defs, admissionChangeStep(change.phrase, change.apply))
	}
	for _, shape := range resourceShapes {
		defs = append(defs, resourceShapeStep(shape.phrase, shape.plan))
	}
	return defs
}

// -----------------------------------------------------------------------
// The six things that can happen to a job before the scheduler looks
// -----------------------------------------------------------------------

// admissionChanges is the Examples column of the admission outline, executable.
//
// Each one moves exactly one of the four columns the policy is written in, and
// each one CONFIRMS it moved. That guard is not ceremony: five of these six
// scenarios assert that a job is absent from a set, and an absent job is also
// what you get when the change silently did not happen. `Pause` on a job whose
// row was replaced under it, or a `SavePipeline` that was refused for a stale
// config version, would leave a scenario passing for the wrong reason and
// nothing on the page would say so.
var admissionChanges = []struct {
	phrase string
	apply  func(JobAdmission, *admittedJob) error
}{
	// The due job. A no-op with a name, so the row that says "nothing was
	// done to it" is on the page beside the five that did something —
	// otherwise a reader of the Examples table has to infer the control case
	// from an empty cell.
	{"is left alone", func(_ JobAdmission, _ *admittedJob) error { return nil }},

	// schedule_requested < last_scheduled: the scheduler has already been
	// round since the job asked.
	{"was scheduled after it asked", func(in JobAdmission, j *admittedJob) error {
		if err := j.Job.UpdateLastScheduled(time.Now().Add(time.Minute)); err != nil {
			return fmt.Errorf("record a later scheduling pass: %w", err)
		}
		requested, scheduled, err := in.scheduleTimes(j)
		if err != nil {
			return err
		}
		if !scheduled.After(requested) {
			return fmt.Errorf("expected the last pass (%s) to be after the request (%s)", scheduled, requested)
		}
		return nil
	}},

	// schedule_requested == last_scheduled: the boundary. The policy is a
	// strict `>`, and a `>=` would re-schedule every job on every tick for as
	// long as nothing else asked — the job never leaves the set, because the
	// pass that would have advanced last_scheduled sets it to exactly the
	// value it is being compared against.
	{"was scheduled at the very moment it asked", func(in JobAdmission, j *admittedJob) error {
		if _, err := j.Job.Reload(); err != nil {
			return fmt.Errorf("reload the job: %w", err)
		}
		if err := j.Job.UpdateLastScheduled(j.Job.ScheduleRequestedTime()); err != nil {
			return fmt.Errorf("record a scheduling pass at the requested time: %w", err)
		}
		requested, scheduled, err := in.scheduleTimes(j)
		if err != nil {
			return err
		}
		if !scheduled.Equal(requested) {
			return fmt.Errorf("expected the last pass (%s) to equal the request (%s)", scheduled, requested)
		}
		return nil
	}},

	{"has been paused", func(_ JobAdmission, j *admittedJob) error {
		if err := j.Job.Pause(""); err != nil {
			return fmt.Errorf("pause the job: %w", err)
		}
		if _, err := j.Job.Reload(); err != nil {
			return fmt.Errorf("reload the job: %w", err)
		}
		if !j.Job.Paused() {
			return fmt.Errorf("the job did not come back paused")
		}
		return nil
	}},

	// Inactive. The row survives a `fly set-pipeline` that removed the job —
	// its builds and history are still there — and `active` is the only thing
	// that says it is no longer part of the pipeline.
	{"is dropped from its pipeline's configuration", func(in JobAdmission, j *admittedJob) error {
		_, _, err := j.Team.SavePipeline(
			atc.PipelineRef{Name: j.Pipeline.Name()},
			atc.Config{},
			j.Pipeline.ConfigVersion(),
			false,
		)
		if err != nil {
			return fmt.Errorf("re-save the pipeline without the job: %w", err)
		}
		active, err := in.jobIsActive(j)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("the job row is still active after the pipeline was saved without it")
		}
		return nil
	}},

	{"has its pipeline paused", func(_ JobAdmission, j *admittedJob) error {
		if err := j.Pipeline.Pause(""); err != nil {
			return fmt.Errorf("pause the pipeline: %w", err)
		}
		if _, err := j.Pipeline.Reload(); err != nil {
			return fmt.Errorf("reload the pipeline: %w", err)
		}
		if !j.Pipeline.Paused() {
			return fmt.Errorf("the pipeline did not come back paused")
		}
		return nil
	}},
}

func admissionChangeStep(phrase string, apply func(JobAdmission, *admittedJob) error) brine.StepDefinition {
	pattern := "the job {string} " + phrase
	return brine.DefineMap[JobAdmission, JobAdmission](
		pattern,
		func(in JobAdmission, p brine.Params, _ *brine.Recorder) (JobAdmission, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return JobAdmission{}, err
			}
			job, err := in.named(name)
			if err != nil {
				return JobAdmission{}, err
			}
			return in, apply(in, job)
		},
	)
}

// -----------------------------------------------------------------------
// The four plan shapes a job can name a resource in
// -----------------------------------------------------------------------

// resourceShapes is the Examples column of the resources outline. The
// pipeline is the same every time and always holds an unused resource beside
// the used one; only the job's plan changes. `jobsToSchedule` reads
// `job_inputs UNION job_outputs`, so a get and a put are two different tables
// and naming the resource in both must still yield it once.
var resourceShapes = []struct {
	phrase string
	plan   []atc.Step
}{
	{"names no resources", nil},
	{"gets the resource", []atc.Step{{Config: &atc.GetStep{Name: usedResource}}}},
	{"puts the resource", []atc.Step{{Config: &atc.PutStep{Name: usedResource}}}},
	{"gets and puts the resource", []atc.Step{
		{Config: &atc.GetStep{Name: usedResource}},
		{Config: &atc.PutStep{Name: usedResource}},
	}},
}

func resourceShapeStep(phrase string, plan []atc.Step) brine.StepDefinition {
	pattern := "a job {string} whose plan " + phrase + ", in a pipeline with a used and an unused resource"
	return brine.DefineMapUsing[brine.Empty, JobAdmission](
		pattern,
		[]string{"jetbridge-db"},
		func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (JobAdmission, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return JobAdmission{}, err
			}
			in, err := newJobAdmission(res)
			if err != nil {
				return JobAdmission{}, err
			}
			return in, in.addPipeline("resource-team", "resource-pipeline", atc.Config{
				Resources: atc.ResourceConfigs{
					{Name: usedResource, Type: "some-type", Source: atc.Source{"some": "source"}},
					{Name: "unused-resource", Type: "some-type", Source: atc.Source{"other": "source"}},
				},
				Jobs: atc.JobConfigs{{Name: name, PlanSequence: plan}},
			})
		},
	)
}

const usedResource = "some-resource"

// -----------------------------------------------------------------------
// Fixtures
// -----------------------------------------------------------------------

func sharedNamePipelineOne() atc.Config {
	return atc.Config{
		Resources: atc.ResourceConfigs{
			{Name: "some-resource", Type: "some-type"},
			{Name: "other-resource", Type: "some-type"},
			{Name: "unused-resource", Type: "some-type"},
		},
		Jobs: atc.JobConfigs{
			{Name: "job-1", PlanSequence: []atc.Step{
				{Config: &atc.GetStep{Name: "some-resource"}},
			}},
			{Name: "job-2", PlanSequence: []atc.Step{
				{Config: &atc.GetStep{Name: "some-resource"}},
				{Config: &atc.GetStep{Name: "other-resource"}},
			}},
		},
	}
}

func sharedNamePipelineTwo() atc.Config {
	return atc.Config{
		Resources: atc.ResourceConfigs{
			{Name: "some-resource", Type: "other-type"},
			{Name: "some-resource-2", Type: "other-type"},
		},
		Jobs: atc.JobConfigs{
			{Name: "job-3", PlanSequence: []atc.Step{
				{Config: &atc.GetStep{Name: "some-resource"}},
				{Config: &atc.GetStep{Name: "some-resource-2"}},
			}},
		},
	}
}

// customTypePipeline defines the named custom types as BOTH resource types and
// prototypes, so one fixture serves both rows of the outline over it.
func customTypePipeline(jobs []string, types ...string) atc.Config {
	config := atc.Config{}
	for _, name := range types {
		config.ResourceTypes = append(config.ResourceTypes, atc.ResourceType{Name: name, Type: "base-type"})
		config.Prototypes = append(config.Prototypes, atc.Prototype{Name: name, Type: "base-type"})
	}
	for _, name := range jobs {
		config.Jobs = append(config.Jobs, atc.JobConfig{Name: name})
	}
	return config
}

// -----------------------------------------------------------------------
// JobAdmission / SchedulerRead mechanics
// -----------------------------------------------------------------------

// JobAdmission is the cluster as a scenario built it: teams, pipelines and the
// jobs in them, every one of which has asked to be scheduled.
type JobAdmission struct {
	DB         JetbridgeDB
	JobFactory db.JobFactory

	// jobs maps the scenario's name for a job to the row, its pipeline and its
	// team — all three, because the changes a scenario makes reach the policy
	// through different objects: `Job.Pause`, `Pipeline.Pause` and
	// `Team.SavePipeline`.
	jobs map[string]*admittedJob
}

type admittedJob struct {
	Team     db.Team
	Pipeline db.Pipeline
	Job      db.Job
}

// SchedulerRead is one call to JobsToSchedule and the cluster it read.
type SchedulerRead struct {
	In   JobAdmission
	Jobs db.SchedulerJobs
}

func newJobAdmission(res brine.Resources) (JobAdmission, error) {
	database, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return JobAdmission{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
	}
	return JobAdmission{
		DB:         database,
		JobFactory: db.NewJobFactory(database.Conn, database.LockFactory),
		jobs:       map[string]*admittedJob{},
	}, nil
}

// addSoloJob gives one job a team and a pipeline of its own, which is what
// lets a scenario pause one job's pipeline without touching the other's.
func (a JobAdmission) addSoloJob(name string) error {
	return a.addPipeline(name+"-team", name+"-pipeline", atc.Config{
		Jobs: atc.JobConfigs{{Name: name}},
	})
}

// addPipeline saves a whole pipeline and requests a schedule for every job in
// it, so `schedule_requested > last_scheduled` holds for all of them before
// any scenario-specific change is made. A job that never asked is invisible to
// the policy for a reason that has nothing to do with what a scenario is
// about, and would make five of the six admission rows pass on their own.
func (a JobAdmission) addPipeline(teamName, pipelineName string, config atc.Config) error {
	team, err := a.DB.TeamFactory.CreateTeam(atc.Team{Name: teamName})
	if err != nil {
		return fmt.Errorf("create team %q: %w", teamName, err)
	}
	pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: pipelineName}, config, db.ConfigVersion(0), false)
	if err != nil {
		return fmt.Errorf("save pipeline %q: %w", pipelineName, err)
	}
	for _, jobConfig := range config.Jobs {
		job, found, err := pipeline.Job(jobConfig.Name)
		if err != nil {
			return fmt.Errorf("find job %q: %w", jobConfig.Name, err)
		}
		if !found {
			return fmt.Errorf("job %q is not in pipeline %q after saving it", jobConfig.Name, pipelineName)
		}
		if err := job.RequestSchedule(); err != nil {
			return fmt.Errorf("request a schedule for %q: %w", jobConfig.Name, err)
		}
		if _, taken := a.jobs[jobConfig.Name]; taken {
			return fmt.Errorf("two jobs in this scenario are called %q, so no assertion could tell them apart", jobConfig.Name)
		}
		a.jobs[jobConfig.Name] = &admittedJob{Team: team, Pipeline: pipeline, Job: job}
	}
	return nil
}

func (a JobAdmission) named(name string) (*admittedJob, error) {
	job, ok := a.jobs[name]
	if !ok {
		return nil, fmt.Errorf("no job named %q was built by this scenario", name)
	}
	return job, nil
}

func (a JobAdmission) scheduleTimes(j *admittedJob) (requested, scheduled time.Time, err error) {
	row := a.DB.Conn.QueryRow(
		`SELECT schedule_requested, last_scheduled FROM jobs WHERE id = $1`, j.Job.ID())
	if err := row.Scan(&requested, &scheduled); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("read the job's scheduling times: %w", err)
	}
	return requested, scheduled, nil
}

func (a JobAdmission) jobIsActive(j *admittedJob) (bool, error) {
	var active bool
	if err := a.DB.Conn.QueryRow(`SELECT active FROM jobs WHERE id = $1`, j.Job.ID()).Scan(&active); err != nil {
		return false, fmt.Errorf("read whether the job is still active: %w", err)
	}
	return active, nil
}

func (r SchedulerRead) names() []string {
	names := make([]string, 0, len(r.Jobs))
	for _, job := range r.Jobs {
		names = append(names, job.Name())
	}
	sort.Strings(names)
	return names
}

func (r SchedulerRead) find(name string) (db.SchedulerJob, error) {
	for _, job := range r.Jobs {
		if job.Name() == name {
			return job, nil
		}
	}
	return db.SchedulerJob{}, fmt.Errorf(
		"the scheduler did not come back with a job called %q, so there is nothing it was handed; it came back with %v",
		name, r.names())
}

func (r SchedulerRead) resources(name string) (string, error) {
	return handedTo(r, name, func(job db.SchedulerJob) []string {
		return describeAll(job.Resources, describeResource)
	})
}

func (r SchedulerRead) resourceTypes(name string) (string, error) {
	return handedTo(r, name, func(job db.SchedulerJob) []string {
		return describeAll(job.ResourceTypes, func(t atc.ResourceType) string {
			return fmt.Sprintf("%s (%s)", t.Name, t.Type)
		})
	})
}

func (r SchedulerRead) prototypes(name string) (string, error) {
	return handedTo(r, name, func(job db.SchedulerJob) []string {
		return describeAll(job.Prototypes, func(p atc.Prototype) string {
			return fmt.Sprintf("%s (%s)", p.Name, p.Type)
		})
	})
}

// handedTo finds the job and renders one of the three collections it is
// carrying. A job the scheduler did not come back with is reported as that,
// rather than as an empty collection: "the scheduler handed it nothing" and
// "the scheduler never mentioned it" are different answers, and a check that
// conflated them would go green on a job that was never admitted.
func handedTo(r SchedulerRead, name string, render func(db.SchedulerJob) []string) (string, error) {
	job, err := r.find(name)
	if err != nil {
		return "", err
	}
	return joinOrNothing(render(job)), nil
}

func describeAll[T any](items []T, describe func(T) string) []string {
	rendered := make([]string, 0, len(items))
	for _, item := range items {
		rendered = append(rendered, describe(item))
	}
	return rendered
}

// summary is the detail every failure in this family carries: the whole answer,
// job by job, with what each one is holding. A wrong set and a right set with
// the wrong payload look identical from the failing line alone.
func (r SchedulerRead) summary() string {
	if len(r.Jobs) == 0 {
		return "the scheduler came back with no jobs at all"
	}
	parts := make([]string, 0, len(r.Jobs))
	for _, job := range r.Jobs {
		resources, _ := r.resources(job.Name())
		types, _ := r.resourceTypes(job.Name())
		prototypes, _ := r.prototypes(job.Name())
		parts = append(parts, fmt.Sprintf("%s{resources: %s; types: %s; prototypes: %s}",
			job.Name(), resources, types, prototypes))
	}
	sort.Strings(parts)
	return "the scheduler came back with " + strings.Join(parts, ", ")
}

// describeResource renders a SchedulerResource as the scheduler needs it: the
// name it is known by, the type that says how to check it, and the source that
// says where. A resource carried without its type or its source is one the
// scheduler cannot do anything with, so all three are in the one line the
// scenario compares rather than in three assertions a reader has to hold
// together.
func describeResource(resource db.SchedulerResource) string {
	if len(resource.Source) == 0 {
		return fmt.Sprintf("%s (%s)", resource.Name, resource.Type)
	}
	keys := make([]string, 0, len(resource.Source))
	for key := range resource.Source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s:%v", key, resource.Source[key]))
	}
	return fmt.Sprintf("%s (%s, %s)", resource.Name, resource.Type, strings.Join(pairs, " "))
}

// joinOrNothing sorts and joins a rendered collection, and says "nothing" for
// an empty one.
//
// The word is the honest reading of the empty case and it keeps the Examples
// table on one vocabulary: the alternative is an empty cell, which reads as an
// omission rather than as a claim. Sorting is because none of the three
// queries behind these has an ORDER BY that the scenario could rely on for
// resources, and a scenario that depended on insertion order would be
// asserting the plan's shape rather than the job's inputs.
func joinOrNothing(rendered []string) string {
	if len(rendered) == 0 {
		return "nothing"
	}
	sort.Strings(rendered)
	return strings.Join(rendered, ", ")
}
