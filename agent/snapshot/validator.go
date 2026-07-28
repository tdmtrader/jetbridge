package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	containername "github.com/google/go-containerregistry/pkg/name"
)

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
	if input == "" || input != strings.TrimSpace(input) {
		return fmt.Errorf("%s must be canonical and non-empty", label)
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
