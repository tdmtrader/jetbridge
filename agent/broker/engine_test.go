package broker_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/workspace"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestEngineRunsConsultationThroughDurablePhases(t *testing.T) {
	catalog, err := broker.NewCatalog([]broker.Profile{validProfile()})
	if err != nil {
		t.Fatal(err)
	}
	authority := &fakeAuthority{}
	runner := &fakeRunner{output: []byte(
		`{"answer":"Keep it.","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`,
	)}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Attachments: fakeAttachments{},
		Credentials: fakeCredentials{}, Runner: runner,
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed consult instructions"},
	})
	result, err := engine.ConsultAgent(context.Background(), broker.ConsultRequest{
		IdempotencyKey: "call-1",
		Selector:       broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Question:       "Which risk matters?", Context: "One compatibility release.",
		Attachments: []string{"design"},
	})
	if err != nil {
		t.Fatalf("ConsultAgent(): %v", err)
	}
	if result.ExecutionID != "child-1" || result.Profile.ID != "balanced-high-v1" ||
		result.Snapshot.ID != 101 {
		t.Fatalf("result = %#v", result)
	}
	if _, ok := result.Body.(contracts.ConsultationBody); !ok {
		t.Fatalf("body type = %T", result.Body)
	}
	if got := strings.Join(authority.phases, ","); got != "running,validating,sealing" {
		t.Fatalf("phases = %s", got)
	}
	prompt := runner.lastPrompt()
	if !strings.Contains(prompt, "fixed consult instructions") ||
		!strings.Contains(prompt, "Which risk matters?") ||
		!strings.Contains(prompt, "attachment design") {
		t.Fatalf("fresh prompt = %q", prompt)
	}
	if len(runner.lastRequest().Attachments) != 1 || runner.lastRequest().Attachments[0].Name != "design" {
		t.Fatalf("runner attachments = %#v", runner.lastRequest().Attachments)
	}
	if authority.admission.ProfileDigest == "" || !json.Valid(authority.seal.Body) {
		t.Fatalf("authority requests = %#v %#v", authority.admission, authority.seal)
	}
}

