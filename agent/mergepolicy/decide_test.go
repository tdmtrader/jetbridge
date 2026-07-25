package mergepolicy_test

import (
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/mergepolicy"
)

func cleanBump() []mergepolicy.Change {
	return []mergepolicy.Change{{Path: "go.mod", LinesAdded: 2, LinesDeleted: 2}}
}

func TestManualTierAlwaysEscalates(t *testing.T) {
	d := mergepolicy.Decide(mergepolicy.Policy{Tier: mergepolicy.TierManual}, cleanBump(), nil)
	if d.Merge || !d.Escalate {
		t.Fatal("manual tier must always escalate")
	}
}

func TestAutoTierMergesWhenFencePasses(t *testing.T) {
	d := mergepolicy.Decide(fencePolicy(), cleanBump(), nil)
	if !d.Merge || d.MergedBy != "auto" {
		t.Fatalf("expected an auto merge, got %+v", d)
	}
}

func TestAutoTierEscalatesWhenFenceFails(t *testing.T) {
	d := mergepolicy.Decide(fencePolicy(), []mergepolicy.Change{
		{Path: "atc/api/handler.go", LinesAdded: 1},
	}, nil)
	if d.Merge || !d.Escalate {
		t.Fatal("a fence failure must escalate, not merge")
	}
}

func TestJudgeTierRequiresAVerdict(t *testing.T) {
	p := fencePolicy()
	p.Tier = mergepolicy.TierJudge
	d := mergepolicy.Decide(p, cleanBump(), nil)
	if d.Merge || !d.Escalate {
		t.Fatal("judge tier with no verdict must escalate")
	}
}

func TestJudgeFaultEscalates(t *testing.T) {
	p := fencePolicy()
	p.Tier = mergepolicy.TierJudge
	d := mergepolicy.Decide(p, cleanBump(), &mergepolicy.JudgeVerdict{Err: errors.New("model timeout")})
	if d.Merge || !d.Escalate {
		t.Fatal("a judge fault must escalate — never merge on judge failure")
	}
}

func TestJudgeCanVetoAPassingFence(t *testing.T) {
	p := fencePolicy()
	p.Tier = mergepolicy.TierJudge
	d := mergepolicy.Decide(p, cleanBump(), &mergepolicy.JudgeVerdict{
		Escalate: true, Reason: "bump crosses a major version",
	})
	if d.Merge {
		t.Fatal("the judge must be able to veto a passing fence")
	}
	if d.Reason != "bump crosses a major version" {
		t.Fatalf("the judge's reason must survive, got %q", d.Reason)
	}
}

func TestJudgeCannotAuthorizeAFailingFence(t *testing.T) {
	p := fencePolicy()
	p.Tier = mergepolicy.TierJudge
	d := mergepolicy.Decide(p, []mergepolicy.Change{
		{Path: "atc/api/handler.go", LinesAdded: 1},
	}, &mergepolicy.JudgeVerdict{Escalate: false})
	if d.Merge {
		t.Fatal("the judge must NEVER authorize past a failing fence")
	}
}

func TestUnknownTierEscalates(t *testing.T) {
	d := mergepolicy.Decide(mergepolicy.Policy{Tier: "yolo"}, cleanBump(), nil)
	if d.Merge || !d.Escalate {
		t.Fatal("an unknown tier must fail safe to escalate")
	}
}
