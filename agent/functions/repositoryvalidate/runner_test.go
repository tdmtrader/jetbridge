package repositoryvalidate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

type registryStub struct {
	validator snapshot.Validator
	lookup    snapshot.TypeRef
}

func (registry *registryStub) Lookup(ref snapshot.TypeRef) (snapshot.Validator, error) {
	registry.lookup = ref
	return registry.validator, nil
}

type validatorFunc func(context.Context, *os.Root, snapshot.ValidationContext) (snapshot.ValidationResult, error)

func (function validatorFunc) Validate(ctx context.Context, root *os.Root, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	return function(ctx, root, validationContext)
}

func TestRunnerRevalidatesExactRepositoryChangeAndProducesReport(t *testing.T) {
	change := validChangeRef()
	base := snapshot.SnapshotRef{ID: 8, Type: "repository/v1", Digest: digest("b")}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "change.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	registry := &registryStub{validator: validatorFunc(func(_ context.Context, _ *os.Root, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
		if got, found := validationContext.Input("base"); !found || got != base {
			t.Fatalf("base input = %+v/%v", got, found)
		}
		return snapshot.ValidationResult{}, nil
	})}
	runner, err := NewRunner(registry)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	document, err := runner.Run(context.Background(), Request{
		Change: change, Root: root, Inputs: map[string]snapshot.SnapshotRef{"base": base},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if registry.lookup != snapshot.TypeRef("repository-change/v1") {
		t.Fatalf("lookup = %q", registry.lookup)
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("validation-report/v1: %v", err)
	}
	if document.Status != "ok" || document.Subject != "snapshot:"+change.ID.String()+"@"+change.Digest.String() || len(document.Checks) != 1 {
		t.Fatalf("document = %+v", document)
	}
}

func TestRunnerRecordsSemanticAndInfrastructureFailuresWithoutForgingSuccess(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status string
	}{
		{name: "invalid candidate", err: errors.New("patch does not apply"), status: "failed"},
		{name: "content unavailable", err: errors.Join(snapshot.ErrContentUnavailable, errors.New("node offline")), status: "error"},
		{name: "expired base", err: snapshot.ErrExpired, status: "error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &registryStub{validator: validatorFunc(func(context.Context, *os.Root, snapshot.ValidationContext) (snapshot.ValidationResult, error) {
				return snapshot.ValidationResult{}, test.err
			})}
			runner, err := NewRunner(registry)
			if err != nil {
				t.Fatal(err)
			}
			document, err := runner.Run(context.Background(), Request{Change: validChangeRef(), Root: t.TempDir()})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if document.Status != test.status || document.Checks[0].Status != test.status || !strings.Contains(document.Checks[0].Detail, test.err.Error()) {
				t.Fatalf("document = %+v", document)
			}
			if err := document.Validate(); err != nil {
				t.Fatalf("invalid report: %v", err)
			}
		})
	}
}

func TestRunnerRejectsWrongTypeAndHonorsCancellation(t *testing.T) {
	runner, err := NewRunner(&registryStub{validator: validatorFunc(func(context.Context, *os.Root, snapshot.ValidationContext) (snapshot.ValidationResult, error) {
		t.Fatal("validator should not run")
		return snapshot.ValidationResult{}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	wrong := validChangeRef()
	wrong.Type = "repository/v1"
	if _, err := runner.Run(context.Background(), Request{Change: wrong, Root: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "repository-change/v1") {
		t.Fatalf("wrong type error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.Run(canceled, Request{Change: validChangeRef(), Root: t.TempDir()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestWriteReportEmitsStrictSnapshotDocument(t *testing.T) {
	document := successfulReport(validChangeRef())
	output := t.TempDir()
	if err := WriteReport(context.Background(), output, document); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(output, "validation-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["schema_version"] != "1.0.0" || decoded["status"] != "ok" {
		t.Fatalf("payload = %s", data)
	}
}

func validChangeRef() snapshot.SnapshotRef {
	return snapshot.SnapshotRef{ID: 7, Type: "repository-change/v1", Digest: digest("a")}
}

func digest(character string) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(character, 64))
}
