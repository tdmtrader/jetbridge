package runner_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/provider"
	"github.com/concourse/concourse/agent/runner"
)

func TestEncodeRecoverySpecUsesStrictLowercaseTransport(t *testing.T) {
	raw, err := runner.EncodeRecoverySpec(runner.RecoverySpec{
		Mode: checkpoint.FallbackWorkspaceOnly, Adapter: provider.Identity{Name: "test", Version: "v1"},
		ExecutionAttempt: 2, CheckpointGeneration: 1, TranscriptCursor: 3,
		CompletedToolCallIDs: []string{"tool-a", "tool-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"adapter":{"name":"test","version":"v1"}`) {
		t.Fatalf("encoded adapter is not strict lowercase JSON: %s", raw)
	}
	exit, err := runner.Run(context.Background(), recoveryTestConfig(t.TempDir(), &recoveryTestAdapter{}, raw))
	if err != nil || exit != 0 {
		t.Fatalf("strictly encoded recovery spec was rejected: %d, %v", exit, err)
	}
}

func TestRunRecoveryWorkspaceAndZeroStartFreshSessionsOnly(t *testing.T) {
	for _, mode := range []string{"checkpoint_zero", "workspace_only"} {
		t.Run(mode, func(t *testing.T) {
			adapter := &recoveryTestAdapter{}
			dir := t.TempDir()
			exit, err := runner.Run(context.Background(), recoveryTestConfig(dir, adapter, recoveryJSON(mode)))
			if err != nil || exit != 0 {
				t.Fatalf("Run() = %d, %v", exit, err)
			}
			if adapter.starts != 1 || adapter.resumes != 0 {
				t.Fatalf("starts/resumes = %d/%d, want 1/0", adapter.starts, adapter.resumes)
			}
		})
	}
}

func TestRunRecoveryNativeResumeUsesOnlyProvenExplicitResume(t *testing.T) {
	adapter := &recoveryTestAdapter{capabilities: nativeCapabilities(), proof: nativeProof()}
	dir := t.TempDir()
	exit, err := runner.Run(context.Background(), recoveryTestConfig(dir, adapter, recoveryJSON("native_resume")))
	if err != nil || exit != 0 {
		t.Fatalf("Run() = %d, %v", exit, err)
	}
	if adapter.starts != 0 || adapter.resumes != 1 {
		t.Fatalf("starts/resumes = %d/%d, want 0/1", adapter.starts, adapter.resumes)
	}
	if adapter.resumeRequest.SessionID != "session-1" || adapter.resumeRequest.ExecutionAttempt != 2 {
		t.Fatalf("resume request = %#v", adapter.resumeRequest)
	}
}

func TestRunRecoveryNativeResumeFailureNeverStartsFreshSession(t *testing.T) {
	adapter := &recoveryTestAdapter{
		capabilities: nativeCapabilities(), proof: nativeProof(), resumeErr: errors.New("resume failed"),
	}
	dir := t.TempDir()
	exit, err := runner.Run(context.Background(), recoveryTestConfig(dir, adapter, recoveryJSON("native_resume")))
	if err != nil || exit != 2 {
		t.Fatalf("Run() = %d, %v", exit, err)
	}
	if adapter.starts != 0 || adapter.resumes != 1 {
		t.Fatalf("starts/resumes = %d/%d, want 0/1", adapter.starts, adapter.resumes)
	}
}

func TestRunRecoveryRequiresExactAdapterIdentityBeforeStartingProvider(t *testing.T) {
	adapter := &recoveryTestAdapter{}
	dir := t.TempDir()
	raw := `{"mode":"workspace_only","adapter":{"name":"other","version":"v1"},"attempt":2,"generation":1,"cursor":3,"completed_tool_ids":[]}`
	exit, err := runner.Run(context.Background(), recoveryTestConfig(dir, adapter, raw))
	if err == nil || exit != 2 {
		t.Fatalf("Run() = %d, %v", exit, err)
	}
	if adapter.starts != 0 || adapter.resumes != 0 {
		t.Fatalf("mismatched recovery adapter started provider: %d/%d", adapter.starts, adapter.resumes)
	}
}

func TestRunRejectsInvalidRecoverySpecBeforeStartingProvider(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"manual_review","adapter":{"name":"test","version":"v1"},"attempt":2,"generation":1,"cursor":3,"completed_tool_ids":[]}`,
		`{"mode":"workspace_only","adapter":{"name":"test","version":"v1"},"attempt":2,"attempt":3,"generation":1,"cursor":3,"completed_tool_ids":[]}`,
		`{"mode":"workspace_only","adapter":{"name":"test","version":"v1"},"attempt":2,"generation":1,"cursor":3,"completed_tool_ids":[],"unknown":true}`,
		`{"mode":"workspace_only","adapter":{"name":"test","version":"v1"},"attempt":2,"generation":1,"cursor":3,"completed_tool_ids":[]} {}`,
	} {
		adapter := &recoveryTestAdapter{}
		dir := t.TempDir()
		exit, err := runner.Run(context.Background(), recoveryTestConfig(dir, adapter, raw))
		if err == nil || exit != 2 {
			t.Fatalf("Run(%s) = %d, %v", raw, exit, err)
		}
		if adapter.starts != 0 || adapter.resumes != 0 {
			t.Fatalf("invalid recovery started provider: %d/%d", adapter.starts, adapter.resumes)
		}
	}
}

func TestRunRejectsNullRequiredRecoveryFieldsBeforeStartingProvider(t *testing.T) {
	for _, field := range []string{"generation", "cursor", "completed_tool_ids"} {
		adapter := &recoveryTestAdapter{}
		dir := t.TempDir()
		raw := `{"mode":"workspace_only","adapter":{"name":"test","version":"v1"},"attempt":2,"generation":1,"cursor":3,"completed_tool_ids":[]}`
		raw = strings.Replace(raw, `"`+field+`":`+map[string]string{"generation": "1", "cursor": "3", "completed_tool_ids": "[]"}[field], `"`+field+`":null`, 1)
		exit, err := runner.Run(context.Background(), recoveryTestConfig(dir, adapter, raw))
		if err == nil || exit != 2 {
			t.Fatalf("Run(%s=null) = %d, %v", field, exit, err)
		}
		if adapter.starts != 0 || adapter.resumes != 0 {
			t.Fatalf("null %s started provider: %d/%d", field, adapter.starts, adapter.resumes)
		}
	}
}

func TestRunRecoveryInjectsFixedNoticeOnlyIntoSystemPrompt(t *testing.T) {
	adapter := &recoveryTestAdapter{}
	dir := t.TempDir()
	exit, err := runner.Run(context.Background(), runner.Config{
		Prompt: "workflow prompt", SystemPrompt: "existing system", FlightDir: filepath.Join(dir, "flight"),
		WorkDir: dir, Adapter: adapter, Stdout: io.Discard, Stderr: io.Discard,
		RecoverySpec: recoveryJSON("workspace_only"),
	})
	if err != nil || exit != 0 {
		t.Fatalf("Run() = %d, %v", exit, err)
	}
	if adapter.startRequest.Prompt != "workflow prompt" {
		t.Fatalf("workflow prompt changed: %q", adapter.startRequest.Prompt)
	}
	for _, required := range []string{"processes", "sockets", "credentials", "mounts", "other ephemeral state"} {
		if !strings.Contains(adapter.startRequest.SystemPrompt, required) {
			t.Fatalf("system prompt misses %q: %q", required, adapter.startRequest.SystemPrompt)
		}
	}
}

type recoveryTestAdapter struct {
	capabilities  provider.Capabilities
	proof         provider.RecoveryProof
	starts        int
	resumes       int
	startRequest  provider.StartRequest
	resumeRequest provider.ResumeRequest
	resumeErr     error
}

func (adapter *recoveryTestAdapter) Identity() provider.Identity {
	return provider.Identity{Name: "test", Version: "v1"}
}

func (adapter *recoveryTestAdapter) Capabilities() provider.Capabilities { return adapter.capabilities }

func (adapter *recoveryTestAdapter) RecoveryProof() provider.RecoveryProof { return adapter.proof }

func (adapter *recoveryTestAdapter) Start(_ context.Context, request provider.StartRequest, _ provider.BoundaryControl) (provider.RunningSession, error) {
	adapter.starts++
	adapter.startRequest = request
	return recoverySession{}, nil
}

func (adapter *recoveryTestAdapter) Resume(_ context.Context, request provider.ResumeRequest, _ provider.BoundaryControl) (provider.RunningSession, error) {
	adapter.resumes++
	adapter.resumeRequest = request.Clone()
	if adapter.resumeErr != nil {
		return nil, adapter.resumeErr
	}
	return recoverySession{}, nil
}

type recoverySession struct{}

func (recoverySession) Wait(context.Context) (provider.Result, error) {
	return provider.Result{Stream: []byte(`{"type":"result","subtype":"success","result":"\"done\"","is_error":false}` + "\n")}, nil
}

func recoveryTestConfig(dir string, adapter provider.Adapter, spec string) runner.Config {
	return runner.Config{
		Prompt: "p", FlightDir: filepath.Join(dir, "flight"), WorkDir: dir, Adapter: adapter,
		Stdout: io.Discard, Stderr: io.Discard, RecoverySpec: spec,
	}
}

func recoveryJSON(mode string) string {
	session := ""
	if mode == "native_resume" {
		session = `,"session_id":"session-1"`
	}
	return `{"mode":"` + mode + `","adapter":{"name":"test","version":"v1"},"attempt":2,"generation":1,"cursor":3,"completed_tool_ids":["tool-1","tool-2"]` + session + `}`
}

func nativeCapabilities() provider.Capabilities {
	return provider.Capabilities{SafeBoundary: true, EffectJournal: true, SessionExport: true, NativeResume: true}
}

func nativeProof() provider.RecoveryProof {
	return provider.RecoveryProof{
		Adapter: provider.Identity{Name: "test", Version: "v1"}, Executable: "test --version: 1", SessionFormat: "test-session-v1",
		SafeBoundary: true, EffectJournal: true, SessionExport: true, NativeResume: true,
	}
}
