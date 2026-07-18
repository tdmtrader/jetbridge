package harvest_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
)

// stubClaude writes an executable that emits the given CLI envelope.
func stubClaude(t *testing.T, envelope string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho '" + envelope + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

var judgeCfg = harvest.JudgeConfig{
	Rubric: []harvest.RubricDimension{
		{Name: "correctness", Weight: 3, Guidance: "does it work"},
		{Name: "tests", Weight: 1, Guidance: "are behaviors covered"},
	},
	PassThreshold: 6.5,
	Model:         "claude-sonnet-4-5",
}

func TestRunJudgeScoresWeightsAndIssues(t *testing.T) {
	// correctness 8 (weight 3), tests 4 (weight 1) -> (24+4)/4 = 7.0
	envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"solid\",\"issues\":[]},{\"name\":\"tests\",\"score\":4,\"rationale\":\"thin\",\"issues\":[{\"title\":\"missing edge case\",\"description\":\"no nil test\",\"file\":\"x.go\",\"line\":10}]}]}","model":"claude-sonnet-4-5","total_cost_usd":0.31,"num_turns":1,"is_error":false,"usage":{"input_tokens":900,"output_tokens":120}}`

	res, err := harvest.RunJudge(context.Background(), judgeCfg, harvest.JudgeOpts{
		ClaudePath: stubClaude(t, envelope),
		WorkDir:    t.TempDir(),
		Diff:       "diff --git a/x.go b/x.go",
	})
	if err != nil {
		t.Fatalf("RunJudge: %v", err)
	}
	if res.Total < 6.999 || res.Total > 7.001 {
		t.Fatalf("weighted total = %v, want 7.0", res.Total)
	}
	if res.MaxTotal != 10 || !res.Pass {
		t.Fatalf("MaxTotal/Pass wrong: %+v", res)
	}
	if res.RubricHash != judgeCfg.RubricHash() {
		t.Fatal("rubric hash mismatch")
	}
	if len(res.Dimensions) != 2 || res.Dimensions[0].Name != "correctness" || res.Dimensions[0].Score != 8 {
		t.Fatalf("dimensions wrong: %+v", res.Dimensions)
	}
	if len(res.Issues) != 1 || res.Issues[0].Dimension != "tests" || res.Issues[0].Title != "missing edge case" {
		t.Fatalf("issues wrong: %+v", res.Issues)
	}
	if res.CostUSD < 0.309 || res.CostUSD > 0.311 || res.Model != "claude-sonnet-4-5" {
		t.Fatalf("cost/model wrong: %+v", res)
	}
}

func TestRunJudgeMissingDimensionErrors(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"r\",\"issues\":[]}]}","model":"m","cost_usd":0.1,"num_turns":1,"is_error":false,"usage":{}}`
	_, err := harvest.RunJudge(context.Background(), judgeCfg, harvest.JudgeOpts{
		ClaudePath: stubClaude(t, envelope), WorkDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), `missing dimension "tests"`) {
		t.Fatalf("want missing-dimension error, got %v", err)
	}
}

func TestRunJudgeCLIErrorEnvelope(t *testing.T) {
	envelope := `{"type":"result","subtype":"error_during_execution","result":"\"\"","is_error":true,"usage":{}}`
	_, err := harvest.RunJudge(context.Background(), judgeCfg, harvest.JudgeOpts{
		ClaudePath: stubClaude(t, envelope), WorkDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "judge CLI reported error") {
		t.Fatalf("want CLI-error, got %v", err)
	}
}

func TestRunJudgeScoresAreClamped(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":15,\"rationale\":\"r\",\"issues\":[]},{\"name\":\"tests\",\"score\":-3,\"rationale\":\"r\",\"issues\":[]}]}","model":"m","cost_usd":0.1,"num_turns":1,"is_error":false,"usage":{}}`
	res, err := harvest.RunJudge(context.Background(), judgeCfg, harvest.JudgeOpts{
		ClaudePath: stubClaude(t, envelope), WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RunJudge: %v", err)
	}
	// clamped: (10*3 + 0*1)/4 = 7.5
	if res.Total < 7.499 || res.Total > 7.501 {
		t.Fatalf("clamping failed: total = %v", res.Total)
	}
}

func TestRunJudgeInvalidConfigRefused(t *testing.T) {
	_, err := harvest.RunJudge(context.Background(), harvest.JudgeConfig{}, harvest.JudgeOpts{
		ClaudePath: "claude-not-called", WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("empty rubric must be refused before any CLI call")
	}
}
