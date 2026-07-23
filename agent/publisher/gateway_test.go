package publisher_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
)

const (
	testBaseSHA   = "1111111111111111111111111111111111111111"
	testResultSHA = "2222222222222222222222222222222222222222"
	testTreeSHA   = "3333333333333333333333333333333333333333"
)

func TestGatewayExecutorStreamsExactAuthorizedRepositoryChange(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	var mu sync.Mutex
	var paths []string
	var archive []byte
	var operationKey string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths = append(paths, request.URL.Path)
		mu.Unlock()
		if got := request.Header.Get("Authorization"); got != "Bearer mounted-token" {
			t.Errorf("Authorization = %q", got)
		}
		if request.URL.Path != "/v1/git/current-base" && request.Header.Get("Idempotency-Key") == "" {
			t.Errorf("%s omitted canonical Idempotency-Key", request.URL.Path)
		}
		switch request.URL.Path {
		case "/v1/publications/lookup":
			writeGatewayJSON(t, response, map[string]any{"found": false})
		case "/v1/git/current-base":
			writeGatewayJSON(t, response, map[string]string{"base_sha": testBaseSHA})
		case "/v1/git/publish":
			operationKey = request.Header.Get("Idempotency-Key")
			reader, err := request.MultipartReader()
			if err != nil {
				t.Errorf("MultipartReader: %v", err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			operationPart, err := reader.NextPart()
			if err != nil || operationPart.FormName() != "operation" {
				t.Errorf("operation part = %q, %v", operationPart.FormName(), err)
				return
			}
			var operation struct {
				OperationKey string                      `json:"operation_key"`
				Input        snapshot.SnapshotRef        `json:"input"`
				Authority    publisher.Authority         `json:"authority"`
				Approval     *publisher.ApprovalEvidence `json:"approval,omitempty"`
			}
			if err := json.NewDecoder(operationPart).Decode(&operation); err != nil {
				t.Errorf("decode operation: %v", err)
			}
			if operation.OperationKey != operationKey || operation.Input != fixture.ref || operation.Authority.WorkflowRunID != 17 {
				t.Errorf("operation = %+v", operation)
			}
			contentPart, err := reader.NextPart()
			if err != nil || contentPart.FormName() != "snapshot" || contentPart.FileName() != "snapshot.tar" {
				t.Errorf("snapshot part = %q/%q, %v", contentPart.FormName(), contentPart.FileName(), err)
				return
			}
			archive, err = io.ReadAll(contentPart)
			if err != nil {
				t.Errorf("read snapshot: %v", err)
			}
			writeGatewayJSON(t, response, map[string]string{
				"external_id": "pull-request/7", "url": "https://git.example/pull/7", "head_sha": testResultSHA,
			})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	config := gatewayTestConfig(t, server, `{
		"schema_version": 1,
		"rules": [{
			"team": "engineering",
			"publisher": "git-publisher/v1",
			"modes": ["pull-request"],
			"approval_policy_versions": ["engineering/v1"],
			"target_branches": ["main"],
			"destinations": ["git.example/acme/widget"]
		}]
	}`)
	captureDirectory := t.TempDir()
	config.SnapshotCanonicalizer.TempDir = captureDirectory
	executor, err := publisher.NewGatewayExecutor(
		publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayGitRequest(fixture.ref)
	publication, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if publication.Status != publisher.StatusSucceeded || publication.Result.ExternalID != "pull-request/7" {
		t.Fatalf("publication = %+v", publication)
	}
	if operationKey != publication.OperationKey || !bytes.Equal(archive, fixture.archive) {
		t.Fatalf("gateway received key/archive = %q/%d bytes, want %q/%d", operationKey, len(archive), publication.OperationKey, len(fixture.archive))
	}
	if entries, err := os.ReadDir(captureDirectory); err != nil || len(entries) != 0 {
		t.Fatalf("repository-change materialization was not released: entries=%v err=%v", entries, err)
	}
	_, gotTeam, _ := fixture.metadata.GetAuthorizedArgsForCall(0)
	if got := gotTeam; got != 9 {
		t.Fatalf("snapshot authorization team = %d, want 9", got)
	}
	mu.Lock()
	gotPaths := slices.Clone(paths)
	mu.Unlock()
	if want := []string{"/v1/publications/lookup", "/v1/git/current-base", "/v1/git/publish"}; !slices.Equal(gotPaths, want) {
		t.Fatalf("gateway calls = %v, want %v", gotPaths, want)
	}

	// A terminal durable publication is returned without another provider call
	// or another snapshot materialization.
	if _, err := executor.Execute(context.Background(), request); err != nil {
		t.Fatalf("replay: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(paths, gotPaths) || fixture.metadata.GetAuthorizedCallCount() != 1 {
		t.Fatalf("replay repeated external work: paths=%v metadata_reads=%d", paths, fixture.metadata.GetAuthorizedCallCount())
	}
}

func TestGatewayExecutorRecoversProviderResultBeforeRetryingWrite(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	var paths []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		writeGatewayJSON(t, response, map[string]any{
			"found":  true,
			"result": map[string]string{"external_id": "branch/9", "url": "https://git.example/tree/agent", "head_sha": testResultSHA},
		})
	}))
	defer server.Close()
	config := gatewayTestConfig(t, server, `{
		"schema_version":1,
		"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["pull-request"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/acme/widget"]}]
	}`)
	executor, err := publisher.NewGatewayExecutor(publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := executor.Execute(context.Background(), gatewayGitRequest(fixture.ref))
	if err != nil || publication.Result.ExternalID != "branch/9" {
		t.Fatalf("Execute = (%+v, %v)", publication, err)
	}
	if want := []string{"/v1/publications/lookup"}; !slices.Equal(paths, want) {
		t.Fatalf("gateway calls = %v, want %v", paths, want)
	}
	if fixture.metadata.GetAuthorizedCallCount() != 1 || fixture.content.OpenCallCount() != 1 {
		t.Fatalf("provider recovery did not revalidate the exact snapshot metadata/content: %d/%d",
			fixture.metadata.GetAuthorizedCallCount(), fixture.content.OpenCallCount())
	}
}

func TestGatewayWorkItemPolicySupportsExplicitPrefixAndRotatedToken(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	var authorizations []string
	var published struct {
		OperationKey          string              `json:"operation_key"`
		Destination           string              `json:"destination"`
		ApprovalPolicyVersion string              `json:"approval_policy_version"`
		Authority             publisher.Authority `json:"authority"`
	}
	var publishedArchive []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		switch request.URL.Path {
		case "/v1/publications/lookup":
			writeGatewayJSON(t, response, map[string]any{"found": false})
		case "/v1/work-items/publish":
			reader, err := request.MultipartReader()
			if err != nil {
				t.Errorf("work-item multipart reader: %v", err)
				return
			}
			operationPart, err := reader.NextPart()
			if err != nil || operationPart.FormName() != "operation" {
				t.Errorf("work-item operation part: %v", err)
				return
			}
			if err := json.NewDecoder(operationPart).Decode(&published); err != nil {
				t.Errorf("decode work-item operation: %v", err)
				return
			}
			contentPart, err := reader.NextPart()
			if err != nil || contentPart.FormName() != "snapshot" || contentPart.FileName() != "snapshot.tar" {
				t.Errorf("work-item snapshot part: %v", err)
				return
			}
			publishedArchive, err = io.ReadAll(contentPart)
			if err != nil {
				t.Errorf("read work-item snapshot: %v", err)
				return
			}
			writeGatewayJSON(t, response, map[string]string{"external_id": "comment/4", "url": "https://work.example/ENG-42#comment-4"})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	config := gatewayTestConfig(t, server, `{
		"schema_version":1,
		"rules":[{"team":"engineering","publisher":"work-item-publisher/v1","modes":["comment"],"approval_policy_versions":["engineering/v1"],"destination_prefixes":["ENG-"]}]
	}`)
	if err := os.WriteFile(config.TokenFile, []byte("rotated-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executor, err := publisher.NewGatewayExecutor(publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config)
	if err != nil {
		t.Fatal(err)
	}
	request := publisher.Request{
		Publisher:   publisher.WorkItemPublisher,
		Input:       fixture.ref,
		Destination: "ENG-42", Mode: publisher.ModeComment,
		Parameters: map[string]string{"body": "agent result"}, ApprovalPolicyVersion: "engineering/v1",
		Authority: publisher.Authority{TeamID: 9, TeamName: "engineering", BuildID: 12, WorkflowRunID: 17, Actor: "build/12"},
	}
	publication, err := executor.Execute(context.Background(), request)
	if err != nil || publication.Result.ExternalID != "comment/4" {
		t.Fatalf("Execute = (%+v, %v)", publication, err)
	}
	if !slices.Equal(authorizations, []string{"Bearer rotated-token", "Bearer rotated-token"}) {
		t.Fatalf("authorizations = %v", authorizations)
	}
	if published.OperationKey != publication.OperationKey || published.Destination != "ENG-42" ||
		published.ApprovalPolicyVersion != "engineering/v1" || published.Authority.WorkflowRunID != 17 {
		t.Fatalf("work-item operation = %+v", published)
	}
	if !bytes.Equal(publishedArchive, fixture.archive) {
		t.Fatal("work-item gateway did not receive the exact revalidated snapshot archive")
	}
}

func TestGatewayPolicyDeniesUnlistedTeamDestinationWithoutNetworkAndDoesNotLeakToken(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	config := gatewayTestConfig(t, server, `{
		"schema_version":1,
		"rules":[{"team":"other-team","publisher":"git-publisher/v1","modes":["pull-request"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/acme/widget"]}]
	}`)
	executor, err := publisher.NewGatewayExecutor(publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), gatewayGitRequest(fixture.ref))
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("policy denial = %v", err)
	}
	if strings.Contains(err.Error(), "mounted-token") {
		t.Fatalf("policy error disclosed bearer token: %v", err)
	}
	if requests != 0 {
		t.Fatalf("policy denial made %d network requests", requests)
	}
}

func TestGatewayPolicyRequiresExactModeTargetBranchAndPolicyVersion(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	for name, mutate := range map[string]func(*publisher.Request){
		"mode": func(request *publisher.Request) {
			request.Mode = publisher.ModeBranch
		},
		"target branch": func(request *publisher.Request) {
			request.Parameters["target_branch"] = "release"
		},
		"approval policy": func(request *publisher.Request) {
			request.ApprovalPolicyVersion = "engineering/v2"
		},
		"merge is separately opt in": func(request *publisher.Request) {
			request.Mode = publisher.ModeMerge
			request.Parameters = map[string]string{"target_branch": "main", "expected_base_sha": testBaseSHA}
			request.ApprovedBy = "reviewer"
			request.Approval = approvalEvidence("reviewer", 9)
		},
	} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
			defer server.Close()
			config := gatewayTestConfig(t, server, `{
				"schema_version":1,
				"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["pull-request"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/acme/widget"]}]
			}`)
			executor, err := publisher.NewGatewayExecutor(publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config)
			if err != nil {
				t.Fatal(err)
			}
			request := gatewayGitRequest(fixture.ref)
			mutate(&request)
			if _, err := executor.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Fatalf("exact policy denial = %v", err)
			}
			if requests != 0 || fixture.metadata.GetAuthorizedCallCount() != 0 || fixture.content.OpenCallCount() != 0 {
				t.Fatalf("denied request crossed gateway/snapshot boundary: network=%d metadata=%d content=%d",
					requests, fixture.metadata.GetAuthorizedCallCount(), fixture.content.OpenCallCount())
			}
		})
	}
}

