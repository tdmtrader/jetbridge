// Package experiment defines durable, snapshot-based workflow experiments.
// It deliberately contains no executor: candidate and evaluator cells are
// admitted through the ordinary workflowrun binder.
package experiment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
)

// Experiment limits are part of admission, not UI guidance. They bound the
// cross product materialized by PostgreSQL and the data copied into API
// responses and scorecards.
const (
	MaxVariants            = 16
	MaxFixtures            = 256
	MaxRepetitions         = 1000
	MaxMaterializedCells   = 2_000
	MaxMeasurementsPerCell = 32
	MaxListedExperiments   = 100
	maxBudgetUSDExclusive  = 1_000_000_000_000
)

var (
	labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	hashPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ID and CellID are positive signed 64-bit database identities. Their wire
// representation is quoted decimal so browsers never lose precision.
type ID int64
type CellID int64

func (id ID) Validate() error     { return validateID(int64(id), "experiment ID") }
func (id CellID) Validate() error { return validateID(int64(id), "experiment cell ID") }
func (id ID) String() string      { return validIDString(int64(id)) }
func (id CellID) String() string  { return validIDString(int64(id)) }

func (id ID) MarshalJSON() ([]byte, error)     { return marshalID(int64(id), "experiment ID") }
func (id CellID) MarshalJSON() ([]byte, error) { return marshalID(int64(id), "experiment cell ID") }
func (id *ID) UnmarshalJSON(raw []byte) error {
	value, err := unmarshalID(raw, "experiment ID")
	if err == nil {
		*id = ID(value)
	}
	return err
}
func (id *CellID) UnmarshalJSON(raw []byte) error {
	value, err := unmarshalID(raw, "experiment cell ID")
	if err == nil {
		*id = CellID(value)
	}
	return err
}

type State string

const (
	StateDraft     State = "draft"
	StateRunning   State = "running"
	StateCanceling State = "canceling"
	StateCanceled  State = "canceled"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
)

func (state State) Validate() error {
	switch state {
	case StateDraft, StateRunning, StateCanceling, StateCanceled, StateCompleted, StateFailed:
		return nil
	default:
		return fmt.Errorf("experiment: invalid state %q", state)
	}
}

type TargetKind string

const (
	TargetWorkflow TargetKind = "workflow"
	TargetFunction TargetKind = "function"
)

type Target struct {
	Kind         TargetKind `json:"kind"`
	WorkflowName string     `json:"workflow_name"`
	DefinitionID int64      `json:"definition_id"`
	Version      int        `json:"version"`
	FunctionID   string     `json:"function_id,omitempty"`
}

func (target Target) Validate() error {
	if target.Kind != TargetWorkflow && target.Kind != TargetFunction {
		return fmt.Errorf("experiment: target kind must be workflow or function")
	}
	if strings.TrimSpace(target.WorkflowName) == "" {
		return fmt.Errorf("experiment: target workflow_name is required")
	}
	if target.DefinitionID <= 0 {
		return fmt.Errorf("experiment: target definition_id must be positive")
	}
	if target.Version <= 0 {
		return fmt.Errorf("experiment: target version must be positive")
	}
	switch target.Kind {
	case TargetWorkflow:
		if target.FunctionID != "" {
			return fmt.Errorf("experiment: workflow target must not set function_id")
		}
	case TargetFunction:
		if strings.TrimSpace(target.FunctionID) == "" {
			return fmt.Errorf("experiment: function target requires function_id")
		}
	}
	return nil
}

type Variant struct {
	Label                       string `json:"label"`
	Control                     bool   `json:"control"`
	Target                      Target `json:"target"`
	SignatureHash               string `json:"signature_hash"`
	TargetConfigHash            string `json:"target_config_hash,omitempty"`
	DevValidationProvenanceHash string `json:"dev_validation_provenance_hash,omitempty"`
}

type FixtureRole string

const (
	FixtureNormal          FixtureRole = "normal"
	FixtureNegativeControl FixtureRole = "negative_control"
)

type Comparator string

const (
	ComparatorLT      Comparator = "lt"
	ComparatorLTE     Comparator = "lte"
	ComparatorGT      Comparator = "gt"
	ComparatorGTE     Comparator = "gte"
	ComparatorBetween Comparator = "between"
)

type Assertion struct {
	Metric     string     `json:"metric"`
	Comparator Comparator `json:"comparator"`
	Thresholds []float64  `json:"thresholds"`
}

func (assertion Assertion) Validate() error {
	if strings.TrimSpace(assertion.Metric) == "" {
		return fmt.Errorf("experiment: assertion metric is required")
	}
	for _, threshold := range assertion.Thresholds {
		if math.IsNaN(threshold) || math.IsInf(threshold, 0) {
			return fmt.Errorf("experiment: assertion %q thresholds must be finite", assertion.Metric)
		}
	}
	switch assertion.Comparator {
	case ComparatorLT, ComparatorLTE, ComparatorGT, ComparatorGTE:
		if len(assertion.Thresholds) != 1 {
			return fmt.Errorf("experiment: assertion %q comparator %s requires exactly one threshold", assertion.Metric, assertion.Comparator)
		}
	case ComparatorBetween:
		if len(assertion.Thresholds) != 2 {
			return fmt.Errorf("experiment: assertion %q comparator between requires exactly two thresholds", assertion.Metric)
		}
		if assertion.Thresholds[0] > assertion.Thresholds[1] {
			return fmt.Errorf("experiment: assertion %q between thresholds must be ascending", assertion.Metric)
		}
	default:
		return fmt.Errorf("experiment: assertion %q comparator is invalid", assertion.Metric)
	}
	return nil
}

func (assertion Assertion) Evaluate(value float64) bool {
	if assertion.Validate() != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return false
	}
	switch assertion.Comparator {
	case ComparatorLT:
		return value < assertion.Thresholds[0]
	case ComparatorLTE:
		return value <= assertion.Thresholds[0]
	case ComparatorGT:
		return value > assertion.Thresholds[0]
	case ComparatorGTE:
		return value >= assertion.Thresholds[0]
	case ComparatorBetween:
		return value >= assertion.Thresholds[0] && value <= assertion.Thresholds[1]
	default:
		return false
	}
}

