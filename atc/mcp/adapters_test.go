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
	count := 0
	for _, op := range bound {
		if op.Arguments != nil {
			count++
			if !op.Implemented() {
				t.Errorf("declared first-slice adapter missing: %s", op.ID)
			}
		} else if op.Implemented() {
			t.Errorf("metadata became executable: %s", op.ID)
		}
	}
	if count == 0 {
		t.Fatal("empty operation coverage")
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
