// A subprocess that impersonates the Codex CLI for the Brine worker features
// (atc/worker/jetbridge/brine/steps), its only remaining consumer. The Go
// tests run the real pinned Codex against a scripted model instead
// (agent/session/codextest); nothing here shows what the real Codex accepts,
// emits or confines. The Brine steps keep it because they drive the worker
// as a process, including inside a cluster Pod that cannot reach a model
// server in the test process, and have no testing.T for codextest.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/review"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-cli " + review.CodexVersion())
		return
	}
	// The session derives its catalog from this one: every entry declares
	// the file-edit tool, which the session must remove.
	if strings.Join(os.Args[1:], " ") == "debug models --bundled" {
		fmt.Println(`{"models":[{"slug":"fixture","apply_patch_tool_type":"freeform"}]}`)
		return
	}
	mode, out, cd := "", "", ""
	for i, a := range os.Args {
		if i+1 < len(os.Args) {
			if a == "--model" {
				mode = os.Args[i+1]
			}
			if a == "--output-last-message" {
				out = os.Args[i+1]
			}
			if a == "--cd" {
				cd = os.Args[i+1]
			}
		}
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" || os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("CODEX_API_KEY") != "" {
		os.Exit(41)
	}
	// Every session runs read-only, from the derived catalog in its home,
	// which offers no file-edit tool.
	var catalog struct {
		Models []struct {
			ApplyPatch *string `json:"apply_patch_tool_type"`
		} `json:"models"`
	}
	data, err := os.ReadFile(filepath.Join(home, "catalog.json"))
	if err != nil || json.Unmarshal(data, &catalog) != nil || len(catalog.Models) == 0 || catalog.Models[0].ApplyPatch != nil ||
		!slices.Contains(os.Args, `model_catalog_json="`+filepath.Join(home, "catalog.json")+`"`) || !slices.Contains(os.Args, "read-only") {
		os.Exit(40)
	}
	if b, err := os.ReadFile(filepath.Join(home, "auth.json")); err != nil || !strings.Contains(string(b), "synthetic-access") {
		os.Exit(42)
	}
	if mode == "handoff-wait" || mode == "handoff-finding" || mode == "handoff-edit" {
		for {
			if _, err := os.Stat(filepath.Join(home, "continue-review")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Refresh and temporary copies must be destroyed, along with the original.
	os.WriteFile(filepath.Join(home, "auth.json"), []byte("synthetic-refreshed"), 0600)
	os.WriteFile(filepath.Join(home, "auth.json.tmp"), []byte("synthetic-temporary"), 0600)
	fmt.Fprintln(os.Stderr, "synthetic-access must never be forwarded")
	if mode == "timeout" {
		time.Sleep(30 * time.Second)
		return
	}
	if mode == "nonzero" {
		os.Exit(7)
	}
	if mode == "provider-startup-error" {
		fmt.Println(`{"type":"item.completed","item":{"type":"error","message":"Code Mode host unavailable"}}`)
		return
	}
	if mode == "missing" {
		fmt.Println(`{"type":"turn.completed"}`)
		return
	}
	if mode == "malformed" {
		os.WriteFile(out, []byte("{"), 0600)
		fmt.Println(`{"type":"turn.completed"}`)
		return
	}
	if implementModes[mode] {
		implement(mode, cd, out)
		return
	}
	if mode == "forbidden" {
		fmt.Println(`{"type":"item.started","item":{"type":"command_execution","command":"go test ./..."}}`)
	}
	assessment := map[string]any{"summary": "Inspected fixture.", "complete": mode != "partial", "reviewed_files": []string{"parser.go", "deleted.txt"}, "limitations": []string{}, "findings": []any{}}
	if mode == "missing-coverage" {
		assessment["reviewed_files"] = []string{"parser.go"}
	}
	if mode == "bundle-paths" {
		assessment["reviewed_files"] = []string{"head/parser.go", "manifest.json"}
	}
	if mode == "unknown-field" {
		assessment["unexpected"] = true
	}
	switch mode {
	case "finding", "handoff-finding", "deletion-finding", "invalid-line", "foreign-path":
		location := review.Location{Side: "head", Path: "parser.go", StartLine: 2, EndLine: 2}
		if mode == "deletion-finding" {
			location = review.Location{Side: "base", Path: "deleted.txt", StartLine: 1, EndLine: 1}
		}
		if mode == "invalid-line" {
			location.StartLine, location.EndLine = 999, 999
		}
		if mode == "foreign-path" {
			location.Path = "../auth.json"
		}
		assessment["findings"] = []review.Finding{{
			ID: "provisional", Severity: "high", Dimension: "correctness",
			Title: "Incorrect first-byte handling", Explanation: "The fixture change no longer returns the first byte.",
			Recommendation: "Preserve first-byte behavior.", Location: location,
		}}
	}
	b, _ := json.Marshal(assessment)
	os.WriteFile(out, b, 0600)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Println(`{"type":"item.completed","item":{"type":"mcp_tool_call","server":"review_input","tool":"read","status":"completed"}}`)
	fmt.Println(`{"type":"turn.completed"}`)
}

// Implement modes edit the workspace the way Codex does under the edit-only
// policy: through the worker's own workspace tool server, started exactly as
// the worker configured it, so its handlers decide what changes. Each call
// is reported as Codex reports a tool call. edit-outside-scratch also asks
// the server to overwrite the staged credential; the server must refuse it,
// or the provider stops so the worker publishes nothing. forbidden-shell and
// native-edit report what an edit-only session must never contain: a command
// execution, and a file change Codex made with a tool of its own. handoff-edit
// is edit, released only once the submission has disconnected, like
// handoff-finding.
var implementModes = map[string]bool{"edit": true, "handoff-edit": true, "edit-outside-scratch": true, "forbidden-shell": true, "native-edit": true}

func implement(mode, cd, out string) {
	if cd == "" {
		os.Exit(43)
	}
	w := startWorkspace()
	defer w.close()
	fmt.Println(`{"type":"thread.started","thread_id":"fixture"}`)
	fmt.Println(`{"type":"turn.started"}`)
	summary := "Return the first byte and cover it with a test."
	if addressed := w.findings(); addressed != "" {
		summary += " Addressed review finding: " + addressed + "."
	}
	for _, edit := range []struct {
		tool string
		args map[string]any
	}{
		{"replace", map[string]any{"path": "parser.go", "old": "return s[1]", "new": "return s[0]"}},
		{"write", map[string]any{"path": "parser_test.go", "content": "package parser\n\nimport \"testing\"\n\nfunc TestFirst(t *testing.T) {\n\tif First(\"ab\") != 'a' {\n\t\tt.Fatal(\"wrong byte\")\n\t}\n}\n"}},
		{"delete", map[string]any{"path": "deleted.txt"}},
	} {
		if _, failed := w.tool(edit.tool, edit.args); failed {
			os.Exit(46)
		}
	}
	switch mode {
	case "edit-outside-scratch":
		if _, failed := w.tool("write", map[string]any{"path": "../codex/auth.json", "content": "{}"}); !failed {
			os.Exit(48)
		}
	case "forbidden-shell":
		fmt.Println(`{"type":"item.started","item":{"id":"item_x","type":"command_execution","command":"go test ./...","status":"in_progress"}}`)
	case "native-edit":
		fmt.Println(`{"type":"item.completed","item":{"id":"item_x","type":"file_change","changes":[{"path":"parser.go","kind":"update"}],"status":"completed"}}`)
	}
	assessment, _ := json.Marshal(map[string]any{"summary": summary, "complete": true, "limitations": []string{}})
	os.WriteFile(out, assessment, 0600)
	fmt.Println(`{"type":"item.completed","item":{"id":"item_m","type":"agent_message","text":"Done."}}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
}

// workspace is the worker's workspace tool server, driven over MCP the way
// Codex drives it.
type workspace struct {
	server  *exec.Cmd
	in      io.WriteCloser
	replies *bufio.Scanner
	id      int
	args    []string
}

func startWorkspace() *workspace {
	command, args := "", []string(nil)
	for i, a := range os.Args {
		if i == 0 || os.Args[i-1] != "-c" {
			continue
		}
		if v, ok := strings.CutPrefix(a, "mcp_servers.workspace.command="); ok {
			json.Unmarshal([]byte(v), &command)
		}
		if v, ok := strings.CutPrefix(a, "mcp_servers.workspace.args="); ok {
			json.Unmarshal([]byte(v), &args)
		}
	}
	w := &workspace{server: exec.Command(command, args...), args: args}
	w.in, _ = w.server.StdinPipe()
	out, _ := w.server.StdoutPipe()
	if w.server.Start() != nil {
		os.Exit(44)
	}
	w.replies = bufio.NewScanner(out)
	w.replies.Buffer(make([]byte, 1<<20), 16<<20)
	w.call("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	return w
}

func (w *workspace) close() { w.in.Close(); w.server.Wait() }

func (w *workspace) call(method string, params any) json.RawMessage {
	w.id++
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": w.id, "method": method, "params": params})
	fmt.Fprintf(w.in, "%s\n", request)
	if !w.replies.Scan() {
		os.Exit(45)
	}
	var reply struct {
		Result json.RawMessage `json:"result"`
	}
	json.Unmarshal(w.replies.Bytes(), &reply)
	return reply.Result
}

// tool calls a workspace tool, reports the call as Codex does, and returns
// its text and whether the server refused it.
func (w *workspace) tool(name string, arguments map[string]any) (string, bool) {
	var result struct {
		Content []struct{ Text string } `json:"content"`
		IsError bool                    `json:"isError"`
	}
	json.Unmarshal(w.call("tools/call", map[string]any{"name": name, "arguments": arguments}), &result)
	status := "completed"
	if result.IsError {
		status = "failed"
	}
	fmt.Printf(`{"type":"item.completed","item":{"id":"item_%d","type":"mcp_tool_call","server":"workspace","tool":%q,"status":%q}}`+"\n", w.id, name, status)
	if len(result.Content) != 1 {
		os.Exit(46)
	}
	return result.Content[0].Text, result.IsError
}

// findings reads review findings the way the model would, through the read
// tool. It returns the first finding's title, or "" when the worker served
// no read-only inputs. Findings listed but unreadable stop the provider so
// the worker publishes nothing.
func (w *workspace) findings() string {
	if !slices.Contains(w.args, "--snapshot") {
		return ""
	}
	text, failed := w.tool("list", map[string]any{"prefix": "/input/"})
	var listed struct{ Paths []string }
	json.Unmarshal([]byte(text), &listed)
	if failed || !slices.Contains(listed.Paths, "/input/findings.json") {
		return ""
	}
	text, failed = w.tool("read", map[string]any{"path": "/input/findings.json", "limit": 500})
	var read struct{ Text string }
	if failed || json.Unmarshal([]byte(text), &read) != nil {
		os.Exit(47)
	}
	// read numbers each line; strip "N: " to recover the JSON.
	var body strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(read.Text, "\n"), "\n") {
		_, rest, _ := strings.Cut(line, ": ")
		body.WriteString(rest + "\n")
	}
	var findings []struct{ Title string }
	if json.Unmarshal([]byte(body.String()), &findings) != nil || len(findings) == 0 || findings[0].Title == "" {
		os.Exit(47)
	}
	return findings[0].Title
}
