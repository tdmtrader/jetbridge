package experiment_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/functions/judge"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
	"github.com/concourse/concourse/atc"
)

const (
	candidateSeedDirectory = "../workflow/seeds/code-review-v3"
	evaluatorSeedDirectory = "../workflow/seeds/measure-review-v3"
)

// TestShippedEvaluatorSeedAdmitsAgainstTheShippedCandidateSeed closes the loop
// the experiment machinery has always described but never had a shipped part
// for: a real seed compiles to a signature that satisfies evaluator admission
// against a real candidate seed, and its rendered plan passes the static budget
// and effect-free proofs that `fly agent experiments start` runs.
//
// Before this seed existed the checks below could only ever be exercised against
// signatures a test typed out by hand, so nothing proved that anything shippable
// could satisfy them.
func TestShippedEvaluatorSeedAdmitsAgainstTheShippedCandidateSeed(t *testing.T) {
	candidate := compileSeedSignature(t, candidateSeedDirectory, "code-review")
	evaluator := compileSeedSignature(t, evaluatorSeedDirectory, "measure-review")

	// The evaluator's only input is the candidate's only output, by exact type.
	// validateEvaluator compares those types itself, so a drift on either side
	// fails here rather than at bind time in a running experiment.
	definition := experiment.Definition{
		Name: "code-review-prompts", State: experiment.StateDraft, Repetitions: 5,
		Signature: candidate,
		Variants: []experiment.Variant{
			{
				Label: "control", Control: true, SignatureHash: signatureHash(t, candidate),
				Target: experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: "code-review", DefinitionID: 11, Version: 1},
			},
			{
				Label: "reworded-prompt", SignatureHash: signatureHash(t, candidate),
				Target: experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: "code-review", DefinitionID: 12, Version: 2},
			},
		},
		Fixtures: []experiment.Fixture{
			{Label: "clean", Role: experiment.FixtureNormal, Inputs: map[string]snapshot.SnapshotID{"before": 101, "after": 102}},
			{
				Label: "planted-defect", Role: experiment.FixtureNegativeControl,
				Inputs: map[string]snapshot.SnapshotID{"before": 103, "after": 104},
				// The negative control asserts on a metric the shipped function
				// really emits. A typo here is the classic way a control silently
				// passes forever, so the same id is derived for real below.
				Assertions: []experiment.Assertion{{
					Metric: "review.findings.blocking", Comparator: experiment.ComparatorGTE, Thresholds: []float64{1},
				}},
			},
		},
		Evaluator: experiment.Evaluator{
			Target:    experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: "measure-review", DefinitionID: 21, Version: 1},
			Signature: evaluator,
			Mappings: []experiment.EvaluatorMapping{
				{EvaluatorPort: "candidate", SourceDirection: experiment.SourceCandidateOutput, SourcePort: "review"},
			},
			MeasurementsPort: "measurements",
		},
	}
	if err := definition.Validate(); err != nil {
		t.Fatalf("shipped seeds do not form an admissible experiment: %v", err)
	}
	if err := definition.ValidateStart(); err != nil {
		t.Fatalf("shipped seeds are not startable: %v", err)
	}

	// A claimed cell carries the frozen evaluator, so the same compatibility is
	// re-proven at execution time against durable rows rather than a draft.
	cell := experiment.EvaluationCell{
		ID: 1, ExperimentID: 2, TeamID: 3, TeamName: "main", CreatedBy: "operator",
		CandidateWorkflowRunID: snapshot.WorkflowRunID(9),
		CandidateSignature:     candidate,
		FixtureInputs:          map[string]snapshot.SnapshotID{"before": 101, "after": 102},
		Evaluator:              definition.Evaluator,
		Role:                   experiment.FixtureNormal,
	}
	cell.Evaluator.TargetConfigHash = strings.Repeat("a", 64)
	if err := cell.Validate(); err != nil {
		t.Fatalf("evaluation cell built from the shipped seeds is invalid: %v", err)
	}
}

