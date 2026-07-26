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
var pendingRules = map[snapshot.TypeRef][]string{
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
		snapshot.TypeRef("review/v1"):       reviewRuleWitnesses(t),
		snapshot.TypeRef("validation/v1"):   validationRuleWitnesses(t),
		snapshot.TypeRef("measurements/v1"): measurementsRuleWitnesses(t),
		snapshot.TypeRef("diagnosis/v1"):    diagnosisRuleWitnesses(t),
	}
}

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

// validationWitnessBase is one accepted validation/v1 candidate: a single failed
// check whose single failed attempt carries one file-lines anchor, bound to one
// primary subject. Every witness starts here and breaks exactly one thing.
func validationWitnessBase(t *testing.T) (snapshot.ValidationContext, []contracts.Subject, contracts.ValidationBody) {
	t.Helper()
	ref := snapshot.SnapshotRef{
		ID: 41, Type: mustTypeRef(t, "repository/v1"), Digest: recordDigest('a'),
	}
	declarations := validationContextFor(t, map[string]snapshot.SnapshotRef{"in": ref})
	subjects := []contracts.Subject{
		contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "in", ref),
	}
	body := contracts.ValidationBody{
		Conclusion: "failed",
		Summary:    "one suite fails",
		Checks: []contracts.ValidationCheck{{
			ID: "unit", Kind: "test", Name: "unit tests", Status: "failed",
			Attempts: []contracts.ValidationAttempt{{
				Number: 1, Status: "failed", Duration: "1s",
				Evidence: []contracts.Anchor{{
					Subject: "primary",
					Locator: contracts.Locator{Kind: "file-lines", Path: "main.go", Start: intPointer(12), End: intPointer(18)},
				}},
			}},
		}},
	}
	return declarations, subjects, body
}

func validationWitness(t *testing.T, mutate func(*contracts.ValidationBody)) witnessCandidate {
	t.Helper()
	declarations, subjects, body := validationWitnessBase(t)
	mutate(&body)
	record, err := contracts.NewRecord(mustTypeRef(t, "validation/v1"), subjects, body)
	if err != nil {
		t.Fatalf("NewRecord(validation/v1): %v", err)
	}
	return witnessCandidate{
		files:        map[string][]byte{"record.json": marshalRecord(t, record)},
		declarations: declarations,
	}
}

func validationRuleWitnesses(t *testing.T) []ruleWitness {
	t.Helper()
	return []ruleWitness{
		{
			rule: "conclusion-is-recomputed-from-the-checks",
			build: func(t *testing.T) witnessCandidate {
				return validationWitness(t, func(body *contracts.ValidationBody) {
					body.Conclusion = "passed"
				})
			},
			wantErr: `conclusion must match derived conclusion "failed"`,
		},
		{
			rule: "status-governs-attempt-count",
			build: func(t *testing.T) witnessCandidate {
				// The skipped arm: a skipped check keeps its attempt. Task 9's named
				// test uses the non-skipped arm, so the two do not duplicate.
				return validationWitness(t, func(body *contracts.ValidationBody) {
					body.Checks[0].Status = "skipped"
					body.Conclusion = "incomplete"
				})
			},
			wantErr: "skipped check must have no attempts",
		},
		{
			rule: "attempt-number-equals-its-position-plus-one",
			build: func(t *testing.T) witnessCandidate {
				return validationWitness(t, func(body *contracts.ValidationBody) {
					body.Checks[0].Attempts[0].Number = 2
				})
			},
			wantErr: "attempt numbers must be contiguous from 1",
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
			rule: "anchor-subject-must-be-a-declared-subject",
			build: func(t *testing.T) witnessCandidate {
				return validationWitness(t, func(body *contracts.ValidationBody) {
					body.Checks[0].Attempts[0].Evidence[0].Subject = "ghost"
				})
			},
			wantErr: `body/checks/*/attempts/*/evidence/*/subject: "ghost" is not declared by this record`,
		},
		{
			rule: "anchor-locator-kind-selects-which-fields-appear",
			build: func(t *testing.T) witnessCandidate {
				return validationWitness(t, func(body *contracts.ValidationBody) {
					body.Checks[0].Attempts[0].Evidence[0].Locator = contracts.Locator{
						Kind: "json-pointer", Pointer: "/checks/0", Path: "a.go",
					}
				})
			},
			wantErr: "json-pointer anchor contains fields for another locator kind",
		},
		{
			rule: "anchor-locators-are-unverified",
			documented: "This rule documents a deliberate NON-check: nothing resolves an anchor's " +
				"locator against any content, because anchor content hashes are deferred. It has no " +
				"rejection witness by construction. The pin makes the claim executable instead: an " +
				"anchor naming a path that exists nowhere, at lines nothing has, is ACCEPTED.",
			pin: func(t *testing.T) {
				candidate := validationWitness(t, func(body *contracts.ValidationBody) {
					body.Checks[0].Attempts[0].Evidence[0].Locator.Path = "does/not/exist.go"
					body.Checks[0].Attempts[0].Evidence[0].Locator.Start = intPointer(400000)
					body.Checks[0].Attempts[0].Evidence[0].Locator.End = intPointer(400001)
				})
				if err := candidate.admit(t, snapshot.TypeRef("validation/v1")); err != nil {
					t.Fatalf(
						"the seal gate rejected an unresolvable anchor locator: %v; if locators are now verified, this rule's documentation is stale and the schema document is the thing to change",
						err,
					)
				}
			},
		},
	}
}