func TestGatewayRejectsOversizedAndMalformedResponses(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"found":false,"padding":"` + strings.Repeat("x", 1024) + `"}`))
	}))
	defer server.Close()
	config := gatewayTestConfig(t, server, `{
		"schema_version":1,
		"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["pull-request"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/acme/widget"]}]
	}`)
	config.MaxResponseBytes = 128
	executor, err := publisher.NewGatewayExecutor(publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), gatewayGitRequest(fixture.ref))
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestGatewayRejectsInsecureProviderResultURL(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/publications/lookup":
			writeGatewayJSON(t, response, map[string]any{"found": false})
		case "/v1/git/current-base":
			writeGatewayJSON(t, response, map[string]string{"base_sha": testBaseSHA})
		case "/v1/git/publish":
			writeGatewayJSON(t, response, map[string]string{
				"external_id": "pr/1", "url": "http://git.example/pr/1", "head_sha": testResultSHA,
			})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	config := gatewayTestConfig(t, server, `{
		"schema_version":1,
		"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["pull-request"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/acme/widget"]}]
	}`)
	executor, err := publisher.NewGatewayExecutor(publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), gatewayGitRequest(fixture.ref)); err == nil || !strings.Contains(err.Error(), "result URL") {
		t.Fatalf("insecure result URL error = %v", err)
	}
}

func TestGatewayConfigurationFailsClosed(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	valid := gatewayTestConfig(t, server, `{
		"schema_version":1,
		"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["pull-request"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/acme/widget"]}]
	}`)
	tests := map[string]func(*publisher.GatewayConfig){
		"http endpoint":     func(config *publisher.GatewayConfig) { config.Endpoint = "http://gateway.example" },
		"endpoint userinfo": func(config *publisher.GatewayConfig) { config.Endpoint = "https://user@gateway.example" },
		"endpoint path":     func(config *publisher.GatewayConfig) { config.Endpoint = "https://gateway.example/api" },
		"endpoint query":    func(config *publisher.GatewayConfig) { config.Endpoint = "https://gateway.example?debug=true" },
		"relative policy":   func(config *publisher.GatewayConfig) { config.PolicyFile = "policy.json" },
		"relative token":    func(config *publisher.GatewayConfig) { config.TokenFile = "token" },
		"relative CA":       func(config *publisher.GatewayConfig) { config.CACertificateFile = "ca.pem" },
		"zero timeout":      func(config *publisher.GatewayConfig) { config.RequestTimeout = 0 },
		"zero lease":        func(config *publisher.GatewayConfig) { config.LeaseDuration = 0 },
		"lease shorter than request": func(config *publisher.GatewayConfig) {
			config.RequestTimeout = time.Minute
			config.LeaseDuration = 30 * time.Second
		},
		"zero response bound": func(config *publisher.GatewayConfig) { config.MaxResponseBytes = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if _, err := publisher.NewGatewayExecutor(publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config); err == nil {
				t.Fatal("invalid gateway configuration was accepted")
			}
		})
	}
	invalidPolicies := map[string]string{
		"missing modes":               `{"schema_version":1,"rules":[{"team":"engineering","publisher":"git-publisher/v1","approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/repo"]}]}`,
		"missing policy versions":     `{"schema_version":1,"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["pull-request"],"target_branches":["main"],"destinations":["git.example/repo"]}]}`,
		"Git missing target branches": `{"schema_version":1,"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["pull-request"],"approval_policy_versions":["engineering/v1"],"destinations":["git.example/repo"]}]}`,
		"work item target branches":   `{"schema_version":1,"rules":[{"team":"engineering","publisher":"work-item-publisher/v1","modes":["comment"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["ENG-1"]}]}`,
		"publisher mode mismatch":     `{"schema_version":1,"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["comment"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/repo"]}]}`,
		"duplicate mode":              `{"schema_version":1,"rules":[{"team":"engineering","publisher":"git-publisher/v1","modes":["merge","merge"],"approval_policy_versions":["engineering/v1"],"target_branches":["main"],"destinations":["git.example/repo"]}]}`,
	}
	for name, policy := range invalidPolicies {
		t.Run("policy "+name, func(t *testing.T) {
			config := valid
			config.PolicyFile = filepath.Join(t.TempDir(), "policy.json")
			if err := os.WriteFile(config.PolicyFile, []byte(policy), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := publisher.NewGatewayExecutor(publisher.NewMemoryStore(time.Now), fixture.metadata, fixture.content, config); err == nil {
				t.Fatal("invalid gateway policy was accepted")
			}
		})
	}
}

