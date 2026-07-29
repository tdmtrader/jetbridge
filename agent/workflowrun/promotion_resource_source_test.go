package workflowrun

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func TestWorkflowTargetRendererValidatesSourceWorkflowForPromotionWithoutRuntimeAdmission(t *testing.T) {
	compiled, err := workflow.CompileDefinition(workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: promotion-source
signature_version: 1
inputs: []
outputs: []
resources:
  - name: repository
    type: git
    source: {uri: https://example.invalid/repository.git}
resource_sources:
  - name: repository-source
    resource: repository
    type: repository/v1
plan:
  - agent: inspect
    function_id: inspect
    prompt: inspect
    inputs: [repository-source]
    input_types:
      repository-source: {type: repository/v1}
`})
	if err != nil {
		t.Fatalf("compile workflow: %v", err)
	}
	definition := workflow.Definition{
		ID: 47, Name: "promotion-source", Version: 2, SchemaVersion: 3,
		SignatureVersion: 1, ContentHash: strings.Repeat("a", 64), Compiled: *compiled,
	}
	renderer := WorkflowTargetRenderer{RuntimeImage: "registry.example/agent-runner@sha256:" + strings.Repeat("a", 64)}

	if err := renderer.ValidatePromotion(definition); err != nil {
		t.Fatalf("validate source promotion: %v", err)
	}
}
