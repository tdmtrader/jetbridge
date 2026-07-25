package contracts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// This file makes an epistemic assignment a testable claim about the validator
// rather than a note the author wrote next to it.
//
// # What is being derived, and what cannot be
//
// The four statuses answer "on whose authority does this value stand?", and the
// only evidence for an answer is what the validator does when the value changes.
// Give one field of an otherwise-valid record a different well-formed value and
// the seal-time gate either admits it or refuses it. Aggregated over several
// alternatives, that yields three distinguishable observations:
//
//	every alternative refused  -> the producer contributes nothing
//	some refused, some admitted -> the producer chooses inside an enforced domain
//	nothing refused             -> nothing verifies the value
//
// platform and derived are NOT separable this way, and pretending otherwise would
// be dishonest: both mean exactly one value passes, and they differ only in where
// that value comes from — a constant the platform hands over, or a recomputation
// from bytes the platform can see. That distinction is documentary. What matters
// for trust is the boundary between "the producer could have written something
// else and it would have been sealed" and "it could not", and that boundary IS
// observable. So the derivation below fixes exactly that, and the mechanical rule
// is:
//
//	platform, derived  every alternative must be refused, and at least one
//	                   alternative must exist. This is the direction that
//	                   actively misleads a reader, because it presents an agent's
//	                   claim as a platform fact, so it is the strictest rule.
//	constrained        at least one alternative must be refused. A domain of one
//	                   value is still a closed domain, so an admitted alternative
//	                   is not required — declaring constrained where the truth is
//	                   platform understates authority, which is safe.
//	asserted           at least one alternative must be admitted. Asserted claims
//	                   nothing, so it cannot over-claim; the rule catches the
//	                   opposite error, a value the validator actually pins being
//	                   described as an unverified claim.
//
// # Which gate the table describes
//
// The statuses describe the SEAL-TIME gate, and they have to. Read-time
// revalidation is deliberately weaker — a reader holds no step declarations, so
// for most record types RevalidateSealed cannot re-check a subject against an
// exposed input at all, and the schema digest widens from the current revision to
// any accepted one. Reading status off the read gate would therefore understate
// the authority of every envelope field and would make a field's status change
// when an unrelated descriptor revision is appended. Seal time is when the
// platform certified the value; that is the authority a reader of a stored record
// inherits.
//
// # Why not derive the table instead of declaring it
//
// It was considered and rejected. Deriving statuses at runtime would mean either
// shipping the probe corpus in production code — mutating candidate records to
// see what the validator says, which is a test harness, not a library — or
// collapsing platform and derived into one value, which throws away the only
// documentation of where a pinned value comes from. The declaration also has to
// survive a validator that cannot run: a reader rendering a stored record has no
// git binary, no base repository and no scratch policy, and must still be able to
// say what a field means. So the table stays declared, and this file is the gate
// that keeps it honest.

// fieldProbe is one alternative-value experiment against one declared field.
type fieldProbe struct {
	// field is the DECLARATION path exactly as the epistemic table spells it.
	field string

	// at are the concrete addresses inside record.json to overwrite, all with the
	// same value. It defaults to field with every "*" replaced by "0".
	//
	// More than one address is occasionally the honest experiment rather than a
	// shortcut. A producer-chosen entity id appears both on the subject and on the
	// body entity that references it; changing one alone breaks a self-consistency
	// rule and would read as "the producer could not have chosen this", when in
	// fact it chose both halves at once.
	at []string

	// values are the alternatives to write. Each is validated independently
	// against an otherwise-untouched copy of the fixture.
	values []any
}

func (probe fieldProbe) addresses() []string {
	if len(probe.at) > 0 {
		return probe.at
	}
	return []string{strings.ReplaceAll(probe.field, "*", "0")}
}

// authorityObservation is what the seal-time gate was observed to do across every
// alternative offered for one declared field.
type authorityObservation struct {
	admitted []string
	refused  []string
}

// recordFixture is one valid record of one type, plus everything needed to
// re-validate a mutated copy of it.
type recordFixture struct {
	rawType           string
	record            map[string]any
	extraFiles        map[string][]byte
	validationContext snapshot.ValidationContext
	probes            []fieldProbe
}

// TestEveryEpistemicAssignmentIsPinnedByValidatorBehaviour is the gate. It fails
// if any declared field has no probe, or if any probe's field is not declared, or
// if what the validator does disagrees with the status the table claims.
func TestEveryEpistemicAssignmentIsPinnedByValidatorBehaviour(t *testing.T) {
	for _, fixture := range epistemicFixtures(t) {
		t.Run(fixture.rawType, func(t *testing.T) {
			ref := mustTypeRef(t, fixture.rawType)
			declaration, found := contracts.CurrentEpistemicDeclarationFor(ref)
			if !found {
				t.Fatalf("CurrentEpistemicDeclarationFor(%q) not found", fixture.rawType)
			}
			observed := observeFieldAuthority(t, fixture)
			for _, complaint := range authorityComplaints(declaration.Fields, observed) {
				t.Error(complaint)
			}
		})
	}
}

