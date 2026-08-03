package occurrence

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
)

// nodeSeed compiles one shipped reusable-node seed and presents it the way a
// node run executes it: as the single-leaf function the binder renders.
func nodeSeed(t *testing.T, name string) *workflow.NodeDefinition {
	t.Helper()
	manifest, err := workflow.ManifestFromDir(
		filepath.Join("..", "..", "workflow", "seeds", name))
	if err != nil {
		t.Fatalf("ManifestFromDir(%q): %v", name, err)
	}
	compiled, err := workflow.CompileNodeDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileNodeDefinition(%q): %v", name, err)
	}
	return &workflow.NodeDefinition{
		ID: 41, Name: compiled.Name, Version: theRunsOwnVersion,
		ContentHash: "node-hash", Compiled: *compiled,
	}
}

// A run of a reusable node carries definition_kind 'node' and names a NODE
// definition. The workflow store's reads are scoped to
// definition_kind = 'workflow', so resolving one through it could only ever
// miss: every healthy node run failed its freeze with
// `workflow "code-review-node" has no version N` and logged an error, and got
// no durable history at all.
func TestFreezerProjectsAReusableNodeRun(t *testing.T) {
	for _, seed := range []string{"code-review-node-v1", "log-diagnosis-node-v1"} {
		t.Run(seed, func(t *testing.T) {
			node := nodeSeed(t, seed)
			function := node.Compiled.Function
			harness := newHarness(t, "small-fix-v3")
			harness.nodes.definitions[definitionKey(node.Name, theRunsOwnVersion)] = node
			harness.run = db.AgentWorkflowRun{
				ID:                   snapshot.WorkflowRunID(42),
				DefinitionKind:       workflow.DefinitionKindNode,
				TeamID:               1,
				WorkflowName:         node.Name,
				WorkflowDefinitionID: node.ID,
				WorkflowVersion:      theRunsOwnVersion,
				Status:               db.AgentWorkflowRunStatusSucceeded,
				ActualPlan: planSeed(t, &workflow.CompiledDefinition{
					SchemaVersion: 3, Name: node.Name, Function: &function,
				}),
			}

			rows := harness.freeze(t)
			if len(rows) == 0 {
				t.Fatal("a node run must be projected like any other")
			}
			// The workflow store must not have been consulted at all: a
			// workflow and a node may share a name and a version (the table's
			// UNIQUE key includes definition_kind), so a lookup that fell back
			// to it could silently build the wrong graph.
			if len(harness.definitions.requested) != 0 {
				t.Fatalf("resolved a node run through the workflow store: %v",
					harness.definitions.requested)
			}
			for _, row := range rows {
				if row.ReusableNodeName != node.Name {
					t.Fatalf("node %q lost its reusable identity: %q", row.NodeID, row.ReusableNodeName)
				}
				if row.ReusableNodeVersion == nil || *row.ReusableNodeVersion != theRunsOwnVersion {
					t.Fatalf("node %q lost its reusable version: %v", row.NodeID, row.ReusableNodeVersion)
				}
			}
		})
	}
}

// The failure must still be loud when the node version really is gone, and it
// must name the node rather than claiming a workflow is missing.
func TestFreezerReportsAMissingNodeVersionAsANode(t *testing.T) {
	node := nodeSeed(t, "code-review-node-v1")
	harness := newHarness(t, "small-fix-v3")
	harness.run.DefinitionKind = workflow.DefinitionKindNode
	harness.run.WorkflowName = node.Name

	err := harness.freezer.FreezeRun(context.Background(), harness.run)
	if err == nil {
		t.Fatal("expected a missing-node error")
	}
	if !containsFold(err.Error(), "node") || containsFold(err.Error(), "workflow \"") {
		t.Fatalf("error = %v, want it to name the node", err)
	}
	if len(harness.store.frozen) != 0 {
		t.Fatalf("a run whose graph is unavailable must not be partially frozen: %+v", harness.store.frozen)
	}
}
