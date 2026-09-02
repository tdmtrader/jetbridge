package steps

import (
	"fmt"
	"reflect"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type DBPipelineFactoryObservation struct {
	Profile string
	Failure string
}

func DBPipelineFactoryStrictDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMapUsing[brine.Empty, DBPipelineFactoryObservation](
			"the production pipeline factory profile {string} is exercised",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, resources brine.Resources) (DBPipelineFactoryObservation, error) {
				profile, err := paramAt("the production pipeline factory profile {string} is exercised", p, 0)
				if err != nil {
					return DBPipelineFactoryObservation{}, err
				}
				database, ok := resources.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return DBPipelineFactoryObservation{}, fmt.Errorf("jetbridge-db resource is %T", resources.Get("jetbridge-db"))
				}
				return DBPipelineFactoryObservation{Profile: profile, Failure: observeDBPipelineFactory(database, profile)}, nil
			},
		),
		brine.DefineCheck[DBPipelineFactoryObservation](
			"the pipeline factory observation exactly matches {string}",
			func(in DBPipelineFactoryObservation, p brine.Params, _ *brine.Recorder) error {
				profile, err := paramAt("the pipeline factory observation exactly matches {string}", p, 0)
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

func observeDBPipelineFactory(database JetbridgeDB, profile string) string {
	factory := db.NewPipelineFactory(database.Conn, database.LockFactory)

	mainTeam, err := database.TeamFactory.CreateTeam(atc.Team{Name: "main"})
	if err != nil {
		return err.Error()
	}
	otherTeam, err := database.TeamFactory.CreateTeam(atc.Team{Name: "some-team"})
	if err != nil {
		return err.Error()
	}

	save := func(team db.Team, ref atc.PipelineRef, public bool) (db.Pipeline, error) {
		pipeline, _, err := team.SavePipeline(ref, atc.Config{Jobs: atc.JobConfigs{{Name: "job"}}}, 1, false)
		if err != nil {
			return nil, err
		}
		if public {
			if err := pipeline.Expose(); err != nil {
				return nil, err
			}
		}
		if _, err := pipeline.Reload(); err != nil {
			return nil, err
		}
		return pipeline, nil
	}

	refs := func(pipelines []db.Pipeline) []atc.PipelineRef {
		result := make([]atc.PipelineRef, len(pipelines))
		for i, pipeline := range pipelines {
			result[i] = atc.PipelineRef{Name: pipeline.Name(), InstanceVars: pipeline.InstanceVars()}
		}
		return result
	}
	check := func(got []db.Pipeline, want []atc.PipelineRef) string {
		gotRefs := refs(got)
		if !reflect.DeepEqual(gotRefs, want) {
			return fmt.Sprintf("pipeline refs got=%#v, want=%#v", gotRefs, want)
		}
		return ""
	}

	if profile == "all-default" || profile == "all-reordered" {
		p2, err := save(otherTeam, atc.PipelineRef{Name: "pipeline-two"}, false)
		if err != nil {
			return err.Error()
		}
		p3, err := save(otherTeam, atc.PipelineRef{Name: "pipeline-three"}, true)
		if err != nil {
			return err.Error()
		}
		p1, err := save(mainTeam, atc.PipelineRef{Name: "pipeline"}, true)
		if err != nil {
			return err.Error()
		}
		p4, err := save(otherTeam, atc.PipelineRef{Name: "pipeline-two", InstanceVars: atc.InstanceVars{"branch": "master"}}, false)
		if err != nil {
			return err.Error()
		}
		want := []atc.PipelineRef{
			{Name: p1.Name(), InstanceVars: p1.InstanceVars()},
			{Name: p2.Name(), InstanceVars: p2.InstanceVars()},
			{Name: p4.Name(), InstanceVars: p4.InstanceVars()},
			{Name: p3.Name(), InstanceVars: p3.InstanceVars()},
		}
		if profile == "all-reordered" {
			if err := otherTeam.OrderPipelinesWithinGroup("pipeline-two", []atc.InstanceVars{{"branch": "master"}, {}}); err != nil {
				return err.Error()
			}
			want[1], want[2] = want[2], want[1]
		}
		pipelines, err := factory.AllPipelines()
		if err != nil {
			return err.Error()
		}
		return check(pipelines, want)
	}

	p1, err := save(otherTeam, atc.PipelineRef{Name: "pipeline"}, false)
	if err != nil {
		return err.Error()
	}
	_, err = save(mainTeam, atc.PipelineRef{Name: "pipeline-two"}, false)
	if err != nil {
		return err.Error()
	}
	p3, err := save(mainTeam, atc.PipelineRef{Name: "pipeline-three"}, true)
	if err != nil {
		return err.Error()
	}
	p4, err := save(otherTeam, atc.PipelineRef{Name: "pipeline", InstanceVars: atc.InstanceVars{"branch": "master"}}, false)
	if err != nil {
		return err.Error()
	}

	var teamNames []string
	want := []atc.PipelineRef{{Name: p3.Name(), InstanceVars: p3.InstanceVars()}}
	switch profile {
	case "visible-team":
		teamNames = []string{"some-team"}
		want = []atc.PipelineRef{
			{Name: p1.Name(), InstanceVars: p1.InstanceVars()},
			{Name: p4.Name(), InstanceVars: p4.InstanceVars()},
			{Name: p3.Name(), InstanceVars: p3.InstanceVars()},
		}
	case "visible-empty-name":
		teamNames = []string{""}
	case "visible-empty":
		teamNames = []string{}
	case "visible-nil":
		teamNames = nil
	case "visible-reordered":
		teamNames = []string{"some-team"}
		if err := otherTeam.OrderPipelinesWithinGroup("pipeline", []atc.InstanceVars{{"branch": "master"}, {}}); err != nil {
			return err.Error()
		}
		want = []atc.PipelineRef{
			{Name: p4.Name(), InstanceVars: p4.InstanceVars()},
			{Name: p1.Name(), InstanceVars: p1.InstanceVars()},
			{Name: p3.Name(), InstanceVars: p3.InstanceVars()},
		}
	default:
		return fmt.Sprintf("unknown pipeline factory profile %q", profile)
	}
	pipelines, err := factory.VisiblePipelines(teamNames)
	if err != nil {
		return err.Error()
	}
	return check(pipelines, want)
}