// measurementsWitnessBase is one accepted measurements/v1 candidate with no
// subjects at all — measurements admits an empty subject set — carrying one
// lower-is-better metric whose value sits outside any bound it does not declare.
// The non-anchor witnesses start here.
func measurementsWitnessBase(t *testing.T) (snapshot.ValidationContext, []contracts.Subject, contracts.MeasurementsBody) {
	t.Helper()
	body := contracts.MeasurementsBody{
		Conclusion: "measured",
		Metrics: []contracts.Measurement{{
			ID: "latency", Value: 1.5, Unit: "milliseconds", Direction: "lower-is-better",
		}},
	}
	return emptyValidationContext(t), nil, body
}

// measurementsAnchorWitnessBase is the one-primary variant the anchor witnesses
// need: the same metric now carries one opaque evidence anchor bound to the
// declared subject, so a mutation to that anchor is the only thing wrong.
func measurementsAnchorWitnessBase(t *testing.T) (snapshot.ValidationContext, []contracts.Subject, contracts.MeasurementsBody) {
	t.Helper()
	ref := snapshot.SnapshotRef{
		ID: 42, Type: mustTypeRef(t, "repository/v1"), Digest: recordDigest('a'),
	}
	declarations := validationContextFor(t, map[string]snapshot.SnapshotRef{"in": ref})
	subjects := []contracts.Subject{
		contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "in", ref),
	}
	body := contracts.MeasurementsBody{
		Conclusion: "measured",
		Metrics: []contracts.Measurement{{
			ID: "latency", Value: 1.5, Unit: "milliseconds", Direction: "lower-is-better",
			Evidence: []contracts.Anchor{{
				Subject: "primary",
				Locator: contracts.Locator{Kind: "opaque", Value: "build log line 44"},
			}},
		}},
	}
	return declarations, subjects, body
}

func newMeasurementsWitness(
	t *testing.T,
	declarations snapshot.ValidationContext,
	subjects []contracts.Subject,
	body contracts.MeasurementsBody,
) witnessCandidate {
	t.Helper()
	record, err := contracts.NewRecord(mustTypeRef(t, "measurements/v1"), subjects, body)
	if err != nil {
		t.Fatalf("NewRecord(measurements/v1): %v", err)
	}
	return witnessCandidate{
		files:        map[string][]byte{"record.json": marshalRecord(t, record)},
		declarations: declarations,
	}
}

func measurementsWitness(t *testing.T, mutate func(*contracts.MeasurementsBody)) witnessCandidate {
	t.Helper()
	declarations, subjects, body := measurementsWitnessBase(t)
	mutate(&body)
	return newMeasurementsWitness(t, declarations, subjects, body)
}

func measurementsAnchorWitness(t *testing.T, mutate func(*contracts.MeasurementsBody)) witnessCandidate {
	t.Helper()
	declarations, subjects, body := measurementsAnchorWitnessBase(t)
	mutate(&body)
	return newMeasurementsWitness(t, declarations, subjects, body)
}

