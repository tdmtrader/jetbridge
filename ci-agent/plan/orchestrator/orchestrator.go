package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/concourse/ci-agent/plan"
	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/plan/confidence"
	"github.com/concourse/ci-agent/schema"
)

// Options configures the orchestrator run.
type Options struct {
	InputPath           string
	OutputDir           string
	Adapter             adapter.Adapter
	ConfidenceThreshold float64
	ConfidenceWeights   confidence.ConfidenceWeights
	Timeout             time.Duration
	SpecOpts            adapter.SpecOpts
	PlanOpts            adapter.PlanOpts
}

// Run executes the full planning pipeline.
func Run(ctx context.Context, opts Options) (*schema.Results, error) {
	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	eventsPath := opts.OutputDir + "/events.ndjson"
	eventsFile, err := os.Create(eventsPath)
	if err != nil {
		return nil, fmt.Errorf("create events file: %w", err)
	}
	defer eventsFile.Close()
	ew := schema.NewEventWriter(eventsFile)

	emitEvent := func(eventType schema.EventType, data interface{}) {
		raw, _ := json.Marshal(data)
		ew.Write(schema.Event{
			EventType: eventType,
			Data:      raw,
		})
	}

	emitEvent(schema.EventAgentStart, map[string]string{"agent": "ci-agent-plan"})

	// 1. Parse input
	input, err := plan.ParseInputFile(opts.InputPath)
	if err != nil {
		return errorResult(opts.OutputDir, ew, fmt.Sprintf("input parse error: %v", err))
	}
	emitEvent(schema.EventPlanInputParsed, map[string]string{"title": input.Title})

	// 2. Completeness scoring
	completenessReport := confidence.ScoreCompleteness(input)

	// Abstain if completeness is very low
	if completenessReport.Score < 0.2 {
		emitEvent(schema.EventAgentEnd, map[string]string{"reason": "abstain: input too incomplete"})
		results := &schema.Results{
			SchemaVersion: "1.0",
			Status:        schema.StatusAbstain,
			Confidence:    0.0,
			Summary:       "Input too incomplete to produce a meaningful plan",
			Artifacts:     []schema.Artifact{{Name: "events", Path: "events.ndjson", MediaType: "application/x-ndjson"}},
		}
		WriteResults(opts.OutputDir, results)
		return results, nil
	}

	// 3. Generate spec
	specOutput, err := opts.Adapter.GenerateSpec(ctx, input, opts.SpecOpts)
	if err != nil {
		return errorResult(opts.OutputDir, ew, fmt.Sprintf("spec generation error: %v", err))
	}
	emitEvent(schema.EventPlanSpecGenerated, map[string]string{"status": "ok"})

	// 4. Render spec
	specMarkdown := plan.RenderSpec(input, specOutput)
	specArtifact, err := WriteSpec(opts.OutputDir, specMarkdown)
	if err != nil {
		return errorResult(opts.OutputDir, ew, fmt.Sprintf("write spec error: %v", err))
	}
	emitEvent(schema.EventArtifactWritten, map[string]string{"name": "spec.md"})

	// 5. Generate plan
	planOutput, err := opts.Adapter.GeneratePlan(ctx, input, specMarkdown, opts.PlanOpts)
	if err != nil {
		return errorResult(opts.OutputDir, ew, fmt.Sprintf("plan generation error: %v", err))
	}
	emitEvent(schema.EventPlanPlanGenerated, map[string]string{"status": "ok"})

	// 6. Render plan
	planMarkdown := plan.RenderPlan(input, planOutput)
	planArtifact, err := WritePlan(opts.OutputDir, planMarkdown)
	if err != nil {
		return errorResult(opts.OutputDir, ew, fmt.Sprintf("write plan error: %v", err))
	}
	emitEvent(schema.EventArtifactWritten, map[string]string{"name": "plan.md"})

	// 7. Score coverage and actionability
	coverageReport := confidence.ScoreCoverage(input, specOutput)
	actionabilityReport := confidence.ScoreActionability(planOutput)
	confidenceReport := confidence.ComputeConfidence(
		completenessReport.Score,
		coverageReport.Score,
		actionabilityReport.Score,
		opts.ConfidenceWeights,
	)
	emitEvent(schema.EventPlanConfidenceScored, map[string]interface{}{
		"score":         confidenceReport.Score,
		"sub_scores":    confidenceReport.SubScores,
	})

	// 8. Build results
	status := schema.StatusPass
	if !confidenceReport.PassesThreshold(opts.ConfidenceThreshold) {
		status = schema.StatusFail
	}

	eventsArtifact := schema.Artifact{Name: "events", Path: "events.ndjson", MediaType: "application/x-ndjson"}

	results := &schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    confidenceReport.Score,
		Summary:       fmt.Sprintf("Planning %s (confidence: %.2f)", status, confidenceReport.Score),
		Artifacts:     []schema.Artifact{*specArtifact, *planArtifact, eventsArtifact},
		Metadata: map[string]string{
			"completeness":  fmt.Sprintf("%.2f", completenessReport.Score),
			"coverage":      fmt.Sprintf("%.2f", coverageReport.Score),
			"actionability": fmt.Sprintf("%.2f", actionabilityReport.Score),
		},
	}

	emitEvent(schema.EventAgentEnd, map[string]string{"status": string(status)})

	if err := WriteResults(opts.OutputDir, results); err != nil {
		return nil, fmt.Errorf("write results: %w", err)
	}

	return results, nil
}

func errorResult(outputDir string, ew *schema.EventWriter, msg string) (*schema.Results, error) {
	raw, _ := json.Marshal(map[string]string{"error": msg})
	ew.Write(schema.Event{
		EventType: schema.EventError,
		Data:      raw,
	})
	endRaw, _ := json.Marshal(map[string]string{"status": "error"})
	ew.Write(schema.Event{
		EventType: schema.EventAgentEnd,
		Data:      endRaw,
	})

	results := &schema.Results{
		SchemaVersion: "1.0",
		Status:        schema.StatusError,
		Confidence:    0.0,
		Summary:       msg,
		Artifacts:     []schema.Artifact{{Name: "events", Path: "events.ndjson", MediaType: "application/x-ndjson"}},
	}
	WriteResults(outputDir, results)
	return results, nil
}
