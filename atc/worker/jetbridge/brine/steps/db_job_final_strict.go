package steps

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/metric"
)

type DBJobFinalObservation struct {
	Profile string
	Failure string
}

type strictJobDatabase struct {
	Conn          db.DbConn
	LockFactory   lock.LockFactory
	TeamFactory   db.TeamFactory
	WorkerFactory db.WorkerFactory
}

func DBJobFinalStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBJobFinalObservation](
			"the remaining production job behavior {string} is exercised",
			[]string{"postgres"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBJobFinalObservation, error) {
				profile, err := paramAt("the remaining production job behavior {string} is exercised", p, 0)
				if err != nil {
					return DBJobFinalObservation{}, err
				}
				pm, ok := resources.Get("postgres").(*postmaster)
				if !ok {
					return DBJobFinalObservation{}, fmt.Errorf("postgres resource is %T", resources.Get("postgres"))
				}
				return DBJobFinalObservation{Profile: profile, Failure: observeDBJobFinal(pm, profile)}, nil
			},
		),
		brine.DefineCheck[DBJobFinalObservation](
			"the remaining production job behavior exactly matches {string}",
			func(in DBJobFinalObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the remaining production job behavior exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if profile != in.Profile {
					return fmt.Errorf("profile got %q, want %q", in.Profile, profile)
				}
				if in.Failure != "" {
					return fmt.Errorf("%s: %s", profile, in.Failure)
				}
				return nil
			},
		),
	}
}

func withStrictJobDatabase(pm *postmaster, fn func(strictJobDatabase) string) string {
	pm.runner.CreateTestDBFromTemplate()
	conn, err := openJetbridgeConn(pm.runner)
	if err != nil {
		pm.runner.DropTestDB()
		return err.Error()
	}
	db.CleanupBaseResourceTypesCache()

	var lockConns [lock.FactoryCount]*sql.DB
	for i := 0; i < lock.FactoryCount; i++ {
		lockConns[i] = pm.runner.OpenSingleton()
	}
	lockFactory := lock.NewLockFactory(lockConns, metric.LogLockAcquired, metric.LogLockReleased)
	workerCache, err := db.NewWorkerCache(lager.NewLogger("brine-job-strict"), conn, 0)
	if err != nil {
		for _, lockConn := range lockConns {
			_ = lockConn.Close()
		}
		_ = conn.Close()
		pm.runner.DropTestDB()
		return err.Error()
	}
	database := strictJobDatabase{
		Conn:          conn,
		LockFactory:   lockFactory,
		TeamFactory:   db.NewTeamFactory(conn, lockFactory),
		WorkerFactory: db.NewWorkerFactory(conn, workerCache),
	}
	failure := fn(database)
	for _, lockConn := range lockConns {
		_ = lockConn.Close()
	}
	if err := conn.Close(); err != nil && failure == "" {
		failure = err.Error()
	}
	pm.runner.DropTestDB()
	return failure
}

func observeDBJobFinal(pm *postmaster, profile string) string {
	return withStrictJobDatabase(pm, func(database strictJobDatabase) string {
		switch {
		case strings.HasPrefix(profile, "schedule-"):
			return observeFinalSchedule(database, profile)
		case strings.HasPrefix(profile, "lifecycle-"):
			return observeFinalLifecycle(database, profile)
		case strings.HasPrefix(profile, "cache-"):
			return observeFinalTaskCache(database, profile)
		default:
			return fmt.Sprintf("unknown profile %q", profile)
		}
	})
}

func createFinalJob(database strictJobDatabase, config atc.Config, jobName string) (db.Team, db.Pipeline, db.Job, error) {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "job-final-team"})
	if err != nil {
		return nil, nil, nil, err
	}
	job, pipeline, err := saveJobForStrictTeam(team, "job-final-pipeline", config, jobName)
	return team, pipeline, job, err
}

func finalScheduleConfig(profile string) atc.Config {
	job := atc.JobConfig{Name: "job"}
	peer := atc.JobConfig{Name: "peer"}
	if profile == "schedule-max-one-allowed" {
		job.RawMaxInFlight = 2
	}
	if strings.Contains(profile, "serial-") {
		job.Serial = true
		job.SerialGroups = []string{"group"}
		peer.SerialGroups = []string{"group"}
	}
	return atc.Config{Jobs: atc.JobConfigs{job, peer}}
}

