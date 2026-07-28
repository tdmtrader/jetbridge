package workflowrun

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func TestTemplateSaverValidationAuthorityPreventsConfigOnlyReuse(t *testing.T) {
	first := validationTemplateSpec(t, "one")
	second := validationTemplateSpec(t, "two")
	if first.FullHash == second.FullHash || first.Name == second.Name {
		t.Fatal("different authority converged on one template identity")
	}
	second.Name = first.Name // model an attempted collision at the same public ATC config.
	pipeline := exactTemplatePipeline(first, 81, 7, 13)
	team := new(dbfakes.FakeTeam)
	team.IDReturns(7)
	team.NameReturns("research")
	team.PipelineReturns(pipeline, true, nil)
	saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) { return team, true, nil }}, alwaysOwnedTemplateStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, second); !errors.Is(err, ErrImmutableTemplateCollision) {
		t.Fatalf("error = %v, want collision", err)
	}
}

func TestTemplateSaverCreatesWithCreateOnlyVersion(t *testing.T) {
	spec := templateSaverSpec(t)
	pipeline := exactTemplatePipeline(spec, 81, 7, 13)
	team := new(dbfakes.FakeTeam)
	team.IDReturns(7)
	team.NameReturns("research")
	team.PipelineReturns(nil, false, nil)
	team.PipelineReturnsOnCall(1, pipeline, true, nil)
	store := &templateStoreStub{save: func(ctx context.Context, teamID int, ref atc.PipelineRef, config atc.Config) (db.Pipeline, bool, error) {
		if ctx == nil || teamID != 7 || ref.Name != spec.Name || ref.InstanceVars != nil || !configsEqual(config, spec.Config) {
			t.Fatalf("Save args = (%v, %d, %+v, %+v)", ctx, teamID, ref, config)
		}
		return pipeline, true, nil
	}, owns: func(context.Context, int) (bool, error) { return true, nil }}
	finder := &teamFinderStub{find: func(name string) (db.Team, bool, error) {
		if name != "research" {
			t.Fatalf("team lookup = %q", name)
		}
		return team, true, nil
	}}
	saver, err := NewTemplateSaver(finder, store)
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
	if store.saveCalls != 1 {
		t.Fatalf("Save calls = %d", store.saveCalls)
	}
}

func TestTemplateSaverReusesOnlyAnExactImmutableTemplate(t *testing.T) {
	spec := templateSaverSpec(t)
	pipeline := exactTemplatePipeline(spec, 81, 7, 13)
	team := new(dbfakes.FakeTeam)
	team.IDReturns(7)
	team.NameReturns("research")
	team.PipelineReturns(pipeline, true, nil)
	store := alwaysOwnedTemplateStore()
	saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) {
		return team, true, nil
	}}, store)
	if err != nil {
		t.Fatal(err)
	}

	ref, err := saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, spec)
	if err != nil {
		t.Fatalf("SaveOrReuse: %v", err)
	}
	if ref.PipelineID != pipeline.ID() || store.saveCalls != 0 {
		t.Fatalf("reuse = %+v, save calls = %d", ref, store.saveCalls)
	}
}

