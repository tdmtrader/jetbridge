package harvest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	schema "github.com/concourse/concourse/agent/schema"
)

// DefaultJudgeTimeout bounds the single judge CLI call.
const DefaultJudgeTimeout = 10 * time.Minute

// JudgeIssue is one cited issue from a rubric dimension — it becomes a
// finding with id "judge-<dimension>-<n>" and category "judge" (§6.4.1).
type JudgeIssue struct {
	Dimension   string `json:"dimension"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
}

// JudgeResult is the scored verdict.
type JudgeResult struct {
	RubricHash   string                       `json:"rubric_hash"`
	Dimensions   []schema.JudgeScoreDimension `json:"dimensions"`
	Total        float64                      `json:"total"`     // 0-10 weighted
	MaxTotal     float64                      `json:"max_total"` // 10
	Pass         bool                         `json:"pass"`
	Issues       []JudgeIssue                 `json:"issues,omitempty"`
	Model        string                       `json:"model"`
	CostUSD      float64                      `json:"cost_usd"`
	Turns        int                          `json:"turns"`
	InputTokens  int64                        `json:"input_tokens"`
	OutputTokens int64                        `json:"output_tokens"`
}

// JudgeOpts configures a judge invocation.
type JudgeOpts struct {
	ClaudePath string        // default "claude"
	WorkDir    string        // the workspace checkout
	Diff       string        // truncated base..head patch embedded in the prompt
	Timeout    time.Duration // default DefaultJudgeTimeout
}

// judgeEnvelope is the claude CLI --output-format json envelope (parity
// with ci-agent/llm/result.go; total_cost_usd fallback for newer CLIs).
type judgeEnvelope struct {
	Type         string          `json:"type"`
	Result       json.RawMessage `json:"result"`
	Model        string          `json:"model"`
	CostUSD      float64         `json:"cost_usd"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	NumTurns     int             `json:"num_turns"`
	IsError      bool            `json:"is_error"`
	Usage        struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (e judgeEnvelope) cost() float64 {
	if e.TotalCostUSD > 0 {
		return e.TotalCostUSD
	}
	return e.CostUSD
}

type judgeVerdict struct {
	Dimensions []struct {
		Name      string  `json:"name"`
		Score     float64 `json:"score"`
		Rationale string  `json:"rationale"`
		Issues    []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			File        string `json:"file"`
			Line        int    `json:"line"`
		} `json:"issues"`
	} `json:"dimensions"`
}

var judgeJSONBlockRe = regexp.MustCompile("(?s)```json\\s*\\n(.+?)\\n```")

// extractJSON mirrors ci-agent/llm.ExtractJSON: unwrap ```json fences.
func extractJSON(data []byte) json.RawMessage {
	if m := judgeJSONBlockRe.FindSubmatch(data); m != nil {
		return json.RawMessage(m[1])
	}
	return json.RawMessage(data)
}

// RunJudge makes one schema-constrained claude CLI call scoring the rubric
// against the workspace + diff (§6.4.1). Funded by the platform credential:
// CLAUDE_CODE_OAUTH_TOKEN must already be in the process env (§8.2).
func RunJudge(ctx context.Context, cfg JudgeConfig, opts JudgeOpts) (*JudgeResult, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cli := opts.ClaudePath
	if cli == "" {
		cli = "claude"
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultJudgeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := buildJudgePrompt(cfg, opts.Diff)
	args := []string{"-p", prompt, "--output-format", "json", "--dangerously-skip-permissions"}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}

	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Dir = opts.WorkDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("judge timed out: %w", ctx.Err())
		}
		return nil, fmt.Errorf("judge CLI failed (%v): %s", err, strings.TrimSpace(stderr.String()))
	}

	var env judgeEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env); err != nil || env.Type == "" {
		return nil, fmt.Errorf("judge CLI output is not a result envelope: %.200s", stdout.String())
	}
	if env.IsError {
		return nil, fmt.Errorf("judge CLI reported error: %.500s", string(env.Result))
	}

	raw := env.Result
	if len(raw) > 0 && raw[0] == '"' { // result is a JSON string, unquote then unfence
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = extractJSON([]byte(s))
		}
	}

	var verdict judgeVerdict
	if err := json.Unmarshal(raw, &verdict); err != nil {
		return nil, fmt.Errorf("judge verdict is not valid JSON: %w", err)
	}

	byName := map[string]int{}
	for i, d := range verdict.Dimensions {
		byName[d.Name] = i
	}

	res := &JudgeResult{
		RubricHash:   cfg.RubricHash(),
		MaxTotal:     10,
		Model:        env.Model,
		CostUSD:      env.cost(),
		Turns:        env.NumTurns,
		InputTokens:  env.Usage.InputTokens,
		OutputTokens: env.Usage.OutputTokens,
	}

	var weighted, weightSum float64
	for _, dim := range cfg.Rubric {
		i, found := byName[dim.Name]
		if !found {
			return nil, fmt.Errorf("judge verdict missing dimension %q", dim.Name)
		}
		d := verdict.Dimensions[i]
		score := d.Score
		if score < 0 {
			score = 0
		}
		if score > 10 {
			score = 10
		}
		weighted += score * dim.Weight
		weightSum += dim.Weight
		res.Dimensions = append(res.Dimensions, schema.JudgeScoreDimension{
			Name: dim.Name, Score: score, Max: 10, Rationale: d.Rationale,
		})
		for _, iss := range d.Issues {
			res.Issues = append(res.Issues, JudgeIssue{
				Dimension: dim.Name, Title: iss.Title, Description: iss.Description,
				File: iss.File, Line: iss.Line,
			})
		}
	}
	if weightSum > 0 {
		res.Total = weighted / weightSum
	}
	res.Pass = res.Total >= cfg.PassThreshold

	return res, nil
}

func buildJudgePrompt(cfg JudgeConfig, diff string) string {
	var b strings.Builder
	b.WriteString("You are a strict code-review judge. Score the committed change in this workspace against each rubric dimension from 0 (unacceptable) to 10 (exemplary). You may read files in the working directory to verify claims.\n\nRubric:\n")
	for _, d := range cfg.Rubric {
		fmt.Fprintf(&b, "- %s (weight %g): %s\n", d.Name, d.Weight, d.Guidance)
	}
	b.WriteString("\nRespond with ONLY a JSON object, no prose, exactly this shape:\n")
	b.WriteString(`{"dimensions":[{"name":"<rubric name>","score":<0-10>,"rationale":"<one paragraph>","issues":[{"title":"","description":"","file":"","line":0}]}]}`)
	b.WriteString("\nInclude one dimensions entry per rubric dimension, in order. Cite concrete issues only when you can point at a file.\n\nThe change under review (diff against the target branch):\n\n")
	b.WriteString(diff)
	return b.String()
}
