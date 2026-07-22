// Package judge implements a rubric-pinned evaluator function. It consumes
// only the materialized workspace and authored evidence supplied by its caller
// and produces a strict measurements/v1 document.
package judge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

const (
	DefaultTimeout       = 10 * time.Minute
	measurementsFileName = "measurements.json"
)

type Config struct {
	EvaluatorVersion string            `json:"evaluator_version"`
	Rubric           []RubricDimension `json:"rubric"`
	PassThreshold    float64           `json:"pass_threshold"`
	Model            string            `json:"model,omitempty"`
	BudgetUSD        float64           `json:"budget_usd,omitempty"`
}

type RubricDimension struct {
	Name     string  `json:"name"`
	Weight   float64 `json:"weight"`
	Guidance string  `json:"guidance"`
}

func (config Config) Validate() error {
	if strings.TrimSpace(config.EvaluatorVersion) == "" {
		return fmt.Errorf("judge: evaluator_version is required")
	}
	if len(config.Rubric) == 0 {
		return fmt.Errorf("judge: rubric must have at least one dimension")
	}
	seen := make(map[string]struct{}, len(config.Rubric))
	for index, dimension := range config.Rubric {
		if strings.TrimSpace(dimension.Name) == "" {
			return fmt.Errorf("judge: rubric dimension %d name is required", index)
		}
		if _, found := seen[dimension.Name]; found {
			return fmt.Errorf("judge: duplicate rubric dimension %q", dimension.Name)
		}
		seen[dimension.Name] = struct{}{}
		if math.IsNaN(dimension.Weight) || math.IsInf(dimension.Weight, 0) || dimension.Weight <= 0 {
			return fmt.Errorf("judge: rubric dimension %q weight must be finite and > 0", dimension.Name)
		}
	}
	if math.IsNaN(config.PassThreshold) || math.IsInf(config.PassThreshold, 0) || config.PassThreshold < 0 || config.PassThreshold > 10 {
		return fmt.Errorf("judge: pass_threshold must be finite and within 0-10, got %g", config.PassThreshold)
	}
	if math.IsNaN(config.BudgetUSD) || math.IsInf(config.BudgetUSD, 0) || config.BudgetUSD < 0 {
		return fmt.Errorf("judge: budget_usd must be finite and nonnegative")
	}
	return nil
}

