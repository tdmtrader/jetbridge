package agentchildexecutions_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/broker/transport"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/api/agentchildexecutions"
)

func TestHandlerRejectsUnauthorizedAndStrictlyMalformedAdmission(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	profile := resolvedAuthorityProfile(t)
	key := []byte(strings.Repeat("k", 32))
	signer, err := agentchildexecutions.NewCapabilitySigner("key-1", key)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := agentchildexecutions.NewCapabilityVerifier("key-1", key)
	if err != nil {
		t.Fatal(err)
	}
	scope := completeScope()
	bootstrap, err := signer.MintBootstrap(scope, []broker.Profile{profile}, now.Add(-time.Second), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := agentchildexecutions.NewHandler(agentchildexecutions.HandlerConfig{Signer: signer, Verifier: verifier, Store: &fakeStore{}, Sealer: &fakeSealer{}, ExecutionCapabilityTTL: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	for name, request := range map[string]*http.Request{
		"absent bearer":      httptest.NewRequest(http.MethodPost, transport.AdmitPath, strings.NewReader(`{}`)),
		"wrong method":       httptest.NewRequest(http.MethodGet, transport.AdmitPath, nil),
		"wrong content type": httptest.NewRequest(http.MethodPost, transport.AdmitPath, strings.NewReader(`{}`)),
		"unknown field":      httptest.NewRequest(http.MethodPost, transport.AdmitPath, strings.NewReader(`{"unknown":true}`)),
		"trailing json":      httptest.NewRequest(http.MethodPost, transport.AdmitPath, strings.NewReader(`{} {}`)),
	} {
		t.Run(name, func(t *testing.T) {
			if name != "absent bearer" && name != "wrong method" {
				request.Header.Set("Authorization", "Bearer "+bootstrap)
			}
			if name != "wrong method" && name != "wrong content type" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code < 400 || response.Code >= 500 {
				t.Fatalf("status = %d, body=%q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), bootstrap) {
				t.Fatal("capability leaked")
			}
		})
	}
	mismatch := httptest.NewRequest(http.MethodPost, transport.AdmitPath, strings.NewReader(`{"idempotency_key":"call","tool":"consult_agent","selector":{"tier":"balanced","effort":"high"},"profile_id":"profile","profile_digest":"sha256:`+strings.Repeat("d", 64)+`","input_digest":"sha256:`+strings.Repeat("c", 64)+`","attachments":["design"]}`))
	mismatch.Header.Set("Authorization", "Bearer "+bootstrap)
	mismatch.Header.Set("Content-Type", "application/json")
	mismatchResponse := httptest.NewRecorder()
	handler.ServeHTTP(mismatchResponse, mismatch)
	if mismatchResponse.Code != http.StatusBadRequest {
		t.Fatalf("profile mismatch status = %d", mismatchResponse.Code)
	}

	overseized := httptest.NewRequest(http.MethodPost, transport.AdmitPath, strings.NewReader(`{"idempotency_key":"`+strings.Repeat("x", (4<<20)+1)+`"}`))
	overseized.Header.Set("Authorization", "Bearer "+bootstrap)
	overseized.Header.Set("Content-Type", "application/json")
	overseizedResponse := httptest.NewRecorder()
	handler.ServeHTTP(overseizedResponse, overseized)
	if overseizedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d", overseizedResponse.Code)
	}
}

func TestNewHandlerRequiresBoundedCapabilityTTLAndMatchingKeyPair(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	otherKey := []byte(strings.Repeat("o", 32))
	signer, err := agentchildexecutions.NewCapabilitySigner("key-1", key)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := agentchildexecutions.NewCapabilityVerifier("key-1", key)
	if err != nil {
		t.Fatal(err)
	}
	for name, config := range map[string]agentchildexecutions.HandlerConfig{
		"maximum ttl accepted": {Signer: signer, Verifier: verifier, Store: &fakeStore{}, Sealer: &fakeSealer{}, ExecutionCapabilityTTL: agentchildexecutions.MaxExecutionCapabilityTTL},
		"ttl too long":         {Signer: signer, Verifier: verifier, Store: &fakeStore{}, Sealer: &fakeSealer{}, ExecutionCapabilityTTL: agentchildexecutions.MaxExecutionCapabilityTTL + time.Second},
		"key id mismatch": func() agentchildexecutions.HandlerConfig {
			wrong, _ := agentchildexecutions.NewCapabilityVerifier("key-2", key)
			return agentchildexecutions.HandlerConfig{Signer: signer, Verifier: wrong, Store: &fakeStore{}, Sealer: &fakeSealer{}, ExecutionCapabilityTTL: time.Minute}
		}(),
		"key mismatch": func() agentchildexecutions.HandlerConfig {
			wrong, _ := agentchildexecutions.NewCapabilityVerifier("key-1", otherKey)
			return agentchildexecutions.HandlerConfig{Signer: signer, Verifier: wrong, Store: &fakeStore{}, Sealer: &fakeSealer{}, ExecutionCapabilityTTL: time.Minute}
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := agentchildexecutions.NewHandler(config)
			if name == "maximum ttl accepted" && err != nil {
				t.Fatalf("NewHandler(): %v", err)
			}
			if name != "maximum ttl accepted" && err == nil {
				t.Fatal("NewHandler() succeeded")
			}
		})
	}
}

func TestHandlerRefusesAdmissionWhenProfileOutlivesExecutionCapability(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	profileInput := authorityProfile()
	profileInput.Limits.Timeout = agentchildexecutions.MaxExecutionCapabilityTTL + time.Second
	catalog, err := broker.NewCatalog([]broker.Profile{profileInput})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(broker.ToolConsultAgent, profileInput.Selector)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	signer, _ := agentchildexecutions.NewCapabilitySigner("key-1", key)
	verifier, _ := agentchildexecutions.NewCapabilityVerifier("key-1", key)
	scope := completeScope()
	bootstrap, _ := signer.MintBootstrap(scope, []broker.Profile{profile}, now.Add(-time.Second), now.Add(time.Minute))
	store := &fakeStore{}
	handler, err := agentchildexecutions.NewHandler(agentchildexecutions.HandlerConfig{Signer: signer, Verifier: verifier, Store: store, Sealer: &fakeSealer{}, ExecutionCapabilityTTL: agentchildexecutions.MaxExecutionCapabilityTTL, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(transport.AdmitRequest{IdempotencyKey: "call", Tool: broker.ToolConsultAgent, Selector: profile.Selector, ProfileID: profile.ID, ProfileDigest: profile.Digest, InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"}})
	request := httptest.NewRequest(http.MethodPost, transport.AdmitPath, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+bootstrap)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("admission status = %d", response.Code)
	}
	if store.execution.ID != "" {
		t.Fatalf("admission created execution %#v", store.execution)
	}
}

func TestHandlerMintsExecutionCapabilityAndEnforcesExactURLScope(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	profile := resolvedAuthorityProfile(t)
	key := []byte(strings.Repeat("k", 32))
	signer, _ := agentchildexecutions.NewCapabilitySigner("key-1", key)
	verifier, _ := agentchildexecutions.NewCapabilityVerifier("key-1", key)
	scope := completeScope()
	bootstrap, _ := signer.MintBootstrap(scope, []broker.Profile{profile}, now.Add(-time.Second), now.Add(time.Minute))
	store := &fakeStore{}
	handler, err := agentchildexecutions.NewHandler(agentchildexecutions.HandlerConfig{Signer: signer, Verifier: verifier, Store: store, Sealer: &fakeSealer{}, ExecutionCapabilityTTL: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(transport.AdmitRequest{IdempotencyKey: "call", Tool: broker.ToolConsultAgent, Selector: profile.Selector, ProfileID: profile.ID, ProfileDigest: profile.Digest, InputDigest: "sha256:" + strings.Repeat("c", 64), Attachments: []string{"design"}})
	admit := httptest.NewRequest(http.MethodPost, transport.AdmitPath, bytes.NewReader(body))
	admit.Header.Set("Authorization", "Bearer "+bootstrap)
	admit.Header.Set("Content-Type", "application/json")
	admitResponse := httptest.NewRecorder()
	handler.ServeHTTP(admitResponse, admit)
	if admitResponse.Code != http.StatusOK {
		t.Fatalf("admit status = %d body=%s", admitResponse.Code, admitResponse.Body.String())
	}
	var admitted transport.AdmitResponse
	if err := json.NewDecoder(admitResponse.Body).Decode(&admitted); err != nil {
		t.Fatal(err)
	}
	if admitted.ExecutionID == "" || admitted.ExecutionCapability == "" {
		t.Fatalf("admit response = %#v", admitted)
	}
	store.execution.BrokerInstance = scope.BrokerInstance

	phase := func(token, executionID, value string) int {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/internal/agent-child-executions/"+executionID+"/phase", strings.NewReader(`{"phase":"`+value+`"}`))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Code
	}
	if status := phase(admitted.ExecutionCapability, admitted.ExecutionID, "running"); status != http.StatusNoContent {
		t.Fatalf("running phase status = %d", status)
	}
	if status := phase(admitted.ExecutionCapability, admitted.ExecutionID, "capturing"); status != http.StatusConflict {
		t.Fatalf("regressing phase status = %d", status)
	}

	wrongID := "c34a6e95-2e3a-45b0-b3f0-30c4e09acb7d"
	if status := phase(admitted.ExecutionCapability, wrongID, "running"); status != http.StatusUnauthorized {
		t.Fatalf("wrong execution status = %d", status)
	}
	wrongAction, _ := signer.MintBootstrap(scope, []broker.Profile{profile}, now.Add(-time.Second), now.Add(time.Minute))
	if status := phase(wrongAction, admitted.ExecutionID, "running"); status != http.StatusUnauthorized {
		t.Fatalf("wrong action status = %d", status)
	}
	foreign := scope
	foreign.TeamID = 9
	foreignToken, _ := signer.MintExecution(foreign, admitted.ExecutionID, profile, now.Add(-time.Second), now.Add(time.Minute))
	if status := phase(foreignToken, admitted.ExecutionID, "running"); status != http.StatusUnauthorized {
		t.Fatalf("cross-team status = %d", status)
	}
	alternateCatalog, _ := broker.NewCatalog([]broker.Profile{func() broker.Profile {
		candidate := authorityProfile()
		candidate.Provider.Model = "other-model"
		return candidate
	}()})
	alternate, _ := alternateCatalog.Resolve(broker.ToolConsultAgent, profile.Selector)
	wrongProfile, _ := signer.MintExecution(scope, admitted.ExecutionID, alternate, now.Add(-time.Second), now.Add(time.Minute))
	if status := phase(wrongProfile, admitted.ExecutionID, "running"); status != http.StatusUnauthorized {
		t.Fatalf("wrong profile status = %d", status)
	}
	expired, _ := signer.MintExecution(scope, admitted.ExecutionID, profile, now.Add(-2*time.Minute), now.Add(-time.Minute))
	if status := phase(expired, admitted.ExecutionID, "running"); status != http.StatusUnauthorized {
		t.Fatalf("expired status = %d", status)
	}
	notYet, _ := signer.MintExecution(scope, admitted.ExecutionID, profile, now.Add(time.Minute), now.Add(2*time.Minute))
	if status := phase(notYet, admitted.ExecutionID, "running"); status != http.StatusUnauthorized {
		t.Fatalf("not-yet-valid status = %d", status)
	}
	if status := phase("malformed", admitted.ExecutionID, "running"); status != http.StatusUnauthorized {
		t.Fatalf("malformed status = %d", status)
	}
}

func TestHandlerStagesReviewCapabilityThroughAuthoritativeWorkspaceCapture(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	profile := resolvedReviewProfile(t)
	key := []byte(strings.Repeat("k", 32))
	signer, _ := agentchildexecutions.NewCapabilitySigner("key-1", key)
	verifier, _ := agentchildexecutions.NewCapabilityVerifier("key-1", key)
	scope := completeScope()
	delete(scope.Inputs, "workspace")
	base := snapshot.SnapshotRef{ID: 8, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("8", 64))}
	scope.WorkspaceBase = &base
	bootstrap, _ := signer.MintBootstrap(scope, []broker.Profile{profile}, now.Add(-time.Second), now.Add(time.Minute))
	store := &fakeStore{}
	sealed := snapshot.SnapshotRef{ID: 9, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("9", 64))}
	handler, err := agentchildexecutions.NewHandler(agentchildexecutions.HandlerConfig{
		Signer: signer, Verifier: verifier, Store: store,
		Sealer: &fakeSealer{workspace: sealed}, ExecutionCapabilityTTL: time.Minute,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	do := func(path, token string, body any) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(body)
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	admit := do(transport.AdmitPath, bootstrap, transport.AdmitRequest{
		IdempotencyKey: "review", Tool: broker.ToolRequestReview,
		Selector: profile.Selector, ProfileID: profile.ID, ProfileDigest: profile.Digest,
		InputDigest: "sha256:" + strings.Repeat("c", 64),
		Attachments: []string{"workspace", "validation"},
	})
	if admit.Code != http.StatusOK {
		t.Fatalf("admit status=%d body=%s", admit.Code, admit.Body.String())
	}
	var admitted transport.AdmitResponse
	if err := json.Unmarshal(admit.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	phase := do(transport.PhasePath(admitted.ExecutionID), admitted.ExecutionCapability, transport.PhaseRequest{Phase: "capturing"})
	if phase.Code != http.StatusOK {
		t.Fatalf("capturing status=%d body=%s", phase.Code, phase.Body.String())
	}
	var phased transport.PhaseResponse
	if err := json.Unmarshal(phase.Body.Bytes(), &phased); err != nil || phased.ExecutionCapability == "" {
		t.Fatalf("phase response=%#v err=%v", phased, err)
	}
	if response := do(transport.PhasePath(admitted.ExecutionID), phased.ExecutionCapability, transport.PhaseRequest{Phase: "running"}); response.Code != http.StatusUnauthorized {
		t.Fatalf("capture token running status=%d", response.Code)
	}
	capture := broker.WorkspaceCapture{
		BaseCommit: strings.Repeat("1", 40), BaseTree: strings.Repeat("2", 40),
		ResultTree:  strings.Repeat("3", 40),
		PatchDigest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		EntryCount:  1, PolicyRevision: "git-workspace-capture/v2",
	}
	captured := do(transport.WorkspaceCapturePath(admitted.ExecutionID), phased.ExecutionCapability, transport.WorkspaceCaptureRequest{Capture: capture})
	if captured.Code != http.StatusOK {
		t.Fatalf("capture status=%d body=%s", captured.Code, captured.Body.String())
	}
	var captureResponse transport.WorkspaceCaptureResponse
	if err := json.Unmarshal(captured.Body.Bytes(), &captureResponse); err != nil ||
		captureResponse.Snapshot != sealed || captureResponse.ExecutionCapability == "" {
		t.Fatalf("capture response=%#v err=%v", captureResponse, err)
	}
	if response := do(transport.PhasePath(admitted.ExecutionID), captureResponse.ExecutionCapability, transport.PhaseRequest{Phase: "running"}); response.Code != http.StatusNoContent {
		t.Fatalf("lifecycle running status=%d body=%s", response.Code, response.Body.String())
	}
	if response := do(transport.WorkspaceCapturePath(admitted.ExecutionID), captureResponse.ExecutionCapability, transport.WorkspaceCaptureRequest{Capture: capture}); response.Code != http.StatusUnauthorized {
		t.Fatalf("lifecycle token recapture status=%d", response.Code)
	}
}

func resolvedAuthorityProfile(t *testing.T) broker.Profile {
	t.Helper()
	catalog, err := broker.NewCatalog([]broker.Profile{authorityProfile()})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := catalog.Resolve(broker.ToolConsultAgent, authorityProfile().Selector)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}
