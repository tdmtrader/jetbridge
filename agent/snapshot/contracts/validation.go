package contracts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

type ValidationBody struct {
	Conclusion         string                `json:"conclusion"`
	Summary            string                `json:"summary"`
	Attestation        ValidationAttestation `json:"attestation,omitempty"`
	Checks             []ValidationCheck     `json:"checks"`
	decoded            bool
	attestationPresent bool
}

type ValidationBaseInput struct {
	Input  string           `json:"input"`
	Type   snapshot.TypeRef `json:"type"`
	Digest snapshot.Digest  `json:"digest"`
}
type ValidationAttestation struct {
	CandidateDigest       snapshot.Digest       `json:"candidate_digest"`
	BaseInputs            []ValidationBaseInput `json:"base_inputs"`
	ProfileDigest         snapshot.Digest       `json:"profile_digest"`
	ProtectedConfigDigest snapshot.Digest       `json:"protected_config_digest"`
	CapabilityImage       string                `json:"capability_image"`
	CapabilityImageDigest snapshot.Digest       `json:"capability_image_digest"`
	WorkflowDefinitionID  int                   `json:"workflow_definition_id"`
	WorkflowVersion       int                   `json:"workflow_version"`
	Toolchain             string                `json:"toolchain"`
}

type ValidationCheck struct {
	ID       string              `json:"id"`
	Kind     string              `json:"kind"`
	Name     string              `json:"name"`
	Status   string              `json:"status"`
	Attempts []ValidationAttempt `json:"attempts"`
	Detail   string              `json:"detail,omitempty"`
}

type ValidationAttempt struct {
	Number     int           `json:"number"`
	Status     string        `json:"status"`
	Duration   string        `json:"duration"`
	Log        ValidationLog `json:"log,omitempty"`
	Evidence   []Anchor      `json:"evidence"`
	Detail     string        `json:"detail,omitempty"`
	decoded    bool
	logPresent bool
}

type ValidationLog struct {
	Path        string          `json:"path"`
	Digest      snapshot.Digest `json:"digest"`
	Size        int64           `json:"size"`
	MediaType   string          `json:"media_type"`
	decoded     bool
	sizePresent bool
}
type validationBodyRevision2 struct {
	Conclusion string                     `json:"conclusion"`
	Summary    string                     `json:"summary"`
	Checks     []validationCheckRevision2 `json:"checks"`
}
type validationCheckRevision2 struct {
	ID       string                       `json:"id"`
	Kind     string                       `json:"kind"`
	Name     string                       `json:"name"`
	Status   string                       `json:"status"`
	Attempts []validationAttemptRevision2 `json:"attempts"`
	Detail   string                       `json:"detail,omitempty"`
}
type validationAttemptRevision2 struct {
	Number   int      `json:"number"`
	Status   string   `json:"status"`
	Duration string   `json:"duration"`
	Evidence []Anchor `json:"evidence"`
	Detail   string   `json:"detail,omitempty"`
}
type validationBodyRevision3Declared struct {
	Conclusion  string                             `json:"conclusion"`
	Summary     string                             `json:"summary"`
	Attestation ValidationAttestation              `json:"attestation"`
	Checks      []validationCheckRevision3Declared `json:"checks"`
}
type validationCheckRevision3Declared struct {
	ID       string                               `json:"id"`
	Kind     string                               `json:"kind"`
	Name     string                               `json:"name"`
	Status   string                               `json:"status"`
	Attempts []validationAttemptRevision3Declared `json:"attempts"`
	Detail   string                               `json:"detail,omitempty"`
}
type validationAttemptRevision3Declared struct {
	Number   int                            `json:"number"`
	Status   string                         `json:"status"`
	Duration string                         `json:"duration"`
	Log      validationLogRevision3Declared `json:"log"`
	Evidence []Anchor                       `json:"evidence"`
	Detail   string                         `json:"detail,omitempty"`
}
type validationLogRevision3Declared struct {
	Path      string          `json:"path"`
	Digest    snapshot.Digest `json:"digest"`
	Size      *int64          `json:"size"`
	MediaType string          `json:"media_type"`
}