func measurementsRuleWitnesses(t *testing.T) []ruleWitness {
	t.Helper()
	return []ruleWitness{
		{
			rule: "conclusion-governs-the-metric-count",
			build: func(t *testing.T) witnessCandidate {
				return measurementsWitness(t, func(body *contracts.MeasurementsBody) {
					body.Metrics = nil
				})
			},
			wantErr: "measured conclusion requires at least one metric",
		},
		{
			rule: "partial-and-not-applicable-require-an-explanation",
			build: func(t *testing.T) witnessCandidate {
				return measurementsWitness(t, func(body *contracts.MeasurementsBody) {
					body.Conclusion = "partial"
				})
			},
			wantErr: "partial conclusion requires an explanation",
		},
		{
			rule: "direction-governs-target",
			build: func(t *testing.T) witnessCandidate {
				return measurementsWitness(t, func(body *contracts.MeasurementsBody) {
					body.Metrics[0].Target = floatPointer(3)
				})
			},
			wantErr: "measurement target is valid only for target direction",
		},
		{
			rule: "bounds-are-declared-together-finite-and-ordered",
			build: func(t *testing.T) witnessCandidate {
				return measurementsWitness(t, func(body *contracts.MeasurementsBody) {
					body.Metrics[0].Minimum = floatPointer(0)
				})
			},
			wantErr: "measurement minimum and maximum must be declared together",
		},
		{
			rule: "value-must-lie-within-any-declared-bounds",
			build: func(t *testing.T) witnessCandidate {
				return measurementsWitness(t, func(body *contracts.MeasurementsBody) {
					body.Metrics[0].Minimum = floatPointer(0)
					body.Metrics[0].Maximum = floatPointer(1)
				})
			},
			wantErr: "measurement value must be within its declared bounds",
		},
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
				// The metric is an entity-set whose element fields are enumerated at
				// body/metrics/*; the element itself is declared at body/metrics.
				element, declared := document.Fields["body/metrics"]
				if !declared {
					t.Fatal("measurements/v1 declares no body/metrics element")
				}
				if element.Kind == contracts.KindScore {
					t.Fatal("measurements/v1 now declares its metric element as a score, claiming a scale no field carries")
				}
				if _, found := document.Fields["body/metrics/*/scale"]; found {
					t.Fatal("measurements/v1 now declares body/metrics/*/scale; a metric with a scale is a score and the rule text is stale")
				}
			},
		},
		{
			rule: "anchor-subject-must-be-a-declared-subject",
			build: func(t *testing.T) witnessCandidate {
				return measurementsAnchorWitness(t, func(body *contracts.MeasurementsBody) {
					body.Metrics[0].Evidence = []contracts.Anchor{{
						Subject: "ghost",
						Locator: contracts.Locator{Kind: "opaque", Value: "build log line 44"},
					}}
				})
			},
			wantErr: `body/metrics/*/evidence/*/subject: "ghost" is not declared by this record`,
		},
		{
			rule: "anchor-locator-kind-selects-which-fields-appear",
			build: func(t *testing.T) witnessCandidate {
				return measurementsAnchorWitness(t, func(body *contracts.MeasurementsBody) {
					body.Metrics[0].Evidence[0].Locator = contracts.Locator{
						Kind: "opaque", Value: "build log line 44", Path: "a.go",
					}
				})
			},
			wantErr: "opaque anchor contains fields for another locator kind",
		},
		{
			rule: "anchor-locators-are-unverified",
			documented: "This rule documents a deliberate NON-check: nothing resolves an anchor's " +
				"locator against any content, because anchor content hashes are deferred. It has no " +
				"rejection witness by construction. The pin makes the claim executable instead: an " +
				"anchor naming a path that exists nowhere, at lines nothing has, is ACCEPTED.",
			pin: func(t *testing.T) {
				candidate := measurementsAnchorWitness(t, func(body *contracts.MeasurementsBody) {
					body.Metrics[0].Evidence[0].Locator = contracts.Locator{
						Kind: "file-lines", Path: "does/not/exist.go", Start: intPointer(400000), End: intPointer(400001),
					}
				})
				if err := candidate.admit(t, snapshot.TypeRef("measurements/v1")); err != nil {
					t.Fatalf(
						"the seal gate rejected an unresolvable anchor locator: %v; if locators are now verified, this rule's documentation is stale and the schema document is the thing to change",
						err,
					)
				}
			},
		},
	}
}

// diagnosisWitnessBase is one accepted diagnosis/v1 candidate: an identified
// conclusion over two ranked hypotheses — the rank-1 carrying one opaque evidence
// anchor — with one immediate action addressing the rank-1, bound to one primary
// subject. Every witness starts here and breaks exactly one thing.
func diagnosisWitnessBase(t *testing.T) (snapshot.ValidationContext, []contracts.Subject, contracts.DiagnosisBody) {
	t.Helper()
	ref := snapshot.SnapshotRef{
		ID: 43, Type: mustTypeRef(t, "repository/v1"), Digest: recordDigest('a'),
	}
	declarations := validationContextFor(t, map[string]snapshot.SnapshotRef{"in": ref})
	subjects := []contracts.Subject{
		contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "in", ref),
	}
	confidence := contracts.Score{Value: 0.9, Scale: "unit-interval", Direction: "higher-is-better"}
	body := contracts.DiagnosisBody{
		Summary:    "the lock is taken twice on one path",
		Conclusion: "identified",
		Hypotheses: []contracts.Hypothesis{
			{
				ID: "h-1", Rank: 1, Statement: "the lock is acquired twice on the retry path",
				Confidence: confidence,
				Evidence: []contracts.Anchor{{
					Subject: "primary",
					Locator: contracts.Locator{Kind: "opaque", Value: "trace line 8"},
				}},
			},
			{
				ID: "h-2", Rank: 2, Statement: "a second goroutine also contends for the lock",
				Confidence: confidence,
			},
		},
		Actions: []contracts.DiagnosisAction{{
			ID: "a-1", Priority: "immediate", Description: "guard the second acquisition",
			Addresses: []string{"h-1"},
		}},
	}
	return declarations, subjects, body
}

