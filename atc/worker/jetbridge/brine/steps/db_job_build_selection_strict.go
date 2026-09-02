package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBJobBuildSelectionObservation struct {
	Profile string
	Failure string
}

func DBJobBuildSelectionStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBJobBuildSelectionObservation](
			"the production job build selection behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBJobBuildSelectionObservation, error) {
				profile, err := paramAt("the production job build selection behavior {string} is exercised", p, 0)
				if err != nil {
					return DBJobBuildSelectionObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBJobBuildSelectionObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBJobBuildSelectionObservation{Profile: profile, Failure: observeDBJobBuildSelection(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBJobBuildSelectionObservation](
			"the job build selection behavior exactly matches {string}",
			func(in DBJobBuildSelectionObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the job build selection behavior exactly matches {string}", p, 0)
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

func observeDBJobBuildSelection(database JetbridgeDB, profile string) string {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "build-selection-team"})
	if err != nil {
		return err.Error()
	}
	job, _, err := saveJobForStrictTeam(team, "build-selection-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, "job")
	if err != nil {
		return err.Error()
	}
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }

	switch profile {
	case "finished-and-next":
		finished, next, err := job.FinishedAndNextBuild()
		if err != nil {
			return err.Error()
		}
		if finished != nil || next != nil {
			return fail("empty job returned finished=%v next=%v", finished, next)
		}
		completed, err := job.CreateBuild("strict migration")
		if err != nil {
			return err.Error()
		}
		if err := completed.Finish(db.BuildStatusSucceeded); err != nil {
			return err.Error()
		}
		running, err := job.CreateBuild("strict migration")
		if err != nil {
			return err.Error()
		}
		started, err := running.Start(atc.Plan{})
		if err != nil || !started {
			return fail("start running build: started=%t err=%v", started, err)
		}
		finished, next, err = job.FinishedAndNextBuild()
		if err != nil {
			return err.Error()
		}
		if finished == nil || next == nil || finished.ID() != completed.ID() || next.ID() != running.ID() {
			return fail("selection finished=%v next=%v want-finished=%d want-next=%d", buildSelectionID(finished), buildSelectionID(next), completed.ID(), running.ID())
		}
	case "chronological":
		first, err := job.CreateBuild("strict migration")
		if err != nil {
			return err.Error()
		}
		second, err := job.CreateBuild("strict migration")
		if err != nil {
			return err.Error()
		}
		rerun, err := job.RerunBuild(first, "strict migration")
		if err != nil {
			return err.Error()
		}
		builds, _, err := job.ChronoBuilds(db.Page{Limit: 3})
		if err != nil {
			return err.Error()
		}
		if len(builds) != 3 || builds[0].ID() != rerun.ID() || builds[1].ID() != second.ID() || builds[2].ID() != first.ID() {
			return fail("chronological ids=%v want=%v", buildSelectionIDs(builds), []int{rerun.ID(), second.ID(), first.ID()})
		}
	default:
		return fail("unknown job build selection profile %q", profile)
	}
	return ""
}

func buildSelectionID(build db.Build) any {
	if build == nil {
		return nil
	}
	return build.ID()
}

func buildSelectionIDs(builds []db.BuildForAPI) []int {
	ids := make([]int, len(builds))
	for i := range builds {
		ids[i] = builds[i].ID()
	}
	return ids
}