// TestTheAuthorityGateRejectsAnOverclaimedAssignment is the gate's own gate.
//
// A harness that reports nothing when an assignment is wrong is worse than no
// harness, because it looks like evidence. So the same observations that pass
// above are replayed against declarations that have been deliberately corrupted,
// one field at a time, and each corruption must be reported.
func TestTheAuthorityGateRejectsAnOverclaimedAssignment(t *testing.T) {
	fixture := reviewFixture(t)
	ref := mustTypeRef(t, fixture.rawType)
	declaration, found := contracts.CurrentEpistemicDeclarationFor(ref)
	if !found {
		t.Fatalf("CurrentEpistemicDeclarationFor(%q) not found", fixture.rawType)
	}
	observed := observeFieldAuthority(t, fixture)
	if complaints := authorityComplaints(declaration.Fields, observed); len(complaints) > 0 {
		t.Fatalf("the real review/v1 declaration is not clean, so corruption proves nothing:\n\t%s",
			strings.Join(complaints, "\n\t"))
	}

	corruptions := []struct {
		name  string
		field string
		to    contracts.EpistemicStatus
	}{
		{
			name:  "agent prose presented as a platform fact",
			field: "body/summary", to: contracts.EpistemicPlatform,
		},
		{
			name:  "agent prose presented as recomputed by the platform",
			field: "body/findings/*/description", to: contracts.EpistemicDerived,
		},
		{
			name:  "an unverified value presented as a closed domain",
			field: "body/findings/*/title", to: contracts.EpistemicConstrained,
		},
		{
			name:  "a platform-stamped value presented as a producer claim",
			field: "schema", to: contracts.EpistemicAsserted,
		},
		{
			name:  "a platform-stamped value presented as a producer choice",
			field: "subjects/*/digest", to: contracts.EpistemicAsserted,
		},
	}
	for _, corruption := range corruptions {
		t.Run(corruption.name, func(t *testing.T) {
			corrupted := make(map[string]contracts.EpistemicStatus, len(declaration.Fields))
			for path, status := range declaration.Fields {
				corrupted[path] = status
			}
			if corrupted[corruption.field] == corruption.to {
				t.Fatalf("%q is already %q, so this corruption changes nothing", corruption.field, corruption.to)
			}
			corrupted[corruption.field] = corruption.to

			complaints := authorityComplaints(corrupted, observed)
			if len(complaints) == 0 {
				t.Fatalf("declaring %q as %q was not reported; the gate has no teeth",
					corruption.field, corruption.to)
			}
			named := false
			for _, complaint := range complaints {
				if strings.Contains(complaint, corruption.field) {
					named = true
				}
			}
			if !named {
				t.Fatalf("complaints %v do not name the corrupted field %q", complaints, corruption.field)
			}
		})
	}

	// Deleting a field's probe must also be reported: an unpinned assignment is
	// author judgment again, which is the whole thing this gate replaces.
	thinned := make(map[string]authorityObservation, len(observed))
	for path, observation := range observed {
		if path == "body/conclusion" {
			continue
		}
		thinned[path] = observation
	}
	complaints := authorityComplaints(declaration.Fields, thinned)
	if len(complaints) != 1 || !strings.Contains(complaints[0], "body/conclusion") {
		t.Fatalf("removing the probe for body/conclusion produced %v, want exactly one complaint naming it", complaints)
	}
}

// TestTheAuthorityGateDeliberatelyToleratesUnderclaims records the gate's limit,
// so nobody reads a passing run as "every status is exactly right".
//
// Weakening a status is not reported, and that is a decision rather than an
// oversight. A reader told "the producer chose this inside an enforced set" when
// the truth is "the producer had no choice" believes something weaker than the
// facts, which loses information but never manufactures trust. Reporting it would
// also require separating platform from derived behaviourally, which is
// impossible — both mean exactly one value passes.
//
// The direction that DOES manufacture trust — a producer's claim described as a
// platform fact — is the one the gate above refuses, and the corruption cases
// prove it does.
func TestTheAuthorityGateDeliberatelyToleratesUnderclaims(t *testing.T) {
	fixture := reviewFixture(t)
	observed := observeFieldAuthority(t, fixture)

	// A closed domain the validator enforces, honestly declared constrained.
	severity, probed := observed["body/findings/*/severity"]
	if !probed || len(severity.admitted) == 0 || len(severity.refused) == 0 {
		t.Fatalf("body/findings/*/severity was observed as %+v, want both admitted and refused alternatives", severity)
	}
	underclaimed := map[string]contracts.EpistemicStatus{
		"body/findings/*/severity": contracts.EpistemicAsserted,
	}
	if complaints := authorityComplaints(underclaimed, map[string]authorityObservation{
		"body/findings/*/severity": severity,
	}); len(complaints) != 0 {
		t.Fatalf("weakening constrained to asserted was reported (%v); if that is now a violation, "+
			"the documented rule and this test disagree", complaints)
	}

	// The reverse — an unverified value dressed up as an enforced domain — is not
	// tolerated, which is what makes the asymmetry a choice rather than a hole.
	title, probed := observed["body/findings/*/title"]
	if !probed || len(title.refused) != 0 {
		t.Fatalf("body/findings/*/title was observed as %+v, want no refused alternative", title)
	}
	if complaints := authorityComplaints(
		map[string]contracts.EpistemicStatus{"body/findings/*/title": contracts.EpistemicConstrained},
		map[string]authorityObservation{"body/findings/*/title": title},
	); len(complaints) == 0 {
		t.Fatal("strengthening asserted to constrained was not reported")
	}
}

