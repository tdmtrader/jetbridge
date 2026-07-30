package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/broker/output"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

type ReviewRequest struct {
	IdempotencyKey string   `json:"idempotency_key"`
	Selector       Selector `json:"selector"`
	Instructions   string   `json:"instructions,omitempty"`
	Attachments    []string `json:"attachments"`
}

type ConsultRequest struct {
	IdempotencyKey string   `json:"idempotency_key"`
	Selector       Selector `json:"selector"`
	Question       string   `json:"question"`
	Context        string   `json:"context,omitempty"`
	Attachments    []string `json:"attachments,omitempty"`
}

type AdmissionRequest struct {
	IdempotencyKey string
	Tool           Tool
	Selector       Selector
	ProfileID      string
	ProfileDigest  string
	InputDigest    string
	Attachments    []string
}

type SealRequest struct {
	ExecutionID string
	Body        json.RawMessage
}

type Attachment struct {
	Name    string
	Prompt  string
	Subject contracts.Subject
}

type RunRequest struct {
	ExecutionID string
	Tool        Tool
	Profile     Profile
	Prompt      string
	Credential  string
	Attachments []Attachment
}

type RunResult struct {
	Output       json.RawMessage
	Events       []Event
	Duration     time.Duration
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
}

// Event is the only event shape that crosses from the broker data plane into
// the execution authority. It deliberately has no provider-supplied text,
// session IDs, environment values, or native payloads.
type Event struct {
	Kind EventKind `json:"kind"`
}

type EventKind string

const (
	EventProgress  EventKind = "progress"
	EventCompleted EventKind = "completed"
	EventFailed    EventKind = "failed"
	maxRunEvents             = 128
)

type RunUpdate struct {
	Events       []Event
	Duration     time.Duration
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
}

type Terminal struct {
	State     ExecutionState
	Code      string
	Retryable bool
	Summary   string
}

type Result struct {
	ExecutionID  string               `json:"execution_id"`
	Selector     Selector             `json:"selector"`
	Profile      Profile              `json:"profile"`
	Snapshot     snapshot.SnapshotRef `json:"snapshot"`
	Body         any                  `json:"body"`
	Duration     time.Duration        `json:"duration"`
	InputTokens  *int64               `json:"input_tokens,omitempty"`
	OutputTokens *int64               `json:"output_tokens,omitempty"`
	CostUSD      *float64             `json:"cost_usd,omitempty"`
	StaticReview bool                 `json:"static_review"`
	TestsRun     bool                 `json:"tests_run"`
}

type Authority interface {
	Admit(context.Context, AdmissionRequest) (string, error)
	Phase(context.Context, string, string) error
	Update(context.Context, string, RunUpdate) error
	Terminal(context.Context, string, Terminal) error
	Seal(context.Context, SealRequest) (snapshot.SnapshotRef, error)
}

type AttachmentResolver interface {
	Resolve(context.Context, []string) ([]Attachment, error)
}

type CredentialResolver interface {
	Resolve(context.Context, string) (string, error)
}

type Runner interface {
	Run(context.Context, RunRequest) (RunResult, error)
}

type EngineConfig struct {
	Catalog      *Catalog
	Authority    Authority
	Attachments  AttachmentResolver
	Credentials  CredentialResolver
	Runner       Runner
	Instructions map[Tool]string
}

type Engine struct {
	config EngineConfig
}

func NewEngine(config EngineConfig) *Engine {
	config.Instructions = cloneInstructions(config.Instructions)
	return &Engine{config: config}
}

func (engine *Engine) RequestReview(ctx context.Context, request ReviewRequest) (Result, error) {
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return Result{}, fmt.Errorf("broker request_review: idempotency key is required")
	}
	if err := ValidateAttachments(ToolRequestReview, request.Attachments); err != nil {
		return Result{}, fmt.Errorf("broker request_review: %w", err)
	}
	caller := strings.TrimSpace(request.Instructions)
	return engine.execute(ctx, executeRequest{
		idempotencyKey: request.IdempotencyKey, tool: ToolRequestReview,
		selector: request.Selector, callerText: caller, attachments: request.Attachments,
		staticReview: true,
	})
}

