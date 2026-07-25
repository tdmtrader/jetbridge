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
	if err := os.WriteFile(filepath.Join(root, "record.json"), []byte(`{}`), 0600); err != nil {
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
	record, err := runner.Run(context.Background(), Request{
		Change: change, Root: root, Inputs: map[string]snapshot.SnapshotRef{"base": base},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if registry.lookup != snapshot.TypeRef("repository-change/v1") {
		t.Fatalf("lookup = %q", registry.lookup)
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		t.Fatalf("validation/v1: %v", err)
	}
	if record.Body.Conclusion != "passed" || record.Subjects[0].Digest != change.Digest || len(record.Body.Checks) != 1 {
		t.Fatalf("record = %+v", record)
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
			record, err := runner.Run(context.Background(), Request{Change: validChangeRef(), Root: t.TempDir()})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if record.Body.Conclusion != test.status || record.Body.Checks[0].Status != test.status ||
				!strings.Contains(record.Body.Checks[0].Attempts[0].Detail, test.err.Error()) {
				t.Fatalf("record = %+v", record)
			}
			if err := record.Body.Validate(record.Subjects); err != nil {
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
	record, err := successfulReport(validChangeRef())
	if err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err := WriteReport(context.Background(), output, record); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(output, "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	body, ok := decoded["body"].(map[string]any)
	if !ok || decoded["type"] != "validation/v1" || body["conclusion"] != "passed" {
		t.Fatalf("payload = %s", data)
	}
}

func validChangeRef() snapshot.SnapshotRef {
	return snapshot.SnapshotRef{ID: 7, Type: "repository-change/v1", Digest: digest("a")}
}

func digest(character string) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(character, 64))
}
