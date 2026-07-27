// Package judge derives a deterministic measurements/v1 record from one sealed
// review/v1 candidate.
//
// It is OFFLINE, HERMETIC, and DETERMINISTIC by construction: it opens exactly
// one materialized input mount, reads the sealed record.json an earlier step
// wrote, counts what that record already states, and emits a measurements/v1
// record.json. It starts no model, resolves no remote, reads no credential, and
// consults no clock, so two runs over the same candidate bytes emit byte-
// identical measurements.
//
// Determinism is the whole point. This function exists to be an EXPERIMENT
// EVALUATOR (agent/experiment), and an evaluator that varied between repetitions
// would inject its own noise into every paired comparison it appears in — the
// scorecard could no longer attribute a delta to the candidate variant. Being
// agent-free is what also keeps an evaluator cell effect-free and zero-dollar,
// so an experiment can be run under the deployment daily cap without declaring a
// budget envelope for the measuring half of every cell.
//
// It is deliberately NOT a rubric judge. Scoring work against authored guidance
// is a stochastic act that needs a model, a budget slice, a transcript and a
// review of its own; that belongs to an `agent:` node in a workflow, not to a
// deterministic function running in an ordinary task pod. What lives here is the
// half that can be trusted to be reproducible.
//
// Errors are reserved for malformed invocation, cancellation, and a candidate
// that does not satisfy its own declared contract. There is no "measured it
// partly" outcome: the platform already guarantees the input is a sealed
// review/v1, so every metric below is derivable whenever the step ran at all.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	// candidateType is the one contract this function can measure. Adding a
	// second measurable contract means adding a second closed metric vocabulary
	// for it, not loosening this one.
	candidateType snapshot.TypeRef = "review/v1"

	// measurementsType is what this function produces. The exact identity written
	// into the envelope is still the platform's declared authority, never this
	// constant — see RecordAuthority.
	measurementsType snapshot.TypeRef = "measurements/v1"

	// recordFileName is the sealed record envelope of both the candidate and the
	// produced value. Each lives at the root of its own task mount, so one name
	// serves both.
	recordFileName = "record.json"

	// maxRecordBytes bounds the candidate record this function will read. It
	// matches the bound the contracts package applies to the same file, so a
	// record it would refuse to decode is refused here before it is read.
	maxRecordBytes = 1 << 20

	// candidateSubjectID names the single subject the produced record carries:
	// the exact review being measured. measurements/v1 permits a record with no
	// subjects at all, but a scorecard number whose provenance cannot be resolved
	// is a number nobody can audit, and without a subject the evidence anchors
	// below would have nothing to resolve against.
	candidateSubjectID = "candidate"

	// derivationExplanation states, inside the value, what kind of numbers these
	// are. It is a constant so the body stays a pure function of the candidate.
	derivationExplanation = "Deterministic counts derived from the candidate review/v1 record. " +
		"They describe what the review reports, not whether the review is correct."

	// indicatorUnit marks a one-hot categorical encoded as a number, always
	// bounded to [0,1]. countUnit marks an unbounded tally. characterUnit marks a
	// length in runes.
	indicatorUnit = "indicator"
	countUnit     = "count"
	characterUnit = "character"
)

// RecordAuthority is the contract identity the PLATFORM declares for one record
// output port, handed to the pod as AGENT_OUTPUT_<PORT>_RECORD_TYPE and
// AGENT_OUTPUT_<PORT>_RECORD_SCHEMA.
//
// It is copied into the record envelope verbatim, exactly as
// agent/functions/repositorymerge does. A producer does not get to choose which
// contract identity its own output advertises: the agent-runner image is
// released independently of the web node, so a pod that stamped its own compiled
// digest would advertise an identity the side that seals the output never
// declared. contracts.Record.AdmitForSeal on the web judges the digest, by the
// side that issued it.
type RecordAuthority struct {
	Type   snapshot.TypeRef
	Schema snapshot.Digest
}

