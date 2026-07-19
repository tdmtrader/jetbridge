package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// runCheckpoint is checkpoint-client mode: POST the sidecar's /checkpoint and
// block until approved/rejected. Exit codes FROZEN (checkpoint-seam + SSE seam
// deltas, 2026-07-09): 0 = approved; 1 = rejected OR non-200 OR bad response
// OR retries exhausted (a sidecar fatal-auth arrives as a 502 whose body
// carries the frozen "principal rejected:" prefix — echoed verbatim to
// stderr); 2 = usage error. Transport errors before a response are retried
// 60 x 5s (the sidecar may still be starting; §8.5 readiness ordering).
// The http.Client MUST have no global timeout (D4): this call blocks for the
// entire park — checkpoints are exempt from the SSE mandate (no claude CLI in
// the loop) but not from the no-timeout rules.
func runCheckpoint(args []string) int {
	fs := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	name := fs.String("name", "", "checkpoint name from the workflow definition (required)")
	description := fs.String("description", "", "what is being approved")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "checkpoint: --name is required")
		return 2
	}

	mcpURL := os.Getenv("PLATFORM_MCP_URL") // e.g. http://127.0.0.1:7781/mcp (§8.1)
	if mcpURL == "" {
		fmt.Fprintln(os.Stderr, "checkpoint: PLATFORM_MCP_URL is required")
		return 2
	}
	endpoint := strings.TrimSuffix(mcpURL, "/mcp") + "/checkpoint"

	body, _ := json.Marshal(map[string]string{"name": *name, "description": *description})
	client := &http.Client{} // no timeout: this call blocks while parked

	for attempt := 1; ; attempt++ {
		resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
		if err != nil {
			if attempt >= 60 {
				fmt.Fprintf(os.Stderr, "checkpoint: sidecar unreachable after %d attempts: %s\n", attempt, err)
				return 1
			}
			time.Sleep(5 * time.Second)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			// Echo the sidecar's error body verbatim — the fatal-auth path's
			// "principal rejected:" prefix must reach the step log (D6).
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			fmt.Fprintf(os.Stderr, "checkpoint: sidecar returned %d: %s\n", resp.StatusCode, bytes.TrimSpace(msg))
			return 1
		}
		var out struct {
			Approved   bool   `json:"approved"`
			Answer     string `json:"answer"`
			AnsweredBy string `json:"answered_by"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			fmt.Fprintf(os.Stderr, "checkpoint: bad response: %s\n", err)
			return 1
		}
		if out.Approved {
			fmt.Printf("checkpoint %q approved by %s\n", *name, out.AnsweredBy)
			return 0
		}
		fmt.Printf("checkpoint %q rejected by %s\n", *name, out.AnsweredBy)
		return 1
	}
}
