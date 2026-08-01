package occurrence

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
)

// The helpers below drive the real authoring pipeline — manifest, compile,
// render, materialize, plan — so every fixture in this package is one the rest
// of the system actually accepts. Synthetic plans cannot prove identity
// agreement, because nothing forces them to resemble what the planner emits.

func seedDir(name string) string {
	return filepath.Join("..", "..", "workflow", "seeds", name)
}

func seedNames(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("..", "..", "workflow", "seeds", "*", "workflow.yaml"))
	if err != nil {
		t.Fatalf("globbing seeds: %v", err)
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, filepath.Base(filepath.Dir(match)))
	}
	return names
}

func compileSeed(t *testing.T, name string) *workflow.CompiledDefinition {
	t.Helper()
	manifest, err := workflow.ManifestFromDir(seedDir(name))
	if err != nil {
		t.Fatalf("ManifestFromDir(%q): %v", name, err)
	}
	return compileManifest(t, manifest)
}

func compileManifest(t *testing.T, manifest map[string]string) *workflow.CompiledDefinition {
	t.Helper()
	compiled, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition: %v", err)
	}
	return compiled
}

// planSeed renders a compiled definition and runs the real build planner over
// it, returning the frozen actual plan exactly as a run would store it.
func planSeed(t *testing.T, compiled *workflow.CompiledDefinition) []byte {
	t.Helper()

	definition := workflow.Definition{
		ID:               41,
		Name:             compiled.Name,
		Version:          3,
		SchemaVersion:    compiled.SchemaVersion,
		SignatureVersion: compiled.Function.SignatureVersion,
		ContentHash:      "definition-hash",
		Compiled:         *compiled,
	}
	target, err := workflow.FullFunctionTarget(definition)
	if err != nil {
		t.Fatalf("FullFunctionTarget: %v", err)
	}
	rendered, err := workflow.RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction: %v", err)
	}

	params := map[string]any{}
	for index, param := range rendered.Config.Params {
		params[param.Name] = strconv.Itoa(9000001 + index)
	}
	validated, err := atc.ValidateRunParams(rendered.Config.Params, params)
	if err != nil {
		t.Fatalf("ValidateRunParams: %v", err)
	}
	concrete, err := atc.MaterializeRunConfig(rendered.Config, 1, 7, validated)
	if err != nil {
		t.Fatalf("MaterializeRunConfig: %v", err)
	}
	if len(concrete.Jobs) != 1 {
		t.Fatalf("expected exactly one job, got %d", len(concrete.Jobs))
	}

	plan, err := builds.NewPlanner(atc.NewPlanFactory(0)).Create(
		concrete.Jobs[0].StepConfig(), nil, concrete.ResourceTypes, concrete.Prototypes, nil, false,
	)
	if err != nil {
		t.Fatalf("planner.Create: %v", err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshalling plan: %v", err)
	}
	return raw
}

// planIDOf returns the plan ID of the single node with the given identity,
// failing the test when the plan does not contain exactly one.
func planIDOf(t *testing.T, actualPlan []byte, nodeID string) string {
	t.Helper()
	nodes, err := PlanNodes(actualPlan)
	if err != nil {
		t.Fatalf("PlanNodes: %v", err)
	}
	var found []string
	for _, node := range nodes {
		if node.NodeID == nodeID {
			found = append(found, node.PlanID)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one plan node %q, got %v", nodeID, found)
	}
	return found[0]
}
