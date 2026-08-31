package steps

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
)

// GCPipelineDefinitions migrates atc/gc/pipeline_collector_test.go and
// atc/gc/build_log_collector_test.go, and adds the atc/gc build collector,
// which has no test file of its own.
//
// Nothing here is a double. The three collectors run against the scenario's
// own PostgreSQL through the same db.PipelineFactory, db.PipelineLifecycle and
// db.BuildFactory the ATC wires in production, and every assertion reads a
// table: `pipelines.archived`, the rows left in `pipeline_build_events_<id>`,
// `jobs.first_logged_build_id`, `builds.interceptible`.
//
// Most of those readings are taken after the sweep, and for three of the four
// columns that is enough, because nothing else in the scenario writes them.
// `builds.interceptible` is the exception — db.Build.Finish writes it too —
// so it is read before the sweep as well, and the pair of readings rather than
// the later one is what attributes the change to the collector.
//
// On the two ways a failure is injected, which are deliberately different.
//
// The two failures that ABORT a sweep are asserted by their message — the
// scenario says the sweep "was refused, saying closed" — so the error has to
// be the database's own. Those use a connection that has been closed, exactly
// as gc_reclamation.go does, and for the same reason: a collector that
// returned an error of its own invention would pass an assertion written
// against a sentinel the test supplied.
//
// The four failures that are SURVIVED are asserted by their outcome — which
// logs are still there, where the cursor is, and whether the pipeline beside
// the broken one was still reaped. Nothing looks at the error at all, because
// the collector swallows it. Those cannot be produced by a closed connection,
// because the call that has to fail is the fourth or fifth one and a closed
// connection fails the first; so they are produced by wrapping the real,
// PostgreSQL-backed row in a type that fails exactly one method. That wrapper
// records nothing and answers nothing — it is a fault, not a recording double,
// and the ginkgo source it comes from uses the same technique for the same
// reason.

// -----------------------------------------------------------------------
// State
// -----------------------------------------------------------------------

// PipelineGCReady is the pipeline collector and the pipelines it will consider.
type PipelineGCReady struct {
	DB        JetbridgeDB
	Collector interface{ Run(context.Context) error }
	Ctx       context.Context
	Team      db.Team

	pipelines map[string]db.Pipeline
}

// PipelineGCSwept is a completed archiving pass.
type PipelineGCSwept struct {
	Ready PipelineGCReady
	Err   error
}

// LogReady is a build log collector that has not run yet, together with the
// pipelines, jobs and builds a scenario has described for it. The collector
// itself is not built until the sweep, because the retention policy, the
// drainer and the fault are all things a Given may still change.
type LogReady struct {
	DB   JetbridgeDB
	Ctx  context.Context
	Team db.Team

	spec    retentionSpec
	drainer bool
	fault   logFault

	jobs    []*jobUnderTest
	current *jobUnderTest
	builds  map[string]*loggedBuild
}

// LogSwept is a completed build log collector pass.
type LogSwept struct {
	Ready LogReady
	Err   error
}

// InterceptReady is the build collector and the builds it will consider.
type InterceptReady struct {
	DB        JetbridgeDB
	Collector interface{ Run(context.Context) error }
	Ctx       context.Context
	Job       db.Job

	builds map[string]int
}

// InterceptSwept is a completed build collector pass.
type InterceptSwept struct {
	Ready InterceptReady
	Err   error
}

// jobUnderTest is one pipeline and its single job. A scenario usually has one;
// the scenarios that need a healthy neighbour to prove the sweep ran have two.
type jobUnderTest struct {
	pipeline db.Pipeline
	job      db.Job
	builds   []string
}

// loggedBuild is enough to find a build's events again: the events live in a
// table named after the pipeline, not the job.
type loggedBuild struct {
	id         int
	pipelineID int
}

// retentionSpec is the job's declared policy plus the operator's ceiling. The
// two are separate because the whole content of one scenario is that they can
// disagree and the ceiling wins.
type retentionSpec struct {
	builds       int
	days         int
	minSucceeded int
	legacyBuilds int
	maxBuilds    uint64
}

type logFault int

const (
	faultNone logFault = iota
	faultCleanupConnClosed
	faultListingConnClosed
	faultJobsUnreadable
	faultBuildsUnreadable
	faultDeleteRefused
	faultCursorRefused
)

// errLogFault is what a wrapped row returns. No scenario asserts on it: the
// four faults it backs are all swallowed by the collector, and what the
// scenarios assert is what the sweep did afterwards.
var errLogFault = errors.New("the database refused")

// logBatchSize is the batch the ginkgo suite's collector used, kept because the
// boundary it draws is the whole subject of one scenario in the feature file.
//
// The claim that stood here before was wrong, and measurably so. It said this
// number is smaller than any fixture below is long, so the `for page != nil`
// loop in reapLogsOfJob is always exercised. It is not: getBuildsWithPagination
// (atc/db/build_factory.go) only reports a Newer page when a build exists past
// the newest one it just returned, so at Limit 5 a job with 5 builds gets its
// whole history on the first page, Newer comes back nil, and the loop body runs
// exactly once. Every fixture here is 1 to 5 builds except the one written to
// cross this line, which has seven and reads two pages.
const logBatchSize = 5

