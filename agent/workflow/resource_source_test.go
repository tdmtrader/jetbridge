package workflow

import (
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

// Removing the source declaration or replacing its VersionConfig with a
// bespoke selector must make this test fail: workflow admission accepts only
// ordinary Concourse version selection.
func TestResourceSourceDeclarationUsesOrdinaryConcourseSelection(t *testing.T) {
	definition, err := CompileDefinition(Manifest{"workflow.yaml": `schema_version: 3
name: selected-repository
signature_version: 1
inputs: []
outputs: []
resources:
  - {name: repository, type: git, source: {uri: https://example.invalid/repository.git}}
resource_sources:
  - {name: repository-source, resource: repository, type: repository/v1, trigger: true, version: {ref: deadbeef}}
plan:
  - agent: inspect
    function_id: inspect
    prompt: inspect
    inputs: [repository-source]
    input_types: {repository-source: {type: repository/v1}}
`})
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	sources := definition.Function.ResourceSources
	if len(sources) != 1 || !sources[0].Trigger || sources[0].Version == nil || sources[0].Version.Pinned["ref"] != "deadbeef" {
		t.Fatalf("resource source = %#v, want preserved ordinary pinned selection", sources)
	}
}

func TestResourceSourceRejectsPassedConstraints(t *testing.T) {
	_, err := CompileDefinition(Manifest{"workflow.yaml": `schema_version: 3
name: selected-repository
signature_version: 1
inputs: []
outputs: []
resources: [{name: repository, type: git, source: {uri: https://example.invalid/repository.git}}]
resource_sources: [{name: repository-source, resource: repository, type: repository/v1, passed: [other]}]
plan: [{agent: inspect, function_id: inspect, prompt: inspect}]
`})
	if err == nil || !strings.Contains(err.Error(), "resource sources do not support passed constraints") {
		t.Fatalf("error = %v", err)
	}
}

func TestRenderResourceSourcePipelinePreservesOrdinaryGets(t *testing.T) {
	target := ResourceSourcePipelineTarget{
		TeamID: 7, WorkflowDefinitionID: 11, WorkflowName: "selected-repository", WorkflowVersion: 2,
		Sources:   []ResourceSource{{Name: "repository-source", Resource: "repository", Type: snapshot.TypeRef("repository/v1"), Trigger: true, Version: &atc.VersionConfig{Pinned: atc.Version{"ref": "deadbeef"}}}},
		Resources: atc.ResourceConfigs{{Name: "repository", Type: "git", Source: atc.Source{"uri": "https://example.invalid/repository.git"}}},
	}
	rendered, err := RenderResourceSourcePipeline(target)
	if err != nil {
		t.Fatalf("RenderResourceSourcePipeline: %v", err)
	}
	if rendered.Config.Template || len(rendered.Config.Jobs) != 1 || rendered.Config.Jobs[0].Name != "admit" {
		t.Fatalf("standing pipeline = %#v", rendered.Config)
	}
	get, ok := rendered.Config.Jobs[0].PlanSequence[0].Config.(*atc.GetStep)
	if !ok || get.Name != "repository-source" || get.Resource != "repository" || !get.Trigger || !reflect.DeepEqual(get.Version, target.Sources[0].Version) {
		t.Fatalf("admit get = %#v", get)
	}
	if len(rendered.ConfigHash) != 64 || rendered.PipelineName != "agent-workflow-source-selected-repository-v2-"+rendered.ConfigHash[:12] {
		t.Fatalf("rendered identity = %#v", rendered)
	}
}

func TestRenderFunctionWithBoundSourcesRemovesLiveResourceReads(t *testing.T) {
	function := FunctionConfig{
		SignatureVersion: 1,
		Resources:        atc.ResourceConfigs{{Name: "repository", Type: "git", Source: atc.Source{"uri": "https://example.invalid/repository.git"}}},
		ResourceSources:  []ResourceSource{{Name: "repository-source", Resource: "repository", Type: snapshot.TypeRef("repository/v1")}},
		Plan:             []atc.Step{{Config: &atc.AgentStep{Name: "inspect", FunctionID: "inspect", Prompt: "inspect", Inputs: []string{"repository-source"}, SnapshotInputs: map[string]atc.SnapshotInputConfig{"repository-source": {Type: snapshot.TypeRef("repository/v1")}}}}},
	}
	target := FunctionTarget{Kind: TargetWorkflow, WorkflowDefinitionID: 11, WorkflowName: "selected-repository", WorkflowVersion: 2, SignatureVersion: 1, Signature: PublicSignature{}, Function: function}
	ref := snapshot.SnapshotRef{ID: 41, Type: snapshot.TypeRef("repository/v1"), Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64))}
	rendered, err := RenderFunctionWithBoundSources(target, map[string]snapshot.SnapshotRef{"repository-source": ref})
	if err != nil {
		t.Fatalf("RenderFunctionWithBoundSources: %v", err)
	}
	if len(rendered.Config.Resources) != 0 {
		t.Fatalf("rendered resources = %#v, want no live reads", rendered.Config.Resources)
	}
	if _, err := RenderFunction(target); err == nil || !strings.Contains(err.Error(), "exact bound source refs") {
		t.Fatalf("unbound source render error = %v", err)
	}
	load, ok := rendered.Config.Jobs[0].PlanSequence[0].Config.(*atc.LoadSnapshotStep)
	if !ok || load.Name != "repository-source" || load.ID != "((snapshot_repository-source))" {
		t.Fatalf("bound source load = %#v", load)
	}
}
