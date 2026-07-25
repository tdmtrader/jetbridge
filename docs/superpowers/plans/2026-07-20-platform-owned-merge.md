# Platform-Owned Merge Implementation Plan — Phase 1

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the greenfield merge-policy domain — the ladder decision function and its deterministic fence — so the platform can later decide, safely, whether a delivered branch may merge without a human click.

**Architecture:** A new leaf package `agent/mergepolicy` holding pure decision logic: policy shape + validation, a fence evaluator over the delivered diff (path allowlist + changed-line ceiling), and `Decide`, which composes them with a judge verdict. Every uncertain path returns *escalate* — the fail-safe direction from the design's §3. No I/O, no DB, no git; it is a pure function over data the caller already has.

**Tech Stack:** Go, stdlib only (`path.Match`, `strings`). Plain `testing` package — matching the idiom in `agent/api/outcomes`, not Ginkgo.

**Design:** `docs/superpowers/specs/2026-07-20-platform-owned-merge-design.md`

---

## Scope and concurrency

Phase 1 is **greenfield only**. It creates one new package and touches nothing
else, because `codex/postgres-delivered-diffs` is concurrently editing
`atc/exec/harvest_step.go`, `agent/harvest/runner.go`, `agent/harvest/flight.go`,
`atc/atccmd/command.go`, `atc/engine/step_factory.go`,
`agent/api/outcomes/diff_handler.go`, `agent/outcomewatcher/mirror_cache.go`, and
has **uncommitted** edits to `00-shared-contracts.md`, `12-delivery-outcomes.md`,
and `remainders/2026-07-17-delivery-outcomes.md`.

**Do not touch any file in that list during Phase 1.**

Phase 2 (commit trailer, merge-time freshness step, the merge route and handler,
migration `1773106096`, Elm) is deliberately **not planned here**. Writing steps
against files that are actively changing produces wrong line numbers and wrong
code. Plan Phase 2 once codex lands and the files are stable.

## File structure

| File | Responsibility |
|---|---|
| `agent/mergepolicy/policy.go` (create) | `Tier`, `Policy`, `Change` types + validation |
| `agent/mergepolicy/policy_test.go` (create) | validation behaviour |
| `agent/mergepolicy/fence.go` (create) | `EvaluateFence` — allowlist + line ceiling over the real diff |
| `agent/mergepolicy/fence_test.go` (create) | fence behaviour incl. glob edge cases |
| `agent/mergepolicy/decide.go` (create) | `Decide` — the ladder, fail-safe |
| `agent/mergepolicy/decide_test.go` (create) | one test per tier + every fault path |

---

### Task 1: Policy types and validation

**Files:**
- Create: `agent/mergepolicy/policy.go`
- Test: `agent/mergepolicy/policy_test.go`

The load-bearing rule: **an `auto` tier with no fence is a configuration error,
not "allow everything."** A policy that names the auto tier but omits the
allowlist or the ceiling must fail validation, so an unfenced auto tier can never
reach `Decide`.

- [ ] **Step 1: Write the failing test**

```go
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
	err := mergepolicy.Validate(mergepolicy.Policy{Tier: mergepolicy.TierAuto})
	if err == nil {
		t.Fatal("auto tier without a fence must be rejected, not treated as allow-all")
	}
	// auto with an allowlist but no ceiling
	err = mergepolicy.Validate(mergepolicy.Policy{
		Tier:         mergepolicy.TierAuto,
		AllowedPaths: []string{"go.mod"},
	})
	if err == nil {
		t.Fatal("auto tier without a changed-line ceiling must be rejected")
	}
	// auto with a ceiling but no allowlist
	err = mergepolicy.Validate(mergepolicy.Policy{
		Tier:            mergepolicy.TierAuto,
		MaxChangedLines: 50,
	})
	if err == nil {
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/mergepolicy/`
Expected: FAIL — the package does not exist yet
(`no Go files in .../agent/mergepolicy`).

- [ ] **Step 3: Write minimal implementation**