func diagnosisWitness(t *testing.T, mutate func(*contracts.DiagnosisBody)) witnessCandidate {
	t.Helper()
	declarations, subjects, body := diagnosisWitnessBase(t)
	mutate(&body)
	record, err := contracts.NewRecord(mustTypeRef(t, "diagnosis/v1"), subjects, body)
	if err != nil {
		t.Fatalf("NewRecord(diagnosis/v1): %v", err)
	}
	return witnessCandidate{
		files:        map[string][]byte{"record.json": marshalRecord(t, record)},
		declarations: declarations,
	}
}

func diagnosisRuleWitnesses(t *testing.T) []ruleWitness {
	t.Helper()
	return []ruleWitness{
		{
			rule: "identified-and-suspected-require-hypotheses",
			build: func(t *testing.T) witnessCandidate {
				return diagnosisWitness(t, func(body *contracts.DiagnosisBody) {
					body.Hypotheses = nil
					body.Actions = nil
				})
			},
			wantErr: "identified conclusion requires hypotheses",
		},
		{
			rule: "hypothesis-ranks-are-unique-and-contiguous-from-one",
			build: func(t *testing.T) witnessCandidate {
				return diagnosisWitness(t, func(body *contracts.DiagnosisBody) {
					body.Hypotheses[1].Rank = 1
				})
			},
			wantErr: "hypotheses[1].rank 1 is duplicate",
		},
		{
			rule: "identified-requires-evidence-on-the-rank-one-hypothesis",
			build: func(t *testing.T) witnessCandidate {
				return diagnosisWitness(t, func(body *contracts.DiagnosisBody) {
					body.Hypotheses[0].Evidence = nil
				})
			},
			wantErr: "identified conclusion requires evidence for the rank-1 hypothesis",
		},
		{
			rule: "addresses-must-name-a-hypothesis-this-record-declares",
			build: func(t *testing.T) witnessCandidate {
				return diagnosisWitness(t, func(body *contracts.DiagnosisBody) {
					body.Actions[0].Addresses = []string{"h-9"}
				})
			},
			wantErr: `action addresses unknown hypothesis "h-9"`,
		},
		{
			rule: "anchor-subject-must-be-a-declared-subject",
			build: func(t *testing.T) witnessCandidate {
				return diagnosisWitness(t, func(body *contracts.DiagnosisBody) {
					body.Hypotheses[0].Evidence[0].Subject = "ghost"
				})
			},
			wantErr: `body/hypotheses/*/evidence/*/subject: "ghost" is not declared by this record`,
		},
		{
			rule: "anchor-locator-kind-selects-which-fields-appear",
			build: func(t *testing.T) witnessCandidate {
				return diagnosisWitness(t, func(body *contracts.DiagnosisBody) {
					body.Hypotheses[0].Evidence[0].Locator = contracts.Locator{
						Kind: "byte-range", Path: "a.go", Start: intPointer(5), End: intPointer(5),
					}
				})
			},
			wantErr: "byte-range anchor requires nonnegative start and end > start",
		},
		{
			rule: "anchor-locators-are-unverified",
			documented: "This rule documents a deliberate NON-check: nothing resolves an anchor's " +
				"locator against any content, because anchor content hashes are deferred. It has no " +
				"rejection witness by construction. The pin makes the claim executable instead: an " +
				"anchor naming a path that exists nowhere, at lines nothing has, is ACCEPTED.",
			pin: func(t *testing.T) {
				candidate := diagnosisWitness(t, func(body *contracts.DiagnosisBody) {
					body.Hypotheses[0].Evidence[0].Locator = contracts.Locator{
						Kind: "file-lines", Path: "does/not/exist.go", Start: intPointer(400000), End: intPointer(400001),
					}
				})
				if err := candidate.admit(t, snapshot.TypeRef("diagnosis/v1")); err != nil {
					t.Fatalf(
						"the seal gate rejected an unresolvable anchor locator: %v; if locators are now verified, this rule's documentation is stale and the schema document is the thing to change",
						err,
					)
				}
			},
		},
	}
}
