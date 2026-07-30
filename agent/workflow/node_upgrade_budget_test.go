package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
)

func TestNodeUpgradeResultResponseBudgetAcceptsAtBoundaryAndRejectsOver(t *testing.T) {
	tests := []struct {
		name          string
		contractNames int
		tooLarge      bool
	}{
		{name: "at boundary", contractNames: 62},
		{name: "over boundary", contractNames: 63, tooLarge: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := budgetUpgradeResult(64, test.contractNames, 1024)
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if test.tooLarge && len(encoded) <= workflow.MaxNodeUpgradeResponseBytes {
				t.Fatalf("over-boundary fixture is only %d bytes", len(encoded))
			}
			if !test.tooLarge && len(encoded) > workflow.MaxNodeUpgradeResponseBytes {
				t.Fatalf("at-boundary fixture is %d bytes", len(encoded))
			}

			err = workflow.ValidateNodeUpgradeResultResponseBudget(result)
			if test.tooLarge && !errors.Is(err, workflow.ErrNodeUpgradeResponseTooLarge) {
				t.Fatalf("error = %v, want response-too-large sentinel", err)
			}
			if !test.tooLarge && err != nil {
				t.Fatalf("at-boundary result rejected: %v", err)
			}
		})
	}
}

func TestNodeUpgradeBreakingBudgetRejectsBeforeWorkflowWork(t *testing.T) {
	predecessor, successor := budgetBreakingNodes(1024, 1024)
	nodes := &budgetReleasedNodeStore{
		definitions: map[int]workflow.NodeDefinition{
			predecessor.Version: predecessor,
			successor.Version:   successor,
		},
	}
	workflows := &budgetWorkflowStore{}
	selected := make([]string, 64)
	for index := range selected {
		selected[index] = fmt.Sprintf("workflow-%03d", index)
	}

	result, err := workflow.NewNodeUpgradeService(nodes, workflows).Upgrade(
		context.Background(),
		workflow.NodeUpgradeRequest{
			NodeName: "code-review", Version: successor.Version,
			Workflows: selected, CreatedBy: "alice",
		},
	)

	if !errors.Is(err, workflow.ErrNodeUpgradeResponseTooLarge) {
		t.Fatalf("error = %v, want response-too-large sentinel", err)
	}
	if result.NodeName != "code-review" || result.Version != successor.Version || len(result.Workflows) != 0 {
		t.Fatalf("partial result = %#v", result)
	}
	if workflows.liveCalls != 0 || workflows.importCalls != 0 || nodes.bindingCalls != 0 {
		t.Fatalf(
			"oversized request reached per-workflow work: live=%d imports=%d bindings=%d",
			workflows.liveCalls,
			workflows.importCalls,
			nodes.bindingCalls,
		)
	}
}

