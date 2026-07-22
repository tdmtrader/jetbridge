package workflowrun

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func TestTemplateSaverCreatesWithCreateOnlyVersion(t *testing.T) {
	spec := templateSaverSpec(t)
	pipeline := exactTemplatePipeline(spec, 81, 7, 13)
	team := new(dbfakes.FakeTeam)
	team.IDReturns(7)
	team.NameReturns("research")
	team.PipelineReturns(nil, false, nil)
	team.PipelineReturnsOnCall(1, pipeline, true, nil)
	team.SavePipelineReturns(pipeline, true, nil)
	finder := &teamFinderStub{find: func(name string) (db.Team, bool, error) {
		if name != "research" {
			t.Fatalf("team lookup = %q", name)
		}
		return team, true, nil
	}}
	saver, err := NewTemplateSaver(finder)
	if err != nil {
		t.Fatalf("NewTemplateSaver: %v", err)
	}

	ref, err := saver.SaveOrReuse(context.Background(), AdmissionContext{
		TeamID: 7, TeamName: "research", CreatedBy: "alice", Origin: Origin{Kind: "manual"},
	}, spec)
	if err != nil {
		t.Fatalf("SaveOrReuse: %v", err)
	}
	if ref.PipelineID != 81 || ref.TeamID != 7 || ref.Name != spec.Name || ref.ConfigVersion != 13 || ref.FullHash != spec.FullHash {
		t.Fatalf("ref = %+v", ref)
	}
	if team.SavePipelineCallCount() != 1 {
		t.Fatalf("SavePipeline calls = %d", team.SavePipelineCallCount())
	}
	gotRef, gotConfig, from, paused := team.SavePipelineArgsForCall(0)
	if gotRef.Name != spec.Name || gotRef.InstanceVars != nil || from != db.ConfigVersion(0) || paused || !configsEqual(gotConfig, spec.Config) {
		t.Fatalf("SavePipeline args = (%+v, %+v, %d, %t)", gotRef, gotConfig, from, paused)
	}
}

func TestTemplateSaverReusesOnlyAnExactImmutableTemplate(t *testing.T) {
	spec := templateSaverSpec(t)
	pipeline := exactTemplatePipeline(spec, 81, 7, 13)
	team := new(dbfakes.FakeTeam)
	team.IDReturns(7)
	team.NameReturns("research")
	team.PipelineReturns(pipeline, true, nil)
	saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) {
		return team, true, nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	ref, err := saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, spec)
	if err != nil {
		t.Fatalf("SaveOrReuse: %v", err)
	}
	if ref.PipelineID != pipeline.ID() || team.SavePipelineCallCount() != 0 {
		t.Fatalf("reuse = %+v, save calls = %d", ref, team.SavePipelineCallCount())
	}
}

func TestTemplateSaverRejectsEveryMutableOrCollidingShape(t *testing.T) {
	spec := templateSaverSpec(t)
	tests := []struct {
		name   string
		mutate func(*dbfakes.FakePipeline)
	}{
		{name: "ordinary", mutate: func(p *dbfakes.FakePipeline) { p.TemplateReturns(false) }},
		{name: "instance", mutate: func(p *dbfakes.FakePipeline) { p.InstanceVarsReturns(atc.InstanceVars{"run": 1}) }},
		{name: "archived", mutate: func(p *dbfakes.FakePipeline) { p.ArchivedReturns(true) }},
		{name: "wrong team", mutate: func(p *dbfakes.FakePipeline) { p.TeamIDReturns(8) }},
		{name: "wrong name", mutate: func(p *dbfakes.FakePipeline) { p.NameReturns(spec.Name + "-other") }},
		{name: "hash or bytes", mutate: func(p *dbfakes.FakePipeline) {
			p.ConfigReturns(atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "different"}}}, nil)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pipeline := exactTemplatePipeline(spec, 81, 7, 13)
			test.mutate(pipeline)
			team := new(dbfakes.FakeTeam)
			team.IDReturns(7)
			team.NameReturns("research")
			team.PipelineReturns(pipeline, true, nil)
			saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) {
				return team, true, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, spec)
			if !errors.Is(err, ErrImmutableTemplateCollision) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTemplateSaverConvergesWithConcurrentExactCreator(t *testing.T) {
	spec := templateSaverSpec(t)
	pipeline := exactTemplatePipeline(spec, 81, 7, 13)
	team := new(dbfakes.FakeTeam)
	team.IDReturns(7)
	team.NameReturns("research")
	team.PipelineReturns(nil, false, nil)
	team.PipelineReturnsOnCall(1, pipeline, true, nil)
	team.PipelineReturnsOnCall(2, pipeline, true, nil)
	team.SavePipelineReturns(nil, false, db.ErrConfigComparisonFailed)
	saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) {
		return team, true, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, spec)
	if err != nil {
		t.Fatalf("SaveOrReuse: %v", err)
	}
	if ref.PipelineID != 81 || team.SavePipelineCallCount() != 1 {
		t.Fatalf("ref = %+v, save calls = %d", ref, team.SavePipelineCallCount())
	}
}

