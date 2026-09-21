// A real subprocess fixture for the Codex process seam, not a model emulator.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/review"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-cli " + review.CodexVersion())
		return
	}
	mode, out := "", ""
	for i, a := range os.Args {
		if i+1 < len(os.Args) {
			if a == "--model" {
				mode = os.Args[i+1]
			}
			if a == "--output-last-message" {
				out = os.Args[i+1]
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
	if mode == "handoff-wait" || mode == "handoff-finding" {
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
