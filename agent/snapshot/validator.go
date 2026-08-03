package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	containername "github.com/google/go-containerregistry/pkg/name"
)

var canonicalSnapshotPortName = regexp.MustCompile(`^[\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d][\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d\-_.]*$`)
var numericSnapshotPortName = regexp.MustCompile(`^\d+$`)

// ValidationFailureReason is a CLOSED set. A reason is a compile-time constant
// with a fixed public message: nothing derived from caller-submitted bytes ever
// becomes a reason, which is what makes it safe to publish one verbatim.
//
// The three families below are not interchangeable, and the HTTP layer maps them
// to different statuses:
//
//   - Repository* is one snapshot type's git-level judgment.
//   - Archive* is a rejection by the canonicalizer, BEFORE any type-specific
//     validator runs. These reach a client as 400 invalid_archive.
//   - Record*/Snapshot* are contract-body judgments, and reach a client as 422
//     validation_failed.
type ValidationFailureReason string

const (
	RepositoryMetadataMissing         ValidationFailureReason = "repository_metadata_missing"
	RepositoryMetadataUnsafe          ValidationFailureReason = "repository_metadata_unsafe"
	RepositoryHistoryIncomplete       ValidationFailureReason = "repository_history_incomplete"
	RepositoryObjectFormatUnsupported ValidationFailureReason = "repository_object_format_unsupported"
	RepositoryGitlinksUnsupported     ValidationFailureReason = "repository_gitlinks_unsupported"
	RepositoryDirty                   ValidationFailureReason = "repository_dirty"
	RepositoryInvalid                 ValidationFailureReason = "repository_invalid"
)

// Archive rejection categories. Each one is a family of rules the canonicalizer
// actually enforces in archive.go; none of them is a rule that does not exist.
const (
	// ArchivePathNotCanonical covers validateArchivePath's shape rules: an empty
	// name, a backslash, an absolute path, a drive-like segment, a trailing
	// separator, and any empty, "." or ".." segment. A "./" entry and a "dir/"
	// entry both land here.
	ArchivePathNotCanonical ValidationFailureReason = "archive_path_not_canonical"

	// ArchivePathTooLong is validateArchivePath's MaxSnapshotPathBytes bound. It
	// is separated from the shape rules because a too-long path is the one
	// non-canonical path whose ONLY defect is length, and the caller's fix is
	// different.
	ArchivePathTooLong ValidationFailureReason = "archive_path_too_long"

	// ArchivePathDuplicate is one canonical path appearing twice, and the related
	// planMaterialization rejection where a path conflicts with something already
	// materialized at that name.
	ArchivePathDuplicate ValidationFailureReason = "archive_path_duplicate"

	// ArchivePathParentInvalid is an entry whose parent is a symlink or is not a
	// directory, from either planMaterialization or validateHostParents.
	ArchivePathParentInvalid ValidationFailureReason = "archive_path_parent_invalid"

	// ArchivePathCollides is hostEquivalentCollision: two DISTINCT POSIX names
	// that the extraction filesystem aliases onto one another. It is deliberately
	// not ArchivePathDuplicate — the archive is self-consistent and the host is
	// what cannot represent it.
	ArchivePathCollides ValidationFailureReason = "archive_path_collides"

	// ArchiveEntryTypeUnsupported is a typeflag outside {regular, directory,
	// symlink}: hard links, devices, FIFOs, sockets.
	ArchiveEntryTypeUnsupported ValidationFailureReason = "archive_entry_type_unsupported"

	// ArchiveEntryMetadataUnsupported is validateHeader's metadata rules: xattrs,
	// unsupported or inconsistent PAX records, and setuid/setgid bits.
	ArchiveEntryMetadataUnsupported ValidationFailureReason = "archive_entry_metadata_unsupported"

	// ArchiveEntrySizeInvalid is a negative regular size, a non-regular entry
	// that declares content, or a regular entry whose content is truncated
	// relative to its header.
	ArchiveEntrySizeInvalid ValidationFailureReason = "archive_entry_size_invalid"

	// ArchiveSymlinkTargetInvalid is cleanSymlinkTarget's shape rules: an empty,
	// over-long, backslashed, absolute, or drive-like target.
	ArchiveSymlinkTargetInvalid ValidationFailureReason = "archive_symlink_target_invalid"

	// ArchiveSymlinkEscapesRoot is the containment rule: a target that resolves
	// outside the archive root.
	ArchiveSymlinkEscapesRoot ValidationFailureReason = "archive_symlink_escapes_root"

	// ArchiveStreamUnreadable is the tar stream itself failing to parse.
	ArchiveStreamUnreadable ValidationFailureReason = "archive_stream_unreadable"
)