func (engine *Engine) ConsultAgent(ctx context.Context, request ConsultRequest) (Result, error) {
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return Result{}, fmt.Errorf("broker consult_agent: idempotency key is required")
	}
	if strings.TrimSpace(request.Question) == "" {
		return Result{}, fmt.Errorf("broker consult_agent: question is required")
	}
	if err := ValidateAttachments(ToolConsultAgent, request.Attachments); err != nil {
		return Result{}, fmt.Errorf("broker consult_agent: %w", err)
	}
	caller := "Question:\n" + strings.TrimSpace(request.Question)
	if strings.TrimSpace(request.Context) != "" {
		caller += "\n\nCaller context:\n" + strings.TrimSpace(request.Context)
	}
	return engine.execute(ctx, executeRequest{
		idempotencyKey: request.IdempotencyKey, tool: ToolConsultAgent,
		selector: request.Selector, callerText: caller, attachments: request.Attachments,
	})
}

type executeRequest struct {
	idempotencyKey string
	tool           Tool
	selector       Selector
	callerText     string
	attachments    []string
	staticReview   bool
}

func (engine *Engine) execute(ctx context.Context, request executeRequest) (Result, error) {
	if err := engine.validateConfig(request.tool); err != nil {
		return Result{}, err
	}
	profile, err := engine.config.Catalog.Resolve(request.tool, request.selector)
	if err != nil {
		return Result{}, err
	}
	executionCtx, cancel := context.WithTimeout(ctx, profile.Limits.Timeout)
	defer cancel()
	admission := AdmissionRequest{
		IdempotencyKey: request.idempotencyKey, Tool: request.tool,
		Selector: request.selector, ProfileID: profile.ID, ProfileDigest: profile.Digest,
		InputDigest: digestInput(request.callerText, request.attachments),
		Attachments: append([]string(nil), request.attachments...),
	}
	executionID, err := engine.config.Authority.Admit(executionCtx, admission)
	if err != nil {
		return Result{}, fmt.Errorf("broker: admit child execution: %w", err)
	}
	if request.tool == ToolRequestReview {
		if err := engine.config.Authority.Phase(executionCtx, executionID, "capturing"); err != nil {
			if executionCtx.Err() != nil {
				return Result{}, engine.fail(authorityContext(ctx), executionID, classifyRunFailure(executionCtx))
			}
			return Result{}, fmt.Errorf("broker: mark execution capturing: %w", err)
		}
	}
	attachments, err := engine.config.Attachments.Resolve(executionCtx, request.attachments)
	if err != nil {
		return Result{}, engine.fail(authorityContext(ctx), executionID,
			failureForContext(executionCtx, failureAttachmentUnknown))
	}
	if err := verifyAttachments(request.attachments, attachments); err != nil {
		return Result{}, engine.fail(authorityContext(ctx), executionID,
			failureForContext(executionCtx, failureAttachmentUnknown))
	}
	subjects := make([]contracts.Subject, len(attachments))
	for index, attachment := range attachments {
		if attachment.Name == "" || strings.TrimSpace(attachment.Prompt) == "" {
			return Result{}, engine.fail(authorityContext(ctx), executionID,
				failureForContext(executionCtx, failureAttachmentUnknown))
		}
		if err := attachment.Subject.Validate(); err != nil {
			return Result{}, engine.fail(authorityContext(ctx), executionID,
				failureForContext(executionCtx, failureAttachmentUnknown))
		}
		subjects[index] = attachment.Subject
	}
	credential, err := engine.config.Credentials.Resolve(executionCtx, profile.CredentialSlot)
	if err != nil {
		return Result{}, engine.fail(authorityContext(ctx), executionID,
			failureForContext(executionCtx, failureCredentialUnavailable))
	}
	prompt := buildPrompt(engine.config.Instructions[request.tool], request.callerText, attachments)
	if err := engine.config.Authority.Phase(executionCtx, executionID, "running"); err != nil {
		if executionCtx.Err() != nil {
			return Result{}, engine.fail(authorityContext(ctx), executionID, classifyRunFailure(executionCtx))
		}
		return Result{}, fmt.Errorf("broker: mark execution running: %w", err)
	}
	run, err := engine.config.Runner.Run(executionCtx, RunRequest{
		ExecutionID: executionID, Tool: request.tool, Profile: profile,
		Prompt: prompt, Credential: credential, Attachments: append([]Attachment(nil), attachments...),
	})
	events, eventErr := NormalizeEvents(run.Events)
	if eventErr != nil {
		return Result{}, engine.fail(authorityContext(ctx), executionID, failureProviderRejected)
	}
	if updateErr := engine.config.Authority.Update(authorityContext(ctx), executionID, RunUpdate{
		Events: events, Duration: run.Duration, InputTokens: run.InputTokens,
		OutputTokens: run.OutputTokens, CostUSD: run.CostUSD,
	}); updateErr != nil {
		return Result{}, fmt.Errorf("broker: persist execution update: %w", updateErr)
	}
	if err != nil {
		return Result{}, engine.fail(authorityContext(ctx), executionID, classifyRunFailure(executionCtx))
	}
	if err := engine.config.Authority.Phase(executionCtx, executionID, "validating"); err != nil {
		if executionCtx.Err() != nil {
			return Result{}, engine.fail(authorityContext(ctx), executionID, classifyRunFailure(executionCtx))
		}
		return Result{}, fmt.Errorf("broker: mark execution validating: %w", err)
	}
	body, err := output.Decode(string(request.tool), run.Output, subjects, int(profile.Limits.MaxOutputBytes))
	if err != nil {
		return Result{}, engine.fail(authorityContext(ctx), executionID, failureOutputInvalid)
	}
	if err := engine.config.Authority.Phase(executionCtx, executionID, "sealing"); err != nil {
		if executionCtx.Err() != nil {
			return Result{}, engine.fail(authorityContext(ctx), executionID, classifyRunFailure(executionCtx))
		}
		return Result{}, fmt.Errorf("broker: mark execution sealing: %w", err)
	}
	candidate, err := json.Marshal(body)
	if err != nil {
		return Result{}, engine.fail(authorityContext(ctx), executionID, failureOutputInvalid)
	}
	sealed, err := engine.config.Authority.Seal(executionCtx, SealRequest{
		ExecutionID: executionID, Body: candidate,
	})
	if err != nil {
		if executionCtx.Err() != nil {
			return Result{}, engine.fail(authorityContext(ctx), executionID, classifyRunFailure(executionCtx))
		}
		return Result{}, engine.fail(authorityContext(ctx), executionID, failureSealingFailed)
	}
	return Result{
		ExecutionID: executionID, Selector: request.selector, Profile: profile,
		Snapshot: sealed, Body: body, Duration: run.Duration,
		InputTokens: run.InputTokens, OutputTokens: run.OutputTokens, CostUSD: run.CostUSD,
		StaticReview: request.staticReview, TestsRun: false,
	}, nil
}

