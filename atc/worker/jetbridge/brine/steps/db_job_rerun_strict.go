package steps

import (
	"fmt"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
)

type DBJobRerunObservation struct {
	Profile string
	Failure string
}

func DBJobRerunStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBJobRerunObservation](
			"the production job rerun behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBJobRerunObservation, error) {
				profile, err := paramAt("the production job rerun behavior {string} is exercised", p, 0)
				if err != nil {
					return DBJobRerunObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBJobRerunObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBJobRerunObservation{Profile: profile, Failure: observeDBJobRerun(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBJobRerunObservation](
			"the job rerun behavior exactly matches {string}",
			func(in DBJobRerunObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the job rerun behavior exactly matches {string}", p, 0)
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

func observeDBJobRerun(database JetbridgeDB, profile string) string {
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "rerun-team"})
	if err != nil {
		return err.Error()
	}
	job, _, err := saveJobForStrictTeam(team, "rerun-pipeline", atc.Config{Jobs: atc.JobConfigs{{Name: "job"}, {Name: "schedule-neighbor"}}}, "job")
	if err != nil {
		return err.Error()
	}
	first, err := job.CreateBuild("strict migration")
	if err != nil {
		return err.Error()
	}
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }

	switch profile {
	case "persisted":
		rerun, err := job.RerunBuild(first, "strict migration")
		if err != nil {
			return err.Error()
		}
		foundBuild, found, err := job.Build(first.Name() + ".1")
		if err != nil {
			return err.Error()
		}
		if rerun.Name() != first.Name()+".1" || rerun.RerunNumber() != 1 || !found || foundBuild.ID() != rerun.ID() || foundBuild.Status() != rerun.Status() {
			return fail("rerun name=%q number=%d found=%t persisted-id=%d rerun-id=%d", rerun.Name(), rerun.RerunNumber(), found, foundBuild.ID(), rerun.ID())
		}
	case "requests-schedule":
		before := job.ScheduleRequestedTime()
		if _, err := job.RerunBuild(first, "strict migration"); err != nil {
			return err.Error()
		}
		found, err := job.Reload()
		if err != nil || !found {
			return fail("reload found=%t err=%v", found, err)
		}
		if !job.ScheduleRequestedTime().After(before) {
			return fail("schedule request did not advance: before=%s after=%s", before, job.ScheduleRequestedTime())
		}
	case "increments":
		one, err := job.RerunBuild(first, "strict migration")
		if err != nil {
			return err.Error()
		}
		if one.Name() != first.Name()+".1" || one.RerunNumber() != 1 {
			return fail("first rerun name=%q number=%d", one.Name(), one.RerunNumber())
		}
		two, err := job.RerunBuild(first, "strict migration")
		if err != nil {
			return err.Error()
		}
		if two.Name() != first.Name()+".2" || two.RerunNumber() != 2 {
			return fail("second rerun name=%q number=%d", two.Name(), two.RerunNumber())
		}
	case "rerun-of-rerun":
		one, err := job.RerunBuild(first, "strict migration")
		if err != nil {
			return err.Error()
		}
		if one.Name() != first.Name()+".1" || one.RerunNumber() != 1 {
			return fail("first rerun name=%q number=%d", one.Name(), one.RerunNumber())
		}
		two, err := job.RerunBuild(one, "strict migration")
		if err != nil {
			return err.Error()
		}
		if two.Name() != first.Name()+".2" || two.RerunNumber() != 2 || two.RerunOf() != first.ID() {
			return fail("rerun-of-rerun name=%q number=%d original=%d want=%d", two.Name(), two.RerunNumber(), two.RerunOf(), first.ID())
		}
	default:
		return fail("unknown job rerun profile %q", profile)
	}
	return ""
}