// Record and tree contract categories. These are cross-contract on purpose: the
// generic core validator in agent/snapshot/contracts enforces the declared half
// of every record type in one place, so a reason attached there covers review,
// diagnosis, consultation, validation, measurements, repository-change,
// pull-request, pull-request-response, and publish-impact at once.
const (
	RecordDocumentMissing        ValidationFailureReason = "record_document_missing"
	RecordDocumentMalformed      ValidationFailureReason = "record_document_malformed"
	RecordEnvelopeInvalid        ValidationFailureReason = "record_envelope_invalid"
	RecordSubjectsInvalid        ValidationFailureReason = "record_subjects_invalid"
	RecordFieldMissing           ValidationFailureReason = "record_field_missing"
	RecordFieldForbidden         ValidationFailureReason = "record_field_forbidden"
	RecordFieldTypeInvalid       ValidationFailureReason = "record_field_type_invalid"
	RecordFieldValueNotAllowed   ValidationFailureReason = "record_field_value_not_allowed"
	RecordFieldOutOfRange        ValidationFailureReason = "record_field_out_of_range"
	RecordIdentifierInvalid      ValidationFailureReason = "record_identifier_invalid"
	RecordEntityIDDuplicate      ValidationFailureReason = "record_entity_id_duplicate"
	RecordEntityIDsUnsorted      ValidationFailureReason = "record_entity_ids_unsorted"
	RecordAnchorInvalid          ValidationFailureReason = "record_anchor_invalid"
	RecordConclusionInconsistent ValidationFailureReason = "record_conclusion_inconsistent"
	RecordBlockingInconsistent   ValidationFailureReason = "record_blocking_inconsistent"
	RecordEvidenceMissing        ValidationFailureReason = "record_evidence_missing"
	RecordRankInvalid            ValidationFailureReason = "record_rank_invalid"
	RecordReferenceUnknown       ValidationFailureReason = "record_reference_unknown"
	SnapshotTreeInvalid          ValidationFailureReason = "snapshot_tree_invalid"
)

const (
	// MaxPublicEntryBytes bounds the ONE caller-derived string this package
	// republishes to a client: the name of the archive entry a rejection is
	// about.
	//
	// It is deliberately far below MaxSnapshotPathBytes. A 4KiB path in an error
	// envelope is a log-flooding and rendering hazard rather than extra help;
	// what a caller needs is enough of the name to find the file. 256 bytes is
	// the bound this package already applies to the other caller-derived string
	// it retains (maxValidationToolchainBytes), and holding the two together
	// means there is one number to reason about instead of two.
	MaxPublicEntryBytes = 256

	// PublicEntryTruncationMarker terminates a truncated entry name. Truncation
	// is never silent: a prefix of a path and a path are different claims about
	// what the archive contains, and a reader has to be able to tell which one
	// they were handed. U+2026 is one rune and is far rarer in real paths than
	// three ASCII dots.
	PublicEntryTruncationMarker = "…"
)

// PublicValidationFailure carries one closed, safe-to-publish validation
// category while preserving its detailed cause exclusively for structured
// server logging.
//
// It may additionally carry the archive entry the rejection is about. That is
// the single deliberate widening of the envelope: the reason stays a
// compile-time constant, and only the entry is caller-derived — bounded to
// MaxPublicEntryBytes and sanitized by sanitizePublicEntry before it is stored,
// never at read time, so no accessor can hand out an unsanitized value.
type PublicValidationFailure struct {
	reason  ValidationFailureReason
	message string
	entry   string
	cause   error
}

func NewPublicValidationFailure(reason ValidationFailureReason, cause error) error {
	message, found := publicValidationMessage(reason)
	if !found || cause == nil {
		if cause == nil {
			return fmt.Errorf("snapshot: invalid public validation failure")
		}
		return fmt.Errorf("snapshot: invalid public validation failure: %w", cause)
	}
	return &PublicValidationFailure{reason: reason, message: message, cause: cause}
}

