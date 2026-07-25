package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
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

// ValidationContext is an immutable view of the input bindings available to a
// validator, together with the server-side port declarations a validator is
// allowed to treat as authority. It deliberately provides no storage or network
// client.
type ValidationContext struct {
	inputs     map[string]SnapshotRef
	candidates map[string]struct{}
	exposures  map[string]InputExposure
	opener     InputOpener
}

// ValidationContextOption carries one server-declared fact about the exposure
// the validator is judging. Options exist so the declarations stay separate
// from, and cannot be confused with, anything the validated bytes claim.
type ValidationContextOption func(*validationContextDeclarations)

type validationContextDeclarations struct {
	candidatePorts []string
	exposures      map[string]InputExposure
}

// WithCandidatePorts declares which exposed input ports are candidate ports:
// the alternatives a selecting step may choose between. The platform derives
// this from the step's compiled port declarations
// (atc.SnapshotInputConfig.Candidate), so candidacy is never inferable from a
// producer's own record. Callers pass only the ports that are actually exposed
// for this invocation — an unbound optional candidate is not something the step
// could have selected, so it must not be declared.
func WithCandidatePorts(names ...string) ValidationContextOption {
	declared := append([]string(nil), names...)
	return func(declarations *validationContextDeclarations) {
		declarations.candidatePorts = append(declarations.candidatePorts, declared...)
	}
}

// WithInputExposures declares the exposure lineage the platform captured when
// it mounted the exposed inputs: the materialization mode plus, for a static
// selector, the exact exposed path set with per-path digests. It is what makes
// "did this step actually look at the diff?" answerable instead of assumed.
//
// The declaration is server-derived occurrence data. It is never part of the
// sealed bytes and never part of ValidationResult, so a validator may read it
// as authority but cannot propagate it into content identity.
func WithInputExposures(exposures map[string]InputExposure) ValidationContextOption {
	declared := cloneInputExposures(exposures)
	return func(declarations *validationContextDeclarations) {
		if declarations.exposures == nil {
			declarations.exposures = make(map[string]InputExposure, len(declared))
		}
		for port, exposure := range declared {
			declarations.exposures[port] = exposure
		}
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
	candidates := make(map[string]struct{}, len(declarations.candidatePorts))
	for _, name := range declarations.candidatePorts {
		if _, exposed := cloned[name]; !exposed {
			return ValidationContext{}, fmt.Errorf("snapshot: candidate input port %q is not an exposed input", name)
		}
		if _, duplicate := candidates[name]; duplicate {
			return ValidationContext{}, fmt.Errorf("snapshot: candidate input port %q is declared more than once", name)
		}
		candidates[name] = struct{}{}
	}
	if err := validateDeclaredExposures(declarations.exposures, cloned); err != nil {
		return ValidationContext{}, err
	}
	return ValidationContext{
		inputs: cloned, candidates: candidates,
		exposures: resolveExposures(declarations.exposures, cloned),
		opener:    opener,
	}, nil
}

func (c ValidationContext) Inputs() map[string]SnapshotRef {
	return cloneSnapshotRefs(c.inputs)
}

func (c ValidationContext) Input(name string) (SnapshotRef, bool) {
	ref, found := c.inputs[name]
	return ref, found
}

// CandidatePorts returns the declared candidate input ports, sorted, as a copy.
//
// This is a SEAL-TIME authority. It is populated from the step's compiled port
// declarations and is empty in every read-time context, because the declarations
// stop existing when the step ends. A read-time validator that consults it will
// therefore reject every stored record — see the selection contract, which takes
// candidacy from the sealed subject roles on read instead.
func (c ValidationContext) CandidatePorts() []string {
	names := make([]string, 0, len(c.candidates))
	for name := range c.candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Exposure returns how much of one exposed input this step was shown. The
// second result reports whether name is an exposed input at all; every exposed
// input has an exposure, defaulting to the whole tree.
func (c ValidationContext) Exposure(name string) (InputExposure, bool) {
	exposure, found := c.exposures[name]
	if !found {
		return InputExposure{}, false
	}
	return exposure.Clone(), true
}

// Exposures returns the exposure lineage for every exposed input, as a copy.
func (c ValidationContext) Exposures() map[string]InputExposure {
	return cloneInputExposures(c.exposures)
}

// IsCandidatePort reports whether name is an exactly-matching declared candidate
// input port. Like CandidatePorts, it is a seal-time authority only.
func (c ValidationContext) IsCandidatePort(name string) bool {
	_, found := c.candidates[name]
	return found
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
