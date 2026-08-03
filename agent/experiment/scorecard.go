package experiment

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type CellStatus string

const (
	CellPending                  CellStatus = "pending"
	CellRunning                  CellStatus = "running"
	CellValidMeasurement         CellStatus = "valid_measurement"
	CellCandidateContractFailure CellStatus = "candidate_contract_failure"
	CellCandidatePlatformFailure CellStatus = "candidate_platform_failure"
	CellEvaluatorFailure         CellStatus = "evaluator_failure"
	CellNegativeControlFailure   CellStatus = "negative_control_assertion_failure"
	CellSkippedBudget            CellStatus = "skipped_budget"
	CellCanceled                 CellStatus = "canceled"
)

func (status CellStatus) terminal() bool {
	switch status {
	case CellValidMeasurement, CellCandidateContractFailure, CellCandidatePlatformFailure,
		CellEvaluatorFailure, CellNegativeControlFailure, CellSkippedBudget, CellCanceled:
		return true
	default:
		return false
	}
}

type CellResult struct {
	ID                    CellID                  `json:"id"`
	Variant               string                  `json:"variant"`
	Fixture               string                  `json:"fixture"`
	Role                  FixtureRole             `json:"role"`
	Repetition            int                     `json:"repetition"`
	Status                CellStatus              `json:"status"`
	Measurements          []contracts.Measurement `json:"measurements,omitempty"`
	NegativeControlPassed bool                    `json:"negative_control_passed,omitempty"`
	CostUSD               float64                 `json:"cost_usd"`
	Latency               time.Duration           `json:"latency"`
	InputTokens           int64                   `json:"input_tokens"`
	OutputTokens          int64                   `json:"output_tokens"`
	HumanInterventions    int                     `json:"human_interventions"`
}

type ScorecardRequest struct {
	ExperimentID            ID
	ControlLabel            string
	ExpectedCellsPerVariant int
	Cells                   []CellResult
}

