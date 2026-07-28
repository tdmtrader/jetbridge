package contracts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

const RecordVersion = "1.0.0"

// The record contract types whose gates are spelled out in this package. Each
// one has an entry in recordSchemaHistories.
const (
	reviewType           = snapshot.TypeRef("review/v1")
	diagnosisType        = snapshot.TypeRef("diagnosis/v1")
	validationType       = snapshot.TypeRef("validation/v1")
	repositoryChangeType = snapshot.TypeRef("repository-change/v1")
	measurementsType     = snapshot.TypeRef("measurements/v1")
)

type SubjectRole string

const (
	SubjectRolePrimary  SubjectRole = "primary"
	SubjectRoleBase     SubjectRole = "base"
	SubjectRoleEvidence SubjectRole = "evidence"
	SubjectRoleContext  SubjectRole = "context"
	// SubjectRoleCandidate survives the removal of selection/v1: measurements/v1's
	// declared schema (schemas/measurements.v1.rev2.json) permits it, and that
	// document's bytes are inside a frozen canonical descriptor. Dropping the role
	// would mean editing the document, which is a descriptor-digest bump and a
	// revalidation break for every measurements record already sealed.
	SubjectRoleCandidate SubjectRole = "candidate"
	SubjectRoleReference SubjectRole = "reference"
)

type StableSnapshotRef struct {
	Type   snapshot.TypeRef `json:"type"`
	Digest snapshot.Digest  `json:"digest"`
}

func (ref StableSnapshotRef) Validate() error {
	if err := ref.Type.Validate(); err != nil {
		return err
	}
	return ref.Digest.Validate()
}

type Subject struct {
	ID     string           `json:"id"`
	Role   SubjectRole      `json:"role"`
	Input  string           `json:"input"`
	Type   snapshot.TypeRef `json:"type"`
	Digest snapshot.Digest  `json:"digest"`
}

func SubjectFromInput(id string, role SubjectRole, input string, ref snapshot.SnapshotRef) Subject {
	return Subject{ID: id, Role: role, Input: input, Type: ref.Type, Digest: ref.Digest}
}

func (subject Subject) StableRef() StableSnapshotRef {
	return StableSnapshotRef{Type: subject.Type, Digest: subject.Digest}
}

func (subject Subject) Validate() error {
	if err := ValidateIdentifier("subject id", subject.ID); err != nil {
		return err
	}
	switch subject.Role {
	case SubjectRolePrimary, SubjectRoleBase, SubjectRoleEvidence,
		SubjectRoleContext, SubjectRoleCandidate, SubjectRoleReference:
	default:
		return fmt.Errorf("subject %q role must be one of primary, base, evidence, context, candidate, reference", subject.ID)
	}
	if strings.TrimSpace(subject.Input) == "" {
		return fmt.Errorf("subject %q input is required", subject.ID)
	}
	if err := subject.Type.Validate(); err != nil {
		return fmt.Errorf("subject %q type: %w", subject.ID, err)
	}
	if err := subject.Digest.Validate(); err != nil {
		return fmt.Errorf("subject %q digest: %w", subject.ID, err)
	}
	return nil
}

type Record[T any] struct {
	RecordVersion string           `json:"record_version"`
	Type          snapshot.TypeRef `json:"type"`
	Schema        snapshot.Digest  `json:"schema"`
	Subjects      []Subject        `json:"subjects"`
	Body          T                `json:"body"`
}

func NewRecord[T any](ref snapshot.TypeRef, subjects []Subject, body T) (Record[T], error) {
	schema, found := SchemaDigestFor(ref)
	if !found {
		return Record[T]{}, fmt.Errorf("snapshot contracts: %q is not a record type", ref)
	}
	record := Record[T]{
		RecordVersion: RecordVersion,
		Type:          ref,
		Schema:        schema,
		Subjects:      append([]Subject(nil), subjects...),
		Body:          body,
	}
	if err := record.validateEnvelopeShape(ref, currentSchemaDigestOnly); err != nil {
		return Record[T]{}, err
	}
	return record, nil
}

