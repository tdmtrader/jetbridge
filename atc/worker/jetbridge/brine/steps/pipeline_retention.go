package steps

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
)

// PipelineRetentionDefinitions backs ../features/pipeline-retention.feature:
// the three policies in atc/db that decide what a deployment keeps and what it
// lets through.
//
// There is no double here, and — unlike every earlier file in this programme —
// there was none to remove either. atc/db already runs on real PostgreSQL with
// real delegates, so nothing is bought by de-faking; the only justification for
// a scenario in this file is that its sentence is one an operator or a pipeline
// author would recognise. Where a Go test was pinning a query shape rather than
// a policy it was left where it is.
//
// Three things are worth knowing about the fixtures below.
//
// The pauser scenarios BACKDATE p.last_updated on every pipeline that is meant
// to be a candidate. That is not decoration. pipeline_pauser.go has two
// independent guards — "nothing ran recently" and "the config has not been
// touched recently" — and a pipeline saved during the scenario fails the second
// one outright, so a fixture that skips the backdate exercises the last_updated
// guard and nothing else. Three of the seven ginkgo cases are in exactly that
// position; see the feature file.
//
// The admission scenarios RE-FETCH the job before every ScheduleBuild.
// isPipelineOrJobPaused reads j.paused from the in-memory job and the pipeline's
// paused flag from the database, so a job handle obtained before a pause would
// answer with the state it was born with and the scenario would pass on a stale
// field rather than on the policy.
//
// The cache scenarios date the two builds ±100 seconds around the prune rather
// than using the prune instant itself. invalid_since is written by a database
// trigger at Postgres's now(); the offset is what keeps the scenario about the
// policy instead of about clock skew between two clocks on one machine.
func PipelineRetentionDefinitions() []brine.StepDefinition {
	defs := pipelinePauserDefinitions()
	defs = append(defs, buildAdmissionDefinitions()...)
	defs = append(defs, cacheSurvivalDefinitions()...)
	return defs
}

// -----------------------------------------------------------------------
// The automatic pipeline pauser
// -----------------------------------------------------------------------

// PauserReady is the pauser and the pipelines it will consider.
type PauserReady struct {
	DB     JetbridgeDB
	Team   db.Team
	Pauser db.PipelinePauser
	Ctx    context.Context
}

// PauserRan is a completed pass. Whether it failed is what a scenario goes on
// to assert, so the error is carried rather than raised.
type PauserRan struct {
	Ready PauserReady
	Err   error
}

// pauserJobs is the shape every pipeline in these scenarios starts with: two
// jobs, neither of which has run. A scenario gives builds only to the jobs it
// names, so "a job that has never run" is the absence of a step rather than a
// step that does nothing — and it is load-bearing, because a job with no builds
// at all is the case that separates "nothing ran recently" from "nothing ran".
var pauserJobs = atc.Config{
	Jobs: atc.JobConfigs{
		{Name: "unit"},
		{Name: "integration"},
	},
}