// authorityComplaints compares one declaration against what the validator was
// observed to do. It is a pure function of the two so the gate's own gate can
// replay real observations against corrupted declarations.
func authorityComplaints(
	declared map[string]contracts.EpistemicStatus,
	observed map[string]authorityObservation,
) []string {
	var complaints []string
	for _, path := range sortedKeys(declared) {
		status := declared[path]
		observation, probed := observed[path]
		if !probed {
			complaints = append(complaints, fmt.Sprintf(
				"%q is declared %q with no probe: an epistemic status that no validator behaviour pins is author judgment, "+
					"which is what this gate replaces", path, status))
			continue
		}
		switch status {
		case contracts.EpistemicPlatform, contracts.EpistemicDerived:
			if len(observation.admitted) > 0 {
				complaints = append(complaints, fmt.Sprintf(
					"%q is declared %q, but the seal-time gate ADMITTED %s; a producer that can write a different value "+
						"and still get it sealed contributes information, so this presents an agent claim as a platform fact",
					path, status, strings.Join(observation.admitted, ", ")))
			}
			if len(observation.refused) == 0 {
				complaints = append(complaints, fmt.Sprintf(
					"%q is declared %q but no alternative was refused, so nothing pins it", path, status))
			}
		case contracts.EpistemicConstrained:
			if len(observation.refused) == 0 {
				complaints = append(complaints, fmt.Sprintf(
					"%q is declared %q, but every alternative was admitted (%s); there is no closed domain here",
					path, status, strings.Join(observation.admitted, ", ")))
			}
		case contracts.EpistemicAsserted:
			if len(observation.admitted) == 0 {
				complaints = append(complaints, fmt.Sprintf(
					"%q is declared %q, but the seal-time gate refused every alternative (%s); the validator pins this "+
						"value, so calling it an unverified producer claim understates it",
					path, status, strings.Join(observation.refused, ", ")))
			}
		}
	}
	for _, path := range sortedKeys(observed) {
		if _, found := declared[path]; !found {
			complaints = append(complaints, fmt.Sprintf(
				"a probe names %q, which no epistemic declaration covers", path))
		}
	}
	return complaints
}

// observeFieldAuthority drives every probe of one fixture through the SEAL-TIME
// gate and returns, per declared field, which alternatives were admitted and
// which were refused.
func observeFieldAuthority(t *testing.T, fixture recordFixture) map[string]authorityObservation {
	t.Helper()

	// An invalid fixture would make every alternative look refused, which would
	// silently turn every field into "platform".
	if _, err := validateFiles(t, fixture.rawType, fixture.files(t, fixture.record), fixture.validationContext); err != nil {
		t.Fatalf("the unmutated %s fixture does not validate, so no probe result means anything: %v",
			fixture.rawType, err)
	}

	observed := make(map[string]authorityObservation, len(fixture.probes))
	for _, probe := range fixture.probes {
		if len(probe.values) == 0 {
			t.Fatalf("probe for %q offers no alternative value", probe.field)
		}
		observation := observed[probe.field]
		for _, value := range probe.values {
			candidate := cloneJSONObject(t, fixture.record)
			for _, address := range probe.addresses() {
				setJSONAt(t, candidate, address, value)
			}
			describe := fmt.Sprintf("%s=%v", strings.Join(probe.addresses(), "+"), value)
			if _, err := validateFiles(t, fixture.rawType, fixture.files(t, candidate), fixture.validationContext); err != nil {
				observation.refused = append(observation.refused, describe)
				continue
			}
			observation.admitted = append(observation.admitted, describe)
		}
		observed[probe.field] = observation
	}
	return observed
}

func (fixture recordFixture) files(t *testing.T, record map[string]any) map[string][]byte {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal %s record.json: %v", fixture.rawType, err)
	}
	files := make(map[string][]byte, len(fixture.extraFiles)+1)
	for name, contents := range fixture.extraFiles {
		files[name] = contents
	}
	files["record.json"] = encoded
	return files
}

func cloneJSONObject(t *testing.T, source map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("clone record: marshal: %v", err)
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatalf("clone record: unmarshal: %v", err)
	}
	return clone
}

// setJSONAt overwrites one value inside a decoded record.json. Addresses are the
// frozen field-path spelling with array positions in place of "*"; positions are
// legal here and nowhere else, because a probe addresses a fixture it wrote
// itself rather than a stored record.
func setJSONAt(t *testing.T, document map[string]any, address string, value any) {
	t.Helper()
	segments := strings.Split(address, "/")
	var cursor any = document
	for _, segment := range segments[:len(segments)-1] {
		cursor = jsonChild(t, cursor, segment, address)
	}
	last := segments[len(segments)-1]
	switch container := cursor.(type) {
	case map[string]any:
		container[last] = value
	case []any:
		index := jsonIndex(t, last, address)
		if index >= len(container) {
			t.Fatalf("probe address %q: index %d is past the end of a %d-element array", address, index, len(container))
		}
		container[index] = value
	default:
		t.Fatalf("probe address %q: %T is neither an object nor an array", address, cursor)
	}
}

