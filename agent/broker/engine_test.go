package broker_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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
	if !strings.Contains(runner.prompt, "fixed consult instructions") ||
		!strings.Contains(runner.prompt, "Which risk matters?") ||
		!strings.Contains(runner.prompt, "attachment design") {
		t.Fatalf("fresh prompt = %q", runner.prompt)
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
				Question:       "question",
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

type fakeAuthority struct {
	mu        sync.Mutex
	admitted  bool
	admission broker.AdmissionRequest
	phases    []string
	seal      broker.SealRequest
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
	if len(result) == 0 {
		result = append(result, broker.Attachment{
			Name: "question", Prompt: "question only",
			Subject: contracts.Subject{
				ID: "primary", Role: contracts.SubjectRolePrimary, Input: "question",
				Type: "opaque/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("b", 64)),
			},
		})
	}
	return result, nil
}

type fakeCredentials struct{}

func (fakeCredentials) Resolve(context.Context, string) (string, error) { return "secret", nil }

type fakeRunner struct {
	output  []byte
	prompt  string
	barrier chan struct{}
	started int32
}

func (runner *fakeRunner) Run(_ context.Context, request broker.RunRequest) (broker.RunResult, error) {
	runner.prompt = request.Prompt
	atomic.AddInt32(&runner.started, 1)
	if runner.barrier != nil {
		<-runner.barrier
	}
	return broker.RunResult{Output: append(json.RawMessage(nil), runner.output...)}, nil
}