func (body *ValidationBody) UnmarshalJSON(data []byte) error {
	type wire ValidationBody
	var value wire
	if err := decodeStrictJSON(data, &value); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	*body = ValidationBody(value)
	body.decoded = true
	_, body.attestationPresent = members["attestation"]
	return nil
}

func (body ValidationBody) MarshalJSON() ([]byte, error) {
	if body.Attestation.CandidateDigest == "" && !body.attestationPresent {
		type legacy struct {
			Conclusion string            `json:"conclusion"`
			Summary    string            `json:"summary"`
			Checks     []ValidationCheck `json:"checks"`
		}
		return json.Marshal(legacy{Conclusion: body.Conclusion, Summary: body.Summary, Checks: body.Checks})
	}
	type wire ValidationBody
	return json.Marshal(wire(body))
}
func (attempt *ValidationAttempt) UnmarshalJSON(data []byte) error {
	type wire ValidationAttempt
	var value wire
	if err := decodeStrictJSON(data, &value); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	*attempt = ValidationAttempt(value)
	attempt.decoded = true
	_, attempt.logPresent = members["log"]
	return nil
}

func (attempt ValidationAttempt) MarshalJSON() ([]byte, error) {
	if attempt.Log.Digest == "" && !attempt.logPresent {
		type legacy struct {
			Number   int      `json:"number"`
			Status   string   `json:"status"`
			Duration string   `json:"duration"`
			Evidence []Anchor `json:"evidence"`
			Detail   string   `json:"detail,omitempty"`
		}
		return json.Marshal(legacy{Number: attempt.Number, Status: attempt.Status, Duration: attempt.Duration, Evidence: attempt.Evidence, Detail: attempt.Detail})
	}
	type wire ValidationAttempt
	return json.Marshal(wire(attempt))
}
func (log *ValidationLog) UnmarshalJSON(data []byte) error {
	type wire ValidationLog
	var value wire
	if err := decodeStrictJSON(data, &value); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	if raw, found := members["size"]; !found || string(raw) == "null" {
		return fmt.Errorf("validation log size is required")
	}
	*log = ValidationLog(value)
	log.decoded = true
	log.sizePresent = true
	return nil
}

func (body ValidationBody) Validate(subjects []Subject) error {
	return body.validateForRevision(subjects, 3)
}

func (body ValidationBody) validateForRevision(subjects []Subject, revision int) error {
	shape := onePrimaryWithSupportingSubjects
	if revision >= 3 {
		shape = onePrimaryWithBaseSubjects
	}
	subjectIDs, err := validateRecordSubjects(subjects, shape)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if revision >= 3 {
		if body.decoded && !body.attestationPresent {
			return fmt.Errorf("attestation is required")
		}
		if err := body.Attestation.Validate(subjects); err != nil {
			return fmt.Errorf("attestation: %w", err)
		}
	} else if body.attestationPresent {
		return fmt.Errorf("attestation is not declared before validation/v1 revision 3")
	}
	ids := make([]string, len(body.Checks))
	for index, check := range body.Checks {
		ids[index] = check.ID
		if err := check.validateForRevision(subjectIDs, revision); err != nil {
			return fmt.Errorf("checks[%d]: %w", index, err)
		}
	}
	if err := ValidateEntityIDs("checks", ids); err != nil {
		return err
	}
	derived := DeriveValidationConclusion(body.Checks)
	if body.Conclusion != derived {
		return fmt.Errorf("conclusion must match derived conclusion %q", derived)
	}
	return nil
}

func (body ValidationBody) ValidateDetached() error {
	detached := body
	detached.Checks = append([]ValidationCheck(nil), body.Checks...)
	for checkIndex := range detached.Checks {
		detached.Checks[checkIndex].Attempts = append([]ValidationAttempt(nil), body.Checks[checkIndex].Attempts...)
		for attemptIndex := range detached.Checks[checkIndex].Attempts {
			detached.Checks[checkIndex].Attempts[attemptIndex].Evidence = nil
		}
	}
	return detached.Validate([]Subject{{ID: "primary", Role: SubjectRolePrimary}})
}