// TestShippedEvaluatorSeedNeedsNoBudgetEnvelopeOfItsOwn proves the property that
// makes a deterministic evaluator worth shipping: the measuring half of every
// cell is free.
//
// ValidateExecutionBudgetsForGlobalCap refuses a zero-dollar experiment whose
// configs can start an agent while the deployment daily cap is on, and refuses
// any publish_snapshot outright. The evaluator passing both with no envelope at
// all is what lets an operator run a matrix whose whole cost is the candidate's.
func TestShippedEvaluatorSeedNeedsNoBudgetEnvelopeOfItsOwn(t *testing.T) {
	candidateConfig := renderSeed(t, candidateSeedDirectory, "code-review")
	evaluatorConfig := renderSeed(t, evaluatorSeedDirectory, "measure-review")

	if err := experiment.ValidateExecutionBudgetsForGlobalCap(
		experiment.Budget{}, 0, nil, evaluatorConfig, true,
	); err != nil {
		t.Fatalf("evaluator seed is not admissible without a dollar envelope: %v", err)
	}

	// The candidate seed declares a 5 USD agent slice. With a 5 USD per-cell
	// reservation the combined candidate+evaluator slice must still fit, which it
	// only does because the evaluator contributes exactly nothing.
	if err := experiment.ValidateExecutionBudgets(
		experiment.Budget{PerCellUSD: 5}, 10, []atc.Config{candidateConfig}, evaluatorConfig,
	); err != nil {
		t.Fatalf("evaluator seed consumed part of the candidate's cell reservation: %v", err)
	}

	// Sanity: the candidate really does carry a slice, so the assertion above is
	// not passing because both sides are empty.
	if err := experiment.ValidateExecutionBudgetsForGlobalCap(
		experiment.Budget{}, 0, []atc.Config{candidateConfig}, evaluatorConfig, true,
	); err == nil {
		t.Fatal("an agent-bearing candidate was admitted with no dollar envelope; this test proves nothing")
	}
}

