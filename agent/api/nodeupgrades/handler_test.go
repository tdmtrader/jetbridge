package nodeupgrades_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/nodeupgrades"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

type fakeConsumers struct {
	get       func(string, int) (*workflow.NodeDefinition, bool, error)
	consumers func(context.Context, string, int, workflow.NodeConsumerRequest) (workflow.NodeConsumerPage, error)
}

type fakeUpgradeService struct {
	upgrade func(context.Context, workflow.NodeUpgradeRequest) (workflow.NodeUpgradeResult, error)
}

func (fake *fakeUpgradeService) Upgrade(ctx context.Context, request workflow.NodeUpgradeRequest) (workflow.NodeUpgradeResult, error) {
	return fake.upgrade(ctx, request)
}

func (fake *fakeConsumers) Get(name string, version int) (*workflow.NodeDefinition, bool, error) {
	return fake.get(name, version)
}

func (fake *fakeConsumers) Consumers(ctx context.Context, name string, version int, request workflow.NodeConsumerRequest) (workflow.NodeConsumerPage, error) {
	return fake.consumers(ctx, name, version, request)
}

func TestNodeConsumersUsesExactNodeVersionAndTupleCursor(t *testing.T) {
	var received workflow.NodeConsumerRequest
	store := &fakeConsumers{
		get: func(name string, version int) (*workflow.NodeDefinition, bool, error) {
			if name != "code-review" || version != 5 {
				t.Fatalf("get = %s@%d", name, version)
			}
			return &workflow.NodeDefinition{
				ID: 9007199254740995, Name: name, Version: version, ContentHash: "hash",
			}, true, nil
		},
		consumers: func(_ context.Context, name string, version int, request workflow.NodeConsumerRequest) (workflow.NodeConsumerPage, error) {
			received = request
			return workflow.NodeConsumerPage{
				Consumers: []workflow.NodeConsumer{{
					WorkflowDefinitionID: 9007199254740993,
					WorkflowName:         "small-fix",
					WorkflowVersion:      7,
					Live:                 true,
					Binding: workflow.ResolvedNodeBinding{
						InstanceName:     "review",
						NodeDefinitionID: 9007199254740995,
						NodeName:         "code-review",
						NodeVersion:      5,
						NodeContentHash:  "hash",
					},
				}},
				NextCursor: workflow.NodeConsumerCursor{WorkflowDefinitionID: 9007199254740993, InstanceName: "review"},
			}, nil
		},
	}
	handler := mustHandler(t, store, nil)
	cursor := base64.RawURLEncoding.EncodeToString([]byte(`{"workflow_definition_id":70,"instance_name":"review-two"}`))
	recorder := httptest.NewRecorder()
	handler.Consumers(recorder, request(http.MethodGet, "/consumers?limit=2&cursor="+url.QueryEscape(cursor), "code-review", "5", ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if received.Limit != 2 || received.Cursor != (workflow.NodeConsumerCursor{WorkflowDefinitionID: 70, InstanceName: "review-two"}) {
		t.Fatalf("consumer request = %#v", received)
	}
	wantNext := base64.RawURLEncoding.EncodeToString([]byte(`{"workflow_definition_id":9007199254740993,"instance_name":"review"}`))
	if recorder.Header().Get("X-Next-Cursor") != wantNext {
		t.Fatalf("next cursor = %q, want %q", recorder.Header().Get("X-Next-Cursor"), wantNext)
	}
	if !strings.Contains(recorder.Body.String(), `"workflow_definition_id":"9007199254740993"`) {
		t.Fatalf("workflow definition ID was not encoded as an exact quoted decimal: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"node_definition_id":"9007199254740995"`) {
		t.Fatalf("node definition ID was not encoded as an exact quoted decimal: %s", recorder.Body.String())
	}
	var consumers []nodeupgrades.Consumer
	if err := json.Unmarshal(recorder.Body.Bytes(), &consumers); err != nil || len(consumers) != 1 ||
		consumers[0].WorkflowName != "small-fix" ||
		consumers[0].WorkflowDefinitionID != 9007199254740993 ||
		consumers[0].Binding.NodeDefinitionID != 9007199254740995 {
		t.Fatalf("consumers/body = %#v/%v", consumers, err)
	}
}

func TestNodeConsumersRejectsUnknownQueryAndNoncanonicalVersion(t *testing.T) {
	calls := 0
	store := &fakeConsumers{
		get: func(string, int) (*workflow.NodeDefinition, bool, error) { calls++; return nil, false, nil },
		consumers: func(context.Context, string, int, workflow.NodeConsumerRequest) (workflow.NodeConsumerPage, error) {
			calls++
			return workflow.NodeConsumerPage{}, nil
		},
	}
	handler := mustHandler(t, store, nil)
	duplicateCursorField := base64.RawURLEncoding.EncodeToString([]byte(
		`{"workflow_definition_id":70,"workflow_definition_id":71,"instance_name":"review"}`,
	))
	for _, test := range []struct {
		path    string
		version string
	}{
		{path: "/consumers?unexpected=true", version: "5"},
		{path: "/consumers", version: "05"},
		{path: "/consumers?cursor=" + url.QueryEscape(duplicateCursorField), version: "5"},
	} {
		recorder := httptest.NewRecorder()
		handler.Consumers(recorder, request(http.MethodGet, test.path, "code-review", test.version, ""))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s version %s: status = %d", test.path, test.version, recorder.Code)
		}
	}
	if calls != 0 {
		t.Fatalf("store calls = %d, want 0", calls)
	}
}

func TestNodeConsumersRejectsUnboundedOrMismatchedStorePages(t *testing.T) {
	for _, test := range []struct {
		name string
		page workflow.NodeConsumerPage
	}{
		{
			name: "unbounded",
			page: workflow.NodeConsumerPage{Consumers: []workflow.NodeConsumer{
				exactConsumer(72, "review-a"),
				exactConsumer(71, "review-b"),
			}},
		},
		{
			name: "wrong exact node",
			page: workflow.NodeConsumerPage{Consumers: []workflow.NodeConsumer{func() workflow.NodeConsumer {
				consumer := exactConsumer(71, "review")
				consumer.Binding.NodeVersion = 4
				return consumer
			}()}},
		},
		{
			name: "cursor does not bind last tuple",
			page: workflow.NodeConsumerPage{
				Consumers:  []workflow.NodeConsumer{exactConsumer(71, "review")},
				NextCursor: workflow.NodeConsumerCursor{WorkflowDefinitionID: 70, InstanceName: "other"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeConsumers{
				get: func(name string, version int) (*workflow.NodeDefinition, bool, error) {
					return &workflow.NodeDefinition{
						ID: 9007199254740995, Name: name, Version: version, ContentHash: "hash",
					}, true, nil
				},
				consumers: func(context.Context, string, int, workflow.NodeConsumerRequest) (workflow.NodeConsumerPage, error) {
					return test.page, nil
				},
			}
			handler := mustHandler(t, store, nil)
			recorder := httptest.NewRecorder()
			handler.Consumers(recorder, request(http.MethodGet, "/consumers?limit=1", "code-review", "5", ""))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("X-Next-Cursor") != "" || recorder.Header().Get("Link") != "" {
				t.Fatalf("invalid store page exposed pagination headers: %v", recorder.Header())
			}
		})
	}
}

func TestNodeUpgradePassesOnlyExactSelectionAndReturnsDeterministicResults(t *testing.T) {
	var received workflow.NodeUpgradeRequest
	upgrader := &fakeUpgradeService{upgrade: func(_ context.Context, request workflow.NodeUpgradeRequest) (workflow.NodeUpgradeResult, error) {
		received = request
		return workflow.NodeUpgradeResult{NodeName: request.NodeName, Version: request.Version, Workflows: []workflow.NodeUpgradeWorkflowResult{
			{Workflow: "version-upgrade", OldVersion: 3, Status: workflow.NodeUpgradeRecompositionRequired, Obligations: &workflow.NodeUpgradeObligations{}},
			{Workflow: "small-fix", OldVersion: 7, NewVersion: 8, Status: workflow.NodeUpgradeCreated},
		}}, nil
	}}
	handler := mustHandler(t, availableStore(), upgrader)
	recorder := httptest.NewRecorder()
	handler.Upgrade(recorder, request(http.MethodPost, "/upgrades", "code-review", "5", `{"workflows":["small-fix","version-upgrade"]}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if received.NodeName != "code-review" || received.Version != 5 || received.CreatedBy != "alice" || strings.Join(received.Workflows, ",") != "small-fix,version-upgrade" {
		t.Fatalf("upgrade request = %#v", received)
	}
	var result workflow.NodeUpgradeResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Workflows) != 2 || result.Workflows[0].Workflow != "small-fix" || result.Workflows[1].Workflow != "version-upgrade" {
		t.Fatalf("result order = %#v", result.Workflows)
	}
}

func TestNodeUpgradeRejectsDuplicateSelectionsUnknownFieldsAndQueries(t *testing.T) {
	calls := 0
	upgrader := &fakeUpgradeService{upgrade: func(context.Context, workflow.NodeUpgradeRequest) (workflow.NodeUpgradeResult, error) {
		calls++
		return workflow.NodeUpgradeResult{}, nil
	}}
	handler := mustHandler(t, availableStore(), upgrader)
	tests := []struct {
		path string
		body string
	}{
		{"/upgrades", `{"workflows":["small-fix","small-fix"]}`},
		{"/upgrades", `{"workflows":["small-fix"],"promote":true}`},
		{"/upgrades?limit=2", `{"workflows":["small-fix"]}`},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		handler.Upgrade(recorder, request(http.MethodPost, test.path, "code-review", "5", test.body))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s %s: status = %d, body = %s", test.path, test.body, recorder.Code, recorder.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("upgrade calls = %d, want 0", calls)
	}
}

func exactConsumer(workflowDefinitionID int, instanceName string) workflow.NodeConsumer {
	return workflow.NodeConsumer{
		WorkflowDefinitionID: workflowDefinitionID,
		WorkflowName:         "small-fix",
		WorkflowVersion:      7,
		Live:                 true,
		Binding: workflow.ResolvedNodeBinding{
			InstanceName:     instanceName,
			NodeDefinitionID: 9007199254740995,
			NodeName:         "code-review",
			NodeVersion:      5,
			NodeContentHash:  "hash",
		},
	}
}

func availableStore() *fakeConsumers {
	return &fakeConsumers{
		get: func(name string, version int) (*workflow.NodeDefinition, bool, error) {
			return &workflow.NodeDefinition{Name: name, Version: version}, true, nil
		},
		consumers: func(context.Context, string, int, workflow.NodeConsumerRequest) (workflow.NodeConsumerPage, error) {
			return workflow.NodeConsumerPage{}, nil
		},
	}
}

func mustHandler(t *testing.T, store nodeupgrades.ConsumerStore, upgrader nodeupgrades.UpgradeService) *nodeupgrades.Handler {
	t.Helper()
	if upgrader == nil {
		upgrader = &fakeUpgradeService{upgrade: func(context.Context, workflow.NodeUpgradeRequest) (workflow.NodeUpgradeResult, error) {
			return workflow.NodeUpgradeResult{}, nil
		}}
	}
	handler, err := nodeupgrades.NewHandler(nodeupgrades.Config{
		TeamID: 1, TeamName: atc.DefaultTeamName, Store: store, Upgrader: upgrader,
		Identity: func(*http.Request) (string, error) { return "alice", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func request(method, path, name, version, body string) *http.Request {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	params := url.Values{":node_name": {name}, ":version": {version}}
	req := httptest.NewRequest(method, path+separator+params.Encode(), strings.NewReader(body))
	if body == "" {
		req.Body = http.NoBody
		req.ContentLength = 0
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}