// AdmitForSeal is the SEAL-TIME gate for a record envelope. It judges a
// candidate an agent just authored, so it requires the CURRENT schema digest for
// the type and binds every subject to a server-declared exposed input.
//
// The declarations are a mandatory argument rather than an option: seal-time
// admission that cannot consult the server's own view of the step has nothing
// left to check a producer's claims against.
func (record Record[T]) AdmitForSeal(expected snapshot.TypeRef, declarations snapshot.ValidationContext) error {
	if err := record.validateEnvelopeShape(expected, currentSchemaDigestOnly); err != nil {
		return err
	}
	return record.RebindSubjectsToExposedInputs(declarations)
}

// RevalidateSealed is the READ-TIME gate for a record envelope. It judges bytes
// the platform already sealed and certified, so it accepts any accepted schema
// digest for the type and takes no declarations at all — a reader loading a
// stored record has none, and requiring them would make the corpus unreadable.
func (record Record[T]) RevalidateSealed(expected snapshot.TypeRef) error {
	return record.validateEnvelopeShape(expected, anyAcceptedSchemaDigest)
}

// RebindSubjectsToExposedInputs checks every subject against the exposed inputs
// of a live step.
//
// At seal time this is part of admission and AdmitForSeal always runs it. It is
// exposed separately only for the read-time caller that happens to hold live
// inputs — an offline merge re-checking the candidate it was handed against its
// own port bindings. It is an ADDITIONAL check there, never a substitute for
// RevalidateSealed, and never a precondition for a record being readable.
func (record Record[T]) RebindSubjectsToExposedInputs(validationContext snapshot.ValidationContext) error {
	for _, subject := range record.Subjects {
		ref, found := validationContext.Input(subject.Input)
		if !found {
			return fmt.Errorf("record subject %q input %q is not an exact declared input", subject.ID, subject.Input)
		}
		if subject.Type != ref.Type {
			return fmt.Errorf("record subject %q type %q does not match input %q type %q", subject.ID, subject.Type, subject.Input, ref.Type)
		}
		if subject.Digest != ref.Digest {
			return fmt.Errorf("record subject %q digest does not match input %q digest", subject.ID, subject.Input)
		}
	}
	return nil
}

// schemaAdmission is which of a record type's accepted schema digests one gate
// lets a record pin.
//
// The zero value admits nothing. A new envelope path that forgets to state which
// gate it is therefore fails closed rather than silently inheriting the looser
// rule, which is the failure mode this type exists to prevent — there is no
// third value to construct and no boolean to invert.
type schemaAdmission int

const (
	schemaAdmissionUnstated schemaAdmission = iota

	// currentSchemaDigestOnly is the SEAL-TIME rule. An agent-authored candidate
	// must pin the digest the platform is handing out right now. Accepting an
	// older revision here would let a producer choose which contract identity its
	// own output advertises, which is authority manufactured from a producer's
	// claim, and would falsify the promise the runner prompt makes to the agent
	// that the values it was handed are verified again when the output is sealed.
	currentSchemaDigestOnly

	// anyAcceptedSchemaDigest is the READ-TIME rule. Stored records are re-decoded
	// on read (agent/publisher/gateway.go, agent/projection/review.go,
	// agent/projection/repository_change.go, agent/functions/repositorymerge), so
	// demanding the current digest there would turn a descriptor bump into
	// retroactive corruption of every record sealed under the previous revision,
	// surfacing as ErrCorruptSnapshot rather than as a versioning event.
	anyAcceptedSchemaDigest
)

