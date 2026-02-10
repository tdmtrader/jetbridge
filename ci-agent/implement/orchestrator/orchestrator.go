package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/ci-agent/implement"
	"github.com/concourse/ci-agent/implement/adapter"
	"github.com/concourse/ci-agent/implement/tdd"
	"github.com/concourse/ci-agent/schema"
)

// Options configures the implementation orchestrator.
type Options struct {
	SpecDir                string
	RepoDir                string
	OutputDir              string
	Adapter                adapter.Adapter
	AgentCLI               string
	BranchName             string
	TestCmd                string
	MaxRetries             int
	MaxConsecutiveFailures int
	ConfidenceThreshold    float64
	Timeout                time.Duration
}

// Run executes the full implementation pipeline.
func Run(ctx context.Context, opts Options) (*schema.Results, error) {
	start := time.Now()

	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		return nil, err
	}

	// Open events file.
	eventsFile, err := os.Create(filepath.Join(opts.OutputDir, "events.ndjson"))
	if err != nil {
		return nil, err
	}
	defer eventsFile.Close()
	eventWriter := schema.NewEventWriter(eventsFile)

	emitEvent(eventWriter, schema.EventAgentStart, map[string]string{
		"agent": "ci-agent-implement",
	})

	// Read spec.
	specCtx, err := implement.ReadSpec(filepath.Join(opts.SpecDir, "spec.md"))
	if err != nil {
		// Spec missing → abstain.
		return buildAbstainResult(opts, eventWriter, "spec read failed: "+err.Error())
	}

	// Parse plan.
	phases, err := implement.ParsePlanFile(filepath.Join(opts.SpecDir, "plan.md"))
	if err != nil {
		return buildAbstainResult(opts, eventWriter, "plan parse failed: "+err.Error())
	}

	emitEvent(eventWriter, "implement.plan_parsed", map[string]interface{}{
		"phases": len(phases),
		"tasks":  countTasks(phases),
	})

	// Initialize tracker.
	tracker := implement.NewTaskTracker(phases)

	// Create branch if specified.
	if opts.BranchName != "" {
		implement.CreateBranch(opts.RepoDir, opts.BranchName)
	}

	// Run sequencer.
	seqOpts := implement.SequencerOpts{
		RepoDir:                opts.RepoDir,
		Phases:                 phases,
		SpecContext:            specCtx.Raw,
		Adapter:                opts.Adapter,
		Tracker:                tracker,
		TestCmd:                opts.TestCmd,
		MaxRetries:             opts.MaxRetries,
		MaxConsecutiveFailures: opts.MaxConsecutiveFailures,
		OutputDir:              opts.OutputDir,
	}

	_, err = implement.RunAll(ctx, seqOpts)
	if err != nil {
		emitEvent(eventWriter, schema.EventError, map[string]string{"error": err.Error()})
	}

	// Final suite check.
	suitePass := true
	if opts.TestCmd != "" {
		suiteResult, err := tdd.RunSuite(ctx, opts.RepoDir, opts.TestCmd)
		if err == nil {
			suitePass = suiteResult.Pass
		}
		emitEvent(eventWriter, "implement.suite_check", map[string]interface{}{
			"pass": suitePass,
		})
	}

	// Score confidence.
	conf := implement.ScoreConfidence(tracker, suitePass)
	emitEvent(eventWriter, "implement.confidence_scored", map[string]interface{}{
		"score":  conf.Score,
		"status": conf.Status,
	})

	// Build results.
	results := implement.BuildResults(tracker, conf, implement.ResultsOpts{
		RepoDir:  opts.RepoDir,
		AgentCLI: opts.AgentCLI,
		Branch:   opts.BranchName,
	})

	// Render summary.
	summaryMD := implement.RenderSummary(tracker, conf, time.Since(start))

	// Write outputs.
	writeJSON(filepath.Join(opts.OutputDir, "results.json"), results)
	os.WriteFile(filepath.Join(opts.OutputDir, "summary.md"), []byte(summaryMD), 0644)
	tracker.Save(opts.OutputDir)

	emitEvent(eventWriter, schema.EventAgentEnd, map[string]string{
		"status": string(results.Status),
	})

	return results, nil
}

func buildAbstainResult(opts Options, ew *schema.EventWriter, reason string) (*schema.Results, error) {
	os.MkdirAll(opts.OutputDir, 0755)

	emitEvent(ew, schema.EventAgentEnd, map[string]string{
		"status": "abstain",
		"reason": reason,
	})

	results := &schema.Results{
		SchemaVersion: "1.0",
		Status:        schema.StatusAbstain,
		Confidence:    0.0,
		Summary:       "Abstained: " + reason,
		Artifacts: []schema.Artifact{
			{Name: "results", Path: "results.json", MediaType: "application/json"},
		},
	}

	writeJSON(filepath.Join(opts.OutputDir, "results.json"), results)
	return results, nil
}

func countTasks(phases []implement.Phase) int {
	n := 0
	for _, p := range phases {
		n += len(p.Tasks)
	}
	return n
}

func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func emitEvent(ew *schema.EventWriter, eventType schema.EventType, data interface{}) {
	raw, _ := json.Marshal(data)
	ew.Write(schema.Event{
		EventType: eventType,
		Data:      raw,
	})
}