func (engine *Engine) validateConfig(tool Tool) error {
	if engine == nil || engine.config.Catalog == nil || engine.config.Authority == nil ||
		engine.config.Attachments == nil || engine.config.Credentials == nil ||
		engine.config.Runner == nil {
		return fmt.Errorf("broker: engine dependencies are incomplete")
	}
	if strings.TrimSpace(engine.config.Instructions[tool]) == "" {
		return fmt.Errorf("broker: fixed instructions for %s are required", tool)
	}
	return nil
}

type failure struct {
	state     ExecutionState
	code      string
	retryable bool
	summary   string
}

var (
	failureAttachmentUnknown     = failure{ExecutionErrored, "attachment_unknown", false, "declared attachments could not be resolved"}
	failureCredentialUnavailable = failure{ExecutionErrored, "credential_unavailable", true, "broker credential is unavailable"}
	failureProviderRejected      = failure{ExecutionErrored, "provider_rejected", true, "provider rejected the child execution"}
	failureDeadlineExceeded      = failure{ExecutionTimedOut, "deadline_exceeded", true, "child execution exceeded its deadline"}
	failureCancelled             = failure{ExecutionCancelled, "cancelled", true, "child execution was cancelled"}
	failureOutputInvalid         = failure{ExecutionErrored, "output_invalid", false, "child execution returned invalid typed output"}
	failureSealingFailed         = failure{ExecutionErrored, "sealing_failed", true, "child result could not be sealed"}
)

