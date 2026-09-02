package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBJobTaskCacheObservation struct {
	Profile string
	Failure string
}

func DBJobTaskCacheStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBJobTaskCacheObservation](
			"the production job task-cache behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBJobTaskCacheObservation, error) {
				profile, err := paramAt("the production job task-cache behavior {string} is exercised", p, 0)
				if err != nil {
					return DBJobTaskCacheObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBJobTaskCacheObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBJobTaskCacheObservation{Profile: profile, Failure: observeDBJobTaskCache(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBJobTaskCacheObservation](
			"the job task-cache behavior exactly matches {string}",
			func(in DBJobTaskCacheObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the job task-cache behavior exactly matches {string}", p, 0)
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

func observeDBJobTaskCache(database JetbridgeDB, profile string) string {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "task-cache-team"})
	if err != nil {
		return err.Error()
	}
	job, _, err := saveJobForStrictTeam(team, "task-cache-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}, {Name: "other-job"}}}, "job")
	if err != nil {
		return err.Error()
	}
	caches := db.NewTaskCacheFactory(database.Conn)
	if _, err := caches.FindOrCreate(job.ID(), "compile", "cache-path"); err != nil {
		return err.Error()
	}

	path := "cache-path"
	if profile == "step-row-count" || profile == "step-removes-cache" {
		path = ""
	}
	rows, err := job.ClearTaskCache("compile", path)
	if err != nil {
		return err.Error()
	}
	if profile == "path-row-count" || profile == "step-row-count" {
		if rows != 1 {
			return fmt.Sprintf("deleted rows got %d, want 1", rows)
		}
		return ""
	}
	cache, found, err := caches.Find(job.ID(), "compile", "cache-path")
	if err != nil {
		return err.Error()
	}
	if found || cache != nil {
		return fmt.Sprintf("cache remained: found=%t cache=%v", found, cache)
	}
	return ""
}
