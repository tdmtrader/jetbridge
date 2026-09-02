package steps

import (
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
)

type DBPipelineQueryObservation struct {
	Profile string
	Failure string
}

func DBPipelineQueryStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBPipelineQueryObservation](
			"the production pipeline query behavior {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBPipelineQueryObservation, error) {
				profile, err := paramAt("the production pipeline query behavior {string} is exercised", p, 0)
				if err != nil {
					return DBPipelineQueryObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBPipelineQueryObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBPipelineQueryObservation{Profile: profile, Failure: observeDBPipelineQuery(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBPipelineQueryObservation](
			"the pipeline query behavior exactly matches {string}",
			func(in DBPipelineQueryObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the pipeline query behavior exactly matches {string}", p, 0)
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

func observeDBPipelineQuery(database JetbridgeDB, profile string) string {
	fail := func(format string, args ...any) string { return fmt.Sprintf(format, args...) }
	team, err := database.TeamFactory.CreateTeam(atc.Team{Name: "strict-pipeline-query-team"})
	if err != nil {
		return err.Error()
	}

	switch profile {
	case "version-enabled", "version-disabled":
		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "strict-version-pipeline"}, atc.Config{
			Resources: atc.ResourceConfigs{{Name: "resource", Type: "type", Source: atc.Source{"key": "value"}}},
		}, 1, false)
		if err != nil {
			return err.Error()
		}
		scenario := &dbtest.Scenario{Team: team, Pipeline: pipeline}
		if err := database.Builder.WithResourceVersions("resource", atc.Version{"version": "1"})(scenario); err != nil {
			return err.Error()
		}
		resource, found, err := pipeline.Resource("resource")
		if err != nil || !found {
			return fail("resource lookup found=%t err=%v", found, err)
		}
		resourceVersion, found, err := resource.FindVersion(atc.Version{"version": "1"})
		if err != nil || !found {
			return fail("version lookup found=%t err=%v", found, err)
		}
		if profile == "version-disabled" {
			if err := resource.DisableVersion(resourceVersion.ID()); err != nil {
				return err.Error()
			}
		}
		got, found, err := pipeline.ResourceVersion(resourceVersion.ID())
		wantEnabled := profile == "version-enabled"
		want := atc.ResourceVersion{Version: atc.Version{"version": "1"}, ID: resourceVersion.ID(), Enabled: wantEnabled}
		if err != nil || !found || !reflect.DeepEqual(got, want) {
			return fail("version found=%t got=%#v want=%#v err=%v", found, got, want, err)
		}
	case "time-none", "time-limit", "time-to", "time-from", "time-range":
		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "strict-time-pipeline"}, atc.Config{
			Jobs: atc.JobConfigs{{Name: "job"}, {Name: "other-job"}},
		}, 1, false)
		if err != nil {
			return err.Error()
		}
		job, found, err := pipeline.Job("job")
		if err != nil || !found {
			return fail("job lookup found=%t err=%v", found, err)
		}
		builds := make([]db.Build, 4)
		for i := range builds {
			build, err := job.CreateBuild("strict pipeline query")
			if err != nil {
				return err.Error()
			}
			start := time.Date(2020, 11, i+1, 0, 0, 0, 0, time.UTC)
			if _, err := database.Conn.Exec("UPDATE builds SET start_time = to_timestamp($1) WHERE id = $2", start.Unix(), build.ID()); err != nil {
				return err.Error()
			}
			builds[i], found, err = job.Build(build.Name())
			if err != nil || !found {
				return fail("build %d reload found=%t err=%v", i, found, err)
			}
		}
		otherPipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "strict-other-time-pipeline"}, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 1, false)
		if err != nil {
			return err.Error()
		}
		otherJob, found, err := otherPipeline.Job("job")
		if err != nil || !found {
			return fail("other job lookup found=%t err=%v", found, err)
		}
		if _, err := otherJob.CreateBuild("strict pipeline query"); err != nil {
			return err.Error()
		}

		var page db.Page
		var want []int
		switch profile {
		case "time-none":
			page = db.Page{}
			want = []int{}
		case "time-limit":
			page = db.Page{Limit: 2}
			want = []int{builds[2].ID(), builds[3].ID()}
		case "time-to":
			page = db.Page{To: db.NewIntPtr(int(builds[2].StartTime().Unix())), Limit: 50}
			want = []int{builds[0].ID(), builds[1].ID(), builds[2].ID()}
		case "time-from":
			page = db.Page{From: db.NewIntPtr(int(builds[1].StartTime().Unix())), Limit: 50}
			want = []int{builds[1].ID(), builds[2].ID(), builds[3].ID()}
		case "time-range":
			page = db.Page{From: db.NewIntPtr(int(builds[1].StartTime().Unix())), To: db.NewIntPtr(int(builds[2].StartTime().Unix())), Limit: 50}
			want = []int{builds[1].ID(), builds[2].ID()}
		}
		got, _, err := pipeline.BuildsWithTime(page)
		if err != nil {
			return err.Error()
		}
		gotIDs := make([]int, len(got))
		for i := range got {
			gotIDs[i] = got[i].ID()
		}
		sort.Ints(gotIDs)
		sort.Ints(want)
		if !reflect.DeepEqual(gotIDs, want) {
			return fail("build ids got=%v want=%v", gotIDs, want)
		}
	case "parent-fields", "parent-invalid", "parent-newer", "parent-null":
		pipeline, _, err := team.SavePipeline(atc.PipelineRef{Name: "strict-parent-pipeline"}, atc.Config{}, 1, false)
		if err != nil {
			return err.Error()
		}
		switch profile {
		case "parent-fields":
			if err := pipeline.SetParentIDs(123, 456); err != nil {
				return err.Error()
			}
			found, err := pipeline.Reload()
			if err != nil || !found || pipeline.ParentJobID() != 123 || pipeline.ParentBuildID() != 456 {
				return fail("parent fields found=%t job=%d build=%d err=%v", found, pipeline.ParentJobID(), pipeline.ParentBuildID(), err)
			}
		case "parent-invalid":
			want := "job and build id cannot be negative or zero-value"
			if err := pipeline.SetParentIDs(0, 0); err == nil || err.Error() != want {
				return fail("zero ids error=%v want=%q", err, want)
			}
			if err := pipeline.SetParentIDs(-1, -6); err == nil || err.Error() != want {
				return fail("negative ids error=%v want=%q", err, want)
			}
		case "parent-newer":
			if err := pipeline.SetParentIDs(1, 60); err != nil {
				return err.Error()
			}
			if err := pipeline.SetParentIDs(1, 2); err != db.ErrSetByNewerBuild {
				return fail("older update error=%v want=%v", err, db.ErrSetByNewerBuild)
			}
		case "parent-null":
			if pipeline.ParentJobID() != 0 || pipeline.ParentBuildID() != 0 {
				return fail("initial parent ids job=%d build=%d", pipeline.ParentJobID(), pipeline.ParentBuildID())
			}
			if err := pipeline.SetParentIDs(1, 6); err != nil {
				return err.Error()
			}
			found, err := pipeline.Reload()
			if err != nil || !found || pipeline.ParentJobID() != 1 || pipeline.ParentBuildID() != 6 {
				return fail("updated parent ids found=%t job=%d build=%d err=%v", found, pipeline.ParentJobID(), pipeline.ParentBuildID(), err)
			}
		}
	default:
		return fail("unknown pipeline query profile %q", profile)
	}
	return ""
}