func TestSnapshotChangeInspectorRevalidatesManifestMetadataDocumentAndPayload(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	inspector, err := publisher.NewSnapshotChangeInspector(fixture.metadata, fixture.content, snapshot.Canonicalizer{})
	if err != nil {
		t.Fatal(err)
	}
	change, err := inspector.Inspect(context.Background(), gatewayGitRequest(fixture.ref))
	if err != nil {
		t.Fatal(err)
	}
	if change.BaseSHA != testBaseSHA || change.ResultSHA != testResultSHA || change.CanonicalArchivePath == "" || change.MaterializedRoot == "" {
		t.Fatalf("change = %+v", change)
	}
	root := filepath.Dir(change.MaterializedRoot)
	if err := change.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("captured tree still exists after Close: %v", err)
	}

	bad := fixture.manifest.Clone()
	bad.IntrinsicMetadata = json.RawMessage(`{"repository_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_sha":"9999999999999999999999999999999999999999","result_sha":"2222222222222222222222222222222222222222","result_tree_sha":"3333333333333333333333333333333333333333","representation":"bundle"}`)
	fixture.metadata.GetAuthorizedReturns(bad, true, nil)
	if _, err := inspector.Inspect(context.Background(), gatewayGitRequest(fixture.ref)); err == nil || !strings.Contains(err.Error(), "intrinsic metadata") {
		t.Fatalf("mismatched metadata error = %v", err)
	}
}

