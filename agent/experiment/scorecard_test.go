package experiment_test

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestBuildScorecardComputesDistributionsAndPairedRecommendation(t *testing.T) {
	cells := pairedCells("higher", 5, 2)
	scorecard, err := experiment.BuildScorecard(experiment.ScorecardRequest{
		ExperimentID: 77, ControlLabel: "control", ExpectedCellsPerVariant: 15, Cells: cells,
	})
	if err != nil {
		t.Fatal(err)
	}
	control := scorecard.Variants["control"]
	if control.ValidCoverage != 1 || control.PlatformErrorRate != 0 || !control.NegativeControlsPass {
		t.Fatalf("control reliability = %+v", control)
	}
	distribution := control.Metrics["quality"]
	if distribution.Count != 10 || distribution.Mean != 5.5 || distribution.Median != 5.5 ||
		distribution.Min != 1 || distribution.Max != 10 || math.Abs(distribution.StdDev-math.Sqrt(8.25)) > 1e-12 {
		t.Fatalf("quality distribution = %+v", distribution)
	}
	if distribution.P05 >= distribution.P25 || distribution.P25 >= distribution.P75 || distribution.P75 >= distribution.P95 {
		t.Fatalf("percentiles are not ordered: %+v", distribution)
	}
	if control.CostUSD.Count != 15 || control.LatencySeconds.Count != 15 || control.Tokens.Count != 15 || control.HumanInterventions.Count != 15 {
		t.Fatalf("operational distributions = cost:%+v latency:%+v tokens:%+v interventions:%+v", control.CostUSD, control.LatencySeconds, control.Tokens, control.HumanInterventions)
	}
	comparison := scorecard.Comparisons["candidate"]["quality"]
	if comparison.PairedCount != 10 || comparison.Wins != 10 || comparison.Ties != 0 || comparison.Losses != 0 ||
		comparison.MeanDelta != 2 || comparison.ConfidenceLow != 2 || comparison.ConfidenceHigh != 2 ||
		comparison.Winner != "candidate" || comparison.Recommendation != experiment.RecommendationWinner || len(comparison.FailedConditions) != 0 {
		t.Fatalf("comparison = %+v", comparison)
	}
	if len(scorecard.Cells) != len(cells) {
		t.Fatalf("raw cell count = %d, want %d", len(scorecard.Cells), len(cells))
	}
}

func TestBuildScorecardOrientsLowerIsBetterMetrics(t *testing.T) {
	cells := pairedCells("lower", 5, -2)
	scorecard, err := experiment.BuildScorecard(experiment.ScorecardRequest{
		ExperimentID: 78, ControlLabel: "control", ExpectedCellsPerVariant: 15, Cells: cells,
	})
	if err != nil {
		t.Fatal(err)
	}
	comparison := scorecard.Comparisons["candidate"]["quality"]
	if comparison.MeanDelta != 2 || comparison.Wins != 10 || comparison.Winner != "candidate" {
		t.Fatalf("lower-is-better comparison = %+v", comparison)
	}
}

func TestBuildScorecardReportsInsufficientEvidenceConditions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*experiment.ScorecardRequest)
		want   string
	}{
		{name: "fewer than five pairs", mutate: func(request *experiment.ScorecardRequest) {
			filtered := request.Cells[:0]
			for _, cell := range request.Cells {
				if cell.Role == experiment.FixtureNegativeControl || cell.Repetition <= 2 {
					filtered = append(filtered, cell)
				}
			}
			request.Cells = filtered
		}, want: "at least five valid paired repetitions"},
		{name: "coverage below eighty percent", mutate: func(request *experiment.ScorecardRequest) {
			request.ExpectedCellsPerVariant = 100
		}, want: "valid coverage is below 80%"},
		{name: "platform error regression", mutate: func(request *experiment.ScorecardRequest) {
			for index := range request.Cells {
				if request.Cells[index].Variant == "candidate" && request.Cells[index].Role == experiment.FixtureNormal && request.Cells[index].Repetition <= 2 {
					request.Cells[index].Status = experiment.CellCandidatePlatformFailure
					request.Cells[index].Measurements = nil
				}
			}
		}, want: "platform-error rate"},
		{name: "negative control failure", mutate: func(request *experiment.ScorecardRequest) {
			for index := range request.Cells {
				if request.Cells[index].Variant == "candidate" && request.Cells[index].Role == experiment.FixtureNegativeControl {
					request.Cells[index].NegativeControlPassed = false
					request.Cells[index].Status = experiment.CellNegativeControlFailure
					break
				}
			}
		}, want: "negative controls did not all pass"},
		{name: "budget-skipped matrix", mutate: func(request *experiment.ScorecardRequest) {
			for index := range request.Cells {
				if request.Cells[index].Variant == "candidate" && request.Cells[index].Role == experiment.FixtureNormal {
					request.Cells[index].Status = experiment.CellSkippedBudget
					request.Cells[index].Measurements = nil
					break
				}
			}
		}, want: "budget-skipped cells make the experiment matrix incomplete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := experiment.ScorecardRequest{ExperimentID: 79, ControlLabel: "control", ExpectedCellsPerVariant: 15, Cells: pairedCells("higher", 5, 2)}
			test.mutate(&request)
			scorecard, err := experiment.BuildScorecard(request)
			if err != nil {
				t.Fatal(err)
			}
			comparison := scorecard.Comparisons["candidate"]["quality"]
			if comparison.Recommendation != experiment.RecommendationInsufficient || comparison.Winner != "" || !containsString(comparison.FailedConditions, test.want) {
				t.Fatalf("comparison = %+v, want condition %q", comparison, test.want)
			}
		})
	}
}