func (admission schemaAdmission) admit(expected snapshot.TypeRef, schema snapshot.Digest) error {
	current, found := SchemaDigestFor(expected)
	if !found {
		return fmt.Errorf("snapshot contracts: %q is not a record type", expected)
	}
	switch admission {
	case currentSchemaDigestOnly:
		if schema != current {
			return fmt.Errorf(
				"record schema must be exactly the current schema digest %q for %q at seal time, got %q",
				current, expected, schema,
			)
		}
	case anyAcceptedSchemaDigest:
		if !IsAcceptedSchemaDigest(expected, schema) {
			return fmt.Errorf("record schema %q is not an accepted schema digest for %q (current is %q)", schema, expected, current)
		}
	default:
		return fmt.Errorf("snapshot contracts: record schema admission for %q did not state a gate", expected)
	}
	return nil
}

func (record Record[T]) validateEnvelopeShape(expected snapshot.TypeRef, admission schemaAdmission) error {
	if record.RecordVersion != RecordVersion {
		return fmt.Errorf("record_version must be exactly %s", RecordVersion)
	}
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("expected record type: %w", err)
	}
	if record.Type != expected {
		return fmt.Errorf("record type must be exactly %q", expected)
	}
	if err := admission.admit(expected, record.Schema); err != nil {
		return err
	}
	ids := make([]string, len(record.Subjects))
	inputs := make(map[string]struct{}, len(record.Subjects))
	for index, subject := range record.Subjects {
		if err := subject.Validate(); err != nil {
			return fmt.Errorf("subjects[%d]: %w", index, err)
		}
		ids[index] = subject.ID
		if _, found := inputs[subject.Input]; found {
			return fmt.Errorf("subjects[%d].input %q is duplicate", index, subject.Input)
		}
		inputs[subject.Input] = struct{}{}
	}
	return ValidateEntityIDs("subjects", ids)
}

// DecodeRecordForSeal decodes a candidate record.json the producing step just
// wrote and runs the SEAL-TIME envelope gate over the exact bytes.
func DecodeRecordForSeal[T any](data []byte, expected snapshot.TypeRef, target *Record[T]) error {
	return decodeRecord(data, expected, target, currentSchemaDigestOnly)
}

// DecodeSealedRecord decodes a stored record.json the platform already sealed
// and runs the READ-TIME envelope gate over it.
func DecodeSealedRecord[T any](data []byte, expected snapshot.TypeRef, target *Record[T]) error {
	return decodeRecord(data, expected, target, anyAcceptedSchemaDigest)
}

func decodeRecord[T any](data []byte, expected snapshot.TypeRef, target *Record[T], admission schemaAdmission) error {
	if target == nil {
		return fmt.Errorf("snapshot contracts: record target is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("snapshot contracts: decode record.json: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("snapshot contracts: record.json contains trailing JSON")
		}
		return fmt.Errorf("snapshot contracts: decode trailing record.json data: %w", err)
	}
	if err := target.validateEnvelopeShape(expected, admission); err != nil {
		return fmt.Errorf("snapshot contracts: record.json: %w", err)
	}
	return nil
}

// admitRecordForSeal reads one candidate record.json out of a tree the producing
// step just wrote and runs the SEAL-TIME envelope gate over it.
func admitRecordForSeal[T any](ctx context.Context, root *os.Root, expected snapshot.TypeRef, declarations snapshot.ValidationContext) (Record[T], error) {
	var record Record[T]
	if err := decodeStrictDocument(ctx, root, "record.json", &record); err != nil {
		return Record[T]{}, err
	}
	if err := record.AdmitForSeal(expected, declarations); err != nil {
		return Record[T]{}, fmt.Errorf("snapshot contracts: record.json: %w", err)
	}
	return record, nil
}

// readSealedRecord reads one record.json out of a tree the platform already
// sealed and runs the READ-TIME envelope gate over it.
func readSealedRecord[T any](ctx context.Context, root *os.Root, expected snapshot.TypeRef) (Record[T], error) {
	var record Record[T]
	if err := decodeStrictDocument(ctx, root, "record.json", &record); err != nil {
		return Record[T]{}, err
	}
	if err := record.RevalidateSealed(expected); err != nil {
		return Record[T]{}, fmt.Errorf("snapshot contracts: record.json: %w", err)
	}
	return record, nil
}

var recordIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func ValidateIdentifier(label, value string) error {
	if !recordIdentifierPattern.MatchString(value) {
		return fmt.Errorf("%s must be a non-empty identifier containing only letters, digits, dot, underscore, colon, or hyphen", label)
	}
	return nil
}

func ValidateEntityIDs(label string, ids []string) error {
	for index, id := range ids {
		if err := ValidateIdentifier(fmt.Sprintf("%s[%d].id", label, index), id); err != nil {
			return err
		}
		if index == 0 {
			continue
		}
		switch strings.Compare(ids[index-1], id) {
		case 0:
			return fmt.Errorf("%s[%d].id %q is duplicate", label, index, id)
		case 1:
			return fmt.Errorf("%s must be lexicographically sorted by id", label)
		}
	}
	return nil
}

type subjectShape int

const (
	onePrimaryWithSupportingSubjects subjectShape = 1
	onePrimaryWithBaseSubjects       subjectShape = 2
)

func validateRecordSubjects(subjects []Subject, shape subjectShape) (map[string]struct{}, error) {
	if len(subjects) == 0 {
		return nil, fmt.Errorf("record requires at least one subject")
	}
	ids := make(map[string]struct{}, len(subjects))
	primaryCount := 0
	for _, subject := range subjects {
		ids[subject.ID] = struct{}{}
		switch shape {
		case onePrimaryWithSupportingSubjects, onePrimaryWithBaseSubjects:
			switch subject.Role {
			case SubjectRolePrimary:
				primaryCount++
			case SubjectRoleEvidence, SubjectRoleContext, SubjectRoleReference:
			case SubjectRoleBase:
				if shape != onePrimaryWithBaseSubjects {
					return nil, fmt.Errorf("subject %q role %q is not valid for this record", subject.ID, subject.Role)
				}
			default:
				return nil, fmt.Errorf("subject %q role %q is not valid for this record", subject.ID, subject.Role)
			}
		}
	}
	if (shape == onePrimaryWithSupportingSubjects || shape == onePrimaryWithBaseSubjects) && primaryCount != 1 {
		return nil, fmt.Errorf("record requires exactly one primary subject")
	}
	return ids, nil
}

type Anchor struct {
	Subject string  `json:"subject"`
	Locator Locator `json:"locator"`
}

type Locator struct {
	Kind    string `json:"kind"`
	Path    string `json:"path,omitempty"`
	Start   *int   `json:"start,omitempty"`
	End     *int   `json:"end,omitempty"`
	Pointer string `json:"pointer,omitempty"`
	Value   string `json:"value,omitempty"`
}

func (anchor Anchor) Validate(subjects map[string]struct{}) error {
	if _, found := subjects[anchor.Subject]; !found {
		return fmt.Errorf("anchor subject %q is not declared", anchor.Subject)
	}
	return anchor.Locator.Validate()
}

func (locator Locator) Validate() error {
	switch locator.Kind {
	case "file-lines", "log-lines":
		if err := validatePOSIXPath("anchor path", locator.Path); err != nil {
			return err
		}
		if locator.Start == nil || locator.End == nil || *locator.Start < 1 || *locator.End < *locator.Start {
			return fmt.Errorf("%s anchor requires positive start and end lines with end >= start", locator.Kind)
		}
		if locator.Pointer != "" || locator.Value != "" {
			return fmt.Errorf("%s anchor contains fields for another locator kind", locator.Kind)
		}
	case "json-pointer":
		if !validJSONPointer(locator.Pointer) {
			return fmt.Errorf("json-pointer anchor pointer must be a valid non-root JSON pointer")
		}
		if locator.Path != "" || locator.Start != nil || locator.End != nil || locator.Value != "" {
			return fmt.Errorf("json-pointer anchor contains fields for another locator kind")
		}
	case "byte-range":
		if err := validatePOSIXPath("anchor path", locator.Path); err != nil {
			return err
		}
		if locator.Start == nil || locator.End == nil || *locator.Start < 0 || *locator.End <= *locator.Start {
			return fmt.Errorf("byte-range anchor requires nonnegative start and end > start")
		}
		if locator.Pointer != "" || locator.Value != "" {
			return fmt.Errorf("byte-range anchor contains fields for another locator kind")
		}
	case "opaque":
		if strings.TrimSpace(locator.Value) == "" {
			return fmt.Errorf("opaque anchor value is required")
		}
		if locator.Path != "" || locator.Start != nil || locator.End != nil || locator.Pointer != "" {
			return fmt.Errorf("opaque anchor contains fields for another locator kind")
		}
	default:
		return fmt.Errorf("anchor locator kind must be one of file-lines, log-lines, json-pointer, byte-range, opaque")
	}
	return nil
}

