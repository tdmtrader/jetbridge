package harvest

import (
	"context"
	"time"

	functionjudge "github.com/concourse/concourse/agent/functions/judge"
	schema "github.com/concourse/concourse/agent/schema"
)

// DefaultJudgeTimeout bounds the single judge CLI call.
const DefaultJudgeTimeout = functionjudge.DefaultTimeout

// JudgeIssue remains the legacy harvest evidence shape. The implementation is
// shared with the ordinary judge function used by version-3 workflows.
type JudgeIssue = functionjudge.Issue

// JudgeResult is the scored legacy harvest verdict.
type JudgeResult struct {
	RubricHash   string                       `json:"rubric_hash"`
	Dimensions   []schema.JudgeScoreDimension `json:"dimensions"`
	Total        float64                      `json:"total"`
	MaxTotal     float64                      `json:"max_total"`
	Pass         bool                         `json:"pass"`
	Issues       []JudgeIssue                 `json:"issues,omitempty"`
	Model        string                       `json:"model"`
	CostUSD      float64                      `json:"cost_usd"`
	Turns        int                          `json:"turns"`
	InputTokens  int64                        `json:"input_tokens"`
	OutputTokens int64                        `json:"output_tokens"`
}

type JudgeOpts struct {
	ClaudePath string
	WorkDir    string
	Diff       string
	Timeout    time.Duration
}

// RunJudge is the version-1/2 compatibility adapter around the reusable judge
// function. Its evaluator identity pins both the compatibility implementation
// and exact rubric; version-3 workflows author their own evaluator version.
func RunJudge(ctx context.Context, config JudgeConfig, options JudgeOpts) (*JudgeResult, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	rubric := make([]functionjudge.RubricDimension, len(config.Rubric))
	for index, dimension := range config.Rubric {
		rubric[index] = functionjudge.RubricDimension{
			Name: dimension.Name, Weight: dimension.Weight, Guidance: dimension.Guidance,
		}
	}
	result, err := functionjudge.Run(ctx, functionjudge.Config{
		EvaluatorVersion: "legacy-harvest-judge/v1@" + config.RubricHash(),
		Rubric:           rubric,
		PassThreshold:    config.PassThreshold,
		Model:            config.Model,
		BudgetUSD:        config.BudgetUSD,
	}, functionjudge.Options{
		CLIPath: options.ClaudePath, WorkDir: options.WorkDir, Evidence: options.Diff, Timeout: options.Timeout,
	})
	if err != nil {
		return nil, err
	}
	legacy := &JudgeResult{
		RubricHash: result.RubricHash, Total: result.Total, MaxTotal: result.MaxTotal,
		Pass: result.Pass, Issues: result.Issues, Model: result.Model, CostUSD: result.CostUSD,
		Turns: result.Turns, InputTokens: result.InputTokens, OutputTokens: result.OutputTokens,
	}
	for _, dimension := range result.Dimensions {
		legacy.Dimensions = append(legacy.Dimensions, schema.JudgeScoreDimension{
			Name: dimension.Name, Score: dimension.Score, Max: dimension.Max, Rationale: dimension.Rationale,
		})
	}
	return legacy, nil
}
