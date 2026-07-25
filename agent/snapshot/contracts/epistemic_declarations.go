package contracts

import (
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
)

// epistemicFieldStatuses is the epistemic status of every field of every record
// type, keyed by (type, revision).
//
// It is data, not a digest input. Nothing in this file is hashed, sealed, or
// carried on the wire, so a wrong entry is corrected by editing it — which is
// the whole reason the vocabulary lives out here instead of inside the declared
// schema.
//
// How the four values were assigned, and where the line falls:
//
//   - platform — the platform supplies the value and the producer transcribes
//     it. The record envelope's type and schema digest, and each subject's type
//     and digest, are printed into the agent's prompt verbatim with "Copy these
//     exact values into record.json; they are verified again when the output is
//     sealed" (agent/runner/runner.go:298-313). The producer contributes no
//     information whatsoever: exactly one value passes.
//   - derived — the platform recomputes the value from bytes it can see and
//     rejects any mismatch. The producer writes it but cannot choose it.
//   - constrained — the validator confines it to a closed domain (an
//     enumeration, the exposed input ports, or the record's own declared entity
//     ids) and the producer chooses freely inside it.
//   - asserted — nothing verifies it.
//
// Every entry describes the SEAL-TIME gate, and the choice of gate is
// load-bearing. Read-time revalidation is deliberately weaker: a reader holds no
// step declarations, so for most record types it cannot re-check a subject
// against an exposed input at all, and it widens the schema digest from the
// current revision to any accepted one. Describing that gate instead would
// understate the authority of every envelope field and would make a field's
// status change whenever an unrelated descriptor revision is appended. Seal time
// is when the platform certified the value, and that is the authority a reader of
// a stored record inherits — the seal gate is also the only write path
// (agent/snapshot/sealer.go:196, :282), so no stored record ever skipped it.
//
// Two rules were applied throughout. First, where a field was genuinely
// ambiguous the WEAKER claim was taken, and the reason is written down at the
// entry. Second, no status was assigned from the field's name: every one below
// was read off the validator that actually enforces it.
//
// None of that is left to the author's word. Every entry here is pinned by
// TestEveryEpistemicAssignmentIsPinnedByValidatorBehaviour
// (epistemic_authority_test.go), which gives each field a different well-formed
// value in an otherwise-valid record, drives the seal-time gate, and fails if the
// observed behaviour disagrees with the status below — in particular if anything
// declared platform or derived can be varied by a producer and still be sealed.
// An entry with no probe fails too, so adding a field without pinning it is not
// possible. Read that file's header before changing a status here.
//
// Nothing here is 'platform' inside a record BODY, and that is a property worth
// stating: production occurrence and server-derived data are kept out of the
// body by invariant (agent/snapshot/validator.go:12-24,
// repository_change.go:152-161), so a body field can never be platform-stamped.
var epistemicFieldStatuses = map[epistemicKey]map[string]EpistemicStatus{
	{ref: snapshot.TypeRef("review/v1"), revision: 1}: mergeEpistemicFields(
		recordEnvelopeEpistemicFields(),
		map[string]EpistemicStatus{
			// Closed vocabulary, cross-checked against the findings' blocking
			// flags but not computed from them: an inconclusive review with no
			// findings is equally legal (review.go:34-38, :54-59).
			"body/conclusion": EpistemicConstrained,
			"body/summary":    EpistemicAsserted,

			// The producer names its own findings; the validator only requires
			// the ids to be sorted identifiers (review.go:51).
			"body/findings/*/id": EpistemicAsserted,
			// Closed vocabulary (review.go:67-79).
			"body/findings/*/severity": EpistemicConstrained,
			// A bool the validator ties to severity — observation may not block,
			// high and critical must (review.go:68-76). Constrained rather than
			// derived: for low and medium the producer still chooses.
			"body/findings/*/blocking": EpistemicConstrained,
			// An identifier, but from an OPEN set: any category passes
			// (review.go:80-82).
			"body/findings/*/category":       EpistemicAsserted,
			"body/findings/*/title":          EpistemicAsserted,
			"body/findings/*/description":    EpistemicAsserted,
			"body/findings/*/recommendation": EpistemicAsserted,
		},
		// Anchors: the subject must be one the record declared, and the kind is
		// a closed vocabulary; everything about WHERE the anchor points is a
		// producer claim until anchor content hashes land.
		anchorEpistemicFields("body/findings/*/evidence/*"),
	),

	{ref: snapshot.TypeRef("diagnosis/v1"), revision: 1}: mergeEpistemicFields(
		recordEnvelopeEpistemicFields(),
		map[string]EpistemicStatus{
			"body/summary": EpistemicAsserted,
			// Closed vocabulary with an implication on hypothesis count and
			// rank-1 evidence, but not computed from them (diagnosis.go:44-52,
			// :81-83).
			"body/conclusion": EpistemicConstrained,

			"body/hypotheses/*/id": EpistemicAsserted,
			// Ranks must be unique and contiguous from 1 (diagnosis.go:76-80), so
			// the domain is closed at 1..N — but which hypothesis takes which
			// rank is entirely the producer's judgment.
			"body/hypotheses/*/rank":      EpistemicConstrained,
			"body/hypotheses/*/statement": EpistemicAsserted,

			// A self-reported confidence. The number is the archetypal producer
			// claim; only its scale, direction and bounds are pinned.
			"body/hypotheses/*/confidence/value": EpistemicAsserted,
			// Pinned to exactly one value each: a diagnosis confidence must be a
			// higher-is-better unit-interval score (diagnosis.go:108-111).
			"body/hypotheses/*/confidence/scale":     EpistemicConstrained,
			"body/hypotheses/*/confidence/direction": EpistemicConstrained,
			// Consequently these three must be ABSENT — unit-interval forbids
			// bounds and higher-is-better forbids a target (record.go:346-365).
			// A closed domain of one value is still a closed domain.
			"body/hypotheses/*/confidence/minimum": EpistemicConstrained,
			"body/hypotheses/*/confidence/maximum": EpistemicConstrained,
			"body/hypotheses/*/confidence/target":  EpistemicConstrained,

			"body/actions/*/id": EpistemicAsserted,
			// Closed vocabulary (diagnosis.go:129-133).
			"body/actions/*/priority":    EpistemicConstrained,
			"body/actions/*/description": EpistemicAsserted,
			// Every entry must name a hypothesis this same record declares
			// (diagnosis.go:140-144) — a closed domain, supplied by the record
			// rather than by the platform, but closed and enforced.
			"body/actions/*/addresses": EpistemicConstrained,
			"body/actions/*/rationale": EpistemicAsserted,
		},
		anchorEpistemicFields("body/hypotheses/*/evidence/*"),
		anchorEpistemicFields("body/hypotheses/*/counterevidence/*"),
	),

	{ref: snapshot.TypeRef("validation/v1"), revision: 1}: mergeEpistemicFields(
		recordEnvelopeEpistemicFields(),
		map[string]EpistemicStatus{
			// Recomputed by DeriveValidationConclusion and rejected unless it
			// matches exactly (validation.go:54-57). The producer has no say.
			"body/conclusion": EpistemicDerived,
			"body/summary":    EpistemicAsserted,

			"body/checks/*/id": EpistemicAsserted,
			// Closed vocabulary (validation.go:77-81).
			"body/checks/*/kind": EpistemicConstrained,
			"body/checks/*/name": EpistemicAsserted,
			// Ambiguous, and resolved to the weaker claim. When a check has
			// attempts its status is forced equal to the final attempt's
			// (validation.go:106-108), which is derivation; but "skipped"
			// requires no attempts (validation.go:86-90) and is therefore an
			// unverified claim that the check never ran.
			"body/checks/*/status": EpistemicConstrained,
			"body/checks/*/detail": EpistemicAsserted,

			// Pinned to the attempt's own position, always, with no exception
			// (validation.go:99-101).
			"body/checks/*/attempts/*/number": EpistemicDerived,
			// Closed vocabulary (validation.go:116-120).
			"body/checks/*/attempts/*/status": EpistemicConstrained,
			// Parsed as a Go duration and required nonnegative
			// (validation.go:121-124) — a syntactic constraint, not a closed
			// domain, and nothing times the attempt independently.
			"body/checks/*/attempts/*/duration": EpistemicAsserted,
			"body/checks/*/attempts/*/detail":   EpistemicAsserted,
		},
		anchorEpistemicFields("body/checks/*/attempts/*/evidence/*"),
	),

	{ref: snapshot.TypeRef("repository-change/v1"), revision: 1}: mergeEpistemicFields(
		recordEnvelopeEpistemicFields(),
		map[string]EpistemicStatus{
			// Read out of the base repository the subject binds to and compared
			// (repository_change.go:121-123, :124-126).
			"body/repository_id": EpistemicDerived,
			"body/base_sha":      EpistemicDerived,
			// Closed vocabulary, with a cross-field rule on result_commit
			// (repository_change.go:56-70).
			"body/representation": EpistemicConstrained,
			// Where inside the sealed tree the producer put the payload. It must
			// live under content/ and be a regular file (record.go:394-408), but
			// the name is the producer's (repository_change.go:361-373).
			"body/payload/path": EpistemicAsserted,
			// Recomputed over the exact payload bytes
			// (repository_change.go:136-138).
			"body/payload/digest": EpistemicDerived,
			// Required non-empty and nothing more (record.go:404-406).
			"body/payload/media_type": EpistemicAsserted,
			// Recomputed: git write-tree for a patch, the result repository's own
			// tree otherwise (repository_change.go:244-246, :302-303).
			"body/result_tree": EpistemicDerived,
			// Recomputed the same way, and required absent for a patch because a
			// patch proves only the tree (repository_change.go:299-301, :56-60).
			"body/result_commit": EpistemicDerived,
		},
	),

	{ref: snapshot.TypeRef("selection/v1"), revision: 1}: mergeEpistemicFields(
		recordEnvelopeEpistemicFields(),
		map[string]EpistemicStatus{
			// Must be one of the record's own candidate subject ids, each of
			// which is bound to a platform-declared candidate port
			// (selection.go:86-88, :119-121). The domain is closed; the pick
			// inside it is the judge's whole job.
			"body/selected":  EpistemicConstrained,
			"body/rationale": EpistemicAsserted,

			// Every assessment must name a declared candidate subject
			// (selection.go:86-88).
			"body/candidates/*/id": EpistemicConstrained,
			// Unique and within 1..N (selection.go:92-98); the ordering is the
			// judge's.
			"body/candidates/*/rank":    EpistemicConstrained,
			"body/candidates/*/summary": EpistemicAsserted,

			// A score dimension the producer names itself; no closed set of
			// dimensions exists (selection.go:102-111).
			"body/candidates/*/scores/*/id": EpistemicAsserted,
			// The judge's own number.
			"body/candidates/*/scores/*/score/value": EpistemicAsserted,
			// Closed vocabularies (record.go:346-384).
			"body/candidates/*/scores/*/score/scale":     EpistemicConstrained,
			"body/candidates/*/scores/*/score/direction": EpistemicConstrained,
			// Unlike diagnosis confidence, a selection score may be bounded, so
			// these carry producer-chosen numbers whenever the scale allows them
			// (record.go:366-377). Asserted, not constrained.
			"body/candidates/*/scores/*/score/minimum": EpistemicAsserted,
			"body/candidates/*/scores/*/score/maximum": EpistemicAsserted,
			"body/candidates/*/scores/*/score/target":  EpistemicAsserted,
		},
	),

	{ref: snapshot.TypeRef("measurements/v1"), revision: 1}: mergeEpistemicFields(
		recordEnvelopeEpistemicFields(),
		map[string]EpistemicStatus{
			// Closed vocabulary with cross-field rules on metric count and
			// explanation (measurements.go:48-69).
			"body/conclusion":  EpistemicConstrained,
			"body/explanation": EpistemicAsserted,

			"body/metrics/*/id": EpistemicAsserted,
			// The measured number and its unit. Nothing re-measures either; the
			// unit is an identifier from an open set (measurements.go:86-94).
			"body/metrics/*/value": EpistemicAsserted,
			"body/metrics/*/unit":  EpistemicAsserted,
			// Closed vocabulary (measurements.go:95-106).
			"body/metrics/*/direction": EpistemicConstrained,
			// Producer-chosen bounds. They constrain the value, not each other's
			// content (measurements.go:107-120).
			"body/metrics/*/minimum": EpistemicAsserted,
			"body/metrics/*/maximum": EpistemicAsserted,
			"body/metrics/*/target":  EpistemicAsserted,
		},
		anchorEpistemicFields("body/metrics/*/evidence/*"),
	),
}

