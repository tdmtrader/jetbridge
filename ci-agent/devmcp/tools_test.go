package devmcp_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/devmcp"
)

var _ = Describe("RegisterTools", func() {
	var ts *httptest.Server

	BeforeEach(func() {
		workdir := GinkgoT().TempDir()
		cfg := devmcp.Config{
			SchemaVersion: 1,
			Repo: &devmcp.ToolCommands{
				Build: &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "echo repo-build"}},
			},
			Components: []devmcp.ComponentConfig{
				{
					ID: "app", Description: "the app", Paths: []string{"src/"}, Kind: "cli",
					Build: &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "echo built-app"}},
					Test:  &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "echo tested"}, FocusFlag: "--focus"},
					Lint:  &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "echo finding; exit 1"}},
				},
				{
					ID: "docs", Description: "the docs", Paths: []string{"docs/"}, Kind: "docs",
					Build: &devmcp.CommandSpec{Cmd: []string{"sh", "-c", "echo built-docs"}},
				},
			},
		}
		s := devmcp.NewServer(0)
		devmcp.RegisterTools(s, cfg, workdir)
		ts = httptest.NewServer(s)
		DeferCleanup(ts.Close)
	})

	callTool := func(name string, args string) map[string]any {
		body := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"%s","arguments":%s}}`,
			name, args)
		return post(ts.URL, body)
	}

	payload := func(resp map[string]any) map[string]any {
		ExpectWithOffset(1, resp).NotTo(HaveKey("error"), "resp: %v", resp)
		text := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		var decoded map[string]any
		ExpectWithOffset(1, json.Unmarshal([]byte(text), &decoded)).To(Succeed())
		return decoded
	}

	It("registers exactly the five contract tools", func() {
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		tools := resp["result"].(map[string]any)["tools"].([]any)
		var names []string
		for _, tool := range tools {
			names = append(names, tool.(map[string]any)["name"].(string))
		}
		Expect(names).To(ConsistOf(
			"list_components", "build", "run_tests", "lint", "affected_components"))
	})

	It("lists components from the config", func() {
		res := payload(callTool("list_components", `{}`))
		comps := res["components"].([]any)
		Expect(comps).To(HaveLen(2))
		first := comps[0].(map[string]any)
		Expect(first["id"]).To(Equal("app"))
		Expect(first["kind"]).To(Equal("cli"))
		Expect(first["paths"]).To(Equal([]any{"src/"}))
	})

	It("builds a component and returns the shared result payload", func() {
		res := payload(callTool("build", `{"component":"app"}`))
		Expect(res["status"]).To(Equal("ok"))
		Expect(res["output_tail"]).To(ContainSubstring("built-app"))
		Expect(res["duration_seconds"]).To(BeNumerically(">=", 0))
		Expect(res["summary"]).NotTo(BeEmpty())
	})

	It("uses the repo section when component is omitted", func() {
		res := payload(callTool("build", `{}`))
		Expect(res["output_tail"]).To(ContainSubstring("repo-build"))
	})

	It("rejects an omitted component when the repo section lacks the command", func() {
		resp := callTool("lint", `{}`)
		Expect(resp["error"].(map[string]any)["code"]).To(BeEquivalentTo(-32602))
	})

	It("returns status failed (not error) when lint finds problems", func() {
		res := payload(callTool("lint", `{"component":"app"}`))
		Expect(res["status"]).To(Equal("failed"))
	})

	It("rejects unknown components with -32602", func() {
		resp := callTool("build", `{"component":"nope"}`)
		rpcErr := resp["error"].(map[string]any)
		Expect(rpcErr["code"]).To(BeEquivalentTo(-32602))
		Expect(rpcErr["message"]).To(ContainSubstring("unknown component"))
	})

	It("appends the focus flag for run_tests", func() {
		res := payload(callTool("run_tests", `{"component":"app","focus":"MySpec"}`))
		Expect(res["status"]).To(Equal("ok"))
	})

	It("rejects focus on a component without focus support", func() {
		resp := callTool("run_tests", `{"component":"docs","focus":"X"}`)
		Expect(resp["error"].(map[string]any)["code"]).To(BeEquivalentTo(-32602))
	})

	It("rejects a component that does not define the command", func() {
		resp := callTool("run_tests", `{"component":"docs"}`)
		Expect(resp["error"].(map[string]any)["code"]).To(BeEquivalentTo(-32602))
	})

	It("maps changed paths onto components and reports unmapped paths", func() {
		res := payload(callTool("affected_components",
			`{"changed_paths":["src/app.sh","docs/readme.md","LICENSE"]}`))
		Expect(res["components"]).To(Equal([]any{"app", "docs"}))
		Expect(res["unmapped_paths"]).To(Equal([]any{"LICENSE"}))
	})

	It("returns an empty components array for empty input", func() {
		res := payload(callTool("affected_components", `{"changed_paths":[]}`))
		Expect(res["components"]).To(Equal([]any{}))
	})

	It("rejects affected_components without changed_paths", func() {
		resp := callTool("affected_components", `{}`)
		Expect(resp["error"].(map[string]any)["code"]).To(BeEquivalentTo(-32602))
	})
})
