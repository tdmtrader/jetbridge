# WS5 — Contract Conformance: Close the Rule-to-Test Loop

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make "every documented semantic rule is proven to reject something" a mechanically enforced property rather than a hope. The six schema documents already enumerate all 52 `go_only_rules` entries; this plan builds the harness that demands a *rejection witness* per rule and runs each witness through the real `AdmitForSeal` gate, fills the enumerated per-type negative-test holes, makes the hard scale limits injectable and exercises them at their boundaries, and closes the invalid-UTF-8 free-text hole at the seal gate only.

**Architecture:** Everything lands in `agent/snapshot/contracts`. A new external test file (`package contracts_test`) holds the linkage harness: it reads the declared rule union out of `Registry.Types()` + `SchemaDocumentFor` (both already exported, so no production API is added), keys a witness registry on `(record type, rule id)`, and drives each witness through `Registry.Lookup(ref).AdmitForSeal` over a real `os.Root` — the same driver `helpers_test.go`'s `validateFiles` already uses. A new internal test file (`package contracts`) holds the limit-boundary and scale tests, because the limits become unexported package-level variables. One production change lands: a seal-only text-encoding gate over the exact bytes of a candidate `record.json`.

**Tech Stack:** Go, plain `testing` (no Ginkgo, no new dependencies), the existing `contracts` fixture builders, `git` on `PATH` for the repository-change witnesses.

## Global Constraints

- **No file under `agent/snapshot/contracts/schemas/` may be created, edited, renamed, or deleted by this plan.** A `rev2` file's bytes are a frozen descriptor; changing them changes the type's schema digest, and a descriptor bump is a data-loss-class event (exact-equality revalidation runs on three read paths). Every rule this plan adds is a Go gate rule and therefore does **not** get a `go_only_rules` entry.
- **Nothing may reject previously-sealed bytes at read time.** `RevalidateSealed`, `readSealedRecord`, `DecodeSealedRecord`, `ReadSealedRepositoryChangeRecord`, `ReadSealedSelectionRecord`, `ReadSealedMeasurementsRecord` and `DecodeSealedReviewRecord` are untouched by every task here. The one production change is confined to the seal-time entry points.
- Add no new third-party dependency. No property-testing framework, no mutation tool.
- No `t.Parallel()` anywhere in this package (there is none today). Two tasks mutate package-level variables and rely on that.
- Do not "fix" a validator to make a witness pass. If a witness does not reject, that is a finding: record it, and either pick the mutation that does reject and file the gap, or stop and escalate. The harness exists to find exactly this.
- Keep the existing test conventions: table-driven `t.Run` subtests, error assertions by `strings.Contains` on a distinctive fragment, `t.Fatalf("... = %v, want %q", err, want)` shape.

## Facts established by scouting (do not re-derive)

These were measured against the plan's base commit. Trust them; re-verify only if something contradicts them.

1. **The rule union is 52 `(type, rule)` pairs over 43 distinct rule ids.** Per document: `diagnosis/v1` 7, `measurements/v1` 9, `repository-change/v1` 12, `review/v1` 7, `selection/v1` 9, `validation/v1` 8. Three anchor rules (`anchor-subject-must-be-a-declared-subject`, `anchor-locator-kind-selects-which-fields-appear`, `anchor-locators-are-unverified`) appear in four documents each. **The harness keys on `(type, rule)`, not on the bare id** — the four anchor validators are four independent code paths and one witness cannot speak for all of them.
2. `SchemaDocumentFor(ref)` returns a document whose `GoOnlyRules` field is exported, and `Registry.Types()` plus `IsRecordType(ref)` are exported. The harness needs no new production API and lives in `package contracts_test`.
3. `maxJSONDocumentBytes` (`json.go:14`) has two read sites (`json.go:17`, `repository.go:159`); `maxRepositoryPayloadBytes` (`repository_change.go:19`) has four, all inside `spoolRepositoryPayload`. `decodeStrictDocument` has eight callers and `snapshot.Validator` is an interface in another package, so threading a `Limits` struct would touch every validator struct and every call site. **Converting the two constants to package-level `var` touches zero callers.**
4. **Go's `encoding/json` sanitizes rather than refuses.** A raw `0xff` inside a JSON string literal decodes to U+FFFD with a nil error; `"\ud800"` likewise. Therefore `utf8.ValidString` on a *decoded* body field can never fire, and a field-level check would be dead code. The gate must be byte-level over the raw `record.json`. Measured directly against the real seal gate: a `record.json` whose bytes are not valid UTF-8 is **accepted today**, and one carrying a lone `\ud800` escape is **accepted today**.
5. A 10,000-finding `review/v1` record is 1,490,393 bytes and validates through the real seal gate in **33 ms**. It exceeds the 1 MiB document limit, so the scale test must raise that limit — which is itself the finding that the document limit is currently the *only* bound on entity-set cardinality.
6. Every error fragment quoted in the witness tables below was produced by running the mutation through the real gate. They are copied from real output, not guessed.

---

### Task 1: The linkage harness, and review/v1's seven witnesses

**Files:**
- Create: `agent/snapshot/contracts/schema_rule_witness_test.go`

The harness lands with a `pendingRules` allowlist so this task is independently landable and green while five of the six witness tables are still unwritten. Task 7 empties and **deletes** that allowlist, after which a rule with no witness is permanently red.

- [x] Create `agent/snapshot/contracts/schema_rule_witness_test.go` in `package contracts_test` with the harness and the review table below, but leave `reviewRuleWitnesses` returning `nil` for the first run.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness' -v -count=1` and confirm it fails, listing all seven review rules:

```
--- FAIL: TestEveryGoOnlyRuleHasARejectionWitness (0.00s)
    schema_rule_witness_test.go:78: "review/v1" declares go rule "accept-forbids-any-blocking-finding" but no witness discharges it and it is not listed as pending
    schema_rule_witness_test.go:78: "review/v1" declares go rule "anchor-locator-kind-selects-which-fields-appear" but no witness discharges it and it is not listed as pending
    ... (7 lines)
FAIL
```

- [x] Write the harness exactly as follows.