// NewPublicValidationFailureForEntry is NewPublicValidationFailure plus the one
// detail a closed reason cannot express: WHICH entry was rejected.
//
// It routes through NewPublicValidationFailure rather than constructing the
// struct, so the fail-safe for an unregistered reason or a nil cause has exactly
// one implementation and cannot be bypassed by using the newer door.
func NewPublicValidationFailureForEntry(reason ValidationFailureReason, entry string, cause error) error {
	failure := NewPublicValidationFailure(reason, cause)
	public, ok := failure.(*PublicValidationFailure)
	if !ok {
		return failure
	}
	public.entry = sanitizePublicEntry(entry)
	return public
}

func (failure *PublicValidationFailure) Error() string {
	return failure.message + ": " + failure.cause.Error()
}

func (failure *PublicValidationFailure) Unwrap() error { return failure.cause }

func (failure *PublicValidationFailure) Reason() ValidationFailureReason { return failure.reason }

func (failure *PublicValidationFailure) PublicMessage() string { return failure.message }

// Entry is the sanitized, bounded name of the archive entry this failure is
// about, or "" when the failure is not about one entry.
func (failure *PublicValidationFailure) Entry() string { return failure.entry }

// sanitizePublicEntry makes a caller-submitted path safe to place in a JSON
// error body and in a terminal that renders it.
//
// Every byte that is not a printable rune is REPLACED with U+FFFD rather than
// dropped. Dropping would let two different archives produce the same published
// name, and would let a name containing an escape sequence shrink into a
// different, legitimate-looking path; replacing keeps the position of the damage
// visible and keeps the result the same shape as the original. Three classes go:
//
//   - invalid UTF-8, one replacement per invalid byte, so the result is always
//     valid UTF-8 and can be marshalled without encoding/json substituting its
//     own replacements;
//   - Cc and Cf, which covers NUL, the ESC that begins an ANSI sequence, and the
//     bidi overrides (U+202E and friends) that reorder a rendered path;
//   - Cs and Co, surrogates and private use, which no real snapshot path needs
//     and which render unpredictably.
//
// The result is at most MaxPublicEntryBytes bytes and is cut on a rune boundary,
// so truncation can never emit a partial rune.
func sanitizePublicEntry(entry string) string {
	budget := MaxPublicEntryBytes - len(PublicEntryTruncationMarker)
	var sanitized strings.Builder
	for index := 0; index < len(entry); {
		character, width := utf8.DecodeRuneInString(entry[index:])
		if character == utf8.RuneError && width <= 1 {
			width = 1
		} else if unicode.In(character, unicode.C) {
			character = utf8.RuneError
		}
		if sanitized.Len()+utf8.RuneLen(character) > budget {
			return sanitized.String() + PublicEntryTruncationMarker
		}
		sanitized.WriteRune(character)
		index += width
	}
	return sanitized.String()
}

