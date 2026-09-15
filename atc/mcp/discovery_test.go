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
	ops := Operations()
	for i := range ops {
		if ops[i].ID == "pipeline_pause" {
			ops[i].handler = func(context.Context, json.RawMessage) (any, error) { return nil, nil }
		}
	}
	p := mcpauth.Principal{Scopes: []string{mcpauth.ScopeAdmin}}
	lookup := func(context.Context, string) (AccountEligibility, error) { return AccountIneligible, nil }
	c := catalog{operations: ops, principal: p, access: lookup}
	result, err := c.explain(context.Background(), explainArgs{Resource: "pipeline", Operation: "pipeline_pause"})
	if err != nil {
		t.Fatal(err)
	}
	e := result.Items[0]
	if e.NextStep != "contact_admin" || len(e.MissingScopes) != 1 || e.MissingScopes[0] != mcpauth.ScopePipelines || e.ExecutableBranchPresent {
		t.Fatalf("wrong denial: %+v", e)
	}
	result, err = c.explain(context.Background(), explainArgs{Resource: "container", Operation: "container_exec"})
	if err != nil || result.Items[0].MCPSupport != "not_implemented" || result.Items[0].NextStep != "stop" {
		t.Fatalf("unsupported: %+v %v", result, err)
	}
	result, err = c.explain(context.Background(), explainArgs{Resource: "worker", Operation: "unknown"})
	if err != nil || result.Items[0].MCPSupport != "unknown" {
		t.Fatalf("unknown: %+v %v", result, err)
	}
	if _, err = c.explain(context.Background(), explainArgs{Resource: "build", Operation: "pipeline_pause"}); err == nil {
		t.Fatal("wrong resource accepted")
	}
	c.access = func(context.Context, string) (AccountEligibility, error) { return "", errors.New("temporary") }
	if _, err = c.explain(context.Background(), explainArgs{Resource: "pipeline", Operation: "pipeline_pause"}); err == nil {
		t.Fatal("outage converted to denial")
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