func observeFinalSchedule(database strictJobDatabase, profile string) string {
	_, pipeline, job, err := createFinalJob(database, finalScheduleConfig(profile), "job")
	if err != nil {
		return err.Error()
	}
	candidate, err := job.CreateBuild("brine")
	if err != nil {
		return err.Error()
	}

	if profile == "schedule-pipeline-paused" {
		if err := pipeline.Pause("brine"); err != nil {
			return err.Error()
		}
	}
	if profile == "schedule-job-paused" || profile == "schedule-job-paused-inputs" {
		if profile == "schedule-job-paused-inputs" {
			if err := job.SaveNextInputMapping(nil, true); err != nil {
				return err.Error()
			}
		}
		if err := job.Pause("brine"); err != nil {
			return err.Error()
		}
	}
	if profile == "schedule-missing-build" {
		deleted, err := candidate.Delete()
		if err != nil || !deleted {
			return fmt.Sprintf("delete found=%t err=%v", deleted, err)
		}
	}
	if profile == "schedule-max-one-allowed" {
		running, err := job.CreateBuild("brine")
		if err != nil {
			return err.Error()
		}
		if scheduled, err := job.ScheduleBuild(running); err != nil || !scheduled {
			return fmt.Sprintf("schedule running=%t err=%v", scheduled, err)
		}
		if started, err := running.Start(atc.Plan{}); err != nil || !started {
			return fmt.Sprintf("start running=%t err=%v", started, err)
		}
	}
	if strings.Contains(profile, "serial-") {
		peer, found, err := pipeline.Job("peer")
		if err != nil || !found {
			return fmt.Sprintf("peer found=%t err=%v", found, err)
		}
		if err := job.SaveNextInputMapping(nil, true); err != nil {
			return err.Error()
		}
		if err := peer.SaveNextInputMapping(nil, true); err != nil {
			return err.Error()
		}
		switch profile {
		case "schedule-serial-finished-allowed":
			peerBuild, err := peer.CreateBuild("brine")
			if err != nil {
				return err.Error()
			}
			if scheduled, err := peer.ScheduleBuild(peerBuild); err != nil || !scheduled {
				return fmt.Sprintf("schedule peer=%t err=%v", scheduled, err)
			}
			if err := peerBuild.Finish(db.BuildStatusSucceeded); err != nil {
				return err.Error()
			}
		case "schedule-serial-earlier-subject-allowed":
			if _, err := peer.CreateBuild("brine"); err != nil {
				return err.Error()
			}
		case "schedule-serial-succeeded-ignored":
			peerBuild, err := peer.CreateBuild("brine")
			if err != nil {
				return err.Error()
			}
			if err := peerBuild.Finish(db.BuildStatusSucceeded); err != nil {
				return err.Error()
			}
		}
	}

	scheduled, scheduleErr := job.ScheduleBuild(candidate)
	found, reloadErr := candidate.Reload()
	if profile == "schedule-missing-build" {
		if scheduleErr == nil || scheduled || found || reloadErr != nil {
			return fmt.Sprintf("scheduled=%t scheduleErr=%v found=%t reloadErr=%v", scheduled, scheduleErr, found, reloadErr)
		}
		return ""
	}
	blocked := profile == "schedule-pipeline-paused" || profile == "schedule-job-paused" || profile == "schedule-job-paused-inputs"
	if scheduleErr != nil || reloadErr != nil || !found || scheduled == blocked || candidate.IsScheduled() == blocked {
		return fmt.Sprintf("scheduled=%t scheduleErr=%v found=%t reloadErr=%v persisted=%t blocked=%t", scheduled, scheduleErr, found, reloadErr, candidate.IsScheduled(), blocked)
	}
	return ""
}

