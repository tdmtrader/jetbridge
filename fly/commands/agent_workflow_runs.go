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
	query := url.Values{"limit": []string{strconv.Itoa(command.Limit)}}
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
	if err := printAgentWorkflowRunDetail(target, detail, command.Json); err != nil {
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
	return printAgentWorkflowRunDetail(target, detail, prepared.json)
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
	return printAgentWorkflowRunDetail(target, detail, command.Json)
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
	return printAgentWorkflowRunDetail(target, detail, command.Json)
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

func printAgentWorkflowRunDetail(target rc.Target, detail workflowrunsapi.RunDetail, jsonOutput bool) error {
	if jsonOutput {
		return displayhelpers.JsonPrint(detail)
	}
	if err := printAgentWorkflowRun(detail.RunSummary); err != nil {
		return err
	}
	fmt.Printf("inputs: %d\noutputs: %d\n", len(detail.Inputs), len(detail.Outputs))
	// The hint must print for every terminal-unsuccessful run, independent of
	// whether a reason was found: a plain `failed` run often has no
	// event.Error at all (see runFailureReason), and that must not leave the
	// user with nothing but a bare status line. This is also what makes the
	// PlannedBuildID != nil check load-bearing rather than a no-op guard
	// mirroring runFailureReason's own check: it protects the deref below.
	if agentWorkflowRunNeedsFailureDiagnosis(detail.Status) && detail.PlannedBuildID != nil {
		if reason := runFailureReason(target, detail.RunSummary); reason != "" {
			fmt.Printf("failure: %s\n", reason)
		}
		// Fly.Target (a package global, not something threaded through
		// target) is safe to read here: every call site reaches this
		// function via loadAgentTarget/executeWithTargetLoader, both of
		// which resolve target from Fly.Target, and target loading fails on
		// an empty target name — so the printed command always matches the
		// target that produced `detail`. rc.Target itself exposes no Name()
		// to read the value back off target instead.
		fmt.Printf("full log: fly -t %s watch -b %d\n", Fly.Target, *detail.PlannedBuildID)
	}
	return nil
}

// agentWorkflowRunNeedsFailureDiagnosis reports whether a run's status is a
// terminal-unsuccessful state worth fetching a failure reason, and worth
// pointing the user at `fly watch` for. Naming the states positively means a
// status added later does not silently start pulling event streams for
// healthy runs.
func agentWorkflowRunNeedsFailureDiagnosis(status db.AgentWorkflowRunStatus) bool {
	switch status {
	case db.AgentWorkflowRunStatusFailed, db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusAborted:
		return true
	default:
		return false
	}
}

// failureReasonFromErrorEvents picks the message a human needs out of a
// build's error events: the last one, on one line.
//
// The last error is the one that terminated the run; earlier errors are
// usually a retried or superseded step. Multi-line messages are collapsed
// because this prints inside a field list, and the full text remains
// available through `fly watch`.
//
// Last-wins is correct for every shipped seed: the renderer emits a flat
// PlanSequence (no across/attempts), and only leaf steps are LogError-
// wrapped, so a run produces at most one error event today. It can mislead
// once authored plans use ensure/on_failure or try: an ensure hook that
// itself errors reports the hook's error instead of the step it was
// guarding, and a try-swallowed error followed by a silent non-zero exit
// reports nothing useful from this function at all (see the FinishTask
// fallback in runFailureReason for that second case). Not fixed here —
// matching an error event back to the step that actually caused the failure
// is more machinery than this diagnostic nicety warrants right now.
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
// step's process exiting non-zero without the step itself erroring, e.g.
// `agent-runner: claude: exit status 1` or an output-contract mismatch —
// never produces an event.Error (atc/exec/agent_step.go takes the
// ExitStatus path, not the error path, and the engine's StatusFailed branch
// adds nothing further). event.FinishTask is the only signal left for that
// case.
//
// The message says "step", not "agent step": a rendered workflow can carry an
// ordinary task in the same build — agent/workflow/render.go injects merge
// preflight tasks into the same PlanSequence — and those reach the same
// taskDelegate.Finished. event.FinishTask.Origin carries only a plan ID, so
// the CLI cannot tell which kind exited without more lookups than a
// diagnostic line is worth.
func exitStatusReasonFromFinishTaskEvents(finishEvents []event.FinishTask) string {
	for index := len(finishEvents) - 1; index >= 0; index-- {
		if finishEvents[index].ExitStatus != 0 {
			return fmt.Sprintf("step exited %d", finishEvents[index].ExitStatus)
		}
	}
	return ""
}

// runFailureReasonEventScanLimit bounds how many stream events
// runFailureReason will read before giving up. The build-events endpoint
// only supports resuming via Last-Event-ID — no type filter, no reverse
// read — so finding a terminal error or exit status means reading every
// event a step logged, and an agent step's stdout is often a full Claude
// stream-json transcript. This keeps a diagnostic nicety from turning into
// an unbounded read; if the limit is hit, runFailureReason returns whatever
// evidence it gathered before the cutoff (often none), and the caller's
// unconditional "full log:" pointer remains the fallback. A var, not a
// const, so tests can shrink it instead of emitting the full limit.
//
// This bounds a large-but-fast stream, NOT a stalled read, and that is the
// right scope: a blocked NextEvent is unreachable for the statuses this runs
// under. build.finish sets status, completed, and the run's execution status
// in one transaction, so a terminal run's build is always complete, and a
// complete build's stream ends rather than parking. The one direct-to-aborted
// path fires only when PlannedBuildID is nil, where no stream is opened at
// all. The residual risk is a half-open connection, which every other request
// fly makes shares equally — the fix for that is a client timeout in fly/rc,
// not a special case here. Do not re-litigate this into a deadline.
var runFailureReasonEventScanLimit = 20000

// runFailureReason reads the terminal error, or (failing that) the terminal
// non-zero exit status, out of the run's planned build. Every failure here
// is non-fatal: this is a diagnostic nicety layered on top of a successful
// show-run, and it must never turn a readable answer into an error.
func runFailureReason(target rc.Target, run workflowrunsapi.RunSummary) string {
	if run.PlannedBuildID == nil || !agentWorkflowRunNeedsFailureDiagnosis(run.Status) {
		return ""
	}
	source, err := target.Client().BuildEvents(fmt.Sprintf("%d", *run.PlannedBuildID))
	if err != nil {
		return ""
	}
	defer source.Close()
	var errorEvents []event.Error
	var finishEvents []event.FinishTask
	for scanned := 0; scanned < runFailureReasonEventScanLimit; scanned++ {
		streamEvent, err := source.NextEvent()
		if err != nil {
			break
		}
		switch typedEvent := streamEvent.(type) {
		case event.Error:
			errorEvents = append(errorEvents, typedEvent)
		case event.FinishTask:
			finishEvents = append(finishEvents, typedEvent)
		}
	}
	if reason := failureReasonFromErrorEvents(errorEvents); reason != "" {
		return reason
	}
	return exitStatusReasonFromFinishTaskEvents(finishEvents)
}

func printAgentWorkflowRunOutputs(response workflowrunsapi.OutputsResponse) error {
	fmt.Printf("workflow run %s outputs:\n", response.WorkflowRunID.String())
	for _, output := range response.Outputs {
		fmt.Printf("%s: snapshot %s (%s, %s)\n", output.Port, output.Snapshot.ID.String(), output.Snapshot.Type, output.Snapshot.ContentState)
	}
	return nil
}