// Request identifies the exact candidate being measured and the declared
// identity of the port the measurements are sealed at.
type Request struct {
	// Candidate is the review/v1 value being measured. Its type and digest are
	// the platform's declaration for the bound input, published to the pod as
	// AGENT_INPUT_<PORT>_SNAPSHOT_TYPE and _SNAPSHOT_DIGEST; they are copied into
	// the record's subject verbatim, for the same reason RecordAuthority is.
	Candidate snapshot.SnapshotRef
	// CandidateInput is the declared port name the candidate is bound at. It
	// becomes the subject's input, which is what the platform re-binds against
	// its own view of the step when it seals the measurements.
	CandidateInput string
	// CandidateRoot is the materialized mount the candidate's record.json lives
	// at the root of.
	CandidateRoot string
	// MeasurementsAuthority is the declared contract identity of the
	// measurements/v1 port. The caller reads it out of the task environment; this
	// package never derives one.
	MeasurementsAuthority RecordAuthority
}

func (request Request) validate() error {
	if err := request.Candidate.Validate(); err != nil {
		return fmt.Errorf("judge: candidate: %w", err)
	}
	if request.Candidate.Type != candidateType {
		return fmt.Errorf(
			"judge: candidate must have exact type %s, got %s",
			candidateType, request.Candidate.Type,
		)
	}
	if strings.TrimSpace(request.CandidateInput) == "" {
		return fmt.Errorf("judge: candidate input port is required")
	}
	if strings.TrimSpace(request.CandidateRoot) == "" {
		return fmt.Errorf("judge: candidate root is required")
	}
	// The measurements identity is declared by the platform, never derived here,
	// so a caller that did not supply one has no way to produce a sealable record
	// and must be told now rather than after the candidate has been read.
	if request.MeasurementsAuthority.Type != measurementsType {
		return fmt.Errorf(
			"judge: measurements type must be exactly %s, got %q",
			measurementsType, request.MeasurementsAuthority.Type,
		)
	}
	if err := request.MeasurementsAuthority.Schema.Validate(); err != nil {
		return fmt.Errorf("judge: measurements schema: %w", err)
	}
	return nil
}