func jsonChild(t *testing.T, cursor any, segment, address string) any {
	t.Helper()
	switch container := cursor.(type) {
	case map[string]any:
		child, found := container[segment]
		if !found {
			t.Fatalf("probe address %q: %q is absent from the fixture", address, segment)
		}
		return child
	case []any:
		index := jsonIndex(t, segment, address)
		if index >= len(container) {
			t.Fatalf("probe address %q: index %d is past the end of a %d-element array", address, index, len(container))
		}
		return container[index]
	default:
		t.Fatalf("probe address %q: %q descends into a %T", address, segment, cursor)
		return nil
	}
}

func jsonIndex(t *testing.T, segment, address string) int {
	t.Helper()
	index, err := strconv.Atoi(segment)
	if err != nil || index < 0 {
		t.Fatalf("probe address %q: %q is not an array position", address, segment)
	}
	return index
}

func sortedKeys[V any](source map[string]V) []string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// --- shared probe generators ---------------------------------------------

// envelopeSpec is the per-type information the envelope probes need: which
// subject can be perturbed without breaking a body cross-reference, and which
// alternatives that type's own subject-set rules leave open.
type envelopeSpec struct {
	rawType string

	// spare is the index of the subject the type-independent probes perturb.
	spare int

	// idAddresses are every address that must change together for a subject id to
	// remain self-consistent, and altID the value they take.
	idAddresses []string
	altID       string

	altRoles  []any
	altInputs []any
}

func envelopeProbes(t *testing.T, spec envelopeSpec) []fieldProbe {
	t.Helper()
	otherType := "diagnosis/v1"
	if spec.rawType == otherType {
		otherType = "review/v1"
	}
	otherSchema, found := contracts.SchemaDigestFor(mustTypeRef(t, otherType))
	if !found {
		t.Fatalf("SchemaDigestFor(%q) not found", otherType)
	}
	subject := func(field string) []string {
		return []string{fmt.Sprintf("subjects/%d/%s", spec.spare, field)}
	}
	return []fieldProbe{{
		field:  "record_version",
		values: []any{"1.0.1", "0.9.0", ""},
	}, {
		field:  "type",
		values: []any{otherType, "not a type ref"},
	}, {
		// The seal-time gate requires exactly the digest SchemaDigestFor handed the
		// agent, so no other digest — not another type's current one, not one from
		// no revision at all — may be admitted.
		field:  "schema",
		values: []any{otherSchema.String(), recordDigest('b').String()},
	}, {
		field:  "subjects/*/id",
		at:     spec.idAddresses,
		values: []any{spec.altID},
	}, {
		field:  "subjects/*/role",
		at:     subject("role"),
		values: append(append([]any(nil), spec.altRoles...), "not-a-role"),
	}, {
		field:  "subjects/*/input",
		at:     subject("input"),
		values: append(append([]any(nil), spec.altInputs...), "undeclared-port"),
	}, {
		field:  "subjects/*/type",
		at:     subject("type"),
		values: []any{"opaque/v1"},
	}, {
		field:  "subjects/*/digest",
		at:     subject("digest"),
		values: []any{recordDigest('c').String()},
	}}
}

// anchorFixtureEvidence is the three-anchor array every anchor-bearing fixture
// uses. One anchor per locator kind, because the kind decides which other locator
// fields may appear at all — probing `pointer` on a file-lines anchor could only
// ever be refused, which would make an unverified field look platform-stamped.
func anchorFixtureEvidence(subject string) []any {
	return []any{
		map[string]any{
			"subject": subject,
			"locator": map[string]any{"kind": "file-lines", "path": "pkg/thing.go", "start": 1, "end": 2},
		},
		map[string]any{
			"subject": subject,
			"locator": map[string]any{"kind": "json-pointer", "pointer": "/checks/0"},
		},
		map[string]any{
			"subject": subject,
			"locator": map[string]any{"kind": "opaque", "value": "run-42"},
		},
	}
}

// anchorProbes pins one Anchor's seven declared fields. field is the declaration
// prefix ending in "/*", at the concrete address of the evidence array, and
// otherSubject a declared subject id the anchors do not already use.
func anchorProbes(field, at, otherSubject string) []fieldProbe {
	return []fieldProbe{{
		field:  field + "/subject",
		at:     []string{at + "/0/subject"},
		values: []any{otherSubject, "no-such-subject"},
	}, {
		field:  field + "/locator/kind",
		at:     []string{at + "/0/locator/kind"},
		values: []any{"log-lines", "not-a-locator-kind"},
	}, {
		field:  field + "/locator/path",
		at:     []string{at + "/0/locator/path"},
		values: []any{"pkg/other.go"},
	}, {
		field:  field + "/locator/start",
		at:     []string{at + "/0/locator/start"},
		values: []any{2},
	}, {
		field:  field + "/locator/end",
		at:     []string{at + "/0/locator/end"},
		values: []any{9},
	}, {
		field:  field + "/locator/pointer",
		at:     []string{at + "/1/locator/pointer"},
		values: []any{"/checks/1"},
	}, {
		field:  field + "/locator/value",
		at:     []string{at + "/2/locator/value"},
		values: []any{"run-43"},
	}}
}

// --- fixtures ------------------------------------------------------------

const (
	recordSubjectChangeDigestFill = 'a'
	recordSubjectBaseDigestFill   = 'd'
)

