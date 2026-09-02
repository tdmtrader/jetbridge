package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBPipelineLifecycleObservation struct {
	Profile string
	Failure string
}

func DBPipelineLifecycleStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBPipelineLifecycleObservation](
			"the production pipeline lifecycle behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBPipelineLifecycleObservation, error) {
				profile, err := paramAt("the production pipeline lifecycle behavior {string} is exercised", p, 0)
				if err != nil {
					return DBPipelineLifecycleObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBPipelineLifecycleObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBPipelineLifecycleObservation{Profile: profile, Failure: observeDBPipelineLifecycle(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBPipelineLifecycleObservation](
			"the pipeline lifecycle behavior exactly matches {string}",
			func(in DBPipelineLifecycleObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the pipeline lifecycle behavior exactly matches {string}", p, 0)
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

func observeDBPipelineLifecycle(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	lifecycle := db.NewPipelineLifecycle(database.Conn, database.LockFactory)

	if profile == "remove-event-tables" || profile == "clear-deleted-pipelines" || profile == "missing-event-table" {
		team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-lifecycle-events-team"})
		if err != nil {
			return err.Error()
		}
		first, _, err := team.SavePipeline(atc.PipelineRef{Name: "first"}, atc.Config{}, 0, false)
		if err != nil {
			return err.Error()
		}
		second, _, err := team.SavePipeline(atc.PipelineRef{Name: "second"}, atc.Config{}, 0, false)
		if err != nil {
			return err.Error()
		}
		if err := first.Destroy(); err != nil {
			return err.Error()
		}
		if profile == "remove-event-tables" {
			if err := second.Destroy(); err != nil {
				return err.Error()
			}
		}
		if profile == "missing-event-table" {
			if _, err := database.Conn.Exec(fmt.Sprintf("DROP TABLE pipeline_build_events_%d", first.ID())); err != nil {
				return err.Error()
			}
		}
		if err := lifecycle.RemoveBuildEventsForDeletedPipelines(); err != nil {
			return fail("remove deleted pipeline events: %v", err)
		}
		if profile == "remove-event-tables" {
			for _, id := range []int{first.ID(), second.ID()} {
				var exists bool
				if err := database.Conn.QueryRow("SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)", fmt.Sprintf("pipeline_build_events_%d", id)).Scan(&exists); err != nil {
					return err.Error()
				}
				if exists {
					return fail("pipeline event table %d still exists", id)
				}
			}
		}
		if profile == "clear-deleted-pipelines" {
			var count int
			if err := database.Conn.QueryRow("SELECT COUNT(*) FROM deleted_pipelines").Scan(&count); err != nil {
				return err.Error()
			}
			if count != 0 {
				return fail("deleted_pipelines count=%d, want 0", count)
			}
		}
		return ""
	}

	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-pipeline-lifecycle-team"})
	if err != nil {
		return err.Error()
	}
	config := atc.Config{Jobs: atc.JobConfigs{{Name: "parent-job"}}}
	parent, _, err := team.SavePipeline(atc.PipelineRef{Name: "parent"}, config, 0, false)
	if err != nil {
		return err.Error()
	}
	if profile == "no-parent" {
		if err := lifecycle.ArchiveAbandonedPipelines(); err != nil {
			return err.Error()
		}
		found, err := parent.Reload()
		if err != nil || !found || parent.Archived() {
			return fail("parent found=%t archived=%t err=%v", found, parent.Archived(), err)
		}
		return ""
	}
	job, found, err := parent.Job("parent-job")
	if err != nil || !found {
		return fail("parent job found=%t err=%v", found, err)
	}
	setBuild, err := job.CreateBuild("strict pipeline lifecycle")
	if err != nil {
		return err.Error()
	}
	child, _, err := setBuild.SavePipeline(atc.PipelineRef{Name: "child"}, team.ID(), config, 0, false)
	if err != nil {
		return err.Error()
	}
	if err := setBuild.Finish(db.BuildStatusSucceeded); err != nil {
		return err.Error()
	}

	wantArchived := false
	var lastUpdated = child.LastUpdated()
	switch profile {
	case "parent-and-child-live":
	case "parent-destroyed":
		wantArchived = true
		if err := parent.Destroy(); err != nil {
			return err.Error()
		}
	case "child-already-archived":
		if err := child.Archive(); err != nil {
			return err.Error()
		}
		found, err := child.Reload()
		if err != nil || !found {
			return fail("reload archived child found=%t err=%v", found, err)
		}
		lastUpdated = child.LastUpdated()
		wantArchived = true
		if err := parent.Destroy(); err != nil {
			return err.Error()
		}
	case "parent-archived":
		wantArchived = true
		if err := parent.Archive(); err != nil {
			return err.Error()
		}
	case "parent-job-removed":
		wantArchived = true
		_, _, err := team.SavePipeline(atc.PipelineRef{Name: "parent"}, atc.Config{Jobs: atc.JobConfigs{{Name: "replacement"}}}, parent.ConfigVersion(), false)
		if err != nil {
			return err.Error()
		}
	case "parent-build-newer":
		if err := child.SetParentIDs(job.ID(), setBuild.ID()+1); err != nil {
			return err.Error()
		}
	case "later-build-succeeded", "later-build-failed":
		later, err := job.CreateBuild("strict pipeline lifecycle later")
		if err != nil {
			return err.Error()
		}
		status := db.BuildStatusSucceeded
		if profile == "later-build-failed" {
			status = db.BuildStatusFailed
		} else {
			wantArchived = true
		}
		if err := later.Finish(status); err != nil {
			return err.Error()
		}
	default:
		return fail("unknown pipeline lifecycle profile %q", profile)
	}

	if err := lifecycle.ArchiveAbandonedPipelines(); err != nil {
		return err.Error()
	}
	found, err = child.Reload()
	if err != nil || !found || child.Archived() != wantArchived {
		return fail("child found=%t archived=%t want=%t err=%v", found, child.Archived(), wantArchived, err)
	}
	if profile == "parent-and-child-live" {
		found, err := parent.Reload()
		if err != nil || !found || parent.Archived() {
			return fail("parent found=%t archived=%t err=%v", found, parent.Archived(), err)
		}
	}
	if profile == "child-already-archived" && !child.LastUpdated().Equal(lastUpdated) {
		return fail("archived child last_updated changed from %s to %s", lastUpdated, child.LastUpdated())
	}
	return ""
}
