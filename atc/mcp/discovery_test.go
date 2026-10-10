package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/skymarshal/mcpauth"
)

func TestPreflightValidatesDeclaredBranchBeforeConsent(t *testing.T) {
	ops := Operations()
	good := `{"request":{"operation":"pipeline_config_set","arguments":{"team":"main","pipeline":"deploy","instance_vars":{},"version":"0","config_yaml":"jobs: []"}}}`
	op, _, err := decodeOperation("pipeline", json.RawMessage(good), ops)
	if err != nil || op.ID != "pipeline_config_set" {
		t.Fatalf("valid declared branch: %v", err)
	}
	for _, bad := range []string{strings.Replace(good, `"0"`, `"2147483648"`, 1), strings.Replace(good, `"instance_vars":{},`, "", 1), strings.Replace(good, `"pipeline_config_set"`, `"job_trigger"`, 1), strings.Replace(good, `"jobs: []"`, `"`+strings.Repeat("é", 70000)+`"`, 1)} {
		if _, _, err := decodeOperation("pipeline", json.RawMessage(bad), ops); err == nil {
			t.Fatal("invalid branch accepted")
		}
	}
	if _, _, err := decodeOperation("build", json.RawMessage(good), ops); err == nil {
		t.Fatal("wrong group accepted")
	}
}

func TestExplanationsSeparateSupportConsentAndAccount(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		scopes                             []string
		access                             AccountEligibility
		disabled                           bool
		wantNext, wantSupport, wantAccount string
		wantMissing, wantBranch            bool
	}{
		{"both blockers", nil, AccountIneligible, false, "contact_admin", "implemented", "ineligible", true, false},
		{"account only", []string{mcpauth.ScopePipelines}, AccountIneligible, false, "contact_admin", "implemented", "ineligible", false, false},
		{"consent only", []string{mcpauth.ScopeAdmin}, AccountTargetCheckRequired, false, "request_new_consent", "implemented", "target_check_required", true, false},
		{"target checks remain", []string{mcpauth.ScopePipelines}, AccountTargetCheckRequired, false, "use_available_tool", "implemented", "target_check_required", false, true},
		{"disabled", nil, AccountIneligible, true, "contact_operator", "disabled", "not_evaluated", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := catalog{operations: Operations(), principal: mcpauth.Principal{Scopes: tc.scopes}, disabled: map[string]bool{"pipeline_pause": tc.disabled},
				access: func(context.Context, string) (AccountEligibility, error) {
					if tc.disabled {
						t.Fatal("disabled operation consulted account access")
					}
					return tc.access, nil
				},
			}
			result, err := c.explain(context.Background(), explainArgs{Resource: "pipeline", Operation: "pipeline_pause"})
			if err != nil || len(result.Items) != 1 {
				t.Fatalf("explanation: %+v %v", result, err)
			}
			e := result.Items[0]
			if e.NextStep != tc.wantNext || e.MCPSupport != tc.wantSupport || e.AccountAccess != tc.wantAccount || e.ExecutableBranchPresent != tc.wantBranch {
				t.Fatalf("wrong recovery: %+v", e)
			}
			if len(e.MissingScopes) != 0 && (len(e.MissingScopes) != 1 || e.MissingScopes[0] != mcpauth.ScopePipelines) {
				t.Fatalf("wrong scopes: %+v", e)
			}
			if (len(e.MissingScopes) > 0) != tc.wantMissing {
				t.Fatalf("lost consent distinction: %+v", e)
			}
		})
	}
}

