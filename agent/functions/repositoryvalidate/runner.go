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
	reportFileName                        = "validation-report.json"
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
// represented in validation-report/v1; malformed invocation and cancellation
// remain function errors.
func (runner *Runner) Run(ctx context.Context, request Request) (contracts.ValidationReportDocument, error) {
	if ctx == nil {
		return contracts.ValidationReportDocument{}, fmt.Errorf("repository validate: context is required")
	}
	if err := ctx.Err(); err != nil {
		return contracts.ValidationReportDocument{}, err
	}
	if err := request.Change.Validate(); err != nil {
		return contracts.ValidationReportDocument{}, fmt.Errorf("repository validate: change: %w", err)
	}
	if request.Change.Type != repositoryChangeType {
		return contracts.ValidationReportDocument{}, fmt.Errorf(
			"repository validate: change must have exact type %s, got %s",
			repositoryChangeType,
			request.Change.Type,
		)
	}
	if strings.TrimSpace(request.Root) == "" {
		return contracts.ValidationReportDocument{}, fmt.Errorf("repository validate: root is required")
	}

	validationContext, err := snapshot.NewValidationContext(request.Inputs, request.OpenInput)
	if err != nil {
		return contracts.ValidationReportDocument{}, fmt.Errorf("repository validate: inputs: %w", err)
	}
	validator, err := runner.registry.Lookup(repositoryChangeType)
	if err != nil {
		return contracts.ValidationReportDocument{}, fmt.Errorf("repository validate: resolve %s validator: %w", repositoryChangeType, err)
	}
	if isNilInterface(validator) {
		return contracts.ValidationReportDocument{}, fmt.Errorf("repository validate: registry returned no %s validator", repositoryChangeType)
	}

	root, err := os.OpenRoot(request.Root)
	if err != nil {
		return contracts.ValidationReportDocument{}, fmt.Errorf("repository validate: open candidate root: %w", err)
	}
	result, validationErr := validator.Validate(ctx, root, validationContext)
	closeErr := root.Close()
	if err := ctx.Err(); err != nil {
		return contracts.ValidationReportDocument{}, err
	}
	if closeErr != nil {
		return contracts.ValidationReportDocument{}, fmt.Errorf("repository validate: close candidate root: %w", closeErr)
	}
	if validationErr == nil {
		validationErr = result.Validate()
		if validationErr != nil {
			validationErr = fmt.Errorf("validator returned invalid metadata: %w", validationErr)
		}
	}

	document := reportFor(request.Change, validationErr)
	if err := document.Validate(); err != nil {
		return contracts.ValidationReportDocument{}, fmt.Errorf("repository validate: produced invalid validation-report/v1: %w", err)
	}
	return document, nil
}

func successfulReport(change snapshot.SnapshotRef) contracts.ValidationReportDocument {
	return reportFor(change, nil)
}

func reportFor(change snapshot.SnapshotRef, validationErr error) contracts.ValidationReportDocument {
	status := "ok"
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
	return contracts.ValidationReportDocument{
		SchemaVersion: "1.0.0",
		Subject:       "snapshot:" + change.ID.String() + "@" + change.Digest.String(),
		Status:        status,
		Summary:       summary,
		Checks: []contracts.ValidationCheck{{
			Name: "repository-change-contract", Status: status, Detail: detail,
		}},
	}
}

func isInfrastructureFailure(err error) bool {
	return errors.Is(err, snapshot.ErrContentUnavailable) ||
		errors.Is(err, snapshot.ErrExpired) ||
		errors.Is(err, snapshot.ErrNotFound)
}

// WriteReport materializes the validated function result at a fixed path
// beneath the caller-provided output mount.
func WriteReport(ctx context.Context, outputRoot string, document contracts.ValidationReportDocument) error {
	if ctx == nil {
		return fmt.Errorf("repository validate: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputRoot) == "" {
		return fmt.Errorf("repository validate: output root is required")
	}
	if err := document.Validate(); err != nil {
		return fmt.Errorf("repository validate: invalid validation-report/v1: %w", err)
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("repository validate: marshal validation-report/v1: %w", err)
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