func (check ValidationCheck) Validate(subjects map[string]struct{}) error {
	return check.validateForRevision(subjects, 3)
}
func (check ValidationCheck) validateForRevision(subjects map[string]struct{}, revision int) error {
	if err := ValidateIdentifier("check id", check.ID); err != nil {
		return err
	}
	switch check.Kind {
	case "build", "test", "lint", "security", "policy", "custom":
	default:
		return fmt.Errorf("check kind must be one of build, test, lint, security, policy, custom")
	}
	if strings.TrimSpace(check.Name) == "" {
		return fmt.Errorf("check name is required")
	}
	switch check.Status {
	case "skipped":
		if len(check.Attempts) != 0 {
			return fmt.Errorf("skipped check must have no attempts")
		}
		return nil
	case "passed", "failed", "error":
		if len(check.Attempts) == 0 {
			return fmt.Errorf("%s check requires at least one attempt", check.Status)
		}
	default:
		return fmt.Errorf("check status must be one of passed, failed, error, skipped")
	}
	for index, attempt := range check.Attempts {
		if attempt.Number != index+1 {
			return fmt.Errorf("attempt numbers must be contiguous from 1")
		}
		if err := attempt.validateForRevision(subjects, revision); err != nil {
			return fmt.Errorf("attempts[%d]: %w", index, err)
		}
	}
	if final := check.Attempts[len(check.Attempts)-1].Status; check.Status != final {
		return fmt.Errorf("check status %q must match final attempt status %q", check.Status, final)
	}
	return nil
}

func (attempt ValidationAttempt) Validate(subjects map[string]struct{}) error {
	return attempt.validateForRevision(subjects, 3)
}
func (attempt ValidationAttempt) validateForRevision(subjects map[string]struct{}, revision int) error {
	if attempt.Number < 1 {
		return fmt.Errorf("attempt number must be positive")
	}
	switch attempt.Status {
	case "passed", "failed", "error":
	default:
		return fmt.Errorf("attempt status must be one of passed, failed, error")
	}
	duration, err := time.ParseDuration(attempt.Duration)
	if err != nil || duration < 0 {
		return fmt.Errorf("attempt duration must be a nonnegative Go duration")
	}
	if revision >= 3 {
		if attempt.decoded && !attempt.logPresent {
			return fmt.Errorf("log is required")
		}
		if err := attempt.Log.Validate(); err != nil {
			return fmt.Errorf("log: %w", err)
		}
	} else if attempt.logPresent {
		return fmt.Errorf("log is not declared before validation/v1 revision 3")
	}
	for index, anchor := range attempt.Evidence {
		if err := anchor.Validate(subjects); err != nil {
			return fmt.Errorf("evidence[%d]: %w", index, err)
		}
	}
	return nil
}