func (config Config) RubricHash() string {
	payload, _ := json.Marshal(config.Rubric)
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

type Options struct {
	CLIPath  string
	WorkDir  string
	Evidence string
	Timeout  time.Duration
}

type ScoreDimension struct {
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	Max       float64 `json:"max"`
	Rationale string  `json:"rationale"`
}

type Issue struct {
	Dimension   string `json:"dimension"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
}

type Result struct {
	RubricHash     string                         `json:"rubric_hash"`
	Dimensions     []ScoreDimension               `json:"dimensions"`
	Total          float64                        `json:"total"`
	MaxTotal       float64                        `json:"max_total"`
	Pass           bool                           `json:"pass"`
	Issues         []Issue                        `json:"issues,omitempty"`
	Model          string                         `json:"model"`
	CostUSD        float64                        `json:"cost_usd"`
	BudgetExceeded bool                           `json:"budget_exceeded,omitempty"`
	Turns          int                            `json:"turns"`
	InputTokens    int64                          `json:"input_tokens"`
	OutputTokens   int64                          `json:"output_tokens"`
	Measurements   contracts.MeasurementsDocument `json:"measurements"`
}

type cliEnvelope struct {
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

func (envelope cliEnvelope) cost() float64 {
	if envelope.TotalCostUSD > 0 {
		return envelope.TotalCostUSD
	}
	return envelope.CostUSD
}

type verdict struct {
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

var jsonBlockPattern = regexp.MustCompile("(?s)```json\\s*\\n(.+?)\\n```")

func Run(ctx context.Context, config Config, options Options) (*Result, error) {
	if ctx == nil {
		return nil, fmt.Errorf("judge: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(options.WorkDir) == "" {
		return nil, fmt.Errorf("judge: work directory is required")
	}
	cli := options.CLIPath
	if cli == "" {
		cli = "claude"
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("judge: timeout must be positive")
	}
	evaluationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"-p", buildPrompt(config, options.Evidence), "--output-format", "json", "--dangerously-skip-permissions"}
	if config.Model != "" {
		args = append(args, "--model", config.Model)
	}
	command := exec.CommandContext(evaluationContext, cli, args...)
	command.Dir = options.WorkDir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if evaluationContext.Err() != nil {
			return nil, fmt.Errorf("judge timed out: %w", evaluationContext.Err())
		}
		return nil, fmt.Errorf("judge CLI failed (%v): %s", err, strings.TrimSpace(stderr.String()))
	}

	var envelope cliEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &envelope); err != nil || envelope.Type == "" {
		return nil, fmt.Errorf("judge CLI output is not a result envelope: %.200s", stdout.String())
	}
	if envelope.IsError {
		return nil, fmt.Errorf("judge CLI reported error: %.500s", string(envelope.Result))
	}
	raw := envelope.Result
	if len(raw) > 0 && raw[0] == '"' {
		var unquoted string
		if json.Unmarshal(raw, &unquoted) == nil {
			raw = extractJSON([]byte(unquoted))
		}
	}
	var evaluated verdict
	if err := json.Unmarshal(raw, &evaluated); err != nil {
		return nil, fmt.Errorf("judge verdict is not valid JSON: %w", err)
	}

	byName := make(map[string]int, len(evaluated.Dimensions))
	for index, dimension := range evaluated.Dimensions {
		if _, found := byName[dimension.Name]; found {
			return nil, fmt.Errorf("judge verdict contains duplicate dimension %q", dimension.Name)
		}
		byName[dimension.Name] = index
	}
	if len(byName) != len(config.Rubric) {
		for name := range byName {
			if !configHasDimension(config, name) {
				return nil, fmt.Errorf("judge verdict contains unexpected dimension %q", name)
			}
		}
	}

	result := &Result{
		RubricHash:   config.RubricHash(),
		MaxTotal:     10,
		Model:        envelope.Model,
		CostUSD:      envelope.cost(),
		Turns:        envelope.NumTurns,
		InputTokens:  envelope.Usage.InputTokens,
		OutputTokens: envelope.Usage.OutputTokens,
	}
	result.BudgetExceeded = config.BudgetUSD > 0 && result.CostUSD > config.BudgetUSD
	var weighted, weightSum float64
	for _, rubricDimension := range config.Rubric {
		index, found := byName[rubricDimension.Name]
		if !found {
			return nil, fmt.Errorf("judge verdict missing dimension %q", rubricDimension.Name)
		}
		dimension := evaluated.Dimensions[index]
		score := min(10, max(0, dimension.Score))
		weighted += score * rubricDimension.Weight
		weightSum += rubricDimension.Weight
		result.Dimensions = append(result.Dimensions, ScoreDimension{
			Name: rubricDimension.Name, Score: score, Max: 10, Rationale: dimension.Rationale,
		})
		for _, issue := range dimension.Issues {
			result.Issues = append(result.Issues, Issue{
				Dimension: rubricDimension.Name, Title: issue.Title, Description: issue.Description,
				File: issue.File, Line: issue.Line,
			})
		}
	}
	result.Total = weighted / weightSum
	result.Pass = result.Total >= config.PassThreshold
	result.Measurements = successfulMeasurements(config, result.Total, result.Dimensions)
	if err := result.Measurements.Validate(); err != nil {
		return nil, fmt.Errorf("judge: produced invalid measurements/v1: %w", err)
	}
	return result, nil
}

func successfulMeasurements(config Config, total float64, dimensions []ScoreDimension) contracts.MeasurementsDocument {
	document := contracts.MeasurementsDocument{
		SchemaVersion: "1.0.0", EvaluatorVersion: config.EvaluatorVersion, Valid: true,
		Metrics: []contracts.Measurement{{Name: "judge.total", Value: total, Unit: "score/10", Direction: "higher"}},
	}
	for _, dimension := range dimensions {
		document.Metrics = append(document.Metrics, contracts.Measurement{
			Name: "judge.dimension." + dimension.Name, Value: dimension.Score, Unit: "score/10", Direction: "higher",
		})
	}
	return document
}

func WriteMeasurements(ctx context.Context, outputRoot string, document contracts.MeasurementsDocument) error {
	if ctx == nil {
		return fmt.Errorf("judge: context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputRoot) == "" {
		return fmt.Errorf("judge: output root is required")
	}
	if err := document.Validate(); err != nil {
		return fmt.Errorf("judge: invalid measurements/v1: %w", err)
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("judge: marshal measurements/v1: %w", err)
	}
	payload = append(payload, '\n')
	root, err := os.OpenRoot(outputRoot)
	if err != nil {
		return fmt.Errorf("judge: open output root: %w", err)
	}
	writeErr := root.WriteFile(measurementsFileName, payload, 0600)
	closeErr := root.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("judge: write %s: %w", measurementsFileName, err)
	}
	return nil
}

func buildPrompt(config Config, evidence string) string {
	var prompt strings.Builder
	prompt.WriteString("You are a strict code-review judge. Score only the declared evidence and materialized workspace against each rubric dimension from 0 (unacceptable) to 10 (exemplary).\n\nRubric:\n")
	for _, dimension := range config.Rubric {
		fmt.Fprintf(&prompt, "- %s (weight %g): %s\n", dimension.Name, dimension.Weight, dimension.Guidance)
	}
	prompt.WriteString("\nRespond with ONLY a JSON object, no prose, exactly this shape:\n")
	prompt.WriteString(`{"dimensions":[{"name":"<rubric name>","score":<0-10>,"rationale":"<one paragraph>","issues":[{"title":"","description":"","file":"","line":0}]}]}`)
	prompt.WriteString("\nInclude exactly one dimensions entry per rubric dimension. Cite concrete issues only when you can point at a materialized file.\n\nDeclared evidence:\n\n")
	prompt.WriteString(evidence)
	return prompt.String()
}

func extractJSON(data []byte) json.RawMessage {
	if match := jsonBlockPattern.FindSubmatch(data); match != nil {
		return json.RawMessage(match[1])
	}
	return json.RawMessage(data)
}

func configHasDimension(config Config, name string) bool {
	for _, dimension := range config.Rubric {
		if dimension.Name == name {
			return true
		}
	}
	return false
}
