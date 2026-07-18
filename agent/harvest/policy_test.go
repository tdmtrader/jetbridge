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

func TestJudgeConfigValidate(t *testing.T) {
	valid := harvest.JudgeConfig{
		Rubric:        []harvest.RubricDimension{{Name: "correctness", Weight: 3, Guidance: "works"}},
		PassThreshold: 6.5,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := map[string]harvest.JudgeConfig{
		"empty rubric":    {PassThreshold: 5},
		"unnamed dim":     {Rubric: []harvest.RubricDimension{{Weight: 1}}, PassThreshold: 5},
		"duplicate dim":   {Rubric: []harvest.RubricDimension{{Name: "a", Weight: 1}, {Name: "a", Weight: 1}}, PassThreshold: 5},
		"non-positive wt": {Rubric: []harvest.RubricDimension{{Name: "a", Weight: 0}}, PassThreshold: 5},
		"threshold > 10":  {Rubric: []harvest.RubricDimension{{Name: "a", Weight: 1}}, PassThreshold: 11},
		"threshold < 0":   {Rubric: []harvest.RubricDimension{{Name: "a", Weight: 1}}, PassThreshold: -1},
	}
	for name, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestRubricHashDeterministicAndOrderSensitive(t *testing.T) {
	a := harvest.JudgeConfig{Rubric: []harvest.RubricDimension{
		{Name: "x", Weight: 1, Guidance: "g"}, {Name: "y", Weight: 2, Guidance: "h"},
	}}
	b := harvest.JudgeConfig{Rubric: []harvest.RubricDimension{
		{Name: "y", Weight: 2, Guidance: "h"}, {Name: "x", Weight: 1, Guidance: "g"},
	}}
	if a.RubricHash() == "" || len(a.RubricHash()) != 64 {
		t.Fatalf("hash must be sha256 hex: %q", a.RubricHash())
	}
	if a.RubricHash() != a.RubricHash() {
		t.Fatal("hash must be deterministic")
	}
	if a.RubricHash() == b.RubricHash() {
		t.Fatal("dimension order is semantic (prompt order) - hash must differ")
	}
}