func (attestation ValidationAttestation) Validate(subjects []Subject) error {
	if err := attestation.CandidateDigest.Validate(); err != nil {
		return err
	}
	for index, base := range attestation.BaseInputs {
		if base.Input == "" || base.Input != strings.TrimSpace(base.Input) {
			return fmt.Errorf("base_inputs[%d].input is required", index)
		}
		if err := base.Type.Validate(); err != nil {
			return fmt.Errorf("base_inputs[%d].type: %w", index, err)
		}
		if err := base.Digest.Validate(); err != nil {
			return fmt.Errorf("base_inputs[%d].digest: %w", index, err)
		}
		if index > 0 && base.Input <= attestation.BaseInputs[index-1].Input {
			return fmt.Errorf("base_inputs must be in canonical lexical input-name order without duplicates")
		}
	}
	// Reuse snapshot's strict pinned-image/toolchain validation with synthetic
	// local bindings; exact exposed bindings are checked below and at admission.
	probe := snapshot.ValidationAttestationAuthority{CandidateInput: "candidate", Candidate: snapshot.SnapshotRef{ID: 1, Type: "opaque/v1", Digest: attestation.CandidateDigest}, ProfileDigest: attestation.ProfileDigest, ProtectedConfigDigest: attestation.ProtectedConfigDigest, CapabilityImage: attestation.CapabilityImage, CapabilityImageDigest: attestation.CapabilityImageDigest, WorkflowDefinitionID: attestation.WorkflowDefinitionID, WorkflowVersion: attestation.WorkflowVersion, Toolchain: attestation.Toolchain}
	for _, base := range attestation.BaseInputs {
		probe.BaseInputs = append(probe.BaseInputs, snapshot.ValidationAuthorityInput{Input: base.Input, Ref: snapshot.SnapshotRef{ID: 1, Type: base.Type, Digest: base.Digest}})
	}
	if err := probe.Validate(); err != nil {
		return err
	}
	var primary Subject
	for _, subject := range subjects {
		if subject.Role == SubjectRolePrimary {
			primary = subject
			break
		}
	}
	if attestation.CandidateDigest != primary.Digest {
		return fmt.Errorf("candidate_digest must equal the primary subject digest")
	}
	bases := make([]Subject, 0, len(subjects))
	for _, subject := range subjects {
		if subject.Role == SubjectRoleBase {
			bases = append(bases, subject)
		}
	}
	if len(bases) != len(attestation.BaseInputs) {
		return fmt.Errorf("base subjects must exactly equal attestation base_inputs")
	}
	for i, base := range attestation.BaseInputs {
		if bases[i].Input != base.Input || bases[i].Type != base.Type || bases[i].Digest != base.Digest {
			return fmt.Errorf("base subjects must exactly equal attestation base_inputs in order")
		}
	}
	return nil
}

func (log ValidationLog) Validate() error {
	if !strings.HasPrefix(log.Path, "content/logs/") || len(log.Path) == len("content/logs/") || strings.Contains(log.Path, "//") || strings.Contains(log.Path, "..") {
		return fmt.Errorf("validation log path must be beneath content/logs/")
	}
	if err := log.Digest.Validate(); err != nil {
		return err
	}
	if log.decoded && !log.sizePresent {
		return fmt.Errorf("validation log size is required")
	}
	if log.Size < 0 {
		return fmt.Errorf("validation log size must be nonnegative")
	}
	if log.MediaType == "" || log.MediaType != strings.TrimSpace(log.MediaType) {
		return fmt.Errorf("validation log media_type must be canonical")
	}
	if _, _, err := mime.ParseMediaType(log.MediaType); err != nil {
		return fmt.Errorf("validation log media_type is invalid: %w", err)
	}
	return nil
}

func (check ValidationCheck) Flaky() bool {
	if check.Status != "passed" || len(check.Attempts) < 2 {
		return false
	}
	for _, attempt := range check.Attempts[:len(check.Attempts)-1] {
		if attempt.Status == "failed" || attempt.Status == "error" {
			return true
		}
	}
	return false
}

func (check ValidationCheck) Duration() time.Duration {
	var total time.Duration
	for _, attempt := range check.Attempts {
		duration, err := time.ParseDuration(attempt.Duration)
		if err == nil && duration >= 0 {
			total += duration
		}
	}
	return total
}

func DeriveValidationConclusion(checks []ValidationCheck) string {
	if len(checks) == 0 {
		return "incomplete"
	}
	hasFailed := false
	hasSkipped := false
	for _, check := range checks {
		switch check.Status {
		case "error":
			return "error"
		case "failed":
			hasFailed = true
		case "skipped":
			hasSkipped = true
		}
	}
	if hasFailed {
		return "failed"
	}
	if hasSkipped {
		return "incomplete"
	}
	return "passed"
}

type validationValidator struct{}