func TestTemplateSaverRejectsAnExactButUnownedPipeline(t *testing.T) {
	spec := templateSaverSpec(t)
	pipeline := exactTemplatePipeline(spec, 81, 7, 13)
	team := new(dbfakes.FakeTeam)
	team.IDReturns(7)
	team.NameReturns("research")
	team.PipelineReturns(pipeline, true, nil)
	store := &templateStoreStub{owns: func(context.Context, int) (bool, error) { return false, nil }}
	saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) {
		return team, true, nil
	}}, store)
	if err != nil {
		t.Fatal(err)
	}

	_, err = saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, spec)
	if !errors.Is(err, ErrImmutableTemplateCollision) {
		t.Fatalf("error = %v", err)
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
			}}, alwaysOwnedTemplateStore())
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
	store := alwaysOwnedTemplateStore()
	store.save = func(context.Context, int, atc.PipelineRef, atc.Config) (db.Pipeline, bool, error) {
		return nil, false, db.ErrConfigComparisonFailed
	}
	saver, err := NewTemplateSaver(&teamFinderStub{find: func(string) (db.Team, bool, error) {
		return team, true, nil
	}}, store)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := saver.SaveOrReuse(context.Background(), AdmissionContext{TeamID: 7, TeamName: "research"}, spec)
	if err != nil {
		t.Fatalf("SaveOrReuse: %v", err)
	}
	if ref.PipelineID != 81 || store.saveCalls != 1 {
		t.Fatalf("ref = %+v, save calls = %d", ref, store.saveCalls)
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
		}}, alwaysOwnedTemplateStore())
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
		}}, alwaysOwnedTemplateStore())
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
			}}, alwaysOwnedTemplateStore())
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

func TestTemplateSaverRequiresAnAuthoritativeTemplateStore(t *testing.T) {
	finder := &teamFinderStub{find: func(string) (db.Team, bool, error) { return nil, false, nil }}
	for _, store := range []WorkflowRunTemplateStore{nil, (*templateStoreStub)(nil)} {
		_, err := NewTemplateSaver(finder, store)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("error = %v", err)
		}
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

func validationTemplateSpec(t *testing.T, value string) ImmutableTemplateSpec {
	t.Helper()
	spec := templateSaverSpec(t)
	profile := []byte("schema_version: 1\nname: check\nchecks: []\n# " + value + "\n")
	config := []byte("schema_version: 1\ncomponents: []\n# " + value + "\n")
	profiles := []workflow.CompiledDevValidationProfile{{Name: "check", Candidate: workflow.DevValidationContract{Name: "candidate", Type: "opaque/v1"}, CapabilityImage: "registry.example/dev-mcp@sha256:" + strings.Repeat("a", 64), CapabilityImageDigest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)), Command: []string{"/usr/local/bin/dev-capability", "validate"}, Profile: profile, ProfileDigest: snapshot.Digest("sha256:" + workflow.Hash(profile)), ProtectedConfig: config, ProtectedConfigDigest: snapshot.Digest("sha256:" + workflow.Hash(config))}}
	provenance, err := workflow.DevValidationProvenanceHash(profiles)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := workflow.RenderedTargetConfigHash(spec.Config, profiles, provenance)
	if err != nil {
		t.Fatal(err)
	}
	name, err := workflow.TemplateName(workflow.TargetWorkflow, "review-flow", 3, "", hash)
	if err != nil {
		t.Fatal(err)
	}
	spec.Name, spec.FullHash, spec.DevValidationProfiles, spec.DevValidationProvenanceHash = name, hash, profiles, provenance
	return spec
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

type templateStoreStub struct {
	save      func(context.Context, int, atc.PipelineRef, atc.Config) (db.Pipeline, bool, error)
	owns      func(context.Context, int) (bool, error)
	saveCalls int
}

func (s *templateStoreStub) SaveWorkflowRunTemplate(ctx context.Context, teamID int, ref atc.PipelineRef, config atc.Config) (db.Pipeline, bool, error) {
	s.saveCalls++
	if s.save == nil {
		return nil, false, errors.New("unexpected SaveWorkflowRunTemplate")
	}
	return s.save(ctx, teamID, ref, config)
}

func (s *templateStoreStub) IsWorkflowRunTemplate(ctx context.Context, pipelineID int) (bool, error) {
	if s.owns == nil {
		return false, errors.New("unexpected IsWorkflowRunTemplate")
	}
	return s.owns(ctx, pipelineID)
}

func alwaysOwnedTemplateStore() *templateStoreStub {
	return &templateStoreStub{owns: func(context.Context, int) (bool, error) { return true, nil }}
}