```go
package contracts_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// THE LINKAGE HARNESS.
//
// The `go_only_rules` array in each schema document is a maintained enumeration
// of every semantic rule the declared schema deliberately does NOT express.
// TestSchemaDocumentGoRuleReferencesResolve already proves those references
// resolve. Nothing proved that any of them REJECTS anything, which is the one
// blind spot the parity gate structurally cannot cover: parity is differential,
// so a rule both descriptions have wrong is invisible to it.
//
// This test closes that loop. Every declared rule must have a registered
// witness, and every witness is driven through the REAL AdmitForSeal gate over a
// real directory. Adding a rule to a schema document without adding its witness
// turns CI red.
//
// The registry is keyed on (record type, rule id) rather than on the rule id
// alone. Three anchor rules appear in four documents each, and those are four
// separate Go code paths over four different body shapes; one witness speaking
// for all four would be exactly the kind of assumed coverage this test exists to
// remove.
const declaredGoOnlyRuleCount = 52

// witnessCandidate is the candidate tree a step is claiming to have written,
// plus the declarations the server holds for that step. Exactly one of files and
// dir is set: files is the ordinary case, dir is for a witness needing a shape a
// map of bytes cannot express — a directory or a symlink where a regular file
// belongs.
type witnessCandidate struct {
	files        map[string][]byte
	dir          string
	declarations snapshot.ValidationContext
}

// ruleWitness discharges one declared rule.
//
// A witness is normally a REJECTION witness: build an otherwise-valid instance
// with exactly one thing wrong, and name a fragment the real gate's error must
// contain. A handful of rules deliberately reject nothing — they document a
// non-check, or a fact about where authority comes from — and those carry
// `documented` instead, which is a written justification the reader can audit,
// plus an optional `pin` that turns the documented claim into an assertion.
type ruleWitness struct {
	rule string

	build   func(t *testing.T) witnessCandidate
	wantErr string

	documented string
	pin        func(t *testing.T)
}

func (candidate witnessCandidate) admit(t *testing.T, ref snapshot.TypeRef) error {
	t.Helper()
	dir := candidate.dir
	if dir == "" {
		dir = writeTree(t, candidate.files)
	}
	_, err := validateDirectory(t, ref.String(), dir, candidate.declarations)
	return err
}

// pendingRules is the staging mechanism that lets the harness land before every
// witness table is written. It is deleted, along with every reference to it, by
// the task that finishes the last table. Do not add to it.
var pendingRules = map[snapshot.TypeRef][]string{}

func TestEveryGoOnlyRuleHasARejectionWitness(t *testing.T) {
	declared := declaredGoOnlyRules(t)
	total := 0
	for _, rules := range declared {
		total += len(rules)
	}
	if total != declaredGoOnlyRuleCount {
		t.Fatalf(
			"the six schema documents declare %d go rules, want %d; a rule was added or removed, so update declaredGoOnlyRuleCount in the same change that adds or removes its witness",
			total, declaredGoOnlyRuleCount,
		)
	}

	witnesses := ruleWitnesses(t)
	for ref, entries := range witnesses {
		declaredIDs := make(map[string]struct{}, len(declared[ref]))
		for _, rule := range declared[ref] {
			declaredIDs[rule.ID] = struct{}{}
		}
		seen := make(map[string]struct{}, len(entries))
		for _, entry := range entries {
			if _, found := declaredIDs[entry.rule]; !found {
				t.Errorf("%q has a witness for %q, which the document does not declare; a renamed rule leaves its witness behind", ref, entry.rule)
			}
			if _, duplicate := seen[entry.rule]; duplicate {
				t.Errorf("%q has two witnesses for %q; one rule, one witness", ref, entry.rule)
			}
			seen[entry.rule] = struct{}{}
			if (entry.build == nil) == (entry.documented == "") {
				t.Errorf("%q witness for %q must set exactly one of build+wantErr and documented", ref, entry.rule)
			}
			if entry.build != nil && strings.TrimSpace(entry.wantErr) == "" {
				t.Errorf("%q witness for %q builds an invalid instance but names no expected error fragment", ref, entry.rule)
			}
		}
	}

	for _, ref := range sortedTypeRefs(declared) {
		for _, rule := range declared[ref] {
			entry, found := witnessFor(witnesses[ref], rule.ID)
			if !found {
				if isPendingRule(ref, rule.ID) {
					t.Logf("%q rule %q is PENDING a witness", ref, rule.ID)
					continue
				}
				t.Errorf("%q declares go rule %q but no witness discharges it and it is not listed as pending", ref, rule.ID)
				continue
			}
			if isPendingRule(ref, rule.ID) {
				t.Errorf("%q rule %q has a witness and is still listed as pending; remove it from pendingRules", ref, rule.ID)
			}
			t.Run(fmt.Sprintf("%s/%s", ref, rule.ID), func(t *testing.T) {
				if entry.documented != "" {
					if entry.pin != nil {
						entry.pin(t)
					}
					return
				}
				err := entry.build(t).admit(t, ref)
				if err == nil {
					t.Fatalf(
						"the seal gate ACCEPTED the witness for %q; the rule the document declares is not enforced, which is a finding and not a test to relax",
						rule.ID,
					)
				}
				if !strings.Contains(err.Error(), entry.wantErr) {
					t.Fatalf("seal gate error = %v, want it to contain %q", err, entry.wantErr)
				}
			})
		}
	}
}

func declaredGoOnlyRules(t *testing.T) map[snapshot.TypeRef][]contracts.GoOnlyRule {
	t.Helper()
	registry, err := contracts.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	declared := make(map[snapshot.TypeRef][]contracts.GoOnlyRule)
	for _, ref := range registry.Types() {
		if !contracts.IsRecordType(ref) {
			continue
		}
		document, found := contracts.SchemaDocumentFor(ref)
		if !found {
			t.Fatalf("%q is a record type with no schema document", ref)
		}
		declared[ref] = document.GoOnlyRules
	}
	return declared
}

func witnessFor(entries []ruleWitness, rule string) (ruleWitness, bool) {
	for _, entry := range entries {
		if entry.rule == rule {
			return entry, true
		}
	}
	return ruleWitness{}, false
}

func isPendingRule(ref snapshot.TypeRef, rule string) bool {
	for _, pending := range pendingRules[ref] {
		if pending == rule {
			return true
		}
	}
	return false
}

func sortedTypeRefs[T any](index map[snapshot.TypeRef]T) []snapshot.TypeRef {
	refs := make([]snapshot.TypeRef, 0, len(index))
	for ref := range index {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(left, right int) bool { return refs[left] < refs[right] })
	return refs
}

// ruleWitnesses is the registry. One function per record type, so a type's table
// is one reviewable unit.
func ruleWitnesses(t *testing.T) map[snapshot.TypeRef][]ruleWitness {
	t.Helper()
	return map[snapshot.TypeRef][]ruleWitness{
		snapshot.TypeRef("review/v1"): reviewRuleWitnesses(t),
	}
}
```

- [x] Populate `pendingRules` with the five tables not yet written, so this task lands green. Each later task deletes its own entry.

```go
var pendingRules = map[snapshot.TypeRef][]string{
	snapshot.TypeRef("validation/v1"): {
		"conclusion-is-recomputed-from-the-checks",
		"status-governs-attempt-count",
		"attempt-number-equals-its-position-plus-one",
		"check-status-equals-the-final-attempt-status",
		"attempts-have-no-stable-id",
		"anchor-subject-must-be-a-declared-subject",
		"anchor-locator-kind-selects-which-fields-appear",
		"anchor-locators-are-unverified",
	},
	snapshot.TypeRef("measurements/v1"): {
		"conclusion-governs-the-metric-count",
		"partial-and-not-applicable-require-an-explanation",
		"direction-governs-target",
		"bounds-are-declared-together-finite-and-ordered",
		"value-must-lie-within-any-declared-bounds",
		"a-metric-is-not-a-score",
		"anchor-subject-must-be-a-declared-subject",
		"anchor-locator-kind-selects-which-fields-appear",
		"anchor-locators-are-unverified",
	},
	snapshot.TypeRef("diagnosis/v1"): {
		"identified-and-suspected-require-hypotheses",
		"hypothesis-ranks-are-unique-and-contiguous-from-one",
		"identified-requires-evidence-on-the-rank-one-hypothesis",
		"addresses-must-name-a-hypothesis-this-record-declares",
		"anchor-subject-must-be-a-declared-subject",
		"anchor-locator-kind-selects-which-fields-appear",
		"anchor-locators-are-unverified",
	},
	snapshot.TypeRef("selection/v1"): {
		"candidacy-is-declared-by-the-platform-and-sourced-differently-at-each-gate",
		"every-candidate-port-has-exactly-one-subject-and-no-others-appear",
		"candidate-subjects-share-one-snapshot-type",
		"candidates-must-assess-every-candidate-subject-exactly-once",
		"each-candidate-id-must-be-a-declared-candidate-subject",
		"candidate-ranks-are-unique-within-one-to-the-candidate-count",
		"selected-must-occur-exactly-once-among-the-candidates",
		"score-internal-consistency",
		"resolving-the-choice-is-a-seal-time-operation",
	},
	snapshot.TypeRef("repository-change/v1"): {
		"repository-id-must-be-a-snapshot-digest",
		"repository-id-must-equal-the-base-repository-id",
		"base-sha-width-selects-the-object-format",
		"base-sha-must-equal-the-base-repository-head",
		"declared-object-ids-must-share-the-base-object-format",
		"representation-governs-result-commit",
		"payload-must-be-a-regular-file-within-the-size-limit",
		"payload-digest-must-equal-the-exact-payload-bytes",
		"result-tree-must-equal-the-recomputed-tree",
		"result-commit-must-descend-from-base-sha",
		"the-change-must-verify-against-the-base-repository",
		"changed-files-is-not-a-body-field",
	},
}
```

- [x] Write review/v1's seven witnesses. The shared builder and the table:

```go
// reviewWitnessBase is one accepted review/v1 candidate. Every witness starts
// from it and breaks exactly one thing, so the error a witness asserts is
// attributable to the rule it discharges and to nothing else.
func reviewWitnessBase(t *testing.T) (snapshot.ValidationContext, []contracts.Subject, contracts.ReviewBody) {
	t.Helper()
	ref := snapshot.SnapshotRef{
		ID: 31, Type: mustTypeRef(t, "repository-change/v1"), Digest: recordDigest('a'),
	}
	declarations := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": ref})
	subjects := []contracts.Subject{
		contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "change", ref),
	}
	body := contracts.ReviewBody{
		Conclusion: "changes-required",
		Summary:    "one blocking defect",
		Findings: []contracts.Finding{{
			ID: "f-1", Severity: "high", Blocking: true,
			Category: "correctness", Title: "unsafe race", Description: "concurrent writes race",
			Evidence: []contracts.Anchor{{
				Subject: "primary",
				Locator: contracts.Locator{Kind: "file-lines", Path: "main.go", Start: intPointer(12), End: intPointer(18)},
			}},
		}},
	}
	return declarations, subjects, body
}

func reviewWitness(t *testing.T, mutate func(*contracts.ReviewBody)) witnessCandidate {
	t.Helper()
	declarations, subjects, body := reviewWitnessBase(t)
	mutate(&body)
	record, err := contracts.NewRecord(mustTypeRef(t, "review/v1"), subjects, body)
	if err != nil {
		t.Fatalf("NewRecord(review/v1): %v", err)
	}
	return witnessCandidate{
		files:        map[string][]byte{"record.json": marshalRecord(t, record)},
		declarations: declarations,
	}
}

func reviewRuleWitnesses(t *testing.T) []ruleWitness {
	t.Helper()
	return []ruleWitness{
		{
			rule: "changes-required-requires-a-blocking-finding",
			build: func(t *testing.T) witnessCandidate {
				return reviewWitness(t, func(body *contracts.ReviewBody) {
					body.Findings[0].Severity = "low"
					body.Findings[0].Blocking = false
				})
			},
			wantErr: "changes-required conclusion requires at least one blocking finding",
		},
		{
			rule: "accept-forbids-any-blocking-finding",
			build: func(t *testing.T) witnessCandidate {
				return reviewWitness(t, func(body *contracts.ReviewBody) {
					body.Conclusion = "accept"
				})
			},
			wantErr: "accept conclusion cannot contain a blocking finding",
		},
		{
			rule: "severity-constrains-blocking",
			build: func(t *testing.T) witnessCandidate {
				return reviewWitness(t, func(body *contracts.ReviewBody) {
					body.Findings[0].Severity = "observation"
				})
			},
			wantErr: "observation finding cannot be blocking",
		},
		{
			rule: "non-observation-finding-requires-evidence",
			build: func(t *testing.T) witnessCandidate {
				return reviewWitness(t, func(body *contracts.ReviewBody) {
					body.Findings[0].Evidence = nil
				})
			},
			wantErr: "non-observation finding evidence is required",
		},
		{
			rule: "anchor-subject-must-be-a-declared-subject",
			build: func(t *testing.T) witnessCandidate {
				return reviewWitness(t, func(body *contracts.ReviewBody) {
					body.Findings[0].Evidence[0].Subject = "ghost"
				})
			},
			wantErr: `body/findings/*/evidence/*/subject: "ghost" is not declared by this record`,
		},
		{
			rule: "anchor-locator-kind-selects-which-fields-appear",
			build: func(t *testing.T) witnessCandidate {
				return reviewWitness(t, func(body *contracts.ReviewBody) {
					body.Findings[0].Evidence[0].Locator.Pointer = "/findings/0"
				})
			},
			wantErr: "file-lines anchor contains fields for another locator kind",
		},
		{
			rule: "anchor-locators-are-unverified",
			documented: "This rule documents a deliberate NON-check: nothing resolves an anchor's " +
				"locator against any content, because anchor content hashes are deferred. It has no " +
				"rejection witness by construction. The pin makes the claim executable instead: an " +
				"anchor naming a path that exists nowhere, at lines nothing has, is ACCEPTED.",
			pin: func(t *testing.T) {
				candidate := reviewWitness(t, func(body *contracts.ReviewBody) {
					body.Findings[0].Evidence[0].Locator.Path = "does/not/exist.go"
					body.Findings[0].Evidence[0].Locator.Start = intPointer(400000)
					body.Findings[0].Evidence[0].Locator.End = intPointer(400001)
				})
				if err := candidate.admit(t, snapshot.TypeRef("review/v1")); err != nil {
					t.Fatalf(
						"the seal gate rejected an unresolvable anchor locator: %v; if locators are now verified, this rule's documentation is stale and the schema document is the thing to change",
						err,
					)
				}
			},
		},
	}
}
```

- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness' -v -count=1` and confirm seven `review/v1` subtests pass and 45 rules log as `PENDING`.
- [x] Deliberately break the loop once to prove it bites: comment out the `accept-forbids-any-blocking-finding` entry, re-run, and confirm `"review/v1" declares go rule "accept-forbids-any-blocking-finding" but no witness discharges it and it is not listed as pending`. Restore it.
- [x] Run `gofmt -l agent/snapshot/contracts` and confirm no output.
- [x] Commit `test(contracts): require a rejection witness per declared go rule`.

---

### Task 2: validation/v1 — eight witnesses

**Files:**
- Modify: `agent/snapshot/contracts/schema_rule_witness_test.go`

Witness mutations are chosen to be *different arms* of their rule from the named hole tests in Task 9, so the two do not duplicate each other: the witness for `status-governs-attempt-count` uses the skipped arm, and Task 9's named test uses the non-skipped arm.

| Rule id | Mutation | Expected error fragment |
|---|---|---|
| `conclusion-is-recomputed-from-the-checks` | `body.Conclusion = "passed"` while a check is `failed` | `conclusion must match derived conclusion "failed"` |
| `status-governs-attempt-count` | `body.Checks[0].Status = "skipped"` and `body.Conclusion = "incomplete"`, keeping the attempt | `skipped check must have no attempts` |
| `attempt-number-equals-its-position-plus-one` | `body.Checks[0].Attempts[0].Number = 2` | `attempt numbers must be contiguous from 1` |
| `check-status-equals-the-final-attempt-status` | append `ValidationAttempt{Number: 2, Status: "passed", Duration: "1s"}` to a `failed` check | `check status "failed" must match final attempt status "passed"` |
| `attempts-have-no-stable-id` | documented + pin | — |
| `anchor-subject-must-be-a-declared-subject` | `Attempts[0].Evidence[0].Subject = "ghost"` | `body/checks/*/attempts/*/evidence/*/subject: "ghost" is not declared by this record` |
| `anchor-locator-kind-selects-which-fields-appear` | replace the evidence anchor with `Locator{Kind: "json-pointer", Pointer: "/checks/0", Path: "a.go"}` | `json-pointer anchor contains fields for another locator kind` |
| `anchor-locators-are-unverified` | documented + pin (path `does/not/exist.go`, lines 400000–400001, must be ACCEPTED) | — |

- [x] Add `validationWitnessBase` and `validationWitness` mirroring the review helpers. The accepted base is one `failed` check with one `failed` attempt carrying one file-lines anchor, `Conclusion: "failed"`, `Summary: "one suite fails"`, and one primary subject bound to input `in` of type `repository/v1`.
- [x] Write the eight entries. The two non-obvious ones:

```go
{
	rule: "attempts-have-no-stable-id",
	documented: "Attempts are addressable only through declaration paths, which is a fact " +
		"about the DECLARATION rather than a rule that rejects an instance: there is no id " +
		"field to get wrong. The pin asserts the absence directly, so adding one becomes a " +
		"visible contract change rather than a quiet convenience.",
	pin: func(t *testing.T) {
		document, found := contracts.SchemaDocumentFor(snapshot.TypeRef("validation/v1"))
		if !found {
			t.Fatal("validation/v1 has no schema document")
		}
		for path := range document.Fields {
			if strings.HasPrefix(path, "body/checks/*/attempts/*/") && strings.HasSuffix(path, "/id") {
				t.Fatalf("validation/v1 declares %q; attempts now have a stable id and the rule text is stale", path)
			}
		}
	},
},
{
	rule: "check-status-equals-the-final-attempt-status",
	build: func(t *testing.T) witnessCandidate {
		return validationWitness(t, func(body *contracts.ValidationBody) {
			body.Checks[0].Attempts = append(body.Checks[0].Attempts, contracts.ValidationAttempt{
				Number: 2, Status: "passed", Duration: "1s",
			})
		})
	},
	wantErr: `check status "failed" must match final attempt status "passed"`,
},
```

- [x] Register `snapshot.TypeRef("validation/v1"): validationRuleWitnesses(t)` in `ruleWitnesses` and delete the `validation/v1` entry from `pendingRules`.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness/validation' -v -count=1` and confirm eight passing subtests.
- [x] Commit `test(contracts): witness every validation/v1 go rule`.

---

### Task 3: measurements/v1 — nine witnesses

**Files:**
- Modify: `agent/snapshot/contracts/schema_rule_witness_test.go`

`measurements/v1` accepts a record with no subjects at all, so the accepted base uses `emptyValidationContext(t)` and `nil` subjects; the two anchor witnesses need a subject, so they use a one-primary base bound to input `in`.

| Rule id | Mutation | Expected error fragment |
|---|---|---|
| `conclusion-governs-the-metric-count` | `body.Metrics = nil` with `Conclusion: "measured"` | `measured conclusion requires at least one metric` |
| `partial-and-not-applicable-require-an-explanation` | `Conclusion = "partial"`, keep the metric, leave `Explanation` empty | `partial conclusion requires an explanation` |
| `direction-governs-target` | `Metrics[0].Target = floatPointer(3)` on a `lower-is-better` metric | `measurement target is valid only for target direction` |
| `bounds-are-declared-together-finite-and-ordered` | `Metrics[0].Minimum = floatPointer(0)` with no maximum | `measurement minimum and maximum must be declared together` |
| `value-must-lie-within-any-declared-bounds` | bounds `0..1` around the base value `1.5` | `measurement value must be within its declared bounds` |
| `a-metric-is-not-a-score` | documented + pin | — |
| `anchor-subject-must-be-a-declared-subject` | `Metrics[0].Evidence = []Anchor{{Subject: "ghost", Locator: Locator{Kind: "opaque", Value: "build log line 44"}}}` | `body/metrics/*/evidence/*/subject: "ghost" is not declared by this record` |
| `anchor-locator-kind-selects-which-fields-appear` | opaque anchor also carrying `Path: "a.go"` | `opaque anchor contains fields for another locator kind` |
| `anchor-locators-are-unverified` | documented + pin | — |

- [x] Write the pin for `a-metric-is-not-a-score`, which is a claim about the declaration:

```go
{
	rule: "a-metric-is-not-a-score",
	documented: "A metric carries a direction and bounds but no scale, so it is deliberately " +
		"NOT declared as the score kind. Declaring it as one would claim a scale that no field " +
		"carries and no validator checks. There is no instance that violates this — the Go type " +
		"has no scale field to set — so the pin asserts the declaration instead.",
	pin: func(t *testing.T) {
		document, found := contracts.SchemaDocumentFor(snapshot.TypeRef("measurements/v1"))
		if !found {
			t.Fatal("measurements/v1 has no schema document")
		}
		element, declared := document.Fields["body/metrics/*"]
		if !declared {
			t.Fatal("measurements/v1 declares no body/metrics/* element")
		}
		if element.Kind == contracts.KindScore {
			t.Fatal("measurements/v1 now declares its metric element as a score, claiming a scale no field carries")
		}
		if _, found := document.Fields["body/metrics/*/scale"]; found {
			t.Fatal("measurements/v1 now declares body/metrics/*/scale; a metric with a scale is a score and the rule text is stale")
		}
	},
},
```

- [x] Register the table and delete the `measurements/v1` entry from `pendingRules`.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness/measurements' -v -count=1` and confirm nine passing subtests.
- [x] Commit `test(contracts): witness every measurements/v1 go rule`.

---

### Task 4: diagnosis/v1 — seven witnesses

**Files:**
- Modify: `agent/snapshot/contracts/schema_rule_witness_test.go`

