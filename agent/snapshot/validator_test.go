package snapshot_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

type validationStub struct{}

func (validationStub) AdmitForSeal(context.Context, *os.Root, snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	return snapshot.ValidationResult{IntrinsicMetadata: json.RawMessage(`{"kind":"stub"}`)}, nil
}

func (validationStub) RevalidateSealed(context.Context, *os.Root, snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	return snapshot.ValidationResult{IntrinsicMetadata: json.RawMessage(`{"kind":"stub"}`)}, nil
}

type validationRegistryStub struct{ validator snapshot.Validator }

func (r validationRegistryStub) Lookup(snapshot.TypeRef) (snapshot.Validator, error) {
	return r.validator, nil
}

var _ snapshot.Validator = validationStub{}
var _ snapshot.ValidatorRegistry = validationRegistryStub{}

func TestValidationContextClonesAndOpensExactNamedInputs(t *testing.T) {
	ref := validValidationSnapshotRef(t, "repository/v1", 1)
	inputs := map[string]snapshot.SnapshotRef{"base": ref}
	var openedName string
	var openedRef snapshot.SnapshotRef

	validationContext, err := snapshot.NewValidationContext(
		inputs,
		func(_ context.Context, name string, got snapshot.SnapshotRef) (io.ReadCloser, error) {
			openedName = name
			openedRef = got
			return io.NopCloser(strings.NewReader("base archive")), nil
		},
	)
	if err != nil {
		t.Fatalf("NewValidationContext() error = %v", err)
	}

	inputs["base"] = validValidationSnapshotRef(t, "opaque/v1", 2)
	got, ok := validationContext.Input("base")
	if !ok || got != ref {
		t.Fatalf("Input(base) = (%+v, %t), want immutable ref %+v", got, ok, ref)
	}
	clonedInputs := validationContext.Inputs()
	clonedInputs["base"] = inputs["base"]
	got, _ = validationContext.Input("base")
	if got != ref {
		t.Fatalf("Input(base) after mutating Inputs() result = %+v, want %+v", got, ref)
	}

	reader, err := validationContext.OpenInput(context.Background(), "base")
	if err != nil {
		t.Fatalf("OpenInput(base) error = %v", err)
	}
	defer reader.Close()
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read opened input: %v", err)
	}
	if string(contents) != "base archive" || openedName != "base" || openedRef != ref {
		t.Fatalf("opener received (%q, %+v) and returned %q, want exact name/ref/content", openedName, openedRef, contents)
	}

	for _, name := range []string{"Base", "base ", " base"} {
		if _, err := validationContext.OpenInput(context.Background(), name); err == nil {
			t.Fatalf("OpenInput(%q) succeeded, want exact-name lookup failure", name)
		}
	}
}

func TestValidationContextRejectsInvalidInputsAndMissingOpener(t *testing.T) {
	validRef := validValidationSnapshotRef(t, "repository/v1", 1)
	for name, inputs := range map[string]map[string]snapshot.SnapshotRef{
		"blank input name": {" ": validRef},
		"invalid ref":      {"base": {}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := snapshot.NewValidationContext(inputs, nil); err == nil {
				t.Fatal("NewValidationContext() succeeded, want validation error")
			}
		})
	}

	validationContext, err := snapshot.NewValidationContext(map[string]snapshot.SnapshotRef{"base": validRef}, nil)
	if err != nil {
		t.Fatalf("NewValidationContext() error = %v", err)
	}
	if _, err := validationContext.OpenInput(context.Background(), "base"); err == nil {
		t.Fatal("OpenInput(base) succeeded without an opener")
	}
}

func TestValidationContextCarriesServerDeclaredCandidatePorts(t *testing.T) {
	base := validValidationSnapshotRef(t, "repository/v1", 1)
	left := validValidationSnapshotRef(t, "repository-change/v1", 2)
	right := validValidationSnapshotRef(t, "repository-change/v1", 3)
	inputs := map[string]snapshot.SnapshotRef{"base": base, "left": left, "right": right}

	validationContext, err := snapshot.NewValidationContext(
		inputs, nil, snapshot.WithCandidatePorts("right", "left"),
	)
	if err != nil {
		t.Fatalf("NewValidationContext() error = %v", err)
	}
	ports := validationContext.CandidatePorts()
	if !reflect.DeepEqual(ports, []string{"left", "right"}) {
		t.Fatalf("CandidatePorts() = %q, want sorted [left right]", ports)
	}
	ports[0] = "mutated"
	if got := validationContext.CandidatePorts(); !reflect.DeepEqual(got, []string{"left", "right"}) {
		t.Fatalf("CandidatePorts() after mutating the result = %q, want an immutable copy", got)
	}
	for name, want := range map[string]bool{"left": true, "right": true, "base": false, "Left": false, "left ": false} {
		if got := validationContext.IsCandidatePort(name); got != want {
			t.Fatalf("IsCandidatePort(%q) = %t, want %t", name, got, want)
		}
	}

	empty, err := snapshot.NewValidationContext(inputs, nil)
	if err != nil {
		t.Fatalf("NewValidationContext() error = %v", err)
	}
	if got := empty.CandidatePorts(); len(got) != 0 {
		t.Fatalf("CandidatePorts() without a declaration = %q, want none", got)
	}
}

func TestValidationContextRejectsCandidatePortsThatAreNotExposedInputs(t *testing.T) {
	inputs := map[string]snapshot.SnapshotRef{"left": validValidationSnapshotRef(t, "repository-change/v1", 1)}
	for name, option := range map[string]snapshot.ValidationContextOption{
		"unexposed port": snapshot.WithCandidatePorts("left", "right"),
		"duplicate port": snapshot.WithCandidatePorts("left", "left"),
		"blank port":     snapshot.WithCandidatePorts(" "),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := snapshot.NewValidationContext(inputs, nil, option); err == nil {
				t.Fatal("NewValidationContext() succeeded, want a candidate port declaration error")
			}
		})
	}
}

func validValidationSnapshotRef(t *testing.T, rawType string, id int64) snapshot.SnapshotRef {
	t.Helper()
	ref, err := snapshot.ParseTypeRef(rawType)
	if err != nil {
		t.Fatalf("ParseTypeRef(%q): %v", rawType, err)
	}
	snapshotID, err := snapshot.NewSnapshotID(id)
	if err != nil {
		t.Fatalf("NewSnapshotID(%d): %v", id, err)
	}
	digest, err := snapshot.ParseDigest("sha256:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("ParseDigest(): %v", err)
	}
	return snapshot.SnapshotRef{ID: snapshotID, Type: ref, Digest: digest}
}