func GCPipelineDefinitions() []brine.StepDefinition {
	defs := pipelineCollectorDefinitions()
	defs = append(defs, buildLogCollectorDefinitions()...)
	defs = append(defs, buildCollectorDefinitions()...)
	return defs
}

// -----------------------------------------------------------------------
// The pipeline collector
// -----------------------------------------------------------------------

func pipelineCollectorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, PipelineGCReady](
			"a collector for pipelines whose parent has gone away",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (PipelineGCReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return PipelineGCReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "gc-pipelines-team"})
				if err != nil {
					return PipelineGCReady{}, fmt.Errorf("create team: %w", err)
				}
				return PipelineGCReady{
					DB:        database,
					Collector: gc.NewPipelineCollector(db.NewPipelineLifecycle(database.Conn, database.LockFactory)),
					Ctx:       gcContext("pipeline-collector"),
					Team:      team,
					pipelines: map[string]db.Pipeline{},
				}, nil
			},
		),

		// A pipeline a team saved itself has no parent job, which is the
		// predicate that keeps ordinary pipelines out of the archiver's
		// reach entirely.
		brine.DefineMap[PipelineGCReady, PipelineGCReady](
			"the pipeline {string} was set directly by its team",
			func(in PipelineGCReady, p brine.Params, _ *brine.Recorder) (PipelineGCReady, error) {
				name, err := paramAt("the pipeline {string} was set directly by its team", p, 0)
				if err != nil {
					return PipelineGCReady{}, err
				}
				pipeline, _, err := in.Team.SavePipeline(
					atc.PipelineRef{Name: name},
					atc.Config{Jobs: atc.JobConfigs{{Name: "parent-job"}}},
					db.ConfigVersion(0), false,
				)
				if err != nil {
					return PipelineGCReady{}, fmt.Errorf("save pipeline %q: %w", name, err)
				}
				in.pipelines[name] = pipeline
				return in, nil
			},
		),

		// A child pipeline: one set by a build of a job in another pipeline,
		// which is what writes parent_job_id and parent_build_id. The build is
		// finished so that the child is the CURRENT output of its parent job —
		// an unfinished one would leave latest_completed_build_id behind
		// parent_build_id and make the child abandoned for a second reason.
		brine.DefineMap[PipelineGCReady, PipelineGCReady](
			"the pipeline {string} was set by a build of the pipeline {string}",
			func(in PipelineGCReady, p brine.Params, _ *brine.Recorder) (PipelineGCReady, error) {
				pattern := "the pipeline {string} was set by a build of the pipeline {string}"
				child, parentName, err := twoParams(pattern, p)
				if err != nil {
					return PipelineGCReady{}, err
				}
				parent, ok := in.pipelines[parentName]
				if !ok {
					return PipelineGCReady{}, fmt.Errorf("no pipeline named %q was created by this scenario", parentName)
				}
				job, found, err := parent.Job("parent-job")
				if err != nil {
					return PipelineGCReady{}, fmt.Errorf("find the job of %q: %w", parentName, err)
				}
				if !found {
					return PipelineGCReady{}, fmt.Errorf("pipeline %q has no job to set a child from", parentName)
				}
				build, err := job.CreateBuild("gc-pipelines")
				if err != nil {
					return PipelineGCReady{}, fmt.Errorf("create a build of %q: %w", parentName, err)
				}
				pipeline, _, err := build.SavePipeline(
					atc.PipelineRef{Name: child}, in.Team.ID(),
					atc.Config{Jobs: atc.JobConfigs{{Name: "child-job"}}},
					db.ConfigVersion(0), false,
				)
				if err != nil {
					return PipelineGCReady{}, fmt.Errorf("set child pipeline %q: %w", child, err)
				}
				if err := build.Finish(db.BuildStatusSucceeded); err != nil {
					return PipelineGCReady{}, fmt.Errorf("finish the build that set %q: %w", child, err)
				}
				in.pipelines[child] = pipeline
				return in, nil
			},
		),

		brine.DefineMap[PipelineGCReady, PipelineGCReady](
			"the pipeline {string} is archived",
			func(in PipelineGCReady, p brine.Params, _ *brine.Recorder) (PipelineGCReady, error) {
				name, err := paramAt("the pipeline {string} is archived", p, 0)
				if err != nil {
					return PipelineGCReady{}, err
				}
				pipeline, ok := in.pipelines[name]
				if !ok {
					return PipelineGCReady{}, fmt.Errorf("no pipeline named %q was created by this scenario", name)
				}
				if err := pipeline.Archive(); err != nil {
					return PipelineGCReady{}, fmt.Errorf("archive %q: %w", name, err)
				}
				return in, nil
			},
		),

		brine.DefineMap[PipelineGCReady, PipelineGCSwept](
			"the collector sweeps for pipelines whose parent is gone",
			func(in PipelineGCReady, _ brine.Params, _ *brine.Recorder) (PipelineGCSwept, error) {
				return PipelineGCSwept{Ready: in, Err: in.Collector.Run(in.Ctx)}, nil
			},
		),

		CheckThat[PipelineGCSwept]("the pipeline sweep completed without error",
			func(in PipelineGCSwept) error {
				if in.Err != nil {
					return fmt.Errorf("the pipeline collector failed: %v", in.Err)
				}
				return nil
			}),

		// Both halves are membership in a list read from the table, and both
		// are POSITIVE: a pipeline the fixture never created appears in
		// neither list, so a broken fixture fails whichever question is asked
		// rather than passing the one phrased as an absence.
		CheckMember[PipelineGCSwept]("the pipeline {string} has been archived",
			"the pipelines the sweep archived",
			func(in PipelineGCSwept) ([]string, error) { return in.pipelinesWhere(true) }),

		CheckMember[PipelineGCSwept]("the pipeline {string} is still active",
			"the pipelines still active",
			func(in PipelineGCSwept) ([]string, error) { return in.pipelinesWhere(false) }),
	}
}

