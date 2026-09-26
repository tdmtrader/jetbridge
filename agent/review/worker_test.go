package review

import (
	"context"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/session"
)

func TestWorkerRequiresMemoryRuntime(t *testing.T) {
	// Ordinary temporary directories must not become a credential store merely
	// because they will be deleted later. No real credentials are read here.
	if err := session.RequireMemoryRuntime(t.TempDir()); err == nil {
		t.Skip("host temporary directory is itself memory backed")
	}
	_, err := RunWorker(context.Background(), WorkerOptions{RuntimeDir: t.TempDir()})
	if err == nil {
		t.Fatal("worker accepted disk runtime")
	}
}

// The review command line is part of the inspection guarantee. Moving it
// behind the provider seam must not change a single argument.
func TestReviewCodexCommandIsUnchanged(t *testing.T) {
	opts := WorkerOptions{Model: "model-x", ReaderCommand: "/usr/local/bin/jb-review-worker"}
	got := session.Codex{}.Command(reviewPolicy(opts, "/rt/s/workspace", "/in/bundle", "/rt/s/assessment.schema.json", "/rt/s/assessment.json"))
	want := []string{"exec", "--ignore-user-config", "--ignore-rules", "--strict-config", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "--color", "never", "--json", "--model", "model-x", "--cd", "/rt/s/workspace", "--output-schema", "/rt/s/assessment.schema.json", "--output-last-message", "/rt/s/assessment.json"}
	for _, setting := range []string{
		`approval_policy="never"`, `forced_login_method="chatgpt"`, `cli_auth_credentials_store="file"`,
		`model_provider="openai"`, `history.persistence="none"`, `project_doc_max_bytes=0`, `web_search="disabled"`,
		`features.shell_tool=false`, `features.unified_exec=false`, `features.shell_snapshot=false`,
		`features.apply_patch_freeform=false`, `features.multi_agent=false`, `features.js_repl=false`,
		`features.apps=false`, `features.plugins=false`, `features.remote_plugin=false`,
		`features.multi_agent_v2=false`, `features.memories=false`, `features.skip_host_skill_discovery=true`,
		`suppress_unstable_features_warning=true`,
		`features.skill_mcp_dependency_install=false`, `shell_environment_policy.inherit="none"`,
		`mcp_servers.review_input.command="/usr/local/bin/jb-review-worker"`,
		`mcp_servers.review_input.args=["input-tools","--input","/in/bundle"]`,
		`mcp_servers.review_input.required=true`,
		`mcp_servers.review_input.enabled_tools=["list","read","search"]`,
	} {
		want = append(want, "-c", setting)
	}
	want = append(want, "-")
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("review command changed:\n got %q\nwant %q", got, want)
	}
}

func TestReviewTraceVocabulary(t *testing.T) {
	codex := session.Codex{}
	for line, ok := range map[string]bool{
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"review_input","tool":"read"}}`: true,
		`{"type":"item.started","item":{"type":"reasoning"}}`:                                             true,
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"workspace","tool":"read"}}`:    false,
		`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"review_input","tool":"exec"}}`: false,
		`{"type":"item.started","item":{"type":"command_execution","command":"go test"}}`:                 false,
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a","kind":"add"}]}}`:   false,
		`{"type":"item.completed","item":{"type":"web_search","query":"x"}}`:                              false,
	} {
		e, err := codex.Decode([]byte(line))
		if err == nil {
			err = reviewItem(e)
		}
		if (err == nil) != ok {
			t.Errorf("%s: accepted=%v, want %v (%v)", line, err == nil, ok, err)
		}
	}
}