func TestBuildScorecardReportsBudgetSkipsWithoutCallingThemPlatformErrors(t *testing.T) {
	cells := pairedCells("higher", 5, 2)
	for index := range cells {
		if cells[index].Variant == "candidate" && cells[index].Role == experiment.FixtureNormal {
			cells[index].Status = experiment.CellSkippedBudget
			cells[index].Measurements = nil
			break
		}
	}
	scorecard, err := experiment.BuildScorecard(experiment.ScorecardRequest{
		ExperimentID: 83, ControlLabel: "control", ExpectedCellsPerVariant: 15, Cells: cells,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := scorecard.Variants["candidate"]
	if candidate.BudgetSkipped != 1 || candidate.PlatformErrors != 0 {
		t.Fatalf("candidate reliability = %+v, want one budget skip and no platform errors", candidate)
	}
	comparison := scorecard.Comparisons["candidate"]["quality"]
	if comparison.Recommendation != experiment.RecommendationInsufficient || comparison.Winner != "" {
		t.Fatalf("comparison = %+v, want no winner from an incomplete budget matrix", comparison)
	}
}

func TestBuildScorecardPairsOnlyMatchingFixtureAndRepetitionAndIsDeterministic(t *testing.T) {
	cells := pairedCells("higher", 5, 1)
	for index := range cells {
		if cells[index].Variant == "candidate" && cells[index].Fixture == "fixture-b" && cells[index].Repetition == 5 {
			cells[index].Status = experiment.CellEvaluatorFailure
			cells[index].Measurements = nil
		}
	}
	request := experiment.ScorecardRequest{ExperimentID: 80, ControlLabel: "control", ExpectedCellsPerVariant: 15, Cells: cells}
	first, err := experiment.BuildScorecard(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := experiment.BuildScorecard(request)
	if err != nil {
		t.Fatal(err)
	}
	comparison := first.Comparisons["candidate"]["quality"]
	if comparison.PairedCount != 9 {
		t.Fatalf("paired count = %d, want 9", comparison.PairedCount)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same immutable cells produced nondeterministic scorecards")
	}
}

func TestBuildScorecardEmitsUnionMetricsWhenOneSideHasNoValues(t *testing.T) {
	cells := pairedCells("higher", 5, 1)
	for index := range cells {
		if cells[index].Variant == "control" && cells[index].Role == experiment.FixtureNormal {
			cells[index].Measurements = append(cells[index].Measurements, contracts.Measurement{
				Name: "control-only", Value: 1, Unit: "score", Direction: "higher",
			})
		}
	}
	scorecard, err := experiment.BuildScorecard(experiment.ScorecardRequest{
		ExperimentID: 82, ControlLabel: "control", ExpectedCellsPerVariant: 15, Cells: cells,
	})
	if err != nil {
		t.Fatal(err)
	}
	comparison, found := scorecard.Comparisons["candidate"]["control-only"]
	if !found {
		t.Fatal("control-only metric disappeared from the comparison union")
	}
	if comparison.ControlCount != 10 || comparison.VariantCount != 0 || comparison.PairedCount != 0 {
		t.Fatalf("raw metric counts = control:%d variant:%d paired:%d", comparison.ControlCount, comparison.VariantCount, comparison.PairedCount)
	}
	if comparison.Recommendation != experiment.RecommendationInsufficient ||
		!containsString(comparison.FailedConditions, "metric is missing") {
		t.Fatalf("comparison = %+v", comparison)
	}
	if len(scorecard.Cells) != len(cells) {
		t.Fatalf("raw cells = %d, want %d", len(scorecard.Cells), len(cells))
	}
}

func TestBuildScorecardRejectsContradictoryMetricDirectionsAndDuplicateCells(t *testing.T) {
	cells := pairedCells("higher", 5, 1)
	for index := range cells {
		if cells[index].Variant == "candidate" && cells[index].Role == experiment.FixtureNormal {
			cells[index].Measurements[0].Direction = "lower"
			break
		}
	}
	_, err := experiment.BuildScorecard(experiment.ScorecardRequest{ExperimentID: 81, ControlLabel: "control", ExpectedCellsPerVariant: 15, Cells: cells})
	if err == nil || !strings.Contains(err.Error(), "inconsistent direction") {
		t.Fatalf("direction error = %v", err)
	}

	cells = pairedCells("higher", 5, 1)
	cells = append(cells, cells[0])
	_, err = experiment.BuildScorecard(experiment.ScorecardRequest{ExperimentID: 81, ControlLabel: "control", ExpectedCellsPerVariant: 15, Cells: cells})
	if err == nil || !strings.Contains(err.Error(), "duplicate cell") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestBuildScorecardRejectsWorkBeyondAdmittedExperimentCapsBeforeAggregation(t *testing.T) {
	tooManyCells := make([]experiment.CellResult, experiment.MaxMaterializedCells+1)
	_, err := experiment.BuildScorecard(experiment.ScorecardRequest{
		ExperimentID: 81, ControlLabel: "control", ExpectedCellsPerVariant: 1, Cells: tooManyCells,
	})
	if err == nil || !strings.Contains(err.Error(), "materialized cell limit") {
		t.Fatalf("cell bound error = %v", err)
	}

	cell := cellResultWithMeasurements(1, "fixture", 0, experiment.MaxMeasurementsPerCell+1)
	_, err = experiment.BuildScorecard(experiment.ScorecardRequest{
		ExperimentID: 81, ControlLabel: "control", ExpectedCellsPerVariant: 1, Cells: []experiment.CellResult{cell},
	})
	if err == nil || !strings.Contains(err.Error(), "measurement limit") {
		t.Fatalf("measurement bound error = %v", err)
	}

	first := cellResultWithMeasurements(1, "fixture-a", 0, experiment.MaxMeasurementsPerCell)
	second := cellResultWithMeasurements(2, "fixture-b", experiment.MaxMeasurementsPerCell, experiment.MaxMeasurementsPerCell)
	_, err = experiment.BuildScorecard(experiment.ScorecardRequest{
		ExperimentID: 81, ControlLabel: "control", ExpectedCellsPerVariant: 2,
		Cells: []experiment.CellResult{first, second},
	})
	if err == nil || !strings.Contains(err.Error(), "distinct metrics") {
		t.Fatalf("distinct metric bound error = %v", err)
	}
}

func cellResultWithMeasurements(id experiment.CellID, fixture string, offset, count int) experiment.CellResult {
	metrics := make([]contracts.Measurement, count)
	for index := range metrics {
		metrics[index] = contracts.Measurement{
			Name: fmt.Sprintf("metric-%d", offset+index), Value: float64(index), Unit: "score", Direction: "higher",
		}
	}
	return experiment.CellResult{
		ID: id, Variant: "control", Fixture: fixture, Role: experiment.FixtureNormal,
		Repetition: 1, Status: experiment.CellValidMeasurement, Measurements: metrics,
	}
}

func pairedCells(direction string, repetitions int, candidateOffset float64) []experiment.CellResult {
	var cells []experiment.CellResult
	cellID := experiment.CellID(1)
	for _, variant := range []string{"control", "candidate"} {
		for _, fixture := range []string{"fixture-a", "fixture-b"} {
			for repetition := 1; repetition <= repetitions; repetition++ {
				base := float64(repetition)
				if fixture == "fixture-b" {
					base += float64(repetitions)
				}
				value := base
				if variant == "candidate" {
					value += candidateOffset
				}
				cells = append(cells, experiment.CellResult{
					ID: cellID, Variant: variant, Fixture: fixture, Role: experiment.FixtureNormal,
					Repetition: repetition, Status: experiment.CellValidMeasurement,
					Measurements: []contracts.Measurement{{Name: "quality", Value: value, Unit: "score", Direction: direction}},
					CostUSD:      0.10 * base, Latency: time.Duration(base) * time.Second,
					InputTokens: int64(base * 100), OutputTokens: int64(base * 10), HumanInterventions: repetition % 2,
				})
				cellID++
			}
		}
		for repetition := 1; repetition <= repetitions; repetition++ {
			cells = append(cells, experiment.CellResult{
				ID: cellID, Variant: variant, Fixture: "negative", Role: experiment.FixtureNegativeControl,
				Repetition: repetition, Status: experiment.CellValidMeasurement, NegativeControlPassed: true,
				Measurements: []contracts.Measurement{{Name: "quality", Value: 0, Unit: "score", Direction: direction}},
				CostUSD:      0.05, Latency: time.Second, InputTokens: 25, OutputTokens: 5,
			})
			cellID++
		}
	}
	return cells
}

func containsString(values []string, fragment string) bool {
	for _, value := range values {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
