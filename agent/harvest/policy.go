// Package harvest owns the terminal harvest step's policy types and
// execution config (00-shared-contracts.md §2.8.1/§6.3/§6.4). Wave-3
// scope note (harvest v0, 2026-07-17): the gate engine and judge are NOT
// implemented yet — GatePolicy/JudgeConfig exist as the frozen wire
// shapes (atc.HarvestStep fields, HARVEST_CONFIG), and harvest-runner
// REFUSES configs that declare them rather than silently skipping.
package harvest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// GatePolicy mirrors the §6.3 grammar (interpreting engine lands with
// the full harvest-step workstream).
type GatePolicy struct {
	Gates         []Gate `json:"gates,omitempty"`
	OnGateFailure string `json:"on_gate_failure,omitempty"` // "needs_review" (only v1 value)
}

// Gate is one named check in the gate policy.
type Gate struct {
	Gate    string `json:"gate"`              // build | test | lint
	Scope   string `json:"scope"`             // affected | full | affected_then_full
	Focus   string `json:"focus,omitempty"`   // optional component focus
	Timeout string `json:"timeout,omitempty"` // Go duration; default 30m
	Retries int    `json:"retries,omitempty"` // 0-2 failed-only re-runs (§6.3 flake stance)
}

// JudgeConfig is the §6.4 rubric block.
type JudgeConfig struct {
	Rubric        []RubricDimension `json:"rubric"`
	PassThreshold float64           `json:"pass_threshold"` // 0-10 weighted total
	Model         string            `json:"model,omitempty"`
	BudgetUSD     float64           `json:"budget_usd,omitempty"` // §6 budget.judge_usd; 0 = uncapped
}

// RubricDimension is one scored dimension of the judge rubric.
type RubricDimension struct {
	Name     string  `json:"name"`
	Weight   float64 `json:"weight"`
	Guidance string  `json:"guidance"`
}

// Validate rejects a judge config the runner could not execute
// faithfully (§6.4.1). Mirrors agent/workflow's import-time checks so a
// hand-authored harvest step gets the same fail-closed treatment.
func (j JudgeConfig) Validate() error {
	if len(j.Rubric) == 0 {
		return fmt.Errorf("judge: rubric must have at least one dimension")
	}
	seen := map[string]bool{}
	for _, d := range j.Rubric {
		if d.Name == "" {
			return fmt.Errorf("judge: rubric dimension name is required")
		}
		if seen[d.Name] {
			return fmt.Errorf("judge: duplicate rubric dimension %q", d.Name)
		}
		seen[d.Name] = true
		if d.Weight <= 0 {
			return fmt.Errorf("judge: rubric dimension %q weight must be > 0", d.Name)
		}
	}
	if j.PassThreshold < 0 || j.PassThreshold > 10 {
		return fmt.Errorf("judge: pass_threshold must be within 0-10, got %g", j.PassThreshold)
	}
	return nil
}

// RubricHash is the judge.score correlation key (§6.4.1): sha256 hex of
// the rubric's canonical JSON. Dimension order is semantic (it is the
// prompt order), so reordering changes the hash.
func (j JudgeConfig) RubricHash() string {
	payload, _ := json.Marshal(j.Rubric)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Config is the HARVEST_CONFIG payload — the frozen execution contract
// between exec.HarvestStep (marshals) and harvest-runner (unmarshals),
// §2.8.1.
type Config struct {
	StepName      string     `json:"step_name"`
	Workspace     string     `json:"workspace"`
	Repo          string     `json:"repo"`
	TargetBranch  string     `json:"target_branch"`
	TicketID      int        `json:"ticket_id"`
	PipelineRunID int        `json:"pipeline_run_id"`
	Branch        string     `json:"branch"`
	Push          bool       `json:"push"`
	GatePolicy    GatePolicy `json:"gate_policy"`
	// Judge omitted in v0 configs; the field exists so a future config
	// with a judge block is visible (and refused) rather than dropped.
	Judge *JudgeConfig `json:"judge,omitempty"`
}

// GitCredSecretName returns the §8.3 per-repo git-credential secret
// name: agent-harvest-git-<slug>, slug lowercased with every
// non-alphanumeric run collapsed to '-'.
func GitCredSecretName(repo string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(repo) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return fmt.Sprintf("agent-harvest-git-%s", strings.Trim(b.String(), "-"))
}
