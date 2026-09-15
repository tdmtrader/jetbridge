package mcp_test

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/auth"
	product "github.com/concourse/concourse/atc/mcp"
	"github.com/concourse/concourse/skymarshal/mcpauth"
	"github.com/google/jsonschema-go/jsonschema"
)

func operation(t *testing.T, id string) product.Operation {
	t.Helper()
	for _, op := range product.Operations() {
		if op.ID == id {
			return op
		}
	}
	t.Fatalf("missing operation %s", id)
	return product.Operation{}
}

func validate(t *testing.T, schema *jsonschema.Schema, value string) error {
	t.Helper()
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var input any
	if err := json.Unmarshal([]byte(value), &input); err != nil {
		t.Fatal(err)
	}
	return resolved.Validate(input)
}

func TestPreciseArgumentsDoNotMixOperations(t *testing.T) {
	get := operation(t, "pipeline_get")
	set := operation(t, "pipeline_config_set")
	full := product.GroupInputSchema([]product.Operation{get, set})
	pruned := product.GroupInputSchema([]product.Operation{get})
	read := `{"request":{"operation":"pipeline_get","arguments":{"team":"main","pipeline":"deploy","instance_vars":{}}}}`
	write := `{"request":{"operation":"pipeline_config_set","arguments":{"team":"main","pipeline":"deploy","instance_vars":{},"version":"0","config_yaml":"jobs: []"}}}`
	for _, input := range []string{read, write} {
		if err := validate(t, full, input); err != nil {
			t.Errorf("valid request rejected: %v", err)
		}
	}
	if err := validate(t, pruned, read); err != nil {
		t.Fatal(err)
	}
	if err := validate(t, pruned, write); err == nil {
		t.Fatal("write survived pruning")
	}
	for _, invalid := range []string{
		`{"request":{"operation":"pipeline_get","arguments":{"team":"main","pipeline":"deploy"}}}`,
		`{"request":{"operation":"pipeline_get","arguments":{"team":"main","pipeline":"deploy","instance_vars":{},"config_yaml":"jobs: []"}}}`,
		`{"request":{"operation":"pipeline_config_set","arguments":{"team":"main","pipeline":"deploy","instance_vars":{},"version":"-1","config_yaml":"jobs: []"}}}`,
		`{"request":{"operation":"build_abort","arguments":{"build_id":12}}}`,
		`{"request":{"operation":"pipeline_get","arguments":{"team":"main","pipeline":"deploy","instance_vars":{}}},"surprise":true}`,
	} {
		if err := validate(t, full, invalid); err == nil {
			t.Errorf("accepted invalid request %s", invalid)
		}
	}
}

func TestCatalogClassifiesEveryDeclaredActionWithoutEnablingIt(t *testing.T) {
	ops := product.Operations()
	if len(ops) == 0 {
		t.Fatal("empty registry")
	}
	seen := map[string]bool{}
	for _, op := range ops {
		if seen[op.ID] {
			t.Fatalf("duplicate operation %s", op.ID)
		}
		seen[op.ID] = true
		if op.ID == "" || op.Resource == "" || op.Description == "" {
			t.Fatalf("incomplete metadata: %+v", op)
		}
		if _, known := auth.AuthorizationKindForAction(op.Action); !known {
			t.Errorf("unknown authority for %s", op.ID)
		}
		scope, known := product.ScopeForAction(op.Action)
		if !known || scope == "" || scope != op.Scope {
			t.Errorf("scope drift for %s", op.ID)
		}
		if op.Implemented() {
			t.Errorf("%s advertises an adapter not yet implemented", op.ID)
		}
		if op.Arguments != nil {
			if op.Result == nil {
				t.Errorf("missing result schema for %s", op.ID)
			}
			if _, err := product.GroupInputSchema([]product.Operation{op}).Resolve(nil); err != nil {
				t.Errorf("%s input: %v", op.ID, err)
			}
			if _, err := product.GroupOutputSchema([]product.Operation{op}).Resolve(nil); err != nil {
				t.Errorf("%s output: %v", op.ID, err)
			}
		}
	}
	if _, known := product.ScopeForAction("arbitrary-new-action"); known {
		t.Fatal("unclassified action allowed")
	}
	if product.AllowsAction(mcpauth.Principal{Scopes: []string{mcpauth.ScopeAdmin}}, atc.SaveConfig) {
		t.Fatal("admin consent became a wildcard")
	}
	if op := operation(t, "builds_list"); op.Action != atc.ListPipelineBuilds {
		t.Fatal("build listing uses unsafe or differently authorized route")
	}
}

func TestOperationAuthoritiesStayIndependent(t *testing.T) {
	for id, scope := range map[string]string{
		"pipeline_get":         mcpauth.ScopeRead,
		"pipeline_config_set":  mcpauth.ScopePipelines,
		"pipeline_pause":       mcpauth.ScopePipelines,
		"pipeline_unpause":     mcpauth.ScopePipelines,
		"job_trigger":          mcpauth.ScopeBuilds,
		"build_abort":          mcpauth.ScopeBuilds,
		"pipeline_run_create":  mcpauth.ScopePipelines,
		"build_create":         mcpauth.ScopePipelines,
		"container_exec":       mcpauth.ScopeHijack,
		"server_log_level_set": mcpauth.ScopeAdmin,
	} {
		op := operation(t, id)
		if op.Scope != scope {
			t.Errorf("%s: got %s, want %s", id, op.Scope, scope)
		}
		for _, other := range []string{mcpauth.ScopeRead, mcpauth.ScopePipelines, mcpauth.ScopeBuilds, mcpauth.ScopeHijack, mcpauth.ScopeAdmin} {
			if product.AllowsAction(mcpauth.Principal{Scopes: []string{other}}, op.Action) != (scope == other) {
				t.Errorf("%s leaks authority from %s", id, other)
			}
		}
	}
}

func TestCatalogSchemasAreIsolatedAndBounded(t *testing.T) {
	first := operation(t, "pipeline_get")
	first.Arguments.Required = nil
	second := operation(t, "pipeline_get")
	if len(second.Arguments.Required) == 0 {
		t.Fatal("caller mutated shared registry")
	}
	builds := operation(t, "builds_list")
	if err := validate(t, builds.Arguments, `{"team":"main"}`); err == nil {
		t.Fatal("team-only build listing accepted")
	}
	logs := operation(t, "build_logs_read")
	if err := validate(t, logs.Arguments, `{"build_id":1,"max_bytes":65537}`); err == nil {
		t.Fatal("oversized log request accepted")
	}
	if err := validate(t, logs.Arguments, `{"build_id":1,"max_bytes":65536}`); err != nil {
		t.Fatal(err)
	}
}
