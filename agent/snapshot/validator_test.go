package snapshot_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

func TestPublicValidationFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		reason snapshot.ValidationFailureReason
		want   string
	}{
		{snapshot.RepositoryMetadataMissing, "repository metadata is incomplete"},
		{snapshot.RepositoryMetadataUnsafe, "repository metadata contains an unsupported or unsafe setting"},
		{snapshot.RepositoryHistoryIncomplete, "repository history is shallow or incomplete"},
		{snapshot.RepositoryObjectFormatUnsupported, "repository object format is unsupported"},
		{snapshot.RepositoryGitlinksUnsupported, "repositories containing submodule gitlinks are unsupported"},
		{snapshot.RepositoryDirty, "repository work tree and index must be clean"},
		{snapshot.RepositoryInvalid, "repository object graph is invalid or incomplete"},
	}
	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			cause := errors.New("git stderr contains /tmp/private and token=secret")
			err := snapshot.NewPublicValidationFailure(test.reason, cause)
			var public *snapshot.PublicValidationFailure
			if !errors.As(err, &public) || public.Reason() != test.reason {
				t.Fatalf("public validation failure = %#v, %v", public, err)
			}
			if public.PublicMessage() != test.want {
				t.Fatalf("public message = %q, want %q", public.PublicMessage(), test.want)
			}
			if !errors.Is(err, cause) {
				t.Fatal("internal cause was not retained for logs")
			}
		})
	}

	unknown := snapshot.NewPublicValidationFailure("invented_reason", errors.New("token=secret"))
	var public *snapshot.PublicValidationFailure
	if errors.As(unknown, &public) {
		t.Fatalf("unknown reason manufactured public failure: %#v", public)
	}
}

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