func publicValidationMessage(reason ValidationFailureReason) (string, bool) {
	switch reason {
	case RepositoryMetadataMissing:
		return "repository metadata is incomplete", true
	case RepositoryMetadataUnsafe:
		return "repository metadata contains an unsupported or unsafe setting", true
	case RepositoryHistoryIncomplete:
		return "repository history is shallow or incomplete", true
	case RepositoryObjectFormatUnsupported:
		return "repository object format is unsupported", true
	case RepositoryGitlinksUnsupported:
		return "repositories containing submodule gitlinks are unsupported", true
	case RepositoryDirty:
		return "repository work tree and index must be clean", true
	case RepositoryInvalid:
		return "repository object graph is invalid or incomplete", true

	case ArchivePathNotCanonical:
		return "snapshot archive entry path is not canonical", true
	case ArchivePathTooLong:
		return "snapshot archive entry path is too long", true
	case ArchivePathDuplicate:
		return "snapshot archive declares one entry path more than once", true
	case ArchivePathParentInvalid:
		return "snapshot archive entry has a symlinked or non-directory parent", true
	case ArchivePathCollides:
		return "snapshot archive entry paths collide on the extraction filesystem", true
	case ArchiveEntryTypeUnsupported:
		return "snapshot archive entry type is unsupported", true
	case ArchiveEntryMetadataUnsupported:
		return "snapshot archive entry carries unsupported or unsafe metadata", true
	case ArchiveEntrySizeInvalid:
		return "snapshot archive entry declares an invalid content size", true
	case ArchiveSymlinkTargetInvalid:
		return "snapshot archive symlink target is not canonical", true
	case ArchiveSymlinkEscapesRoot:
		return "snapshot archive symlink target escapes the archive root", true
	case ArchiveStreamUnreadable:
		return "snapshot archive stream is not a readable tar", true

	case RecordDocumentMissing:
		return "a required snapshot document is missing or is not a regular file", true
	case RecordDocumentMalformed:
		return "a snapshot document is not strict, well-formed JSON", true
	case RecordEnvelopeInvalid:
		return "record envelope version, type, or schema digest is not accepted", true
	case RecordSubjectsInvalid:
		return "record subject set does not satisfy its contract", true
	case RecordFieldMissing:
		return "a required record field is missing or blank", true
	case RecordFieldForbidden:
		return "a record field is present where its contract forbids it", true
	case RecordFieldTypeInvalid:
		return "a record field does not have the shape its contract declares", true
	case RecordFieldValueNotAllowed:
		return "a record field value is outside the closed set its contract allows", true
	case RecordFieldOutOfRange:
		return "a record field value is outside the range its contract allows", true
	case RecordIdentifierInvalid:
		return "a record identifier does not match the identifier grammar", true
	case RecordEntityIDDuplicate:
		return "a record entity set contains a duplicate id", true
	case RecordEntityIDsUnsorted:
		return "a record entity set is not lexicographically sorted by id", true
	case RecordAnchorInvalid:
		return "a record anchor does not resolve to a declared subject and locator", true
	case RecordConclusionInconsistent:
		return "the record conclusion contradicts the rest of the record", true
	case RecordBlockingInconsistent:
		return "a finding severity and its blocking flag contradict each other", true
	case RecordEvidenceMissing:
		return "the record omits evidence its contract requires", true
	case RecordRankInvalid:
		return "record ranks are not unique and contiguous from one", true
	case RecordReferenceUnknown:
		return "the record references an entity it does not contain", true
	case SnapshotTreeInvalid:
		return "the snapshot tree does not have the shape its type requires", true

	default:
		return "", false
	}
}

// ValidationResult contains metadata derived only from the validated snapshot
// bytes. Callers persist it on the deduplicated snapshot value, so validators
// must not include invocation or source-adapter data.
type ValidationResult struct {
	IntrinsicMetadata json.RawMessage
}

func (r ValidationResult) Validate() error {
	if err := validateRawMessage(r.IntrinsicMetadata); err != nil {
		return fmt.Errorf("snapshot: validation intrinsic metadata: %w", err)
	}
	return nil
}

func (r ValidationResult) Clone() ValidationResult {
	r.IntrinsicMetadata = cloneRaw(r.IntrinsicMetadata)
	return r
}

// InputOpener opens the immutable canonical archive for one exact named input.
// The name and validated reference are supplied together so implementations
// can authorize and audit the same binding that the validator requested.
type InputOpener func(context.Context, string, SnapshotRef) (io.ReadCloser, error)

// ValidationAuthorityInput binds an attested immutable base to the exact
// exposed snapshot reference. It deliberately retains the port name as well as
// type and digest: the port is part of the frozen workflow provenance.
type ValidationAuthorityInput struct {
	Input string
	Ref   SnapshotRef
}

// ValidationAttestationAuthority is process-only, per-output authority for a
// validation/v1 rev3 record. It is compiled by the server; no archive, source
// metadata, or validation result can manufacture it.
type ValidationAttestationAuthority struct {
	CandidateInput        string
	Candidate             SnapshotRef
	BaseInputs            []ValidationAuthorityInput
	ProfileDigest         Digest
	ProtectedConfigDigest Digest
	CapabilityImage       string
	CapabilityImageDigest Digest
	WorkflowDefinitionID  int
	WorkflowVersion       int
	Toolchain             string
}

const maxValidationToolchainBytes = 256