The accepted base: `Conclusion: "identified"`, `Summary: "the lock is taken twice on one path"`, two hypotheses (`h-1` rank 1 with one opaque anchor and a `unit-interval`/`higher-is-better` confidence of 0.9; `h-2` rank 2 with the same confidence shape and no evidence), one action `a-1` with `Priority: "immediate"` addressing `h-1`, and one primary subject bound to input `in`.

| Rule id | Mutation | Expected error fragment |
|---|---|---|
| `identified-and-suspected-require-hypotheses` | `body.Hypotheses = nil; body.Actions = nil` | `identified conclusion requires hypotheses` |
| `hypothesis-ranks-are-unique-and-contiguous-from-one` | `body.Hypotheses[1].Rank = 1` | `hypotheses[1].rank 1 is duplicate` |
| `identified-requires-evidence-on-the-rank-one-hypothesis` | `body.Hypotheses[0].Evidence = nil` | `identified conclusion requires evidence for the rank-1 hypothesis` |
| `addresses-must-name-a-hypothesis-this-record-declares` | `body.Actions[0].Addresses = []string{"h-9"}` | `action addresses unknown hypothesis "h-9"` |
| `anchor-subject-must-be-a-declared-subject` | `body.Hypotheses[0].Evidence[0].Subject = "ghost"` | `body/hypotheses/*/evidence/*/subject: "ghost" is not declared by this record` |
| `anchor-locator-kind-selects-which-fields-appear` | replace with `Locator{Kind: "byte-range", Path: "a.go", Start: intPointer(5), End: intPointer(5)}` | `byte-range anchor requires nonnegative start and end > start` |
| `anchor-locators-are-unverified` | documented + pin | — |

- [x] Write the seven entries with `diagnosisWitnessBase`/`diagnosisWitness` helpers mirroring the review pair.
- [x] Register the table and delete the `diagnosis/v1` entry from `pendingRules`.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness/diagnosis' -v -count=1` and confirm seven passing subtests.
- [x] Commit `test(contracts): witness every diagnosis/v1 go rule`.

---

### Task 5: selection/v1 — nine witnesses

**Files:**
- Modify: `agent/snapshot/contracts/schema_rule_witness_test.go`

Selection is the one type whose gate consults server-declared candidate ports, so the base uses `candidateValidationContextFor(t, inputs, "left", "right")`. All refs share one snapshot type (`repository-change/v1`) except where the uniformity rule is under test — `selection/v1` declares `uniform_subject_type`, so a foreign type anywhere else would pre-empt the rule being witnessed. **Subjects must be lexicographically sorted by id** or `NewRecord` fails before the gate is reached.

Accepted base: subjects `left` and `right` (both candidate role, bound to the identically named inputs), body `Selected: "right"`, candidates `[{left, rank 2}, {right, rank 1}]`, `Rationale: "right wins"`.

| Rule id | Mutation | Expected error fragment |
|---|---|---|
| `candidacy-is-declared-by-the-platform-and-sourced-differently-at-each-gate` | declare only `"left"` as a candidate port, and have the producer write a second candidate-role subject `rubric` bound to a non-candidate input of the same type | `is not a declared candidate port` |
| `every-candidate-port-has-exactly-one-subject-and-no-others-appear` | drop the `right` subject, leaving `"right"` declared as a port | `declared candidate port "right" has no selection subject` |
| `candidate-subjects-share-one-snapshot-type` | bind the `right` subject to a `review/v1` ref | `requires every subject to share one snapshot type` |
| `candidates-must-assess-every-candidate-subject-exactly-once` | keep both subjects, cut `Candidates` to `[{left, rank 1}]`, `Selected: "left"` | `candidates must assess every candidate subject exactly once` |
| `each-candidate-id-must-be-a-declared-candidate-subject` | `Candidates[0].ID = "ghost"` | `is not a declared candidate subject` |
| `candidate-ranks-are-unique-within-one-to-the-candidate-count` | `Candidates[0].Rank = 1` | `candidates[1].rank 1 is duplicate` |
| `selected-must-occur-exactly-once-among-the-candidates` | `Selected = "nobody"` | `selected candidate must occur exactly once in candidates` |
| `score-internal-consistency` | `Candidates[0].Scores = []NamedScore{{ID: "s-1", Score: Score{Value: 2, Scale: "unit-interval", Direction: "higher-is-better"}}}` | `unit-interval score value must be within 0..1` |
| `resolving-the-choice-is-a-seal-time-operation` | documented + pin | — |

- [x] The producer-claimed-candidacy witness needs a hand-built record rather than `NewRecord`, matching the existing `TestSealTimeSelectionIgnoresProducerClaimedCandidateRoles`:

```go
{
	rule: "candidacy-is-declared-by-the-platform-and-sourced-differently-at-each-gate",
	build: func(t *testing.T) witnessCandidate {
		changeType := mustTypeRef(t, "repository-change/v1")
		left := snapshot.SnapshotRef{ID: 81, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("c", 64))}
		rubric := snapshot.SnapshotRef{ID: 82, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("d", 64))}
		// The step declares exactly one candidate port. The producer writes both of
		// its inputs as candidate subjects, promoting the rubric it was handed for
		// context into something selectable. Seal-time candidacy comes from the
		// server's port declarations, so the claim buys nothing.
		declarations := candidateValidationContextFor(
			t, map[string]snapshot.SnapshotRef{"left": left, "rubric": rubric}, "left",
		)
		record := contracts.Record[contracts.SelectionBody]{
			RecordVersion: contracts.RecordVersion,
			Type:          mustTypeRef(t, "selection/v1"),
			Schema:        mustSelectionSchema(t),
			Subjects: []contracts.Subject{
				contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
				contracts.SubjectFromInput("rubric", contracts.SubjectRoleCandidate, "rubric", rubric),
			},
			Body: contracts.SelectionBody{
				Selected: "rubric",
				Candidates: []contracts.CandidateAssessment{
					{ID: "left", Rank: 2, Summary: "the only declared candidate"},
					{ID: "rubric", Rank: 1, Summary: "context promoted to a candidate"},
				},
				Rationale: "claiming candidacy for an input that was never a candidate port",
			},
		}
		return witnessCandidate{
			files:        map[string][]byte{"record.json": marshalRecord(t, record)},
			declarations: declarations,
		}
	},
	wantErr: "is not a declared candidate port",
},
```

- [x] Write the documented pin for `resolving-the-choice-is-a-seal-time-operation`, which is the read-time half of the same split:

```go
{
	rule: "resolving-the-choice-is-a-seal-time-operation",
	documented: "Turning a selection into a live snapshot reference needs the step's input " +
		"bindings, which exist only while the step runs, so this rule rejects no instance — it " +
		"says where the operation lives. The pin is its observable consequence: a stored " +
		"selection revalidates with NO declarations at all, because a reader reads the chosen " +
		"subject's type and digest out of the sealed bytes instead of resolving anything.",
	pin: func(t *testing.T) {
		files := acceptedSelectionFiles(t)
		if _, err := revalidateSealedFiles(t, "selection/v1", files, emptyValidationContext(t)); err != nil {
			t.Fatalf("read-time revalidation of a stored selection needed declarations: %v", err)
		}
	},
},
```

- [x] Add `acceptedSelectionFiles(t)` returning the accepted base's `map[string][]byte`, so the pin and the rejection witnesses share one definition of "valid".
- [x] Register the table and delete the `selection/v1` entry from `pendingRules`.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness/selection' -v -count=1` and confirm nine passing subtests.
- [x] Commit `test(contracts): witness every selection/v1 go rule`.

---

### Task 6: repository-change/v1 — twelve witnesses

**Files:**
- Modify: `agent/snapshot/contracts/schema_rule_witness_test.go`

These are the only witnesses that need real Git. Reuse `repository_test.go`'s existing builders by exact name: `newGitRepository`, `canonicalRepositoryArchive`, `repositoryMetadataForDirectory`, `repositoryMetadataForDirectoryRevision`, `repositoryInputContext`, `repositoryChangeFiles`, `validChangeDocument`, `repositoryChangeFixture`, `runTestGit`, `writeTestFile`, `digestBytes`. Build the shared base repository and patch **once** in `repositoryChangeRuleWitnesses(t)` and close over them, so eleven of the twelve witnesses cost no extra `git init` (the ancestry witness needs its own history).

```go
// repositoryChangeWitnessFixture is the one base repository every
// repository-change witness shares. Building it once matters: each witness
// otherwise pays a git init, a commit, a canonical capture and an fsck.
type repositoryChangeWitnessFixture struct {
	baseArchive  []byte
	baseDigest   snapshot.Digest
	baseMetadata contracts.RepositoryMetadata
	patch        []byte
	resultTree   string
	declarations snapshot.ValidationContext
}

func newRepositoryChangeWitnessFixture(t *testing.T) repositoryChangeWitnessFixture {
	t.Helper()
	base := newGitRepository(t, "")
	baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
	baseMetadata := repositoryMetadataForDirectory(t, base)

	writeTestFile(t, filepath.Join(base, "README.md"), "patched\n")
	patch := []byte(runTestGit(t, base, "diff", "--binary", "--no-ext-diff") + "\n")
	runTestGit(t, base, "add", "README.md")
	resultTree := runTestGit(t, base, "write-tree")
	// Put the working tree back: canonicalRepositoryArchive already captured the
	// bytes the declarations bind to, and leaving it dirty would make any later
	// capture of this directory fail the cleanliness rule.
	runTestGit(t, base, "reset", "-q", "--hard", "HEAD")

	return repositoryChangeWitnessFixture{
		baseArchive: baseArchive, baseDigest: baseDigest, baseMetadata: baseMetadata,
		patch: patch, resultTree: resultTree,
		declarations: repositoryInputContext(t, "base", baseArchive, baseDigest),
	}
}

// accepted is the valid patch change every witness starts from.
func (fixture repositoryChangeWitnessFixture) accepted() repositoryChangeFixture {
	return repositoryChangeFixture{
		SchemaVersion: "1.0.0", RepositoryID: fixture.baseMetadata.RepositoryID, BaseInput: "base",
		BaseSHA: fixture.baseMetadata.HeadSHA, ResultTreeSHA: fixture.resultTree,
		Representation: "patch", PayloadPath: "change.patch", PayloadDigest: digestBytes(fixture.patch),
	}
}

func (fixture repositoryChangeWitnessFixture) witness(
	t *testing.T, mutate func(*repositoryChangeFixture),
) witnessCandidate {
	t.Helper()
	document := fixture.accepted()
	mutate(&document)
	return witnessCandidate{
		files:        repositoryChangeFiles(t, document, "change.patch", fixture.patch, fixture.baseDigest),
		declarations: fixture.declarations,
	}
}
```