// recordEnvelopeEpistemicFields is the envelope's contribution to every record
// type. It is a function returning a fresh map, not a shared variable, because
// mergeEpistemicFields writes into its first argument and a shared map would
// leak one type's body into another's declaration.
//
// The envelope is identical across the six types on purpose: it is the same
// three platform-stamped fields and the same subject binding for all of them.
// Per-type differences in what a SUBJECT SET must look like — exactly one base
// for repository-change, exactly the declared candidate ports for selection —
// are structural rules about the array, not claims about a field's value, and
// they belong to the schema and the validators, not here.
func recordEnvelopeEpistemicFields() map[string]EpistemicStatus {
	return map[string]EpistemicStatus{
		// Fixed to the RecordVersion constant by exact comparison
		// (record.go:127-129).
		"record_version": EpistemicPlatform,
		// Fixed to the type the platform expected for this output, printed into
		// the prompt as $NAME_RECORD_TYPE (record.go:133-135,
		// agent/runner/runner.go:307-313).
		"type": EpistemicPlatform,
		// Fixed by the SEAL-TIME gate to exactly the digest SchemaDigestFor hands
		// out, which is the value printed into the prompt as $NAME_RECORD_SCHEMA
		// (record.go currentSchemaDigestOnly, SchemaDigestFor).
		//
		// The read-time gate is looser — it accepts any digest in the type's
		// accepted history (IsAcceptedSchemaDigest), which is a set-membership check
		// and would read as EpistemicConstrained if this table described that gate.
		// It does not, and citing it here was the mistake: a superseded digest is
		// admitted on READ only because the seal that produced those bytes already
		// required exact equality with the digest that was current then. Every
		// stored record therefore carries a digest its producer had no say in, which
		// is what platform means. The looser read rule exists so a descriptor bump
		// is a versioning event instead of retroactive corruption, not to give a
		// producer a choice — and nothing can reach the store without the seal gate,
		// which is the only write path (agent/snapshot/sealer.go:196, :282;
		// agent/functions/repositorymerge/runner.go:899 re-runs it on the bytes it
		// authors).
		"schema": EpistemicPlatform,

		// The producer's own handle for the subject; only its shape is checked
		// (record.go:60-62, :162).
		"subjects/*/id": EpistemicAsserted,
		// Closed vocabulary (record.go:63-68).
		"subjects/*/role": EpistemicConstrained,
		// Must be an exactly-named declared input port (record.go:112-115). The
		// set is closed and platform-supplied; which of them to make a subject is
		// the producer's choice.
		"subjects/*/input": EpistemicConstrained,
		// Both are printed into the prompt for transcription as
		// $NAME_SNAPSHOT_TYPE and $NAME_SNAPSHOT_DIGEST and then compared for
		// exact equality with the exposed input (record.go:116-121,
		// agent/runner/runner.go:302-306). Once `input` is chosen these are
		// fully determined, so the producer contributes nothing.
		"subjects/*/type":   EpistemicPlatform,
		"subjects/*/digest": EpistemicPlatform,
	}
}

