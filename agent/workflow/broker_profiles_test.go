package workflow_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

func TestCompileDefinitionWithBrokerCatalogFreezesOnlyNodeSelections(t *testing.T) {
	catalog, err := broker.NewCatalog([]broker.Profile{
		brokerProfile("consult-balanced", broker.ToolConsultAgent, broker.TierBalanced, broker.EffortHigh, "gpt-5.6"),
		brokerProfile("review-frontier", broker.ToolRequestReview, broker.TierFrontier, broker.EffortMedium, "gpt-5.6-pro"),
	})
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}

	definition, err := workflow.CompileDefinitionWithBrokerCatalog(workflow.Manifest{
		workflow.WorkflowFileName: `schema_version: 3
name: brokered
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: implement
    function_id: implement
    prompt: implement
    broker_profiles:
      - tool: consult_agent
        tier: balanced
        effort: high
  - agent: review
    function_id: review
    prompt: review
    broker_profiles:
      - tool: request_review
        tier: frontier
        effort: medium
`,
	}, catalog)
	if err != nil {
		t.Fatalf("compile brokered definition: %v", err)
	}

	if len(definition.Function.BrokerProfiles) != 2 {
		t.Fatalf("compiled profiles = %#v, want exactly the two selected mappings", definition.Function.BrokerProfiles)
	}
	implement, err := definition.Function.ResolveBrokerProfile("implement", broker.ToolConsultAgent, broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh})
	if err != nil {
		t.Fatalf("resolve implement profile: %v", err)
	}
	if implement.ID != "consult-balanced" || implement.Provider.Model != "gpt-5.6" || implement.Digest == "" {
		t.Fatalf("compiled implement profile = %#v, want frozen exact operator profile", implement)
	}
	if _, err := definition.Function.ResolveBrokerProfile("implement", broker.ToolRequestReview, broker.Selector{Tier: broker.TierFrontier, Effort: broker.EffortMedium}); err == nil || !strings.Contains(err.Error(), "not frozen") {
		t.Fatalf("unexpected profile outside implement node subset: %v", err)
	}
	if _, err := definition.Function.ResolveBrokerProfile("review", broker.ToolConsultAgent, broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh}); err == nil || !strings.Contains(err.Error(), "not frozen") {
		t.Fatalf("unexpected profile outside review node subset: %v", err)
	}

	if definition.Function.BrokerProfileProvenanceHash == "" {
		t.Fatal("compiled broker profiles have no authority hash")
	}
	changedCatalog, err := broker.NewCatalog([]broker.Profile{
		brokerProfile("consult-balanced", broker.ToolConsultAgent, broker.TierBalanced, broker.EffortHigh, "gpt-5.7"),
		brokerProfile("review-frontier", broker.ToolRequestReview, broker.TierFrontier, broker.EffortMedium, "gpt-5.6-pro"),
	})
	if err != nil {
		t.Fatalf("new changed catalog: %v", err)
	}
	changed, err := workflow.CompileDefinitionWithBrokerCatalog(workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: brokered
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: implement
    function_id: implement
    prompt: implement
    broker_profiles:
      - tool: consult_agent
        tier: balanced
        effort: high
  - agent: review
    function_id: review
    prompt: review
    broker_profiles:
      - tool: request_review
        tier: frontier
        effort: medium
`}, changedCatalog)
	if err != nil {
		t.Fatalf("compile changed brokered definition: %v", err)
	}
	if changed.Function.BrokerProfileProvenanceHash == definition.Function.BrokerProfileProvenanceHash {
		t.Fatal("operator model change did not change broker authority hash")
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal compiled definition: %v", err)
	}
	tampered := strings.Replace(string(encoded), "gpt-5.6", "gpt-5.7", 1)
	if _, err := workflow.ParseCompiled([]byte(tampered)); err == nil || !strings.Contains(err.Error(), "digest does not match") {
		t.Fatalf("tampered compiled broker authority error = %v, want exact profile digest rejection", err)
	}
}

func TestCompileDefinitionWithBrokerCatalogRejectsOperatorFieldsAndMissingCatalog(t *testing.T) {
	base := `schema_version: 3
name: brokered
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: implement
    function_id: implement
    prompt: implement
    broker_profiles:
      - tool: consult_agent
        tier: balanced
        effort: high
`

	if _, err := workflow.CompileDefinition(workflow.Manifest{workflow.WorkflowFileName: base}); err == nil || !strings.Contains(err.Error(), "broker catalog is required") {
		t.Fatalf("compile without broker catalog error = %v, want missing catalog rejection", err)
	}

	catalog, err := broker.NewCatalog([]broker.Profile{brokerProfile("consult-balanced", broker.ToolConsultAgent, broker.TierBalanced, broker.EffortHigh, "gpt-5.6")})
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}
	for field := range map[string]string{"provider": "openai", "model": "gpt-5.6", "harness": "codex"} {
		source := strings.Replace(base, "        effort: high", "        effort: high\n        "+field+": rejected", 1)
		if _, err := workflow.CompileDefinitionWithBrokerCatalog(workflow.Manifest{workflow.WorkflowFileName: source}, catalog); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("%s selector error = %v, want authored operator-field rejection", field, err)
		}
	}
}

func TestResolveBrokerProfileRejectsInMemoryAuthorityMutation(t *testing.T) {
	catalog, err := broker.NewCatalog([]broker.Profile{
		brokerProfile("consult-balanced", broker.ToolConsultAgent, broker.TierBalanced, broker.EffortHigh, "gpt-5.6"),
	})
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}
	definition, err := workflow.CompileDefinitionWithBrokerCatalog(workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: brokered
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: implement
    function_id: implement
    prompt: implement
    broker_profiles:
      - tool: consult_agent
        tier: balanced
        effort: high
`}, catalog)
	if err != nil {
		t.Fatalf("compile brokered definition: %v", err)
	}

	definition.Function.BrokerProfiles[0].Profile.Provider.Model = "attacker-controlled"
	if _, err := definition.Function.ResolveBrokerProfile("implement", broker.ToolConsultAgent, broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh}); err == nil || !strings.Contains(err.Error(), "digest does not match") {
		t.Fatalf("mutated in-memory broker authority error = %v, want exact authority rejection", err)
	}
}