func TestTemplateSaverRetriesVersionDriftAndBoundsPersistentMutation(t *testing.T) {
	spec := templateSaverSpec(t)
	t.Run("converges", func(t *testing.T) {
		version10 := exactTemplatePipeline(spec, 81, 7, 10)
		version11 := exactTemplatePipeline(spec, 81, 7, 11)
		team := new(dbfakes.FakeTeam)
		team.IDReturns(7)
		team.NameReturns("research")
		team.PipelineReturns(version11, true, nil)
		team.PipelineReturnsOnCall(0, version10, true, nil)
		team.PipelineReturnsOnCall(1, version11, true, nil)
		saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) {
			return team, true, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		ref, err := saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, spec)
		if err != nil {
			t.Fatalf("SaveOrReuse: %v", err)
		}
		if ref.ConfigVersion != 11 {
			t.Fatalf("config version = %d", ref.ConfigVersion)
		}
	})

	t.Run("persistent drift", func(t *testing.T) {
		team := new(dbfakes.FakeTeam)
		team.IDReturns(7)
		team.NameReturns("research")
		call := 0
		team.PipelineStub = func(atc.PipelineRef) (db.Pipeline, bool, error) {
			call++
			return exactTemplatePipeline(spec, 81, 7, call), true, nil
		}
		saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) {
			return team, true, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, spec)
		if !errors.Is(err, ErrImmutableTemplateCollision) {
			t.Fatalf("error = %v", err)
		}
		if call > 1+templateVersionReadAttempts*2 {
			t.Fatalf("version reads were not bounded: %d", call)
		}
	})
}

func TestTemplateSaverRequiresAuthoritativeTeamIdentity(t *testing.T) {
	spec := templateSaverSpec(t)
	for _, test := range []struct {
		name     string
		teamID   int
		teamName string
		found    bool
	}{
		{name: "missing", found: false},
		{name: "wrong id", teamID: 8, teamName: "research", found: true},
		{name: "renamed", teamID: 7, teamName: "renamed", found: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			team := new(dbfakes.FakeTeam)
			team.IDReturns(test.teamID)
			team.NameReturns(test.teamName)
			saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) {
				return team, test.found, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, spec)
			if !errors.Is(err, ErrImmutableTemplateCollision) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func templateSaverSpec(t *testing.T) ImmutableTemplateSpec {
	t.Helper()
	config := atc.Config{
		Template: true,
		Params:   []atc.ParamSchema{{Name: "workflow_run_id", Type: "string", Required: true}},
		Jobs:     atc.JobConfigs{{Name: "run"}},
	}
	hash, err := workflow.TargetConfigHash(config)
	if err != nil {
		t.Fatal(err)
	}
	name, err := workflow.TemplateName(workflow.TargetWorkflow, "review-flow", 3, "", hash)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return ImmutableTemplateSpec{Name: name, FullHash: hash, CanonicalJSON: canonical, Config: config}
}

func exactTemplatePipeline(spec ImmutableTemplateSpec, id, teamID, version int) *dbfakes.FakePipeline {
	pipeline := new(dbfakes.FakePipeline)
	pipeline.IDReturns(id)
	pipeline.TeamIDReturns(teamID)
	pipeline.NameReturns(spec.Name)
	pipeline.ConfigVersionReturns(db.ConfigVersion(version))
	pipeline.TemplateReturns(true)
	pipeline.InstanceVarsReturns(nil)
	pipeline.ArchivedReturns(false)
	pipeline.ConfigReturns(spec.Config, nil)
	return pipeline
}

func configsEqual(left, right atc.Config) bool {
	leftBytes, leftErr := left.CanonicalJSON()
	rightBytes, rightErr := right.CanonicalJSON()
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

type teamFinderStub struct {
	find func(string) (db.Team, bool, error)
}

func (s *teamFinderStub) FindTeam(name string) (db.Team, bool, error) { return s.find(name) }