// Measure reads the candidate at the READ-TIME gate and derives the complete
// metric set from it. The returned record is always valid or the call failed:
// there is no half-populated success.
func Measure(ctx context.Context, request Request) (contracts.Record[contracts.MeasurementsBody], error) {
	var empty contracts.Record[contracts.MeasurementsBody]
	if ctx == nil {
		return empty, fmt.Errorf("judge: context is required")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := request.validate(); err != nil {
		return empty, err
	}
	candidate, err := readCandidateRecord(ctx, request.CandidateRoot)
	if err != nil {
		return empty, fmt.Errorf("judge: candidate is not a readable %s: %w", candidateType, err)
	}
	record := contracts.Record[contracts.MeasurementsBody]{
		RecordVersion: contracts.RecordVersion,
		Type:          request.MeasurementsAuthority.Type,
		Schema:        request.MeasurementsAuthority.Schema,
		Subjects: []contracts.Subject{contracts.SubjectFromInput(
			candidateSubjectID, contracts.SubjectRolePrimary, request.CandidateInput, request.Candidate,
		)},
		Body: Derive(candidate.Body),
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return empty, fmt.Errorf("judge: produced invalid %s: %w", measurementsType, err)
	}
	return record, nil
}

// Derive is the pure half: one review body in, the complete metric set out. It
// is exported so a caller can reason about the vocabulary without a mount.
func Derive(review contracts.ReviewBody) contracts.MeasurementsBody {
	body := contracts.MeasurementsBody{
		Conclusion:  "measured",
		Explanation: derivationExplanation,
		Metrics:     make([]contracts.Measurement, 0, len(metrics)),
	}
	for _, definition := range metrics {
		measurement := contracts.Measurement{
			ID:        definition.id,
			Value:     definition.value(review),
			Unit:      definition.unit,
			Direction: definition.direction,
			Evidence: []contracts.Anchor{{
				Subject: candidateSubjectID,
				Locator: contracts.Locator{Kind: "json-pointer", Pointer: definition.pointer},
			}},
		}
		if definition.indicator {
			measurement.Minimum, measurement.Maximum = floatPointer(0), floatPointer(1)
		}
		body.Metrics = append(body.Metrics, measurement)
	}
	// The contract requires metric ids to be lexicographically sorted, so the
	// authored table below never has to be.
	sort.Slice(body.Metrics, func(left, right int) bool {
		return body.Metrics[left].ID < body.Metrics[right].ID
	})
	return body
}

// metric is one stable measurement this function emits.
//
// The set is a CLOSED vocabulary and every entry is a pure function of the
// candidate's own body, which is what lets a scorecard compare them: a metric's
// id, unit, direction and bounds must not drift between cells, or
// agent/experiment/scorecard.go rejects the whole matrix as inconsistent.
type metric struct {
	id        string
	unit      string
	direction string
	// indicator metrics carry declared [0,1] bounds, which is what makes a
	// number readable as one arm of a categorical rather than as a tally.
	indicator bool
	// pointer is the JSON pointer into the candidate's own record.json that the
	// number was derived from. It becomes the metric's single evidence anchor, so
	// a reader can go from a scorecard number back to the exact field it came
	// from. An opaque locator would be the weaker claim here — the field really
	// is addressable, and review/v1 stores its whole value in record.json.
	pointer string
	value   func(contracts.ReviewBody) float64
}

// The directions below all express ONE reading, and it is the reading an
// experiment needs: a review that reports LESS unresolved risk about the thing
// it reviewed sorts as better. That is what makes the vocabulary usable for the
// case these evaluators exist for — comparing variants of a workflow that
// produces work, where the review is the measurement instrument.
//
// Direction is a sign convention, not a verdict: the scorecard uses it only to
// orient a paired delta, and promotion stays a human decision. Assertions on a
// negative-control fixture ("this planted defect must produce at least one
// blocking finding") are expressed with comparators and do not consult it at
// all.
var metrics = []metric{
	// review/v1 forbids a blocking finding under "accept" and requires one under
	// "changes-required", so the three indicators below and the blocking count
	// are consistent by contract, not by this function's good behavior.
	{
		id: "review.conclusion.accept", unit: indicatorUnit, direction: "higher-is-better",
		indicator: true, pointer: "/body/conclusion", value: conclusionIs("accept"),
	},
	{
		id: "review.conclusion.changes-required", unit: indicatorUnit, direction: "lower-is-better",
		indicator: true, pointer: "/body/conclusion", value: conclusionIs("changes-required"),
	},
	{
		// "inconclusive" is the outcome that resolves nothing, so it sorts with
		// the risk-reporting arm rather than between the other two.
		id: "review.conclusion.inconclusive", unit: indicatorUnit, direction: "lower-is-better",
		indicator: true, pointer: "/body/conclusion", value: conclusionIs("inconclusive"),
	},
	{
		id: "review.findings.blocking", unit: countUnit, direction: "lower-is-better",
		pointer: "/body/findings", value: func(review contracts.ReviewBody) float64 {
			count := 0
			for _, finding := range review.Findings {
				if finding.Blocking {
					count++
				}
			}
			return float64(count)
		},
	},
	{
		id: "review.findings.severity.critical", unit: countUnit, direction: "lower-is-better",
		pointer: "/body/findings", value: severityCount("critical"),
	},
	{
		id: "review.findings.severity.high", unit: countUnit, direction: "lower-is-better",
		pointer: "/body/findings", value: severityCount("high"),
	},
	{
		id: "review.findings.severity.low", unit: countUnit, direction: "lower-is-better",
		pointer: "/body/findings", value: severityCount("low"),
	},
	{
		id: "review.findings.severity.medium", unit: countUnit, direction: "lower-is-better",
		pointer: "/body/findings", value: severityCount("medium"),
	},
	{
		id: "review.findings.severity.observation", unit: countUnit, direction: "lower-is-better",
		pointer: "/body/findings", value: severityCount("observation"),
	},
	{
		id: "review.findings.total", unit: countUnit, direction: "lower-is-better",
		pointer: "/body/findings", value: func(review contracts.ReviewBody) float64 {
			return float64(len(review.Findings))
		},
	},
	{
		// review/v1 already rejects a blank summary, so this measures how much
		// explanation the review carries above that floor. A review that decides
		// and explains is more useful than one that decides and does not.
		id: "review.summary.characters", unit: characterUnit, direction: "higher-is-better",
		pointer: "/body/summary", value: func(review contracts.ReviewBody) float64 {
			return float64(utf8.RuneCountInString(strings.TrimSpace(review.Summary)))
		},
	},
}

func conclusionIs(conclusion string) func(contracts.ReviewBody) float64 {
	return func(review contracts.ReviewBody) float64 {
		if review.Conclusion == conclusion {
			return 1
		}
		return 0
	}
}

func severityCount(severity string) func(contracts.ReviewBody) float64 {
	return func(review contracts.ReviewBody) float64 {
		count := 0
		for _, finding := range review.Findings {
			if finding.Severity == severity {
				count++
			}
		}
		return float64(count)
	}
}

// readCandidateRecord reads the candidate at the READ-TIME gate: it was sealed
// by an earlier step, so it may legitimately pin a superseded schema digest, and
// its subjects name that step's inputs rather than this one's. The contracts
// package owns the bounded strict decode, the envelope shape, the declared
// schema and the body invariants, so this package keeps no second copy of them.
func readCandidateRecord(ctx context.Context, candidateRoot string) (contracts.Record[contracts.ReviewBody], error) {
	var empty contracts.Record[contracts.ReviewBody]
	root, err := os.OpenRoot(candidateRoot)
	if err != nil {
		return empty, fmt.Errorf("open candidate root: %w", err)
	}
	payload, readErr := readBoundedRecord(root)
	closeErr := root.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return empty, fmt.Errorf("read %s: %w", recordFileName, err)
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return contracts.DecodeSealedReviewRecord(payload)
}

// readBoundedRecord refuses an oversized or non-regular record BEFORE reading
// it. Checking the length after the read would already have pulled whatever the
// mount contained into the pod's memory, which is the failure the bound exists
// to prevent.
func readBoundedRecord(root *os.Root) ([]byte, error) {
	info, err := root.Lstat(recordFileName)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", recordFileName)
	}
	if info.Size() > maxRecordBytes {
		return nil, fmt.Errorf("%s exceeds size limit of %d bytes", recordFileName, maxRecordBytes)
	}
	payload, err := root.ReadFile(recordFileName)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxRecordBytes {
		return nil, fmt.Errorf("%s exceeds size limit of %d bytes", recordFileName, maxRecordBytes)
	}
	return payload, nil
}