func (p PipelineGCSwept) pipelinesWhere(archived bool) ([]string, error) {
	rows, err := p.Ready.DB.Conn.Query(
		`SELECT name FROM pipelines WHERE archived = $1 ORDER BY name`, archived)
	if err != nil {
		return nil, fmt.Errorf("read the pipelines the sweep left behind: %w", err)
	}
	defer rows.Close()

	var names []string
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
// The build log collector
// -----------------------------------------------------------------------

func buildLogCollectorDefinitions() []brine.StepDefinition {
	defs := []brine.StepDefinition{

		// The eight policies. Each is its own sentence rather than one
		// parameterised one because each names a different rule, and a reader
		// of an Examples table should see the rule without decoding a column
		// of numbers. They all reach the same body.
		retentionGiven("a job that keeps only its most recent build",
			func(brine.Params) (retentionSpec, error) {
				return retentionSpec{builds: 1}, nil
			}),

		retentionGiven("a job that keeps its {int} most recent builds",
			func(p brine.Params) (retentionSpec, error) {
				n, err := intAt("a job that keeps its {int} most recent builds", p, 0)
				return retentionSpec{builds: n}, err
			}),

		retentionGiven("a job that keeps any build finished in the last day",
			func(brine.Params) (retentionSpec, error) {
				return retentionSpec{days: 1}, nil
			}),

		retentionGiven("a job that keeps any build finished in the last {int} days",
			func(p brine.Params) (retentionSpec, error) {
				n, err := intAt("a job that keeps any build finished in the last {int} days", p, 0)
				return retentionSpec{days: n}, err
			}),

		retentionGiven("a job that keeps only its most recent build, plus anything finished in the last {int} days",
			func(p brine.Params) (retentionSpec, error) {
				n, err := intAt("a job that keeps only its most recent build, plus anything finished in the last {int} days", p, 0)
				return retentionSpec{builds: 1, days: n}, err
			}),

		retentionGiven("a job that keeps its {int} most recent builds, at least {int} of them successful",
			func(p brine.Params) (retentionSpec, error) {
				pattern := "a job that keeps its {int} most recent builds, at least {int} of them successful"
				n, err := intAt(pattern, p, 0)
				if err != nil {
					return retentionSpec{}, err
				}
				m, err := intAt(pattern, p, 1)
				return retentionSpec{builds: n, minSucceeded: m}, err
			}),

		// Builds and Days both zero is not "no policy configured" — it is a
		// policy of keeping nothing, which reapLogsOfJob reads as "do not
		// touch this job at all".
		retentionGiven("a job that keeps no build logs at all",
			func(brine.Params) (retentionSpec, error) {
				return retentionSpec{}, nil
			}),

		// The job asks in its own config; the operator's flag caps it. These
		// are the only two numbers in the file that are allowed to disagree.
		retentionGiven("a job that asks to keep its {int} most recent builds where the operator allows at most {int}",
			func(p brine.Params) (retentionSpec, error) {
				pattern := "a job that asks to keep its {int} most recent builds where the operator allows at most {int}"
				asked, err := intAt(pattern, p, 0)
				if err != nil {
					return retentionSpec{}, err
				}
				allowed, err := intAt(pattern, p, 1)
				return retentionSpec{legacyBuilds: asked, maxBuilds: uint64(allowed)}, err
			}),

		// The neighbour that proves a sweep ran. Every build sentence after
		// this line belongs to the second pipeline.
		brine.DefineMap[LogReady, LogReady](
			"a second pipeline whose job keeps only its most recent build",
			func(in LogReady, _ brine.Params, _ *brine.Recorder) (LogReady, error) {
				return in.addJob("gc-logs-second-pipeline", retentionSpec{builds: 1})
			},
		),

		buildGiven("the build {string} failed {int} hours ago", db.BuildStatusFailed),
		buildGiven("the build {string} succeeded {int} hours ago", db.BuildStatusSucceeded),

		// A build with no end time and no status: the row a step is writing
		// events into right now.
		brine.DefineMap[LogReady, LogReady](
			"the build {string} is still running",
			func(in LogReady, p brine.Params, _ *brine.Recorder) (LogReady, error) {
				name, err := paramAt("the build {string} is still running", p, 0)
				if err != nil {
					return LogReady{}, err
				}
				return in, in.createBuild(name, "", 0)
			},
		),

		// Builds are created drained, because a deployment with no drainer
		// never sets the column and the collector ignores it there. This
		// sentence is the exception, and it is spelled out on its own line so
		// the scenario shows which build is the undrained one.
		brine.DefineMap[LogReady, LogReady](
			"the build {string} has not been drained yet",
			func(in LogReady, p brine.Params, _ *brine.Recorder) (LogReady, error) {
				name, err := paramAt("the build {string} has not been drained yet", p, 0)
				if err != nil {
					return LogReady{}, err
				}
				build, ok := in.builds[name]
				if !ok {
					return LogReady{}, fmt.Errorf("no build named %q was created by this scenario", name)
				}
				if _, err := in.DB.Conn.Exec(
					`UPDATE builds SET drained = false WHERE id = $1`, build.id); err != nil {
					return LogReady{}, fmt.Errorf("undrain build %q: %w", name, err)
				}
				return in, nil
			},
		),

		brine.DefineMap[LogReady, LogReady](
			"the build {string} was reaped by an earlier sweep",
			func(in LogReady, p brine.Params, _ *brine.Recorder) (LogReady, error) {
				name, err := paramAt("the build {string} was reaped by an earlier sweep", p, 0)
				if err != nil {
					return LogReady{}, err
				}
				build, ok := in.builds[name]
				if !ok {
					return LogReady{}, fmt.Errorf("no build named %q was created by this scenario", name)
				}
				if _, err := in.DB.Conn.Exec(
					`UPDATE builds SET reap_time = $1 WHERE id = $2`,
					time.Now().Add(-time.Hour), build.id); err != nil {
					return LogReady{}, fmt.Errorf("backdate the reap time of %q: %w", name, err)
				}
				return in, nil
			},
		),

		logRefine("a drainer is configured", func(in LogReady) LogReady {
			in.drainer = true
			return in
		}),

		// Not a no-op: it states the default the other row varies from, and
		// setting it explicitly means the outline reads the same either way.
		logRefine("no drainer is configured", func(in LogReady) LogReady {
			in.drainer = false
			return in
		}),

		brine.DefineMap[LogReady, LogReady](
			"the pipeline holding that job is paused",
			func(in LogReady, _ brine.Params, _ *brine.Recorder) (LogReady, error) {
				if in.current == nil {
					return LogReady{}, errNoJobYet
				}
				if err := in.current.pipeline.Pause("gc-pipelines"); err != nil {
					return LogReady{}, fmt.Errorf("pause the pipeline: %w", err)
				}
				return in, nil
			},
		),

		brine.DefineMap[LogReady, LogReady](
			"the job itself is paused",
			func(in LogReady, _ brine.Params, _ *brine.Recorder) (LogReady, error) {
				if in.current == nil {
					return LogReady{}, errNoJobYet
				}
				if err := in.current.job.Pause("gc-pipelines"); err != nil {
					return LogReady{}, fmt.Errorf("pause the job: %w", err)
				}
				return in, nil
			},
		),

		logRefine("the database behind the deleted-pipeline cleanup has gone away",
			func(in LogReady) LogReady { in.fault = faultCleanupConnClosed; return in }),
		logRefine("the database the pipeline listing reads has gone away",
			func(in LogReady) LogReady { in.fault = faultListingConnClosed; return in }),
		logRefine("the database will not list the jobs of the first pipeline",
			func(in LogReady) LogReady { in.fault = faultJobsUnreadable; return in }),
		logRefine("the database will not list the builds of the first pipeline's job",
			func(in LogReady) LogReady { in.fault = faultBuildsUnreadable; return in }),
		logRefine("the database will not delete the build events of the first pipeline",
			func(in LogReady) LogReady { in.fault = faultDeleteRefused; return in }),
		logRefine("the database will not advance the log cursor of the first pipeline's job",
			func(in LogReady) LogReady { in.fault = faultCursorRefused; return in }),

		brine.DefineMap[LogReady, LogSwept](
			"the build log collector sweeps",
			func(in LogReady, _ brine.Params, _ *brine.Recorder) (LogSwept, error) {
				return in.sweep()
			},
		),

		CheckThat[LogSwept]("the log sweep completed without error",
			func(in LogSwept) error {
				if in.Err != nil {
					return fmt.Errorf("the build log collector failed: %v", in.Err)
				}
				return nil
			}),

		CheckContains[LogSwept]("the log sweep was refused, saying {string}",
			"the refusal",
			func(in LogSwept) (string, error) { return refusal(in.Err, "the build log sweep") }),

		// The two halves are derived from the builds THIS SCENARIO CREATED, so
		// both are positive membership and neither can pass on an empty table.
		CheckMember[LogSwept]("the log of the build {string} survived the sweep",
			"the builds whose logs are still in the database",
			func(in LogSwept) ([]string, error) { return in.buildsWithLogs(true) }),

		CheckMember[LogSwept]("the log of the build {string} has been reaped",
			"the builds whose logs the sweep deleted",
			func(in LogSwept) ([]string, error) { return in.buildsWithLogs(false) }),

		CheckString[LogSwept]("the next sweep will start from the build {string}",
			"the build the next sweep resumes from",
			func(in LogSwept) (string, error) { return in.cursor() }),
	}
	return defs
}

var errNoJobYet = errors.New("no job has been described yet — a policy sentence has to come first")

// retentionGiven builds one of the eight policy sentences. They differ only in
// how the numbers are read out of the line, which is the argument.
func retentionGiven(pattern string, read func(brine.Params) (retentionSpec, error)) brine.StepDefinition {
	return brine.DefineMapUsing[brine.Empty, LogReady](
		pattern,
		[]string{"jetbridge-db"},
		func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (LogReady, error) {
			database, ok := res.Get("jetbridge-db").(JetbridgeDB)
			if !ok {
				return LogReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
			}
			spec, err := read(p)
			if err != nil {
				return LogReady{}, err
			}
			team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "gc-logs-team"})
			if err != nil {
				return LogReady{}, fmt.Errorf("create team: %w", err)
			}
			ready := LogReady{
				DB:     database,
				Ctx:    gcContext("build-reaper"),
				Team:   team,
				spec:   spec,
				builds: map[string]*loggedBuild{},
			}
			return ready.addJob("gc-logs-pipeline", spec)
		},
	)
}

