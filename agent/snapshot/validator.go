package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
// validator. It deliberately provides no storage or network client.
type ValidationContext struct {
	inputs map[string]SnapshotRef
	opener InputOpener
}

func NewValidationContext(inputs map[string]SnapshotRef, opener InputOpener) (ValidationContext, error) {
	cloned := cloneSnapshotRefs(inputs)
	for name, ref := range cloned {
		if strings.TrimSpace(name) == "" {
			return ValidationContext{}, fmt.Errorf("snapshot: validation input name is required")
		}
		if err := ref.Validate(); err != nil {
			return ValidationContext{}, fmt.Errorf("snapshot: validation input %q: %w", name, err)
		}
	}
	return ValidationContext{inputs: cloned, opener: opener}, nil
}

func (c ValidationContext) Inputs() map[string]SnapshotRef {
	return cloneSnapshotRefs(c.inputs)
}

func (c ValidationContext) Input(name string) (SnapshotRef, bool) {
	ref, found := c.inputs[name]
	return ref, found
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