func (authority ValidationAttestationAuthority) Validate() error {
	if err := validateValidationInputName("candidate input", authority.CandidateInput); err != nil {
		return err
	}
	if err := authority.Candidate.Validate(); err != nil {
		return fmt.Errorf("candidate: %w", err)
	}
	previous := ""
	for index, base := range authority.BaseInputs {
		if err := validateValidationInputName("base input", base.Input); err != nil {
			return fmt.Errorf("base_inputs[%d]: %w", index, err)
		}
		if base.Input == authority.CandidateInput {
			return fmt.Errorf("base_inputs[%d] must not name the candidate input", index)
		}
		if previous != "" && base.Input <= previous {
			return fmt.Errorf("base_inputs must be in canonical lexical input-name order without duplicates")
		}
		previous = base.Input
		if err := base.Ref.Validate(); err != nil {
			return fmt.Errorf("base_inputs[%d]: %w", index, err)
		}
	}
	for _, digest := range []struct {
		name  string
		value Digest
	}{
		{"profile_digest", authority.ProfileDigest},
		{"protected_config_digest", authority.ProtectedConfigDigest},
		{"capability_image_digest", authority.CapabilityImageDigest},
	} {
		if err := digest.value.Validate(); err != nil {
			return fmt.Errorf("%s: %w", digest.name, err)
		}
	}
	if err := validateValidationCapabilityImage(authority.CapabilityImage, authority.CapabilityImageDigest); err != nil {
		return err
	}
	if authority.WorkflowDefinitionID <= 0 || authority.WorkflowVersion <= 0 {
		return fmt.Errorf("workflow definition ID and workflow version must be positive")
	}
	return validateValidationToolchain(authority.Toolchain)
}

func validateValidationInputName(label, input string) error {
	if input == "" || input != strings.TrimSpace(input) ||
		!canonicalSnapshotPortName.MatchString(input) || numericSnapshotPortName.MatchString(input) {
		return fmt.Errorf("%s must use the canonical snapshot port-name grammar", label)
	}
	return nil
}

func validateValidationCapabilityImage(image string, expected Digest) error {
	if image == "" || image != strings.TrimSpace(image) {
		return fmt.Errorf("capability_image must be a canonical OCI digest reference")
	}
	parsed, err := containername.NewDigest(image, containername.StrictValidation)
	if err != nil {
		return fmt.Errorf("capability_image is not a valid OCI digest reference: %w", err)
	}
	actual, err := ParseDigest(parsed.DigestStr())
	if err != nil {
		return fmt.Errorf("capability_image digest: %w", err)
	}
	if actual != expected {
		return fmt.Errorf("capability_image_digest must equal the capability_image pin")
	}
	return nil
}

func validateValidationToolchain(toolchain string) error {
	if toolchain == "" || toolchain != strings.TrimSpace(toolchain) || len(toolchain) > maxValidationToolchainBytes || !utf8.ValidString(toolchain) {
		return fmt.Errorf("toolchain must be canonical non-empty UTF-8 text no longer than %d bytes", maxValidationToolchainBytes)
	}
	for _, character := range toolchain {
		if unicode.IsControl(character) {
			return fmt.Errorf("toolchain must not contain control characters")
		}
	}
	return nil
}

// ValidationContext is an immutable view of the input bindings available to a
// validator. It deliberately provides no storage or network client.
type ValidationContext struct {
	inputs                            map[string]SnapshotRef
	opener                            InputOpener
	validationAttestationAuthority    ValidationAttestationAuthority
	hasValidationAttestationAuthority bool
}

type ValidationContextOption func(*validationContextDeclarations)

type validationContextDeclarations struct {
	validationAttestationAuthorities []ValidationAttestationAuthority
}

func WithValidationAttestationAuthority(authority ValidationAttestationAuthority) ValidationContextOption {
	cloned := cloneValidationAttestationAuthority(authority)
	return func(declarations *validationContextDeclarations) {
		declarations.validationAttestationAuthorities = append(declarations.validationAttestationAuthorities, cloneValidationAttestationAuthority(cloned))
	}
}