// anchorEpistemicFields is one Anchor's contribution at prefix.
//
// Every locator detail is asserted today. Anchor content hashes are deferred
// because nothing yet defines whether Locator.Path is relative to the subject
// archive root or the pod mount path, so no code verifies that an anchor points
// at anything at all. When that lands, path/start/end become derived — and
// because this table is outside every canonical digest, that correction costs a
// one-line edit per entry and no descriptor bump.
func anchorEpistemicFields(prefix string) map[string]EpistemicStatus {
	return map[string]EpistemicStatus{
		// Must name a subject the record declared (record.go:268-271).
		prefix + "/subject": EpistemicConstrained,
		// Closed vocabulary, and it decides which other locator fields may
		// appear at all (record.go:275-315).
		prefix + "/locator/kind":    EpistemicConstrained,
		prefix + "/locator/path":    EpistemicAsserted,
		prefix + "/locator/start":   EpistemicAsserted,
		prefix + "/locator/end":     EpistemicAsserted,
		prefix + "/locator/pointer": EpistemicAsserted,
		prefix + "/locator/value":   EpistemicAsserted,
	}
}

// mergeEpistemicFields combines the parts of one type's declaration and panics
// if two parts claim the same field path. A field with two statuses has no
// status, and the ambiguity would otherwise resolve to whichever literal the
// compiler happened to evaluate last.
func mergeEpistemicFields(base map[string]EpistemicStatus, parts ...map[string]EpistemicStatus) map[string]EpistemicStatus {
	for _, part := range parts {
		for path, status := range part {
			if existing, found := base[path]; found {
				panic(fmt.Sprintf(
					"snapshot contracts: epistemic status for %q declared twice, as %q and %q; every field has exactly one status",
					path, existing, status,
				))
			}
			base[path] = status
		}
	}
	return base
}
