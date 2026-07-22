package judge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testConfig = Config{
	EvaluatorVersion: "code-review-judge/v3",
	Rubric: []RubricDimension{
		{Name: "correctness", Weight: 3, Guidance: "does it work"},
		{Name: "tests", Weight: 1, Guidance: "are behaviors covered"},
	},
	PassThreshold: 6.5,
	Model:         "test-model",
}

func TestRunScoresDeclaredRubricAndProducesMeasurements(t *testing.T) {
	envelope := `{"type":"result","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"solid\",\"issues\":[]},{\"name\":\"tests\",\"score\":4,\"rationale\":\"thin\",\"issues\":[{\"title\":\"missing edge case\",\"description\":\"no nil test\",\"file\":\"x.go\",\"line\":10}]}]}","model":"test-model","total_cost_usd":0.31,"num_turns":1,"is_error":false,"usage":{"input_tokens":900,"output_tokens":120}}`
	result, err := Run(context.Background(), testConfig, Options{
		CLIPath: stubCLI(t, envelope), WorkDir: t.TempDir(), Evidence: "diff --git a/x.go b/x.go",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Total != 7 || !result.Pass || result.MaxTotal != 10 {
		t.Fatalf("score = %+v", result)
	}
	if len(result.Issues) != 1 || result.Issues[0].Dimension != "tests" {
		t.Fatalf("issues = %+v", result.Issues)
	}
	if result.Measurements.EvaluatorVersion != testConfig.EvaluatorVersion || !result.Measurements.Valid {
		t.Fatalf("measurements = %+v", result.Measurements)
	}
	if err := result.Measurements.Validate(); err != nil {
		t.Fatalf("measurements/v1: %v", err)
	}
	if got := measurementValue(t, result, "judge.total"); got != 7 {
		t.Fatalf("judge.total = %v", got)
	}
}

func TestRunRequiresPinnedEvaluatorAndExactDimensions(t *testing.T) {
	config := testConfig
	config.EvaluatorVersion = ""
	if _, err := Run(context.Background(), config, Options{CLIPath: "must-not-run", WorkDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "evaluator_version") {
		t.Fatalf("unpinned evaluator error = %v", err)
	}

	envelope := `{"type":"result","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"ok\",\"issues\":[]}]}","model":"test-model","is_error":false,"usage":{}}`
	if _, err := Run(context.Background(), testConfig, Options{CLIPath: stubCLI(t, envelope), WorkDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), `missing dimension "tests"`) {
		t.Fatalf("missing dimension error = %v", err)
	}
}

func TestRunClampsScoresAndRejectsDuplicateOrUnexpectedDimensions(t *testing.T) {
	envelope := `{"type":"result","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":15,\"rationale\":\"ok\",\"issues\":[]},{\"name\":\"tests\",\"score\":-3,\"rationale\":\"ok\",\"issues\":[]}]}","model":"test-model","is_error":false,"usage":{}}`
	result, err := Run(context.Background(), testConfig, Options{CLIPath: stubCLI(t, envelope), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 7.5 {
		t.Fatalf("clamped total = %v", result.Total)
	}

	duplicate := `{"type":"result","result":"{\"dimensions\":[{\"name\":\"correctness\",\"score\":8,\"rationale\":\"ok\",\"issues\":[]},{\"name\":\"correctness\",\"score\":8,\"rationale\":\"again\",\"issues\":[]},{\"name\":\"tests\",\"score\":8,\"rationale\":\"ok\",\"issues\":[]}]}","model":"test-model","is_error":false,"usage":{}}`
	if _, err := Run(context.Background(), testConfig, Options{CLIPath: stubCLI(t, duplicate), WorkDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "duplicate dimension") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestWriteMeasurementsEmitsStrictSnapshotDocument(t *testing.T) {
	document := successfulMeasurements(testConfig, 7, []ScoreDimension{{Name: "correctness", Score: 7, Max: 10}})
	root := t.TempDir()
	if err := WriteMeasurements(context.Background(), root, document); err != nil {
		t.Fatalf("WriteMeasurements: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(root, "measurements.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["evaluator_version"] != testConfig.EvaluatorVersion || decoded["valid"] != true {
		t.Fatalf("payload = %s", payload)
	}
}

func measurementValue(t *testing.T, result *Result, name string) float64 {
	t.Helper()
	for _, metric := range result.Measurements.Metrics {
		if metric.Name == name {
			return metric.Value
		}
	}
	t.Fatalf("missing measurement %q", name)
	return 0
}

func stubCLI(t *testing.T, envelope string) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "judge")
	script := "#!/bin/sh\nprintf '%s\\n' '" + envelope + "'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