func TestAbsentOperationsDoNotConsultAccessOrSuggestConsent(t *testing.T) {
	c := catalog{operations: Operations(), access: func(context.Context, string) (AccountEligibility, error) {
		t.Fatal("absent operation consulted account access")
		return "", errors.New("unavailable")
	}}
	for _, tc := range []struct{ resource, operation string }{
		{"resource", "resource_check"}, {"resource", "resource_pin"}, {"resource", "resource_unpin"},
		{"pipeline", "pipeline_run_create"}, {"build", "build_create"}, {"container", "container_exec"},
		{"team", "team_set"}, {"team", "team_delete"}, {"server", "server_log_level_set"},
		{"worker", "unknown_action"}, {"unregistered", "unknown_action"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			_, err := c.explain(context.Background(), explainArgs{Resource: tc.resource, Operation: tc.operation})
			if err == nil || !strings.Contains(err.Error(), "NO_REGISTERED_OPERATION: no registered MCP operation") {
				t.Fatalf("absent operation returned empty success or unclear error: %v", err)
			}
			input := json.RawMessage(`{"request":{"operation":"` + tc.operation + `","arguments":{}}}`)
			// Preflight must reject the call before it can request an OAuth scope.
			if _, _, err := decodeOperation(tc.resource, input, c.operations); err == nil {
				t.Fatal("absent operation passed consent preflight")
			}
			result, err := c.dispatch(context.Background(), tc.resource, input)
			encoded, _ := json.Marshal(result)
			if err != nil || !result.IsError || !strings.Contains(string(encoded), "NO_REGISTERED_OPERATION") {
				t.Fatalf("guessed call: %s %v", encoded, err)
			}
		})
	}
	if _, err := c.explain(context.Background(), explainArgs{Resource: "container"}); err == nil {
		t.Fatal("absent resource returned empty success")
	}
}

func TestExplanationsReturnWholeResourceWithoutCursor(t *testing.T) {
	c := catalog{operations: Operations(), access: func(context.Context, string) (AccountEligibility, error) { return AccountTargetCheckRequired, nil }}
	want := map[string]bool{}
	for _, op := range c.operations {
		if op.Resource == "pipeline" {
			want[op.ID] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("empty pipeline coverage")
	}
	result, err := c.explain(context.Background(), explainArgs{Resource: "pipeline"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range result.Items {
		if !want[e.Operation] {
			t.Fatalf("unexpected or duplicate operation %s", e.Operation)
		}
		delete(want, e.Operation)
	}
	if len(want) != 0 {
		t.Fatalf("missing operations: %v", want)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "next_cursor") {
		t.Fatalf("discovery still paginated: %s", encoded)
	}
	if err := validateJSON(explainSchema(), []byte(`{"resource":"pipeline","cursor":"old"}`)); err == nil {
		t.Fatal("discovery accepts cursor")
	}
	if _, err := c.explain(context.Background(), explainArgs{Resource: "build", Operation: "pipeline_pause"}); err == nil {
		t.Fatal("wrong resource accepted")
	}
	c.access = func(context.Context, string) (AccountEligibility, error) { return "", errors.New("temporary") }
	if _, err := c.explain(context.Background(), explainArgs{Resource: "pipeline", Operation: "pipeline_pause"}); err == nil || !strings.Contains(err.Error(), "TEMPORARY_ACCESS_FAILURE") {
		t.Fatalf("outage converted to denial: %v", err)
	}
}

func TestIntegerRepresentationsPreserveRequestedBounds(t *testing.T) {
	for _, tc := range []struct{ group, op, key, value, want string }{
		{"pipeline", "pipelines_list", "limit", "1.0", "1"},
		{"build", "build_logs_read", "max_bytes", "1e3", "1000"},
	} {
		args := `{"` + tc.key + `":` + tc.value + `}`
		if tc.group == "build" {
			args = `{"build_id":3000000000,"` + tc.key + `":` + tc.value + `}`
		}
		_, normalized, err := decodeOperation(tc.group, json.RawMessage(`{"request":{"operation":"`+tc.op+`","arguments":`+args+`}}`), Operations())
		if err != nil {
			t.Fatal(err)
		}
		var typed map[string]json.RawMessage
		_ = json.Unmarshal(normalized, &typed)
		if string(typed[tc.key]) != tc.want {
			t.Fatalf("lost exact integer: %s", normalized)
		}
	}
}