type Distribution struct {
	Count  int     `json:"count"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	StdDev float64 `json:"stddev"`
	P05    float64 `json:"p05"`
	P25    float64 `json:"p25"`
	P75    float64 `json:"p75"`
	P95    float64 `json:"p95"`
}

type VariantScore struct {
	ExpectedCells        int                     `json:"expected_cells"`
	ObservedCells        int                     `json:"observed_cells"`
	ValidCells           int                     `json:"valid_cells"`
	ValidCoverage        float64                 `json:"valid_coverage"`
	PlatformErrors       int                     `json:"platform_errors"`
	PlatformErrorRate    float64                 `json:"platform_error_rate"`
	BudgetSkipped        int                     `json:"budget_skipped"`
	NegativeControlsPass bool                    `json:"negative_controls_pass"`
	Metrics              map[string]Distribution `json:"metrics"`
	MetricDirections     map[string]string       `json:"metric_directions"`
	CostUSD              Distribution            `json:"cost_usd"`
	LatencySeconds       Distribution            `json:"latency_seconds"`
	Tokens               Distribution            `json:"tokens"`
	HumanInterventions   Distribution            `json:"human_interventions"`
}

type Recommendation string

const (
	RecommendationWinner       Recommendation = "winner"
	RecommendationInsufficient Recommendation = "insufficient_evidence"
)

type PairedComparison struct {
	Variant          string         `json:"variant"`
	Control          string         `json:"control"`
	Metric           string         `json:"metric"`
	Direction        string         `json:"direction"`
	ControlCount     int            `json:"control_count"`
	VariantCount     int            `json:"variant_count"`
	PairedCount      int            `json:"paired_count"`
	Wins             int            `json:"wins"`
	Ties             int            `json:"ties"`
	Losses           int            `json:"losses"`
	MeanDelta        float64        `json:"mean_delta"`
	ConfidenceLow    float64        `json:"confidence_low"`
	ConfidenceHigh   float64        `json:"confidence_high"`
	Winner           string         `json:"winner,omitempty"`
	Recommendation   Recommendation `json:"recommendation"`
	FailedConditions []string       `json:"failed_conditions,omitempty"`
}

type Scorecard struct {
	ExperimentID ID                                     `json:"experiment_id"`
	Control      string                                 `json:"control"`
	Variants     map[string]VariantScore                `json:"variants"`
	Comparisons  map[string]map[string]PairedComparison `json:"comparisons"`
	Cells        []CellResult                           `json:"cells"`
}

type cellKey struct {
	fixture    string
	repetition int
}

type cellIdentity struct {
	variant    string
	fixture    string
	repetition int
}

type variantAccumulator struct {
	cells         []CellResult
	metricValues  map[string][]float64
	directions    map[string]string
	metricByCell  map[cellKey]map[string]float64
	cost          []float64
	latency       []float64
	tokens        []float64
	interventions []float64
	valid         int
	platform      int
	budgetSkipped int
	negativeSeen  bool
	negativePass  bool
}

type metricDefinition struct {
	unit       string
	direction  string
	minimumSet bool
	minimum    float64
	maximumSet bool
	maximum    float64
	targetSet  bool
	target     float64
}

func BuildScorecard(request ScorecardRequest) (Scorecard, error) {
	if err := request.ExperimentID.Validate(); err != nil {
		return Scorecard{}, err
	}
	if strings.TrimSpace(request.ControlLabel) == "" {
		return Scorecard{}, fmt.Errorf("experiment scorecard: control label is required")
	}
	if request.ExpectedCellsPerVariant <= 0 {
		return Scorecard{}, fmt.Errorf("experiment scorecard: expected cells per variant must be positive")
	}
	if request.ExpectedCellsPerVariant > MaxMaterializedCells {
		return Scorecard{}, fmt.Errorf("experiment scorecard: expected cells exceed the materialized cell limit of %d", MaxMaterializedCells)
	}
	if len(request.Cells) > MaxMaterializedCells {
		return Scorecard{}, fmt.Errorf("experiment scorecard: cells exceed the materialized cell limit of %d", MaxMaterializedCells)
	}
	accumulators := make(map[string]*variantAccumulator)
	identities := make(map[cellIdentity]struct{}, len(request.Cells))
	globalDirections := make(map[string]string)
	globalDefinitions := make(map[string]metricDefinition)
	matrixBudgetSkipped := false
	for index, cell := range request.Cells {
		if len(cell.Measurements) > MaxMeasurementsPerCell {
			return Scorecard{}, fmt.Errorf(
				"experiment scorecard: cells[%d] exceeds the measurement limit of %d",
				index, MaxMeasurementsPerCell,
			)
		}
		if err := validateScorecardCell(cell); err != nil {
			return Scorecard{}, fmt.Errorf("experiment scorecard: cells[%d]: %w", index, err)
		}
		identity := cellIdentity{variant: cell.Variant, fixture: cell.Fixture, repetition: cell.Repetition}
		if _, found := identities[identity]; found {
			return Scorecard{}, fmt.Errorf("experiment scorecard: duplicate cell for variant %q fixture %q repetition %d", cell.Variant, cell.Fixture, cell.Repetition)
		}
		identities[identity] = struct{}{}
		accumulator := accumulators[cell.Variant]
		if accumulator == nil {
			if len(accumulators) >= MaxVariants {
				return Scorecard{}, fmt.Errorf("experiment scorecard: variants exceed the limit of %d", MaxVariants)
			}
			accumulator = &variantAccumulator{
				metricValues: make(map[string][]float64), directions: make(map[string]string),
				metricByCell: make(map[cellKey]map[string]float64), negativePass: true,
			}
			accumulators[cell.Variant] = accumulator
		}
		accumulator.cells = append(accumulator.cells, cloneCellResult(cell))
		if cell.Status != CellSkippedBudget {
			accumulator.cost = append(accumulator.cost, cell.CostUSD)
			accumulator.latency = append(accumulator.latency, cell.Latency.Seconds())
			accumulator.tokens = append(accumulator.tokens, float64(cell.InputTokens+cell.OutputTokens))
			accumulator.interventions = append(accumulator.interventions, float64(cell.HumanInterventions))
		}
		if cell.Status == CellValidMeasurement || cell.Status == CellNegativeControlFailure {
			accumulator.valid++
		}
		if cell.Status == CellCandidatePlatformFailure || cell.Status == CellEvaluatorFailure {
			accumulator.platform++
		}
		if cell.Status == CellSkippedBudget {
			accumulator.budgetSkipped++
			matrixBudgetSkipped = true
			continue
		}
		if cell.Role == FixtureNegativeControl {
			accumulator.negativeSeen = true
			if cell.Status != CellValidMeasurement || !cell.NegativeControlPassed {
				accumulator.negativePass = false
			}
			continue
		}
		if cell.Status != CellValidMeasurement {
			continue
		}
		key := cellKey{fixture: cell.Fixture, repetition: cell.Repetition}
		values := make(map[string]float64, len(cell.Measurements))
		for _, measurement := range cell.Measurements {
			if _, found := globalDirections[measurement.ID]; !found && len(globalDirections) >= MaxMeasurementsPerCell {
				return Scorecard{}, fmt.Errorf(
					"experiment scorecard: distinct metrics exceed the measurement limit of %d",
					MaxMeasurementsPerCell,
				)
			}
			definition := definitionOf(measurement)
			if prior, found := globalDefinitions[measurement.ID]; found && prior != definition {
				switch {
				case prior.direction != definition.direction:
					return Scorecard{}, fmt.Errorf("experiment scorecard: metric %q has inconsistent direction %q and %q", measurement.ID, prior.direction, definition.direction)
				case prior.unit != definition.unit:
					return Scorecard{}, fmt.Errorf("experiment scorecard: metric %q has inconsistent unit %q and %q", measurement.ID, prior.unit, definition.unit)
				default:
					return Scorecard{}, fmt.Errorf("experiment scorecard: metric %q has inconsistent bounds or target", measurement.ID)
				}
			}
			globalDefinitions[measurement.ID] = definition
			globalDirections[measurement.ID] = measurement.Direction
			accumulator.directions[measurement.ID] = measurement.Direction
			accumulator.metricValues[measurement.ID] = append(accumulator.metricValues[measurement.ID], measurement.Value)
			values[measurement.ID] = measurement.Value
		}
		accumulator.metricByCell[key] = values
	}
	control := accumulators[request.ControlLabel]
	if control == nil {
		return Scorecard{}, fmt.Errorf("experiment scorecard: control variant %q has no cells", request.ControlLabel)
	}

	scorecard := Scorecard{
		ExperimentID: request.ExperimentID, Control: request.ControlLabel,
		Variants:    make(map[string]VariantScore, len(accumulators)),
		Comparisons: make(map[string]map[string]PairedComparison),
		Cells:       cloneCellResults(request.Cells),
	}
	for label, accumulator := range accumulators {
		if len(accumulator.cells) > request.ExpectedCellsPerVariant {
			return Scorecard{}, fmt.Errorf("experiment scorecard: variant %q has %d observed cells, exceeding expected matrix size %d", label, len(accumulator.cells), request.ExpectedCellsPerVariant)
		}
		metrics := make(map[string]Distribution, len(accumulator.metricValues))
		for name, values := range accumulator.metricValues {
			metrics[name] = distribution(values)
		}
		negativePass := true
		if accumulator.negativeSeen {
			negativePass = accumulator.negativePass
		}
		scorecard.Variants[label] = VariantScore{
			ExpectedCells: request.ExpectedCellsPerVariant, ObservedCells: len(accumulator.cells), ValidCells: accumulator.valid,
			ValidCoverage:  float64(accumulator.valid) / float64(request.ExpectedCellsPerVariant),
			PlatformErrors: accumulator.platform, PlatformErrorRate: float64(accumulator.platform) / float64(request.ExpectedCellsPerVariant),
			BudgetSkipped:        accumulator.budgetSkipped,
			NegativeControlsPass: negativePass, Metrics: metrics, MetricDirections: cloneStringMap(accumulator.directions),
			CostUSD: distribution(accumulator.cost), LatencySeconds: distribution(accumulator.latency),
			Tokens: distribution(accumulator.tokens), HumanInterventions: distribution(accumulator.interventions),
		}
	}
	for label, accumulator := range accumulators {
		if label == request.ControlLabel {
			continue
		}
		comparisons := make(map[string]PairedComparison)
		metrics := make(map[string]struct{}, len(control.metricValues)+len(accumulator.metricValues))
		for metric := range control.metricValues {
			metrics[metric] = struct{}{}
		}
		for metric := range accumulator.metricValues {
			metrics[metric] = struct{}{}
		}
		for metric := range metrics {
			comparisons[metric] = comparePaired(request, label, metric, globalDefinitions[metric], control, accumulator, scorecard.Variants, matrixBudgetSkipped)
		}
		scorecard.Comparisons[label] = comparisons
	}
	return scorecard, nil
}

func comparePaired(
	request ScorecardRequest,
	variantLabel, metric string,
	definition metricDefinition,
	control, variant *variantAccumulator,
	scores map[string]VariantScore,
	matrixBudgetSkipped bool,
) PairedComparison {
	keys := make([]cellKey, 0)
	for key, controlMetrics := range control.metricByCell {
		if _, found := controlMetrics[metric]; !found {
			continue
		}
		if variantMetrics, found := variant.metricByCell[key]; found {
			if _, found := variantMetrics[metric]; found {
				keys = append(keys, key)
			}
		}
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].fixture != keys[right].fixture {
			return keys[left].fixture < keys[right].fixture
		}
		return keys[left].repetition < keys[right].repetition
	})
	deltas := make([]float64, 0, len(keys))
	comparison := PairedComparison{
		Variant: variantLabel, Control: request.ControlLabel, Metric: metric, Direction: definition.direction,
		ControlCount: len(control.metricValues[metric]), VariantCount: len(variant.metricValues[metric]),
		Recommendation: RecommendationInsufficient,
	}
	for _, key := range keys {
		controlValue := control.metricByCell[key][metric]
		variantValue := variant.metricByCell[key][metric]
		delta := variantValue - controlValue
		switch definition.direction {
		case "lower-is-better":
			delta = controlValue - variantValue
		case "target":
			// Positive always means the variant improved over control. For a
			// target-directed metric that is a reduction in absolute error,
			// not an increase in the raw measurement.
			delta = math.Abs(controlValue-definition.target) - math.Abs(variantValue-definition.target)
		}
		deltas = append(deltas, delta)
		switch {
		case delta > 0:
			comparison.Wins++
		case delta < 0:
			comparison.Losses++
		default:
			comparison.Ties++
		}
	}
	comparison.PairedCount = len(deltas)
	comparison.MeanDelta = mean(deltas)
	comparison.ConfidenceLow, comparison.ConfidenceHigh = pairedBootstrapInterval(request.ExperimentID, metric, variantLabel, deltas)
	variantScore := scores[variantLabel]
	controlScore := scores[request.ControlLabel]
	if comparison.PairedCount < 5 {
		comparison.FailedConditions = append(comparison.FailedConditions, "at least five valid paired repetitions are required")
	}
	if comparison.ControlCount == 0 || comparison.VariantCount == 0 {
		comparison.FailedConditions = append(comparison.FailedConditions, "metric is missing from the variant or control measurements")
	}
	if matrixBudgetSkipped {
		comparison.FailedConditions = append(comparison.FailedConditions, "budget-skipped cells make the experiment matrix incomplete")
	}
	if variantScore.ValidCoverage < 0.80 || controlScore.ValidCoverage < 0.80 {
		comparison.FailedConditions = append(comparison.FailedConditions, "valid coverage is below 80% for the variant or control")
	}
	if variantScore.PlatformErrorRate > controlScore.PlatformErrorRate+0.05+1e-12 {
		comparison.FailedConditions = append(comparison.FailedConditions, "variant platform-error rate is more than five percentage points worse than control")
	}
	if !variantScore.NegativeControlsPass || !controlScore.NegativeControlsPass {
		comparison.FailedConditions = append(comparison.FailedConditions, "variant or control negative controls did not all pass")
	}
	if comparison.ConfidenceLow <= 0 && comparison.ConfidenceHigh >= 0 {
		comparison.FailedConditions = append(comparison.FailedConditions, "95% paired bootstrap interval includes zero")
	}
	if len(comparison.FailedConditions) == 0 {
		comparison.Recommendation = RecommendationWinner
		if comparison.ConfidenceLow > 0 {
			comparison.Winner = variantLabel
		} else {
			comparison.Winner = request.ControlLabel
		}
	}
	return comparison
}

func validateScorecardCell(cell CellResult) error {
	if err := cell.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(cell.Variant) == "" || strings.TrimSpace(cell.Fixture) == "" {
		return fmt.Errorf("variant and fixture are required")
	}
	if cell.Role != FixtureNormal && cell.Role != FixtureNegativeControl {
		return fmt.Errorf("fixture role is invalid")
	}
	if cell.Repetition <= 0 {
		return fmt.Errorf("repetition must be positive")
	}
	if !cell.Status.terminal() {
		return fmt.Errorf("cell status %q is not terminal", cell.Status)
	}
	if !finiteNonnegative(cell.CostUSD) || cell.Latency < 0 || cell.InputTokens < 0 || cell.OutputTokens < 0 || cell.HumanInterventions < 0 {
		return fmt.Errorf("telemetry values must be finite and nonnegative")
	}
	if cell.Status == CellValidMeasurement && len(cell.Measurements) == 0 {
		return fmt.Errorf("valid measurement cell has no measurements")
	}
	seen := make(map[string]struct{}, len(cell.Measurements))
	for _, measurement := range cell.Measurements {
		if strings.TrimSpace(measurement.ID) == "" || strings.TrimSpace(measurement.Unit) == "" ||
			math.IsNaN(measurement.Value) || math.IsInf(measurement.Value, 0) ||
			(measurement.Direction != "higher-is-better" && measurement.Direction != "lower-is-better" && measurement.Direction != "target") {
			return fmt.Errorf("measurement %q is invalid", measurement.ID)
		}
		if err := validateMetricDefinition(measurement); err != nil {
			return fmt.Errorf("measurement %q is invalid: %w", measurement.ID, err)
		}
		if _, found := seen[measurement.ID]; found {
			return fmt.Errorf("duplicate measurement %q", measurement.ID)
		}
		seen[measurement.ID] = struct{}{}
	}
	return nil
}

func definitionOf(measurement contracts.Measurement) metricDefinition {
	definition := metricDefinition{unit: measurement.Unit, direction: measurement.Direction}
	if measurement.Minimum != nil {
		definition.minimumSet, definition.minimum = true, *measurement.Minimum
	}
	if measurement.Maximum != nil {
		definition.maximumSet, definition.maximum = true, *measurement.Maximum
	}
	if measurement.Target != nil {
		definition.targetSet, definition.target = true, *measurement.Target
	}
	return definition
}

func validateMetricDefinition(measurement contracts.Measurement) error {
	if (measurement.Minimum == nil) != (measurement.Maximum == nil) {
		return fmt.Errorf("minimum and maximum must be declared together")
	}
	if measurement.Minimum != nil {
		if math.IsNaN(*measurement.Minimum) || math.IsInf(*measurement.Minimum, 0) ||
			math.IsNaN(*measurement.Maximum) || math.IsInf(*measurement.Maximum, 0) ||
			*measurement.Minimum > *measurement.Maximum ||
			measurement.Value < *measurement.Minimum || measurement.Value > *measurement.Maximum {
			return fmt.Errorf("bounds are invalid")
		}
	}
	if measurement.Direction == "target" {
		if measurement.Target == nil || math.IsNaN(*measurement.Target) || math.IsInf(*measurement.Target, 0) {
			return fmt.Errorf("target is required")
		}
	} else if measurement.Target != nil {
		return fmt.Errorf("target is valid only for target direction")
	}
	return nil
}

func distribution(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	average := mean(ordered)
	variance := 0.0
	for _, value := range ordered {
		difference := value - average
		variance += difference * difference
	}
	variance /= float64(len(ordered))
	return Distribution{
		Count: len(ordered), Mean: average, Median: percentile(ordered, 0.5), Min: ordered[0], Max: ordered[len(ordered)-1],
		StdDev: math.Sqrt(variance), P05: percentile(ordered, 0.05), P25: percentile(ordered, 0.25),
		P75: percentile(ordered, 0.75), P95: percentile(ordered, 0.95),
	}
}

func percentile(ordered []float64, quantile float64) float64 {
	if len(ordered) == 0 {
		return 0
	}
	position := quantile * float64(len(ordered)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return ordered[lower]
	}
	weight := position - float64(lower)
	return ordered[lower]*(1-weight) + ordered[upper]*weight
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func pairedBootstrapInterval(experimentID ID, metric, variant string, deltas []float64) (float64, float64) {
	if len(deltas) == 0 {
		return 0, 0
	}
	seedInput := experimentID.String() + "\x00" + metric + "\x00" + variant
	digest := sha256.Sum256([]byte(seedInput))
	random := rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(digest[:8]))))
	// Admission bounds cells, variants, and distinct metrics. Keeping the
	// deterministic bootstrap itself fixed and modest makes the complete
	// worst-case scorecard CPU budget bounded as well.
	const resamples = 2_000
	means := make([]float64, resamples)
	for sample := range means {
		total := 0.0
		for range deltas {
			total += deltas[random.Intn(len(deltas))]
		}
		means[sample] = total / float64(len(deltas))
	}
	sort.Float64s(means)
	return percentile(means, 0.025), percentile(means, 0.975)
}

func cloneCellResult(cell CellResult) CellResult {
	cell.Measurements = append([]contracts.Measurement(nil), cell.Measurements...)
	return cell
}

func cloneCellResults(cells []CellResult) []CellResult {
	result := make([]CellResult, len(cells))
	for index, cell := range cells {
		result[index] = cloneCellResult(cell)
	}
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
