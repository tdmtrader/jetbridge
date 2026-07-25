// Package repositoryvalidate implements the deterministic repository-change
// validation function used by version-3 workflows.
package repositoryvalidate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	repositoryChangeType snapshot.TypeRef = "repository-change/v1"
	reportFileName                        = "record.json"
	maximumDetailBytes                    = 4096
)

// Request identifies the exact immutable candidate and input bindings to
// validate. Root is the already-materialized canonical repository-change tree;
// OpenInput is the only way a validator may read another bound snapshot.
type Request struct {
	Change    snapshot.SnapshotRef
	Root      string
	Inputs    map[string]snapshot.SnapshotRef
	OpenInput snapshot.InputOpener
}

type Runner struct {
	registry snapshot.ValidatorRegistry
}

func NewRunner(registry snapshot.ValidatorRegistry) (*Runner, error) {
	if isNilInterface(registry) {
		return nil, fmt.Errorf("repository validate: validator registry is required")
	}
	return &Runner{registry: registry}, nil
}

// Run revalidates the candidate without granting the validator network or
// storage access. A semantic rejection and an unavailable immutable input are
// represented in validation/v1; malformed invocation and cancellation
// remain function errors.
func (runner *Runner) Run(ctx context.Context, request Request) (contracts.Record[contracts.ValidationBody], error) {
	if ctx == nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository validate: context is required")
	}
	if err := ctx.Err(); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	if err := request.Change.Validate(); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository validate: change: %w", err)
	}
	if request.Change.Type != repositoryChangeType {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf(
			"repository validate: change must have exact type %s, got %s",
			repositoryChangeType,
			request.Change.Type,
		)
	}
	if strings.TrimSpace(request.Root) == "" {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository validate: root is required")
	}

	validationContext, err := snapshot.NewValidationContext(request.Inputs, request.OpenInput)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository validate: inputs: %w", err)
	}
	validator, err := runner.registry.Lookup(repositoryChangeType)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository validate: resolve %s validator: %w", repositoryChangeType, err)
	}
	if isNilInterface(validator) {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository validate: registry returned no %s validator", repositoryChangeType)
	}

	root, err := os.OpenRoot(request.Root)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository validate: open candidate root: %w", err)
	}
	// request.Change is an already-sealed snapshot this function was handed to
	// report on, not a candidate it authored, so it goes through read-time
	// revalidation.
	result, validationErr := validator.RevalidateSealed(ctx, root, validationContext)
	closeErr := root.Close()
	if err := ctx.Err(); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	if closeErr != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository validate: close candidate root: %w", closeErr)
	}
	if validationErr == nil {
		validationErr = result.Validate()
		if validationErr != nil {
			validationErr = fmt.Errorf("validator returned invalid metadata: %w", validationErr)
		}
	}

	record, err := reportFor(request.Change, validationErr)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, fmt.Errorf("repository validate: produced invalid validation/v1: %w", err)
	}
	return record, nil
}

func successfulReport(change snapshot.SnapshotRef) (contracts.Record[contracts.ValidationBody], error) {
	return reportFor(change, nil)
}

func reportFor(change snapshot.SnapshotRef, validationErr error) (contracts.Record[contracts.ValidationBody], error) {
	status := "passed"
	detail := "repository change satisfies repository-change/v1"
	summary := "repository change is valid"
	if validationErr != nil {
		status = "failed"
		summary = "repository change is invalid"
		if isInfrastructureFailure(validationErr) {
			status = "error"
			summary = "repository change could not be validated"
		}
		detail = boundedDetail(validationErr.Error())
	}
	body := contracts.ValidationBody{
		Conclusion: contracts.DeriveValidationConclusion([]contracts.ValidationCheck{{Status: status}}),
		Summary:    summary,
		Checks: []contracts.ValidationCheck{{
			ID: "repository-change-contract", Kind: "policy",
			Name: "repository-change contract", Status: status,
			Attempts: []contracts.ValidationAttempt{{
				Number: 1, Status: status, Duration: "0s", Detail: detail,
			}},
		}},
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("validation/v1"),
		[]contracts.Subject{contracts.SubjectFromInput(
			"primary", contracts.SubjectRolePrimary, "change", change,
		)},
		body,
	)
	if err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	if err := body.Validate(record.Subjects); err != nil {
		return contracts.Record[contracts.ValidationBody]{}, err
	}
	return record, nil
}

func isInfrastructureFailure(err error) bool {
	return errors.Is(err, snapshot.ErrContentUnavailable) ||
		errors.Is(err, snapshot.ErrExpired) ||
		errors.Is(err, snapshot.ErrNotFound)
}

// WriteReport materializes the validated function result at a fixed path
// beneath the caller-provided output mount.
func WriteReport(ctx context.Context, outputRoot string, record contracts.Record[contracts.ValidationBody]) error {
	if ctx == nil {
		return fmt.Errorf("repository validate: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputRoot) == "" {
		return fmt.Errorf("repository validate: output root is required")
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		return fmt.Errorf("repository validate: invalid validation/v1: %w", err)
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("repository validate: marshal validation/v1: %w", err)
	}
	payload = append(payload, '\n')
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(outputRoot)
	if err != nil {
		return fmt.Errorf("repository validate: open output root: %w", err)
	}
	writeErr := root.WriteFile(reportFileName, payload, 0600)
	closeErr := root.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("repository validate: write %s: %w", reportFileName, err)
	}
	return nil
}

func boundedDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) <= maximumDetailBytes {
		return detail
	}
	return strings.TrimSpace(detail[:maximumDetailBytes-3]) + "..."
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