func TestNodeUpgradeBreakingResultsShareImmutableObligations(t *testing.T) {
	nodes := newUpgradeNodeStore()
	predecessor, err := nodes.ImportManifest("contract-node", breakingPredecessorManifest(), "author")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.Release("contract-node", predecessor.Version, workflow.ReleaseCompatible, "releaser"); err != nil {
		t.Fatal(err)
	}
	successor, err := nodes.ImportManifest("contract-node", breakingSuccessorManifest(), "author")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nodes.Release("contract-node", successor.Version, workflow.ReleaseBreaking, "releaser"); err != nil {
		t.Fatal(err)
	}
	memory := workflowtest.NewMemoryStoreWithNodeResolver(nodes, upgradePromotionValidator{})
	workflows := &bindingWorkflowStore{
		Store: memory, nodes: nodes,
		importFailures: map[string]error{},
		corruptImports: map[string]bool{},
	}
	selected := []string{"breaking-consumer-a", "breaking-consumer-b"}
	for _, name := range selected {
		manifest := breakingConsumerManifest()
		manifest[workflow.WorkflowFileName] = strings.Replace(
			manifest[workflow.WorkflowFileName],
			"name: breaking-consumer",
			"name: "+name,
			1,
		)
		outcome, err := workflows.ImportManifestWithOutcome(name, manifest, "author")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := workflows.Promote(name, outcome.Definition.Version, "promoter"); err != nil {
			t.Fatal(err)
		}
	}
	beforeImports := workflows.importCallCount()

	result, err := workflow.NewNodeUpgradeService(nodes, workflows).Upgrade(
		context.Background(),
		workflow.NodeUpgradeRequest{
			NodeName: "contract-node", Version: successor.Version,
			Workflows: selected, CreatedBy: "alice",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Workflows) != 2 ||
		result.Workflows[0].Status != workflow.NodeUpgradeRecompositionRequired ||
		result.Workflows[1].Status != workflow.NodeUpgradeRecompositionRequired ||
		result.Workflows[0].Obligations == nil ||
		result.Workflows[0].Obligations != result.Workflows[1].Obligations {
		t.Fatalf("breaking results do not share one obligations graph: %#v", result.Workflows)
	}
	if workflows.importCallCount() != beforeImports {
		t.Fatal("breaking upgrade imported a workflow")
	}
}

type budgetReleasedNodeStore struct {
	workflow.NodeStore
	definitions  map[int]workflow.NodeDefinition
	bindingCalls int
}

func (store *budgetReleasedNodeStore) Released(name string, version int) (workflow.NodeDefinition, bool, error) {
	definition, found := store.definitions[version]
	if !found || definition.Name != name {
		return workflow.NodeDefinition{}, false, nil
	}
	return definition, true, nil
}

func (store *budgetReleasedNodeStore) Bindings(int) ([]workflow.ResolvedNodeBinding, error) {
	store.bindingCalls++
	return nil, nil
}

type budgetWorkflowStore struct {
	workflow.Store
	liveCalls   int
	importCalls int
}

func (store *budgetWorkflowStore) Live(string) (*workflow.Definition, bool, error) {
	store.liveCalls++
	return nil, false, nil
}

func (store *budgetWorkflowStore) ImportManifestWithOutcome(
	string,
	workflow.Manifest,
	string,
) (workflow.ImportOutcome, error) {
	store.importCalls++
	return workflow.ImportOutcome{}, nil
}

func budgetBreakingNodes(contractNames, contractNameBytes int) (workflow.NodeDefinition, workflow.NodeDefinition) {
	inputs := make([]snapshot.Port, contractNames)
	for index := range inputs {
		inputs[index] = snapshot.Port{
			Name: fmt.Sprintf("contract-%04d-%s", index, strings.Repeat("x", contractNameBytes)),
			Type: "opaque/v1",
		}
	}
	predecessor := workflow.NodeDefinition{
		ID: 1, Name: "code-review", Version: 1,
		Compiled: workflow.CompiledNodeDefinition{
			Function: workflow.FunctionConfig{Inputs: []snapshot.Port{}},
		},
		Release: workflow.NodeRelease{
			ReleasedAt: 1, Compatibility: workflow.ReleaseCompatible,
		},
	}
	successor := workflow.NodeDefinition{
		ID: 2, Name: "code-review", Version: 2,
		Compiled: workflow.CompiledNodeDefinition{
			Function: workflow.FunctionConfig{Inputs: inputs},
		},
		Release: workflow.NodeRelease{
			ReleasedAt: 2, PredecessorVersion: 1, Compatibility: workflow.ReleaseBreaking,
		},
	}
	return predecessor, successor
}

func budgetUpgradeResult(workflows, contractNames, contractNameBytes int) workflow.NodeUpgradeResult {
	names := make([]string, contractNames)
	for index := range names {
		names[index] = fmt.Sprintf("contract-%03d-%s", index, strings.Repeat("x", contractNameBytes))
	}
	obligations := &workflow.NodeUpgradeObligations{
		Inputs: workflow.NodeContractChanges{
			Added: names, Removed: []string{}, Changed: []string{},
		},
		Outputs: workflow.NodeContractChanges{
			Added: []string{}, Removed: []string{}, Changed: []string{},
		},
		Parameters: workflow.NodeContractChanges{
			Added: []string{}, Removed: []string{}, Changed: []string{},
		},
	}
	result := workflow.NodeUpgradeResult{
		NodeName: "code-review", Version: 5,
		Workflows: make([]workflow.NodeUpgradeWorkflowResult, workflows),
	}
	for index := range result.Workflows {
		result.Workflows[index] = workflow.NodeUpgradeWorkflowResult{
			Workflow:   fmt.Sprintf("workflow-%03d", index),
			OldVersion: 7, Status: workflow.NodeUpgradeRecompositionRequired,
			Obligations: obligations,
		}
	}
	return result
}