func TestSnapshotValueInspectorRehashesExactAuthorizedBytes(t *testing.T) {
	fixture := newGatewaySnapshotFixture(t)
	inspector, err := publisher.NewSnapshotValueInspector(fixture.metadata, fixture.content, snapshot.Canonicalizer{})
	if err != nil {
		t.Fatal(err)
	}
	request := gatewayGitRequest(fixture.ref)
	value, err := inspector.InspectValue(context.Background(), request)
	if err != nil || value.CanonicalArchivePath == "" {
		t.Fatalf("InspectValue = (%+v, %v)", value, err)
	}
	root := filepath.Dir(value.CanonicalArchivePath)
	if err := value.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("captured generic snapshot tree still exists after Close: %v", err)
	}

	fixture.content.OpenStub = func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(tarBytes(t, map[string][]byte{"changed": []byte("bytes")}))), nil
	}
	if _, err := inspector.InspectValue(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("corrupt generic snapshot error = %v", err)
	}
}

type gatewaySnapshotFixture struct {
	metadata *snapshotfakes.FakeMetadataStore
	content  *snapshotfakes.FakeContentStore
	manifest snapshot.Snapshot
	ref      snapshot.SnapshotRef
	archive  []byte
}

func newGatewaySnapshotFixture(t *testing.T) gatewaySnapshotFixture {
	t.Helper()
	payload := []byte("bundle bytes")
	document := contracts.RepositoryChangeDocument{
		SchemaVersion: "1.0.0", RepositoryID: digestOf([]byte("repository")).String(), BaseInput: "repository",
		BaseSHA: testBaseSHA, ResultSHA: testResultSHA, ResultTreeSHA: testTreeSHA,
		Representation: "bundle", PayloadPath: "change.bundle", PayloadDigest: digestOf(payload).String(),
	}
	documentBytes, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	raw := tarBytes(t, map[string][]byte{"change.bundle": payload, "change.json": documentBytes})
	tree, err := (snapshot.Canonicalizer{}).Capture(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	digest, size, files := tree.Digest, tree.ByteSize, tree.FileCount
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	metadataBytes, err := json.Marshal(contracts.RepositoryChangeMetadata{
		RepositoryID: document.RepositoryID, BaseSHA: testBaseSHA, ResultSHA: testResultSHA,
		ResultTreeSHA: testTreeSHA, Representation: "bundle",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := snapshot.Snapshot{
		ID: 41, Type: "repository-change/v1", Digest: digest, ByteSize: size, FileCount: files,
		Representation: "application/x-tar", IntrinsicMetadata: metadataBytes,
		ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
	}
	metadata := &snapshotfakes.FakeMetadataStore{}
	metadata.GetAuthorizedReturns(manifest, true, nil)
	content := &snapshotfakes.FakeContentStore{}
	content.OpenStub = func(context.Context, snapshot.Snapshot) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(archive)), nil
	}
	return gatewaySnapshotFixture{
		metadata: metadata, content: content, manifest: manifest,
		ref: snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}, archive: archive,
	}
}

