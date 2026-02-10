package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/concourse/ci-agent/browserplan"
	"github.com/concourse/ci-agent/config"
	"github.com/concourse/ci-agent/gapgen"
	"github.com/concourse/ci-agent/mapper"
	"github.com/concourse/ci-agent/schema"
	"github.com/concourse/ci-agent/scoring"
	"github.com/concourse/ci-agent/specparser"
)

// QAAgentRunner is a general-purpose agent interface for QA.
type QAAgentRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// QAOptions configures the QA orchestrator.
type QAOptions struct {
	RepoDir    string
	SpecFile   string
	OutputDir  string
	Config     *config.QAConfig
	Agent      QAAgentRunner
	TargetURL  string
}

// RunQA executes the full QA pipeline.
func RunQA(ctx context.Context, opts QAOptions) (*schema.QAOutput, error) {
	// 1. Parse spec
	specData, err := os.ReadFile(opts.SpecFile)
	if err != nil {
		return nil, fmt.Errorf("read spec file: %w", err)
	}
	spec, err := specparser.ParseSpec(specData)
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}

	// 2. Build test index
	index, err := mapper.BuildTestIndex(opts.RepoDir, opts.Config.Include)
	if err != nil {
		return nil, fmt.Errorf("build test index: %w", err)
	}

	// 3. Map requirements to tests
	mappings := mapper.MapRequirements(spec, index)

	// 4. Build requirement results from mappings
	var results []schema.RequirementResult
	for _, m := range mappings {
		status := classifyMappingStatus(m.Status)
		results = append(results, schema.RequirementResult{
			ID:             m.SpecItem.ID,
			Text:           m.SpecItem.Text,
			Status:         status,
			CoveragePoints: status.CoveragePoints(),
		})
	}

	// 5. Gap test generation (if enabled)
	if opts.Config.GenerateTests && opts.Agent != nil {
		gapAgent := &gapAgentWrapper{agent: opts.Agent}
		tests, err := gapgen.GenerateGapTests(ctx, gapAgent, opts.RepoDir, mappings)
		if err == nil && len(tests) > 0 {
			testResults, _ := gapgen.ExecuteGapTests(ctx, opts.RepoDir, tests)
			for i, r := range results {
				if r.Status == schema.CoverageUncoveredBroken {
					if tr, ok := testResults[r.ID]; ok {
						results[i] = gapgen.ClassifyGapResults(r.ID, r.Text, tr)
					}
				}
			}
		}
	}

	// 6. Score
	qaScore := scoring.ComputeQAScore(results, opts.Config.Threshold)
	gaps := scoring.ExtractGaps(results)

	// 7. Browser plan (if enabled)
	var browserPlanText string
	if opts.Config.BrowserPlan {
		browserPlanText, _ = browserplan.GenerateBrowserPlan(ctx, nil, spec, results, opts.TargetURL)
	}

	// 8. Build output
	covered := 0
	generatedCount := 0
	for _, r := range results {
		if r.Status == schema.CoverageCovered || r.Status == schema.CoveragePartial || r.Status == schema.CoverageUncoveredImplemented {
			covered++
		}
		generatedCount += len(r.GeneratedTests)
	}

	output := &schema.QAOutput{
		SchemaVersion: "1.0.0",
		Results:       results,
		Score:         qaScore,
		Gaps:          gaps,
		BrowserPlan:   browserPlanText,
		Metadata: schema.QAMetadata{
			SpecFile:             opts.SpecFile,
			RequirementsTotal:    len(results),
			RequirementsCovered:  covered,
			GeneratedTestsCount:  generatedCount,
			BrowserPlanGenerated: browserPlanText != "",
		},
	}

	// 9. Write output
	if err := writeQAOutput(opts.OutputDir, output, browserPlanText); err != nil {
		return nil, fmt.Errorf("write output: %w", err)
	}

	return output, nil
}

func classifyMappingStatus(status string) schema.CoverageStatus {
	switch status {
	case "covered":
		return schema.CoverageCovered
	case "partial":
		return schema.CoveragePartial
	default:
		return schema.CoverageUncoveredBroken
	}
}

func writeQAOutput(outputDir string, output *schema.QAOutput, browserPlan string) error {
	os.MkdirAll(outputDir, 0755)

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal qa.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "qa.json"), data, 0644); err != nil {
		return err
	}

	if browserPlan != "" {
		if err := os.WriteFile(filepath.Join(outputDir, "browser-qa-plan.md"), []byte(browserPlan), 0644); err != nil {
			return err
		}
	}

	return nil
}

type gapAgentWrapper struct {
	agent QAAgentRunner
}

func (w *gapAgentWrapper) Run(ctx context.Context, prompt string) (string, error) {
	return w.agent.Run(ctx, prompt)
}