func pipelinePauserDefinitions() []brine.StepDefinition {
	const (
		setUpDaysAgo  = `the pipeline {string} was set up {int} days ago with the jobs "unit" and "integration"`
		setUpJustNow  = `the pipeline {string} was set up just now with the jobs "unit" and "integration"`
		jobFinished   = "the job {string} in the pipeline {string} finished a build {int} days ago"
		jobRunning    = "the job {string} in the pipeline {string} has a build running right now"
		pauserRunsFor = "the pauser pauses every pipeline idle for {int} days"
	)

	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, PauserReady](
			"the automatic pipeline pauser",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (PauserReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return PauserReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "retention-team"})
				if err != nil {
					return PauserReady{}, fmt.Errorf("create team: %w", err)
				}
				return PauserReady{
					DB:     database,
					Team:   team,
					Pauser: db.NewPipelinePauser(database.Conn, database.LockFactory),
					Ctx: lagerctx.NewContext(context.Background(),
						lagertest.NewTestLogger("pipeline-pauser")),
				}, nil
			},
		),

		// The backdate is the whole reason this step takes a number. See the
		// header: without it the last_updated guard alone decides the scenario.
		brine.DefineMap[PauserReady, PauserReady](setUpDaysAgo,
			func(in PauserReady, p brine.Params, _ *brine.Recorder) (PauserReady, error) {
				name, err := paramAt(setUpDaysAgo, p, 0)
				if err != nil {
					return PauserReady{}, err
				}
				days, err := intAt(setUpDaysAgo, p, 1)
				if err != nil {
					return PauserReady{}, err
				}
				pipeline, err := in.savePipeline(name)
				if err != nil {
					return PauserReady{}, err
				}
				_, err = in.DB.Conn.Exec(
					`UPDATE pipelines SET last_updated = NOW() - make_interval(days => $1) WHERE id = $2`,
					days, pipeline.ID())
				if err != nil {
					return PauserReady{}, fmt.Errorf("backdate the configuration of %q: %w", name, err)
				}
				return in, nil
			},
		),

		brine.DefineMap[PauserReady, PauserReady](setUpJustNow,
			func(in PauserReady, p brine.Params, _ *brine.Recorder) (PauserReady, error) {
				name, err := paramAt(setUpJustNow, p, 0)
				if err != nil {
					return PauserReady{}, err
				}
				_, err = in.savePipeline(name)
				return in, err
			},
		),

		// A finished build sets the job's latest_completed_build_id and clears
		// its next_build_id, which is the pair the pauser's subquery reads.
		brine.DefineMap[PauserReady, PauserReady](jobFinished,
			func(in PauserReady, p brine.Params, _ *brine.Recorder) (PauserReady, error) {
				jobName, pipelineName, err := twoParams(jobFinished, p)
				if err != nil {
					return PauserReady{}, err
				}
				days, err := intAt(jobFinished, p, 2)
				if err != nil {
					return PauserReady{}, err
				}
				job, err := in.job(pipelineName, jobName)
				if err != nil {
					return PauserReady{}, err
				}
				build, err := job.CreateBuild("retention-scenario")
				if err != nil {
					return PauserReady{}, fmt.Errorf("create a build of %q: %w", jobName, err)
				}
				if err := build.Finish(db.BuildStatusSucceeded); err != nil {
					return PauserReady{}, fmt.Errorf("finish the build of %q: %w", jobName, err)
				}
				_, err = in.DB.Conn.Exec(
					`UPDATE builds SET end_time = NOW() - make_interval(days => $1) WHERE id = $2`,
					days, build.ID())
				if err != nil {
					return PauserReady{}, fmt.Errorf("backdate the build of %q: %w", jobName, err)
				}
				return in, nil
			},
		),

		// A build that was created and never finished leaves next_build_id set,
		// which is the other arm of the subquery: a pipeline with work in
		// flight is not idle however old its last finished build is.
		brine.DefineMap[PauserReady, PauserReady](jobRunning,
			func(in PauserReady, p brine.Params, _ *brine.Recorder) (PauserReady, error) {
				jobName, pipelineName, err := twoParams(jobRunning, p)
				if err != nil {
					return PauserReady{}, err
				}
				job, err := in.job(pipelineName, jobName)
				if err != nil {
					return PauserReady{}, err
				}
				if _, err := job.CreateBuild("retention-scenario"); err != nil {
					return PauserReady{}, fmt.Errorf("create a running build of %q: %w", jobName, err)
				}
				return in, nil
			},
		),

		brine.DefineMap[PauserReady, PauserRan](pauserRunsFor,
			func(in PauserReady, p brine.Params, _ *brine.Recorder) (PauserRan, error) {
				days, err := intAt(pauserRunsFor, p, 0)
				if err != nil {
					return PauserRan{}, err
				}
				return PauserRan{Ready: in, Err: in.Pauser.PausePipelines(in.Ctx, days)}, nil
			},
		),

		CheckThat[PauserRan]("the pauser completed without error",
			func(in PauserRan) error {
				if in.Err != nil {
					return fmt.Errorf("the pauser failed: %v", in.Err)
				}
				return nil
			}),

		// Both of these look the pipeline up again rather than reading a handle
		// the fixture kept, and both refuse a name no pipeline has. "Is not
		// paused" is otherwise true of a pipeline that was never created, and a
		// scenario asserting that would be asserting nothing.
		checkPaused("the pipeline {string} is paused", true),
		checkPaused("the pipeline {string} is not paused", false),

		CheckStringFor[PauserRan]("the pipeline {string} was paused by {string}",
			"the user recorded against the pause",
			func(in PauserRan, name string) (string, error) {
				pipeline, err := in.pipeline(name)
				if err != nil {
					return "", err
				}
				if !pipeline.Paused() {
					return "", fmt.Errorf("the pipeline %q is not paused, so nobody is recorded as having paused it", name)
				}
				return pipeline.PausedBy(), nil
			}),
	}
}

// checkPaused backs both directions of the pause assertion. The failure lists
// every pipeline the pass left paused, because a surprise about one is nearly
// always diagnosed by the others beside it.
func checkPaused(pattern string, want bool) brine.StepDefinition {
	return brine.DefineCheck[PauserRan](pattern,
		func(in PauserRan, p brine.Params, _ *brine.Recorder) error {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return err
			}
			pipeline, err := in.pipeline(name)
			if err != nil {
				return err
			}
			if pipeline.Paused() == want {
				return nil
			}
			paused, listErr := in.pausedNames()
			if listErr != nil {
				return listErr
			}
			if want {
				return fmt.Errorf("expected the pipeline %q to be paused; the pauser left these paused: %v", name, paused)
			}
			return fmt.Errorf("expected the pipeline %q to be left alone, but the pauser paused it; it paused these: %v", name, paused)
		},
	)
}

func (r PauserReady) savePipeline(name string) (db.Pipeline, error) {
	pipeline, found, err := r.Team.Pipeline(atc.PipelineRef{Name: name})
	if err != nil {
		return nil, fmt.Errorf("look for the pipeline %q: %w", name, err)
	}
	var from db.ConfigVersion
	if found {
		from = pipeline.ConfigVersion()
	}
	saved, _, err := r.Team.SavePipeline(atc.PipelineRef{Name: name}, pauserJobs, from, false)
	if err != nil {
		return nil, fmt.Errorf("save the pipeline %q: %w", name, err)
	}
	return saved, nil
}