func TestCompileDefinitionWithNodesAndBrokerCatalogPreservesReleasedNodeAuthority(t *testing.T) {
	catalog, err := broker.NewCatalog([]broker.Profile{
		brokerProfile("consult-balanced", broker.ToolConsultAgent, broker.TierBalanced, broker.EffortHigh, "gpt-5.6"),
	})
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}
	node, err := workflow.CompileNodeDefinitionWithBrokerCatalog(workflow.Manifest{workflow.NodeFileName: `schema_version: 1
name: brokered-review
step:
  agent: review
  function_id: review
  prompt: review
  broker_profiles:
    - tool: consult_agent
      tier: balanced
      effort: high
`}, catalog)
	if err != nil {
		t.Fatalf("compile released node: %v", err)
	}
	compiled, _, err := workflow.CompileDefinitionWithNodesAndBrokerCatalog(workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: consumer
signature_version: 1
inputs: []
outputs: []
plan:
  - node: review-instance
    uses: brokered-review@1
`}, releasedNodeResolver{node: workflow.NodeDefinition{ID: 31, Name: "brokered-review", Version: 1, ContentHash: "exact", Compiled: *node}}, catalog)
	if err != nil {
		t.Fatalf("compile consumer: %v", err)
	}
	if len(compiled.Function.BrokerProfiles) != 1 {
		t.Fatalf("compiled profiles = %#v, want released node authority", compiled.Function.BrokerProfiles)
	}
	profile := compiled.Function.BrokerProfiles[0]
	if profile.FunctionID != "review-instance" || profile.Profile.ID != "consult-balanced" || compiled.Function.BrokerProfileProvenanceHash == "" {
		t.Fatalf("compiled broker authority = %#v, hash = %q", profile, compiled.Function.BrokerProfileProvenanceHash)
	}
}

func TestRenderFunctionInjectsOnlyExactBrokerAuthorityIntoAgentStep(t *testing.T) {
	catalog, err := broker.NewCatalog([]broker.Profile{
		brokerProfile("consult-balanced", broker.ToolConsultAgent, broker.TierBalanced, broker.EffortHigh, "gpt-5.6"),
	})
	if err != nil {
		t.Fatalf("new catalog: %v", err)
	}
	definition, err := workflow.CompileDefinitionWithBrokerCatalog(workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: brokered
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: consult
    function_id: consult
    prompt: consult
    broker_profiles:
      - tool: consult_agent
        tier: balanced
        effort: high
  - agent: plain
    function_id: plain
    prompt: plain
`}, catalog)
	if err != nil {
		t.Fatalf("compile definition: %v", err)
	}
	persisted := workflow.Definition{ID: 41, Name: definition.Name, Version: 2, SchemaVersion: 3, SignatureVersion: 1, Compiled: *definition}
	target, err := workflow.FullFunctionTarget(persisted)
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	rendered, err := workflow.RenderFunction(target)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	consult := rendered.Config.Jobs[0].PlanSequence[0].Config.(*atc.AgentStep)
	plain := rendered.Config.Jobs[0].PlanSequence[1].Config.(*atc.AgentStep)
	if len(consult.BrokerAuthority) != 1 || consult.BrokerAuthority[0].ProfileDigest != definition.Function.BrokerProfiles[0].Profile.Digest || consult.BrokerAuthority[0].FunctionID != "consult" {
		t.Fatalf("rendered consult broker authority = %#v", consult.BrokerAuthority)
	}
	if len(plain.BrokerAuthority) != 0 {
		t.Fatalf("plain agent received broker authority: %#v", plain.BrokerAuthority)
	}
}