func gatewayGitRequest(ref snapshot.SnapshotRef) publisher.Request {
	return publisher.Request{
		Publisher: publisher.GitPublisher, Input: ref, Destination: "git.example/acme/widget", Mode: publisher.ModePullRequest,
		Parameters:            map[string]string{"source_branch": "agent/change", "target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v1",
		Authority:             publisher.Authority{TeamID: 9, TeamName: "engineering", BuildID: 12, WorkflowRunID: 17, Actor: "build/12"},
	}
}

func gatewayTestConfig(t *testing.T, server *httptest.Server, policy string) publisher.GatewayConfig {
	t.Helper()
	directory := t.TempDir()
	policyPath := filepath.Join(directory, "policy.json")
	tokenPath := filepath.Join(directory, "token")
	caPath := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(policyPath, []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("mounted-token"), 0600); err != nil {
		t.Fatal(err)
	}
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("TLS server has no certificate")
	}
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParseCertificate(certificate.Raw); err != nil {
		t.Fatal(err)
	}
	return publisher.GatewayConfig{
		Endpoint: server.URL, PolicyFile: policyPath, TokenFile: tokenPath, CACertificateFile: caPath,
		RequestTimeout: 5 * time.Second, LeaseDuration: time.Minute, MaxResponseBytes: 1 << 20,
	}
}

func tarBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		content := files[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func digestOf(content []byte) snapshot.Digest {
	digest := sha256.Sum256(content)
	return snapshot.Digest("sha256:" + hex.EncodeToString(digest[:]))
}

func writeGatewayJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