func epistemicFixtures(t *testing.T) []recordFixture {
	t.Helper()
	return []recordFixture{
		reviewFixture(t),
		diagnosisFixture(t),
		validationFixture(t),
		selectionFixture(t),
		measurementsFixture(t),
		repositoryChangeAuthorityFixture(t),
	}
}

// recordEnvelope builds the envelope of a valid record of rawType.
func recordEnvelope(t *testing.T, rawType string, subjects []any, body map[string]any) map[string]any {
	t.Helper()
	schema, found := contracts.SchemaDigestFor(mustTypeRef(t, rawType))
	if !found {
		t.Fatalf("SchemaDigestFor(%q) not found", rawType)
	}
	return map[string]any{
		"record_version": contracts.RecordVersion,
		"type":           rawType,
		"schema":         schema.String(),
		"subjects":       subjects,
		"body":           body,
	}
}

func recordSubject(id, role, input, rawType string, digestFill byte) map[string]any {
	return map[string]any{
		"id":     id,
		"role":   role,
		"input":  input,
		"type":   rawType,
		"digest": recordDigest(digestFill).String(),
	}
}

// judgementInputs exposes the ports the five document-shaped record fixtures bind
// to: one change, one base, and two spares whose type and digest are identical to
// the base so that "which port did this subject come from" has an admissible
// alternative.
func judgementInputs(t *testing.T) snapshot.ValidationContext {
	t.Helper()
	change := snapshot.SnapshotRef{ID: 1, Type: mustTypeRef(t, "repository-change/v1"), Digest: recordDigest(recordSubjectChangeDigestFill)}
	base := snapshot.SnapshotRef{ID: 2, Type: mustTypeRef(t, "repository/v1"), Digest: recordDigest(recordSubjectBaseDigestFill)}
	return validationContextFor(t, map[string]snapshot.SnapshotRef{
		"change": change,
		"base":   base,
		"spare":  base,
		"spare2": base,
	})
}

// judgementSubjects is the subject set the review, diagnosis and validation
// fixtures share: one primary, one supporting subject the anchors point at, and
// one spare that nothing references so envelope probes have somewhere safe to
// perturb.
func judgementSubjects() []any {
	return []any{
		recordSubject("base", "evidence", "base", "repository/v1", recordSubjectBaseDigestFill),
		recordSubject("change", "primary", "change", "repository-change/v1", recordSubjectChangeDigestFill),
		recordSubject("spare", "context", "spare", "repository/v1", recordSubjectBaseDigestFill),
	}
}

func judgementEnvelopeSpec(rawType string) envelopeSpec {
	return envelopeSpec{
		rawType:     rawType,
		spare:       2,
		idAddresses: []string{"subjects/2/id"},
		altID:       "spare-renamed",
		// primary is refused because the set already has one; candidate and base
		// are refused because this record shape does not allow them.
		altRoles:  []any{"reference", "candidate"},
		altInputs: []any{"spare2", "base"},
	}
}

func reviewFixture(t *testing.T) recordFixture {
	t.Helper()
	body := map[string]any{
		"conclusion": "changes-required",
		"summary":    "one blocking finding",
		"findings": []any{
			map[string]any{
				"id": "f-001", "severity": "medium", "blocking": true,
				"category": "correctness", "title": "t", "description": "d",
				"evidence":       anchorFixtureEvidence("change"),
				"recommendation": "r",
			},
			map[string]any{
				"id": "f-002", "severity": "observation", "blocking": false,
				"category": "style", "title": "t", "description": "d",
				"evidence": []any{},
			},
			map[string]any{
				"id": "f-003", "severity": "low", "blocking": false,
				"category": "style", "title": "t", "description": "d",
				"evidence": anchorFixtureEvidence("change"),
			},
		},
	}
	probes := append(envelopeProbes(t, judgementEnvelopeSpec("review/v1")), []fieldProbe{{
		field: "body/conclusion", values: []any{"inconclusive", "not-a-conclusion"},
	}, {
		field: "body/summary", values: []any{"a different summary"},
	}, {
		field: "body/findings/*/id", at: []string{"body/findings/1/id"}, values: []any{"f-002x"},
	}, {
		// low and medium leave blocking to the producer; observation forbids it and
		// high forces it, so the domain is enforced but not determined.
		field: "body/findings/*/severity", values: []any{"low", "observation", "not-a-severity"},
	}, {
		field: "body/findings/*/blocking", at: []string{"body/findings/2/blocking"}, values: []any{true},
	}, {
		field: "body/findings/*/blocking", at: []string{"body/findings/1/blocking"}, values: []any{true},
	}, {
		field: "body/findings/*/category", values: []any{"any.category-at.all"},
	}, {
		field: "body/findings/*/title", values: []any{"a different title"},
	}, {
		field: "body/findings/*/description", values: []any{"a different description"},
	}, {
		field: "body/findings/*/recommendation", values: []any{"a different recommendation"},
	}}...)
	probes = append(probes, anchorProbes("body/findings/*/evidence/*", "body/findings/0/evidence", "base")...)
	return recordFixture{
		rawType:           "review/v1",
		record:            recordEnvelope(t, "review/v1", judgementSubjects(), body),
		validationContext: judgementInputs(t),
		probes:            probes,
	}
}

