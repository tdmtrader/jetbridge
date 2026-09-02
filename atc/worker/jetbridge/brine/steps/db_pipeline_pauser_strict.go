package steps

import (
	"context"
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBPipelinePauserObservation struct {
	Profile string
	Failure string
}

func DBPipelinePauserStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBPipelinePauserObservation](
			"the production pipeline pauser profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBPipelinePauserObservation, error) {
				profile, err := paramAt("the production pipeline pauser profile {string} is exercised", p, 0)
				if err != nil {
					return DBPipelinePauserObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBPipelinePauserObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBPipelinePauserObservation{Profile: profile, Failure: observeDBPipelinePauser(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBPipelinePauserObservation](
			"the pipeline pauser observation exactly matches {string}",
			func(in DBPipelinePauserObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the pipeline pauser observation exactly matches {string}", p, 0)
				if err != nil {
					return err
				}
				if in.Profile != profile {
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

func observeDBPipelinePauser(database JetbridgeDB, profile string) string {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "pipeline-pauser-team"})
	if err != nil {
		return err.Error()
	}
	pipeline, _, err := team.SavePipeline(
		atc.PipelineRef{Name: "pipeline-pauser-pipeline"},
		atc.Config{Jobs: atc.JobConfigs{{Name: "job-one"}, {Name: "job-two"}}},
		0,
		false,
	)
	if err != nil {
		return err.Error()
	}
	jobOne, found, err := pipeline.Job("job-one")
	if err != nil || !found {
		return fmt.Sprintf("job-one found=%t err=%v", found, err)
	}
	jobTwo, found, err := pipeline.Job("job-two")
	if err != nil || !found {
		return fmt.Sprintf("job-two found=%t err=%v", found, err)
	}

	setPipelineAge := func(days int) error {
		_, err := database.Conn.Exec(`UPDATE pipelines SET last_updated = NOW() - make_interval(days => $1) WHERE id = $2`, days, pipeline.ID())
		return err
	}
	finishAtAge := func(job db.Job, days int) error {
		build, err := job.CreateBuild("strict pipeline pauser")
		if err != nil {
			return err
		}
		if err := build.Finish(db.BuildStatusSucceeded); err != nil {
			return err
		}
		_, err = database.Conn.Exec(`UPDATE builds SET end_time = NOW() - make_interval(days => $1) WHERE id = $2`, days, build.ID())
		return err
	}

	wantPaused := false
	wantReason := ""
	switch profile {
	case "old-zero-job":
		if err := setPipelineAge(15); err != nil {
			return err.Error()
		}
		if err := finishAtAge(jobOne, 15); err != nil {
			return err.Error()
		}
		wantPaused = true
	case "old-all-jobs":
		if err := setPipelineAge(15); err != nil {
			return err.Error()
		}
		if err := finishAtAge(jobOne, 15); err != nil {
			return err.Error()
		}
		if err := finishAtAge(jobTwo, 20); err != nil {
			return err.Error()
		}
		wantPaused = true
	case "pause-reason":
		if err := setPipelineAge(20); err != nil {
			return err.Error()
		}
		if err := finishAtAge(jobOne, 20); err != nil {
			return err.Error()
		}
		wantPaused = true
		wantReason = "automatic-pipeline-pauser"
	case "recent-build":
		if err := finishAtAge(jobOne, 1); err != nil {
			return err.Error()
		}
		if err := finishAtAge(jobTwo, 11); err != nil {
			return err.Error()
		}
	case "boundary-build":
		if err := finishAtAge(jobOne, 10); err != nil {
			return err.Error()
		}
		if err := finishAtAge(jobTwo, 20); err != nil {
			return err.Error()
		}
	case "newly-set":
		// A freshly saved pipeline with no builds must stay unpaused.
	case "running-build":
		if err := setPipelineAge(5); err != nil {
			return err.Error()
		}
		if err := finishAtAge(jobOne, 11); err != nil {
			return err.Error()
		}
		build, err := jobOne.CreateBuild("strict pipeline pauser")
		if err != nil {
			return err.Error()
		}
		if !build.IsRunning() {
			return "new build is not running"
		}
	default:
		return fmt.Sprintf("unknown pipeline pauser profile %q", profile)
	}

	pauser := db.NewPipelinePauser(database.Conn, database.LockFactory)
	if err := pauser.PausePipelines(context.Background(), 10); err != nil {
		return err.Error()
	}
	loaded, err := pipeline.Reload()
	if err != nil || !loaded {
		return fmt.Sprintf("pipeline reload loaded=%t err=%v", loaded, err)
	}
	if pipeline.Paused() != wantPaused {
		return fmt.Sprintf("paused=%t, want %t", pipeline.Paused(), wantPaused)
	}
	if wantReason != "" && pipeline.PausedBy() != wantReason {
		return fmt.Sprintf("paused by=%q, want %q", pipeline.PausedBy(), wantReason)
	}
	return ""
}
