// Package mergepolicy holds the merge-policy ladder: whether a delivered
// branch may merge without a human click, and under what fence.
//
// Every function here is pure — no I/O, no DB, no git. The fail-safe
// direction is ESCALATE: any uncertainty resolves toward human review,
// never toward a merge (design 2026-07-20 §3).
package mergepolicy

import "errors"

// Tier is the merge-policy tier declared on a workflow definition.
type Tier string

const (
	// TierManual is the default: a human clicks merge.
	TierManual Tier = "manual"
	// TierJudge adds a mandatory judge non-veto on top of the fence. The
	// judge can only ESCALATE — it never authorizes a merge on its own.
	TierJudge Tier = "judge"
	// TierAuto merges when the deterministic fence passes.
	TierAuto Tier = "auto"
)

func ValidTier(t Tier) bool {
	switch t {
	case TierManual, TierJudge, TierAuto:
		return true
	}
	return false
}

// Policy is the workflow-definition merge block, sitting beside GatePolicy.
type Policy struct {
	Tier Tier `yaml:"tier" json:"tier"`
	// AllowedPaths are globs every changed file must match. A trailing
	// "/**" means "this directory and everything under it".
	AllowedPaths []string `yaml:"allowed_paths,omitempty" json:"allowed_paths,omitempty"`
	// MaxChangedLines caps added+deleted lines across the whole diff.
	MaxChangedLines int `yaml:"max_changed_lines,omitempty" json:"max_changed_lines,omitempty"`
}

// Change is one file in the delivered diff.
type Change struct {
	Path         string
	LinesAdded   int
	LinesDeleted int
}

var (
	ErrInvalidTier     = errors.New("invalid merge-policy tier")
	ErrUnfencedTier    = errors.New("auto and judge tiers require both allowed_paths and a positive max_changed_lines")
	ErrNegativeCeiling = errors.New("max_changed_lines must not be negative")
)

// Validate rejects a policy that could not be evaluated safely. An auto or
// judge tier with no fence is a CONFIGURATION ERROR, never an allow-all:
// the whole point of the fence is that it is explicit.
func Validate(p Policy) error {
	if !ValidTier(p.Tier) {
		return ErrInvalidTier
	}
	if p.MaxChangedLines < 0 {
		return ErrNegativeCeiling
	}
	if p.Tier == TierAuto || p.Tier == TierJudge {
		if len(p.AllowedPaths) == 0 || p.MaxChangedLines == 0 {
			return ErrUnfencedTier
		}
	}
	return nil
}
