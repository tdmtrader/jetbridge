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
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
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
	if reason := runFailureReasonFn(targetName, detail.RunSummary); reason != "" {
		fmt.Printf("failure: %s\n", reason)
	}
	return nil
}

// runFailureReason reads the terminal error out of the run's planned build, so
// a failed run says WHY on the surface the operator is already looking at.
// Without it the reason exists only in the build event stream, which is what
// the "inspect logs" hint above is a pointer to — useful, but a second command
// and a wall of JSON away.
//
// Every failure here is non-fatal: this is a diagnostic layered on top of an
// already-successful show-run, and it must never turn a readable answer into
// an error. It also loads its own target rather than taking one, so the
// printer's signature stays the target NAME it needs for the hint.
func runFailureReason(targetName rc.TargetName, run workflowrunsapi.RunSummary) string {
	if run.PlannedBuildID == nil || targetName == "" {
		return ""
	}
	// Only the terminal-unsuccessful states have a reason worth fetching.
	// Naming them positively means a status added later does not silently
	// start pulling event streams for healthy runs. Checked before the target
	// is loaded so a healthy run pays nothing at all.
	if !agentWorkflowRunNeedsFailureDiagnosis(run.Status) {
		return ""
	}
	target, err := rc.LoadTarget(targetName, false)
	if err != nil {
		return ""
	}
	return failureReasonFromTarget(target, run)
}

// failureReasonFromTarget is the testable core of runFailureReason: it takes
// an already-resolved target so a test can inject a fake client, while the
// caller above keeps the target-NAME signature the printer needs for its
// "inspect logs" hint.
func failureReasonFromTarget(target rc.Target, run workflowrunsapi.RunSummary) string {
	if run.PlannedBuildID == nil {
		return ""
	}
	// Repeated from the caller deliberately. The caller checks it first so a
	// healthy run never even loads a target, but the guard belongs with the
	// code that does the fetching: this is the function that would open an
	// event stream for a succeeded run if it were ever called directly.
	if !agentWorkflowRunNeedsFailureDiagnosis(run.Status) {
		return ""
	}
	source, err := target.Client().BuildEvents(fmt.Sprintf("%d", *run.PlannedBuildID))
	if err != nil {
		return ""
	}
	defer source.Close()
	var errorEvents []event.Error
	var finishEvents []event.FinishTask
	// Bounded: an agent step's stdout becomes event.Log, so a transcript-heavy
	// build can carry a very large stream. This scans a large-but-fast stream,
	// not a stalled read — a terminal run's build is always complete, and a
	// complete build's stream ends rather than parking.
	for scanned := 0; scanned < runFailureReasonEventScanLimit; scanned++ {
		streamEvent, err := source.NextEvent()
		if err != nil {
			break
		}
		switch typed := streamEvent.(type) {
		case event.Error:
			errorEvents = append(errorEvents, typed)
		case event.FinishTask:
			finishEvents = append(finishEvents, typed)
		}
	}
	if reason := failureReasonFromErrorEvents(errorEvents); reason != "" {
		return reason
	}
	return exitStatusReasonFromFinishTaskEvents(finishEvents)
}

// agentWorkflowRunNeedsFailureDiagnosis names the terminal-unsuccessful
// statuses positively, so a status added later does not silently start
// pulling event streams for healthy runs.
func agentWorkflowRunNeedsFailureDiagnosis(status db.AgentWorkflowRunStatus) bool {
	switch status {
	case db.AgentWorkflowRunStatusFailed, db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusAborted:
		return true
	default:
		return false
	}
}

var runFailureReasonEventScanLimit = 20000

// runFailureReasonFn is the printer's seam onto runFailureReason. The printer
// takes a target NAME (all it needs for the "inspect logs" hint) and resolves
// the target itself, so a test cannot inject a fake client through it; this
// var lets a test assert what the printer actually writes without giving the
// production path an argument it has no use for.
var runFailureReasonFn = runFailureReason

// failureReasonFromErrorEvents picks the message a human needs out of a
// build's error events: the last one, on one line.
//
// The last error is the one that terminated the run; earlier errors are
// usually a retried or superseded step. Two authored-plan shapes invert that —
// an ensure/on_failure hook that itself errors, and a try-swallowed error
// followed by a silent non-zero exit — but neither occurs in any shipped seed,
// where the renderer emits a flat plan and only leaf steps are error-wrapped.
func failureReasonFromErrorEvents(errorEvents []event.Error) string {
	for index := len(errorEvents) - 1; index >= 0; index-- {
		message := strings.TrimSpace(errorEvents[index].Message)
		if message == "" {
			continue
		}
		if newline := strings.IndexByte(message, '\n'); newline >= 0 {
			message = strings.TrimSpace(message[:newline])
		}
		return message
	}
	return ""
}

// exitStatusReasonFromFinishTaskEvents falls back to the last non-zero exit
// status when no step reported an error message. A plain `failed` run — a
// step's process exiting non-zero without the step itself erroring — never
// produces an event.Error: atc/exec/agent_step.go takes the ExitStatus path,
// not the error path. event.FinishTask is the only signal left for that case,
// which is the majority of real agent failures.
//
// The message says "step", not "agent step": a rendered workflow can carry an
// ordinary task in the same build, and FinishTask.Origin carries only a plan
// ID, so the CLI cannot tell which kind exited.
func exitStatusReasonFromFinishTaskEvents(finishEvents []event.FinishTask) string {
	for index := len(finishEvents) - 1; index >= 0; index-- {
		if finishEvents[index].ExitStatus != 0 {
			return fmt.Sprintf("step exited %d", finishEvents[index].ExitStatus)
		}
	}
	return ""
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