// buildGiven backs the two finished-build sentences. A build is created,
// started, finished and marked drained, and then its end time is backdated,
// which is the only way to have a build that finished two days ago inside a
// scenario that started a moment ago.
func buildGiven(pattern string, status db.BuildStatus) brine.StepDefinition {
	return brine.DefineMap[LogReady, LogReady](pattern,
		func(in LogReady, p brine.Params, _ *brine.Recorder) (LogReady, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return LogReady{}, err
			}
			hours, err := intAt(pattern, p, 1)
			if err != nil {
				return LogReady{}, err
			}
			return in, in.createBuild(name, status, time.Duration(hours)*time.Hour)
		},
	)
}

// logRefine is a LogReady adjustment that cannot fail: it only records what
// the sweep should be built with.
func logRefine(pattern string, apply func(LogReady) LogReady) brine.StepDefinition {
	return brine.DefineMap[LogReady, LogReady](pattern,
		func(in LogReady, _ brine.Params, _ *brine.Recorder) (LogReady, error) {
			return apply(in), nil
		},
	)
}

// addJob saves a pipeline with a single job carrying the retention policy and
// makes it the job that later build sentences belong to.
func (in LogReady) addJob(pipelineName string, spec retentionSpec) (LogReady, error) {
	jobConfig := atc.JobConfig{Name: "some-job"}
	if spec.legacyBuilds > 0 {
		jobConfig.BuildLogsToRetain = spec.legacyBuilds
	} else {
		jobConfig.BuildLogRetention = &atc.BuildLogRetention{
			Builds:                 spec.builds,
			Days:                   spec.days,
			MinimumSucceededBuilds: spec.minSucceeded,
		}
	}

	pipeline, _, err := in.Team.SavePipeline(
		atc.PipelineRef{Name: pipelineName},
		atc.Config{Jobs: atc.JobConfigs{jobConfig}},
		db.ConfigVersion(0), false,
	)
	if err != nil {
		return LogReady{}, fmt.Errorf("save pipeline %q: %w", pipelineName, err)
	}
	job, found, err := pipeline.Job("some-job")
	if err != nil {
		return LogReady{}, fmt.Errorf("find the job of %q: %w", pipelineName, err)
	}
	if !found {
		return LogReady{}, fmt.Errorf("pipeline %q has no job", pipelineName)
	}

	under := &jobUnderTest{pipeline: pipeline, job: job}
	in.jobs = append(in.jobs, under)
	in.current = under
	return in, nil
}

