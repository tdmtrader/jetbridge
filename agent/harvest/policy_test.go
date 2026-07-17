package harvest_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
)

func TestGitCredSecretName(t *testing.T) {
	cases := map[string]string{
		"tdmtrader/jetbridge":  "agent-harvest-git-tdmtrader-jetbridge",
		"TdmTrader/Concourse":  "agent-harvest-git-tdmtrader-concourse",
		"org/repo.name":        "agent-harvest-git-org-repo-name",
		"weird slug!with?bits": "agent-harvest-git-weird-slug-with-bits",
	}
	for slug, want := range cases {
		if got := harvest.GitCredSecretName(slug); got != want {
			t.Errorf("GitCredSecretName(%q) = %q, want %q", slug, got, want)
		}
	}
}

func TestConfigJSONShape(t *testing.T) {
	// HARVEST_CONFIG is the frozen §2.8.1 execution contract: the exec
	// marshals it, harvest-runner unmarshals it. Pin the wire keys.
	cfg := harvest.Config{
		StepName:      "harvest",
		Workspace:     "workspace",
		Repo:          "tdmtrader/jetbridge",
		TargetBranch:  "main",
		TicketID:      42,
		PipelineRunID: 7,
		Branch:        "agent/ticket-42",
		Push:          true,
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(payload, &raw)
	for _, key := range []string{"step_name", "workspace", "repo", "target_branch", "ticket_id", "pipeline_run_id", "branch", "push", "gate_policy"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("HARVEST_CONFIG missing frozen key %q (got %v)", key, raw)
		}
	}

	var back harvest.Config
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, cfg) {
		t.Errorf("round-trip mismatch: %+v != %+v", back, cfg)
	}
}
