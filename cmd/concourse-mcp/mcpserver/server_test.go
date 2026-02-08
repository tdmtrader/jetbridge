package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/cmd/concourse-mcp/mcpserver"
	"github.com/concourse/concourse/cmd/concourse-mcp/mcpserver/mcpserverfakes"
	"github.com/concourse/concourse/go-concourse/concourse"
	"github.com/concourse/concourse/go-concourse/concourse/concoursefakes"
	"github.com/vito/go-sse/sse"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// mcpMessage sends a JSON-RPC request and returns the parsed response.
func mcpCall(s *mcpserver.Server, method string, id int, params any) map[string]any {
	reqMap := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		reqMap["params"] = params
	}
	reqBytes, err := json.Marshal(reqMap)
	Expect(err).NotTo(HaveOccurred())

	input := bytes.NewBuffer(append(reqBytes, '\n'))
	output := &bytes.Buffer{}

	srv := mcpserver.NewWithIO(nil, nil, "", input, output)
	// We can't reuse the server because the reader is consumed.
	// Instead, create a fresh server for each call. But we need the real server.
	// Let's use a different approach.
	_ = srv
	_ = output
	return nil // placeholder
}

// testHarness provides a reusable test setup for the MCP server.
type testHarness struct {
	fakeClient *mcpserverfakes.FakeClientAPI
	fakeTeam   *mcpserverfakes.FakeTeamAPI
}

func newHarness() *testHarness {
	fakeClient := new(mcpserverfakes.FakeClientAPI)
	fakeTeam := new(mcpserverfakes.FakeTeamAPI)
	fakeTeam.NameReturns("main")
	return &testHarness{fakeClient: fakeClient, fakeTeam: fakeTeam}
}

func (h *testHarness) call(method string, id int, params any) map[string]any {
	reqMap := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		reqMap["params"] = params
	}
	reqBytes, err := json.Marshal(reqMap)
	Expect(err).NotTo(HaveOccurred())

	input := bytes.NewBuffer(append(reqBytes, '\n'))
	output := &bytes.Buffer{}

	server := mcpserver.NewWithIO(h.fakeClient, h.fakeTeam, "http://localhost:8080", input, output)
	err = server.Run(context.Background())
	Expect(err).NotTo(HaveOccurred())

	var resp map[string]any
	err = json.Unmarshal(output.Bytes(), &resp)
	Expect(err).NotTo(HaveOccurred(), "response: %s", output.String())
	return resp
}

func (h *testHarness) callTool(name string, args any) map[string]any {
	return h.call("tools/call", 1, map[string]any{
		"name":      name,
		"arguments": args,
	})
}

// extractToolResult parses the tool result text content as JSON.
func extractToolResult(resp map[string]any) (map[string]any, bool) {
	result, ok := resp["result"].(map[string]any)
	if !ok {
		return nil, false
	}
	isError, _ := result["isError"].(bool)
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		return nil, isError
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		return nil, isError
	}
	text, ok := block["text"].(string)
	if !ok {
		return nil, isError
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		// Return as-is in a map
		return map[string]any{"_text": text}, isError
	}
	return parsed, isError
}

// extractToolResultArray parses the tool result text content as a JSON array.
func extractToolResultArray(resp map[string]any) ([]any, bool) {
	result, ok := resp["result"].(map[string]any)
	if !ok {
		return nil, false
	}
	isError, _ := result["isError"].(bool)
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		return nil, isError
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		return nil, isError
	}
	text, ok := block["text"].(string)
	if !ok {
		return nil, isError
	}
	var parsed []any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, isError
	}
	return parsed, isError
}

