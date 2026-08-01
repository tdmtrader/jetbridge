package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/fly/rc"
)

func TestPrintAgentWorkflowRunDetailIncludesPlannedBuildHint(t *testing.T) {
	plannedBuildID := int64(418)
	detail := workflowrunsapi.RunDetail{RunSummary: workflowrunsapi.RunSummary{WorkflowRunID: 7, PlannedBuildID: &plannedBuildID}}

	output, err := captureAgentWorkflowRunDetail(t, "home", detail, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "planned build: 418\n") {
		t.Fatalf("output %q does not contain planned build correlation", output)
	}
	if !strings.Contains(output, "inspect logs: fly -t home watch -b 418\n") {
		t.Fatalf("output %q does not contain exact build-log command", output)
	}
}

func TestPrintAgentWorkflowRunDetailQuotesUnsafeTargetInBuildHint(t *testing.T) {
	plannedBuildID := int64(418)
	detail := workflowrunsapi.RunDetail{RunSummary: workflowrunsapi.RunSummary{PlannedBuildID: &plannedBuildID}}

	output, err := captureAgentWorkflowRunDetail(t, "prod'; echo pwn", detail, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `inspect logs: fly -t 'prod'\''; echo pwn' watch -b 418`+"\n") {
		t.Fatalf("output %q does not contain a POSIX-shell-quoted target", output)
	}
}

func TestPrintAgentWorkflowRunDetailOmitsBuildHintWithoutPlannedBuild(t *testing.T) {
	output, err := captureAgentWorkflowRunDetail(t, "home", workflowrunsapi.RunDetail{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "planned build:") || strings.Contains(output, "inspect logs:") {
		t.Fatalf("output %q contains a build hint without a planned build", output)
	}
}

func TestPrintAgentWorkflowRunDetailJSONRemainsAPIOnly(t *testing.T) {
	plannedBuildID := int64(418)
	detail := workflowrunsapi.RunDetail{RunSummary: workflowrunsapi.RunSummary{WorkflowRunID: 7, PlannedBuildID: &plannedBuildID}}

	output, err := captureAgentWorkflowRunDetail(t, "", detail, true)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := json.MarshalIndent(detail, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	expected = append(expected, '\n')
	if !bytes.Equal([]byte(output), expected) {
		t.Fatalf("JSON output = %q, want exact API serialization %q", output, expected)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("decode JSON output: %v\noutput: %q", err, output)
	}
	if decoded["planned_build_id"] != float64(418) {
		t.Fatalf("planned_build_id = %#v, want 418", decoded["planned_build_id"])
	}
	if strings.Contains(output, "planned build:") || strings.Contains(output, "inspect logs:") {
		t.Fatalf("JSON output %q contains human prose", output)
	}
}

func TestPrintAgentWorkflowRunDetailRequiresTargetOnlyForPlainBuildHint(t *testing.T) {
	if _, err := captureAgentWorkflowRunDetail(t, "", workflowrunsapi.RunDetail{}, false); err != nil {
		t.Fatalf("plain detail without a build unexpectedly required a target: %v", err)
	}

	plannedBuildID := int64(418)
	detail := workflowrunsapi.RunDetail{RunSummary: workflowrunsapi.RunSummary{PlannedBuildID: &plannedBuildID}}
	if _, err := captureAgentWorkflowRunDetail(t, "", detail, false); err == nil {
		t.Fatal("plain build hint accepted an empty target")
	}
}

// This adapter keeps the first RED focused on the missing renderer behavior;
// it passes targetName through once the production renderer accepts it.
func renderAgentWorkflowRunDetailForTest(targetName rc.TargetName, detail workflowrunsapi.RunDetail, jsonOutput bool) error {
	return printAgentWorkflowRunDetail(targetName, detail, jsonOutput)
}

func captureAgentWorkflowRunDetail(
	t *testing.T,
	targetName rc.TargetName,
	detail workflowrunsapi.RunDetail,
	jsonOutput bool,
) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStdout := os.Stdout
	os.Stdout = writer
	renderErr := renderAgentWorkflowRunDetailForTest(targetName, detail, jsonOutput)
	os.Stdout = previousStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output), renderErr
}
