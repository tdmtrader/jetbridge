package broker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
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
	if authority.admission.ProfileDigest == "" || authority.seal.StaticReview {
		t.Fatalf("authority requests = %#v %#v", authority.admission, authority.seal)
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
		IdempotencyKey: "deadline", Selector: profile.Selector, Question: "question",
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
		Question:       "question",
	})
	if err == nil || !strings.Contains(err.Error(), "provider_rejected") {
		t.Fatalf("ConsultAgent() error = %v, want safe event rejection", err)
	}
	if len(authority.updates) != 0 {
		t.Fatalf("unsafe events reached authority: %#v", authority.updates)
	}
}

type fakeAuthority struct {
	mu        sync.Mutex
	admitted  bool
	admission broker.AdmissionRequest
	phases    []string
	seal      broker.SealRequest
	updates   []broker.RunUpdate
	terminals []broker.Terminal
}

func (authority *fakeAuthority) Admit(_ context.Context, request broker.AdmissionRequest) (string, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.admitted = true
	authority.admission = request
	return "child-1", nil
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
	return nil
}
func (authority *fakeAuthority) Seal(_ context.Context, request broker.SealRequest) (snapshot.SnapshotRef, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.seal = request
	return snapshot.SnapshotRef{
		ID: 101, Type: request.ResultType, Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64)),
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

type fakeRunner struct {
	mu      sync.Mutex
	output  []byte
	prompts []string
	barrier chan struct{}
	started int32
}

func (runner *fakeRunner) Run(_ context.Context, request broker.RunRequest) (broker.RunResult, error) {
	runner.mu.Lock()
	runner.prompts = append(runner.prompts, request.Prompt)
	runner.mu.Unlock()
	atomic.AddInt32(&runner.started, 1)
	if runner.barrier != nil {
		<-runner.barrier
	}
	return broker.RunResult{Output: append(json.RawMessage(nil), runner.output...)}, nil
}

func (runner *fakeRunner) lastPrompt() string {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.prompts[len(runner.prompts)-1]
}
