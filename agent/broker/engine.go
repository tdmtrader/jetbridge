package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	ExecutionID  string
	ResultType   snapshot.TypeRef
	Subjects     []contracts.Subject
	Body         any
	Profile      Profile
	StaticReview bool
	TestsRun     bool
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
}

type RunResult struct {
	Output       json.RawMessage
	Duration     time.Duration
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
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
	if !containsString(request.Attachments, "workspace") {
		return Result{}, fmt.Errorf("broker request_review: workspace attachment is required")
	}
	if err := validateAttachments(request.Attachments); err != nil {
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
	if err := validateAttachments(request.Attachments); err != nil {
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
	admission := AdmissionRequest{
		IdempotencyKey: request.idempotencyKey, Tool: request.tool,
		Selector: request.selector, ProfileID: profile.ID, ProfileDigest: profile.Digest,
		InputDigest: digestInput(request.callerText, request.attachments),
		Attachments: append([]string(nil), request.attachments...),
	}
	executionID, err := engine.config.Authority.Admit(ctx, admission)
	if err != nil {
		return Result{}, fmt.Errorf("broker: admit child execution: %w", err)
	}
	attachments, err := engine.config.Attachments.Resolve(ctx, request.attachments)
	if err != nil {
		return Result{}, engine.fail(ctx, executionID, "attachment_unknown", err)
	}
	subjects := make([]contracts.Subject, len(attachments))
	for index, attachment := range attachments {
		if attachment.Name == "" || strings.TrimSpace(attachment.Prompt) == "" {
			return Result{}, engine.fail(ctx, executionID, "attachment_unknown",
				fmt.Errorf("attachment %d is incomplete", index))
		}
		if err := attachment.Subject.Validate(); err != nil {
			return Result{}, engine.fail(ctx, executionID, "attachment_unknown", err)
		}
		subjects[index] = attachment.Subject
	}
	if len(subjects) == 0 {
		return Result{}, engine.fail(ctx, executionID, "attachment_unknown",
			fmt.Errorf("authority supplied no primary subject"))
	}
	credential, err := engine.config.Credentials.Resolve(ctx, profile.CredentialSlot)
	if err != nil {
		return Result{}, engine.fail(ctx, executionID, "credential_unavailable", err)
	}
	prompt := buildPrompt(engine.config.Instructions[request.tool], request.callerText, attachments)
	if err := engine.config.Authority.Phase(ctx, executionID, "running"); err != nil {
		return Result{}, fmt.Errorf("broker: mark execution running: %w", err)
	}
	run, err := engine.config.Runner.Run(ctx, RunRequest{
		ExecutionID: executionID, Tool: request.tool, Profile: profile,
		Prompt: prompt, Credential: credential,
	})
	if err != nil {
		return Result{}, engine.fail(ctx, executionID, "provider_rejected", err)
	}
	if err := engine.config.Authority.Phase(ctx, executionID, "validating"); err != nil {
		return Result{}, fmt.Errorf("broker: mark execution validating: %w", err)
	}
	body, err := output.Decode(string(request.tool), run.Output, subjects, int(profile.Limits.MaxOutputBytes))
	if err != nil {
		return Result{}, engine.fail(ctx, executionID, "output_invalid", err)
	}
	if err := engine.config.Authority.Phase(ctx, executionID, "sealing"); err != nil {
		return Result{}, fmt.Errorf("broker: mark execution sealing: %w", err)
	}
	resultType := snapshot.TypeRef("consultation/v1")
	if request.tool == ToolRequestReview {
		resultType = "review/v1"
	}
	sealed, err := engine.config.Authority.Seal(ctx, SealRequest{
		ExecutionID: executionID, ResultType: resultType, Subjects: subjects,
		Body: body, Profile: profile, StaticReview: request.staticReview, TestsRun: false,
	})
	if err != nil {
		return Result{}, engine.fail(ctx, executionID, "sealing_failed", err)
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

func (engine *Engine) fail(ctx context.Context, executionID, code string, cause error) error {
	_ = engine.config.Authority.Phase(ctx, executionID, "errored")
	return &ExecutionError{ExecutionID: executionID, Code: code, Cause: cause}
}

type ExecutionError struct {
	ExecutionID string
	Code        string
	Cause       error
}

func (failure *ExecutionError) Error() string {
	return fmt.Sprintf("broker child %s: %s: %v", failure.ExecutionID, failure.Code, failure.Cause)
}

func (failure *ExecutionError) Unwrap() error { return failure.Cause }

var attachmentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func validateAttachments(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !attachmentNamePattern.MatchString(name) {
			return fmt.Errorf("attachment %q must be a logical name", name)
		}
		if _, found := seen[name]; found {
			return fmt.Errorf("attachment %q is duplicate", name)
		}
		seen[name] = struct{}{}
	}
	return nil
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