func diagnosisFixture(t *testing.T) recordFixture {
	t.Helper()
	confidence := func(value float64) map[string]any {
		return map[string]any{"value": value, "scale": "unit-interval", "direction": "higher-is-better"}
	}
	body := map[string]any{
		"summary":    "the cache is stale",
		"conclusion": "identified",
		"hypotheses": []any{
			map[string]any{
				"id": "h-001", "rank": 1, "statement": "s", "confidence": confidence(0.8),
				"evidence":        anchorFixtureEvidence("change"),
				"counterevidence": anchorFixtureEvidence("change"),
			},
			map[string]any{
				"id": "h-002", "rank": 2, "statement": "s", "confidence": confidence(0.2),
				"evidence":        []any{},
				"counterevidence": []any{},
			},
		},
		"actions": []any{
			map[string]any{
				"id": "a-001", "priority": "next", "description": "d",
				"addresses": []any{"h-001"}, "rationale": "r",
			},
		},
	}
	probes := append(envelopeProbes(t, judgementEnvelopeSpec("diagnosis/v1")), []fieldProbe{{
		field: "body/summary", values: []any{"a different summary"},
	}, {
		field: "body/conclusion", values: []any{"suspected", "not-a-conclusion"},
	}, {
		field: "body/hypotheses/*/id", at: []string{"body/hypotheses/1/id"}, values: []any{"h-002x"},
	}, {
		// Ranks must be the exact set 1..N, so every single-field alternative is
		// refused: the domain is closed even though which hypothesis takes which
		// rank is the producer's judgment.
		field: "body/hypotheses/*/rank", values: []any{2, 3, 0},
	}, {
		field: "body/hypotheses/*/statement", values: []any{"a different statement"},
	}, {
		field: "body/hypotheses/*/confidence/value", values: []any{0.1, 1},
	}, {
		field: "body/hypotheses/*/confidence/scale", values: []any{"bounded", "unbounded", "not-a-scale"},
	}, {
		field: "body/hypotheses/*/confidence/direction", values: []any{"lower-is-better", "target"},
	}, {
		// unit-interval forbids bounds and higher-is-better forbids a target, so all
		// three are pinned absent — a closed domain of exactly one value.
		field: "body/hypotheses/*/confidence/minimum", values: []any{0},
	}, {
		field: "body/hypotheses/*/confidence/maximum", values: []any{1},
	}, {
		field: "body/hypotheses/*/confidence/target", values: []any{0.5},
	}, {
		field: "body/actions/*/id", values: []any{"a-002"},
	}, {
		field: "body/actions/*/priority", values: []any{"immediate", "optional", "not-a-priority"},
	}, {
		field: "body/actions/*/description", values: []any{"a different description"},
	}, {
		field: "body/actions/*/addresses", values: []any{[]any{"h-002"}, []any{"h-999"}},
	}, {
		field: "body/actions/*/rationale", values: []any{"a different rationale"},
	}}...)
	probes = append(probes, anchorProbes("body/hypotheses/*/evidence/*", "body/hypotheses/0/evidence", "base")...)
	probes = append(probes, anchorProbes("body/hypotheses/*/counterevidence/*", "body/hypotheses/0/counterevidence", "base")...)
	return recordFixture{
		rawType:           "diagnosis/v1",
		record:            recordEnvelope(t, "diagnosis/v1", judgementSubjects(), body),
		validationContext: judgementInputs(t),
		probes:            probes,
	}
}

func validationFixture(t *testing.T) recordFixture {
	t.Helper()
	body := map[string]any{
		// DeriveValidationConclusion: one passed check and one skipped check is
		// "incomplete", and nothing else.
		"conclusion": "incomplete",
		"summary":    "one check passed on retry, one skipped",
		"checks": []any{
			map[string]any{
				"id": "c-001", "kind": "test", "name": "unit", "status": "passed",
				"detail": "d",
				"attempts": []any{
					map[string]any{
						"number": 1, "status": "failed", "duration": "1s",
						"evidence": anchorFixtureEvidence("change"), "detail": "d",
					},
					map[string]any{
						"number": 2, "status": "passed", "duration": "2s",
						"evidence": []any{},
					},
				},
			},
			map[string]any{
				"id": "c-002", "kind": "lint", "name": "vet", "status": "skipped",
				"attempts": []any{},
			},
		},
	}
	probes := append(envelopeProbes(t, judgementEnvelopeSpec("validation/v1")), []fieldProbe{{
		field: "body/conclusion", values: []any{"passed", "failed", "error", "not-a-conclusion"},
	}, {
		field: "body/summary", values: []any{"a different summary"},
	}, {
		field: "body/checks/*/id", at: []string{"body/checks/1/id"}, values: []any{"c-002x"},
	}, {
		field: "body/checks/*/kind", values: []any{"build", "security", "not-a-kind"},
	}, {
		field: "body/checks/*/name", values: []any{"a different name"},
	}, {
		// A check's status is forced equal to its final attempt's, and "skipped"
		// forbids attempts, so both checks refuse every alternative.
		field: "body/checks/*/status", values: []any{"failed", "skipped", "not-a-status"},
	}, {
		field: "body/checks/*/detail", values: []any{"a different detail"},
	}, {
		field: "body/checks/*/attempts/*/number", values: []any{2, 0, 5},
	}, {
		// A non-final attempt may be any of the three; the check's own status is
		// what pins the final one.
		field: "body/checks/*/attempts/*/status", values: []any{"error", "not-a-status"},
	}, {
		field: "body/checks/*/attempts/*/duration", values: []any{"5m", "not-a-duration"},
	}, {
		field: "body/checks/*/attempts/*/detail", values: []any{"a different detail"},
	}}...)
	probes = append(probes, anchorProbes(
		"body/checks/*/attempts/*/evidence/*", "body/checks/0/attempts/0/evidence", "base")...)
	return recordFixture{
		rawType:           "validation/v1",
		record:            recordEnvelope(t, "validation/v1", judgementSubjects(), body),
		validationContext: judgementInputs(t),
		probes:            probes,
	}
}