type Fixture struct {
	Label      string                         `json:"label"`
	Role       FixtureRole                    `json:"role"`
	Inputs     map[string]snapshot.SnapshotID `json:"inputs"`
	Assertions []Assertion                    `json:"assertions,omitempty"`
}

type SourceDirection string

const (
	SourceFixtureInput    SourceDirection = "fixture_input"
	SourceCandidateOutput SourceDirection = "candidate_output"
)

type EvaluatorMapping struct {
	EvaluatorPort   string          `json:"evaluator_port"`
	SourceDirection SourceDirection `json:"source_direction"`
	SourcePort      string          `json:"source_port"`
}

type Evaluator struct {
	Target                      Target                   `json:"target"`
	TargetConfigHash            string                   `json:"target_config_hash,omitempty"`
	DevValidationProvenanceHash string                   `json:"dev_validation_provenance_hash,omitempty"`
	Signature                   workflow.PublicSignature `json:"signature"`
	Mappings                    []EvaluatorMapping       `json:"mappings"`
	MeasurementsPort            string                   `json:"measurements_port"`
}

type Budget struct {
	PerCellUSD       float64 `json:"per_cell_usd,omitempty"`
	TotalUSD         float64 `json:"total_usd,omitempty"`
	MaxTokensPerCell int64   `json:"max_tokens_per_cell,omitempty"`
}

func (budget Budget) Limited() bool {
	return budget.PerCellUSD > 0 || budget.TotalUSD > 0 || budget.MaxTokensPerCell > 0
}

func (budget Budget) Validate() error {
	if !finiteNonnegative(budget.PerCellUSD) || !finiteNonnegative(budget.TotalUSD) {
		return fmt.Errorf("experiment: budget amounts must be finite and nonnegative")
	}
	for _, amount := range []float64{budget.PerCellUSD, budget.TotalUSD} {
		scaled := amount * 1_000_000
		if math.IsInf(scaled, 0) || scaled > float64(math.MaxInt64) {
			return fmt.Errorf("experiment: budget amounts must fit a signed 64-bit micro-USD integer")
		}
		if amount >= maxBudgetUSDExclusive {
			return fmt.Errorf("experiment: budget amounts must fit durable NUMERIC(18,6) precision")
		}
		if math.Abs(scaled-math.Round(scaled)) > 0.0000001 {
			return fmt.Errorf("experiment: budget amounts support at most six decimal places")
		}
	}
	if budget.MaxTokensPerCell < 0 {
		return fmt.Errorf("experiment: max_tokens_per_cell must be nonnegative")
	}
	return nil
}

// Definition is the complete mutable draft and the immutable value frozen by
// Start. DB timestamps and ownership live in persistence records, not in this
// semantic contract.
type Definition struct {
	Name        string                   `json:"name"`
	State       State                    `json:"state"`
	Signature   workflow.PublicSignature `json:"signature"`
	Variants    []Variant                `json:"variants"`
	Fixtures    []Fixture                `json:"fixtures"`
	Evaluator   Evaluator                `json:"evaluator"`
	Repetitions int                      `json:"repetitions"`
	Budget      Budget                   `json:"budget"`
}