func (validationValidator) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := admitRecordForSeal[ValidationBody](ctx, root, validationType, declarations)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := validationBody(record); err != nil {
		return snapshot.ValidationResult{}, err
	}
	authority, found := declarations.ValidationAttestationAuthority()
	if !found {
		return snapshot.ValidationResult{}, fmt.Errorf("snapshot contracts: validation/v1 seal admission requires server-declared validation attestation authority")
	}
	if err := validateValidationAttestationAuthority(record.Body.Attestation, record.Subjects, authority); err != nil {
		return snapshot.ValidationResult{}, err
	}
	return snapshot.ValidationResult{}, validateValidationLogs(ctx, root, record.Body)
}

func (validationValidator) RevalidateSealed(ctx context.Context, root *os.Root, _ snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	record, err := readSealedRecord[ValidationBody](ctx, root, validationType)
	if err != nil {
		return snapshot.ValidationResult{}, err
	}
	if err := validationBody(record); err != nil {
		return snapshot.ValidationResult{}, err
	}
	if revision, _ := validationRecordRevision(record); revision >= 3 {
		return snapshot.ValidationResult{}, validateValidationLogs(ctx, root, record.Body)
	}
	return snapshot.ValidationResult{}, nil
}

func validationBody(record Record[ValidationBody]) error {
	revision, err := validationRecordRevision(record)
	if err != nil {
		return err
	}
	if err := validateDeclaredBody(validationType, record.Subjects, validationDeclaredBody(record.Body, revision)); err != nil {
		return err
	}
	if err := record.Body.validateForRevision(record.Subjects, revision); err != nil {
		return fmt.Errorf("snapshot contracts: record.json body: %w", err)
	}
	return nil
}

