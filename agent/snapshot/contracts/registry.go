// Package contracts implements the server-authoritative built-in snapshot
// validators. It contains no global registry; each caller owns an immutable
// exact-match registry instance.
package contracts

import (
	"context"
	"fmt"
	"os"

	"github.com/concourse/concourse/agent/snapshot"
)

type Registration struct {
	Type      snapshot.TypeRef
	Validator snapshot.Validator
}

type Registry struct {
	validators map[snapshot.TypeRef]snapshot.Validator
	types      []snapshot.TypeRef
}

type registryConfig struct {
	canonicalizer snapshot.Canonicalizer
}

type RegistryOption func(*registryConfig)

// WithCanonicalizer supplies the bounded, deployment-owned scratch policy
// used by validators that must materialize nested snapshot archives.
func WithCanonicalizer(canonicalizer snapshot.Canonicalizer) RegistryOption {
	return func(config *registryConfig) {
		config.canonicalizer = canonicalizer
	}
}

func NewRegistry(options ...RegistryOption) (*Registry, error) {
	config := registryConfig{}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("snapshot contracts: registry option is required")
		}
		option(&config)
	}
	registrations := make([]Registration, 0, len(builtinTypeNames()))
	for _, raw := range builtinTypeNames() {
		ref, err := snapshot.ParseTypeRef(raw)
		if err != nil {
			return nil, err
		}
		validator, err := builtinValidator(ref, config)
		if err != nil {
			return nil, err
		}
		registrations = append(registrations, Registration{
			Type:      ref,
			Validator: validator,
		})
	}
	return NewRegistryWith(registrations...)
}

// gateInvariant promotes one gate-independent validation function to the
// two-gate snapshot.Validator surface.
//
// A snapshot type whose tree carries no record envelope has nothing that can
// differ between the gates: there is no schema digest for a producer to choose
// and no server-declared authority to consult, so seal-time admission and
// read-time revalidation are literally the same predicate. Spelling it out twice
// per type would create a dozen chances for two copies to drift, so they share
// one function here and the sharing is visible at the registration site below.
type gateInvariant struct {
	validate func(context.Context, *os.Root, snapshot.ValidationContext) (snapshot.ValidationResult, error)
}

func (gate gateInvariant) AdmitForSeal(ctx context.Context, root *os.Root, declarations snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	return gate.validate(ctx, root, declarations)
}

func (gate gateInvariant) RevalidateSealed(ctx context.Context, root *os.Root, exposed snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	return gate.validate(ctx, root, exposed)
}

func builtinValidator(ref snapshot.TypeRef, config registryConfig) (snapshot.Validator, error) {
	switch ref.String() {
	case "opaque/v1":
		return gateInvariant{opaqueValidator{}.Validate}, nil
	case "repository/v1":
		return gateInvariant{repositoryValidator{}.Validate}, nil
	case "repository-change/v1":
		return repositoryChangeValidator{canonicalizer: config.canonicalizer}, nil
	case "review/v1":
		return reviewValidator{}, nil
	case "validation/v1":
		return validationValidator{}, nil
	case "work-item/v1":
		return gateInvariant{workItemValidator{}.Validate}, nil
	case "measurements/v1":
		return measurementsValidator{}, nil
	case "selection/v1":
		return selectionValidator{}, nil
	case "upgrade-request/v1":
		return gateInvariant{documentValidator[*UpgradeRequestDocument]{fileName: "upgrade-request.json"}.Validate}, nil
	case "upgrade-report/v1":
		return gateInvariant{documentValidator[*UpgradeReportDocument]{fileName: "upgrade-report.json"}.Validate}, nil
	case "database-snapshot/v1":
		return gateInvariant{auditValidator{kind: "database"}.Validate}, nil
	case "deployment-snapshot/v1":
		return gateInvariant{auditValidator{kind: "deployment"}.Validate}, nil
	case "audit-findings/v1":
		return gateInvariant{auditValidator{kind: "findings"}.Validate}, nil
	case "diagnosis/v1":
		return diagnosisValidator{}, nil
	case "question/v1":
		return gateInvariant{documentValidator[*QuestionDocument]{fileName: "question.json"}.Validate}, nil
	case "human-answer/v1":
		return gateInvariant{documentValidator[*HumanAnswerDocument]{fileName: "human-answer.json"}.Validate}, nil
	case "log-bundle/v1":
		return gateInvariant{logBundleValidator{}.Validate}, nil
	default:
		return nil, fmt.Errorf("snapshot contracts: no built-in validator for %q", ref)
	}
}

func NewRegistryWith(registrations ...Registration) (*Registry, error) {
	registry := &Registry{
		validators: make(map[snapshot.TypeRef]snapshot.Validator, len(registrations)),
		types:      make([]snapshot.TypeRef, 0, len(registrations)),
	}
	for _, registration := range registrations {
		if err := registration.Type.Validate(); err != nil {
			return nil, fmt.Errorf("snapshot contracts: register type: %w", err)
		}
		if registration.Validator == nil {
			return nil, fmt.Errorf("snapshot contracts: validator for %q is required", registration.Type)
		}
		if _, found := registry.validators[registration.Type]; found {
			return nil, fmt.Errorf("snapshot contracts: duplicate registration for %q", registration.Type)
		}
		registry.validators[registration.Type] = registration.Validator
		registry.types = append(registry.types, registration.Type)
	}
	return registry, nil
}

func (r *Registry) Lookup(ref snapshot.TypeRef) (snapshot.Validator, error) {
	if r == nil {
		return nil, fmt.Errorf("snapshot contracts: registry is required")
	}
	if err := ref.Validate(); err != nil {
		return nil, fmt.Errorf("snapshot contracts: lookup: %w", err)
	}
	validator, found := r.validators[ref]
	if !found {
		return nil, fmt.Errorf("snapshot contracts: unsupported type %q", ref)
	}
	return validator, nil
}

func (r *Registry) Types() []snapshot.TypeRef {
	if r == nil {
		return nil
	}
	return append([]snapshot.TypeRef(nil), r.types...)
}

func builtinTypeNames() []string {
	return []string{
		"opaque/v1",
		"repository/v1",
		"repository-change/v1",
		"review/v1",
		"work-item/v1",
		"log-bundle/v1",
		"measurements/v1",
		"selection/v1",
		"validation/v1",
		"upgrade-request/v1",
		"upgrade-report/v1",
		"database-snapshot/v1",
		"deployment-snapshot/v1",
		"audit-findings/v1",
		"diagnosis/v1",
		"question/v1",
		"human-answer/v1",
	}
}

var _ snapshot.ValidatorRegistry = (*Registry)(nil)