func (definition Definition) CanMutate() bool { return definition.State == StateDraft }

func (definition Definition) ExpectedCells() int {
	count, err := definition.MaterializedCellCount()
	if err != nil {
		return 0
	}
	return count
}

// MaterializedCellCount computes the matrix without integer overflow and
// rejects work beyond the durable/API bound.
func (definition Definition) MaterializedCellCount() (int, error) {
	if definition.Repetitions <= 0 || len(definition.Variants) == 0 || len(definition.Fixtures) == 0 {
		return 0, fmt.Errorf("experiment: matrix dimensions must be positive")
	}
	variants := uint64(len(definition.Variants))
	fixtures := uint64(len(definition.Fixtures))
	repetitions := uint64(definition.Repetitions)
	if variants > MaxVariants {
		return 0, fmt.Errorf("experiment: at most %d variants are allowed", MaxVariants)
	}
	if fixtures > MaxFixtures {
		return 0, fmt.Errorf("experiment: at most %d fixtures are allowed", MaxFixtures)
	}
	if repetitions > MaxRepetitions {
		return 0, fmt.Errorf("experiment: repetitions must be within 1-%d", MaxRepetitions)
	}
	if variants > uint64(MaxMaterializedCells) ||
		fixtures > uint64(MaxMaterializedCells)/variants ||
		repetitions > uint64(MaxMaterializedCells)/(variants*fixtures) {
		return 0, fmt.Errorf("experiment: matrix must contain at most %d materialized cells", MaxMaterializedCells)
	}
	return int(variants * fixtures * repetitions), nil
}

func (definition Definition) Validate() error {
	if strings.TrimSpace(definition.Name) == "" {
		return fmt.Errorf("experiment: name is required")
	}
	if err := definition.State.Validate(); err != nil {
		return err
	}
	if definition.Repetitions <= 0 || definition.Repetitions > MaxRepetitions {
		return fmt.Errorf("experiment: repetitions must be within 1-%d", MaxRepetitions)
	}
	if err := validateSignature(definition.Signature); err != nil {
		return fmt.Errorf("experiment: candidate signature: %w", err)
	}
	signatureHash, err := HashSignature(definition.Signature)
	if err != nil {
		return err
	}
	if len(definition.Variants) < 2 {
		return fmt.Errorf("experiment: at least two variants are required")
	}
	if len(definition.Variants) > MaxVariants {
		return fmt.Errorf("experiment: at most %d variants are allowed", MaxVariants)
	}
	labels := make(map[string]struct{}, len(definition.Variants))
	controls := 0
	for index, variant := range definition.Variants {
		if err := validateLabel(variant.Label); err != nil {
			return fmt.Errorf("experiment: variants[%d]: %w", index, err)
		}
		if _, found := labels[variant.Label]; found {
			return fmt.Errorf("experiment: duplicate variant label %q", variant.Label)
		}
		labels[variant.Label] = struct{}{}
		if variant.Control {
			controls++
		}
		if err := variant.Target.Validate(); err != nil {
			return fmt.Errorf("experiment: variants[%d]: %w", index, err)
		}
		if !hashPattern.MatchString(variant.SignatureHash) || variant.SignatureHash != signatureHash {
			return fmt.Errorf("experiment: variant %q has incompatible signature %q", variant.Label, variant.SignatureHash)
		}
		if definition.State == StateDraft && variant.TargetConfigHash != "" {
			return fmt.Errorf("experiment: variant %q target_config_hash is server-derived at start", variant.Label)
		}
		if definition.State == StateDraft && variant.DevValidationProvenanceHash != "" {
			return fmt.Errorf("experiment: variant %q dev_validation_provenance_hash is server-derived at start", variant.Label)
		}
		if definition.State != StateDraft && !hashPattern.MatchString(variant.TargetConfigHash) {
			return fmt.Errorf("experiment: variant %q has invalid frozen target_config_hash", variant.Label)
		}
		if definition.State != StateDraft && variant.DevValidationProvenanceHash != "" &&
			!hashPattern.MatchString(variant.DevValidationProvenanceHash) {
			return fmt.Errorf("experiment: variant %q has invalid frozen dev_validation_provenance_hash", variant.Label)
		}
	}
	if controls != 1 {
		return fmt.Errorf("experiment: exactly one control variant is required, got %d", controls)
	}
	if len(definition.Fixtures) == 0 {
		return fmt.Errorf("experiment: at least one fixture is required")
	}
	if len(definition.Fixtures) > MaxFixtures {
		return fmt.Errorf("experiment: at most %d fixtures are allowed", MaxFixtures)
	}
	if _, err := definition.MaterializedCellCount(); err != nil {
		return err
	}
	fixtureLabels := make(map[string]struct{}, len(definition.Fixtures))
	for index, fixture := range definition.Fixtures {
		if err := validateFixture(fixture, definition.Signature); err != nil {
			return fmt.Errorf("experiment: fixtures[%d]: %w", index, err)
		}
		if _, found := fixtureLabels[fixture.Label]; found {
			return fmt.Errorf("experiment: duplicate fixture label %q", fixture.Label)
		}
		fixtureLabels[fixture.Label] = struct{}{}
	}
	if err := validateEvaluator(definition.Evaluator, definition.Signature); err != nil {
		return err
	}
	if definition.State == StateDraft && definition.Evaluator.TargetConfigHash != "" {
		return fmt.Errorf("experiment: evaluator target_config_hash is server-derived at start")
	}
	if definition.State == StateDraft && definition.Evaluator.DevValidationProvenanceHash != "" {
		return fmt.Errorf("experiment: evaluator dev_validation_provenance_hash is server-derived at start")
	}
	if definition.State != StateDraft && !hashPattern.MatchString(definition.Evaluator.TargetConfigHash) {
		return fmt.Errorf("experiment: evaluator has invalid frozen target_config_hash")
	}
	if definition.State != StateDraft && definition.Evaluator.DevValidationProvenanceHash != "" &&
		!hashPattern.MatchString(definition.Evaluator.DevValidationProvenanceHash) {
		return fmt.Errorf("experiment: evaluator has invalid frozen dev_validation_provenance_hash")
	}
	if err := definition.Budget.Validate(); err != nil {
		return err
	}
	return nil
}