func validationRecordRevision(record Record[ValidationBody]) (int, error) {
	revision, found := SchemaRevisionFor(validationType, record.Schema)
	if !found {
		return 0, fmt.Errorf("snapshot contracts: validation/v1 schema digest %q has no accepted revision", record.Schema)
	}
	return revision, nil
}
func validationDeclaredBody(body ValidationBody, revision int) any {
	if revision >= 3 {
		declared := validationBodyRevision3Declared{Conclusion: body.Conclusion, Summary: body.Summary, Attestation: body.Attestation, Checks: make([]validationCheckRevision3Declared, len(body.Checks))}
		for i, check := range body.Checks {
			declared.Checks[i] = validationCheckRevision3Declared{ID: check.ID, Kind: check.Kind, Name: check.Name, Status: check.Status, Detail: check.Detail, Attempts: make([]validationAttemptRevision3Declared, len(check.Attempts))}
			for j, attempt := range check.Attempts {
				size := attempt.Log.Size
				declared.Checks[i].Attempts[j] = validationAttemptRevision3Declared{Number: attempt.Number, Status: attempt.Status, Duration: attempt.Duration, Log: validationLogRevision3Declared{Path: attempt.Log.Path, Digest: attempt.Log.Digest, Size: &size, MediaType: attempt.Log.MediaType}, Evidence: attempt.Evidence, Detail: attempt.Detail}
			}
		}
		return declared
	}
	legacy := validationBodyRevision2{Conclusion: body.Conclusion, Summary: body.Summary, Checks: make([]validationCheckRevision2, len(body.Checks))}
	for i, check := range body.Checks {
		legacy.Checks[i] = validationCheckRevision2{ID: check.ID, Kind: check.Kind, Name: check.Name, Status: check.Status, Detail: check.Detail, Attempts: make([]validationAttemptRevision2, len(check.Attempts))}
		for j, attempt := range check.Attempts {
			legacy.Checks[i].Attempts[j] = validationAttemptRevision2{Number: attempt.Number, Status: attempt.Status, Duration: attempt.Duration, Evidence: attempt.Evidence, Detail: attempt.Detail}
		}
	}
	return legacy
}
func validateValidationAttestationAuthority(attestation ValidationAttestation, subjects []Subject, authority snapshot.ValidationAttestationAuthority) error {
	var primary Subject
	for _, subject := range subjects {
		if subject.Role == SubjectRolePrimary {
			primary = subject
			break
		}
	}
	if primary.Input != authority.CandidateInput || primary.Type != authority.Candidate.Type || primary.Digest != authority.Candidate.Digest || attestation.CandidateDigest != authority.Candidate.Digest {
		return fmt.Errorf("snapshot contracts: record.json body: attestation.candidate_digest does not match server-declared validation authority")
	}
	if len(attestation.BaseInputs) != len(authority.BaseInputs) {
		return fmt.Errorf("snapshot contracts: record.json body: attestation.base_inputs does not match server-declared validation authority")
	}
	for i, base := range authority.BaseInputs {
		got := attestation.BaseInputs[i]
		if got.Input != base.Input || got.Type != base.Ref.Type || got.Digest != base.Ref.Digest {
			return fmt.Errorf("snapshot contracts: record.json body: attestation.base_inputs does not match server-declared validation authority")
		}
	}
	if attestation.ProfileDigest != authority.ProfileDigest || attestation.ProtectedConfigDigest != authority.ProtectedConfigDigest || attestation.CapabilityImage != authority.CapabilityImage || attestation.CapabilityImageDigest != authority.CapabilityImageDigest || attestation.WorkflowDefinitionID != authority.WorkflowDefinitionID || attestation.WorkflowVersion != authority.WorkflowVersion || attestation.Toolchain != authority.Toolchain {
		return fmt.Errorf("snapshot contracts: record.json body: attestation does not match server-declared validation authority")
	}
	return nil
}
func validateValidationLogs(ctx context.Context, root *os.Root, body ValidationBody) error {
	seen := map[string]fs.FileInfo{}
	for ci, check := range body.Checks {
		for ai, attempt := range check.Attempts {
			if _, found := seen[attempt.Log.Path]; found {
				return fmt.Errorf("snapshot contracts: record.json body: checks[%d].attempts[%d].log.path is duplicate", ci, ai)
			}
			info, err := root.Lstat(attempt.Log.Path)
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("snapshot contracts: record.json body: checks[%d].attempts[%d].log must be a regular file", ci, ai)
			}
			for path, prior := range seen {
				if os.SameFile(prior, info) {
					return fmt.Errorf("snapshot contracts: record.json body: log.path %q aliases %q", attempt.Log.Path, path)
				}
			}
			if info.Size() != attempt.Log.Size {
				return fmt.Errorf("snapshot contracts: record.json body: log size does not match file")
			}
			if info.Size() > snapshot.DefaultMaxSnapshotContentBytes {
				return fmt.Errorf("snapshot contracts: record.json body: log exceeds content limit")
			}
			file, err := root.Open(attempt.Log.Path)
			if err != nil {
				return err
			}
			opened, statErr := file.Stat()
			if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != info.Size() {
				_ = file.Close()
				return fmt.Errorf("snapshot contracts: record.json body: log changed while opening")
			}
			hash := sha256.New()
			count, copyErr := io.Copy(hash, io.LimitReader(contextReader{ctx: ctx, reader: file}, snapshot.DefaultMaxSnapshotContentBytes+1))
			finalInfo, finalStatErr := file.Stat()
			closeErr := file.Close()
			after, afterErr := root.Lstat(attempt.Log.Path)
			if copyErr != nil || finalStatErr != nil || closeErr != nil || afterErr != nil || count != attempt.Log.Size || !finalInfo.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(info, finalInfo) || !os.SameFile(info, after) || finalInfo.Size() != info.Size() || after.Size() != info.Size() {
				return fmt.Errorf("snapshot contracts: record.json body: log bytes changed or size does not match")
			}
			actual := snapshot.Digest("sha256:" + hex.EncodeToString(hash.Sum(nil)))
			if actual != attempt.Log.Digest {
				return fmt.Errorf("snapshot contracts: record.json body: log digest does not match file")
			}
			seen[attempt.Log.Path] = info
		}
	}
	return nil
}
