package mergepolicy_test

import (
	"testing"

	"github.com/concourse/concourse/agent/mergepolicy"
)

func fencePolicy() mergepolicy.Policy {
	return mergepolicy.Policy{
		Tier:            mergepolicy.TierAuto,
		AllowedPaths:    []string{"go.mod", "go.sum", "vendor/**"},
		MaxChangedLines: 50,
	}
}

func TestFencePassesWhenAllPathsAllowedAndUnderCeiling(t *testing.T) {
	res := mergepolicy.EvaluateFence(fencePolicy(), []mergepolicy.Change{
		{Path: "go.mod", LinesAdded: 2, LinesDeleted: 2},
		{Path: "go.sum", LinesAdded: 8, LinesDeleted: 8},
	})
	if !res.Passed {
		t.Fatalf("expected fence to pass, got %q", res.Reason)
	}
}

func TestFenceFailsOnPathOutsideAllowlist(t *testing.T) {
	res := mergepolicy.EvaluateFence(fencePolicy(), []mergepolicy.Change{
		{Path: "go.mod", LinesAdded: 2, LinesDeleted: 2},
		{Path: "atc/db/migration/foo.go", LinesAdded: 1},
	})
	if res.Passed {
		t.Fatal("a file outside the allowlist must fail the fence")
	}
	if res.Reason == "" {
		t.Fatal("a failing fence must explain itself")
	}
}

func TestFenceFailsOverCeiling(t *testing.T) {
	res := mergepolicy.EvaluateFence(fencePolicy(), []mergepolicy.Change{
		{Path: "go.sum", LinesAdded: 40, LinesDeleted: 40},
	})
	if res.Passed {
		t.Fatal("80 changed lines must exceed a ceiling of 50")
	}
}

func TestFenceFailsOnEmptyDiff(t *testing.T) {
	if mergepolicy.EvaluateFence(fencePolicy(), nil).Passed {
		t.Fatal("an empty diff must fail closed")
	}
}

func TestFenceDoubleStarMatchesSubtreeNotSibling(t *testing.T) {
	p := mergepolicy.Policy{
		Tier: mergepolicy.TierAuto, AllowedPaths: []string{"vendor/**"}, MaxChangedLines: 100,
	}
	if !mergepolicy.EvaluateFence(p, []mergepolicy.Change{
		{Path: "vendor/github.com/x/y.go", LinesAdded: 1},
	}).Passed {
		t.Fatal("vendor/** must match a nested path")
	}
	if mergepolicy.EvaluateFence(p, []mergepolicy.Change{
		{Path: "vendored-secrets.yml", LinesAdded: 1},
	}).Passed {
		t.Fatal("vendor/** must NOT match a sibling with a shared prefix")
	}
}