func TestEngineReturnsDurableSucceededReplayWithoutInvokingRunner(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{validProfile()})
	authority := &fakeAuthority{replay: &broker.SucceededReplay{Snapshot: snapshot.SnapshotRef{ID: 101, Type: "consultation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64))}, Body: []byte(`{"answer":"cached","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`)}}
	runner := &fakeRunner{}
	engine := broker.NewEngine(broker.EngineConfig{Catalog: catalog, Authority: authority, Attachments: fakeAttachments{}, Credentials: fakeCredentials{}, Runner: runner, Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"}})
	result, err := engine.ConsultAgent(context.Background(), broker.ConsultRequest{IdempotencyKey: "call", Selector: validProfile().Selector, Question: "question", Attachments: []string{"design"}})
	if err != nil || result.Snapshot.ID != 101 || runner.started != 0 {
		t.Fatalf("result=%#v err=%v runner=%d", result, err, runner.started)
	}
}

func TestEngineCapturesReviewWorkspaceAfterAdmissionAndRunsExactCapture(t *testing.T) {
	profileInput := validProfile()
	profileInput.Tools = []broker.Tool{broker.ToolRequestReview}
	catalog, _ := broker.NewCatalog([]broker.Profile{profileInput})
	authority := &fakeAuthority{}
	preparer := &fakeWorkspacePreparer{capture: workspace.Result{
		RepositoryRoot: "/parent/repository",
		BaseCommit:     strings.Repeat("1", 40), BaseTree: strings.Repeat("2", 40),
		ResultTree: strings.Repeat("3", 40), Patch: []byte("patch"),
		PatchDigest: "sha256:a4895eb44afc336fecbba6e520cd67e178dace0276655d102fceffa8e5f70570", EntryCount: 7,
		PolicyRevision: "git-workspace-capture/v2",
	}}
	runner := &fakeRunner{output: []byte(`{"conclusion":"accept","summary":"ok","findings":[]}`)}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Workspace: preparer,
		Attachments: reviewAttachments{}, Credentials: fakeCredentials{}, Runner: runner,
		Instructions: map[broker.Tool]string{broker.ToolRequestReview: "fixed review"},
	})
	result, err := engine.RequestReview(context.Background(), broker.ReviewRequest{
		IdempotencyKey: "review-capture", Selector: profileInput.Selector,
		Attachments: []string{"workspace", "validation"},
	})
	if err != nil {
		t.Fatalf("RequestReview(): %v", err)
	}
	if result.Snapshot.ID != 101 || preparer.calls != 1 {
		t.Fatalf("result=%#v capture calls=%d", result, preparer.calls)
	}
	if got := strings.Join(authority.phases, ","); got != "capturing,running,validating,sealing" {
		t.Fatalf("phases = %s", got)
	}
	if string(authority.workspace.Patch) != "patch" {
		t.Fatalf("authority capture = %#v", authority.workspace)
	}
	request := runner.lastRequest()
	if request.Workspace == nil || request.Workspace.RepositoryRoot != "/parent/repository" {
		t.Fatalf("runner workspace = %#v", request.Workspace)
	}
	if len(request.Attachments) != 2 || request.Attachments[0].Name != "workspace" ||
		request.Attachments[0].Subject.Type != "repository-change/v1" {
		t.Fatalf("runner attachments = %#v", request.Attachments)
	}
}

func TestEngineTerminalReplayDoesNotCaptureWorkspace(t *testing.T) {
	profileInput := validProfile()
	profileInput.Tools = []broker.Tool{broker.ToolRequestReview}
	catalog, _ := broker.NewCatalog([]broker.Profile{profileInput})
	authority := &fakeAuthority{terminalReplay: &broker.Terminal{
		State: broker.ExecutionErrored, Code: "workspace_capture_failed", Retryable: false,
	}}
	preparer := &fakeWorkspacePreparer{}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Workspace: preparer,
		Attachments: fakeAttachments{}, Credentials: fakeCredentials{}, Runner: &fakeRunner{},
		Instructions: map[broker.Tool]string{broker.ToolRequestReview: "fixed review"},
	})
	_, err := engine.RequestReview(context.Background(), broker.ReviewRequest{
		IdempotencyKey: "review-replay", Selector: profileInput.Selector,
		Attachments: []string{"workspace"},
	})
	if err == nil || !strings.Contains(err.Error(), "workspace_capture_failed") {
		t.Fatalf("RequestReview() error = %v", err)
	}
	if preparer.calls != 0 {
		t.Fatalf("capture calls = %d", preparer.calls)
	}
}

func TestEngineRetriesExactWorkspaceCaptureAfterAmbiguousResponse(t *testing.T) {
	profileInput := validProfile()
	profileInput.Tools = []broker.Tool{broker.ToolRequestReview}
	catalog, _ := broker.NewCatalog([]broker.Profile{profileInput})
	authority := &fakeAuthority{captureErrors: []error{errors.New("response lost"), nil}}
	preparer := &fakeWorkspacePreparer{capture: workspace.Result{
		RepositoryRoot: "/parent/repository",
		BaseCommit:     strings.Repeat("1", 40), BaseTree: strings.Repeat("2", 40),
		ResultTree: strings.Repeat("3", 40), Patch: []byte("patch"),
		PatchDigest: "sha256:a4895eb44afc336fecbba6e520cd67e178dace0276655d102fceffa8e5f70570",
		EntryCount:  7, PolicyRevision: "git-workspace-capture/v2",
	}}
	runner := &fakeRunner{output: []byte(`{"conclusion":"accept","summary":"ok","findings":[]}`)}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Workspace: preparer,
		Attachments: reviewAttachments{}, Credentials: fakeCredentials{}, Runner: runner,
		Instructions: map[broker.Tool]string{broker.ToolRequestReview: "fixed review"},
	})
	_, err := engine.RequestReview(context.Background(), broker.ReviewRequest{
		IdempotencyKey: "ambiguous-capture", Selector: profileInput.Selector,
		Attachments: []string{"workspace"},
	})
	if err != nil {
		t.Fatalf("RequestReview(): %v", err)
	}
	if authority.captureCalls != 2 || authority.captureFailureCalls != 0 ||
		preparer.calls != 1 || atomic.LoadInt32(&runner.started) != 1 {
		t.Fatalf("capture calls=%d failure calls=%d prepare=%d runner=%d",
			authority.captureCalls, authority.captureFailureCalls,
			preparer.calls, runner.started)
	}
}

func TestEngineValidatesReviewWorkspaceBeforeAdmission(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{validProfile()})
	authority := &fakeAuthority{}
	engine := broker.NewEngine(broker.EngineConfig{Catalog: catalog, Authority: authority})
	_, err := engine.RequestReview(context.Background(), broker.ReviewRequest{
		IdempotencyKey: "review-1",
		Selector:       broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
	})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("RequestReview() error = %v", err)
	}
	if authority.admitted {
		t.Fatal("invalid review was admitted")
	}
}

func TestEngineDoesNotSerializeConcurrentCalls(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{validProfile()})
	runner := &fakeRunner{
		output:  []byte(`{"answer":"ok","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`),
		barrier: make(chan struct{}),
	}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: &fakeAuthority{}, Attachments: fakeAttachments{},
		Credentials: fakeCredentials{}, Runner: runner,
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"},
	})
	var wait sync.WaitGroup
	wait.Add(2)
	errors := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			defer wait.Done()
			_, err := engine.ConsultAgent(context.Background(), broker.ConsultRequest{
				IdempotencyKey: "parallel",
				Selector:       broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
				Question:       "question", Attachments: []string{"design"},
			})
			errors <- err
		}()
	}
	for atomic.LoadInt32(&runner.started) < 2 {
	}
	close(runner.barrier)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestEngineBoundsRunnerContextAndTerminalizesDeadline(t *testing.T) {
	profile := validProfile()
	profile.Limits.Timeout = 25 * time.Millisecond
	catalog, err := broker.NewCatalog([]broker.Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	authority := &fakeAuthority{}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Attachments: fakeAttachments{},
		Credentials: fakeCredentials{}, Runner: deadlineRunner{maximum: 100 * time.Millisecond},
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"},
	})
	caller, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = engine.ConsultAgent(caller, broker.ConsultRequest{
		IdempotencyKey: "deadline", Selector: profile.Selector, Question: "question", Attachments: []string{"design"},
	})
	if err == nil || !strings.Contains(err.Error(), "deadline_exceeded") {
		t.Fatalf("ConsultAgent() error = %v, want deadline_exceeded", err)
	}
	if got := strings.Join(authority.phases, ","); got != "running,timed_out" {
		t.Fatalf("phases = %s, want running,timed_out", got)
	}
}