func measurementsFixture(t *testing.T) recordFixture {
	t.Helper()
	body := map[string]any{
		"conclusion":  "measured",
		"explanation": "two metrics",
		"metrics": []any{
			map[string]any{
				"id": "m-001", "value": 5, "unit": "ms", "direction": "higher-is-better",
				"minimum": 0, "maximum": 10,
				"evidence": anchorFixtureEvidence("base"),
			},
			map[string]any{
				"id": "m-002", "value": 5, "unit": "ms", "direction": "target",
				"target": 5, "evidence": []any{},
			},
		},
	}
	subjects := []any{
		recordSubject("base", "evidence", "base", "repository/v1", recordSubjectBaseDigestFill),
		recordSubject("spare", "context", "spare", "repository/v1", recordSubjectBaseDigestFill),
	}
	probes := append(envelopeProbes(t, envelopeSpec{
		rawType:     "measurements/v1",
		spare:       1,
		idAddresses: []string{"subjects/1/id"},
		altID:       "spare-renamed",
		// measurements imposes no subject-set shape of its own, so any role in the
		// envelope vocabulary is admissible here.
		altRoles:  []any{"reference", "primary"},
		altInputs: []any{"spare2"},
	}), []fieldProbe{{
		field: "body/conclusion", values: []any{"partial", "not-applicable", "not-a-conclusion"},
	}, {
		field: "body/explanation", values: []any{"a different explanation"},
	}, {
		field: "body/metrics/*/id", at: []string{"body/metrics/1/id"}, values: []any{"m-002x"},
	}, {
		field: "body/metrics/*/value", values: []any{7, 99},
	}, {
		field: "body/metrics/*/unit", values: []any{"seconds"},
	}, {
		field: "body/metrics/*/direction", values: []any{"lower-is-better", "not-a-direction"},
	}, {
		field: "body/metrics/*/minimum", values: []any{1, 99},
	}, {
		field: "body/metrics/*/maximum", values: []any{20, -1},
	}, {
		field: "body/metrics/*/target", at: []string{"body/metrics/1/target"}, values: []any{6},
	}}...)
	probes = append(probes, anchorProbes("body/metrics/*/evidence/*", "body/metrics/0/evidence", "spare")...)
	return recordFixture{
		rawType:           "measurements/v1",
		record:            recordEnvelope(t, "measurements/v1", subjects, body),
		validationContext: judgementInputs(t),
		probes:            probes,
	}
}

func selectionFixture(t *testing.T) recordFixture {
	t.Helper()
	score := func(value float64, direction string) map[string]any {
		scored := map[string]any{
			"value": value, "scale": "bounded", "direction": direction,
			"minimum": 0, "maximum": 10,
		}
		if direction == "target" {
			scored["target"] = 5
		}
		return scored
	}
	body := map[string]any{
		// "right" rather than "left" so renaming the left candidate is a two-address
		// change rather than a three-address one.
		"selected":  "right",
		"rationale": "right scored higher",
		"candidates": []any{
			map[string]any{
				"id": "left", "rank": 2, "summary": "s",
				"scores": []any{map[string]any{"id": "s-001", "score": score(5, "higher-is-better")}},
			},
			map[string]any{
				"id": "right", "rank": 1, "summary": "s",
				"scores": []any{map[string]any{"id": "s-001", "score": score(7, "target")}},
			},
		},
	}
	subjects := []any{
		recordSubject("left", "candidate", "left", "repository-change/v1", 'a'),
		recordSubject("right", "candidate", "right", "repository-change/v1", 'e'),
	}
	inputs := map[string]snapshot.SnapshotRef{
		"left":   {ID: 1, Type: mustTypeRef(t, "repository-change/v1"), Digest: recordDigest('a')},
		"right":  {ID: 2, Type: mustTypeRef(t, "repository-change/v1"), Digest: recordDigest('e')},
		"rubric": {ID: 3, Type: mustTypeRef(t, "opaque/v1"), Digest: recordDigest('f')},
	}
	probes := append(envelopeProbes(t, envelopeSpec{
		rawType: "selection/v1",
		spare:   0,
		// A candidate's id is written twice, on the subject and on the assessment
		// that scores it. The producer chose both, so both move together.
		idAddresses: []string{"subjects/0/id", "body/candidates/0/id"},
		altID:       "aleft",
		// Selection admits exactly one role and exactly the declared candidate
		// ports, one subject each, so neither has an admissible alternative — a
		// closed domain of one value.
		altRoles:  nil,
		altInputs: []any{"rubric"},
	}), []fieldProbe{{
		field: "body/selected", values: []any{"left", "no-such-candidate"},
	}, {
		field: "body/rationale", values: []any{"a different rationale"},
	}, {
		field: "body/candidates/*/id", values: []any{"no-such-candidate"},
	}, {
		field: "body/candidates/*/rank", values: []any{1, 3, 0},
	}, {
		field: "body/candidates/*/summary", values: []any{"a different summary"},
	}, {
		field: "body/candidates/*/scores/*/id", values: []any{"s-002"},
	}, {
		field: "body/candidates/*/scores/*/score/value", values: []any{3, 99},
	}, {
		field: "body/candidates/*/scores/*/score/scale", values: []any{"unit-interval", "unbounded", "not-a-scale"},
	}, {
		field: "body/candidates/*/scores/*/score/direction", values: []any{"lower-is-better", "not-a-direction"},
	}, {
		field: "body/candidates/*/scores/*/score/minimum", values: []any{1, 99},
	}, {
		field: "body/candidates/*/scores/*/score/maximum", values: []any{20, -1},
	}, {
		field: "body/candidates/*/scores/*/score/target",
		at:    []string{"body/candidates/1/scores/0/score/target"}, values: []any{6},
	}}...)
	return recordFixture{
		rawType:           "selection/v1",
		record:            recordEnvelope(t, "selection/v1", subjects, body),
		validationContext: candidateValidationContextFor(t, inputs, "left", "right"),
		probes:            probes,
	}
}