func validJSONPointer(pointer string) bool {
	if pointer == "" || pointer[0] != '/' {
		return false
	}
	for index := 0; index < len(pointer); index++ {
		if pointer[index] != '~' {
			continue
		}
		if index+1 >= len(pointer) || (pointer[index+1] != '0' && pointer[index+1] != '1') {
			return false
		}
		index++
	}
	return true
}

type Score struct {
	Value     float64  `json:"value"`
	Scale     string   `json:"scale"`
	Direction string   `json:"direction"`
	Minimum   *float64 `json:"minimum,omitempty"`
	Maximum   *float64 `json:"maximum,omitempty"`
	Target    *float64 `json:"target,omitempty"`
}

func (score Score) Validate() error {
	if !finiteNumber(score.Value) {
		return fmt.Errorf("score value must be finite")
	}
	switch score.Direction {
	case "higher-is-better", "lower-is-better":
		if score.Target != nil {
			return fmt.Errorf("score target is valid only for target direction")
		}
	case "target":
		if score.Target == nil || !finiteNumber(*score.Target) {
			return fmt.Errorf("score target direction requires a finite target")
		}
	default:
		return fmt.Errorf("score direction must be one of higher-is-better, lower-is-better, target")
	}
	switch score.Scale {
	case "unit-interval":
		if score.Value < 0 || score.Value > 1 {
			return fmt.Errorf("unit-interval score value must be within 0..1")
		}
		if score.Minimum != nil || score.Maximum != nil {
			return fmt.Errorf("unit-interval score must not declare bounds")
		}
	case "bounded":
		if score.Minimum == nil || score.Maximum == nil ||
			!finiteNumber(*score.Minimum) || !finiteNumber(*score.Maximum) ||
			*score.Minimum > *score.Maximum {
			return fmt.Errorf("bounded score requires finite ordered minimum and maximum")
		}
		if score.Value < *score.Minimum || score.Value > *score.Maximum {
			return fmt.Errorf("bounded score value must be within minimum and maximum")
		}
		if score.Target != nil && (*score.Target < *score.Minimum || *score.Target > *score.Maximum) {
			return fmt.Errorf("bounded score target must be within minimum and maximum")
		}
	case "unbounded":
		if score.Minimum != nil || score.Maximum != nil {
			return fmt.Errorf("unbounded score must not declare bounds")
		}
	default:
		return fmt.Errorf("score scale must be one of unit-interval, bounded, unbounded")
	}
	return nil
}

type ContentRef struct {
	Path      string          `json:"path"`
	Digest    snapshot.Digest `json:"digest"`
	MediaType string          `json:"media_type"`
}

func (ref ContentRef) Validate() error {
	if err := validatePOSIXPath("content path", ref.Path); err != nil {
		return err
	}
	if !strings.HasPrefix(ref.Path, "content/") {
		return fmt.Errorf("content path must be beneath content/")
	}
	if err := ref.Digest.Validate(); err != nil {
		return fmt.Errorf("content digest: %w", err)
	}
	if strings.TrimSpace(ref.MediaType) == "" {
		return fmt.Errorf("content media_type is required")
	}
	return nil
}

func finiteNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
