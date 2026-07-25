package mergepolicy_test

import (
	"testing"

	"github.com/concourse/concourse/agent/mergepolicy"
)

func TestValidTier(t *testing.T) {
	for _, tier := range []mergepolicy.Tier{mergepolicy.TierManual, mergepolicy.TierJudge, mergepolicy.TierAuto} {
		if !mergepolicy.ValidTier(tier) {
			t.Errorf("%q must be a valid tier", tier)
		}
	}
	for _, tier := range []mergepolicy.Tier{"", "always", "AUTO"} {
		if mergepolicy.ValidTier(tier) {
			t.Errorf("%q must not be a valid tier", tier)
		}
	}
}

func TestValidateRejectsUnfencedAutoTier(t *testing.T) {
	// auto with neither allowlist nor ceiling
	if err := mergepolicy.Validate(mergepolicy.Policy{Tier: mergepolicy.TierAuto}); err == nil {
		t.Fatal("auto tier without a fence must be rejected, not treated as allow-all")
	}
	// auto with an allowlist but no ceiling
	if err := mergepolicy.Validate(mergepolicy.Policy{
		Tier:         mergepolicy.TierAuto,
		AllowedPaths: []string{"go.mod"},
	}); err == nil {
		t.Fatal("auto tier without a changed-line ceiling must be rejected")
	}
	// auto with a ceiling but no allowlist
	if err := mergepolicy.Validate(mergepolicy.Policy{
		Tier:            mergepolicy.TierAuto,
		MaxChangedLines: 50,
	}); err == nil {
		t.Fatal("auto tier without a path allowlist must be rejected")
	}
}

func TestValidateAcceptsFencedAutoAndBareManual(t *testing.T) {
	if err := mergepolicy.Validate(mergepolicy.Policy{
		Tier:            mergepolicy.TierAuto,
		AllowedPaths:    []string{"go.mod", "go.sum"},
		MaxChangedLines: 50,
	}); err != nil {
		t.Fatalf("fenced auto tier must validate: %v", err)
	}
	if err := mergepolicy.Validate(mergepolicy.Policy{Tier: mergepolicy.TierManual}); err != nil {
		t.Fatalf("manual tier needs no fence: %v", err)
	}
}

func TestValidateRejectsNegativeCeiling(t *testing.T) {
	if err := mergepolicy.Validate(mergepolicy.Policy{
		Tier:            mergepolicy.TierAuto,
		AllowedPaths:    []string{"go.mod"},
		MaxChangedLines: -1,
	}); err == nil {
		t.Fatal("a negative changed-line ceiling must be rejected")
	}
}
