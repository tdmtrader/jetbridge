// A real subprocess fixture for the Codex process seam, not a model emulator.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
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

// Implement modes edit the workspace (the working directory) the way Codex's
// file-edit tool does, then report the edit. The refused modes make the same
// edits, so only the worker's policy can keep them from being published.
// handoff-edit is edit, released only once the detached submission has
// disconnected, like handoff-finding.
var implementModes = map[string]bool{"edit": true, "handoff-edit": true, "edit-outside-scratch": true, "forbidden-shell": true, "malformed-edit": true}

func implement(mode, cd, out string) {
	if cd == "" {
		os.Exit(43)
	}
	os.WriteFile(filepath.Join(cd, "parser.go"), []byte("package parser\nfunc First(s string) byte { return s[0] }\n"), 0600)
	os.WriteFile(filepath.Join(cd, "parser_test.go"), []byte("package parser\n\nimport \"testing\"\n\nfunc TestFirst(t *testing.T) {\n\tif First(\"ab\") != 'a' {\n\t\tt.Fatal(\"wrong byte\")\n\t}\n}\n"), 0644)
	os.Remove(filepath.Join(cd, "deleted.txt"))
	summary := "Return the first byte and cover it with a test."
	if addressed := readFindings(); addressed != "" {
		summary += " Addressed review finding: " + addressed + "."
	}
	assessment, _ := json.Marshal(map[string]any{"summary": summary, "complete": true, "limitations": []string{}})
	os.WriteFile(out, assessment, 0600)
	fmt.Println(`{"type":"thread.started","thread_id":"fixture"}`)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Println(`{"type":"item.completed","item":{"id":"item_0","type":"mcp_tool_call","server":"workspace","tool":"read","status":"completed"}}`)
	changes := []map[string]string{
		{"path": filepath.Join(cd, "parser.go"), "kind": "update"},
		{"path": "parser_test.go", "kind": "add"},
		{"path": filepath.Join(cd, "deleted.txt"), "kind": "delete"},
	}
	switch mode {
	case "edit-outside-scratch":
		changes = append(changes, map[string]string{"path": filepath.Join(cd, "..", "codex", "auth.json"), "kind": "update"})
	case "forbidden-shell":
		fmt.Println(`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"go test ./...","status":"in_progress"}}`)
	case "malformed-edit":
		changes = append(changes, map[string]string{"kind": "update"})
	}
	event, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_2", "type": "file_change", "changes": changes, "status": "completed"}})
	fmt.Println(string(event))
	fmt.Println(`{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Done."}}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
}

// readFindings reads review findings the way the model would: it starts the
// workspace tool server exactly as the worker configured it and reads
// /input/findings.json through its read tool. It returns the first finding's
// title, or "" when the worker served no read-only inputs. A server that
// fails, or lists findings it cannot read, stops the provider so the worker
// publishes nothing.
func readFindings() string {
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
	if !slices.Contains(args, "--snapshot") {
		return ""
	}
	server := exec.Command(command, args...)
	in, _ := server.StdinPipe()
	out, _ := server.StdoutPipe()
	if server.Start() != nil {
		os.Exit(44)
	}
	defer func() { in.Close(); server.Wait() }()
	replies := bufio.NewScanner(out)
	replies.Buffer(make([]byte, 1<<20), 16<<20)
	call := func(id int, method string, params any) json.RawMessage {
		request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		fmt.Fprintf(in, "%s\n", request)
		if !replies.Scan() {
			os.Exit(45)
		}
		var reply struct {
			Result json.RawMessage `json:"result"`
		}
		json.Unmarshal(replies.Bytes(), &reply)
		return reply.Result
	}
	tool := func(id int, name string, arguments map[string]any) string {
		var result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		}
		json.Unmarshal(call(id, "tools/call", map[string]any{"name": name, "arguments": arguments}), &result)
		if result.IsError || len(result.Content) != 1 {
			os.Exit(46)
		}
		return result.Content[0].Text
	}
	call(1, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	var listed struct{ Paths []string }
	json.Unmarshal([]byte(tool(2, "list", map[string]any{"prefix": "/input/"})), &listed)
	if !slices.Contains(listed.Paths, "/input/findings.json") {
		return ""
	}
	var read struct{ Text string }
	json.Unmarshal([]byte(tool(3, "read", map[string]any{"path": "/input/findings.json", "limit": 500})), &read)
	// read numbers each line; strip "N: " to recover the JSON.
	var text strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(read.Text, "\n"), "\n") {
		_, rest, _ := strings.Cut(line, ": ")
		text.WriteString(rest + "\n")
	}
	var findings []struct{ Title string }
	if json.Unmarshal([]byte(text.String()), &findings) != nil || len(findings) == 0 || findings[0].Title == "" {
		os.Exit(47)
	}
	return findings[0].Title
}