// createBuild makes a build of the current job and leaves it in the state the
// sentence described. An empty status means the build is still running.
//
// The event-count check at the end is the precondition the reap assertions
// depend on: "has been reaped" is an absence, and a build that never had a log
// would satisfy it without the collector doing anything.
func (in LogReady) createBuild(name string, status db.BuildStatus, endedAgo time.Duration) error {
	if in.current == nil {
		return errNoJobYet
	}
	if _, taken := in.builds[name]; taken {
		return fmt.Errorf("this scenario already has a build named %q", name)
	}

	build, err := in.current.job.CreateBuild("gc-pipelines")
	if err != nil {
		return fmt.Errorf("create build %q: %w", name, err)
	}
	started, err := build.Start(atc.Plan{ID: atc.PlanID("log-" + name)})
	if err != nil {
		return fmt.Errorf("start build %q: %w", name, err)
	}
	if !started {
		return fmt.Errorf("build %q refused to start", name)
	}

	if status != "" {
		if err := build.Finish(status); err != nil {
			return fmt.Errorf("finish build %q: %w", name, err)
		}
	}
	if err := build.SetDrained(true); err != nil {
		return fmt.Errorf("mark build %q drained: %w", name, err)
	}
	if endedAgo > 0 {
		if _, err := in.DB.Conn.Exec(
			`UPDATE builds SET end_time = $1 WHERE id = $2`,
			time.Now().Add(-endedAgo), build.ID()); err != nil {
			return fmt.Errorf("backdate the end time of %q: %w", name, err)
		}
	}

	pipelineID := in.current.pipeline.ID()
	count, err := in.eventCount(pipelineID, build.ID())
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("build %q was created with no build events, so nothing later could "+
			"tell a reaped log from one that never existed", name)
	}

	in.builds[name] = &loggedBuild{id: build.ID(), pipelineID: pipelineID}
	in.current.builds = append(in.current.builds, name)
	return nil
}