func observeFinalLifecycle(database strictJobDatabase, profile string) string {
	team, _, job, err := createFinalJob(database, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, "job")
	if err != nil {
		return err.Error()
	}
	first, err := job.CreateBuild("brine")
	if err != nil {
		return err.Error()
	}

	switch profile {
	case "lifecycle-cross-pipeline-scope":
		other, _, err := saveJobForStrictTeam(team, "other-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, "job")
		if err != nil {
			return err.Error()
		}
		if _, err := other.CreateBuild("brine"); err != nil {
			return err.Error()
		}
		pending, err := job.GetPendingBuilds()
		if err != nil || len(pending) != 1 || pending[0].ID() != first.ID() {
			return fmt.Sprintf("pending=%v err=%v", buildNames(pending), err)
		}
	case "lifecycle-scheduled-remains-pending":
		if scheduled, err := job.ScheduleBuild(first); err != nil || !scheduled {
			return fmt.Sprintf("schedule=%t err=%v", scheduled, err)
		}
		pending, err := job.GetPendingBuilds()
		if err != nil || len(pending) != 1 || pending[0].ID() != first.ID() {
			return fmt.Sprintf("pending=%v err=%v", buildNames(pending), err)
		}
	case "lifecycle-two-pending-order":
		second, err := job.CreateBuild("brine")
		if err != nil {
			return err.Error()
		}
		return expectPendingNames(job, []string{first.Name(), second.Name()})
	case "lifecycle-rerun-old-order":
		original, err := job.CreateBuild("brine")
		if err != nil {
			return err.Error()
		}
		newest, err := job.CreateBuild("brine")
		if err != nil {
			return err.Error()
		}
		if err := original.Finish(db.BuildStatusSucceeded); err != nil {
			return err.Error()
		}
		rerun, err := job.RerunBuild(original, "brine")
		if err != nil {
			return err.Error()
		}
		return expectPendingNames(job, []string{first.Name(), rerun.Name(), newest.Name()})
	case "lifecycle-rerun-newest-order":
		original, err := job.CreateBuild("brine")
		if err != nil {
			return err.Error()
		}
		rerun, err := job.RerunBuild(original, "brine")
		if err != nil {
			return err.Error()
		}
		newest, err := job.CreateBuild("brine")
		if err != nil {
			return err.Error()
		}
		return expectPendingNames(job, []string{first.Name(), original.Name(), rerun.Name(), newest.Name()})
	case "lifecycle-multiple-rerun-order":
		original, err := job.CreateBuild("brine")
		if err != nil {
			return err.Error()
		}
		newest, err := job.CreateBuild("brine")
		if err != nil {
			return err.Error()
		}
		newestRerun, err := job.RerunBuild(newest, "brine")
		if err != nil {
			return err.Error()
		}
		rerun, err := job.RerunBuild(original, "brine")
		if err != nil {
			return err.Error()
		}
		rerun2, err := job.RerunBuild(rerun, "brine")
		if err != nil {
			return err.Error()
		}
		return expectPendingNames(job, []string{first.Name(), original.Name(), rerun.Name(), rerun2.Name(), newest.Name(), newestRerun.Name()})
	case "lifecycle-start-state-plan":
		started, err := first.Start(atc.Plan{ID: "strict-plan"})
		if err != nil || !started {
			return fmt.Sprintf("started=%t err=%v", started, err)
		}
		found, err := first.Reload()
		if err != nil || !found || first.Status() != db.BuildStatusStarted || first.Schema() != "exec.v2" || first.PrivatePlan().ID != "strict-plan" {
			return fmt.Sprintf("found=%t status=%s schema=%q plan=%q err=%v", found, first.Status(), first.Schema(), first.PrivatePlan().ID, err)
		}
	case "lifecycle-start-time":
		if started, err := first.Start(atc.Plan{}); err != nil || !started {
			return fmt.Sprintf("started=%t err=%v", started, err)
		}
		found, err := first.Reload()
		if err != nil || !found || time.Since(first.StartTime()) > 10*time.Second || first.StartTime().After(time.Now().Add(time.Second)) {
			return fmt.Sprintf("found=%t start=%s err=%v", found, first.StartTime(), err)
		}
	case "lifecycle-finish-state-time":
		if err := first.Finish(db.BuildStatusSucceeded); err != nil {
			return err.Error()
		}
		found, err := first.Reload()
		if err != nil || !found || first.Status() != db.BuildStatusSucceeded || time.Since(first.EndTime()) > 10*time.Second || first.EndTime().After(time.Now().Add(time.Second)) {
			return fmt.Sprintf("found=%t status=%s end=%s err=%v", found, first.Status(), first.EndTime(), err)
		}
	default:
		return fmt.Sprintf("unknown lifecycle profile %q", profile)
	}
	return ""
}

func buildNames(builds []db.Build) []string {
	names := make([]string, len(builds))
	for i, build := range builds {
		names[i] = build.Name()
	}
	return names
}

func expectPendingNames(job db.Job, want []string) string {
	pending, err := job.GetPendingBuilds()
	if err != nil {
		return err.Error()
	}
	got := buildNames(pending)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		return fmt.Sprintf("pending got=%v want=%v", got, want)
	}
	return ""
}