func TestEngineClassifiesPreflightDeadlineBeforeRunning(t *testing.T) {
	profile := validProfile()
	profile.Limits.Timeout = 25 * time.Millisecond
	catalog, _ := broker.NewCatalog([]broker.Profile{profile})
	authority := &fakeAuthority{}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Attachments: deadlineAttachments{},
		Credentials: fakeCredentials{}, Runner: &fakeRunner{},
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"},
	})
	_, err := engine.ConsultAgent(context.Background(), broker.ConsultRequest{
		IdempotencyKey: "preflight-deadline", Selector: profile.Selector, Question: "question",
		Attachments: []string{"design"},
	})
	if err == nil || !strings.Contains(err.Error(), "deadline_exceeded") {
		t.Fatalf("ConsultAgent() error = %v, want deadline_exceeded", err)
	}
	if got := strings.Join(authority.phases, ","); got != "timed_out" {
		t.Fatalf("phases = %s, want timed_out", got)
	}
}

func TestEngineTerminalizesCallerCancellationDistinctly(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{validProfile()})
	authority := &fakeAuthority{}
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Attachments: fakeAttachments{},
		Credentials: fakeCredentials{}, Runner: cancellationRunner{cancel: cancel},
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"},
	})
	_, err := engine.ConsultAgent(caller, broker.ConsultRequest{
		IdempotencyKey: "cancelled", Selector: broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Question: "question", Attachments: []string{"design"},
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("ConsultAgent() error = %v, want cancelled", err)
	}
	if len(authority.terminals) != 1 || authority.terminals[0].State != broker.ExecutionCancelled ||
		authority.terminals[0].Code != "cancelled" {
		t.Fatalf("terminal = %#v", authority.terminals)
	}
}