func TestRenderedBrokerAuthorityRejectsOuterProfileDrift(t *testing.T) {
	catalog, err := broker.NewCatalog([]broker.Profile{brokerProfile("consult-balanced", broker.ToolConsultAgent, broker.TierBalanced, broker.EffortHigh, "gpt-5.6")})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := workflow.CompileDefinitionWithBrokerCatalog(workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: brokered
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: consult
    function_id: consult
    prompt: consult
    broker_profiles:
      - tool: consult_agent
        tier: balanced
        effort: high
`}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	target, err := workflow.FullFunctionTarget(workflow.Definition{ID: 52, Name: compiled.Name, Version: 1, SchemaVersion: 3, SignatureVersion: 1, Compiled: *compiled})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := workflow.RenderFunction(target)
	if err != nil {
		t.Fatal(err)
	}
	rendered.Config.Jobs[0].PlanSequence[0].Config.(*atc.AgentStep).BrokerAuthority[0].WorkerImage = "registry.example/broker@sha256:" + strings.Repeat("c", 64)
	if _, err := workflow.RenderedTargetConfigHashWithBrokerProfiles(rendered.Config, rendered.DevValidationProfiles, rendered.DevValidationProvenanceHash, rendered.BrokerProfiles, rendered.BrokerProfileProvenanceHash); err == nil || !strings.Contains(err.Error(), "broker authority") {
		t.Fatalf("drifted rendered broker authority error = %v", err)
	}
}

func TestRenderedBrokerAuthorityRejectsUnknownOrNoncanonicalProfileJSON(t *testing.T) {
	catalog, err := broker.NewCatalog([]broker.Profile{brokerProfile("consult-balanced", broker.ToolConsultAgent, broker.TierBalanced, broker.EffortHigh, "gpt-5.6")})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := workflow.CompileDefinitionWithBrokerCatalog(workflow.Manifest{workflow.WorkflowFileName: `schema_version: 3
name: brokered
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: consult
    function_id: consult
    prompt: consult
    broker_profiles:
      - tool: consult_agent
        tier: balanced
        effort: high
`}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	target, err := workflow.FullFunctionTarget(workflow.Definition{ID: 53, Name: compiled.Name, Version: 1, SchemaVersion: 3, SignatureVersion: 1, Compiled: *compiled})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := workflow.RenderFunction(target)
	if err != nil {
		t.Fatal(err)
	}
	authority := &rendered.Config.Jobs[0].PlanSequence[0].Config.(*atc.AgentStep).BrokerAuthority[0]
	var decoded map[string]any
	if err := json.Unmarshal(authority.Profile, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["unknown"] = true
	authority.Profile, err = json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.RenderedTargetConfigHashWithBrokerProfiles(rendered.Config, rendered.DevValidationProfiles, rendered.DevValidationProvenanceHash, rendered.BrokerProfiles, rendered.BrokerProfileProvenanceHash); err == nil || !strings.Contains(err.Error(), "invalid profile") {
		t.Fatalf("unknown broker profile field error = %v", err)
	}
}

func brokerProfile(id string, tool broker.Tool, tier broker.Tier, effort broker.Effort, model string) broker.Profile {
	return broker.Profile{
		ID: id, Revision: 1, Selector: broker.Selector{Tier: tier, Effort: effort}, Tools: []broker.Tool{tool},
		WorkerImage: "registry.example/broker@sha256:" + strings.Repeat("a", 64),
		Adapter:     broker.AdapterSpec{Name: broker.AdapterCodex, Version: "1.2.3"},
		Provider:    broker.ProviderSpec{Name: "openai", Model: model}, NativeEffort: "high",
		InstructionsDigest: "sha256:" + strings.Repeat("b", 64), CredentialSlot: "broker-openai",
		Limits:   broker.Limits{Timeout: time.Minute, MaxInputBytes: 1024, MaxOutputBytes: 1024},
		Controls: broker.Controls{ReadOnlyWorkspace: true, NoBrokerRecursion: true, TestsUnavailable: true, NativeOutputSchema: true, IgnoresUserConfig: true},
	}
}
