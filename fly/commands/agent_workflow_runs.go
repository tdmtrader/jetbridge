package commands

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/fly/commands/internal/displayhelpers"
	"github.com/concourse/concourse/fly/rc"
	"github.com/concourse/concourse/fly/ui"
	"github.com/fatih/color"
)

const agentWorkflowRunPollInterval = 250 * time.Millisecond

type WorkflowsRunsCommand struct {
	Args struct {
		Workflow string `positional-arg-name:"WORKFLOW" required:"true" description:"Workflow definition name"`
	} `positional-args:"yes"`
	Status          string `long:"status" description:"Filter by admitting, running, canceling, succeeded, failed, errored, or aborted"`
	OriginKind      string `long:"origin-kind" description:"Filter by exact origin kind"`
	OriginReference string `long:"origin-reference" description:"Filter by exact origin reference"`
	Cursor          string `long:"cursor" description:"Continue after an opaque cursor returned by an earlier page"`
	Limit           int    `long:"limit" default:"100" description:"Maximum runs to return (1-1000)"`
	Json            bool   `long:"json" description:"Print command result as JSON"`
}

func (command *WorkflowsRunsCommand) Execute([]string) error {
	if command.Status != "" && !agentWorkflowRunStatusValid(command.Status) {
		return fmt.Errorf("agent workflow runs: invalid --status %q", command.Status)
	}
	if command.Limit < 1 || command.Limit > 1000 {
		return fmt.Errorf("agent workflow runs: --limit must be between 1 and 1000")
	}
	// The API defaults to the attention lens for the web run list, whose job is
	// finding unresolved work. `fly` lists run history, so it asks for the
	// unfiltered population explicitly rather than inheriting a default that
	// would silently hide resolved runs.
	query := url.Values{
		"limit": []string{strconv.Itoa(command.Limit)},
		"lens":  []string{"all"},
	}
	if command.Status != "" {
		query.Set("status", command.Status)
	}
	if command.OriginKind != "" {
		query.Set("origin_kind", command.OriginKind)
	}
	if command.OriginReference != "" {
		query.Set("origin_reference", command.OriginReference)
	}
	if err := addAgentHistoryCursor(query, command.Cursor); err != nil {
		return fmt.Errorf("agent workflow runs: %w", err)
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentAPIRequest(
		target,
		http.MethodGet,
		"/api/v1/agent/workflows/"+url.PathEscape(command.Args.Workflow)+"/runs?"+query.Encode(),
		nil,
	)
	if err != nil {
		return err
	}
	nextCursor := response.Header.Get("X-Next-Cursor")
	var runs []workflowrunsapi.RunSummary
	if err := decodeOrError(response, &runs); err != nil {
		return err
	}
	if err := reportNextAgentHistoryCursor(os.Stderr, "workflow-run", nextCursor); err != nil {
		return err
	}
	if command.Json {
		return displayhelpers.JsonPrint(runs)
	}
	return renderAgentWorkflowRuns(runs)
}

func agentWorkflowRunStatusValid(status string) bool {
	switch status {
	case "admitting", "running", "canceling", "succeeded", "failed", "errored", "aborted":
		return true
	default:
		return false
	}
}

func renderAgentWorkflowRuns(runs []workflowrunsapi.RunSummary) error {
	table := ui.Table{Headers: ui.TableRow{
		{Contents: "workflow run", Color: color.New(color.Bold)},
		{Contents: "pipeline run", Color: color.New(color.Bold)},
		{Contents: "version", Color: color.New(color.Bold)},
		{Contents: "status", Color: color.New(color.Bold)},
		{Contents: "origin", Color: color.New(color.Bold)},
	}}
	for _, run := range runs {
		pipelineRunID := "none"
		if run.PipelineRunID != nil {
			pipelineRunID = strconv.Itoa(*run.PipelineRunID)
		}
		origin := run.OriginKind
		if run.OriginReference != "" {
			origin += ":" + run.OriginReference
		}
		table.Data = append(table.Data, ui.TableRow{
			{Contents: run.WorkflowRunID.String()},
			{Contents: pipelineRunID},
			{Contents: strconv.Itoa(run.WorkflowVersion)},
			{Contents: string(run.Status)},
			{Contents: origin},
		})
	}
	return table.Render(os.Stdout, Fly.PrintTableHeaders)
}

type WorkflowsRunCommand struct {
	Args struct {
		Workflow string `positional-arg-name:"WORKFLOW" required:"true" description:"Workflow definition name"`
	} `positional-args:"yes"`
	Input          []string `long:"input" description:"Bind a named input as NAME=SNAPSHOT-ID (repeatable)"`
	Version        int      `long:"version" description:"Pinned workflow version (default: live version)"`
	IdempotencyKey string   `long:"idempotency-key" description:"Idempotency key for this invocation (generated when omitted)"`
	Wait           bool     `long:"wait" description:"Wait for the durable workflow run to reach a terminal state"`
	Follow         bool     `long:"follow" description:"Wait and print status transitions to stderr"`
	Json           bool     `long:"json" description:"Print command result as JSON"`
}

func (command *WorkflowsRunCommand) Execute([]string) error {
	inputs, err := parseAgentWorkflowRunInputs(command.Input)
	if err != nil {
		return err
	}
	idempotencyKey := command.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey, err = newAgentWorkflowRunIdempotencyKey()
		if err != nil {
			return err
		}
	}
	var version *int
	if command.Version != 0 {
		if command.Version < 1 {
			return fmt.Errorf("agent workflow run: --version must be positive")
		}
		version = &command.Version
	}
	payload, err := json.Marshal(workflowrunsapi.CreateRequest{
		Version: version, Inputs: inputs, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentAPIRequestWithType(
		target,
		http.MethodPost,
		"/api/v1/agent/workflows/"+url.PathEscape(command.Args.Workflow)+"/runs",
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	var detail workflowrunsapi.RunDetail
	if err := decodeOrError(response, &detail); err != nil {
		return err
	}
	if command.Wait || command.Follow {
		detail, err = waitForAgentWorkflowRun(target, command.Args.Workflow, detail, command.Follow)
		if err != nil {
			return err
		}
	}
	if err := printAgentWorkflowRunDetail(Fly.Target, detail, command.Json); err != nil {
		return err
	}
	if command.Wait || command.Follow {
		return agentWorkflowRunOutcomeError(detail)
	}
	return nil
}

func newAgentWorkflowRunIdempotencyKey() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("agent workflow run: generate idempotency key: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func parseAgentWorkflowRunInputs(values []string) (map[string]snapshot.SnapshotID, error) {
	inputs := make(map[string]snapshot.SnapshotID, len(values))
	for _, value := range values {
		port, rawID, found := strings.Cut(value, "=")
		if !found || strings.TrimSpace(port) == "" || port != strings.TrimSpace(port) {
			return nil, fmt.Errorf("agent workflow run: --input must be NAME=SNAPSHOT-ID")
		}
		if _, exists := inputs[port]; exists {
			return nil, fmt.Errorf("agent workflow run: duplicate input %q", port)
		}
		id, err := snapshot.ParseSnapshotID(rawID)
		if err != nil {
			return nil, fmt.Errorf("agent workflow run: input %q: %w", port, err)
		}
		inputs[port] = id
	}
	return inputs, nil
}

type WorkflowsShowRunCommand struct {
	Args struct {
		Workflow string `positional-arg-name:"WORKFLOW" required:"true" description:"Workflow definition name"`
		RunID    string `positional-arg-name:"WORKFLOW-RUN-ID" required:"true" description:"Durable workflow run ID"`
	} `positional-args:"yes"`
	Outputs bool `long:"outputs" description:"Print the run's public output manifest"`
	Wait    bool `long:"wait" description:"Wait for the durable workflow run to reach a terminal state"`
	Follow  bool `long:"follow" description:"Wait and print status transitions to stderr"`
	Json    bool `long:"json" description:"Print command result as JSON"`
}

type preparedWorkflowsShowRun struct {
	workflow string
	runID    snapshot.WorkflowRunID
	outputs  bool
	wait     bool
	follow   bool
	json     bool
}

func (command *WorkflowsShowRunCommand) prepare() (preparedWorkflowsShowRun, error) {
	if command.Outputs && (command.Wait || command.Follow) {
		return preparedWorkflowsShowRun{}, fmt.Errorf("agent workflow run: --outputs cannot be combined with --wait or --follow")
	}
	runID, err := snapshot.ParseWorkflowRunID(command.Args.RunID)
	if err != nil {
		return preparedWorkflowsShowRun{}, fmt.Errorf("agent workflow run: %w", err)
	}
	return preparedWorkflowsShowRun{
		workflow: command.Args.Workflow,
		runID:    runID,
		outputs:  command.Outputs,
		wait:     command.Wait,
		follow:   command.Follow,
		json:     command.Json,
	}, nil
}

func (command *WorkflowsShowRunCommand) Execute([]string) error {
	return command.executeWithTargetLoader(loadAgentTarget)
}

func (command *WorkflowsShowRunCommand) executeWithTargetLoader(
	loadTarget func() (rc.Target, error),
) error {
	prepared, err := command.prepare()
	if err != nil {
		return err
	}
	target, err := loadTarget()
	if err != nil {
		return err
	}
	return command.executePreparedWithTarget(target, prepared)
}

func (command *WorkflowsShowRunCommand) executePreparedWithTarget(
	target rc.Target,
	prepared preparedWorkflowsShowRun,
) error {
	path := agentWorkflowRunPath(prepared.workflow, prepared.runID)
	if prepared.outputs {
		response, err := agentAPIRequest(target, http.MethodGet, path+"/outputs", nil)
		if err != nil {
			return err
		}
		var outputs workflowrunsapi.OutputsResponse
		if err := decodeOrError(response, &outputs); err != nil {
			return err
		}
		if prepared.json {
			return displayhelpers.JsonPrint(outputs)
		}
		return printAgentWorkflowRunOutputs(outputs)
	}

	response, err := agentAPIRequest(target, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var detail workflowrunsapi.RunDetail
	if err := decodeOrError(response, &detail); err != nil {
		return err
	}
	if prepared.wait || prepared.follow {
		detail, err = waitForAgentWorkflowRun(target, prepared.workflow, detail, prepared.follow)
		if err != nil {
			return err
		}
	}
	return printAgentWorkflowRunDetail(Fly.Target, detail, prepared.json)
}

func waitForAgentWorkflowRun(
	target rc.Target,
	workflowName string,
	detail workflowrunsapi.RunDetail,
	follow bool,
) (workflowrunsapi.RunDetail, error) {
	lastStatus := ""
	for {
		status := string(detail.Status)
		if follow && status != lastStatus {
			fmt.Fprintf(os.Stderr, "workflow run %s: %s\n", detail.WorkflowRunID.String(), status)
			lastStatus = status
		}

		terminal, err := agentWorkflowRunStatusTerminal(status)
		if err != nil {
			return workflowrunsapi.RunDetail{}, err
		}
		if terminal {
			return detail, nil
		}

		time.Sleep(agentWorkflowRunPollInterval)
		response, err := agentAPIRequest(
			target,
			http.MethodGet,
			agentWorkflowRunPath(workflowName, detail.WorkflowRunID),
			nil,
		)
		if err != nil {
			return workflowrunsapi.RunDetail{}, err
		}
		if err := decodeOrError(response, &detail); err != nil {
			return workflowrunsapi.RunDetail{}, err
		}
	}
}

func agentWorkflowRunStatusTerminal(status string) (bool, error) {
	switch status {
	case "admitting", "running", "canceling":
		return false, nil
	case "succeeded", "failed", "errored", "aborted":
		return true, nil
	default:
		return false, fmt.Errorf("agent workflow run: unexpected status %q", status)
	}
}

func agentWorkflowRunOutcomeError(detail workflowrunsapi.RunDetail) error {
	switch detail.Status {
	case "succeeded":
		return nil
	case "failed", "errored", "aborted":
		return fmt.Errorf("workflow run %s finished with status %s", detail.WorkflowRunID.String(), detail.Status)
	default:
		return fmt.Errorf("agent workflow run: expected terminal status, got %q", detail.Status)
	}
}

func agentWorkflowRunPath(workflowName string, runID snapshot.WorkflowRunID) string {
	return "/api/v1/agent/workflows/" + url.PathEscape(workflowName) + "/runs/" + url.PathEscape(runID.String())
}

type WorkflowsCancelRunCommand struct {
	Args struct {
		Workflow string `positional-arg-name:"WORKFLOW" required:"true" description:"Workflow definition name"`
		RunID    string `positional-arg-name:"WORKFLOW-RUN-ID" required:"true" description:"Durable workflow run ID"`
	} `positional-args:"yes"`
	Json bool `long:"json" description:"Print command result as JSON"`
}

func (command *WorkflowsCancelRunCommand) Execute([]string) error {
	runID, err := snapshot.ParseWorkflowRunID(command.Args.RunID)
	if err != nil {
		return fmt.Errorf("agent workflow run: %w", err)
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentAPIRequest(target, http.MethodPost, agentWorkflowRunPath(command.Args.Workflow, runID)+"/cancel", nil)
	if err != nil {
		return err
	}
	var detail workflowrunsapi.RunDetail
	if err := decodeOrError(response, &detail); err != nil {
		return err
	}
	return printAgentWorkflowRunDetail(Fly.Target, detail, command.Json)
}

type WorkflowsRetryRunCommand struct {
	Args struct {
		Workflow string `positional-arg-name:"WORKFLOW" required:"true" description:"Workflow definition name"`
		RunID    string `positional-arg-name:"WORKFLOW-RUN-ID" required:"true" description:"Durable source workflow run ID"`
	} `positional-args:"yes"`
	IdempotencyKey string `long:"idempotency-key" description:"Fresh idempotency key for the retry (generated when omitted)"`
	Json           bool   `long:"json" description:"Print command result as JSON"`
}

func (command *WorkflowsRetryRunCommand) Execute([]string) error {
	runID, err := snapshot.ParseWorkflowRunID(command.Args.RunID)
	if err != nil {
		return fmt.Errorf("agent workflow run: %w", err)
	}
	idempotencyKey := command.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey, err = newAgentWorkflowRunIdempotencyKey()
		if err != nil {
			return err
		}
	}
	payload, err := json.Marshal(workflowrunsapi.RetryRequest{IdempotencyKey: idempotencyKey})
	if err != nil {
		return err
	}
	target, err := loadAgentTarget()
	if err != nil {
		return err
	}
	response, err := agentAPIRequestWithType(
		target,
		http.MethodPost,
		agentWorkflowRunPath(command.Args.Workflow, runID)+"/retry",
		"application/json",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	var detail workflowrunsapi.RunDetail
	if err := decodeOrError(response, &detail); err != nil {
		return err
	}
	return printAgentWorkflowRunDetail(Fly.Target, detail, command.Json)
}

func printAgentWorkflowRun(run workflowrunsapi.RunSummary) error {
	pipelineRunID := "none"
	if run.PipelineRunID != nil {
		pipelineRunID = fmt.Sprintf("%d", *run.PipelineRunID)
	}
	fmt.Printf("workflow run %s\nworkflow: %s@%d\nstatus: %s\npipeline run: %s\n",
		run.WorkflowRunID.String(), run.WorkflowName, run.WorkflowVersion, run.Status, pipelineRunID)
	return nil
}

func printAgentWorkflowRunDetail(targetName rc.TargetName, detail workflowrunsapi.RunDetail, jsonOutput bool) error {
	if jsonOutput {
		return displayhelpers.JsonPrint(detail)
	}
	if err := printAgentWorkflowRun(detail.RunSummary); err != nil {
		return err
	}
	if detail.PlannedBuildID != nil {
		if targetName == "" {
			return fmt.Errorf("agent workflow run: target is required to print planned build hint")
		}
		fmt.Printf("planned build: %d\n", *detail.PlannedBuildID)
		fmt.Printf("inspect logs: fly -t %s watch -b %d\n", shellQuoteTargetAlias(targetName), *detail.PlannedBuildID)
	}
	fmt.Printf("inputs: %d\noutputs: %d\n", len(detail.Inputs), len(detail.Outputs))
	return nil
}

func shellQuoteTargetAlias(targetName rc.TargetName) string {
	value := string(targetName)
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			strings.ContainsRune("_@%+=:,./-", r))
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func printAgentWorkflowRunOutputs(response workflowrunsapi.OutputsResponse) error {
	fmt.Printf("workflow run %s outputs:\n", response.WorkflowRunID.String())
	for _, output := range response.Outputs {
		fmt.Printf("%s: snapshot %s (%s, %s)\n", output.Port, output.Snapshot.ID.String(), output.Snapshot.Type, output.Snapshot.ContentState)
	}
	return nil
}