func TestEngineRejectsAttachmentsOutsideTheToolAllowlistBeforeAdmission(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{validProfile()})
	authority := &fakeAuthority{}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Attachments: fakeAttachments{},
		Credentials: fakeCredentials{}, Runner: &fakeRunner{output: []byte(
			`{"answer":"ok","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`,
		)},
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"},
	})
	_, err := engine.ConsultAgent(context.Background(), broker.ConsultRequest{
		IdempotencyKey: "unknown-attachment",
		Selector:       broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Question:       "question",
		Attachments:    []string{"unapproved"},
	})
	if err == nil || !strings.Contains(err.Error(), "attachment") {
		t.Fatalf("ConsultAgent() error = %v, want attachment rejection", err)
	}
	if authority.admitted {
		t.Fatal("unapproved attachment was admitted")
	}
}

func TestEngineRequiresConsultAttachmentBeforeAdmission(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{validProfile()})
	authority := &fakeAuthority{}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Attachments: fakeAttachments{},
		Credentials: fakeCredentials{}, Runner: &fakeRunner{},
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"},
	})
	_, err := engine.ConsultAgent(context.Background(), broker.ConsultRequest{
		IdempotencyKey: "empty-attachments",
		Selector:       broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Question:       "question",
	})
	if err == nil || !strings.Contains(err.Error(), "attachment") {
		t.Fatalf("ConsultAgent() error = %v, want attachment rejection", err)
	}
	if authority.admitted {
		t.Fatal("consultation without attachments was admitted")
	}
}

func TestEngineReturnsSafeErrorWhenTerminalPersistenceFails(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{validProfile()})
	authority := &fakeAuthority{terminalErr: errors.New("database unavailable")}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Attachments: fakeAttachments{},
		Credentials: fakeCredentials{}, Runner: failingRunner{},
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"},
	})
	_, err := engine.ConsultAgent(context.Background(), broker.ConsultRequest{
		IdempotencyKey: "terminal-write", Selector: broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Question: "question", Attachments: []string{"design"},
	})
	if err == nil || !strings.Contains(err.Error(), "record terminal") || strings.Contains(err.Error(), "provider body") {
		t.Fatalf("ConsultAgent() error = %v, want safe terminal persistence error", err)
	}
}

func TestEngineRejectsResolverAttachmentNameMismatchBeforePrompt(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{validProfile()})
	runner := &fakeRunner{output: []byte(
		`{"answer":"ok","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`,
	)}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: &fakeAuthority{}, Attachments: renamedAttachments{},
		Credentials: fakeCredentials{}, Runner: runner,
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"},
	})
	_, err := engine.ConsultAgent(context.Background(), broker.ConsultRequest{
		IdempotencyKey: "name-mismatch",
		Selector:       broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Question:       "question",
		Attachments:    []string{"design"},
	})
	if err == nil || !strings.Contains(err.Error(), "attachment_unknown") {
		t.Fatalf("ConsultAgent() error = %v, want attachment_unknown", err)
	}
	if atomic.LoadInt32(&runner.started) != 0 {
		t.Fatal("runner received a prompt with mismatched attachments")
	}
}