// ValidateStart is separate so admission-only invariants can be added without
// changing draft portability. Dollar limits have durable reservations and
// process-level caps; token limits remain draftable but fail closed here until
// the runtime can genuinely stop execution at an exact token boundary.
func (definition Definition) ValidateStart() error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if definition.Budget.MaxTokensPerCell > 0 {
		return fmt.Errorf("experiment: max_tokens_per_cell cannot be hard-enforced by the current agent runtime")
	}
	return nil
}

func HashSignature(signature workflow.PublicSignature) (string, error) {
	if err := validateSignature(signature); err != nil {
		return "", err
	}
	payload, err := json.Marshal(signature)
	if err != nil {
		return "", fmt.Errorf("experiment: marshal signature: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func validateFixture(fixture Fixture, signature workflow.PublicSignature) error {
	if err := validateLabel(fixture.Label); err != nil {
		return err
	}
	switch fixture.Role {
	case FixtureNormal:
		if len(fixture.Assertions) != 0 {
			return fmt.Errorf("normal fixture must not have assertions")
		}
	case FixtureNegativeControl:
		if len(fixture.Assertions) == 0 {
			return fmt.Errorf("negative_control fixture requires at least one assertion")
		}
	default:
		return fmt.Errorf("fixture role must be normal or negative_control")
	}
	if len(fixture.Assertions) > MaxMeasurementsPerCell {
		return fmt.Errorf("fixture %q may contain at most %d assertion metrics", fixture.Label, MaxMeasurementsPerCell)
	}
	ports := portMap(signature.Inputs)
	for _, port := range signature.Inputs {
		if !port.Optional {
			if _, found := fixture.Inputs[port.Name]; !found {
				return fmt.Errorf("fixture %q is missing required input %q", fixture.Label, port.Name)
			}
		}
	}
	for port, id := range fixture.Inputs {
		if _, found := ports[port]; !found {
			return fmt.Errorf("fixture %q has unknown input %q", fixture.Label, port)
		}
		if err := id.Validate(); err != nil {
			return fmt.Errorf("fixture %q input %q: %w", fixture.Label, port, err)
		}
	}
	seen := make(map[string]struct{}, len(fixture.Assertions))
	for _, assertion := range fixture.Assertions {
		if _, found := seen[assertion.Metric]; found {
			return fmt.Errorf("fixture %q has duplicate assertion metric %q", fixture.Label, assertion.Metric)
		}
		seen[assertion.Metric] = struct{}{}
		if err := assertion.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateEvaluator(evaluator Evaluator, candidate workflow.PublicSignature) error {
	if err := evaluator.Target.Validate(); err != nil {
		return fmt.Errorf("experiment: evaluator: %w", err)
	}
	if err := validateSignature(evaluator.Signature); err != nil {
		return fmt.Errorf("experiment: evaluator signature: %w", err)
	}
	if strings.TrimSpace(evaluator.MeasurementsPort) == "" {
		return fmt.Errorf("experiment: evaluator measurements_port is required")
	}
	outputs := portMap(evaluator.Signature.Outputs)
	measurements, found := outputs[evaluator.MeasurementsPort]
	if !found {
		return fmt.Errorf("experiment: evaluator measurements_port %q is not an output", evaluator.MeasurementsPort)
	}
	if measurements.Type != snapshot.TypeRef("measurements/v1") {
		return fmt.Errorf("experiment: evaluator measurements_port %q must have exact type measurements/v1", evaluator.MeasurementsPort)
	}
	evaluatorInputs := portMap(evaluator.Signature.Inputs)
	candidateInputs := portMap(candidate.Inputs)
	candidateOutputs := portMap(candidate.Outputs)
	mapped := make(map[string]struct{}, len(evaluator.Mappings))
	for _, mapping := range evaluator.Mappings {
		target, found := evaluatorInputs[mapping.EvaluatorPort]
		if !found {
			return fmt.Errorf("experiment: evaluator mapping has unknown evaluator input %q", mapping.EvaluatorPort)
		}
		if _, found := mapped[mapping.EvaluatorPort]; found {
			return fmt.Errorf("experiment: duplicate evaluator mapping for %q", mapping.EvaluatorPort)
		}
		mapped[mapping.EvaluatorPort] = struct{}{}
		var source workflow.SignaturePort
		switch mapping.SourceDirection {
		case SourceFixtureInput:
			var sourceFound bool
			source, sourceFound = candidateInputs[mapping.SourcePort]
			if !sourceFound {
				return fmt.Errorf("experiment: evaluator mapping has unknown fixture input %q", mapping.SourcePort)
			}
		case SourceCandidateOutput:
			var sourceFound bool
			source, sourceFound = candidateOutputs[mapping.SourcePort]
			if !sourceFound {
				return fmt.Errorf("experiment: evaluator mapping has unknown candidate output %q", mapping.SourcePort)
			}
		default:
			return fmt.Errorf("experiment: evaluator mapping source_direction is invalid")
		}
		if source.Type != target.Type {
			return fmt.Errorf("experiment: evaluator mapping %q type mismatch: source %s, evaluator %s", mapping.EvaluatorPort, source.Type, target.Type)
		}
	}
	for _, port := range evaluator.Signature.Inputs {
		if !port.Optional {
			if _, found := mapped[port.Name]; !found {
				return fmt.Errorf("experiment: missing required evaluator input %q", port.Name)
			}
		}
	}
	return nil
}

func validateSignature(signature workflow.PublicSignature) error {
	for direction, ports := range map[string][]workflow.SignaturePort{"input": signature.Inputs, "output": signature.Outputs} {
		seen := make(map[string]struct{}, len(ports))
		for index, port := range ports {
			if strings.TrimSpace(port.Name) == "" {
				return fmt.Errorf("%s port %d name is required", direction, index)
			}
			if err := port.Type.Validate(); err != nil {
				return fmt.Errorf("%s port %q: %w", direction, port.Name, err)
			}
			if _, found := seen[port.Name]; found {
				return fmt.Errorf("duplicate %s port %q", direction, port.Name)
			}
			seen[port.Name] = struct{}{}
		}
	}
	return nil
}

func portMap(ports []workflow.SignaturePort) map[string]workflow.SignaturePort {
	result := make(map[string]workflow.SignaturePort, len(ports))
	for _, port := range ports {
		result[port.Name] = port
	}
	return result
}

func validateLabel(label string) error {
	if !labelPattern.MatchString(label) {
		return fmt.Errorf("label %q must be 1-128 letters, digits, dots, dashes, or underscores", label)
	}
	return nil
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func validateID(value int64, name string) error {
	if value <= 0 {
		return fmt.Errorf("experiment: %s must be a positive signed 64-bit integer", name)
	}
	return nil
}

func validIDString(value int64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func marshalID(value int64, name string) ([]byte, error) {
	if err := validateID(value, name); err != nil {
		return nil, err
	}
	return []byte(`"` + strconv.FormatInt(value, 10) + `"`), nil
}

func unmarshalID(raw []byte, name string) (int64, error) {
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return 0, fmt.Errorf("experiment: %s must be a quoted decimal string", name)
	}
	if encoded == "" || encoded[0] == '+' || (len(encoded) > 1 && encoded[0] == '0') {
		return 0, fmt.Errorf("experiment: %s must be a canonical positive decimal string", name)
	}
	value, err := strconv.ParseInt(encoded, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("experiment: %s must be a canonical positive decimal string", name)
	}
	return value, nil
}