| Rule id | Mutation | Expected error fragment |
|---|---|---|
| `repository-id-must-be-a-snapshot-digest` | `document.RepositoryID = "not-a-digest"` | `repository_id` |
| `repository-id-must-equal-the-base-repository-id` | `document.RepositoryID = "sha256:" + strings.Repeat("9", 64)` | `repository_id does not match base repository` |
| `base-sha-width-selects-the-object-format` | `document.BaseSHA = strings.Repeat("a", 41)` | `base_sha: object ID must be a full sha1 or sha256 hexadecimal value` |
| `base-sha-must-equal-the-base-repository-head` | `document.BaseSHA = strings.Repeat("f", 40)` | `base_sha does not match base repository HEAD` |
| `declared-object-ids-must-share-the-base-object-format` | `document.ResultTreeSHA = strings.Repeat("b", 64)` under a sha1 base | `result_tree: object ID must contain 40 lowercase hexadecimal characters` |
| `representation-governs-result-commit` | `document.ResultSHA = fixture.baseMetadata.HeadSHA` on a `patch` | `result_commit must be omitted for patch representation` |
| `payload-must-be-a-regular-file-within-the-size-limit` | build the accepted tree, then replace `content/change.patch` with a **directory** | `payload.path: payload must be a regular file` |
| `payload-digest-must-equal-the-exact-payload-bytes` | `document.PayloadDigest = "sha256:" + strings.Repeat("0", 64)` | `payload.digest does not match exact payload bytes` |
| `result-tree-must-equal-the-recomputed-tree` | `document.ResultTreeSHA = fixture.baseMetadata.TreeSHA` | `result_tree does not match the applied patch` |
| `result-commit-must-descend-from-base-sha` | sibling-branch bundle (below) | `bundle result_commit does not descend from base_sha` |
| `the-change-must-verify-against-the-base-repository` | payload bytes with `README.md` rewritten to `NOSUCH.md`, digest updated to match | `patch failed git apply --check --index` |
| `changed-files-is-not-a-body-field` | splice `"changed_files":["README.md"],` into the record's `"body":{` | `unknown field "changed_files"` |

- [x] Write the directory-payload witness, which is the one that needs `dir` rather than `files`:

```go
{
	rule: "payload-must-be-a-regular-file-within-the-size-limit",
	build: func(t *testing.T) witnessCandidate {
		candidate := fixture.witness(t, func(*repositoryChangeFixture) {})
		dir := writeTree(t, candidate.files)
		payload := filepath.Join(dir, "content", "change.patch")
		if err := os.Remove(payload); err != nil {
			t.Fatalf("remove payload: %v", err)
		}
		if err := os.Mkdir(payload, 0755); err != nil {
			t.Fatalf("replace payload with a directory: %v", err)
		}
		return witnessCandidate{dir: dir, declarations: fixture.declarations}
	},
	wantErr: "payload.path: payload must be a regular file",
},
```

- [x] Write the ancestry witness. A sibling commit off the **same root** keeps `repository_id` equal (identity is derived from the sorted root commits), so the ancestry rule is the only thing left to fire:

```go
{
	rule: "result-commit-must-descend-from-base-sha",
	build: func(t *testing.T) witnessCandidate {
		base := newGitRepository(t, "")
		root := runTestGit(t, base, "rev-list", "--max-parents=0", "HEAD")
		writeTestFile(t, filepath.Join(base, "README.md"), "second\n")
		runTestGit(t, base, "add", "README.md")
		runTestGit(t, base, "commit", "-q", "-m", "second")
		baseArchive, baseDigest := canonicalRepositoryArchive(t, base)
		baseMetadata := repositoryMetadataForDirectory(t, base)

		// A sibling off the SAME root commit: a valid object, the same repository
		// identity, and NOT a descendant of the base HEAD. Anything with a
		// different root would be refused as a different repository first, and the
		// ancestry rule would never be reached.
		runTestGit(t, base, "checkout", "-q", "-b", "sibling", root)
		writeTestFile(t, filepath.Join(base, "SIDE.md"), "side\n")
		runTestGit(t, base, "add", "SIDE.md")
		runTestGit(t, base, "commit", "-q", "-m", "sibling")
		sibling := repositoryMetadataForDirectoryRevision(t, base, "HEAD", baseMetadata.RepositoryID)

		bundlePath := filepath.Join(t.TempDir(), "sibling.bundle")
		runTestGit(t, base, "bundle", "create", bundlePath, "HEAD", "^"+root)
		bundle, err := os.ReadFile(bundlePath)
		if err != nil {
			t.Fatalf("read sibling bundle: %v", err)
		}
		document := validChangeDocument(baseMetadata, sibling, "git-bundle", "sibling.bundle", digestBytes(bundle))
		return witnessCandidate{
			files:        repositoryChangeFiles(t, document, "sibling.bundle", bundle, baseDigest),
			declarations: repositoryInputContext(t, "base", baseArchive, baseDigest),
		}
	},
	wantErr: "bundle result_commit does not descend from base_sha",
},
```

- [x] Write the `changed-files-is-not-a-body-field` witness, which mutates bytes rather than a struct because the Go type has no field to set:

```go
{
	rule: "changed-files-is-not-a-body-field",
	build: func(t *testing.T) witnessCandidate {
		candidate := fixture.witness(t, func(*repositoryChangeFixture) {})
		record := candidate.files["record.json"]
		smuggled := bytes.Replace(record, []byte(`"body":{`), []byte(`"body":{"changed_files":["README.md"],`), 1)
		if bytes.Equal(record, smuggled) {
			t.Fatal("record.json does not contain the expected body opening; the splice matched nothing")
		}
		candidate.files["record.json"] = smuggled
		return candidate
	},
	wantErr: `unknown field "changed_files"`,
},
```

- [x] Add `bytes`, `os` and `path/filepath` to the harness file's imports; the earlier tables needed none of them.
- [x] Register the table and delete the `repository-change/v1` entry from `pendingRules`.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness/repository-change' -v -count=1` and confirm twelve passing subtests in roughly ten seconds.
- [x] Commit `test(contracts): witness every repository-change/v1 go rule`.

---

### Task 7: Retire the pending-rule allowlist

**Files:**
- Modify: `agent/snapshot/contracts/schema_rule_witness_test.go`

- [x] Confirm `pendingRules` is now `map[snapshot.TypeRef][]string{}`.
- [x] Delete `pendingRules`, `isPendingRule`, and both call sites, so a declared rule with no witness fails unconditionally. Replace the missing-witness message with:

```go
t.Errorf(
	"%q declares go rule %q but no witness discharges it; a rule in a schema document is a promise that something is rejected, and this test is where that promise is paid",
	ref, rule.ID,
)
```

- [x] Add the total-coverage assertion so the count is checked from both directions:

```go
discharged := 0
for _, entries := range witnesses {
	discharged += len(entries)
}
if discharged != declaredGoOnlyRuleCount {
	t.Fatalf("witness registry discharges %d rules, the documents declare %d", discharged, declaredGoOnlyRuleCount)
}
```

- [x] Run `rg -n 'pendingRules|isPendingRule' agent/snapshot/contracts` and confirm no output.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness' -count=1` and confirm 52 subtests pass.
- [x] Delete one witness entry, re-run, confirm the failure names the rule, and restore it.
- [x] Commit `test(contracts): make a rule without a witness fail unconditionally`.

---

### Task 8: review/v1's enumerated negative-test holes

**Files:**
- Modify: `agent/snapshot/contracts/review_test.go`

The witness table is machine-checked coverage; these are the human-readable regression tests the audit named. Each is a distinct arm from its witness.

- [x] Add `TestReviewRecordRejectsDuplicateFindingIdentities`, `TestReviewRecordRejectsAnUndeclaredConclusion`, and `TestReviewRecordRequiresExactlyOnePrimarySubject` (covering both the zero and the two case) to `review_test.go`.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestReviewRecordRejects|TestReviewRecordRequiresExactly' -v -count=1` and confirm they fail to build:

```
# github.com/concourse/concourse/agent/snapshot/contracts_test
agent/snapshot/contracts/review_test.go:NN:6: undefined: ...
```

- [x] Implement them. The two-primary case needs two distinct inputs, and the subject ids must be lexicographically sorted:

```go
// A record with no primary and a record with two are the two ways to miss the
// one-primary shape, and they fail in different places: the declared subject
// shape refuses both, and ReviewBody.Validate refuses both again. Naming them
// separately keeps a change that loosens either bound from passing silently.
func TestReviewRecordRequiresExactlyOnePrimarySubject(t *testing.T) {
	changeType := mustTypeRef(t, "repository-change/v1")
	first := snapshot.SnapshotRef{ID: 41, Type: changeType, Digest: recordDigest('b')}
	second := snapshot.SnapshotRef{ID: 42, Type: changeType, Digest: recordDigest('c')}
	context := validationContextFor(t, map[string]snapshot.SnapshotRef{"change": first, "other": second})
	body := contracts.ReviewBody{Conclusion: "accept", Summary: "nothing to change"}

	for name, subjects := range map[string][]contracts.Subject{
		"no primary subject": {
			contracts.SubjectFromInput("context-1", contracts.SubjectRoleContext, "change", first),
		},
		"two primary subjects": {
			contracts.SubjectFromInput("primary-1", contracts.SubjectRolePrimary, "change", first),
			contracts.SubjectFromInput("primary-2", contracts.SubjectRolePrimary, "other", second),
		},
	} {
		t.Run(name, func(t *testing.T) {
			record, err := contracts.NewRecord(mustTypeRef(t, "review/v1"), subjects, body)
			if err != nil {
				t.Fatalf("NewRecord(): %v", err)
			}
			_, err = validateFiles(t, "review/v1", map[string][]byte{
				"record.json": marshalRecord(t, record),
			}, context)
			if err == nil || !strings.Contains(err.Error(), `"primary" role`) {
				t.Fatalf("review error = %v, want a primary-role cardinality rejection", err)
			}
		})
	}
}
```

- [x] The duplicate-finding test appends a second copy of `f-1`; assert the fragment `body/findings/*/id: "f-1" is duplicate`. The garbage-conclusion test sets `Conclusion = "approved-with-love"`; assert `body/conclusion: "approved-with-love" is not one of accept, changes-required, inconclusive`.
- [x] Re-run the focused command and confirm three passing tests (four subtests).
- [x] Commit `test(contracts): name review/v1's duplicate, enum and primary-subject rejections`.

---

### Task 9: validation, measurements and diagnosis holes

**Files:**
- Modify: `agent/snapshot/contracts/validation_test.go`
- Modify: `agent/snapshot/contracts/measurements_test.go`
- Modify: `agent/snapshot/contracts/diagnosis_test.go`

| New named test | Setup | Expected fragment |
|---|---|---|
| `TestValidationRecordRejectsANonSkippedCheckWithNoAttempts` | `Checks[0].Status` stays `failed`, `Attempts = nil` | `failed check requires at least one attempt` |
| `TestMeasurementsRecordRejectsAPartialConclusionWithNoMetrics` | `Conclusion = "partial"`, `Metrics = nil`, `Explanation` set | `partial conclusion requires at least one metric` |
| `TestMeasurementsRecordRequiresAnExplanationForPartialAndNotApplicable` | subtest `partial`: metric kept, no explanation; subtest `not-applicable`: no metrics, no explanation | `partial conclusion requires an explanation` / `not-applicable conclusion requires an explanation` |
| `TestMeasurementsMetricDirectionGovernsItsTarget` | subtest `higher-is-better carries a target`: `Target = floatPointer(3)`; subtest `target direction carries none`: `Direction = "target"`, `Target = nil` | `measurement target is valid only for target direction` / `target direction requires a finite target` |
| `TestDiagnosisRecordRejectsDuplicateHypothesisRanks` | two hypotheses both `Rank: 1` | `hypotheses[1].rank 1 is duplicate` |

- [x] Add all five tests. `TestMeasurementsMetricDirectionGovernsItsTarget` carries the note that this is the `Measurement`-level cross-rule, distinct from `Score.Validate`'s identically shaped rule — two types, two rules, and only `selection/v1` exercised the score one.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestValidationRecordRejectsANonSkipped|TestMeasurements(RecordRejectsAPartial|RecordRequiresAnExplanation|MetricDirection)|TestDiagnosisRecordRejectsDuplicate' -v -count=1` and confirm they fail to build first, then pass.
- [x] Commit `test(contracts): close the validation, measurements and diagnosis negative holes`.

---

### Task 10: repository-change holes

**Files:**
- Modify: `agent/snapshot/contracts/repository_test.go`

Task 6 already discharges the directory-payload, non-ancestor, apply-check and base-sha-width rules as witnesses. These named tests are the readable arms, plus the one shape the witness table does not cover at all: the payload path resolving to a **symlink**.

- [x] Add `TestRepositoryChangeRejectsAPayloadThatIsNotARegularFile` with two subtests, `directory` and `symlink`, both asserting `payload.path: payload must be a regular file`. The symlink arm renames the real payload aside and links to it, so the target genuinely exists and the rejection is about the *kind* of the path rather than about a broken link:

```go
"symlink": func(t *testing.T, dir string) {
	content := filepath.Join(dir, "content")
	if err := os.Rename(filepath.Join(content, "change.patch"), filepath.Join(content, "real.patch")); err != nil {
		t.Fatalf("move payload aside: %v", err)
	}
	// The link target exists and holds the exact declared bytes. The rejection
	// is about the payload path not being a regular file, not about a dangling
	// link, which is the distinction os.Root alone would not draw.
	if err := os.Symlink("real.patch", filepath.Join(content, "change.patch")); err != nil {
		t.Fatalf("link payload: %v", err)
	}
},
```

- [x] Add `TestRepositoryChangeRejectsAValidResultCommitThatDoesNotDescendFromBase` using the sibling-branch bundle construction from Task 6, asserting `does not descend from base_sha`. Carry the comment about the shared root commit keeping `repository_id` equal.
- [x] Add `TestRepositoryChangeRejectsAPatchThatFailsGitApplyCheck`, rewriting `README.md` to `NOSUCH.md` in the patch bytes and re-deriving `PayloadDigest` with `digestBytes`, asserting `patch failed git apply --check --index`.
- [x] Add `TestRepositoryChangeRejectsObjectIdsOutsideTheBaseObjectFormat` with two subtests: `base_sha` of width 41 → `base_sha: object ID must be a full sha1 or sha256 hexadecimal value`, and a 64-hex `result_tree` under a 40-hex base → `result_tree: object ID must contain 40 lowercase hexadecimal characters`. Add the comment recording what scouting found:

```go
// The cross-check at repository_change.go's verifyAgainstBase — declared object
// format versus the base repository's — is defensive only and is not reachable
// from here: base_sha must equal the base HEAD before it runs, and a string
// equal to the HEAD necessarily has the HEAD's width. The width rule in
// RepositoryChangeBody.Validate is where a mismatched object format is actually
// caught, so that is what this test pins.
```

- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestRepositoryChangeRejects' -v -count=1` and confirm red-then-green.
- [x] Commit `test(contracts): close the repository-change payload, ancestry and object-format holes`.

---

### Task 11: An empty-id named test for every entity set

**Files:**
- Modify: `agent/snapshot/contracts/record_test.go`

Empty ids are covered today only by the parity gate's generic blank mutation, which proves the two descriptions agree rather than that the gate rejects. One named, greppable test per entity-set family closes that.

- [x] Add `TestEveryEntitySetRejectsAnEmptyIdentity`, a table over the six entity-set families with one subtest each: `review/v1` findings, `validation/v1` checks, `measurements/v1` metrics, `diagnosis/v1` hypotheses, `diagnosis/v1` actions, `selection/v1` candidates.
- [x] Assert the exact declared-grammar fragment per family, all of the form `body/<set>/*/id: is required and must not be blank; a missing key and a blank value decode identically`. Concretely: `body/findings/*/id`, `body/checks/*/id`, `body/metrics/*/id`, `body/hypotheses/*/id`, `body/actions/*/id`, `body/candidates/*/id`.
- [x] Add the note that the assertion is on the frozen field-path grammar, not on `ValidateEntityIDs`' `findings[0].id` spelling, because the core validator runs first and its message is the one an operator sees.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestEveryEntitySetRejectsAnEmptyIdentity' -v -count=1` and confirm six passing subtests.
- [x] Commit `test(contracts): name the empty-identity rejection of every entity set`.

---

### Task 12: Injectable limits and their boundaries

**Files:**
- Modify: `agent/snapshot/contracts/json.go`
- Modify: `agent/snapshot/contracts/repository_change.go`
- Create: `agent/snapshot/contracts/schema_limits_internal_test.go`

**Mechanism decision: package-level `var`, restored by `t.Cleanup`.** `maxJSONDocumentBytes` has two read sites and `maxRepositoryPayloadBytes` four, but `decodeStrictDocument` has eight callers and `snapshot.Validator` is an interface owned by another package, so a `Limits` struct would have to be threaded through the registry, every validator struct and the interface itself to reach `readRegularFile` — dozens of touched call sites to make two numbers configurable in tests. Converting the constants to variables touches zero callers. The package contains no `t.Parallel()`, so the override is safe; the helper says so in its doc comment.

The boundary pair is built by setting the limit to the candidate's exact size and then to one byte less. That exercises `>` versus `>=` directly and needs no padding: an off-by-one in either comparison flips exactly one of the two assertions.

- [x] Change `const maxJSONDocumentBytes int64 = 1 << 20` to a `var` and add the doc comment:

```go
// maxJSONDocumentBytes bounds every strict JSON document a snapshot tree can
// carry. It is a var rather than a const only so tests can drive its boundary
// with a candidate small enough to build; production never assigns it.
var maxJSONDocumentBytes int64 = 1 << 20
```

- [x] Change `const maxRepositoryPayloadBytes int64 = 10 << 30` to a `var` with the equivalent comment.
- [x] Run `go build ./agent/... && go vet ./agent/snapshot/...` and confirm both clean.
- [x] Create `agent/snapshot/contracts/schema_limits_internal_test.go` in `package contracts` with the internal seal-gate driver and the two override helpers:

```go
package contracts

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// These tests are inside the package because the limits they drive are
// unexported. They therefore carry their own seal-gate driver rather than
// helpers_test.go's validateFiles, which lives in the external test package.
func admitTreeForSeal(t *testing.T, ref snapshot.TypeRef, dir string, declarations snapshot.ValidationContext) error {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot(%q): %v", dir, err)
	}
	defer root.Close()
	registry, err := NewRegistry(WithCanonicalizer(snapshot.Canonicalizer{TempDir: t.TempDir()}))
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	validator, err := registry.Lookup(ref)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", ref, err)
	}
	_, err = validator.AdmitForSeal(context.Background(), root, declarations)
	return err
}