func TestEngineRejectsUnnormalizedRunnerEventsBeforeAuthorityPersistence(t *testing.T) {
	catalog, _ := broker.NewCatalog([]broker.Profile{validProfile()})
	authority := &fakeAuthority{}
	engine := broker.NewEngine(broker.EngineConfig{
		Catalog: catalog, Authority: authority, Attachments: fakeAttachments{},
		Credentials: fakeCredentials{}, Runner: eventRunner{},
		Instructions: map[broker.Tool]string{broker.ToolConsultAgent: "fixed"},
	})
	_, err := engine.ConsultAgent(context.Background(), broker.ConsultRequest{
		IdempotencyKey: "unsafe-event",
		Selector:       broker.Selector{Tier: broker.TierBalanced, Effort: broker.EffortHigh},
		Question:       "question", Attachments: []string{"design"},
	})
	if err == nil || !strings.Contains(err.Error(), "provider_rejected") {
		t.Fatalf("ConsultAgent() error = %v, want safe event rejection", err)
	}
	if len(authority.updates) != 0 {
		t.Fatalf("unsafe events reached authority: %#v", authority.updates)
	}
}

type fakeAuthority struct {
	mu                  sync.Mutex
	admitted            bool
	admission           broker.AdmissionRequest
	phases              []string
	seal                broker.SealRequest
	updates             []broker.RunUpdate
	terminals           []broker.Terminal
	terminalErr         error
	replay              *broker.SucceededReplay
	terminalReplay      *broker.Terminal
	workspace           broker.WorkspaceCapture
	captureErrors       []error
	captureCalls        int
	captureFailureCalls int
}

func (authority *fakeAuthority) Admit(_ context.Context, request broker.AdmissionRequest) (broker.Admission, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.admitted = true
	authority.admission = request
	return broker.Admission{ExecutionID: "child-1", Succeeded: authority.replay, Terminal: authority.terminalReplay}, nil
}
func (authority *fakeAuthority) CaptureWorkspace(_ context.Context, _ string, capture broker.WorkspaceCapture) (snapshot.SnapshotRef, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	call := authority.captureCalls
	authority.captureCalls++
	authority.workspace = capture
	if call < len(authority.captureErrors) && authority.captureErrors[call] != nil {
		return snapshot.SnapshotRef{}, authority.captureErrors[call]
	}
	return snapshot.SnapshotRef{ID: 77, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("7", 64))}, nil
}
func (authority *fakeAuthority) FailWorkspaceCapture(_ context.Context, _ string) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.captureFailureCalls++
	return nil
}
func (authority *fakeAuthority) Phase(_ context.Context, _ string, phase string) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.phases = append(authority.phases, phase)
	return nil
}
func (authority *fakeAuthority) Update(_ context.Context, _ string, update broker.RunUpdate) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.updates = append(authority.updates, update)
	return nil
}
func (authority *fakeAuthority) Terminal(_ context.Context, _ string, terminal broker.Terminal) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.phases = append(authority.phases, string(terminal.State))
	authority.terminals = append(authority.terminals, terminal)
	return authority.terminalErr
}
func (authority *fakeAuthority) Seal(_ context.Context, request broker.SealRequest) (snapshot.SnapshotRef, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.seal = request
	return snapshot.SnapshotRef{
		ID: 101, Type: "consultation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64)),
	}, nil
}

type fakeAttachments struct{}

func (fakeAttachments) Resolve(_ context.Context, names []string) ([]broker.Attachment, error) {
	result := make([]broker.Attachment, len(names))
	for index, name := range names {
		result[index] = broker.Attachment{
			Name: name, Prompt: "attachment " + name,
			Subject: contracts.Subject{
				ID: "primary", Role: contracts.SubjectRolePrimary, Input: name,
				Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
			},
		}
	}
	return result, nil
}