func NewValidationContext(inputs map[string]SnapshotRef, opener InputOpener, options ...ValidationContextOption) (ValidationContext, error) {
	cloned := cloneSnapshotRefs(inputs)
	for name, ref := range cloned {
		if strings.TrimSpace(name) == "" {
			return ValidationContext{}, fmt.Errorf("snapshot: validation input name is required")
		}
		if err := ref.Validate(); err != nil {
			return ValidationContext{}, fmt.Errorf("snapshot: validation input %q: %w", name, err)
		}
	}
	var declarations validationContextDeclarations
	for _, option := range options {
		if option == nil {
			return ValidationContext{}, fmt.Errorf("snapshot: validation context option is required")
		}
		option(&declarations)
	}
	if len(declarations.validationAttestationAuthorities) > 1 {
		return ValidationContext{}, fmt.Errorf("snapshot: validation attestation authority is declared more than once")
	}
	var authority ValidationAttestationAuthority
	hasAuthority := len(declarations.validationAttestationAuthorities) == 1
	if hasAuthority {
		authority = cloneValidationAttestationAuthority(declarations.validationAttestationAuthorities[0])
		if err := authority.Validate(); err != nil {
			return ValidationContext{}, fmt.Errorf("snapshot: validation attestation authority: %w", err)
		}
		if candidate, found := cloned[authority.CandidateInput]; !found || candidate != authority.Candidate {
			return ValidationContext{}, fmt.Errorf("snapshot: validation attestation candidate is not an exact exposed input")
		}
		for _, base := range authority.BaseInputs {
			if exposed, found := cloned[base.Input]; !found || exposed != base.Ref {
				return ValidationContext{}, fmt.Errorf("snapshot: validation attestation base input %q is not an exact exposed input", base.Input)
			}
		}
	}
	return ValidationContext{inputs: cloned, opener: opener, validationAttestationAuthority: authority, hasValidationAttestationAuthority: hasAuthority}, nil
}

func (c ValidationContext) Inputs() map[string]SnapshotRef {
	return cloneSnapshotRefs(c.inputs)
}

func (c ValidationContext) Input(name string) (SnapshotRef, bool) {
	ref, found := c.inputs[name]
	return ref, found
}

func (c ValidationContext) ValidationAttestationAuthority() (ValidationAttestationAuthority, bool) {
	if !c.hasValidationAttestationAuthority {
		return ValidationAttestationAuthority{}, false
	}
	return cloneValidationAttestationAuthority(c.validationAttestationAuthority), true
}

func (c ValidationContext) OpenInput(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ref, found := c.inputs[name]
	if !found {
		return nil, fmt.Errorf("snapshot: validation input %q is not declared", name)
	}
	if c.opener == nil {
		return nil, fmt.Errorf("snapshot: validation input %q cannot be opened", name)
	}
	reader, err := c.opener(ctx, name, ref)
	if err != nil {
		return nil, fmt.Errorf("snapshot: open validation input %q: %w", name, err)
	}
	if reader == nil {
		return nil, fmt.Errorf("snapshot: open validation input %q returned no content", name)
	}
	return reader, nil
}

func cloneValidationAttestationAuthority(authority ValidationAttestationAuthority) ValidationAttestationAuthority {
	authority.BaseInputs = append([]ValidationAuthorityInput(nil), authority.BaseInputs...)
	return authority
}

// ValidationAuthorityInputNames returns a copy in canonical authority order.
func (authority ValidationAttestationAuthority) ValidationAuthorityInputNames() []string {
	names := make([]string, 0, len(authority.BaseInputs))
	for _, base := range authority.BaseInputs {
		names = append(names, base.Input)
	}
	sort.Strings(names)
	return names
}

// Validator validates one already-canonicalized snapshot tree at one of two
// gates. The supplied os.Root anchors every document read beneath that tree;
// validators must not close it.
//
// The two gates are deliberately two methods rather than one method plus a
// mode argument. They judge different things:
//
//   - AdmitForSeal judges a candidate the producing step just wrote, before the
//     platform has certified anything about it. The producer has authority over
//     nothing, so every fact the validator relies on must come from a
//     server-side declaration, and a record envelope must pin the CURRENT
//     contract identity for its type — accepting a superseded one would let a
//     producer choose which contract identity its own output advertises.
//
//   - RevalidateSealed judges bytes the platform already sealed and certified.
//     It must accept ANY accepted contract identity for the type, because a
//     descriptor bump is a versioning event and not retroactive corruption of
//     the stored corpus, and it must rely on the sealed bytes rather than on
//     live workflow declarations that no longer exist when a reader loads a
//     record.
//
// A caller therefore has to say which situation it is in, and cannot drift into
// the wrong one by passing a flag wrongly. A validator that cannot re-validate
// a stored record is a defect: it makes the corpus unreadable.
type Validator interface {
	AdmitForSeal(context.Context, *os.Root, ValidationContext) (ValidationResult, error)
	RevalidateSealed(context.Context, *os.Root, ValidationContext) (ValidationResult, error)
}

// ValidatorRegistry resolves validators by an exact canonical type reference.
type ValidatorRegistry interface {
	Lookup(TypeRef) (Validator, error)
}