func writeCandidateTree(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", name, err)
		}
		if err := os.WriteFile(full, contents, 0o644); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}
	return dir
}

// withJSONDocumentLimit lowers the strict-document limit for one test and puts
// it back afterwards. It mutates package state, so it must never be called from
// a test that also calls t.Parallel(); no test in this package does.
func withJSONDocumentLimit(t *testing.T, limit int64) {
	t.Helper()
	original := maxJSONDocumentBytes
	maxJSONDocumentBytes = limit
	t.Cleanup(func() { maxJSONDocumentBytes = original })
}

func withRepositoryPayloadLimit(t *testing.T, limit int64) {
	t.Helper()
	original := maxRepositoryPayloadBytes
	maxRepositoryPayloadBytes = limit
	t.Cleanup(func() { maxRepositoryPayloadBytes = original })
}
```

- [x] Write `TestStrictDocumentLimitAdmitsExactlyItsOwnSizeAndNoMore`: build an accepted `review/v1` candidate, write it, then admit twice — once with the limit set to `int64(len(encoded))` (must succeed) and once with `int64(len(encoded)) - 1` (must fail with `exceeds size limit of`). Expected failing output on a wrong comparison:

```
--- FAIL: TestStrictDocumentLimitAdmitsExactlyItsOwnSizeAndNoMore (0.01s)
    schema_limits_internal_test.go:NN: a record.json of exactly the limit was rejected: snapshot contracts: "record.json" exceeds size limit of 512 bytes
```

- [x] Write `TestRepositoryPayloadLimitAdmitsExactlyItsOwnSizeAndNoMore` the same way against the shared repository-change fixture, with the limit set to `int64(len(patch))` and then `int64(len(patch)) - 1`, asserting `payload exceeds size limit of`. Because this internal file cannot reach `repository_test.go`'s builders, drive the payload limit through `spoolRepositoryPayload` directly over a tree holding just the payload bytes — the limit is enforced there, before any git work, and the test says so:

```go
// The payload limit is enforced in spoolRepositoryPayload, before any git
// command runs, so exercising it needs no repository at all. Driving the whole
// repository-change gate here would only add a git init to a test about one
// comparison.
```

- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestStrictDocumentLimit|TestRepositoryPayloadLimit' -v -count=1` and confirm both pass with four assertions.
- [x] Commit `feat(contracts): make the document and payload limits injectable and pin their boundaries`.

---

### Task 13: The ten-thousand-finding scale guard

**Files:**
- Modify: `agent/snapshot/contracts/schema_limits_internal_test.go`

Entity sets have no declared cardinality bound; today the 1 MiB document limit is the only thing bounding them. That is worth knowing and worth pinning, and the instance also guards against a validator turning quadratic.

- [x] Add `encoding/json`, `fmt` and `time` to `schema_limits_internal_test.go`'s imports. (Landed: `encoding/json` was already imported in Task 12, whose document-limit test marshals a record; Task 13 added only `fmt` and `time`.)
- [x] Add the generator and the test:

```go
// A review carrying ten thousand findings is 1.49 MB of record.json, so it does
// not fit under the ordinary document limit — which is the finding, not an
// inconvenience: no entity set declares a cardinality bound, and the document
// limit is currently the only thing bounding one. This test raises that limit
// deliberately so the remaining question is the one it is about, namely whether
// the validators stay linear. They do: the measured time is around 30 ms, so a
// quadratic regression at this size would blow the ceiling below by orders of
// magnitude rather than by a flaky margin.
func TestAReviewCarryingTenThousandFindingsValidatesQuickly(t *testing.T) {
	withJSONDocumentLimit(t, 64<<20)

	const count = 10000
	const ceiling = 60 * time.Second

	ref := snapshot.SnapshotRef{ID: 71, Type: snapshot.TypeRef("repository-change/v1"), Digest: fixtureDigest('a')}
	declarations, err := snapshot.NewValidationContext(map[string]snapshot.SnapshotRef{"change": ref}, nil)
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}

	findings := make([]Finding, 0, count)
	for index := 1; index <= count; index++ {
		// Zero-padded, so lexicographic order is numeric order and the entity-set
		// sort rule is satisfied by construction. Observation severity needs no
		// evidence and may not be blocking, so the instance stays valid at any size.
		findings = append(findings, Finding{
			ID: fmt.Sprintf("f-%06d", index), Severity: "observation", Category: "style",
			Title: "naming", Description: "prefer a fuller name",
		})
	}
	record, err := NewRecord(reviewType,
		[]Subject{SubjectFromInput("primary", SubjectRolePrimary, "change", ref)},
		ReviewBody{Conclusion: "inconclusive", Summary: "ten thousand observations", Findings: findings},
	)
	if err != nil {
		t.Fatalf("NewRecord(review/v1): %v", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	dir := writeCandidateTree(t, map[string][]byte{"record.json": encoded})

	started := time.Now()
	if err := admitTreeForSeal(t, reviewType, dir, declarations); err != nil {
		t.Fatalf("a %d-finding review was rejected: %v", count, err)
	}
	elapsed := time.Since(started)
	t.Logf("%d findings, %d bytes of record.json, validated in %s", count, len(encoded), elapsed.Round(time.Millisecond))
	if elapsed > ceiling {
		t.Fatalf("validating %d findings took %s, over the %s ceiling; a validator has gone superlinear", count, elapsed, ceiling)
	}
}
```

- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestAReviewCarryingTenThousandFindings' -v -count=1` and confirm the log line reports about 1.49 MB and tens of milliseconds:

```
    schema_limits_internal_test.go:NN: 10000 findings, 1490393 bytes of record.json, validated in 33ms
```

- [x] Commit `test(contracts): guard entity-set scale with a ten-thousand-finding review`.

---

### Task 14: UTF-8 admission at the seal gate

**Files:**
- Modify: `agent/snapshot/contracts/json.go`
- Modify: `agent/snapshot/contracts/record.go`
- Create: `agent/snapshot/contracts/record_text_admission_test.go`

**Deviation from the spec's letter, with the measurement behind it.** WS5 asks for `utf8.ValidString` checks on free-text body fields. Scouting proves that check can never fire: Go's `encoding/json` *sanitizes* rather than refuses, mapping a raw `0xff` inside a string literal and a lone `\ud800` escape both to U+FFFD with a nil error, so every decoded body string is valid UTF-8 by construction. A field-level check would be dead code. The rule is therefore applied where it can bite — to the **exact bytes** of a candidate `record.json`, at the seal gate only — using the same two predicates canonical JSON already applies to a schema document. The intent of the spec item is met and strengthened: the byte gate also catches the unpaired-surrogate case, which no field-level check could see.

- [x] Write the failing tests first in `agent/snapshot/contracts/record_text_admission_test.go` (`package contracts_test`), all four driving the real gates:
  1. `TestSealTimeAdmissionRejectsRecordBytesThatAreNotValidUTF8` — splice a raw `0xff` into the summary value of an accepted `review/v1` record and admit; want an error naming `UTF-8`.
  2. `TestSealTimeAdmissionRejectsAnUnpairedSurrogateEscape` — splice `"lone \ud800 surrogate"` in; want an error naming `surrogate`.
  3. `TestStoredRecordsWithInvalidTextEncodingStillRevalidate` — the *same* two byte sequences through `revalidateSealedFiles`; both must **succeed**. This is the hard constraint made executable.
  4. `TestTheJSONDecoderSanitizesRatherThanRefusesBadTextEncoding` — decode both forms into a struct and assert `err == nil` and `utf8.ValidString(decoded)`, recording why the gate is byte-level.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestSealTimeAdmissionRejects|TestStoredRecordsWithInvalidTextEncoding|TestTheJSONDecoderSanitizes' -v -count=1` and confirm the two rejection tests fail:

```
--- FAIL: TestSealTimeAdmissionRejectsRecordBytesThatAreNotValidUTF8 (0.01s)
    record_text_admission_test.go:NN: seal gate error = <nil>, want it to name UTF-8
--- FAIL: TestSealTimeAdmissionRejectsAnUnpairedSurrogateEscape (0.00s)
    record_text_admission_test.go:NN: seal gate error = <nil>, want it to name a surrogate escape
```

- [x] Split `decodeStrictDocument` in `json.go` into a read half and a decode half so the seal path can see the bytes, keeping the read path byte-for-byte identical:

```go
func decodeStrictDocument(ctx context.Context, root *os.Root, name string, target any) error {
	contents, err := readRegularFile(ctx, root, name, maxJSONDocumentBytes)
	if err != nil {
		return err
	}
	return decodeStrictJSON(name, contents, target)
}

// admitStrictDocument is decodeStrictDocument plus the SEAL-ONLY text-encoding
// gate. Only seal-time entry points call it.
func admitStrictDocument(ctx context.Context, root *os.Root, name string, target any) error {
	contents, err := readRegularFile(ctx, root, name, maxJSONDocumentBytes)
	if err != nil {
		return err
	}
	if err := admitRecordTextEncoding(name, contents); err != nil {
		return err
	}
	return decodeStrictJSON(name, contents, target)
}
```

- [x] Add `decodeStrictJSON(name string, contents []byte, target any) error` holding the existing decoder body verbatim (`DisallowUnknownFields`, the trailing-JSON check, the same two error strings). (Landed as `decodeStrictJSONBytes`: `schema_document_load.go` already owns an unexported `decodeStrictJSON` with a different signature, so the plan's name would not compile; a doc comment on the helper records the rename.)
- [x] Add `admitRecordTextEncoding` with the safety argument as its doc comment, verbatim:

```go
// admitRecordTextEncoding is the SEAL-ONLY text-encoding gate over the exact
// bytes of a candidate document.
//
// It applies the two predicates canonical JSON already applies to a schema
// document: the bytes must be valid UTF-8, and they must carry no unpaired
// surrogate escape. Both are byte-level on purpose. Go's encoding/json
// SANITIZES rather than refuses — a raw 0xff inside a string literal and a lone
// \ud800 escape both decode to U+FFFD with a nil error — so a utf8.ValidString
// check on a decoded body field could never fire, and the only door the rule can
// stand at is this one, on the bytes.
//
// THE TWO-GATE ARGUMENT, which is why this tightening needs no descriptor bump,
// no history entry and no data migration:
//
//   - It is the same predicate canonical JSON already enforces, for the reason
//     stated there: replacing a bad byte with U+FFFD maps two distinct inputs to
//     one canonical form. This moves it to the remaining door a producer writes
//     through.
//   - It runs at ADMISSION only. RevalidateSealed and every stored-record reader
//     are untouched, so every record already in the corpus reads exactly as it
//     read before — including a hypothetical record whose bytes are not valid
//     UTF-8. A gate that rejected stored bytes on read would be the
//     descriptor-digest data-loss class of change, and it is forbidden.
//   - A candidate refused here was never a sealed record, so no digest moves and
//     no schema document changes. This is a Go gate rule, not a declared field
//     rule: writing it into a schema document would change that document's
//     canonical bytes, and a rev2 file's bytes are a frozen descriptor.
func admitRecordTextEncoding(name string, contents []byte) error {
	if !utf8.Valid(contents) {
		return fmt.Errorf(
			"snapshot contracts: %s is not valid UTF-8; replacing a bad byte with U+FFFD would map two distinct inputs to one record",
			name,
		)
	}
	if err := rejectUnpairedSurrogateEscapes(contents); err != nil {
		return fmt.Errorf("snapshot contracts: %s: %w", name, err)
	}
	return nil
}
```

- [x] Point the two seal-time entry points at it, and only those: `admitRecordForSeal` in `record.go` swaps `decodeStrictDocument` for `admitStrictDocument`, and `decodeRecord` calls `admitRecordTextEncoding("record.json", data)` when `admission == currentSchemaDigestOnly` (that is the `DecodeRecordForSeal` path, whose one production caller is `agent/functions/repositorymerge/runner.go`). Leave `readSealedRecord` and `DecodeSealedRecord` alone.
- [x] Add `"unicode/utf8"` to `json.go`'s imports.
- [x] Run `go test ./agent/snapshot/contracts/ -run 'TestSealTimeAdmissionRejects|TestStoredRecordsWithInvalidTextEncoding|TestTheJSONDecoderSanitizes' -v -count=1` and confirm all four pass.
- [x] Add `TestEveryDeclaredFreeTextFieldIsCoveredByTheByteLevelTextGate` to the same file: walk `SchemaDocumentFor(ref).Fields` for every record type, collect the paths whose `Kind` is `contracts.KindString` or `contracts.KindMarkdown`, and assert the set equals a pinned list of fifteen paths. (The prose above said "fourteen"; the declared set is fifteen `(type, path)` pairs — `body/summary` is a distinct declared leaf in three documents — and the pinned list below already enumerates all fifteen. The landed test asserts fifteen.) The pinned list, verified against the base commit:

```
diagnosis/v1:           body/actions/*/description, body/actions/*/rationale,
                        body/hypotheses/*/statement, body/summary
measurements/v1:        body/explanation
review/v1:              body/findings/*/description, body/findings/*/recommendation,
                        body/findings/*/title, body/summary
selection/v1:           body/candidates/*/summary, body/rationale
validation/v1:          body/checks/*/attempts/*/detail, body/checks/*/detail,
                        body/checks/*/name, body/summary
```

- [x] Give that test the comment explaining what it is for: the gate is document-wide, so one witness covers every field, and this test is the ledger that makes adding a sixteenth free-text field a deliberate, reviewed act rather than a silent one.
- [x] Run the full package: `go test ./agent/snapshot/contracts/ -count=1` and confirm `ok` in roughly 20 s.
- [x] Run `go test ./agent/... -count=1` and confirm nothing downstream of the seal gate regressed, in particular `agent/functions/repositorymerge`.
- [x] Commit `feat(contracts): admit only well-formed text encoding at the seal gate`.

---

### Task 15: Self-review against WS5's acceptance criteria

**Files:**
- Modify: `docs/superpowers/plans/test-hardening/05-contract-conformance.md`

- [x] "Deleting any `go_only_rules` witness fails the linkage test": delete one entry from each of the six tables in turn, run `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness' -count=1`, confirm six distinct failures, restore each.
- [x] Adding a rule fails too: append a throwaway `{"id": "temporary-probe", "rule": "probe"}` to a **copy** of a schema document under `/tmp`, confirm by inspection that the harness's `declaredGoOnlyRuleCount` assertion is what would catch it, and record that the real file was not touched. Verify with `git status --porcelain agent/snapshot/contracts/schemas` producing no output.
- [x] "Every enumerated hole has a named red-then-green test": confirm one named test exists for each of duplicate finding id, garbage conclusion enum, zero primary subjects, two primary subjects, non-skipped check with zero attempts, partial with zero metrics, missing explanation for partial, missing explanation for not-applicable, `Measurement`-level direction/target, duplicate hypothesis rank, payload path is a directory, payload path is a symlink, non-ancestor `result_commit`, patch failing `git apply --check`, `base_sha` width versus object format, and an empty id per entity set.
- [x] "Limits are exercised at their boundaries with test-injected thresholds": confirm four boundary assertions across two limits, and that `rg -n 'maxJSONDocumentBytes|maxRepositoryPayloadBytes' agent/snapshot/contracts` shows both as `var` with no production assignment.
- [x] Confirm the witness total: `go test ./agent/snapshot/contracts/ -run 'TestEveryGoOnlyRuleHasARejectionWitness' -v -count=1 | rg -c '^\s+--- (PASS|FAIL)'` reports **52**.
- [x] Confirm the hard constraint: `git diff --stat` shows no change to any `RevalidateSealed`, `readSealedRecord`, `DecodeSealedRecord`, `ReadSealedRepositoryChangeRecord`, `ReadSealedSelectionRecord`, `ReadSealedMeasurementsRecord` or `DecodeSealedReviewRecord`, and no change under `agent/snapshot/contracts/schemas/`.
- [x] Run `go test ./agent/snapshot/... -count=1`, `go vet ./agent/snapshot/...`, and `gofmt -l agent/snapshot` and confirm all clean. (Landed: `go test` and `go vet` clean; `gofmt -l agent/snapshot` reports only `agent/snapshot/types.go`, which was already unformatted at the branch base `1aa847c09d` and is outside WS5's Files scope — all five files this workstream touched are gofmt-clean.)
- [x] Tick every completed checkbox in this plan and commit `docs(test-hardening): record WS5 contract-conformance completion`.

---

## Deviations from the spec, and why

1. **UTF-8 is checked on bytes, not on decoded free-text fields.** Measured: `encoding/json` sanitizes invalid UTF-8 and unpaired surrogates to U+FFFD with a nil error, so a `utf8.ValidString` check on any decoded body string is unreachable. The byte-level gate at the seal entry points is the same rule at the only place it can fire, and it additionally catches the surrogate-escape case. Task 14 carries the evidence and the two-gate comment.
2. **The UTF-8 rule is not added to the parity mutation families.** The parity gate is differential: it fails only when the two descriptions disagree. A seal-gate-only rule appears in neither description, so a parity family for it would assert nothing. Making it assertable there would mean putting the rule into `validateDecodedRecord`, which runs at read time — forbidden by the hard constraint. The declared-free-text ledger test in Task 14 provides the same additive-only property (a new free-text field with no entry turns the test red) without touching the read gate.
3. **The harness keys on `(type, rule)` — 52 witnesses — rather than on the 43 distinct rule ids.** The three anchor rules appear in four documents each and are four independent validator paths; one witness for all four would be assumed coverage.
4. **`declaredGoOnlyRuleCount` is pinned at 52.** Adding a rule *with* a witness therefore still requires a deliberate one-line edit. That is intentional: a rule count that drifts silently is how a copy-pasted witness gets in.