type reviewAttachments struct{}

func (reviewAttachments) Resolve(_ context.Context, names []string) ([]broker.Attachment, error) {
	result := make([]broker.Attachment, 0, len(names))
	for _, name := range names {
		if name != "validation" {
			return nil, fmt.Errorf("unexpected review attachment %q", name)
		}
		result = append(result, broker.Attachment{
			Name: name, Prompt: "attachment validation",
			Subject: contracts.Subject{
				ID: "validation", Role: contracts.SubjectRoleEvidence, Input: name,
				Type: "validation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("b", 64)),
			},
		})
	}
	return result, nil
}

type fakeCredentials struct{}

func (fakeCredentials) Resolve(context.Context, string) (string, error) { return "secret", nil }

type deadlineRunner struct{ maximum time.Duration }

func (runner deadlineRunner) Run(ctx context.Context, _ broker.RunRequest) (broker.RunResult, error) {
	deadline, found := ctx.Deadline()
	if !found || time.Until(deadline) > runner.maximum {
		return broker.RunResult{}, fmt.Errorf("profile deadline was not applied")
	}
	<-ctx.Done()
	return broker.RunResult{}, ctx.Err()
}

type renamedAttachments struct{}

func (renamedAttachments) Resolve(context.Context, []string) ([]broker.Attachment, error) {
	return []broker.Attachment{{
		Name: "workspace", Prompt: "wrong attachment",
		Subject: contracts.Subject{
			ID: "primary", Role: contracts.SubjectRolePrimary, Input: "workspace",
			Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
		},
	}}, nil
}

type deadlineAttachments struct{}

func (deadlineAttachments) Resolve(ctx context.Context, _ []string) ([]broker.Attachment, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type cancellationRunner struct{ cancel context.CancelFunc }

func (runner cancellationRunner) Run(ctx context.Context, _ broker.RunRequest) (broker.RunResult, error) {
	runner.cancel()
	<-ctx.Done()
	return broker.RunResult{}, ctx.Err()
}

type eventRunner struct{}

func (eventRunner) Run(context.Context, broker.RunRequest) (broker.RunResult, error) {
	return broker.RunResult{
		Output: []byte(`{"answer":"ok","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`),
		Events: []broker.Event{{Kind: "native TOKEN=super-secret"}},
	}, nil
}

type failingRunner struct{}

func (failingRunner) Run(context.Context, broker.RunRequest) (broker.RunResult, error) {
	return broker.RunResult{}, errors.New("provider body must not escape")
}

type fakeRunner struct {
	mu       sync.Mutex
	output   []byte
	prompts  []string
	requests []broker.RunRequest
	barrier  chan struct{}
	started  int32
}

type fakeWorkspacePreparer struct {
	capture workspace.Result
	err     error
	calls   int
}

func (preparer *fakeWorkspacePreparer) CaptureWorkspace(context.Context) (workspace.Result, error) {
	preparer.calls++
	return preparer.capture, preparer.err
}

func (runner *fakeRunner) Run(_ context.Context, request broker.RunRequest) (broker.RunResult, error) {
	runner.mu.Lock()
	runner.prompts = append(runner.prompts, request.Prompt)
	runner.requests = append(runner.requests, request)
	runner.mu.Unlock()
	atomic.AddInt32(&runner.started, 1)
	if runner.barrier != nil {
		<-runner.barrier
	}
	return broker.RunResult{Output: append(json.RawMessage(nil), runner.output...)}, nil
}

func (runner *fakeRunner) lastRequest() broker.RunRequest {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.requests[len(runner.requests)-1]
}

func (runner *fakeRunner) lastPrompt() string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.prompts[len(runner.prompts)-1]
}