func repositoryChangeAuthorityFixture(t *testing.T) recordFixture {
	t.Helper()
	base := newGitRepository(t, "")
	archive, digest := canonicalRepositoryArchive(t, base)
	metadata := repositoryMetadataForDirectory(t, base)

	writeTestFile(t, filepath.Join(base, "README.md"), "patched\n")
	patch := []byte(runTestGit(t, base, "diff", "--binary", "--no-ext-diff") + "\n")
	runTestGit(t, base, "add", "README.md")
	resultTree := runTestGit(t, base, "write-tree")

	body := map[string]any{
		"repository_id":  metadata.RepositoryID,
		"base_sha":       metadata.HeadSHA,
		"representation": "patch",
		"result_tree":    resultTree,
		"payload": map[string]any{
			"path":       "content/change.patch",
			"digest":     digestBytes(patch),
			"media_type": "text/x-diff",
		},
	}
	subjects := []any{map[string]any{
		"id": "base", "role": "base", "input": "base",
		"type": "repository/v1", "digest": digest.String(),
	}}
	otherSHA := strings.Repeat("1", len(metadata.HeadSHA))
	probes := append(envelopeProbes(t, envelopeSpec{
		rawType:     "repository-change/v1",
		spare:       0,
		idAddresses: []string{"subjects/0/id"},
		altID:       "base-renamed",
		// Exactly one base subject is allowed, so role has no admissible
		// alternative; the port does, because the same repository is exposed twice.
		altRoles:  nil,
		altInputs: []any{"spare"},
	}), []fieldProbe{{
		field: "body/repository_id", values: []any{recordDigest('9').String()},
	}, {
		field: "body/base_sha", values: []any{otherSHA},
	}, {
		field: "body/representation", values: []any{"git-tree", "git-bundle", "not-a-representation"},
	}, {
		field: "body/payload/path", values: []any{"content/same-bytes.patch"},
	}, {
		field: "body/payload/digest", values: []any{recordDigest('9').String()},
	}, {
		field: "body/payload/media_type", values: []any{"text/plain"},
	}, {
		field: "body/result_tree", values: []any{otherSHA},
	}, {
		// A patch proves only the tree, so result_commit is pinned absent here and
		// recomputed for the other two representations.
		field: "body/result_commit", values: []any{metadata.HeadSHA, otherSHA},
	}}...)
	return recordFixture{
		rawType: "repository-change/v1",
		record:  recordEnvelope(t, "repository-change/v1", subjects, body),
		extraFiles: map[string][]byte{
			"content/change.patch": patch,
			// Byte-identical, so the payload PATH has an admissible alternative
			// while the payload DIGEST still has none.
			"content/same-bytes.patch": patch,
		},
		validationContext: repositoryInputsContext(t, archive, digest, "base", "spare"),
		probes:            probes,
	}
}

// repositoryInputsContext exposes one repository archive under several port names.
// The repository-change validator opens its base input, so the ports have to be
// backed by real bytes rather than by a reference alone.
func repositoryInputsContext(t *testing.T, archive []byte, digest snapshot.Digest, names ...string) snapshot.ValidationContext {
	t.Helper()
	typeRef := mustTypeRef(t, "repository/v1")
	inputs := make(map[string]snapshot.SnapshotRef, len(names))
	for index, name := range names {
		id, err := snapshot.NewSnapshotID(int64(index) + 1)
		if err != nil {
			t.Fatalf("NewSnapshotID(): %v", err)
		}
		inputs[name] = snapshot.SnapshotRef{ID: id, Type: typeRef, Digest: digest}
	}
	validationContext, err := snapshot.NewValidationContext(inputs, func(_ context.Context, name string, _ snapshot.SnapshotRef) (io.ReadCloser, error) {
		if _, found := inputs[name]; !found {
			return nil, fmt.Errorf("no such input %q", name)
		}
		return io.NopCloser(bytes.NewReader(archive)), nil
	})
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}
	return validationContext
}