func (r PauserReady) job(pipelineName, jobName string) (db.Job, error) {
	pipeline, found, err := r.Team.Pipeline(atc.PipelineRef{Name: pipelineName})
	if err != nil {
		return nil, fmt.Errorf("look for the pipeline %q: %w", pipelineName, err)
	}
	if !found {
		return nil, fmt.Errorf("no pipeline named %q was set up by this scenario", pipelineName)
	}
	job, found, err := pipeline.Job(jobName)
	if err != nil {
		return nil, fmt.Errorf("look for the job %q: %w", jobName, err)
	}
	if !found {
		return nil, fmt.Errorf("the pipeline %q has no job named %q", pipelineName, jobName)
	}
	return job, nil
}

func (r PauserRan) pipeline(name string) (db.Pipeline, error) {
	pipeline, found, err := r.Ready.Team.Pipeline(atc.PipelineRef{Name: name})
	if err != nil {
		return nil, fmt.Errorf("read the pipeline %q back: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("no pipeline named %q exists — this scenario never created it, "+
			"so it would be asserting nothing about the pauser", name)
	}
	return pipeline, nil
}

func (r PauserRan) pausedNames() ([]string, error) {
	rows, err := r.Ready.DB.Conn.Query(`SELECT name FROM pipelines WHERE paused ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read which pipelines the pass left paused: %w", err)
	}
	defer rows.Close()

	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read a pipeline name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// -----------------------------------------------------------------------
// Admission: max-in-flight, serial groups, and pauses
// -----------------------------------------------------------------------

// AdmissionReady is one pipeline, the jobs configured so far, and the builds a
// scenario has created. Jobs accumulate because a job's limit and its serial
// group are properties of the pipeline CONFIG, so each job step re-saves it.
type AdmissionReady struct {
	DB   JetbridgeDB
	Team db.Team

	jobs   []atc.JobConfig
	builds map[string]db.Build
	owner  map[string]string
}

// admissionAnswer is one ScheduleBuild call: whether the build was let through,
// and whether the call itself failed.
type admissionAnswer struct {
	admitted bool
	err      error
}

// AdmissionAsked carries every answer in the order it was given, keyed by the
// scenario's name for the build. A scenario that asks twice records twice, and
// the sentences below say which ask they mean.
type AdmissionAsked struct {
	Ready   AdmissionReady
	Answers map[string][]admissionAnswer
}

const admissionPipeline = "some-pipeline"

func buildAdmissionDefinitions() []brine.StepDefinition {
	const (
		jobWithLimit  = "the job {string} allows {int} builds at a time"
		jobsNoLimit   = "the jobs {string} and {string} have no limit of their own"
		jobsSerial    = "the jobs {string} and {string} share the serial group {string}"
		jobPaused     = "the job {string} is paused"
		buildRunning  = "the build {string} of {string} is running"
		buildWaiting  = "the build {string} of {string} is waiting"
		askThenFinish = "the scheduler asks whether {string} may start, then {string} finishes and it asks again"
		askThenPause  = "the scheduler asks whether {string} may start, then the job {string} is paused and it asks again"
		askThenHalt   = "the scheduler asks whether {string} and {string} may start, then the pipeline is paused and it asks about {string}"
	)

	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, AdmissionReady](
			"a scheduler deciding which builds may start",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (AdmissionReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return AdmissionReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "admission-team"})
				if err != nil {
					return AdmissionReady{}, fmt.Errorf("create team: %w", err)
				}
				return AdmissionReady{
					DB:     database,
					Team:   team,
					builds: map[string]db.Build{},
					owner:  map[string]string{},
				}, nil
			},
		),

		brine.DefineMap[AdmissionReady, AdmissionReady](jobWithLimit,
			func(in AdmissionReady, p brine.Params, _ *brine.Recorder) (AdmissionReady, error) {
				name, err := paramAt(jobWithLimit, p, 0)
				if err != nil {
					return AdmissionReady{}, err
				}
				limit, err := intAt(jobWithLimit, p, 1)
				if err != nil {
					return AdmissionReady{}, err
				}
				in.jobs = append(in.jobs, atc.JobConfig{Name: name, RawMaxInFlight: limit})
				return in, in.saveJobs()
			},
		),

		brine.DefineMap[AdmissionReady, AdmissionReady](jobsNoLimit,
			func(in AdmissionReady, p brine.Params, _ *brine.Recorder) (AdmissionReady, error) {
				first, second, err := twoParams(jobsNoLimit, p)
				if err != nil {
					return AdmissionReady{}, err
				}
				in.jobs = append(in.jobs, atc.JobConfig{Name: first}, atc.JobConfig{Name: second})
				return in, in.saveJobs()
			},
		),

		// A job with serial groups has a max-in-flight of one whatever else its
		// config says (atc.JobConfig.MaxInFlight), so the limit these two jobs
		// share is a single slot.
		brine.DefineMap[AdmissionReady, AdmissionReady](jobsSerial,
			func(in AdmissionReady, p brine.Params, _ *brine.Recorder) (AdmissionReady, error) {
				first, second, err := twoParams(jobsSerial, p)
				if err != nil {
					return AdmissionReady{}, err
				}
				group, err := paramAt(jobsSerial, p, 2)
				if err != nil {
					return AdmissionReady{}, err
				}
				in.jobs = append(in.jobs,
					atc.JobConfig{Name: first, SerialGroups: []string{group}},
					atc.JobConfig{Name: second, SerialGroups: []string{group}},
				)
				return in, in.saveJobs()
			},
		),

		brine.DefineMap[AdmissionReady, AdmissionReady](jobPaused,
			func(in AdmissionReady, p brine.Params, _ *brine.Recorder) (AdmissionReady, error) {
				name, err := paramAt(jobPaused, p, 0)
				if err != nil {
					return AdmissionReady{}, err
				}
				job, err := in.job(name)
				if err != nil {
					return AdmissionReady{}, err
				}
				return in, job.Pause("an-operator")
			},
		),

		// Running means scheduled AND started: getRunningBuildsBySerialGroup
		// counts `b.scheduled AND NOT b.completed`, so a build that was created
		// and never admitted occupies no slot.
		brine.DefineMap[AdmissionReady, AdmissionReady](buildRunning,
			func(in AdmissionReady, p brine.Params, _ *brine.Recorder) (AdmissionReady, error) {
				buildName, jobName, err := twoParams(buildRunning, p)
				if err != nil {
					return AdmissionReady{}, err
				}
				build, err := in.createBuild(buildName, jobName)
				if err != nil {
					return AdmissionReady{}, err
				}
				job, err := in.job(jobName)
				if err != nil {
					return AdmissionReady{}, err
				}
				admitted, err := job.ScheduleBuild(build)
				if err != nil {
					return AdmissionReady{}, fmt.Errorf("schedule the running build %q: %w", buildName, err)
				}
				if !admitted {
					return AdmissionReady{}, fmt.Errorf(
						"the fixture could not get the build %q running: the scheduler turned it away", buildName)
				}
				started, err := build.Start(atc.Plan{})
				if err != nil {
					return AdmissionReady{}, fmt.Errorf("start the build %q: %w", buildName, err)
				}
				if !started {
					return AdmissionReady{}, fmt.Errorf("the build %q did not start", buildName)
				}
				return in, nil
			},
		),

		brine.DefineMap[AdmissionReady, AdmissionReady](buildWaiting,
			func(in AdmissionReady, p brine.Params, _ *brine.Recorder) (AdmissionReady, error) {
				buildName, jobName, err := twoParams(buildWaiting, p)
				if err != nil {
					return AdmissionReady{}, err
				}
				_, err = in.createBuild(buildName, jobName)
				return in, err
			},
		),

		brine.DefineMap[AdmissionReady, AdmissionAsked](askThenFinish,
			func(in AdmissionReady, p brine.Params, _ *brine.Recorder) (AdmissionAsked, error) {
				asking, finishing, err := twoParams(askThenFinish, p)
				if err != nil {
					return AdmissionAsked{}, err
				}
				asked := AdmissionAsked{Ready: in, Answers: map[string][]admissionAnswer{}}
				asked.ask(asking)
				done, ok := in.builds[finishing]
				if !ok {
					return AdmissionAsked{}, fmt.Errorf("no build named %q was created by this scenario", finishing)
				}
				if err := done.Finish(db.BuildStatusSucceeded); err != nil {
					return AdmissionAsked{}, fmt.Errorf("finish the build %q: %w", finishing, err)
				}
				asked.ask(asking)
				return asked, nil
			},
		),

		brine.DefineMap[AdmissionReady, AdmissionAsked](askThenPause,
			func(in AdmissionReady, p brine.Params, _ *brine.Recorder) (AdmissionAsked, error) {
				asking, pausing, err := twoParams(askThenPause, p)
				if err != nil {
					return AdmissionAsked{}, err
				}
				asked := AdmissionAsked{Ready: in, Answers: map[string][]admissionAnswer{}}
				asked.ask(asking)
				job, err := in.job(pausing)
				if err != nil {
					return AdmissionAsked{}, err
				}
				if err := job.Pause("an-operator"); err != nil {
					return AdmissionAsked{}, fmt.Errorf("pause the job %q: %w", pausing, err)
				}
				asked.ask(asking)
				return asked, nil
			},
		),

		brine.DefineMap[AdmissionReady, AdmissionAsked](askThenHalt,
			func(in AdmissionReady, p brine.Params, _ *brine.Recorder) (AdmissionAsked, error) {
				first, second, err := twoParams(askThenHalt, p)
				if err != nil {
					return AdmissionAsked{}, err
				}
				third, err := paramAt(askThenHalt, p, 2)
				if err != nil {
					return AdmissionAsked{}, err
				}
				asked := AdmissionAsked{Ready: in, Answers: map[string][]admissionAnswer{}}
				asked.ask(first)
				asked.ask(second)

				pipeline, found, err := in.Team.Pipeline(atc.PipelineRef{Name: admissionPipeline})
				if err != nil {
					return AdmissionAsked{}, fmt.Errorf("look for the pipeline: %w", err)
				}
				if !found {
					return AdmissionAsked{}, fmt.Errorf("this scenario configured no pipeline to pause")
				}
				if err := pipeline.Pause("an-operator"); err != nil {
					return AdmissionAsked{}, fmt.Errorf("pause the pipeline: %w", err)
				}
				asked.ask(third)
				return asked, nil
			},
		),

		checkAdmission("the build {string} was turned away", false, onlyAsk),
		checkAdmission("the build {string} was let through", true, onlyAsk),
		checkAdmission("the build {string} was turned away the first time it asked", false, firstAsk),
		checkAdmission("the build {string} was let through the second time it asked", true, secondAsk),

		// The answer and the row are two facts. A scheduler that returned true
		// without writing `scheduled` would leave the build sitting in the
		// pending queue forever while telling its caller it had started.
		checkScheduledRow("the build {string} is marked scheduled", true),
		checkScheduledRow("the build {string} is not marked scheduled", false),
	}
}

// which ask a sentence is talking about.
type whichAsk int

const (
	onlyAsk whichAsk = iota
	firstAsk
	secondAsk
)

func (w whichAsk) describe() string {
	switch w {
	case firstAsk:
		return "the first time it asked"
	case secondAsk:
		return "the second time it asked"
	default:
		return "when it asked"
	}
}

func (w whichAsk) pick(answers []admissionAnswer) (admissionAnswer, error) {
	switch w {
	case onlyAsk:
		if len(answers) != 1 {
			return admissionAnswer{}, fmt.Errorf(
				"this sentence is about a build that was asked about once, but it was asked about %d times", len(answers))
		}
		return answers[0], nil
	case firstAsk:
		if len(answers) < 1 {
			return admissionAnswer{}, fmt.Errorf("nothing ever asked about this build")
		}
		return answers[0], nil
	default:
		if len(answers) < 2 {
			return admissionAnswer{}, fmt.Errorf(
				"this sentence is about a second ask, but the build was asked about %d times", len(answers))
		}
		return answers[1], nil
	}
}

func checkAdmission(pattern string, want bool, which whichAsk) brine.StepDefinition {
	return brine.DefineCheck[AdmissionAsked](pattern,
		func(in AdmissionAsked, p brine.Params, _ *brine.Recorder) error {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return err
			}
			answers, ok := in.Answers[name]
			if !ok {
				return fmt.Errorf("nothing asked the scheduler about a build named %q, "+
					"so there is no answer to check", name)
			}
			answer, err := which.pick(answers)
			if err != nil {
				return fmt.Errorf("the build %q: %w", name, err)
			}
			if answer.err != nil {
				return fmt.Errorf("asking about the build %q failed: %v", name, answer.err)
			}
			if answer.admitted == want {
				return nil
			}
			if want {
				return fmt.Errorf("expected the build %q to be let through %s, but the scheduler turned it away",
					name, which.describe())
			}
			return fmt.Errorf("expected the build %q to be turned away %s, but the scheduler let it through",
				name, which.describe())
		},
	)
}

func checkScheduledRow(pattern string, want bool) brine.StepDefinition {
	return brine.DefineCheck[AdmissionAsked](pattern,
		func(in AdmissionAsked, p brine.Params, _ *brine.Recorder) error {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return err
			}
			build, ok := in.Ready.builds[name]
			if !ok {
				return fmt.Errorf("no build named %q was created by this scenario", name)
			}
			var scheduled bool
			err = in.Ready.DB.Conn.QueryRow(
				`SELECT scheduled FROM builds WHERE id = $1`, build.ID()).Scan(&scheduled)
			if err != nil {
				return fmt.Errorf("read the row for the build %q: %w", name, err)
			}
			if scheduled == want {
				return nil
			}
			if want {
				return fmt.Errorf("expected the build %q to be marked scheduled in the database, but it is not", name)
			}
			return fmt.Errorf("expected the build %q not to be marked scheduled, but it is", name)
		},
	)
}

// ask re-fetches both the job and the build before calling ScheduleBuild. See
// the header: a job handle carries j.paused from the moment it was read, and a
// Build that already knows it is scheduled short-circuits the whole decision.
func (a *AdmissionAsked) ask(name string) {
	build, ok := a.Ready.builds[name]
	if !ok {
		a.Answers[name] = append(a.Answers[name], admissionAnswer{
			err: fmt.Errorf("no build named %q was created by this scenario", name),
		})
		return
	}
	job, err := a.Ready.job(a.Ready.owner[name])
	if err != nil {
		a.Answers[name] = append(a.Answers[name], admissionAnswer{err: err})
		return
	}
	if _, err := build.Reload(); err != nil {
		a.Answers[name] = append(a.Answers[name], admissionAnswer{
			err: fmt.Errorf("reload the build %q: %w", name, err),
		})
		return
	}
	admitted, err := job.ScheduleBuild(build)
	a.Answers[name] = append(a.Answers[name], admissionAnswer{admitted: admitted, err: err})
}

func (r AdmissionReady) saveJobs() error {
	pipeline, found, err := r.Team.Pipeline(atc.PipelineRef{Name: admissionPipeline})
	if err != nil {
		return fmt.Errorf("look for the pipeline: %w", err)
	}
	var from db.ConfigVersion
	if found {
		from = pipeline.ConfigVersion()
	}
	_, _, err = r.Team.SavePipeline(
		atc.PipelineRef{Name: admissionPipeline},
		atc.Config{Jobs: r.jobs},
		from, false)
	if err != nil {
		return fmt.Errorf("save the pipeline: %w", err)
	}
	return nil
}

func (r AdmissionReady) job(name string) (db.Job, error) {
	if name == "" {
		return nil, fmt.Errorf("no job was recorded for this build")
	}
	pipeline, found, err := r.Team.Pipeline(atc.PipelineRef{Name: admissionPipeline})
	if err != nil {
		return nil, fmt.Errorf("look for the pipeline: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("this scenario configured no pipeline")
	}
	job, found, err := pipeline.Job(name)
	if err != nil {
		return nil, fmt.Errorf("look for the job %q: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("no job named %q is configured", name)
	}
	return job, nil
}

// createBuild also declares the job's inputs determined. getNextPendingBuild-
// BySerialGroup only considers builds of jobs whose inputs are determined, so a
// fixture that skipped this would have every scheduler answer be "no" for a
// reason no scenario is about.
func (r AdmissionReady) createBuild(buildName, jobName string) (db.Build, error) {
	job, err := r.job(jobName)
	if err != nil {
		return nil, err
	}
	if err := job.SaveNextInputMapping(nil, true); err != nil {
		return nil, fmt.Errorf("determine the inputs of %q: %w", jobName, err)
	}
	build, err := job.CreateBuild("admission-scenario")
	if err != nil {
		return nil, fmt.Errorf("create the build %q: %w", buildName, err)
	}
	r.builds[buildName] = build
	r.owner[buildName] = jobName
	return build, nil
}

// -----------------------------------------------------------------------
// A resource cache that outlives the worker it came from
// -----------------------------------------------------------------------

// CacheReady is a resource cache, the workers that hold it, and the moment its
// origin worker was pruned.
type CacheReady struct {
	DB   JetbridgeDB
	Team db.Team

	Cache   db.ResourceCache
	Workers map[string]db.Worker
	Volumes map[string]db.CreatedVolume

	source   *db.UsedWorkerResourceCache
	origin   string
	prunedAt time.Time

	// The worker_resource_caches row the STREAMED copy was registered under,
	// and the worker it sits on. "The volume is still there" is about this
	// row as much as about the volume: a prune that left the volume behind
	// but repointed or nulled its worker_resource_cache_id has still thrown
	// the cache away, and a check that only asks whether the handle resolves
	// cannot tell the two apart.
	streamed   *db.UsedWorkerResourceCache
	streamedOn string
}

// CacheLookedUp is what two builds were offered: one that started before the
// prune and one that started after it.
type CacheLookedUp struct {
	Ready CacheReady

	EarlierWorkers []string
	LaterWorkers   []string

	// worker name -> the handle FindResourceCacheVolume returned for it, or
	// "" when it found nothing. The handle rather than a boolean because the
	// sentence is "finds the cache on worker-2", and a lookup that returned
	// some other volume sitting on worker-2 answers found=true to a boolean
	// while being exactly the drift the sentence is meant to catch.
	EarlierVolume map[string]string
	LaterVolume   map[string]string
}

func cacheSurvivalDefinitions() []brine.StepDefinition {
	const (
		produced = "the resource cache {string} produced on the worker {string}"
		streamed = "the cache streamed to the worker {string}"
		pruned   = "the worker {string} is pruned"
		stalled  = "the worker {string} has stopped heartbeating and stalled"
		lookedUp = "a build that started before the prune and a build that started after both look for a worker holding the cache"
		lookNow  = "a build looks for a worker holding the cache"
	)

	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, CacheReady](
			produced,
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (CacheReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return CacheReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				cacheName, workerName, err := twoParams(produced, p)
				if err != nil {
					return CacheReady{}, err
				}

				team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "cache-team"})
				if err != nil {
					return CacheReady{}, fmt.Errorf("create team: %w", err)
				}

				ready := CacheReady{
					DB:      database,
					Team:    team,
					Workers: map[string]db.Worker{},
					Volumes: map[string]db.CreatedVolume{},
					origin:  workerName,
				}
				if _, err := ready.registerWorker(workerName, db.WorkerStateRunning); err != nil {
					return CacheReady{}, err
				}

				build, err := team.CreateOneOffBuild()
				if err != nil {
					return CacheReady{}, fmt.Errorf("create the build that owns the cache: %w", err)
				}
				cache, err := database.Builder.ResourceCacheFactory.FindOrCreateResourceCache(
					db.ForBuild(build.ID()),
					dbtest.BaseResourceType,
					atc.Version{"tag": cacheName},
					atc.Source{"repository": cacheName},
					atc.Params{},
					nil,
				)
				if err != nil {
					return CacheReady{}, fmt.Errorf("create the resource cache %q: %w", cacheName, err)
				}
				ready.Cache = cache

				volume, err := ready.createVolume(workerName)
				if err != nil {
					return CacheReady{}, err
				}
				uwrc, err := volume.InitializeResourceCache(cache)
				if err != nil {
					return CacheReady{}, fmt.Errorf("initialize the cache on %q: %w", workerName, err)
				}
				if uwrc == nil {
					return CacheReady{}, fmt.Errorf("the cache was not initialized on %q", workerName)
				}
				ready.source = uwrc
				return ready, nil
			},
		),

		// The streamed copy keeps the ORIGIN worker's base resource type id.
		// That is the link the prune breaks, and it is why a cache on a worker
		// that is perfectly healthy can stop being usable.
		brine.DefineMap[CacheReady, CacheReady](streamed,
			func(in CacheReady, p brine.Params, _ *brine.Recorder) (CacheReady, error) {
				workerName, err := paramAt(streamed, p, 0)
				if err != nil {
					return CacheReady{}, err
				}
				if _, err := in.registerWorker(workerName, db.WorkerStateRunning); err != nil {
					return CacheReady{}, err
				}
				volume, err := in.createVolume(workerName)
				if err != nil {
					return CacheReady{}, err
				}
				uwrc, err := volume.InitializeStreamedResourceCache(in.Cache, in.source.ID)
				if err != nil {
					return CacheReady{}, fmt.Errorf("initialize the streamed cache on %q: %w", workerName, err)
				}
				if uwrc == nil {
					return CacheReady{}, fmt.Errorf(
						"the streamed copy on %q was not registered as a cache", workerName)
				}
				in.streamed = uwrc
				in.streamedOn = workerName
				return in, nil
			},
		),

		brine.DefineMap[CacheReady, CacheReady](pruned,
			func(in CacheReady, p brine.Params, _ *brine.Recorder) (CacheReady, error) {
				workerName, err := paramAt(pruned, p, 0)
				if err != nil {
					return CacheReady{}, err
				}
				worker, ok := in.Workers[workerName]
				if !ok {
					return CacheReady{}, fmt.Errorf("no worker named %q was registered by this scenario", workerName)
				}
				if err := worker.Delete(); err != nil {
					return CacheReady{}, fmt.Errorf("prune the worker %q: %w", workerName, err)
				}
				in.prunedAt = time.Now()
				return in, nil
			},
		),

		brine.DefineMap[CacheReady, CacheReady](stalled,
			func(in CacheReady, p brine.Params, _ *brine.Recorder) (CacheReady, error) {
				workerName, err := paramAt(stalled, p, 0)
				if err != nil {
					return CacheReady{}, err
				}
				if _, ok := in.Workers[workerName]; !ok {
					return CacheReady{}, fmt.Errorf("no worker named %q was registered by this scenario", workerName)
				}
				// Re-saving with the same resource types keeps the worker's
				// base resource type rows and their ids, so nothing here
				// invalidates the cache: the only thing that changes is state.
				if _, err := in.registerWorker(workerName, db.WorkerStateStalled); err != nil {
					return CacheReady{}, err
				}
				return in, nil
			},
		),

		brine.DefineMap[CacheReady, CacheLookedUp](lookedUp,
			func(in CacheReady, _ brine.Params, _ *brine.Recorder) (CacheLookedUp, error) {
				if in.prunedAt.IsZero() {
					return CacheLookedUp{}, fmt.Errorf(
						"this sentence dates two builds around a prune, but nothing was pruned")
				}
				return in.lookUp(in.prunedAt.Add(-100*time.Second), in.prunedAt.Add(100*time.Second))
			},
		),

		// No prune, so both builds are dated now and the two answers are the
		// same question asked twice; the scenarios using this one assert only
		// the first.
		brine.DefineMap[CacheReady, CacheLookedUp](lookNow,
			func(in CacheReady, _ brine.Params, _ *brine.Recorder) (CacheLookedUp, error) {
				now := time.Now()
				return in.lookUp(now, now)
			},
		),

		CheckMember[CacheLookedUp]("a build that started before the prune is offered the worker {string}",
			"the workers offered to a build that started before the prune",
			func(in CacheLookedUp) ([]string, error) { return in.EarlierWorkers, nil }),

		CheckThat[CacheLookedUp]("a build that started after the prune is offered no worker at all",
			func(in CacheLookedUp) error {
				if len(in.LaterWorkers) != 0 {
					return fmt.Errorf("expected a build that started after the prune to be offered nothing, "+
						"but it was offered %v", in.LaterWorkers)
				}
				return nil
			}),

		CheckMember[CacheLookedUp]("the build is offered the worker {string}",
			"the workers offered to the build",
			func(in CacheLookedUp) ([]string, error) { return in.EarlierWorkers, nil }),

		CheckNotMember[CacheLookedUp]("the build is not offered the worker {string}",
			"the workers offered to the build",
			func(in CacheLookedUp) ([]string, error) { return in.EarlierWorkers, nil }),

		checkCacheVolume("a build that started before the prune finds the cache on the worker {string}", true, true),
		checkCacheVolume("a build that started after the prune finds no cache on the worker {string}", false, false),

		// The row and the rule are separate facts. Without this, "finds no
		// cache" would also be satisfied by a prune that deleted the streamed
		// worker's volume outright — which is a different, and much worse,
		// behaviour than declining to use it.
		//
		// "Still there" is two assertions, because there are two ways to throw
		// the copy away. The handle still has to resolve, AND the row still has
		// to point at the worker_resource_caches row the streamed copy was
		// registered under — team_test.go asserts the second half
		// (Expect(v.WorkerResourceCacheID()).To(Equal(uwrc2.ID))) and it is the
		// half that separates "the rule declined to use this volume" from "the
		// prune nulled its cache pointer, and the bytes are now unreachable
		// garbage waiting for the collector".
		brine.DefineCheck[CacheLookedUp](
			"the volume holding the cache on {string} is still there",
			func(in CacheLookedUp, p brine.Params, _ *brine.Recorder) error {
				workerName, err := paramAt("the volume holding the cache on {string} is still there", p, 0)
				if err != nil {
					return err
				}
				volume, ok := in.Ready.Volumes[workerName]
				if !ok {
					return fmt.Errorf("this scenario put no volume on the worker %q", workerName)
				}
				if in.Ready.streamed == nil || in.Ready.streamedOn != workerName {
					return fmt.Errorf("this scenario streamed no cache onto the worker %q, "+
						"so there is no cache pointer to check", workerName)
				}
				reread, found, err := in.Ready.DB.VolumeRepository.FindVolume(volume.Handle())
				if err != nil {
					return fmt.Errorf("look for the volume on %q: %w", workerName, err)
				}
				if !found {
					return fmt.Errorf("the volume holding the cache on %q is gone", workerName)
				}
				if reread.WorkerResourceCacheID() != in.Ready.streamed.ID {
					return fmt.Errorf("the volume on %q is still present but no longer holds the cache: "+
						"its worker_resource_cache_id is %d, and the streamed copy was registered under %d",
						workerName, reread.WorkerResourceCacheID(), in.Ready.streamed.ID)
				}
				return nil
			},
		),
	}
}

// checkCacheVolume backs the two sentences about what a get step's own lookup
// answers. It is a different production function from the worker list —
// volume_repository.FindResourceCacheVolume rather than team.FindWorkersFor-
// ResourceCache — implementing the same rule, which is why both are asserted.
//
// The positive direction compares the HANDLE against the volume this scenario
// streamed onto that worker, which is what team_test.go's "cached volume should
// found on worker2" does (Expect(volume.Handle()).To(Equal(volumeOnWorker2.
// Handle()))). A lookup that returned some other volume sitting on the same
// worker satisfies "found" and is exactly the drift the sentence is about.
func checkCacheVolume(pattern string, earlier, want bool) brine.StepDefinition {
	return brine.DefineCheck[CacheLookedUp](pattern,
		func(in CacheLookedUp, p brine.Params, _ *brine.Recorder) error {
			workerName, err := paramAt(pattern, p, 0)
			if err != nil {
				return err
			}
			seen := in.LaterVolume
			when := "after"
			if earlier {
				seen = in.EarlierVolume
				when = "before"
			}
			handle, ok := seen[workerName]
			if !ok {
				return fmt.Errorf("no lookup was made for the worker %q", workerName)
			}
			if !want {
				if handle == "" {
					return nil
				}
				return fmt.Errorf("expected a build that started %s the prune to find no cache on %q, "+
					"but it was given the volume %q", when, workerName, handle)
			}
			if handle == "" {
				return fmt.Errorf("expected a build that started %s the prune to find the cache on %q, but it did not",
					when, workerName)
			}
			held, ok := in.Ready.Volumes[workerName]
			if !ok {
				return fmt.Errorf("this scenario put no volume on the worker %q", workerName)
			}
			if handle != held.Handle() {
				return fmt.Errorf("expected a build that started %s the prune to be given the cache volume %q "+
					"on %q, but the lookup returned the volume %q",
					when, held.Handle(), workerName, handle)
			}
			return nil
		},
	)
}

// lookUp asks both questions production asks — which workers may serve the
// cache, and whether a given worker's copy may be used — for two build start
// times.
func (c CacheReady) lookUp(earlier, later time.Time) (CacheLookedUp, error) {
	out := CacheLookedUp{
		Ready:         c,
		EarlierVolume: map[string]string{},
		LaterVolume:   map[string]string{},
	}

	for _, pair := range []struct {
		at    time.Time
		names *[]string
		vols  map[string]string
	}{
		{earlier, &out.EarlierWorkers, out.EarlierVolume},
		{later, &out.LaterWorkers, out.LaterVolume},
	} {
		workers, err := c.Team.FindWorkersForResourceCache(c.Cache.ID(), pair.at)
		if err != nil {
			return CacheLookedUp{}, fmt.Errorf("look for workers holding the cache: %w", err)
		}
		names := []string{}
		for _, w := range workers {
			names = append(names, w.Name())
		}
		*pair.names = names

		for workerName := range c.Volumes {
			volume, found, err := c.DB.VolumeRepository.FindResourceCacheVolume(workerName, c.Cache, pair.at)
			if err != nil {
				return CacheLookedUp{}, fmt.Errorf("look for the cache volume on %q: %w", workerName, err)
			}
			if !found {
				pair.vols[workerName] = ""
				continue
			}
			if volume == nil {
				return CacheLookedUp{}, fmt.Errorf(
					"the cache lookup on %q reported found but returned no volume", workerName)
			}
			pair.vols[workerName] = volume.Handle()
		}
	}

	return out, nil
}

func (c CacheReady) registerWorker(name string, state db.WorkerState) (db.Worker, error) {
	atcWorker := dbtest.BaseWorker(name)
	atcWorker.State = string(state)
	worker, err := c.DB.WorkerFactory.SaveWorker(atcWorker, 0)
	if err != nil {
		return nil, fmt.Errorf("register the worker %q: %w", name, err)
	}
	c.Workers[name] = worker
	return worker, nil
}

func (c CacheReady) createVolume(workerName string) (db.CreatedVolume, error) {
	creating, err := c.DB.VolumeRepository.CreateVolume(c.Team.ID(), workerName, db.VolumeTypeResource)
	if err != nil {
		return nil, fmt.Errorf("create a volume on %q: %w", workerName, err)
	}
	created, err := creating.Created()
	if err != nil {
		return nil, fmt.Errorf("mark the volume on %q created: %w", workerName, err)
	}
	c.Volumes[workerName] = created
	return created, nil
}