func (in LogReady) eventCount(pipelineID, buildID int) (int, error) {
	var count int
	err := in.DB.Conn.QueryRow(
		fmt.Sprintf(`SELECT count(*) FROM pipeline_build_events_%d WHERE build_id = $1`, pipelineID),
		buildID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count the events of build %d: %w", buildID, err)
	}
	return count, nil
}

// sweep starts every job's cursor on its oldest build, assembles the collector
// the scenario described, and runs it. The error is recorded rather than
// returned: whether the sweep was refused is what the scenario goes on to say.
func (in LogReady) sweep() (LogSwept, error) {
	for _, under := range in.jobs {
		oldest := 0
		for _, name := range under.builds {
			id := in.builds[name].id
			if oldest == 0 || id < oldest {
				oldest = id
			}
		}
		if oldest == 0 {
			continue
		}
		if _, err := in.DB.Conn.Exec(
			`UPDATE jobs SET first_logged_build_id = $1 WHERE id = $2`,
			oldest, under.job.ID()); err != nil {
			return LogSwept{}, fmt.Errorf("start the log cursor of job %d: %w", under.job.ID(), err)
		}
	}

	factory := db.NewPipelineFactory(in.DB.Conn, in.DB.LockFactory)
	lifecycle := db.NewPipelineLifecycle(in.DB.Conn, in.DB.LockFactory)

	first := ""
	if len(in.jobs) > 0 {
		first = in.jobs[0].pipeline.Name()
	}

	switch in.fault {
	case faultCleanupConnClosed:
		closed, err := in.DB.ClosedConn()
		if err != nil {
			return LogSwept{}, err
		}
		lifecycle = db.NewPipelineLifecycle(closed, in.DB.LockFactory)
	case faultListingConnClosed:
		closed, err := in.DB.ClosedConn()
		if err != nil {
			return LogSwept{}, err
		}
		factory = db.NewPipelineFactory(closed, in.DB.LockFactory)
	case faultJobsUnreadable:
		factory = faultedPipelines{factory, first, func(p db.Pipeline) db.Pipeline {
			return unlistableJobs{p}
		}}
	case faultDeleteRefused:
		factory = faultedPipelines{factory, first, func(p db.Pipeline) db.Pipeline {
			return undeletableEvents{p}
		}}
	case faultBuildsUnreadable:
		factory = faultedPipelines{factory, first, func(p db.Pipeline) db.Pipeline {
			return faultedJobs{p, func(j db.Job) db.Job { return unlistableBuilds{j} }}
		}}
	case faultCursorRefused:
		factory = faultedPipelines{factory, first, func(p db.Pipeline) db.Pipeline {
			return faultedJobs{p, func(j db.Job) db.Job { return unmovableCursor{j} }}
		}}
	}

	collector := gc.NewBuildLogCollector(
		factory, lifecycle, logBatchSize,
		gc.NewBuildLogRetentionCalculator(0, in.spec.maxBuilds, 0, 0),
		in.drainer,
	)
	return LogSwept{Ready: in, Err: collector.Run(in.Ctx)}, nil
}