```go
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
	ErrInvalidTier    = errors.New("invalid merge-policy tier")
	ErrUnfencedTier   = errors.New("auto and judge tiers require both allowed_paths and a positive max_changed_lines")
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/mergepolicy/ -v`
Expected: PASS — all four tests.

- [ ] **Step 5: Commit**

```bash
git add agent/mergepolicy/policy.go agent/mergepolicy/policy_test.go
git commit -m "feat(mergepolicy): policy tiers with mandatory fence validation"
```

---

### Task 2: The fence evaluator

**Files:**
- Create: `agent/mergepolicy/fence.go`
- Test: `agent/mergepolicy/fence_test.go`

Evaluated against the **real delivered diff**, never the workflow's
self-description. The empty-diff case fails closed: there is nothing to merge,
and a policy that auto-merges an empty change set is a bug waiting to happen.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/mergepolicy/ -run TestFence`
Expected: FAIL — `undefined: mergepolicy.EvaluateFence`.

- [ ] **Step 3: Write minimal implementation**

```go
package mergepolicy

import (
	"fmt"
	"path"
	"strings"
)

// FenceResult is the outcome of evaluating a policy against a real diff.
type FenceResult struct {
	Passed bool
	Reason string // always populated when Passed is false
}

// EvaluateFence checks the DELIVERED DIFF against the policy — never the
// workflow's self-description. "Version bump" is a category of intent; this
// looks at what actually changed.
func EvaluateFence(p Policy, changes []Change) FenceResult {
	if len(changes) == 0 {
		return FenceResult{Reason: "empty diff: nothing to merge"}
	}
	total := 0
	for _, c := range changes {
		if !allowedPath(p.AllowedPaths, c.Path) {
			return FenceResult{Reason: fmt.Sprintf("path %q is outside the allowlist", c.Path)}
		}
		total += c.LinesAdded + c.LinesDeleted
	}
	if total > p.MaxChangedLines {
		return FenceResult{Reason: fmt.Sprintf("%d changed lines exceeds the ceiling of %d", total, p.MaxChangedLines)}
	}
	return FenceResult{Passed: true}
}

func allowedPath(patterns []string, p string) bool {
	for _, pattern := range patterns {
		if matchPath(pattern, p) {
			return true
		}
	}
	return false
}

// matchPath supports a trailing "/**" for subtree matching (which
// path.Match cannot express) and delegates everything else to path.Match.
func matchPath(pattern, p string) bool {
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	}
	ok, err := path.Match(pattern, p)
	return err == nil && ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/mergepolicy/ -v`
Expected: PASS — all tests including Task 1's.

- [ ] **Step 5: Commit**

```bash
git add agent/mergepolicy/fence.go agent/mergepolicy/fence_test.go
git commit -m "feat(mergepolicy): deterministic fence over the delivered diff"
```

---

### Task 3: The ladder decision

**Files:**
- Create: `agent/mergepolicy/decide.go`
- Test: `agent/mergepolicy/decide_test.go`

The safety property under test: **the judge can veto but never authorize.** A
missing verdict, a judge fault, an unknown tier, or a failed fence all resolve to
escalate.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/mergepolicy/ -run 'TestManual|TestAuto|TestJudge|TestUnknown'`
Expected: FAIL — `undefined: mergepolicy.Decide`.

- [ ] **Step 3: Write minimal implementation**