func (engine *Engine) fail(ctx context.Context, executionID string, terminal failure) error {
	if err := engine.config.Authority.Terminal(ctx, executionID, Terminal{
		State: terminal.state, Code: terminal.code, Retryable: terminal.retryable, Summary: terminal.summary,
	}); err != nil {
		return fmt.Errorf("broker child %s: record terminal execution failed", executionID)
	}
	return &ExecutionError{ExecutionID: executionID, Code: terminal.code, Retryable: terminal.retryable, Summary: terminal.summary}
}

type ExecutionError struct {
	ExecutionID string
	Code        string
	Retryable   bool
	Summary     string
}

func (failure *ExecutionError) Error() string {
	return fmt.Sprintf("broker child %s: %s: %s", failure.ExecutionID, failure.Code, failure.Summary)
}

var attachmentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

var allowedAttachments = map[Tool]map[string]struct{}{
	ToolRequestReview: {"workspace": {}, "validation": {}},
	ToolConsultAgent:  {"design": {}, "api-contract": {}},
}

// ValidateAttachments enforces the fixed per-tool logical attachment contract
// at every untrusted boundary before admission, prompt assembly, or sealing.
func ValidateAttachments(tool Tool, names []string) error {
	allowed, found := allowedAttachments[tool]
	if !found {
		return fmt.Errorf("unsupported tool %q", tool)
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !attachmentNamePattern.MatchString(name) {
			return fmt.Errorf("attachment %q must be a logical name", name)
		}
		if _, found := seen[name]; found {
			return fmt.Errorf("attachment %q is duplicate", name)
		}
		if _, found := allowed[name]; !found {
			return fmt.Errorf("attachment %q is not allowed for %s", name, tool)
		}
		seen[name] = struct{}{}
	}
	switch tool {
	case ToolRequestReview:
		if !containsString(names, "workspace") {
			return fmt.Errorf("workspace attachment is required")
		}
	case ToolConsultAgent:
		if len(names) == 0 {
			return fmt.Errorf("at least one declared attachment is required")
		}
	}
	return nil
}

func verifyAttachments(requested []string, resolved []Attachment) error {
	if len(requested) != len(resolved) {
		return fmt.Errorf("resolved attachment count does not match request")
	}
	for index, name := range requested {
		if resolved[index].Name != name {
			return fmt.Errorf("resolved attachment %d does not match request", index)
		}
	}
	return nil
}

func classifyRunFailure(ctx context.Context) failure {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return failureDeadlineExceeded
	case errors.Is(ctx.Err(), context.Canceled):
		return failureCancelled
	default:
		return failureProviderRejected
	}
}

func failureForContext(ctx context.Context, fallback failure) failure {
	if ctx.Err() == nil {
		return fallback
	}
	return classifyRunFailure(ctx)
}

// NormalizeEvents accepts only the bounded, content-free event DTO that may
// be persisted by the authority. Callers must retain native detail separately
// in a protected local transcript.
func NormalizeEvents(events []Event) ([]Event, error) {
	if len(events) > maxRunEvents {
		events = events[:maxRunEvents]
	}
	result := make([]Event, len(events))
	for index, event := range events {
		switch event.Kind {
		case EventProgress, EventCompleted, EventFailed:
			result[index] = event
		default:
			return nil, fmt.Errorf("broker runner returned an invalid event kind")
		}
	}
	return result, nil
}

func authorityContext(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

func buildPrompt(fixed, caller string, attachments []Attachment) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "# Fixed instructions\n\n%s\n\n# Caller request\n\n%s", fixed, caller)
	if len(attachments) > 0 {
		prompt.WriteString("\n\n# Declared attachments")
		for _, attachment := range attachments {
			fmt.Fprintf(&prompt, "\n\n## %s\n\n%s", attachment.Name, attachment.Prompt)
		}
	}
	return prompt.String()
}

func digestInput(caller string, attachments []string) string {
	encoded, _ := json.Marshal(struct {
		Caller      string   `json:"caller"`
		Attachments []string `json:"attachments"`
	}{Caller: caller, Attachments: attachments})
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func cloneInstructions(source map[Tool]string) map[Tool]string {
	result := make(map[Tool]string, len(source))
	for tool, instructions := range source {
		result[tool] = instructions
	}
	return result
}