// TestShippedEvaluatorMeasurementsReachTheScorecard runs the real function over
// real review records, re-reads them through the exact gate the web node uses,
// and feeds the result into the scorecard.
//
// Each hop is a place the loop has silently broken before: a metric vocabulary
// the scorecard rejects as inconsistent, a record the read-time gate refuses, or
// a negative-control assertion naming a metric nothing emits.
func TestShippedEvaluatorMeasurementsReachTheScorecard(t *testing.T) {
	blocking := contracts.ReviewBody{
		Conclusion: "changes-required",
		Summary:    "two blocking defects",
		Findings: []contracts.Finding{
			{
				ID: "F-1", Severity: "critical", Blocking: true, Category: "correctness",
				Title: "unchecked error", Description: "the error is discarded",
				Evidence: []contracts.Anchor{reviewAnchor("main.go", 10)},
			},
			{
				ID: "F-2", Severity: "high", Blocking: true, Category: "security",
				Title: "unvalidated input", Description: "the path is not cleaned",
				Evidence: []contracts.Anchor{reviewAnchor("main.go", 20)},
			},
		},
	}
	clean := contracts.ReviewBody{Conclusion: "accept", Summary: "no defects found"}

	control := measurementsFor(t, blocking)
	variant := measurementsFor(t, clean)
	if len(control.Metrics) > experiment.MaxMeasurementsPerCell {
		t.Fatalf("the shipped vocabulary emits %d metrics, beyond the admitted bound of %d",
			len(control.Metrics), experiment.MaxMeasurementsPerCell)
	}

	scorecard, err := experiment.BuildScorecard(experiment.ScorecardRequest{
		ExperimentID: 7, ControlLabel: "control", ExpectedCellsPerVariant: 1,
		Cells: []experiment.CellResult{
			{
				ID: 1, Variant: "control", Fixture: "planted-defect", Role: experiment.FixtureNormal,
				Repetition: 1, Status: experiment.CellValidMeasurement, Measurements: control.Metrics,
			},
			{
				ID: 2, Variant: "reworded-prompt", Fixture: "planted-defect", Role: experiment.FixtureNormal,
				Repetition: 1, Status: experiment.CellValidMeasurement, Measurements: variant.Metrics,
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildScorecard over shipped measurements: %v", err)
	}

	controlScore := scorecard.Variants["control"]
	if controlScore.Metrics["review.findings.blocking"].Mean != 2 {
		t.Fatalf("control blocking distribution = %+v", controlScore.Metrics["review.findings.blocking"])
	}
	if controlScore.MetricDirections["review.findings.blocking"] != "lower-is-better" {
		t.Fatalf("blocking direction = %q", controlScore.MetricDirections["review.findings.blocking"])
	}
	if scorecard.Variants["reworded-prompt"].Metrics["review.conclusion.accept"].Mean != 1 {
		t.Fatalf("variant accept indicator = %+v", scorecard.Variants["reworded-prompt"].Metrics["review.conclusion.accept"])
	}
	// A lower-is-better metric is oriented by the scorecard, so the variant that
	// reports fewer blocking findings shows a positive delta.
	comparison := scorecard.Comparisons["reworded-prompt"]["review.findings.blocking"]
	if comparison.MeanDelta != 2 || comparison.Wins != 1 {
		t.Fatalf("paired comparison = %+v", comparison)
	}

	// The negative-control assertion the definition above declares must actually
	// bind to a metric this function emits, in both directions.
	assertion := experiment.Assertion{
		Metric: "review.findings.blocking", Comparator: experiment.ComparatorGTE, Thresholds: []float64{1},
	}
	if !assertion.Evaluate(metricValue(t, control, "review.findings.blocking")) {
		t.Fatal("negative-control assertion failed against a review with two blocking findings")
	}
	if assertion.Evaluate(metricValue(t, variant, "review.findings.blocking")) {
		t.Fatal("negative-control assertion passed against a review with no blocking findings")
	}
}

// measurementsFor runs the shipped function over one review and reads the emitted
// bytes back through contracts.ReadSealedMeasurementsRecord — the exact call
// atc/atccmd's experiment measurements reader makes before a cell's numbers are
// allowed into a scorecard.
func measurementsFor(t *testing.T, review contracts.ReviewBody) contracts.MeasurementsDocument {
	t.Helper()
	candidateRoot := t.TempDir()
	writeReviewRecord(t, candidateRoot, review)

	schema, found := contracts.SchemaDigestFor(snapshot.TypeRef("measurements/v1"))
	if !found {
		t.Fatal("measurements/v1 has no compiled schema digest")
	}
	record, err := judge.Measure(context.Background(), judge.Request{
		Candidate: snapshot.SnapshotRef{
			ID: 5, Type: snapshot.TypeRef("review/v1"),
			Digest: snapshot.Digest("sha256:" + strings.Repeat("b", 64)),
		},
		CandidateInput: "candidate",
		CandidateRoot:  candidateRoot,
		MeasurementsAuthority: judge.RecordAuthority{
			Type: snapshot.TypeRef("measurements/v1"), Schema: schema,
		},
	})
	if err != nil {
		t.Fatalf("judge.Measure: %v", err)
	}

	outputRoot := t.TempDir()
	if err := judge.WriteMeasurements(context.Background(), outputRoot, record); err != nil {
		t.Fatalf("judge.WriteMeasurements: %v", err)
	}
	sealed, err := os.OpenRoot(outputRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer sealed.Close()
	stored, err := contracts.ReadSealedMeasurementsRecord(context.Background(), sealed)
	if err != nil {
		t.Fatalf("emitted measurements are not readable by the platform: %v", err)
	}
	return stored.Body
}

func metricValue(t *testing.T, document contracts.MeasurementsDocument, id string) float64 {
	t.Helper()
	for _, metric := range document.Metrics {
		if metric.ID == id {
			return metric.Value
		}
	}
	t.Fatalf("measurements do not contain %q", id)
	return 0
}

func writeReviewRecord(t *testing.T, root string, body contracts.ReviewBody) {
	t.Helper()
	reviewed := snapshot.SnapshotRef{
		ID: 4, Type: snapshot.TypeRef("repository/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64)),
	}
	record, err := contracts.NewRecord(
		snapshot.TypeRef("review/v1"),
		[]contracts.Subject{contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "after", reviewed)},
		body,
	)
	if err != nil {
		t.Fatalf("NewRecord: %v", err)
	}
	if err := record.Body.Validate(record.Subjects); err != nil {
		t.Fatalf("review fixture is invalid: %v", err)
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "record.json"), append(payload, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func reviewAnchor(path string, line int) contracts.Anchor {
	return contracts.Anchor{
		Subject: "primary",
		Locator: contracts.Locator{Kind: "file-lines", Path: path, Start: &line, End: &line},
	}
}

func compileSeedSignature(t *testing.T, directory, name string) workflow.PublicSignature {
	t.Helper()
	signature, err := compileSeed(t, directory, name).Compiled.PublicSignature()
	if err != nil {
		t.Fatalf("PublicSignature(%q): %v", directory, err)
	}
	return signature
}

func renderSeed(t *testing.T, directory, name string) atc.Config {
	t.Helper()
	target, err := workflow.FullFunctionTarget(*compileSeed(t, directory, name))
	if err != nil {
		t.Fatalf("FullFunctionTarget(%q): %v", directory, err)
	}
	rendered, err := workflow.RenderFunction(target)
	if err != nil {
		t.Fatalf("RenderFunction(%q): %v", directory, err)
	}
	return rendered.Config
}

func compileSeed(t *testing.T, directory, name string) *workflow.Definition {
	t.Helper()
	manifest, err := workflow.ManifestFromDir(directory)
	if err != nil {
		t.Fatalf("ManifestFromDir(%q): %v", directory, err)
	}
	definition, err := workflowtest.NewMemoryStore().ImportManifest(name, manifest, "experiment-test")
	if err != nil {
		t.Fatalf("ImportManifest(%q): %v", directory, err)
	}
	return definition
}

func signatureHash(t *testing.T, signature workflow.PublicSignature) string {
	t.Helper()
	hash, err := experiment.HashSignature(signature)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