```go
package mergepolicy

// JudgeVerdict is the judge's input to the ladder. The judge may only
// ESCALATE — there is deliberately no field by which it can authorize a
// merge that the fence rejected.
type JudgeVerdict struct {
	Escalate bool
	Reason   string
	Err      error // any judge fault; non-nil always escalates
}

// Decision is the ladder's answer.
type Decision struct {
	Merge    bool
	MergedBy string // "auto" | "judge"; empty when escalating
	Escalate bool
	Reason   string
}

func escalate(reason string) Decision {
	return Decision{Escalate: true, Reason: reason}
}

// Decide reports whether the platform may merge without a human click.
//
// Fail-safe direction: every uncertain path returns escalate. The judge tier
// is strictly MORE conservative than auto — it is the fence plus a mandatory
// judge non-veto.
func Decide(p Policy, changes []Change, judge *JudgeVerdict) Decision {
	switch p.Tier {
	case TierAuto:
		if r := EvaluateFence(p, changes); !r.Passed {
			return escalate(r.Reason)
		}
		return Decision{Merge: true, MergedBy: "auto"}

	case TierJudge:
		// Fence first: the judge never gets to authorize past it.
		if r := EvaluateFence(p, changes); !r.Passed {
			return escalate(r.Reason)
		}
		if judge == nil {
			return escalate("judge tier requires a judge verdict")
		}
		if judge.Err != nil {
			return escalate("judge fault: " + judge.Err.Error())
		}
		if judge.Escalate {
			return escalate(judge.Reason)
		}
		return Decision{Merge: true, MergedBy: "judge"}

	default:
		// TierManual and any unknown/corrupt tier value.
		return escalate("manual review required")
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/mergepolicy/ -v`
Expected: PASS — all tests across the three files.

- [ ] **Step 5: Verify the package is vet-clean and commit**

```bash
go vet ./agent/mergepolicy/
git add agent/mergepolicy/decide.go agent/mergepolicy/decide_test.go
git commit -m "feat(mergepolicy): fail-safe ladder decision (judge vetoes, never authorizes)"
```

---

## Phase 1 exit criteria — MET (`61e4415fad`)

- `go test ./agent/mergepolicy/` passes (17 tests).
- `go vet ./agent/mergepolicy/` is clean.
- No file outside `agent/mergepolicy/` was modified.

## Landed beyond Phase 1

**`agent/merge` — the speculative merge engine (`bf83d15aa8`, 7 tests).**
Greenfield, no collision. `Prepare` computes a prospective merge on a scratch
branch of a working clone and **never touches the remote**, so a caller can gate
the result before landing it (design §4.3). A conflict is a reported `Result`,
not an error, and the merge is aborted so the clone stays usable. `Staleness` is
the read-only half of freshness (§4.1).

Runs **pod-side**, as `agent/harvest` does. This is a deliberate constraint, not
an accident: `codex/postgres-delivered-diffs` is concurrently making the web node
stateless with respect to git, and a web-side merge would undo that.

## Priority change (owner, 2026-07-20)

**The merge-policy ladder is demoted to a later extension.** `agent/mergepolicy`
stays as landed — tested and self-contained — but nothing should be built on top
of it for now. The `auto` and `judge` tiers, and the invariant reversal in design
§2.1, are **not** part of the core path.

The core is narrower and safer: **a human clicks merge, and the platform performs
it.** That alone converts outcome tracking from inference to record, which is the
whole point of the design. `Decide` is simply not consulted in the core path — a
manual-tier policy escalates every time, which is exactly right.

## Phase 2 — blocked on codex, plan when unblocked

Unblock condition: `codex/postgres-delivered-diffs` merges and the quarantined
files are stable. A monitor is armed for this. Revised order, ladder removed:

1. **Commit trailer** in `agent/harvest` (tree-identical amend before push).
   Highest value per line of code — independently fixes squash detection for
   merges performed outside the platform.
2. **Staleness surfacing** — `merge.Staleness` is already built; this is only
   plumbing it to the ticket page. No mutation.
3. **Merge step + runner** — a pod-side entry point calling `merge.Prepare`,
   running gates on the result, then pushing. Mirrors `cmd/harvest-runner`.
4. **Merge route + handler** — triggers the step, records the outcome from what
   the platform actually did.
5. **Elm merge button** — serialize against in-flight UX4 Elm work; not
   gate-verifiable, so do not dispatch to the loop.

Migration `1773106096` is reserved and **must land after codex's `1773106095`**.
It is needed only once step 4 records something new; steps 1–3 need no schema
change.