func observeFinalTaskCache(database strictJobDatabase, profile string) string {
	_, _, job, err := createFinalJob(database, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}, {Name: "other"}}}, "job")
	if err != nil {
		return err.Error()
	}
	team, found, err := database.TeamFactory.FindTeam("job-final-team")
	if err != nil || !found {
		return fmt.Sprintf("team found=%t err=%v", found, err)
	}
	pipeline, found, err := team.Pipeline(atc.PipelineRef{Name: "job-final-pipeline"})
	if err != nil || !found {
		return fmt.Sprintf("pipeline found=%t err=%v", found, err)
	}
	other, found, err := pipeline.Job("other")
	if err != nil || !found {
		return fmt.Sprintf("other found=%t err=%v", found, err)
	}
	if _, err := database.WorkerFactory.SaveWorker(atc.Worker{Name: "worker", Platform: "linux", State: string(db.WorkerStateRunning)}, 0); err != nil {
		return err.Error()
	}
	taskCaches := db.NewTaskCacheFactory(database.Conn)
	workerCaches := db.NewWorkerTaskCacheFactory(database.Conn)
	selected, err := taskCaches.FindOrCreate(job.ID(), "step", "path")
	if err != nil {
		return err.Error()
	}
	if _, err := workerCaches.FindOrCreate(db.WorkerTaskCache{TaskCache: selected, WorkerName: "worker"}); err != nil {
		return err.Error()
	}
	otherCache, err := taskCaches.FindOrCreate(other.ID(), "other-step", "other-path")
	if err != nil {
		return err.Error()
	}
	if _, err := workerCaches.FindOrCreate(db.WorkerTaskCache{TaskCache: otherCache, WorkerName: "worker"}); err != nil {
		return err.Error()
	}

	step, path := "step", "path"
	if profile == "cache-missing-path-zero" {
		path = "missing"
	}
	if profile == "cache-missing-step-zero" || profile == "cache-missing-step-preserves" {
		step, path = "missing", ""
	}
	if profile == "cache-step-preserves-other-job" {
		path = ""
	}
	rows, err := job.ClearTaskCache(step, path)
	if err != nil {
		return err.Error()
	}
	if (profile == "cache-missing-path-zero" || profile == "cache-missing-step-zero") && rows != 0 {
		return fmt.Sprintf("deleted rows=%d want=0", rows)
	}
	if profile == "cache-missing-step-preserves" {
		cache, found, err := taskCaches.Find(job.ID(), "step", "path")
		if err != nil || !found || cache == nil {
			return fmt.Sprintf("selected cache found=%t cache=%v err=%v", found, cache, err)
		}
		_, found, err = workerCaches.Find(db.WorkerTaskCache{TaskCache: cache, WorkerName: "worker"})
		if err != nil || !found {
			return fmt.Sprintf("selected worker cache found=%t err=%v", found, err)
		}
	}
	if profile == "cache-path-preserves-other-job" || profile == "cache-step-preserves-other-job" {
		cache, found, err := taskCaches.Find(other.ID(), "other-step", "other-path")
		if err != nil || !found || cache == nil {
			return fmt.Sprintf("other cache found=%t cache=%v err=%v", found, cache, err)
		}
	}
	return ""
}
