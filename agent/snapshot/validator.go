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

// Validator validates one already-canonicalized snapshot tree. The supplied
// os.Root anchors every document read beneath that tree; validators must not
// close it.
type Validator interface {
	Validate(context.Context, *os.Root, ValidationContext) (ValidationResult, error)
}

// ValidatorRegistry resolves validators by an exact canonical type reference.
type ValidatorRegistry interface {
	Lookup(TypeRef) (Validator, error)
}