// WriteMeasurements materializes the derived record as a sealed measurements/v1
// record.json beneath the caller-provided output mount.
func WriteMeasurements(ctx context.Context, outputRoot string, record contracts.Record[contracts.MeasurementsBody]) error {
	if ctx == nil {
		return fmt.Errorf("judge: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputRoot) == "" {
		return fmt.Errorf("judge: output root is required")
	}
	if err := validateMeasurementsEnvelope(record); err != nil {
		return fmt.Errorf("judge: invalid %s: %w", measurementsType, err)
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("judge: invalid %s: %w", measurementsType, err)
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("judge: marshal %s: %w", measurementsType, err)
	}
	payload = append(payload, '\n')
	root, err := os.OpenRoot(outputRoot)
	if err != nil {
		return fmt.Errorf("judge: open output root: %w", err)
	}
	writeErr := root.WriteFile(recordFileName, payload, 0600)
	closeErr := root.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("judge: write %s: %w", recordFileName, err)
	}
	return nil
}

// validateMeasurementsEnvelope checks everything this function is the authority
// on, and deliberately stops short of the schema-DIGEST gate.
//
// The digest arrived from the web node as AGENT_OUTPUT_<PORT>_RECORD_SCHEMA and
// is copied through verbatim. The agent-runner image is released independently
// of the web, so a pod that re-judged the digest against its own compiled table
// would reject a perfectly good identity the moment the two builds differ.
// contracts.Record.AdmitForSeal on the web is where the digest is judged, by the
// side that issued it.
func validateMeasurementsEnvelope(record contracts.Record[contracts.MeasurementsBody]) error {
	if record.RecordVersion != contracts.RecordVersion {
		return fmt.Errorf("record_version must be exactly %s", contracts.RecordVersion)
	}
	if record.Type != measurementsType {
		return fmt.Errorf("record type must be exactly %s, got %q", measurementsType, record.Type)
	}
	if err := record.Schema.Validate(); err != nil {
		return fmt.Errorf("record schema: %w", err)
	}
	ids := make([]string, len(record.Subjects))
	inputs := make(map[string]struct{}, len(record.Subjects))
	for index, subject := range record.Subjects {
		if err := subject.Validate(); err != nil {
			return fmt.Errorf("subjects[%d]: %w", index, err)
		}
		if _, found := inputs[subject.Input]; found {
			return fmt.Errorf("subjects[%d].input %q is duplicate", index, subject.Input)
		}
		inputs[subject.Input] = struct{}{}
		ids[index] = subject.ID
	}
	return contracts.ValidateEntityIDs("subjects", ids)
}

func floatPointer(value float64) *float64 { return &value }
