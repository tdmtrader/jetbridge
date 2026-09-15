package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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