// extractErrorText returns the error text from a tool result.
func extractErrorText(resp map[string]any) string {
	result, ok := resp["result"].(map[string]any)
	if !ok {
		return ""
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		return ""
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := block["text"].(string)
	return text
}

var _ = Describe("MCP Server Protocol", func() {
	var h *testHarness

	BeforeEach(func() {
		h = newHarness()
	})

	Describe("initialize", func() {
		It("returns server info and capabilities", func() {
			resp := h.call("initialize", 1, map[string]any{
				"protocolVersion": "2024-11-05",
				"clientInfo":      map[string]any{"name": "test"},
			})

			Expect(resp["jsonrpc"]).To(Equal("2.0"))
			result := resp["result"].(map[string]any)
			Expect(result["protocolVersion"]).To(Equal("2024-11-05"))
			serverInfo := result["serverInfo"].(map[string]any)
			Expect(serverInfo["name"]).To(Equal("concourse-mcp"))
			caps := result["capabilities"].(map[string]any)
			Expect(caps).To(HaveKey("tools"))
		})
	})

	Describe("tools/list", func() {
		It("returns all registered tools", func() {
			resp := h.call("tools/list", 2, nil)

			result := resp["result"].(map[string]any)
			tools := result["tools"].([]any)
			Expect(len(tools)).To(BeNumerically(">=", 10))

			toolNames := make([]string, len(tools))
			for i, t := range tools {
				tool := t.(map[string]any)
				toolNames[i] = tool["name"].(string)
			}

			Expect(toolNames).To(ContainElements(
				"list_pipelines",
				"get_pipeline",
				"list_jobs",
				"list_builds",
				"get_build",
				"get_build_log",
				"trigger_job",
				"set_pipeline",
				"abort_build",
				"pause_pipeline",
				"unpause_pipeline",
			))
		})

		It("includes input schemas for each tool", func() {
			resp := h.call("tools/list", 1, nil)
			result := resp["result"].(map[string]any)
			tools := result["tools"].([]any)
			for _, t := range tools {
				tool := t.(map[string]any)
				Expect(tool).To(HaveKey("inputSchema"), "tool %s missing inputSchema", tool["name"])
				schema := tool["inputSchema"].(map[string]any)
				Expect(schema["type"]).To(Equal("object"))
			}
		})
	})

	Describe("ping", func() {
		It("responds with empty object", func() {
			resp := h.call("ping", 3, nil)
			Expect(resp["result"]).To(Equal(map[string]any{}))
		})
	})

	Describe("unknown method", func() {
		It("returns method not found error", func() {
			resp := h.call("nonexistent", 4, nil)
			errObj := resp["error"].(map[string]any)
			Expect(errObj["code"]).To(BeNumerically("==", -32601))
			Expect(errObj["message"]).To(ContainSubstring("method not found"))
		})
	})
})

var _ = Describe("MCP Server Tools", func() {
	var h *testHarness

	BeforeEach(func() {
		h = newHarness()
	})

	Describe("list_pipelines", func() {
		It("returns pipelines from the default team", func() {
			h.fakeTeam.ListPipelinesReturns([]atc.Pipeline{
				{Name: "test-pipeline", Paused: false, Public: true, Archived: false, TeamName: "main"},
				{Name: "staging", Paused: true, Public: false, Archived: false, TeamName: "main"},
			}, nil)

			resp := h.callTool("list_pipelines", map[string]any{})
			result, isError := extractToolResultArray(resp)
			Expect(isError).To(BeFalse())
			Expect(result).To(HaveLen(2))

			p1 := result[0].(map[string]any)
			Expect(p1["name"]).To(Equal("test-pipeline"))
			Expect(p1["paused"]).To(Equal(false))
			Expect(p1["public"]).To(Equal(true))
			Expect(p1["team_name"]).To(Equal("main"))

			p2 := result[1].(map[string]any)
			Expect(p2["name"]).To(Equal("staging"))
			Expect(p2["paused"]).To(Equal(true))
		})

		It("uses a different team when specified", func() {
			otherTeam := new(concoursefakes.FakeTeam)
			otherTeam.NameReturns("other")
			otherTeam.ListPipelinesReturns([]atc.Pipeline{
				{Name: "other-pipeline", TeamName: "other"},
			}, nil)
			h.fakeClient.FindTeamReturns(otherTeam, nil)

			resp := h.callTool("list_pipelines", map[string]any{"team": "other"})
			result, isError := extractToolResultArray(resp)
			Expect(isError).To(BeFalse())
			Expect(result).To(HaveLen(1))
			Expect(result[0].(map[string]any)["name"]).To(Equal("other-pipeline"))

			Expect(h.fakeClient.FindTeamCallCount()).To(Equal(1))
			Expect(h.fakeClient.FindTeamArgsForCall(0)).To(Equal("other"))
		})

		It("returns an error when listing fails", func() {
			h.fakeTeam.ListPipelinesReturns(nil, errors.New("connection refused"))

			resp := h.callTool("list_pipelines", map[string]any{})
			_, isError := extractToolResultArray(resp)
			Expect(isError).To(BeTrue())
			Expect(extractErrorText(resp)).To(ContainSubstring("connection refused"))
		})
	})

	Describe("get_pipeline", func() {
		It("returns pipeline config as JSON", func() {
			h.fakeTeam.PipelineConfigReturns(
				atc.Config{
					Jobs: atc.JobConfigs{{Name: "build"}},
					Resources: atc.ResourceConfigs{{Name: "src", Type: "git"}},
				},
				"42",
				true,
				nil,
			)

			resp := h.callTool("get_pipeline", map[string]any{"pipeline": "my-pipeline"})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["version"]).To(Equal("42"))
			Expect(result["yaml"]).NotTo(BeEmpty())

			ref := h.fakeTeam.PipelineConfigArgsForCall(0)
			Expect(ref.Name).To(Equal("my-pipeline"))
		})

		It("returns error for missing pipeline", func() {
			h.fakeTeam.PipelineConfigReturns(atc.Config{}, "", false, nil)

			resp := h.callTool("get_pipeline", map[string]any{"pipeline": "nonexistent"})
			_, isError := extractToolResult(resp)
			Expect(isError).To(BeTrue())
			Expect(extractErrorText(resp)).To(ContainSubstring("not found"))
		})
	})

	Describe("list_jobs", func() {
		It("returns jobs for a pipeline", func() {
			h.fakeTeam.ListJobsReturns([]atc.Job{
				{
					Name:         "build",
					PipelineName: "my-pipeline",
					TeamName:     "main",
					FinishedBuild: &atc.Build{ID: 10, Name: "5", Status: atc.StatusSucceeded, StartTime: 1000, EndTime: 1060},
				},
				{
					Name:         "deploy",
					PipelineName: "my-pipeline",
					TeamName:     "main",
					Paused:       true,
					NextBuild:    &atc.Build{ID: 11, Name: "3", Status: atc.StatusStarted, StartTime: 2000},
				},
			}, nil)

			resp := h.callTool("list_jobs", map[string]any{"pipeline": "my-pipeline"})
			result, isError := extractToolResultArray(resp)
			Expect(isError).To(BeFalse())
			Expect(result).To(HaveLen(2))

			j1 := result[0].(map[string]any)
			Expect(j1["name"]).To(Equal("build"))
			Expect(j1["pipeline_name"]).To(Equal("my-pipeline"))
			Expect(j1["finished_build"]).NotTo(BeNil())

			j2 := result[1].(map[string]any)
			Expect(j2["name"]).To(Equal("deploy"))
			Expect(j2["paused"]).To(Equal(true))
			Expect(j2["next_build"]).NotTo(BeNil())

			ref := h.fakeTeam.ListJobsArgsForCall(0)
			Expect(ref.Name).To(Equal("my-pipeline"))
		})

		It("returns error when listing fails", func() {
			h.fakeTeam.ListJobsReturns(nil, errors.New("pipeline not found"))

			resp := h.callTool("list_jobs", map[string]any{"pipeline": "nonexistent"})
			_, isError := extractToolResultArray(resp)
			Expect(isError).To(BeTrue())
			Expect(extractErrorText(resp)).To(ContainSubstring("pipeline not found"))
		})
	})

	Describe("list_builds", func() {
		It("returns builds for a job", func() {
			h.fakeTeam.JobBuildsReturns([]atc.Build{
				{ID: 100, Name: "10", Status: atc.StatusSucceeded, PipelineName: "p", JobName: "j", TeamName: "main", StartTime: 1000, EndTime: 1060},
				{ID: 99, Name: "9", Status: atc.StatusFailed, PipelineName: "p", JobName: "j", TeamName: "main", StartTime: 900, EndTime: 950},
			}, concourse.Pagination{}, true, nil)

			resp := h.callTool("list_builds", map[string]any{"pipeline": "p", "job": "j"})
			result, isError := extractToolResultArray(resp)
			Expect(isError).To(BeFalse())
			Expect(result).To(HaveLen(2))

			b1 := result[0].(map[string]any)
			Expect(b1["id"]).To(BeNumerically("==", 100))
			Expect(b1["name"]).To(Equal("10"))
			Expect(b1["status"]).To(Equal("succeeded"))
			Expect(b1["duration_seconds"]).To(BeNumerically("==", 60))

			b2 := result[1].(map[string]any)
			Expect(b2["status"]).To(Equal("failed"))
			Expect(b2["duration_seconds"]).To(BeNumerically("==", 50))
		})

		It("uses default limit of 10", func() {
			h.fakeTeam.JobBuildsReturns(nil, concourse.Pagination{}, true, nil)

			h.callTool("list_builds", map[string]any{"pipeline": "p", "job": "j"})

			Expect(h.fakeTeam.JobBuildsCallCount()).To(Equal(1))
			_, _, page := h.fakeTeam.JobBuildsArgsForCall(0)
			Expect(page.Limit).To(Equal(10))
		})

		It("respects custom limit", func() {
			h.fakeTeam.JobBuildsReturns(nil, concourse.Pagination{}, true, nil)

			h.callTool("list_builds", map[string]any{"pipeline": "p", "job": "j", "limit": 5})

			_, _, page := h.fakeTeam.JobBuildsArgsForCall(0)
			Expect(page.Limit).To(Equal(5))
		})
	})

	Describe("get_build", func() {
		It("returns build details", func() {
			h.fakeClient.BuildReturns(atc.Build{
				ID:           42,
				Name:         "5",
				Status:       atc.StatusStarted,
				PipelineName: "my-pipeline",
				JobName:      "my-job",
				TeamName:     "main",
				StartTime:    1700000000,
			}, true, nil)

			resp := h.callTool("get_build", map[string]any{"build_id": 42})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["id"]).To(BeNumerically("==", 42))
			Expect(result["name"]).To(Equal("5"))
			Expect(result["status"]).To(Equal("started"))
			Expect(result["pipeline_name"]).To(Equal("my-pipeline"))
			Expect(result["job_name"]).To(Equal("my-job"))
			Expect(result["duration_seconds"]).To(BeNumerically("==", 0)) // no end_time

			Expect(h.fakeClient.BuildArgsForCall(0)).To(Equal("42"))
		})

		It("returns error for unknown build", func() {
			h.fakeClient.BuildReturns(atc.Build{}, false, nil)

			resp := h.callTool("get_build", map[string]any{"build_id": 999})
			_, isError := extractToolResult(resp)
			Expect(isError).To(BeTrue())
			Expect(extractErrorText(resp)).To(ContainSubstring("not found"))
		})
	})

	Describe("get_build_log", func() {
		It("returns concatenated log output", func() {
			fakeEvents := &fakeEventStream{
				events: []atc.Event{
					event.Log{Payload: "line 1\n"},
					event.Log{Payload: "line 2\n"},
					event.SelectedWorker{WorkerName: "w1"}, // non-log event, should be skipped
					event.Log{Payload: "line 3\n"},
				},
			}
			h.fakeClient.BuildEventsReturns(fakeEvents, nil)

			resp := h.callTool("get_build_log", map[string]any{"build_id": 42})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["log"]).To(Equal("line 1\nline 2\nline 3\n"))
		})

		It("returns error when events connection fails", func() {
			h.fakeClient.BuildEventsReturns(nil, errors.New("not authorized"))

			resp := h.callTool("get_build_log", map[string]any{"build_id": 42})
			_, isError := extractToolResult(resp)
			Expect(isError).To(BeTrue())
			Expect(extractErrorText(resp)).To(ContainSubstring("not authorized"))
		})

		It("returns empty log for build with no log events", func() {
			fakeEvents := &fakeEventStream{
				events: []atc.Event{
					event.SelectedWorker{WorkerName: "w1"},
				},
			}
			h.fakeClient.BuildEventsReturns(fakeEvents, nil)

			resp := h.callTool("get_build_log", map[string]any{"build_id": 42})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["log"]).To(Equal(""))
		})
	})

	Describe("trigger_job", func() {
		It("triggers a job and returns build info", func() {
			h.fakeTeam.CreateJobBuildReturns(atc.Build{
				ID:       200,
				Name:     "15",
				TeamName: "main",
			}, nil)

			resp := h.callTool("trigger_job", map[string]any{"pipeline": "my-pipeline", "job": "build"})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["build_id"]).To(BeNumerically("==", 200))
			Expect(result["build_name"]).To(Equal("15"))
			Expect(result["url"]).To(Equal("http://localhost:8080/teams/main/pipelines/my-pipeline/jobs/build/builds/15"))

			ref, jobName := h.fakeTeam.CreateJobBuildArgsForCall(0)
			Expect(ref.Name).To(Equal("my-pipeline"))
			Expect(jobName).To(Equal("build"))
		})

		It("returns error when trigger fails", func() {
			h.fakeTeam.CreateJobBuildReturns(atc.Build{}, errors.New("job not found"))

			resp := h.callTool("trigger_job", map[string]any{"pipeline": "p", "job": "nope"})
			_, isError := extractToolResult(resp)
			Expect(isError).To(BeTrue())
			Expect(extractErrorText(resp)).To(ContainSubstring("job not found"))
		})
	})

	Describe("set_pipeline", func() {
		It("creates a new pipeline", func() {
			h.fakeTeam.PipelineConfigReturns(atc.Config{}, "", false, nil) // not found = create
			h.fakeTeam.CreateOrUpdatePipelineConfigReturns(true, false, nil, nil)

			yaml := "jobs:\n- name: test\n  plan:\n  - task: t\n"
			resp := h.callTool("set_pipeline", map[string]any{"pipeline": "new-pipe", "yaml": yaml})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["created"]).To(Equal(true))
			Expect(result["updated"]).To(Equal(false))

			ref, version, config, _ := h.fakeTeam.CreateOrUpdatePipelineConfigArgsForCall(0)
			Expect(ref.Name).To(Equal("new-pipe"))
			Expect(version).To(Equal(""))
			Expect(string(config)).To(ContainSubstring("test"))
		})

		It("updates an existing pipeline", func() {
			h.fakeTeam.PipelineConfigReturns(atc.Config{}, "5", true, nil) // found, version 5
			h.fakeTeam.CreateOrUpdatePipelineConfigReturns(false, true, nil, nil)

			resp := h.callTool("set_pipeline", map[string]any{"pipeline": "existing", "yaml": "jobs: []"})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["created"]).To(Equal(false))
			Expect(result["updated"]).To(Equal(true))

			_, version, _, _ := h.fakeTeam.CreateOrUpdatePipelineConfigArgsForCall(0)
			Expect(version).To(Equal("5"))
		})

		It("returns warnings", func() {
			h.fakeTeam.PipelineConfigReturns(atc.Config{}, "", false, nil)
			h.fakeTeam.CreateOrUpdatePipelineConfigReturns(true, false, []concourse.ConfigWarning{
				{Type: "warning", Message: "job 'x' has no builds"},
			}, nil)

			resp := h.callTool("set_pipeline", map[string]any{"pipeline": "p", "yaml": "jobs: []"})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			warnings := result["warnings"].([]any)
			Expect(warnings).To(HaveLen(1))
			Expect(warnings[0]).To(Equal("job 'x' has no builds"))
		})

		It("returns error on invalid config", func() {
			h.fakeTeam.PipelineConfigReturns(atc.Config{}, "", false, nil)
			h.fakeTeam.CreateOrUpdatePipelineConfigReturns(false, false, nil, errors.New("invalid pipeline config"))

			resp := h.callTool("set_pipeline", map[string]any{"pipeline": "p", "yaml": "bad"})
			_, isError := extractToolResult(resp)
			Expect(isError).To(BeTrue())
			Expect(extractErrorText(resp)).To(ContainSubstring("invalid pipeline config"))
		})
	})

	Describe("abort_build", func() {
		It("aborts a running build", func() {
			h.fakeClient.AbortBuildReturns(nil)

			resp := h.callTool("abort_build", map[string]any{"build_id": 42})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["success"]).To(Equal(true))

			Expect(h.fakeClient.AbortBuildArgsForCall(0)).To(Equal("42"))
		})

		It("returns error when abort fails", func() {
			h.fakeClient.AbortBuildReturns(errors.New("build already finished"))

			resp := h.callTool("abort_build", map[string]any{"build_id": 42})
			_, isError := extractToolResult(resp)
			Expect(isError).To(BeTrue())
			Expect(extractErrorText(resp)).To(ContainSubstring("build already finished"))
		})
	})

	Describe("pause_pipeline", func() {
		It("pauses a pipeline", func() {
			h.fakeTeam.PausePipelineReturns(true, nil)

			resp := h.callTool("pause_pipeline", map[string]any{"pipeline": "my-pipeline"})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["success"]).To(Equal(true))

			ref := h.fakeTeam.PausePipelineArgsForCall(0)
			Expect(ref.Name).To(Equal("my-pipeline"))
		})

		It("returns error when pause fails", func() {
			h.fakeTeam.PausePipelineReturns(false, errors.New("not authorized"))

			resp := h.callTool("pause_pipeline", map[string]any{"pipeline": "p"})
			_, isError := extractToolResult(resp)
			Expect(isError).To(BeTrue())
		})
	})

	Describe("unpause_pipeline", func() {
		It("unpauses a pipeline", func() {
			h.fakeTeam.UnpausePipelineReturns(true, nil)

			resp := h.callTool("unpause_pipeline", map[string]any{"pipeline": "my-pipeline"})
			result, isError := extractToolResult(resp)
			Expect(isError).To(BeFalse())
			Expect(result["success"]).To(Equal(true))

			ref := h.fakeTeam.UnpausePipelineArgsForCall(0)
			Expect(ref.Name).To(Equal("my-pipeline"))
		})
	})

	Describe("unknown tool", func() {
		It("returns an error for unknown tool names", func() {
			resp := h.callTool("nonexistent_tool", map[string]any{})
			_, isError := extractToolResult(resp)
			Expect(isError).To(BeTrue())
			Expect(extractErrorText(resp)).To(ContainSubstring("unknown tool"))
		})
	})
})

// fakeEventStream implements concourse.Events for testing.
type fakeEventStream struct {
	events []atc.Event
	index  int
}

func (f *fakeEventStream) NextEvent() (atc.Event, error) {
	if f.index >= len(f.events) {
		return nil, io.EOF
	}
	ev := f.events[f.index]
	f.index++
	return ev, nil
}

func (f *fakeEventStream) NextEventRaw() (sse.Event, error) {
	return sse.Event{}, errors.New("not implemented")
}

func (f *fakeEventStream) Close() error {
	return nil
}

