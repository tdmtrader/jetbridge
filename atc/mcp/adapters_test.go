package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/concourse/concourse/skymarshal/mcpauth"
)

func TestBoundRegistryAndAmbiguousMutationOutcomes(t *testing.T) {
	ctx := context.WithValue(context.Background(), principalKey{}, mcpauth.Principal{})
	for _, op := range []string{"job_trigger", "pipeline_config_set"} {
		for _, status := range []int{200, 500} {
			calls := 0
			api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "" || len(r.Cookies()) != 0 {
					t.Error("forwarded caller credential")
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{}`))
			})
			args := json.RawMessage(`{"team":"main","pipeline":"deploy","instance_vars":{},"job":"unit","version":"0","config_yaml":"jobs: []"}`)
			_, err := coreAdapter(api, op)(ctx, args)
			if err == nil || !strings.Contains(err.Error(), "OUTCOME_UNKNOWN") || calls != 1 {
				t.Fatalf("%s status=%d calls=%d error=%v", op, status, calls, err)
			}
		}
	}
	bound := boundOperations(http.NotFoundHandler())
	if len(bound) == 0 {
		t.Fatal("empty operation coverage")
	}
	for _, op := range bound {
		if op.handler == nil || op.Arguments == nil || op.Result == nil {
			t.Errorf("incomplete bound operation: %s", op.ID)
		}
	}
}
func TestResultBudgetIncludesEscapingAndBothContentForms(t *testing.T) {
	huge := toolSuccess(map[string]any{"text": strings.Repeat("<\x00", 130000)})
	if !huge.IsError {
		t.Fatal("escaped duplicated result exceeded budget without rejection")
	}
	bounded := toolError(strings.Repeat("🙂", 4000))
	encoded, err := json.Marshal(bounded)
	if err != nil || len(encoded) > 5000 {
		t.Fatalf("error not bounded: %d %v", len(encoded), err)
	}
}

// A committed config whose warning exceeds the result schema's bound must still
// produce a valid receipt: the mutation already happened, so INVALID_RESULT
// there tells the caller a successful write failed.
func TestOversizeConfigWarningStillValidates(t *testing.T) {
	ctx := context.WithValue(context.Background(), principalKey{}, mcpauth.Principal{})
	warning := strings.Repeat("え", configWarningMaxChars+500)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Concourse-Config-Version", "7")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"warnings": []map[string]string{{"type": "pipeline", "message": warning}},
		})
	})
	args := json.RawMessage(`{"team":"main","pipeline":"deploy","instance_vars":{},"version":"0","config_yaml":"jobs: []"}`)
	result, err := coreAdapter(api, "pipeline_config_set")(ctx, args)
	if err != nil {
		t.Fatalf("committed write reported an error: %v", err)
	}
	got := result.(map[string]any)["warnings"].([]string)
	if len(got) != 1 || utf8.RuneCountInString(got[0]) != configWarningMaxChars {
		t.Fatalf("warning not bounded to %d characters: %d", configWarningMaxChars, utf8.RuneCountInString(got[0]))
	}
	for _, op := range Operations() {
		if op.ID != "pipeline_config_set" {
			continue
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("encode receipt: %v", err)
		}
		if err := validateJSON(op.Result, encoded); err != nil {
			t.Fatalf("truncated receipt still fails the result schema: %v", err)
		}
	}
}

// Every bound adapter refuses arguments it cannot decode, naming the field
// or the JSON problem, and never calls the API with zero values in their place.
func TestAdaptersRefuseMalformedArguments(t *testing.T) {
	ctx := context.WithValue(context.Background(), principalKey{}, mcpauth.Principal{})
	field := map[string]string{
		"build_get": "build_id", "build_abort": "build_id", "build_logs_read": "build_id",
		"pipelines_list": "team", "pipeline_get": "team", "pipeline_config_get": "team",
		"pipeline_config_set": "team", "pipeline_pause": "team", "pipeline_unpause": "team",
		"builds_list": "team", "job_trigger": "team",
	}
	payloads := []struct {
		name, input string
		want        func(op string) string
	}{
		{"wrong field type", `{"build_id":"seven","team":7}`, func(op string) string { return `"` + field[op] + `"` }},
		{"truncated JSON", `{"team":`, func(string) string { return "malformed JSON" }},
		{"not an object", `["main"]`, func(string) string { return "JSON object" }},
	}
	calls := 0
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ })
	for _, op := range boundOperations(api) {
		if _, ok := field[op.ID]; !ok {
			t.Errorf("no malformed-argument case for bound operation %s", op.ID)
			continue
		}
		for _, p := range payloads {
			calls = 0
			_, err := op.handler(ctx, json.RawMessage(p.input))
			if err == nil || !strings.HasPrefix(err.Error(), "INVALID_ARGUMENTS: ") || !strings.Contains(err.Error(), p.want(op.ID)) {
				t.Errorf("%s / %s: want INVALID_ARGUMENTS naming %s, got %v", op.ID, p.name, p.want(op.ID), err)
			}
			if calls != 0 {
				t.Errorf("%s / %s: API called %d times with undecodable arguments", op.ID, p.name, calls)
			}
		}
	}
}