// buildsWithLogs answers with the scenario's own build names, split by whether
// their events are still there. Reading it this way rather than asking about
// one build is what makes both sentences positive: an unknown name is in
// neither list.
func (s LogSwept) buildsWithLogs(present bool) ([]string, error) {
	var names []string
	for _, under := range s.Ready.jobs {
		for _, name := range under.builds {
			build := s.Ready.builds[name]
			count, err := s.Ready.eventCount(build.pipelineID, build.id)
			if err != nil {
				return nil, err
			}
			if (count > 0) == present {
				names = append(names, name)
			}
		}
	}
	return names, nil
}

// cursor reports first_logged_build_id for the FIRST job, in the scenario's own
// vocabulary. Zero is a real answer and a distinct one — it means the next
// sweep starts from the beginning of the job's history — so it is named rather
// than reported as a missing build.
func (s LogSwept) cursor() (string, error) {
	if len(s.Ready.jobs) == 0 {
		return "", errNoJobYet
	}
	var id int
	if err := s.Ready.DB.Conn.QueryRow(
		`SELECT first_logged_build_id FROM jobs WHERE id = $1`,
		s.Ready.jobs[0].job.ID()).Scan(&id); err != nil {
		return "", fmt.Errorf("read the log cursor: %w", err)
	}
	if id == 0 {
		return "the beginning", nil
	}
	for name, build := range s.Ready.builds {
		if build.id == id {
			return name, nil
		}
	}
	return fmt.Sprintf("build %d, which this scenario did not create", id), nil
}

// -----------------------------------------------------------------------
// The faults
//
// Each wraps a real, PostgreSQL-backed row and fails exactly one method.
// Nothing is recorded and nothing is answered from a script; every other call
// still reaches the database, which is what lets the assertions afterwards be
// about real row state.
// -----------------------------------------------------------------------

// faultedPipelines keeps the real AllPipelines lookup and wraps only the
// pipeline the scenario named, so the healthy neighbour beside it is untouched
// and its reaping is evidence the sweep carried on.
type faultedPipelines struct {
	db.PipelineFactory
	target   string
	decorate func(db.Pipeline) db.Pipeline
}

func (f faultedPipelines) AllPipelines() ([]db.Pipeline, error) {
	pipelines, err := f.PipelineFactory.AllPipelines()
	if err != nil {
		return nil, err
	}
	for i, pipeline := range pipelines {
		if pipeline.Name() == f.target {
			pipelines[i] = f.decorate(pipeline)
		}
	}
	return pipelines, nil
}

type faultedJobs struct {
	db.Pipeline
	decorate func(db.Job) db.Job
}

func (f faultedJobs) Jobs() (db.Jobs, error) {
	jobs, err := f.Pipeline.Jobs()
	if err != nil {
		return nil, err
	}
	for i, job := range jobs {
		jobs[i] = f.decorate(job)
	}
	return jobs, nil
}

type unlistableJobs struct{ db.Pipeline }

func (unlistableJobs) Jobs() (db.Jobs, error) { return nil, errLogFault }

type undeletableEvents struct{ db.Pipeline }

func (undeletableEvents) DeleteBuildEventsByBuildIDs([]int) error { return errLogFault }

type unlistableBuilds struct{ db.Job }

func (unlistableBuilds) ChronoBuilds(db.Page) ([]db.BuildForAPI, db.Pagination, error) {
	return nil, db.Pagination{}, errLogFault
}

type unmovableCursor struct{ db.Job }

func (unmovableCursor) UpdateFirstLoggedBuildID(int) error { return errLogFault }

// -----------------------------------------------------------------------
// The build collector
// -----------------------------------------------------------------------

func buildCollectorDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, InterceptReady](
			"a collector that retires finished builds from interception",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (InterceptReady, error) {
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return InterceptReady{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "gc-intercept-team"})
				if err != nil {
					return InterceptReady{}, fmt.Errorf("create team: %w", err)
				}
				pipeline, _, err := team.SavePipeline(
					atc.PipelineRef{Name: "gc-intercept-pipeline"},
					atc.Config{Jobs: atc.JobConfigs{{Name: "some-job"}}},
					db.ConfigVersion(0), false,
				)
				if err != nil {
					return InterceptReady{}, fmt.Errorf("save pipeline: %w", err)
				}
				job, found, err := pipeline.Job("some-job")
				if err != nil {
					return InterceptReady{}, fmt.Errorf("find the job: %w", err)
				}
				if !found {
					return InterceptReady{}, fmt.Errorf("the pipeline has no job")
				}
				return InterceptReady{
					DB:        database,
					Collector: gc.NewBuildCollector(database.BuildFactory),
					Ctx:       gcContext("build-collector"),
					Job:       job,
					builds:    map[string]int{},
				}, nil
			},
		),

		// Three states, and the reason the middle one is carried at all.
		//
		// The claim that used to stand here was that the ATC keeps a FAILED
		// build interceptible for a grace period, so using one would put the
		// scenario on a clock. That is not what this tree does. db.Build.Finish
		// sets interceptible = false in the same UPDATE that completes the
		// build, for every status other than succeeded (atc/db/build.go), so a
		// failed build arrives at the sweep already retired and the grace
		// period in constructBuildFilter never sees it.
		//
		// Corrected, the reason for choosing SUCCEEDED is stronger than the
		// wrong one was: it is the only finished status whose retirement the
		// collector can be responsible for. The failed build is here to hold
		// that difference still. If Finish stopped marking non-succeeded
		// builds, its precondition line below reddens and the scenario says
		// what changed, instead of the succeeded build's assertion staying
		// green while meaning something else.
		interceptGiven("the build {string} has finished successfully", db.BuildStatusSucceeded),
		interceptGiven("the build {string} has finished, failing", db.BuildStatusFailed),
		interceptGiven("the build {string} is still in flight", ""),

		// The two readings taken BEFORE the sweep.
		//
		// `interceptible` is not a column only this collector writes, so a
		// post-state on its own attributes nothing: "the finished build can no
		// longer be intercepted" is equally true of a build that was never
		// interceptible in the first place. These sentences are the missing
		// half, and they are in the scenario rather than hidden in the fixture
		// because the before-and-after IS the argument, and a reader should be
		// able to see both halves of it on the page.
		CheckMember[InterceptReady]("the build {string} can be intercepted before the sweep",
			"the builds that can be intercepted before the sweep",
			func(in InterceptReady) ([]string, error) { return in.interceptible(true) }),

		CheckMember[InterceptReady]("the build {string} could not be intercepted even before the sweep",
			"the builds already retired from interception before the sweep",
			func(in InterceptReady) ([]string, error) { return in.interceptible(false) }),

		brine.DefineMap[InterceptReady, InterceptSwept](
			"the collector sweeps builds that can no longer be intercepted",
			func(in InterceptReady, _ brine.Params, _ *brine.Recorder) (InterceptSwept, error) {
				return InterceptSwept{Ready: in, Err: in.Collector.Run(in.Ctx)}, nil
			},
		),

		CheckThat[InterceptSwept]("the interception sweep completed without error",
			func(in InterceptSwept) error {
				if in.Err != nil {
					return fmt.Errorf("the build collector failed: %v", in.Err)
				}
				return nil
			}),

		CheckMember[InterceptSwept]("the build {string} can still be intercepted",
			"the builds that can still be intercepted",
			func(in InterceptSwept) ([]string, error) { return in.interceptible(true) }),

		CheckMember[InterceptSwept]("the build {string} can no longer be intercepted",
			"the builds the sweep retired from interception",
			func(in InterceptSwept) ([]string, error) { return in.interceptible(false) }),
	}
}

// interceptGiven creates a build of the collector's job and leaves it in the
// state the sentence named. An empty status means the build never finished.
func interceptGiven(pattern string, status db.BuildStatus) brine.StepDefinition {
	return brine.DefineMap[InterceptReady, InterceptReady](pattern,
		func(in InterceptReady, p brine.Params, _ *brine.Recorder) (InterceptReady, error) {
			name, err := paramAt(pattern, p, 0)
			if err != nil {
				return InterceptReady{}, err
			}
			build, err := in.Job.CreateBuild("gc-pipelines")
			if err != nil {
				return InterceptReady{}, fmt.Errorf("create build %q: %w", name, err)
			}
			if _, err := build.Start(atc.Plan{ID: atc.PlanID("intercept-" + name)}); err != nil {
				return InterceptReady{}, fmt.Errorf("start build %q: %w", name, err)
			}
			if status != "" {
				if err := build.Finish(status); err != nil {
					return InterceptReady{}, fmt.Errorf("finish build %q: %w", name, err)
				}
			}
			in.builds[name] = build.ID()
			return in, nil
		},
	)
}

// interceptible answers with the scenario's own build names, split by whether
// the `interceptible` column is still set. It hangs off InterceptReady, not
// InterceptSwept, so the same question can be put before the sweep and after
// it — which is what turns a post-state into a before-and-after.
func (in InterceptReady) interceptible(want bool) ([]string, error) {
	var names []string
	for name, id := range in.builds {
		var interceptible bool
		if err := in.DB.Conn.QueryRow(
			`SELECT interceptible FROM builds WHERE id = $1`, id).Scan(&interceptible); err != nil {
			return nil, fmt.Errorf("read whether build %q is interceptible: %w", name, err)
		}
		if interceptible == want {
			names = append(names, name)
		}
	}
	return names, nil
}

func (s InterceptSwept) interceptible(want bool) ([]string, error) {
	return s.Ready.interceptible(want)
}

// gcContext gives a collector the logger it reaches for through the context.
// The build collector emits a duration metric through it, so an absent one is
// not merely quiet.
func gcContext(session string) context.Context {
	return lagerctx.NewContext(context.Background(), lagertest.NewTestLogger(session))
}
