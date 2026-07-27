# dev-mcp Implementation Plan

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. The five-tool dev-mcp contract and sidecar packaging convention remain in force; their ticket-centric wave placement below is historical.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Freeze the five-tool per-repo dev-mcp contract and ship its four artifacts — a contract-test kit, a code-callable Go client, this repo's own config-driven reference implementation, and the sidecar image packaging convention (Dockerfile + theborg CI job) that every later MCP sidecar reuses.

**Architecture:** The contract types and the streamable-HTTP Go client live in the main module at `agent/devmcp` (with the contract-test kit at `agent/devmcp/contracttest`); the server is a generic, `dev-mcp.yml`-driven binary in the standalone ci-agent module (`ci-agent/devmcp` + `ci-agent/cmd/dev-mcp`) because ci-agent has no Go dependency on the main module (00-shared-contracts.md conventions bullet 2) and the sidecar image must stay lean. A second minimal fixture repo (shell scripts only, one component) proves the contract is layout- and language-agnostic before it freezes; an e2e suite builds the real binary and runs the kit against both the fixture and this repo's `dev-mcp.yml`.

**Tech Stack:** Go 1.25; Ginkgo/Gomega (main-module and ci-agent test convention); goccy/go-yaml (already a ci-agent dep); MCP streamable HTTP (JSON-RPC 2.0 over a single `POST /mcp`, SSE for progress); counterfeiter fakes; Docker-in-DinD image build on the existing theborg `cicd` pipeline.

---

## Context

**Charter (workstreams.json id `dev-mcp`, wave 1, size M, `depends_on: []`).** Scope in: (1) interface finalization of spec open item 3 — tool schemas, opaque component ids, ok/failed/error taxonomy, streaming/heartbeat semantics for multi-minute runs; (2) a contract-test kit any repo implementation runs against its server; (3) a Go client so platform code (harvest, wave 3) invokes tools as code; (4) this repo's reference implementation mapping components to make/ginkgo targets, plus a second minimal single-component fixture repo; (5) ownership of the sidecar image build/version/publish convention and the first CI job, reused verbatim by platform-mcp and gateway. Scope out (do NOT build): sidecar mounting mechanics inside pods (agent-step, wave 2), gate policy language (harvest-step), platform-mcp and gateway tools.

**Prior waves:** none — this is wave 1. Nothing from other workstreams is consumed as code; wave-mates (agent-identity, credentials-and-budgets, pipeline-runs, workflow-store) touch disjoint files. No migration numbers are allocated to dev-mcp (00-shared-contracts.md §1.1 has no dev-mcp block) — this workstream creates no tables.

**Contract surfaces this plan PRODUCES** (00-shared-contracts.md):
- **§3.1 "dev-mcp"** — the five tool schemas, the shared result payload, the Go client interface at `agent/devmcp/client.go`, the contract-test kit at `agent/devmcp/contracttest`, and this repo's implementation at `ci-agent/cmd/dev-mcp` with components `atc`, `fly`, `web`, `ci-agent`, `topgun`.
- **§8.5 "Sidecar image packaging convention"** — `ghcr.io/tdmtrader/mcp-dev-concourse`, bare static Go binary entrypoint (no hardcoded paths; workspace taken from CWD set by the owning exec — 2026-07-09 CWD convention) on `MCP_LISTEN_ADDR` (default `:7780`), `GET /healthz`, non-root (MCP sidecar images only; runner images run as root per the 2026-07-09 §8.5 scoping), contract-kit-gated CI job in the existing `cicd` pipeline as the copyable template.
- **§3 preamble + §11** — implementation-level finalization of open item 3 is appended to the amendment log in Task 1 (SSE frame format, error-code assignments, exit-code convention, heartbeat env var, log-path convention, `dev-mcp.yml` reference config format).

**Contract surfaces this plan CONSUMES:**
- **"Conventions that apply to the whole document"** — the ok/failed/error status taxonomy; the fact that ci-agent is a standalone module with no dependency on the main module (which forces the server implementation into the ci-agent module).
- **§8.1 "Env vars in the agent step's main container and sidecars"** — fixed port 7780, `DEV_MCP_URL`, `MCP_LISTEN_ADDR` override (dev-mcp owns ports/packaging per the §8 header).
- **§3 "MCP tool schemas" preamble** — streamable HTTP decision, `AddTool` registration style from `atc/api/mcpserver` (verified: `atc/api/mcpserver/server.go:31`), progress-notification requirement.

**Downstream consumers (later waves, do not block on them):** harvest-step and process-intel-experiments consume the Go client; agent-step consumes the sidecar wiring assumption; platform-mcp-hitl and gateway-mcp copy the packaging convention and CI job.

**Key codebase facts verified for this plan:**
- `atc/api/mcpserver/server.go` — in-house MCP precedent: `AddTool(name, description, schema, handler)`, JSON-RPC dispatch for `initialize`/`tools/list`/`tools/call`/`ping`, tool payload serialized as a single `text` content block (`server.go:161-167`). It has **no** SSE/progress support, so the dev-mcp server re-implements the pattern with progress added (and cannot import it anyway — different module). *(2026-07-09: still true at this plan's wave-1 execution time; the SSE delta later upgrades `atc/api/mcpserver` in place with a byte-similar port of this plan's SSE path — 08 Task 9b — with mirrored server tests as the drift guard; see the Task 1 2026-07-09 amendment entry.)*
- `ci-agent/go.mod` — module `github.com/concourse/ci-agent`, deps: goccy/go-yaml, ginkgo/gomega, otel. Test style: Ginkgo bootstrap per package (`ci-agent/phaseconfig`).
- Root `package.json` scripts: `build` (`yarn run build`), `test` (`cd web/elm && elm-test`), `analyse` — the `web` component's commands.
- `Makefile:5-16` — `test-unit` is `ginkgo -r -p` (skips ci-agent), `test-ci-agent` is `cd ci-agent && go test ./...`. **`ginkgo -r` does not run packages without Ginkgo suites**, so the plain-`testing` packages (contracttest, e2e) get a new `test-dev-mcp` make target wired into `test-quick`.
- `deploy/concourse-pipeline.yml` — the live `cicd` pipeline on theborg; the `tag-push-release` job (lines 520-676) is the verified DinD build/push pattern the new image job copies.
- Counterfeiter recipe: `atc/db/agent_reviews_factory.go:11-13` (`//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate` + `//counterfeiter:generate . X`).

---

### Task 1: Contract addendum — interface finalization (open item 3 residue)

The contracts doc decides the big points (tool schemas, taxonomy, streamable HTTP, ≥30s progress). This task writes the remaining implementation-level decisions into the amendment log so harvest-step/agent-step/process-intel build against pinned semantics, not this plan's prose.

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md:1471` (append to §11 Amendment log)

**Steps:**

- [ ] Append the following entry to the end of §11 (after the 2026-07-08 review-fixes entry):

```markdown
- 2026-07-08 (dev-mcp interface finalization — closes the implementation-level residue of spec open item 3; owner: dev-mcp; consumers notified: harvest-step, agent-step, process-intel-experiments, platform-mcp-hitl, gateway-mcp):
  - **Result encoding:** tool payloads are carried as a single `text` content block containing the JSON object (`{"content":[{"type":"text","text":"<json>"}]}`), matching the `atc/api/mcpserver` precedent. `isError` content results are never used for the ok/failed/error taxonomy.
  - **JSON-RPC error codes:** `-32602` covers all malformed input — unknown tool name, unknown component id, a component that does not define the requested command, `focus` on a component with no `focus_flag`, and missing/mistyped arguments. `-32603` covers server-internal marshaling faults. `-32700` parse errors, `-32601` unknown methods (unchanged from the precedent).
  - **Exit-code convention** (command-backed implementations): exit 0 → `ok`; exit codes listed in the command's `failed_exit_codes` (default `[1]`) → `failed`; any other exit code, spawn failure, or context cancellation → `error`.
  - **Progress/SSE:** the server responds with `text/event-stream` when the client sends `Accept: text/event-stream` AND `params._meta.progressToken`; otherwise buffered JSON (progress dropped). SSE frames are `event: message` + `data: <json-rpc message>`; progress notifications are `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":<echoed verbatim>,"message":"<latest output line>"}}` emitted on a heartbeat (default 15s — half the contract's 30s bound; env override `DEV_MCP_PROGRESS_INTERVAL`, Go duration syntax), with the final JSON-RPC response as the last frame.
  - **Logs:** `log_path` is workspace-relative under `.dev-mcp/logs/`; `output_tail` is the last ≤200 lines. The v1 reference implementation emits no structured `failures` entries (the field is optional in §3.1).
  - **Reference implementation config:** `dev-mcp.yml` at the repo root — `schema_version: 1`, a `components` list (`id`/`description`/`paths`/`kind` + optional `build`/`test`/`lint` command specs: `cmd` argv array, optional `dir`, `focus_flag`, `failed_exit_codes`), and an optional top-level `repo:` command group used when `component` is omitted. Whole-repo calls on an implementation without a `repo:` section are malformed input (`-32602`). This repo's `topgun` component defines only `test` (no Makefile build/lint target exists for it).
  - **Contract-test kit API:** `contracttest.Run(t, endpointURL)` runs the universal protocol/schema/taxonomy checks; `contracttest.RunWithOptions(t, endpointURL, Options{...})` adds opt-in execution checks (exercise-ok component, failing-lint taxonomy, slow-test progress emission, affected-path mapping).
```

- [ ] Verify the entry landed: `grep -n "dev-mcp interface finalization" docs/superpowers/plans/agentic-platform/00-shared-contracts.md` — expect one hit in §11.
- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(dev-mcp): finalize open-item-3 interface details in shared-contracts amendment log" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

> **Amended 2026-07-09 (SSE transport & park hardening delta, resolves F13 — this plan's assignment is this amendment-log entry PLUS the Task 8 heartbeat bounds validation; no dev-mcp config or image changes).** *(Corrected 2026-07-10, verifier follow-up: this preamble originally claimed "this amendment-log entry ONLY; no dev-mcp code, config, or image changes" — internally contradicting the "VALIDATION in all three binaries" sentence the entry itself promotes. The pre-correction Task 8 `main.go` only `log.Fatalf`'d on an unparseable `DEV_MCP_PROGRESS_INTERVAL`; `0s`/`-5s` flowed into `devmcp.NewServer`, whose `<= 0 → DefaultHeartbeat` fallback is exactly the silent clamp the delta forbids, and `45s` was silently accepted past the ≤30s bound — re-opening the F13 failure class for harvest-driven long tool calls. Task 8 now carries the same fatal `<= 0` / `> 30s` bounds check as platform-mcp (08 Task 9 `ConfigFromEnv` + Task 13 `TestServeModeFailsFastOnBadProgressInterval`) and gateway (10 Task 2), with a mirrored cmd-level test.)* dev-mcp's Task 4 server is the empirically proven-surviving pattern (the claude CLI v2.1.77 abandons a progress-free buffered tools/call at exactly 60s, silently; the 15s SSE heartbeat keeps it alive) and its §3-preamble SSE finalization is promoted to the wire spec of record for all three sidecars. The steps below record that promotion in the §11 amendment log.

- [ ] Append the following second entry to the end of §11 (after the 2026-07-08 dev-mcp interface-finalization entry above):

```markdown
- 2026-07-09 (SSE transport generalization — dev-mcp's §3-preamble SSE finalization becomes NORMATIVE for all three sidecars; owner: dev-mcp; consumers notified: platform-mcp-hitl, gateway-mcp, agent-step, harvest-step; resolves F13):
  - **Wire spec of record:** the 2026-07-08 Progress/SSE bullet above — SSE gating on `Accept: text/event-stream` AND `params._meta.progressToken`, frames `event: message` + `data: <json-rpc message>`, progress notifications `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":<echoed verbatim>,"message":"<latest>"}}` on a coalescing heartbeat ticker, final JSON-RPC response as the LAST SSE frame, buffered JSON when the client doesn't opt in — now binds dev-mcp, platform-mcp, AND gateway (delta D1/D3). Rationale is empirical: the claude CLI (v2.1.77) abandons a progress-free buffered tools/call at exactly 60s, silently ("(completed with no output)", no error flag); `MCP_TOOL_TIMEOUT` does NOT prevent it. Any MCP tool whose handler can block longer than 30s MUST be served over this SSE path.
  - **Mirrored implementation, not shared code:** `ci-agent` is a standalone Go module — the root module MUST NOT `require` ci-agent and ci-agent MUST NOT `require` the root. `ci-agent/devmcp` stays the reference server, unchanged; `atc/api/mcpserver` (currently buffered-only) is upgraded IN PLACE with a byte-similar port of `ci-agent/devmcp`'s SSE path (lands as 08 Task 9b, before 08 Task 10 and 10 Task 7), and platform-mcp/gateway build on it. No new shared module is extracted. Drift guard: `atc/api/mcpserver/server_test.go` gains SSE tests mirrored from `ci-agent/devmcp/server_test.go` (04 Task 4) asserting the identical frame shape.
  - **Heartbeat env pattern:** `DEV_MCP_PROGRESS_INTERVAL` generalizes to `<ROLE>_MCP_PROGRESS_INTERVAL` — `DEV_MCP_PROGRESS_INTERVAL` / `PLATFORM_MCP_PROGRESS_INTERVAL` / `GATEWAY_MCP_PROGRESS_INTERVAL` — Go duration syntax, default 15s (`DefaultHeartbeat`, half the §3.1 30s progress bound, 4x margin under the 60s CLI cliff). VALIDATION in all three binaries: a set-but-invalid value, a value <= 0, or a value > 30s is a FATAL startup error — never clamp silently.
  - dev-mcp's TRANSPORT is already compliant (its Task 4 server is the F13 proven-surviving implementation) and its BINARY enforces the same fatal bounds on `DEV_MCP_PROGRESS_INTERVAL` (04 Task 8: set-but-invalid, <= 0, or > 30s exits at startup, mirrored cmd-level test; `devmcp.NewServer`'s `<= 0 → DefaultHeartbeat` fallback is a library convenience for the unset case only — never reachable from a set env var); nothing else in plan 04 changes.
```

- [ ] Verify the entry landed: `grep -n "SSE transport generalization" docs/superpowers/plans/agentic-platform/00-shared-contracts.md` — expect one hit in §11.
- [ ] Commit: `git add docs/superpowers/plans/agentic-platform/00-shared-contracts.md && git commit -m "docs(dev-mcp): promote SSE frame/heartbeat spec to normative for all three sidecars (F13 delta)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 2: `agent/devmcp` contract types

**Files:**
- Create: `agent/devmcp/types.go`
- Test: `agent/devmcp/devmcp_suite_test.go`, `agent/devmcp/types_test.go`

**Steps:**

- [ ] Write the Ginkgo suite bootstrap `agent/devmcp/devmcp_suite_test.go`:

```go
package devmcp_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDevMCP(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DevMCP Suite")
}
```

- [ ] Write the failing test `agent/devmcp/types_test.go` asserting the wire shapes match 00-shared-contracts.md §3.1 exactly:

```go
package devmcp_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/devmcp"
)

var _ = Describe("contract types", func() {
	It("marshals Component with the §3.1 field names", func() {
		data, err := json.Marshal(devmcp.Component{
			ID:          "atc",
			Description: "ATC web node",
			Paths:       []string{"atc/"},
			Kind:        "service",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(MatchJSON(`{
			"id": "atc",
			"description": "ATC web node",
			"paths": ["atc/"],
			"kind": "service"
		}`))
	})

	It("marshals ToolResult with the §3.1 field names and omits empty optionals", func() {
		data, err := json.Marshal(devmcp.ToolResult{
			Status:          devmcp.StatusFailed,
			Summary:         "2 specs failed",
			DurationSeconds: 93.5,
			OutputTail:      "FAIL",
			LogPath:         ".dev-mcp/logs/test-atc-1.log",
			Failures: []devmcp.Failure{
				{ID: "TestX", Message: "boom", Path: "atc/x_test.go", Line: 12},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(MatchJSON(`{
			"status": "failed",
			"summary": "2 specs failed",
			"duration_seconds": 93.5,
			"output_tail": "FAIL",
			"log_path": ".dev-mcp/logs/test-atc-1.log",
			"failures": [{"id": "TestX", "message": "boom", "path": "atc/x_test.go", "line": 12}]
		}`))

		bare, err := json.Marshal(devmcp.ToolResult{
			Status: devmcp.StatusOK, Summary: "ok", DurationSeconds: 0.1,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(bare).To(MatchJSON(`{"status": "ok", "summary": "ok", "duration_seconds": 0.1}`))
	})

	It("pins the taxonomy and transport constants", func() {
		Expect(string(devmcp.StatusOK)).To(Equal("ok"))
		Expect(string(devmcp.StatusFailed)).To(Equal("failed"))
		Expect(string(devmcp.StatusError)).To(Equal("error"))
		Expect(devmcp.DefaultListenAddr).To(Equal(":7780"))
		Expect(devmcp.EndpointPath).To(Equal("/mcp"))
		Expect(devmcp.EnvEndpoint).To(Equal("DEV_MCP_URL"))
		Expect(devmcp.EnvListenAddr).To(Equal("MCP_LISTEN_ADDR"))
	})
})
```

- [ ] Run to verify failure: `ginkgo ./agent/devmcp/` — expect a compile failure (`undefined: devmcp.Component` etc.).
- [ ] Write `agent/devmcp/types.go`:

```go
// Package devmcp defines the dev-mcp per-repo tool contract
// (docs/superpowers/plans/agentic-platform/00-shared-contracts.md §3.1)
// and a streamable-HTTP MCP client for invoking a repo's dev-mcp
// implementation from Go code.
package devmcp

// Status is the three-way result taxonomy shared by build/run_tests/lint:
// ok = the checked thing passed, failed = it ran and found problems,
// error = tooling itself broke.
type Status string

const (
	StatusOK     Status = "ok"
	StatusFailed Status = "failed"
	StatusError  Status = "error"
)

// Tool names as registered on every dev-mcp server.
const (
	ToolListComponents     = "list_components"
	ToolBuild              = "build"
	ToolRunTests           = "run_tests"
	ToolLint               = "lint"
	ToolAffectedComponents = "affected_components"
)

// Transport constants (00-shared-contracts.md §8.1, §8.5).
const (
	DefaultListenAddr = ":7780"
	EndpointPath      = "/mcp"
	HealthPath        = "/healthz"
	EnvListenAddr     = "MCP_LISTEN_ADDR"
	EnvEndpoint       = "DEV_MCP_URL"
)

// Component is one entry of the list_components result. IDs are opaque,
// repo-defined, stable strings.
type Component struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	Kind        string   `json:"kind"` // service|library|cli|web|docs|other
}

// Failure is one structured failure in a ToolResult (optional; emitted
// when the implementation can parse test names / lint rules).
type Failure struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// ToolResult is the shared result payload of build, run_tests, and lint.
type ToolResult struct {
	Status          Status    `json:"status"`
	Summary         string    `json:"summary"`
	DurationSeconds float64   `json:"duration_seconds"`
	OutputTail      string    `json:"output_tail,omitempty"`
	LogPath         string    `json:"log_path,omitempty"`
	Failures        []Failure `json:"failures,omitempty"`
}

// AffectedResult is the result payload of affected_components.
type AffectedResult struct {
	Components    []string `json:"components"`
	UnmappedPaths []string `json:"unmapped_paths,omitempty"`
}
```

- [ ] Run to verify pass: `ginkgo ./agent/devmcp/` — expect all specs green.
- [ ] Commit: `git add agent/devmcp && git commit -m "feat(devmcp): add dev-mcp contract types (00-shared-contracts §3.1)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 3: `agent/devmcp` streamable-HTTP Go client

The exact call path harvest-step will use. No transport timeout — per §3.1 the caller applies a per-gate timeout via `ctx`.

**Files:**
- Create: `agent/devmcp/client.go`, `agent/devmcp/devmcpfakes/fake_client.go` (generated)
- Test: `agent/devmcp/client_test.go`

**Steps:**

- [ ] Write the failing test `agent/devmcp/client_test.go`:

```go
package devmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/devmcp"
)

// toolTextResult wraps a payload the way MCP servers return tool results:
// a single text content block containing the JSON object.
func toolTextResult(id json.RawMessage, payload string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": payload}},
		},
	}
}

var _ = Describe("HTTPClient", func() {
	Describe("buffered JSON responses", func() {
		It("sends tools/call with arguments and a progress token, and decodes the payload", func() {
			var gotBody map[string]any
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal(http.MethodPost))
				Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
				Expect(r.Header.Get("Accept")).To(ContainSubstring("text/event-stream"))
				Expect(json.NewDecoder(r.Body).Decode(&gotBody)).To(Succeed())

				w.Header().Set("Content-Type", "application/json")
				id, _ := json.Marshal(gotBody["id"])
				json.NewEncoder(w).Encode(toolTextResult(id,
					`{"status":"ok","summary":"built","duration_seconds":2.5}`))
			}))
			defer ts.Close()

			res, err := devmcp.NewClient(ts.URL).Build(context.Background(), "atc")
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Status).To(Equal(devmcp.StatusOK))
			Expect(res.Summary).To(Equal("built"))
			Expect(res.DurationSeconds).To(Equal(2.5))

			Expect(gotBody["method"]).To(Equal("tools/call"))
			params := gotBody["params"].(map[string]any)
			Expect(params["name"]).To(Equal("build"))
			Expect(params["arguments"]).To(Equal(map[string]any{"component": "atc"}))
			Expect(params["_meta"].(map[string]any)["progressToken"]).NotTo(BeEmpty())
		})

		It("returns AffectedComponents ids from the result payload", func() {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
				args := body["params"].(map[string]any)["arguments"].(map[string]any)
				Expect(args["changed_paths"]).To(Equal([]any{"atc/api/handler.go"}))

				w.Header().Set("Content-Type", "application/json")
				id, _ := json.Marshal(body["id"])
				json.NewEncoder(w).Encode(toolTextResult(id,
					`{"components":["atc"],"unmapped_paths":[]}`))
			}))
			defer ts.Close()

			ids, err := devmcp.NewClient(ts.URL).AffectedComponents(
				context.Background(), []string{"atc/api/handler.go"})
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]string{"atc"}))
		})

		It("maps JSON-RPC errors to *devmcp.RPCError", func() {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      body["id"],
					"error":   map[string]any{"code": -32602, "message": "unknown component: nope"},
				})
			}))
			defer ts.Close()

			_, err := devmcp.NewClient(ts.URL).Build(context.Background(), "nope")
			var rpcErr *devmcp.RPCError
			Expect(errors.As(err, &rpcErr)).To(BeTrue(), "got %v", err)
			Expect(rpcErr.Code).To(Equal(-32602))
			Expect(rpcErr.Message).To(ContainSubstring("unknown component"))
		})
	})

	Describe("SSE responses", func() {
		It("forwards progress notifications and returns the final response", func() {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
				id, _ := json.Marshal(body["id"])

				w.Header().Set("Content-Type", "text/event-stream")
				flusher := w.(http.Flusher)
				fmt.Fprintf(w, "event: message\ndata: %s\n\n",
					`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"t","message":"running case 3"}}`)
				flusher.Flush()

				final, _ := json.Marshal(toolTextResult(id,
					`{"status":"ok","summary":"10 cases passed","duration_seconds":1.2}`))
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", final)
				flusher.Flush()
			}))
			defer ts.Close()

			var mu sync.Mutex
			var progress []string
			client := devmcp.NewClient(ts.URL, devmcp.WithProgress(func(tool, msg string) {
				mu.Lock()
				progress = append(progress, tool+"|"+msg)
				mu.Unlock()
			}))

			res, err := client.RunTests(context.Background(), "app", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Status).To(Equal(devmcp.StatusOK))
			Expect(res.Summary).To(Equal("10 cases passed"))

			mu.Lock()
			defer mu.Unlock()
			Expect(progress).To(ContainElement("run_tests|running case 3"))
		})
	})
})
```

- [ ] Run to verify failure: `ginkgo ./agent/devmcp/` — expect compile failure (`undefined: devmcp.NewClient`, `devmcp.RPCError`, `devmcp.WithProgress`).
- [ ] Write `agent/devmcp/client.go`:

```go
package devmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Client is the code-callable dev-mcp interface (00-shared-contracts.md §3.1).
// The harvest step invokes gates exclusively through this interface.
//
//counterfeiter:generate . Client
type Client interface {
	ListComponents(ctx context.Context) ([]Component, error)
	Build(ctx context.Context, component string) (*ToolResult, error)
	RunTests(ctx context.Context, component, focus string) (*ToolResult, error)
	Lint(ctx context.Context, component string) (*ToolResult, error)
	AffectedComponents(ctx context.Context, paths []string) ([]string, error)
}

// RPCError is a JSON-RPC-level error from the server. Per the contract
// taxonomy this means MALFORMED INPUT (unknown tool/component, bad args);
// run failures come back as ToolResult.Status, never as RPCError.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// HTTPClient speaks MCP streamable HTTP: a single POST endpoint with SSE
// responses for long calls. It applies NO transport timeout — per §3.1 the
// caller applies a per-gate timeout through ctx.
type HTTPClient struct {
	endpoint   string
	httpClient *http.Client
	onProgress func(tool, message string)
	nextID     atomic.Int64
}

// Option configures an HTTPClient.
type Option func(*HTTPClient)

// WithProgress registers a callback invoked for every notifications/progress
// message received during a tool call.
func WithProgress(fn func(tool, message string)) Option {
	return func(c *HTTPClient) { c.onProgress = fn }
}

// WithHTTPClient overrides the underlying *http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *HTTPClient) { c.httpClient = hc }
}

// NewClient returns a Client for the dev-mcp server at endpoint, e.g.
// os.Getenv(EnvEndpoint) == "http://127.0.0.1:7780/mcp".
func NewClient(endpoint string, opts ...Option) *HTTPClient {
	c := &HTTPClient{
		endpoint:   endpoint,
		httpClient: &http.Client{}, // deliberately no Timeout: ctx governs
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type callToolParams struct {
	Name      string    `json:"name"`
	Arguments any       `json:"arguments,omitempty"`
	Meta      *callMeta `json:"_meta,omitempty"`
}

type callMeta struct {
	ProgressToken string `json:"progressToken"`
}

type progressParams struct {
	ProgressToken json.RawMessage `json:"progressToken"`
	Message       string          `json:"message"`
}

type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (c *HTTPClient) callTool(ctx context.Context, tool string, args any, out any) error {
	id := c.nextID.Add(1)
	reqBody, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/call",
		Params: callToolParams{
			Name:      tool,
			Arguments: args,
			Meta:      &callMeta{ProgressToken: fmt.Sprintf("devmcp-%d", id)},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("call %s: %w", tool, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("call %s: unexpected HTTP status %d", tool, resp.StatusCode)
	}

	var final rpcMessage
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		final, err = c.readSSE(resp.Body, tool)
	} else {
		err = json.NewDecoder(resp.Body).Decode(&final)
	}
	if err != nil {
		return fmt.Errorf("read %s response: %w", tool, err)
	}
	if final.Error != nil {
		return final.Error
	}

	var ctr callToolResult
	if err := json.Unmarshal(final.Result, &ctr); err != nil {
		return fmt.Errorf("decode %s result: %w", tool, err)
	}
	if ctr.IsError {
		text := ""
		if len(ctr.Content) > 0 {
			text = ctr.Content[0].Text
		}
		return fmt.Errorf("%s: tool error: %s", tool, text)
	}
	if len(ctr.Content) == 0 {
		return fmt.Errorf("%s: empty result content", tool)
	}
	if err := json.Unmarshal([]byte(ctr.Content[0].Text), out); err != nil {
		return fmt.Errorf("decode %s payload: %w", tool, err)
	}
	return nil
}

// readSSE consumes an SSE stream, forwarding notifications/progress to the
// progress callback and returning the final JSON-RPC response message.
func (c *HTTPClient) readSSE(body io.Reader, tool string) (rpcMessage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			continue
		}
		if line != "" || data.Len() == 0 {
			continue // event-name lines, comments, or blank keepalives
		}
		var msg rpcMessage
		if err := json.Unmarshal([]byte(data.String()), &msg); err != nil {
			return rpcMessage{}, fmt.Errorf("bad SSE frame: %w", err)
		}
		data.Reset()
		if msg.Method == "notifications/progress" {
			if c.onProgress != nil {
				var p progressParams
				_ = json.Unmarshal(msg.Params, &p)
				c.onProgress(tool, p.Message)
			}
			continue
		}
		if len(msg.ID) > 0 {
			return msg, nil // the response for our call
		}
	}
	if err := scanner.Err(); err != nil {
		return rpcMessage{}, err
	}
	return rpcMessage{}, fmt.Errorf("SSE stream ended without a response")
}

type listComponentsResult struct {
	Components []Component `json:"components"`
}

func (c *HTTPClient) ListComponents(ctx context.Context) ([]Component, error) {
	var res listComponentsResult
	if err := c.callTool(ctx, ToolListComponents, struct{}{}, &res); err != nil {
		return nil, err
	}
	return res.Components, nil
}

type componentOnlyArgs struct {
	Component string `json:"component,omitempty"`
}

func (c *HTTPClient) Build(ctx context.Context, component string) (*ToolResult, error) {
	var res ToolResult
	if err := c.callTool(ctx, ToolBuild, componentOnlyArgs{Component: component}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

type runTestsArgs struct {
	Component string `json:"component,omitempty"`
	Focus     string `json:"focus,omitempty"`
}

func (c *HTTPClient) RunTests(ctx context.Context, component, focus string) (*ToolResult, error) {
	var res ToolResult
	if err := c.callTool(ctx, ToolRunTests, runTestsArgs{Component: component, Focus: focus}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *HTTPClient) Lint(ctx context.Context, component string) (*ToolResult, error) {
	var res ToolResult
	if err := c.callTool(ctx, ToolLint, componentOnlyArgs{Component: component}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

type affectedArgs struct {
	ChangedPaths []string `json:"changed_paths"`
}

func (c *HTTPClient) AffectedComponents(ctx context.Context, paths []string) ([]string, error) {
	var res AffectedResult
	if err := c.callTool(ctx, ToolAffectedComponents, affectedArgs{ChangedPaths: paths}, &res); err != nil {
		return nil, err
	}
	return res.Components, nil
}
```

- [ ] Run to verify pass: `ginkgo ./agent/devmcp/` — all specs green.
- [ ] Generate the counterfeiter fake for downstream consumers (harvest-step): `go generate ./agent/devmcp/...` — expect `agent/devmcp/devmcpfakes/fake_client.go` created; then `go build ./agent/devmcp/...` succeeds.
- [ ] Commit: `git add agent/devmcp && git commit -m "feat(devmcp): streamable-HTTP Go client with SSE progress and counterfeiter fake" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 4: `ci-agent/devmcp` MCP server core with progress/SSE

The server re-implements the `atc/api/mcpserver` pattern inside the ci-agent module (ci-agent cannot import the main module — 00-shared-contracts conventions bullet 2) and adds the one thing the precedent lacks: SSE progress streaming.

**Files:**
- Create: `ci-agent/devmcp/server.go`
- Test: `ci-agent/devmcp/devmcp_suite_test.go`, `ci-agent/devmcp/server_test.go`

**Steps:**

- [ ] Write the Ginkgo bootstrap `ci-agent/devmcp/devmcp_suite_test.go`:

```go
package devmcp_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDevMCP(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DevMCP Server Suite")
}
```

- [ ] Write the failing test `ci-agent/devmcp/server_test.go`:

```go
package devmcp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/devmcp"
)

func post(url, body string) map[string]any {
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	var decoded map[string]any
	Expect(json.NewDecoder(resp.Body).Decode(&decoded)).To(Succeed())
	return decoded
}

var _ = Describe("Server", func() {
	var ts *httptest.Server

	newServer := func(heartbeat time.Duration, handler devmcp.ToolHandler) {
		s := devmcp.NewServer(heartbeat)
		s.AddTool("echo", "echoes back", json.RawMessage(`{"type":"object"}`), handler)
		ts = httptest.NewServer(s)
		DeferCleanup(ts.Close)
	}

	It("answers initialize with the tools capability", func() {
		newServer(0, nil)
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
		result := resp["result"].(map[string]any)
		Expect(result["protocolVersion"]).NotTo(BeEmpty())
		caps := result["capabilities"].(map[string]any)
		Expect(caps).To(HaveKey("tools"))
	})

	It("lists registered tools with schemas", func() {
		newServer(0, nil)
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		tools := resp["result"].(map[string]any)["tools"].([]any)
		Expect(tools).To(HaveLen(1))
		tool := tools[0].(map[string]any)
		Expect(tool["name"]).To(Equal("echo"))
		Expect(tool["description"]).To(Equal("echoes back"))
		Expect(tool["inputSchema"]).To(HaveKeyWithValue("type", "object"))
	})

	It("returns the tool payload as a single text content block", func() {
		newServer(0, func(_ context.Context, _ json.RawMessage, _ devmcp.ProgressFunc) (any, error) {
			return map[string]any{"status": "ok", "summary": "done"}, nil
		})
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
		Expect(resp).NotTo(HaveKey("error"))
		content := resp["result"].(map[string]any)["content"].([]any)
		Expect(content).To(HaveLen(1))
		block := content[0].(map[string]any)
		Expect(block["type"]).To(Equal("text"))
		Expect(block["text"]).To(MatchJSON(`{"status":"ok","summary":"done"}`))
	})

	It("maps handler errors to JSON-RPC -32602 (malformed input only)", func() {
		newServer(0, func(_ context.Context, _ json.RawMessage, _ devmcp.ProgressFunc) (any, error) {
			return nil, devmcp.ErrInvalidParams("unknown component: nope")
		})
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
		rpcErr := resp["error"].(map[string]any)
		Expect(rpcErr["code"]).To(BeEquivalentTo(-32602))
		Expect(rpcErr["message"]).To(ContainSubstring("unknown component"))
	})

	It("returns -32602 for an unknown tool and -32601 for an unknown method", func() {
		newServer(0, nil)
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
		Expect(resp["error"].(map[string]any)["code"]).To(BeEquivalentTo(-32602))

		resp = post(ts.URL, `{"jsonrpc":"2.0","id":6,"method":"bogus/method"}`)
		Expect(resp["error"].(map[string]any)["code"]).To(BeEquivalentTo(-32601))
	})

	It("streams progress notifications over SSE when the client asks for them", func() {
		newServer(50*time.Millisecond, func(_ context.Context, _ json.RawMessage, progress devmcp.ProgressFunc) (any, error) {
			progress("halfway there")
			time.Sleep(150 * time.Millisecond)
			return map[string]any{"status": "ok"}, nil
		})

		req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(
			`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{},"_meta":{"progressToken":"tok-1"}}}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.Header.Get("Content-Type")).To(Equal("text/event-stream"))

		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(ContainSubstring(`"notifications/progress"`))
		Expect(string(body)).To(ContainSubstring(`"tok-1"`))
		Expect(string(body)).To(ContainSubstring("halfway there"))
		Expect(string(body)).To(ContainSubstring(`"id":7`))
	})
})
```

- [ ] Run to verify failure: `cd ci-agent && go test ./devmcp/ -count=1` — expect compile failure (`undefined: devmcp.NewServer` etc.).
- [ ] Write `ci-agent/devmcp/server.go`:

```go
// Package devmcp implements a generic, config-driven dev-mcp server
// (docs/superpowers/plans/agentic-platform/00-shared-contracts.md §3.1):
// a streamable-HTTP MCP server whose five tools are backed by per-component
// commands declared in dev-mcp.yml.
//
// The JSON-RPC plumbing mirrors the main module's atc/api/mcpserver
// precedent (which this standalone module cannot import) and adds SSE
// progress streaming, which the precedent lacks.
package devmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const protocolVersion = "2024-11-05"

// DefaultHeartbeat is half the contract's "progress at least every 30s"
// bound, leaving margin for scheduling jitter.
const DefaultHeartbeat = 15 * time.Second

// ProgressFunc reports the latest human-readable progress line for a
// running tool call.
type ProgressFunc func(message string)

// ToolHandler handles one MCP tool call. Returning a non-nil error signals
// MALFORMED INPUT ONLY (mapped to JSON-RPC -32602); run outcomes — including
// tooling breakage — are expressed in the returned payload's status field.
type ToolHandler func(ctx context.Context, args json.RawMessage, progress ProgressFunc) (any, error)

// ErrInvalidParams builds the error a ToolHandler returns for malformed
// input; the server maps it to JSON-RPC -32602.
func ErrInvalidParams(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// ToolDef describes one registered tool for tools/list.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Server is a streamable-HTTP MCP server.
type Server struct {
	tools     []ToolDef
	handlers  map[string]ToolHandler
	heartbeat time.Duration
}

// NewServer creates a server. heartbeat <= 0 uses DefaultHeartbeat.
func NewServer(heartbeat time.Duration) *Server {
	if heartbeat <= 0 {
		heartbeat = DefaultHeartbeat
	}
	return &Server{handlers: map[string]ToolHandler{}, heartbeat: heartbeat}
}

// AddTool registers a tool (atc/api/mcpserver registration style).
func (s *Server) AddTool(name, description string, schema json.RawMessage, handler ToolHandler) {
	s.tools = append(s.tools, ToolDef{Name: name, Description: description, InputSchema: schema})
	s.handlers[name] = handler
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Meta      *struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta,omitempty"`
}

type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ServeHTTP implements the MCP streamable HTTP transport: POST requests
// carry JSON-RPC messages; responses are JSON, or SSE for progress-bearing
// tools/call requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "failed to read request body"}})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}

	switch req.Method {
	case "initialize":
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "dev-mcp", "version": "1"},
		}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusNoContent)
	case "tools/list":
		tools := s.tools
		if tools == nil {
			tools = []ToolDef{}
		}
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools}})
	case "ping":
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "tools/call":
		s.handleToolsCall(w, r, &req)
	default:
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}})
	}
}

func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req *rpcRequest) {
	var params callToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
		return
	}
	handler, ok := s.handlers[params.Name]
	if !ok {
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", params.Name)}})
		return
	}

	flusher, canFlush := w.(http.Flusher)
	wantSSE := canFlush &&
		strings.Contains(r.Header.Get("Accept"), "text/event-stream") &&
		params.Meta != nil && len(params.Meta.ProgressToken) > 0

	if !wantSSE {
		result, err := handler(r.Context(), params.Arguments, func(string) {})
		writeJSON(w, s.toolResponse(req.ID, result, err))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	progressCh := make(chan string, 64)
	done := make(chan *rpcResponse, 1)
	go func() {
		result, err := handler(r.Context(), params.Arguments, func(msg string) {
			select {
			case progressCh <- msg:
			default: // never block the running tool on a slow consumer
			}
		})
		done <- s.toolResponse(req.ID, result, err)
	}()

	emit := func(msg string) {
		writeSSE(w, &rpcResponse{
			JSONRPC: "2.0",
			Method:  "notifications/progress",
			Params: map[string]any{
				"progressToken": params.Meta.ProgressToken,
				"message":       msg,
			},
		})
		flusher.Flush()
	}

	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	lastMsg := fmt.Sprintf("running %s", params.Name)
	for {
		select {
		case msg := <-progressCh:
			lastMsg = msg // coalesce: remember, emit on the next tick
		case <-ticker.C:
			emit(lastMsg)
		case resp := <-done:
			writeSSE(w, resp)
			flusher.Flush()
			return
		}
	}
}

func (s *Server) toolResponse(id json.RawMessage, result any, err error) *rpcResponse {
	if err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32602, Message: err.Error()}}
	}
	payload, merr := json.Marshal(result)
	if merr != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32603, Message: fmt.Sprintf("marshal result: %s", merr)}}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: callToolResult{
		Content: []contentBlock{{Type: "text", Text: string(payload)}},
	}}
}

func writeJSON(w http.ResponseWriter, resp *rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeSSE(w io.Writer, resp *rpcResponse) {
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
}
```

- [ ] Run to verify pass: `cd ci-agent && go test ./devmcp/ -count=1` — all specs green.
- [ ] Commit: `git add ci-agent/devmcp && git commit -m "feat(ci-agent/devmcp): streamable-HTTP MCP server core with SSE progress heartbeats" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 5: `ci-agent/devmcp` config — `dev-mcp.yml` parsing and validation

**Files:**
- Create: `ci-agent/devmcp/config.go`
- Test: `ci-agent/devmcp/config_test.go`

**Steps:**

- [ ] Write the failing test `ci-agent/devmcp/config_test.go`:

```go
package devmcp_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/devmcp"
)

var _ = Describe("Config", func() {
	valid := []byte(`
schema_version: 1
repo:
  test: { cmd: ["make", "test-quick"], failed_exit_codes: [1, 2] }
components:
  - id: app
    description: single shell-script application
    paths: ["src/"]
    kind: cli
    build: { cmd: ["sh", "scripts/build.sh"] }
    test:  { cmd: ["sh", "scripts/test.sh"], focus_flag: "--focus" }
    lint:  { cmd: ["sh", "scripts/lint.sh"] }
`)

	It("parses a valid config", func() {
		cfg, err := devmcp.Parse(valid)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.SchemaVersion).To(Equal(1))
		Expect(cfg.Repo.Test.Cmd).To(Equal([]string{"make", "test-quick"}))
		Expect(cfg.Repo.Test.FailedExitCodes).To(Equal([]int{1, 2}))
		Expect(cfg.Components).To(HaveLen(1))
		Expect(cfg.Components[0].ID).To(Equal("app"))
		Expect(cfg.Components[0].Test.FocusFlag).To(Equal("--focus"))

		comp, found := cfg.Component("app")
		Expect(found).To(BeTrue())
		Expect(comp.Kind).To(Equal("cli"))
		_, found = cfg.Component("nope")
		Expect(found).To(BeFalse())
	})

	DescribeTable("rejects invalid configs",
		func(yaml string, msg string) {
			_, err := devmcp.Parse([]byte(yaml))
			Expect(err).To(MatchError(ContainSubstring(msg)))
		},
		Entry("wrong schema_version",
			"schema_version: 2\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: cli}\n",
			"unsupported schema_version"),
		Entry("no components",
			"schema_version: 1\ncomponents: []\n",
			"at least one component"),
		Entry("duplicate ids",
			"schema_version: 1\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: cli}\n  - {id: a, description: d, paths: [\"b/\"], kind: cli}\n",
			"duplicate id"),
		Entry("invalid kind",
			"schema_version: 1\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: banana}\n",
			"invalid kind"),
		Entry("missing paths",
			"schema_version: 1\ncomponents:\n  - {id: a, description: d, paths: [], kind: cli}\n",
			"paths is required"),
		Entry("empty cmd",
			"schema_version: 1\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: cli, build: {cmd: []}}\n",
			"cmd must be non-empty"),
		Entry("unknown top-level key (strict decoding)",
			"schema_version: 1\nbogus: true\ncomponents:\n  - {id: a, description: d, paths: [\"a/\"], kind: cli}\n",
			"bogus"),
	)
})
```

- [ ] Run to verify failure: `cd ci-agent && go test ./devmcp/ -count=1` — compile failure (`undefined: devmcp.Parse`).
- [ ] Write `ci-agent/devmcp/config.go`:

```go
package devmcp

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// Config is the dev-mcp.yml reference-implementation config: the repo's
// component inventory and the commands backing each tool. Format pinned in
// 00-shared-contracts.md §11 (dev-mcp interface finalization).
type Config struct {
	SchemaVersion int               `yaml:"schema_version"`
	Repo          *ToolCommands     `yaml:"repo,omitempty"` // whole-repo commands (component omitted)
	Components    []ComponentConfig `yaml:"components"`
}

// ToolCommands groups the three command slots.
type ToolCommands struct {
	Build *CommandSpec `yaml:"build,omitempty"`
	Test  *CommandSpec `yaml:"test,omitempty"`
	Lint  *CommandSpec `yaml:"lint,omitempty"`
}

// ComponentConfig declares one component and its commands.
type ComponentConfig struct {
	ID          string       `yaml:"id"`
	Description string       `yaml:"description"`
	Paths       []string     `yaml:"paths"`
	Kind        string       `yaml:"kind"`
	Build       *CommandSpec `yaml:"build,omitempty"`
	Test        *CommandSpec `yaml:"test,omitempty"`
	Lint        *CommandSpec `yaml:"lint,omitempty"`
}

// CommandSpec is one runnable command.
type CommandSpec struct {
	Cmd             []string `yaml:"cmd"`
	Dir             string   `yaml:"dir,omitempty"`               // workdir-relative
	FocusFlag       string   `yaml:"focus_flag,omitempty"`        // run_tests only; appended as <flag>=<focus>
	FailedExitCodes []int    `yaml:"failed_exit_codes,omitempty"` // exit codes meaning "failed"; default [1]
}

var validKinds = map[string]bool{
	"service": true, "library": true, "cli": true,
	"web": true, "docs": true, "other": true,
}

// Load reads and validates a dev-mcp.yml file.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

// Parse validates eagerly (phaseconfig style): all config errors surface at
// startup, never mid-tool-call.
func Parse(raw []byte) (Config, error) {
	var cfg Config
	if err := yaml.UnmarshalWithOptions(raw, &cfg, yaml.Strict()); err != nil {
		return Config{}, fmt.Errorf("parse dev-mcp.yml: %w", err)
	}
	if cfg.SchemaVersion != 1 {
		return Config{}, fmt.Errorf("unsupported schema_version %d (want 1)", cfg.SchemaVersion)
	}
	if len(cfg.Components) == 0 {
		return Config{}, fmt.Errorf("at least one component is required")
	}
	seen := map[string]bool{}
	for i, comp := range cfg.Components {
		if comp.ID == "" {
			return Config{}, fmt.Errorf("components[%d]: id is required", i)
		}
		if seen[comp.ID] {
			return Config{}, fmt.Errorf("components[%d]: duplicate id %q", i, comp.ID)
		}
		seen[comp.ID] = true
		if !validKinds[comp.Kind] {
			return Config{}, fmt.Errorf("component %q: invalid kind %q", comp.ID, comp.Kind)
		}
		if len(comp.Paths) == 0 {
			return Config{}, fmt.Errorf("component %q: paths is required", comp.ID)
		}
		if err := validateSpecs(comp.ID, comp.Build, comp.Test, comp.Lint); err != nil {
			return Config{}, err
		}
	}
	if cfg.Repo != nil {
		if err := validateSpecs("repo", cfg.Repo.Build, cfg.Repo.Test, cfg.Repo.Lint); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func validateSpecs(owner string, specs ...*CommandSpec) error {
	for _, spec := range specs {
		if spec != nil && len(spec.Cmd) == 0 {
			return fmt.Errorf("%s: cmd must be non-empty", owner)
		}
	}
	return nil
}

// Component returns the component with the given id.
func (c Config) Component(id string) (ComponentConfig, bool) {
	for _, comp := range c.Components {
		if comp.ID == id {
			return comp, true
		}
	}
	return ComponentConfig{}, false
}
```

- [ ] Run to verify pass: `cd ci-agent && go test ./devmcp/ -count=1` — all green.
- [ ] Commit: `git add ci-agent/devmcp && git commit -m "feat(ci-agent/devmcp): dev-mcp.yml config parsing with eager validation" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 6: `ci-agent/devmcp` command runner — exit-code taxonomy, log capture, tail, progress

**Files:**
- Create: `ci-agent/devmcp/runner.go`
- Test: `ci-agent/devmcp/runner_test.go`

**Steps:**

- [ ] Write the failing white-box test `ci-agent/devmcp/runner_test.go` (internal package so it can call `runCommand`; Ginkgo collects specs from both internal and external test files into the Task 4 suite):

```go
package devmcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("runCommand", func() {
	var workdir string

	BeforeEach(func() {
		workdir = GinkgoT().TempDir()
	})

	run := func(spec CommandSpec, extra []string, progress ProgressFunc) ToolResult {
		if progress == nil {
			progress = func(string) {}
		}
		return runCommand(context.Background(), workdir, "test-app", spec, extra, progress)
	}

	It("classifies exit 0 as ok and captures output, duration, and a log file", func() {
		res := run(CommandSpec{Cmd: []string{"sh", "-c", "echo one; echo two"}}, nil, nil)
		Expect(res.Status).To(Equal(StatusOK))
		Expect(res.Summary).To(ContainSubstring("ok"))
		Expect(res.DurationSeconds).To(BeNumerically(">=", 0))
		Expect(res.OutputTail).To(ContainSubstring("one"))
		Expect(res.OutputTail).To(ContainSubstring("two"))

		Expect(res.LogPath).To(HavePrefix(filepath.Join(".dev-mcp", "logs")))
		logged, err := os.ReadFile(filepath.Join(workdir, res.LogPath))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(logged)).To(ContainSubstring("one\ntwo\n"))
	})

	It("classifies exit 1 as failed by default", func() {
		res := run(CommandSpec{Cmd: []string{"sh", "-c", "echo boom; exit 1"}}, nil, nil)
		Expect(res.Status).To(Equal(StatusFailed))
		Expect(res.Summary).To(ContainSubstring("exit 1"))
	})

	It("honors failed_exit_codes and treats unlisted codes as error", func() {
		spec := CommandSpec{Cmd: []string{"sh", "-c", "exit 2"}, FailedExitCodes: []int{2}}
		Expect(run(spec, nil, nil).Status).To(Equal(StatusFailed))

		spec = CommandSpec{Cmd: []string{"sh", "-c", "exit 1"}, FailedExitCodes: []int{2}}
		Expect(run(spec, nil, nil).Status).To(Equal(StatusError))
	})

	It("classifies spawn failures as error", func() {
		res := run(CommandSpec{Cmd: []string{"definitely-not-a-real-binary-xyz"}}, nil, nil)
		Expect(res.Status).To(Equal(StatusError))
	})

	It("appends extra args (the focus flag)", func() {
		res := run(CommandSpec{Cmd: []string{"echo", "base"}}, []string{"--focus=MySpec"}, nil)
		Expect(res.Status).To(Equal(StatusOK))
		Expect(res.OutputTail).To(ContainSubstring("base --focus=MySpec"))
	})

	It("keeps only the last 200 output lines in the tail", func() {
		script := "i=1; while [ $i -le 300 ]; do echo line-$i; i=$((i+1)); done"
		res := run(CommandSpec{Cmd: []string{"sh", "-c", script}}, nil, nil)
		Expect(res.OutputTail).NotTo(ContainSubstring("line-100\n"))
		Expect(res.OutputTail).To(ContainSubstring("line-101"))
		Expect(res.OutputTail).To(ContainSubstring("line-300"))
	})

	It("reports each completed output line to the progress func", func() {
		var mu sync.Mutex
		var lines []string
		run(CommandSpec{Cmd: []string{"sh", "-c", "echo alpha; echo beta"}}, nil, func(msg string) {
			mu.Lock()
			lines = append(lines, msg)
			mu.Unlock()
		})
		mu.Lock()
		defer mu.Unlock()
		Expect(lines).To(ContainElements("alpha", "beta"))
	})

	It("runs in the spec's dir relative to the workdir", func() {
		Expect(os.MkdirAll(filepath.Join(workdir, "sub"), 0o755)).To(Succeed())
		res := run(CommandSpec{Cmd: []string{"pwd"}, Dir: "sub"}, nil, nil)
		Expect(res.OutputTail).To(HaveSuffix(fmt.Sprintf("%c%s", filepath.Separator, "sub")))
	})
})
```

- [ ] Run to verify failure: `cd ci-agent && go test ./devmcp/ -count=1` — compile failure (`undefined: runCommand`, `StatusOK`...).
- [ ] Write `ci-agent/devmcp/runner.go`:

```go
package devmcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Status values mirror the contract taxonomy (§3.1): ok = passed,
// failed = ran and found problems, error = tooling broke.
const (
	StatusOK     = "ok"
	StatusFailed = "failed"
	StatusError  = "error"
)

const tailLines = 200

// ToolResult is the §3.1 shared result payload for build/run_tests/lint
// (wire shape mirrored from the main module's agent/devmcp, which this
// standalone module cannot import; the contract kit enforces conformance).
type ToolResult struct {
	Status          string    `json:"status"`
	Summary         string    `json:"summary"`
	DurationSeconds float64   `json:"duration_seconds"`
	OutputTail      string    `json:"output_tail,omitempty"`
	LogPath         string    `json:"log_path,omitempty"`
	Failures        []Failure `json:"failures,omitempty"`
}

// Failure is one structured failure. The v1 reference implementation never
// populates these (the field is optional in the contract).
type Failure struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// lineTail is an io.Writer that keeps the last tailLines completed lines,
// reports each completed line to progress, and mirrors all bytes to an
// underlying writer (the log file). It is safe for the concurrent
// stdout/stderr writes exec.Cmd performs.
type lineTail struct {
	mu       sync.Mutex
	mirror   io.Writer
	lines    []string
	partial  strings.Builder
	progress ProgressFunc
}

func (t *lineTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.mirror != nil {
		t.mirror.Write(p)
	}
	for _, b := range p {
		if b == '\n' {
			line := t.partial.String()
			t.partial.Reset()
			t.lines = append(t.lines, line)
			if len(t.lines) > tailLines {
				t.lines = t.lines[1:]
			}
			if t.progress != nil && strings.TrimSpace(line) != "" {
				t.progress(line)
			}
		} else {
			t.partial.WriteByte(b)
		}
	}
	return len(p), nil
}

func (t *lineTail) Tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// runCommand executes one CommandSpec under workdir and classifies its exit
// per the contract's exit-code convention: 0 → ok; failed_exit_codes
// (default [1]) → failed; anything else — other exit codes, spawn failure,
// context cancellation — → error.
func runCommand(ctx context.Context, workdir, label string, spec CommandSpec, extraArgs []string, progress ProgressFunc) ToolResult {
	start := time.Now()

	relLog := filepath.Join(".dev-mcp", "logs", fmt.Sprintf("%s-%d.log", label, start.UnixNano()))
	absLog := filepath.Join(workdir, relLog)
	if err := os.MkdirAll(filepath.Dir(absLog), 0o755); err != nil {
		return errorResult(label, start, "", fmt.Sprintf("create log dir: %s", err))
	}
	logFile, err := os.Create(absLog)
	if err != nil {
		return errorResult(label, start, "", fmt.Sprintf("create log file: %s", err))
	}
	defer logFile.Close()

	tail := &lineTail{mirror: logFile, progress: progress}

	args := append(append([]string{}, spec.Cmd[1:]...), extraArgs...)
	cmd := exec.CommandContext(ctx, spec.Cmd[0], args...)
	cmd.Dir = filepath.Join(workdir, spec.Dir) // spec.Dir == "" keeps workdir
	cmd.Stdout = tail
	cmd.Stderr = tail

	runErr := cmd.Run()
	duration := time.Since(start).Seconds()

	status := StatusOK
	detail := "exit 0"
	if runErr != nil {
		status = StatusError
		detail = runErr.Error()
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && ctx.Err() == nil {
			code := exitErr.ExitCode()
			detail = fmt.Sprintf("exit %d", code)
			for _, failed := range spec.failedCodes() {
				if code == failed {
					status = StatusFailed
					break
				}
			}
		}
	}

	return ToolResult{
		Status:          status,
		Summary:         fmt.Sprintf("%s: %s (%s) in %.1fs", label, status, detail, duration),
		DurationSeconds: duration,
		OutputTail:      tail.Tail(),
		LogPath:         relLog,
	}
}

func (s CommandSpec) failedCodes() []int {
	if len(s.FailedExitCodes) == 0 {
		return []int{1}
	}
	return s.FailedExitCodes
}

func errorResult(label string, start time.Time, logPath, msg string) ToolResult {
	return ToolResult{
		Status:          StatusError,
		Summary:         fmt.Sprintf("%s: error (%s)", label, msg),
		DurationSeconds: time.Since(start).Seconds(),
		LogPath:         logPath,
	}
}
```

- [ ] Run to verify pass: `cd ci-agent && go test ./devmcp/ -count=1` — all green.
- [ ] Commit: `git add ci-agent/devmcp && git commit -m "feat(ci-agent/devmcp): command runner with exit-code taxonomy, log capture, 200-line tail" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 7: `ci-agent/devmcp` — the five tools

**Files:**
- Create: `ci-agent/devmcp/tools.go`
- Test: `ci-agent/devmcp/tools_test.go`

**Steps:**

- [ ] Write the failing test `ci-agent/devmcp/tools_test.go`:

```go
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
```

- [ ] Run to verify failure: `cd ci-agent && go test ./devmcp/ -count=1` — compile failure (`undefined: devmcp.RegisterTools`).
- [ ] Write `ci-agent/devmcp/tools.go`:

```go
package devmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Component is the wire shape of one list_components entry (§3.1).
type Component struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	Kind        string   `json:"kind"`
}

// AffectedResult is the affected_components result payload.
type AffectedResult struct {
	Components    []string `json:"components"`
	UnmappedPaths []string `json:"unmapped_paths"`
}

// Input schemas: copied verbatim from 00-shared-contracts.md §3.1.
var (
	emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

	componentSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "component": {"type": "string", "description": "component id; omitted = whole repo"}
  },
  "additionalProperties": false
}`)

	runTestsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "component": {"type": "string"},
    "focus": {"type": "string", "description": "test-name filter, implementation-defined semantics (e.g. ginkgo --focus)"}
  },
  "additionalProperties": false
}`)

	affectedSchema = json.RawMessage(`{
  "type": "object",
  "required": ["changed_paths"],
  "properties": {
    "changed_paths": {"type": "array", "items": {"type": "string"}}
  },
  "additionalProperties": false
}`)
)

// RegisterTools wires the five §3.1 tools onto the server, backed by cfg's
// commands executed under workdir.
func RegisterTools(s *Server, cfg Config, workdir string) {
	registerListComponents(s, cfg)
	registerCommandTool(s, cfg, workdir, "build",
		"Build a component (or the whole repo when component is omitted).")
	registerRunTests(s, cfg, workdir)
	registerCommandTool(s, cfg, workdir, "lint",
		"Lint a component (or the whole repo when component is omitted).")
	registerAffectedComponents(s, cfg)
}

func registerListComponents(s *Server, cfg Config) {
	s.AddTool("list_components", "List this repo's buildable/testable components.", emptyObjectSchema,
		func(_ context.Context, _ json.RawMessage, _ ProgressFunc) (any, error) {
			comps := make([]Component, len(cfg.Components))
			for i, c := range cfg.Components {
				comps[i] = Component{ID: c.ID, Description: c.Description, Paths: c.Paths, Kind: c.Kind}
			}
			return map[string]any{"components": comps}, nil
		})
}

type componentArgs struct {
	Component string `json:"component"`
	Focus     string `json:"focus"`
}

func parseComponentArgs(raw json.RawMessage, allowFocus bool) (componentArgs, error) {
	var args componentArgs
	if len(raw) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&args); err != nil {
			return componentArgs{}, ErrInvalidParams("invalid arguments: %s", err)
		}
	}
	if !allowFocus && args.Focus != "" {
		return componentArgs{}, ErrInvalidParams("invalid arguments: focus is not accepted by this tool")
	}
	return args, nil
}

func pickSpec(tool string, build, test, lint *CommandSpec) *CommandSpec {
	switch tool {
	case "build":
		return build
	case "test":
		return test
	case "lint":
		return lint
	}
	return nil
}

// resolveSpec picks the CommandSpec for tool ("build"|"test"|"lint") and
// component (empty = whole-repo scope, which requires the repo: section).
// All misses are malformed input (-32602 at the server layer).
func resolveSpec(cfg Config, tool, component string) (CommandSpec, string, error) {
	if component == "" {
		if cfg.Repo != nil {
			if spec := pickSpec(tool, cfg.Repo.Build, cfg.Repo.Test, cfg.Repo.Lint); spec != nil {
				return *spec, tool + "-repo", nil
			}
		}
		return CommandSpec{}, "", ErrInvalidParams(
			"whole-repo %s is not configured (no repo: section in dev-mcp.yml); pass a component", tool)
	}
	comp, found := cfg.Component(component)
	if !found {
		return CommandSpec{}, "", ErrInvalidParams("unknown component: %s", component)
	}
	spec := pickSpec(tool, comp.Build, comp.Test, comp.Lint)
	if spec == nil {
		return CommandSpec{}, "", ErrInvalidParams("component %s does not support %s", component, tool)
	}
	return *spec, fmt.Sprintf("%s-%s", tool, component), nil
}

func registerCommandTool(s *Server, cfg Config, workdir, tool, description string) {
	s.AddTool(tool, description, componentSchema,
		func(ctx context.Context, raw json.RawMessage, progress ProgressFunc) (any, error) {
			args, err := parseComponentArgs(raw, false)
			if err != nil {
				return nil, err
			}
			spec, label, err := resolveSpec(cfg, tool, args.Component)
			if err != nil {
				return nil, err
			}
			return runCommand(ctx, workdir, label, spec, nil, progress), nil
		})
}

func registerRunTests(s *Server, cfg Config, workdir string) {
	s.AddTool("run_tests", "Run a component's tests (or the whole repo's when component is omitted).", runTestsSchema,
		func(ctx context.Context, raw json.RawMessage, progress ProgressFunc) (any, error) {
			args, err := parseComponentArgs(raw, true)
			if err != nil {
				return nil, err
			}
			spec, label, err := resolveSpec(cfg, "test", args.Component)
			if err != nil {
				return nil, err
			}
			var extra []string
			if args.Focus != "" {
				if spec.FocusFlag == "" {
					return nil, ErrInvalidParams("component %q does not support focus", args.Component)
				}
				extra = []string{fmt.Sprintf("%s=%s", spec.FocusFlag, args.Focus)}
			}
			return runCommand(ctx, workdir, label, spec, extra, progress), nil
		})
}

func registerAffectedComponents(s *Server, cfg Config) {
	s.AddTool("affected_components", "Map changed file paths to the component ids that own them.", affectedSchema,
		func(_ context.Context, raw json.RawMessage, _ ProgressFunc) (any, error) {
			var args struct {
				ChangedPaths *[]string `json:"changed_paths"`
			}
			if err := json.Unmarshal(raw, &args); err != nil || args.ChangedPaths == nil {
				return nil, ErrInvalidParams("invalid arguments: changed_paths is required")
			}
			hit := map[string]bool{}
			unmapped := []string{}
			for _, changed := range *args.ChangedPaths {
				clean := filepath.ToSlash(filepath.Clean(changed))
				matched := false
				for _, comp := range cfg.Components {
					for _, prefix := range comp.Paths {
						p := strings.TrimSuffix(filepath.ToSlash(prefix), "/")
						if clean == p || strings.HasPrefix(clean, p+"/") {
							hit[comp.ID] = true
							matched = true
						}
					}
				}
				if !matched {
					unmapped = append(unmapped, changed)
				}
			}
			ids := make([]string, 0, len(hit))
			for id := range hit {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			return AffectedResult{Components: ids, UnmappedPaths: unmapped}, nil
		})
}
```

- [ ] Run to verify pass: `cd ci-agent && go test ./devmcp/ -count=1` — all green.
- [ ] Commit: `git add ci-agent/devmcp && git commit -m "feat(ci-agent/devmcp): implement the five dev-mcp contract tools" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 8: `ci-agent/cmd/dev-mcp` binary — `/mcp` + `/healthz`

**Files:**
- Create: `ci-agent/cmd/dev-mcp/main.go`
- Test: `ci-agent/cmd/dev-mcp/main_test.go`

> **Amended 2026-07-10 (verifier follow-up on the F13 SSE delta):** the binary enforces the frozen heartbeat bounds (00 §3.1 as amended 2026-07-09: a set-but-invalid `DEV_MCP_PROGRESS_INTERVAL`, a value `<= 0`, or a value `> 30s` is a FATAL startup error — never clamped silently). The pre-amendment `main.go` only caught unparseable values: `0s`/`-5s` flowed into `devmcp.NewServer`, whose `<= 0 → DefaultHeartbeat` fallback is exactly the forbidden silent clamp, and `45s` was accepted past the ≤30s bound. Mirrors 08 Task 9/Task 13 and 10 Task 2; the `NewServer` fallback itself stays (unset env = library default), matching `mcpserver.NewServerWithHeartbeat`.

**Steps:**

- [ ] Write `ci-agent/cmd/dev-mcp/main.go`:

```go
// Command dev-mcp serves a repo's dev-mcp implementation: the five-tool MCP
// contract (00-shared-contracts.md §3.1) backed by commands declared in
// dev-mcp.yml, over streamable HTTP, with GET /healthz for readiness probes
// (§8.5 packaging convention).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/concourse/ci-agent/devmcp"
)

func main() {
	configPath := flag.String("config", "dev-mcp.yml", "path to the dev-mcp config file")
	workdir := flag.String("workdir", ".", "workspace root that commands run in")
	flag.Parse()

	cfg, err := devmcp.Load(*configPath)
	if err != nil {
		log.Fatalf("dev-mcp: %s", err)
	}

	// F13 delta (00 §3.1, 2026-07-09): a set-but-invalid, <= 0, or > 30s
	// heartbeat is a FATAL startup error — never clamped silently. The
	// <= 0 check must live HERE: devmcp.NewServer's <= 0 fallback would
	// otherwise silently substitute DefaultHeartbeat.
	heartbeat := devmcp.DefaultHeartbeat
	if raw := os.Getenv("DEV_MCP_PROGRESS_INTERVAL"); raw != "" {
		heartbeat, err = time.ParseDuration(raw)
		if err != nil {
			log.Fatalf("dev-mcp: invalid DEV_MCP_PROGRESS_INTERVAL: %s", err)
		}
		if heartbeat <= 0 || heartbeat > 30*time.Second {
			log.Fatalf("dev-mcp: DEV_MCP_PROGRESS_INTERVAL must be > 0 and <= 30s (contracts §3.1 progress bound), got %s", heartbeat)
		}
	}

	server := devmcp.NewServer(heartbeat)
	devmcp.RegisterTools(server, cfg, *workdir)

	addr := os.Getenv("MCP_LISTEN_ADDR")
	if addr == "" {
		addr = ":7780"
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", server)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	httpServer := &http.Server{Addr: addr, Handler: mux}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("dev-mcp: serving %d components on %s", len(cfg.Components), addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("dev-mcp: %s", err)
	}
}
```

- [ ] Verify it builds: `cd ci-agent && go build ./cmd/dev-mcp` — expect success (delete the produced `dev-mcp` binary from the working tree afterwards: `rm -f ci-agent/dev-mcp`).
- [ ] Add the F13 heartbeat-bounds validation smoke `ci-agent/cmd/dev-mcp/main_test.go` (mirrors 08 Task 13's `TestServeModeFailsFastOnBadProgressInterval`; fails against a binary that ignores or clamps the env — in particular against the pre-amendment `main.go`, where `0s`/`-5s`/`45s` all started serving):

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMainFailsFastOnBadProgressInterval (F13 SSE seam delta, 00 §3.1
// 2026-07-09; mirrored from 08 Task 13 / 10 Task 2): a
// DEV_MCP_PROGRESS_INTERVAL that is invalid, <= 0, or > 30s must be a
// FATAL startup error — never clamped silently to DefaultHeartbeat.
func TestMainFailsFastOnBadProgressInterval(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "dev-mcp")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	smoke, err := filepath.Abs(filepath.Join("..", "..", "devmcp", "testdata", "smoke.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"bogus", "0s", "-5s", "45s"} {
		cmd := exec.Command(bin, "--config", smoke, "--workdir", t.TempDir())
		cmd.Env = append(os.Environ(), "DEV_MCP_PROGRESS_INTERVAL="+bad)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("DEV_MCP_PROGRESS_INTERVAL=%q: expected non-zero exit, got clean start", bad)
		}
		// the config is valid (Task 8 smoke.yml), so the fatal must be the
		// heartbeat's — assert the message names the env var, ruling out a
		// config-load failure masquerading as a pass.
		if !strings.Contains(string(out), "DEV_MCP_PROGRESS_INTERVAL") {
			t.Fatalf("DEV_MCP_PROGRESS_INTERVAL=%q: fatal exit, but not for the heartbeat: %s", bad, out)
		}
	}
}
```

- [ ] Run to verify pass: `cd ci-agent && go test ./cmd/dev-mcp/ -count=1` — all green (valid overrides stay covered by Task 11's e2e, which starts the binary with short in-bounds heartbeats like `200ms`).
- [ ] Smoke-check by hand (automated coverage lands in Task 11's e2e suite):

```bash
cd ci-agent && go run ./cmd/dev-mcp --config devmcp/testdata/smoke.yml --workdir /tmp &
SMOKE_PID=$!; sleep 1
curl -fsS http://127.0.0.1:7780/healthz            # expect: ok
curl -fsS -X POST http://127.0.0.1:7780/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | grep list_components
kill $SMOKE_PID
```

where `ci-agent/devmcp/testdata/smoke.yml` (create it as part of this step) is:

```yaml
schema_version: 1
components:
  - id: smoke
    description: smoke-test component
    paths: ["smoke/"]
    kind: other
    build: { cmd: ["true"] }
```

- [ ] Commit: `git add ci-agent/cmd/dev-mcp ci-agent/devmcp/testdata && git commit -m "feat(ci-agent): dev-mcp server binary serving /mcp and /healthz" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 9: This repo's `dev-mcp.yml` — the reference implementation config

Components per 00-shared-contracts.md §3.1: `atc`, `fly`, `web`, `ci-agent`, `topgun`, mapped from the CLAUDE.md/Makefile inventory and the root `package.json` scripts (verified: `build`, `test`, `analyse`).

**Files:**
- Create: `dev-mcp.yml` (repo root)
- Modify: `.gitignore:48` (append `.dev-mcp/`)
- Test: `ci-agent/devmcp/repoconfig_test.go`

**Steps:**

- [ ] Write the failing test `ci-agent/devmcp/repoconfig_test.go`:

```go
package devmcp_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/devmcp"
)

var _ = Describe("the repo's own dev-mcp.yml", func() {
	It("parses and declares the five contract components", func() {
		cfg, err := devmcp.Load("../../dev-mcp.yml")
		Expect(err).NotTo(HaveOccurred())

		var ids []string
		for _, comp := range cfg.Components {
			ids = append(ids, comp.ID)
		}
		Expect(ids).To(ConsistOf("atc", "fly", "web", "ci-agent", "topgun"))

		Expect(cfg.Repo).NotTo(BeNil())
		Expect(cfg.Repo.Build).NotTo(BeNil())
		Expect(cfg.Repo.Test).NotTo(BeNil())

		// every component has test; topgun is test-only (no Makefile
		// build/lint target exists for it — pinned in the §11 addendum)
		for _, comp := range cfg.Components {
			Expect(comp.Test).NotTo(BeNil(), "component %s must define test", comp.ID)
		}
		atc, _ := cfg.Component("atc")
		Expect(atc.Test.FocusFlag).To(Equal("--focus"))
		ciAgent, _ := cfg.Component("ci-agent")
		Expect(ciAgent.Test.Dir).To(Equal("ci-agent"))
	})
})
```

- [ ] Run to verify failure: `cd ci-agent && go test ./devmcp/ -count=1` — expect failure `read config: open ../../dev-mcp.yml: no such file or directory`.
- [ ] Write `dev-mcp.yml` at the repo root:

```yaml
# dev-mcp reference-implementation config for this repo
# (docs/superpowers/plans/agentic-platform/00-shared-contracts.md §3.1, §11).
# Commands mirror the CLAUDE.md test inventory and Makefile targets.
# NOTE: atc/fly test targets need a local PostgreSQL (see CLAUDE.md); the
# `web` component needs node/yarn (root package.json scripts).
schema_version: 1

repo:
  build: { cmd: ["go", "build", "./cmd/concourse", "./cmd/artifact-daemon"] }
  # make exits 2 when a sub-command fails, so 2 is a test failure, not
  # harness breakage.
  test:  { cmd: ["make", "test-quick"], failed_exit_codes: [1, 2] }
  lint:  { cmd: ["go", "vet", "./atc/...", "./fly/...", "./agent/..."] }

components:
  - id: atc
    description: "ATC web node: API, scheduler, worker runtimes, DB layer (Ginkgo; needs local PostgreSQL)"
    paths: ["atc/"]
    kind: service
    build: { cmd: ["go", "build", "./atc/..."] }
    test:
      cmd: ["ginkgo", "-r", "-p", "--keep-going", "--skip-package=integration", "./atc/"]
      focus_flag: "--focus"
    lint: { cmd: ["go", "vet", "./atc/..."] }

  - id: fly
    description: "fly CLI (Ginkgo; integration suite excluded — run via make test-fly-integration)"
    paths: ["fly/"]
    kind: cli
    build: { cmd: ["go", "build", "./fly/..."] }
    test:
      cmd: ["ginkgo", "-r", "--keep-going", "--skip-package=integration", "./fly/"]
      focus_flag: "--focus"
    lint: { cmd: ["go", "vet", "./fly/..."] }

  - id: web
    description: "Elm/JS frontend bundle (root package.json scripts; needs node + yarn)"
    paths: ["web/"]
    kind: web
    build: { cmd: ["yarn", "run", "build"] }
    test:  { cmd: ["yarn", "run", "test"] }
    lint:  { cmd: ["yarn", "run", "analyse"] }

  - id: ci-agent
    description: "standalone ci-agent module (review/QA/plan phases, dev-mcp server)"
    paths: ["ci-agent/"]
    kind: cli
    build: { cmd: ["go", "build", "./..."], dir: "ci-agent" }
    test:
      cmd: ["go", "test", "./...", "-count=1", "-timeout", "5m"]
      dir: "ci-agent"
      focus_flag: "-run"
    lint: { cmd: ["go", "vet", "./..."], dir: "ci-agent" }

  - id: topgun
    description: "K8s integration/behavioral suites (needs Docker + KinD/K3s; multi-minute; test only)"
    paths: ["topgun/"]
    kind: other
    test: { cmd: ["make", "test-k8s-integration"], failed_exit_codes: [1, 2] }
```

- [ ] Append to `.gitignore` (after the `package-lock.json` line at the end of the file):

```
# dev-mcp command logs (workspace-relative, contract §11 addendum)
.dev-mcp/
```

- [ ] Run to verify pass: `cd ci-agent && go test ./devmcp/ -count=1` — all green.
- [ ] Commit: `git add dev-mcp.yml .gitignore ci-agent/devmcp/repoconfig_test.go && git commit -m "feat(devmcp): repo-root dev-mcp.yml declaring atc/fly/web/ci-agent/topgun components" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 10: Contract-test kit `agent/devmcp/contracttest`

Plain `testing`-based (the kit's public API is `Run(t *testing.T, endpoint string)` per §3.1, so any external repo can call it from a vanilla `go test`). Checks are internal `func(ctx, ...) error` so they unit-test without a real server.

**Files:**
- Create: `agent/devmcp/contracttest/kit.go`, `agent/devmcp/contracttest/rpc.go`
- Test: `agent/devmcp/contracttest/kit_test.go`

**Steps:**

- [ ] Write the failing white-box test `agent/devmcp/contracttest/kit_test.go` (plain `testing`, same package):

```go
package contracttest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cannedServer answers every JSON-RPC method from a fixed response map,
// echoing the request id.
func cannedServer(responses map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  responses[req.Method],
		})
	}))
}

// toolText wraps a JSON payload string as an MCP tool result.
func toolText(payload string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": payload}},
	}
}

func TestCheckInitialize(t *testing.T) {
	good := cannedServer(map[string]any{
		"initialize": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
		},
	})
	defer good.Close()
	if err := checkInitialize(context.Background(), good.URL); err != nil {
		t.Fatalf("conforming server rejected: %v", err)
	}

	bad := cannedServer(map[string]any{
		"initialize": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
		},
	})
	defer bad.Close()
	err := checkInitialize(context.Background(), bad.URL)
	if err == nil || !strings.Contains(err.Error(), "tools capability") {
		t.Fatalf("expected tools-capability error, got %v", err)
	}
}

func TestCheckToolsListRequiresAllFiveTools(t *testing.T) {
	tool := func(name string) map[string]any {
		return map[string]any{
			"name":        name,
			"description": "d",
			"inputSchema": map[string]any{"type": "object"},
		}
	}
	missing := cannedServer(map[string]any{
		"tools/list": map[string]any{"tools": []any{
			tool("list_components"), tool("build"), tool("run_tests"), tool("lint"),
			// affected_components missing
		}},
	})
	defer missing.Close()
	err := checkToolsList(context.Background(), missing.URL)
	if err == nil || !strings.Contains(err.Error(), "affected_components") {
		t.Fatalf("expected missing-tool error, got %v", err)
	}
}

func TestCheckListComponentsValidatesFields(t *testing.T) {
	badKind := cannedServer(map[string]any{
		"tools/call": toolText(`{"components":[{"id":"a","description":"d","paths":["a/"],"kind":"banana"}]}`),
	})
	defer badKind.Close()
	err := checkListComponents(context.Background(), badKind.URL)
	if err == nil || !strings.Contains(err.Error(), "invalid kind") {
		t.Fatalf("expected invalid-kind error, got %v", err)
	}
}

func TestCheckFailingLintDemandsFailedStatus(t *testing.T) {
	okServer := cannedServer(map[string]any{
		"tools/call": toolText(`{"status":"ok","summary":"clean","duration_seconds":0.1}`),
	})
	defer okServer.Close()
	err := checkFailingLint(context.Background(), okServer.URL, "app")
	if err == nil || !strings.Contains(err.Error(), `want failed`) {
		t.Fatalf("expected want-failed error, got %v", err)
	}

	failedServer := cannedServer(map[string]any{
		"tools/call": toolText(`{"status":"failed","summary":"1 finding","duration_seconds":0.1}`),
	})
	defer failedServer.Close()
	if err := checkFailingLint(context.Background(), failedServer.URL, "app"); err != nil {
		t.Fatalf("conforming server rejected: %v", err)
	}
}

func TestCheckUnknownComponentDemandsRPCError(t *testing.T) {
	// A server that answers unknown components with a payload instead of a
	// JSON-RPC error violates the taxonomy.
	lenient := cannedServer(map[string]any{
		"tools/call": toolText(`{"status":"error","summary":"unknown","duration_seconds":0}`),
	})
	defer lenient.Close()
	if err := checkUnknownComponent(context.Background(), lenient.URL); err == nil {
		t.Fatal("expected an error for a server answering unknown components with a payload")
	}
}
```

- [ ] Run to verify failure: `go test ./agent/devmcp/contracttest/ -count=1` — compile failure (`undefined: checkInitialize` etc.).
- [ ] Write `agent/devmcp/contracttest/rpc.go`:

```go
// Package contracttest is the dev-mcp contract-test kit
// (00-shared-contracts.md §3.1): any repo's dev-mcp implementation runs
// Run/RunWithOptions against its live endpoint in its own CI.
package contracttest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// rawCall POSTs a single JSON-RPC request with Accept: application/json
// (the buffered, non-SSE path) and decodes the response envelope.
func rawCall(ctx context.Context, endpoint, method string, params any) (rpcEnvelope, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return rpcEnvelope{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return rpcEnvelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return rpcEnvelope{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rpcEnvelope{}, fmt.Errorf("%s: unexpected HTTP status %d", method, resp.StatusCode)
	}
	var env rpcEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return rpcEnvelope{}, fmt.Errorf("decode %s response: %w", method, err)
	}
	return env, nil
}
```

- [ ] Write `agent/devmcp/contracttest/kit.go`:

```go
package contracttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/devmcp"
)

// Options selects the optional execution checks. All fields are opt-in;
// zero Options runs only the universal protocol/schema/taxonomy checks.
type Options struct {
	// ExerciseComponent names a component whose build and run_tests are
	// executed and expected to return status "ok".
	ExerciseComponent string
	// FailingLintComponent names a component whose lint is expected to
	// return status "failed" (ran, found problems — never "error").
	FailingLintComponent string
	// SlowTestComponent names a component whose run_tests runs long enough
	// that at least one notifications/progress frame must be observed
	// (pair with a short DEV_MCP_PROGRESS_INTERVAL on the server).
	SlowTestComponent string
	// AffectedPath + ExpectAffected assert the affected_components mapping
	// for one known path.
	AffectedPath   string
	ExpectAffected []string
	// Timeout bounds each individual check (default 5m).
	Timeout time.Duration
}

// Run executes the universal contract checks against endpoint
// (e.g. "http://127.0.0.1:7780/mcp").
func Run(t *testing.T, endpoint string) {
	RunWithOptions(t, endpoint, Options{})
}

// RunWithOptions executes the universal checks plus any opted-in
// execution checks.
func RunWithOptions(t *testing.T, endpoint string, opts Options) {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}

	run := func(name string, fn func(ctx context.Context) error) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
			defer cancel()
			if err := fn(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}

	run("initialize", func(ctx context.Context) error { return checkInitialize(ctx, endpoint) })
	run("tools_list", func(ctx context.Context) error { return checkToolsList(ctx, endpoint) })
	run("list_components", func(ctx context.Context) error { return checkListComponents(ctx, endpoint) })
	run("unknown_tool_is_rpc_error", func(ctx context.Context) error { return checkUnknownTool(ctx, endpoint) })
	run("unknown_component_is_rpc_error", func(ctx context.Context) error { return checkUnknownComponent(ctx, endpoint) })
	run("missing_changed_paths_is_rpc_error", func(ctx context.Context) error { return checkMissingChangedPaths(ctx, endpoint) })
	run("affected_components_empty_input", func(ctx context.Context) error { return checkAffectedEmpty(ctx, endpoint) })

	if opts.ExerciseComponent != "" {
		run("exercise_ok_component", func(ctx context.Context) error {
			return checkExerciseOK(ctx, endpoint, opts.ExerciseComponent)
		})
	}
	if opts.FailingLintComponent != "" {
		run("failing_lint_taxonomy", func(ctx context.Context) error {
			return checkFailingLint(ctx, endpoint, opts.FailingLintComponent)
		})
	}
	if opts.SlowTestComponent != "" {
		run("progress_emission", func(ctx context.Context) error {
			return checkProgress(ctx, endpoint, opts.SlowTestComponent)
		})
	}
	if opts.AffectedPath != "" {
		run("affected_components_mapping", func(ctx context.Context) error {
			return checkAffectedMapping(ctx, endpoint, opts.AffectedPath, opts.ExpectAffected)
		})
	}
}

func checkInitialize(ctx context.Context, endpoint string) error {
	env, err := rawCall(ctx, endpoint, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "contracttest", "version": "1"},
	})
	if err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("initialize returned error: %+v", env.Error)
	}
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct{} `json:"tools"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil {
		return fmt.Errorf("decode initialize result: %w", err)
	}
	if res.ProtocolVersion == "" {
		return fmt.Errorf("initialize result missing protocolVersion")
	}
	if res.Capabilities.Tools == nil {
		return fmt.Errorf("server does not advertise the tools capability")
	}
	return nil
}

var requiredTools = []string{"affected_components", "build", "lint", "list_components", "run_tests"}

func checkToolsList(ctx context.Context, endpoint string) error {
	env, err := rawCall(ctx, endpoint, "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("tools/list returned error: %+v", env.Error)
	}
	var res struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil {
		return fmt.Errorf("decode tools/list result: %w", err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			return fmt.Errorf("tool %s has no description", tool.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return fmt.Errorf("tool %s inputSchema is not a JSON object: %w", tool.Name, err)
		}
		if schema["type"] != "object" {
			return fmt.Errorf("tool %s inputSchema type is %v, want object", tool.Name, schema["type"])
		}
	}
	for _, want := range requiredTools {
		if !slices.Contains(names, want) {
			return fmt.Errorf("missing required tool %s (got %v)", want, names)
		}
	}
	return nil
}

var validKinds = map[string]bool{
	"service": true, "library": true, "cli": true,
	"web": true, "docs": true, "other": true,
}

func checkListComponents(ctx context.Context, endpoint string) error {
	comps, err := devmcp.NewClient(endpoint).ListComponents(ctx)
	if err != nil {
		return err
	}
	if len(comps) == 0 {
		return fmt.Errorf("list_components returned no components")
	}
	seen := map[string]bool{}
	for _, comp := range comps {
		if comp.ID == "" {
			return fmt.Errorf("component with empty id")
		}
		if seen[comp.ID] {
			return fmt.Errorf("duplicate component id %q", comp.ID)
		}
		seen[comp.ID] = true
		if comp.Description == "" {
			return fmt.Errorf("component %s: empty description", comp.ID)
		}
		if len(comp.Paths) == 0 {
			return fmt.Errorf("component %s: empty paths", comp.ID)
		}
		if !validKinds[comp.Kind] {
			return fmt.Errorf("component %s: invalid kind %q", comp.ID, comp.Kind)
		}
	}
	return nil
}

func checkUnknownTool(ctx context.Context, endpoint string) error {
	env, err := rawCall(ctx, endpoint, "tools/call", map[string]any{
		"name": "no_such_tool_xyz", "arguments": map[string]any{},
	})
	if err != nil {
		return err
	}
	if env.Error == nil {
		return fmt.Errorf("calling an unknown tool must return a JSON-RPC error (MCP errors are for malformed input)")
	}
	return nil
}

func checkUnknownComponent(ctx context.Context, endpoint string) error {
	_, err := devmcp.NewClient(endpoint).Build(ctx, "no-such-component-xyz")
	var rpcErr *devmcp.RPCError
	if !errors.As(err, &rpcErr) {
		return fmt.Errorf("build of an unknown component must fail with a JSON-RPC error, got %v", err)
	}
	return nil
}

func checkMissingChangedPaths(ctx context.Context, endpoint string) error {
	env, err := rawCall(ctx, endpoint, "tools/call", map[string]any{
		"name": "affected_components", "arguments": map[string]any{},
	})
	if err != nil {
		return err
	}
	if env.Error == nil {
		return fmt.Errorf("affected_components without changed_paths must return a JSON-RPC error")
	}
	return nil
}

func checkAffectedEmpty(ctx context.Context, endpoint string) error {
	ids, err := devmcp.NewClient(endpoint).AffectedComponents(ctx, []string{})
	if err != nil {
		return err
	}
	if len(ids) != 0 {
		return fmt.Errorf("empty changed_paths must map to no components, got %v", ids)
	}
	return nil
}

func validateResult(res *devmcp.ToolResult) error {
	switch res.Status {
	case devmcp.StatusOK, devmcp.StatusFailed, devmcp.StatusError:
	default:
		return fmt.Errorf("status %q outside ok|failed|error", res.Status)
	}
	if res.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if res.DurationSeconds < 0 {
		return fmt.Errorf("duration_seconds must be >= 0")
	}
	return nil
}

func checkExerciseOK(ctx context.Context, endpoint, component string) error {
	client := devmcp.NewClient(endpoint)
	calls := []struct {
		name string
		fn   func() (*devmcp.ToolResult, error)
	}{
		{"build", func() (*devmcp.ToolResult, error) { return client.Build(ctx, component) }},
		{"run_tests", func() (*devmcp.ToolResult, error) { return client.RunTests(ctx, component, "") }},
	}
	for _, call := range calls {
		res, err := call.fn()
		if err != nil {
			return fmt.Errorf("%s(%s): %w", call.name, component, err)
		}
		if err := validateResult(res); err != nil {
			return fmt.Errorf("%s(%s): %w", call.name, component, err)
		}
		if res.Status != devmcp.StatusOK {
			return fmt.Errorf("%s(%s): status %q, want ok (summary: %s)", call.name, component, res.Status, res.Summary)
		}
	}
	return nil
}

func checkFailingLint(ctx context.Context, endpoint, component string) error {
	res, err := devmcp.NewClient(endpoint).Lint(ctx, component)
	if err != nil {
		return fmt.Errorf("lint(%s): %w", component, err)
	}
	if err := validateResult(res); err != nil {
		return fmt.Errorf("lint(%s): %w", component, err)
	}
	if res.Status != devmcp.StatusFailed {
		return fmt.Errorf("lint(%s): status %q, want failed — findings are failed, never error", component, res.Status)
	}
	return nil
}

func checkProgress(ctx context.Context, endpoint, component string) error {
	var (
		mu    sync.Mutex
		count int
	)
	client := devmcp.NewClient(endpoint, devmcp.WithProgress(func(_, _ string) {
		mu.Lock()
		count++
		mu.Unlock()
	}))
	res, err := client.RunTests(ctx, component, "")
	if err != nil {
		return fmt.Errorf("run_tests(%s): %w", component, err)
	}
	if res.Status != devmcp.StatusOK {
		return fmt.Errorf("run_tests(%s): status %q, want ok", component, res.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if count == 0 {
		return fmt.Errorf("no notifications/progress observed during a long run — the contract requires progress at least every 30s")
	}
	return nil
}

func checkAffectedMapping(ctx context.Context, endpoint, path string, expect []string) error {
	ids, err := devmcp.NewClient(endpoint).AffectedComponents(ctx, []string{path})
	if err != nil {
		return err
	}
	got := append([]string{}, ids...)
	want := append([]string{}, expect...)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		return fmt.Errorf("affected_components(%q) = %v, want %v", path, got, want)
	}
	return nil
}
```

- [ ] Run to verify pass: `go test ./agent/devmcp/contracttest/ -count=1` — all green.
- [ ] Commit: `git add agent/devmcp/contracttest && git commit -m "feat(devmcp): contract-test kit with universal and opt-in execution checks" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 11: Fixture repo + e2e suite + `test-dev-mcp` make target

The fixture repo is the layout-agnosticism guard from the charter: a single-component, shell-script-only "repo" with zero Go/Concourse shape, served by the same generic binary via its own `dev-mcp.yml`. The e2e suite builds the real `ci-agent/cmd/dev-mcp` binary and runs the kit against (a) the fixture with all execution checks and (b) this repo's `dev-mcp.yml` with universal checks — plus the Go-client end-to-end path harvest will use, and an env-gated live-image test for the CI job.

**Files:**
- Create: `agent/devmcp/contracttest/testdata/fixture-repo/dev-mcp.yml`, `agent/devmcp/contracttest/testdata/fixture-repo/scripts/build.sh`, `agent/devmcp/contracttest/testdata/fixture-repo/scripts/test.sh`, `agent/devmcp/contracttest/testdata/fixture-repo/scripts/lint.sh`, `agent/devmcp/contracttest/testdata/fixture-repo/src/app.sh`, `agent/devmcp/e2e/e2e_test.go`
- Modify: `Makefile:1` (.PHONY line), `Makefile:56` (`test-quick` deps)

**Steps:**

- [ ] Create the fixture repo. `agent/devmcp/contracttest/testdata/fixture-repo/dev-mcp.yml`:

```yaml
# Minimal single-component fixture repo: proves the dev-mcp contract is
# layout- and language-agnostic (no Go, no Makefile, no Concourse shape).
schema_version: 1
repo:
  build: { cmd: ["sh", "scripts/build.sh"] }
  test:  { cmd: ["sh", "scripts/test.sh"], focus_flag: "--focus" }
  lint:  { cmd: ["sh", "scripts/lint.sh"] }
components:
  - id: app
    description: single shell-script application
    paths: ["src/"]
    kind: cli
    build: { cmd: ["sh", "scripts/build.sh"] }
    test:  { cmd: ["sh", "scripts/test.sh"], focus_flag: "--focus" }
    lint:  { cmd: ["sh", "scripts/lint.sh"] }
```

`scripts/build.sh`:

```sh
#!/bin/sh
echo "building app"
exit 0
```

`scripts/test.sh`:

```sh
#!/bin/sh
# Emits output slowly enough that a server running with a short
# DEV_MCP_PROGRESS_INTERVAL must emit at least one progress notification.
for i in 1 2 3 4 5 6 7 8 9 10; do
  echo "running case $i"
  sleep 0.1
done
echo "10 cases passed"
exit 0
```

`scripts/lint.sh`:

```sh
#!/bin/sh
# Deterministic lint findings: "ran and found problems" = status failed.
echo "src/app.sh:1: warning: found a lint problem"
exit 1
```

`src/app.sh`:

```sh
#!/bin/sh
echo hello
```

- [ ] Write the failing e2e test `agent/devmcp/e2e/e2e_test.go` (plain `testing` — external repos consume the kit exactly this way):

```go
// Package e2e builds the real ci-agent/cmd/dev-mcp binary and proves the
// whole stack: binary + config against the contract-test kit, and the Go
// client path (the exact call path harvest-step will use).
//
// NOTE: this package has no Ginkgo suite, so `ginkgo -r` (make test-unit)
// skips it — run it via `make test-dev-mcp` or `go test ./agent/devmcp/...`.
package e2e_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/devmcp"
	"github.com/concourse/concourse/agent/devmcp/contracttest"
)

var devMCPBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "devmcp-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	devMCPBin = filepath.Join(dir, "dev-mcp")
	build := exec.Command("go", "build", "-o", devMCPBin, "./cmd/dev-mcp")
	build.Dir = filepath.Join(mustRepoRoot(), "ci-agent")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building dev-mcp: %s\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func mustRepoRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	// agent/devmcp/e2e/e2e_test.go → repo root is four dirs up from the file
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// copyFixture copies the fixture repo into a temp dir so command logs
// (.dev-mcp/logs) never pollute testdata.
func copyFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join(mustRepoRoot(), "agent", "devmcp", "contracttest", "testdata", "fixture-repo")
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func startDevMCP(t *testing.T, workdir, config, heartbeat string) string {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))

	cmd := exec.Command(devMCPBin, "--config", config, "--workdir", workdir)
	cmd.Env = append(os.Environ(),
		"MCP_LISTEN_ADDR="+addr,
		"DEV_MCP_PROGRESS_INTERVAL="+heartbeat,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})

	healthz := fmt.Sprintf("http://%s/healthz", addr)
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(healthz)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("dev-mcp at %s never became healthy: %v", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Sprintf("http://%s/mcp", addr)
}

func TestFixtureRepoContract(t *testing.T) {
	fixture := copyFixture(t)
	endpoint := startDevMCP(t, fixture, filepath.Join(fixture, "dev-mcp.yml"), "200ms")

	contracttest.RunWithOptions(t, endpoint, contracttest.Options{
		ExerciseComponent:    "app",
		FailingLintComponent: "app",
		SlowTestComponent:    "app",
		AffectedPath:         "src/app.sh",
		ExpectAffected:       []string{"app"},
		Timeout:              time.Minute,
	})
}

func TestConcourseRepoContract(t *testing.T) {
	root := mustRepoRoot()
	endpoint := startDevMCP(t, root, filepath.Join(root, "dev-mcp.yml"), "15s")

	// Universal checks only: exercising this repo's build/test takes minutes
	// and belongs to the theborg CI job (TestLiveImageContract below).
	contracttest.Run(t, endpoint)
}

// TestGoClientRunTestsEndToEnd drives run_tests(component) through the Go
// client with progress — the exact call path harvest-step will use.
func TestGoClientRunTestsEndToEnd(t *testing.T) {
	fixture := copyFixture(t)
	endpoint := startDevMCP(t, fixture, filepath.Join(fixture, "dev-mcp.yml"), "200ms")

	var mu sync.Mutex
	var progress []string
	client := devmcp.NewClient(endpoint, devmcp.WithProgress(func(tool, msg string) {
		mu.Lock()
		progress = append(progress, tool+": "+msg)
		mu.Unlock()
	}))

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res, err := client.RunTests(ctx, "app", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != devmcp.StatusOK {
		t.Fatalf("status %q, want ok (summary %q)", res.Status, res.Summary)
	}
	if res.DurationSeconds <= 0 {
		t.Fatalf("duration_seconds = %v, want > 0", res.DurationSeconds)
	}
	if res.LogPath == "" {
		t.Fatal("log_path is empty")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(progress) == 0 {
		t.Fatal("no progress notifications observed")
	}
}

// TestLiveImageContract exercises a running mcp-dev-concourse container.
// It is driven by the build-mcp-dev-image CI job (deploy/concourse-pipeline.yml)
// and skipped everywhere else.
func TestLiveImageContract(t *testing.T) {
	endpoint := os.Getenv("DEV_MCP_ENDPOINT")
	if endpoint == "" {
		t.Skip("DEV_MCP_ENDPOINT not set; this test runs in the build-mcp-dev-image CI job")
	}
	contracttest.RunWithOptions(t, endpoint, contracttest.Options{
		ExerciseComponent: "ci-agent",
		AffectedPath:      "atc/api/handler.go",
		ExpectAffected:    []string{"atc"},
		Timeout:           20 * time.Minute,
	})
}
```

- [ ] Run to verify current state: `go test ./agent/devmcp/e2e/ -count=1 -v -timeout 5m` — expect it to compile and PASS (all pieces landed in Tasks 2-9). If any check fails, the failure is a real contract violation in the server — fix the server (Tasks 4-7 files), not the kit.
- [ ] Add the make target. In `Makefile`, extend the `.PHONY` line (line 1) with `test-dev-mcp`, insert after the `test-ci-agent` recipe (line 16):

```make
# dev-mcp contract kit + e2e (plain go tests; ginkgo -r does not pick these up)
# Requires: nothing (builds ci-agent/cmd/dev-mcp on the fly)
test-dev-mcp:
	@echo "==> Running dev-mcp contract/e2e tests..."
	go test ./agent/devmcp/... -count=1 -timeout 10m
```

and change line 56 from `test-quick: test-unit test-ci-agent` to:

```make
test-quick: test-unit test-ci-agent test-dev-mcp
```

- [ ] Verify the target: `make test-dev-mcp` — expect the contracttest unit tests and the e2e suite to pass (TestLiveImageContract skipped).
- [ ] Commit: `git add agent/devmcp/contracttest/testdata agent/devmcp/e2e Makefile && git commit -m "test(devmcp): fixture repo, e2e contract suite, and test-dev-mcp make target" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 12: Sidecar image packaging — Dockerfile + convention doc

Owns 00-shared-contracts.md §8.5. The image bundles the toolchain this repo's `dev-mcp.yml` commands need (go, ginkgo, node/yarn); the image never hardcodes a workspace path — per the §8.5 CWD convention (co-signed dev-mcp + agent-step + harvest-step, 2026-07-09), the owning exec implementation sets the sidecar container's `WorkingDir` to the workspace artifact's mount path, and the binary's path-valued flags default relative to CWD (`--config dev-mcp.yml`, `--workdir .`; Task 8).

> **Amended 2026-07-09 (F21 — dev-mcp `/workspace` mismatch, runtime-seams delta Piece 4c):** jetbridge mounts the workspace at a hashed workdir path and nothing mounts `/workspace`, so a hardcoded `--config /workspace/dev-mcp.yml --workdir /workspace` ENTRYPOINT dies at start (RestartPolicyNever), gates get connection-refused, ticket errored. Fix: bare-binary ENTRYPOINT relying on the already-relative flag defaults; the owning exec (07/09 Task 12 step 7) sets the sidecar `WorkingDir`. The Args-append variant (appending flags after an exec-form ENTRYPOINT that already carries them) is FORBIDDEN — duplicate flags. `MCP_IMAGES.md` additionally records the CWD convention and scopes the non-root rule to MCP sidecar images only (main-step runner images `agent-runner`/`harvest-runner` run as root, per §8.5 as amended by runtime-seams Piece 5b).

**Files:**
- Create: `deploy/Dockerfile.mcp-dev-concourse`, `deploy/MCP_IMAGES.md`

**Steps:**

- [ ] Write `deploy/Dockerfile.mcp-dev-concourse`:

```dockerfile
# ghcr.io/tdmtrader/mcp-dev-concourse — this repo's dev-mcp sidecar image.
#
# Packaging convention (00-shared-contracts.md §8.5, owned by dev-mcp and
# reused by mcp-platform and mcp-gateway):
#   - static Go binary ENTRYPOINT (bare — no hardcoded paths) serving
#     streamable-HTTP MCP on MCP_LISTEN_ADDR (default :7780 for the dev role)
#   - GET /healthz for the pod readiness probe
#   - runs as non-root (MCP sidecar images only; main-step runner images
#     run as root — §8.5 scoping, 2026-07-09)
#   - version tag = the shipping repo's release tag (or short sha for
#     untagged builds); `latest` is never referenced by workflow definitions

FROM golang:1.25-bookworm AS builder
WORKDIR /src
COPY ci-agent/ .
RUN CGO_ENABLED=0 go build -o /dev-mcp ./cmd/dev-mcp

FROM golang:1.25-bookworm

# Toolchain required by this repo's dev-mcp.yml commands:
# ginkgo (atc/fly test targets; version pinned to ci-agent/go.mod),
# node + yarn via corepack (web component's package.json scripts).
RUN go install github.com/onsi/ginkgo/v2/ginkgo@v2.28.1
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    corepack enable && \
    rm -rf /var/lib/apt/lists/*

RUN useradd --uid 1000 --create-home devmcp
COPY --from=builder /dev-mcp /usr/local/bin/dev-mcp

USER devmcp
ENV MCP_LISTEN_ADDR=:7780
ENV GOPATH=/home/devmcp/go
EXPOSE 7780

# The owning exec sets this container's WorkingDir to the workspace
# artifact's mount path (shared-contracts §8.5 CWD convention); all
# path-valued flags default relative to CWD, so no path is hardcoded.
ENTRYPOINT ["/usr/local/bin/dev-mcp"]
```

- [ ] Write `deploy/MCP_IMAGES.md` — the copyable convention platform-mcp and gateway reuse:

```markdown
# MCP sidecar image packaging convention

Owner: dev-mcp workstream. Contract: 00-shared-contracts.md §8.5.
Consumers: platform-mcp-hitl (`mcp-platform`), gateway-mcp (`mcp-gateway`).

## Convention

| Aspect | Rule |
|---|---|
| Registry/name | `ghcr.io/tdmtrader/mcp-<name>` (`mcp-dev-concourse`, `mcp-platform`, `mcp-gateway`) |
| Version tag | the shipping repo's release tag when HEAD is tagged, else the short sha; `latest` is never pushed nor referenced (workflow-definition import validation rejects untagged/`latest` images) |
| Entrypoint | bare static Go binary (`ENTRYPOINT ["/usr/local/bin/<name>"]`, no flags, no hardcoded paths) serving streamable-HTTP MCP (`POST /mcp`) on `MCP_LISTEN_ADDR` (defaults: `:7780` dev, `:7781` platform, `:7782` gateway) |
| Workspace discovery | §8.5 CWD convention (co-signed dev-mcp, agent-step, harvest-step): images never hardcode a workspace path (no `/workspace`); every path-valued flag defaults relative to the process CWD (dev-mcp: `--config dev-mcp.yml`, `--workdir .`); the owning exec implementation sets the sidecar's `WorkingDir` to the workspace artifact's mount path inside the hashed build workdir (jetbridge falls back to the main container's Dir when no workspace artifact exists) |
| Health | `GET /healthz` → 200, used as the pod readiness probe |
| User | non-root (uid 1000) — **MCP sidecar images only**; main-step runner images (`agent-runner`, `harvest-runner`) run as **root** like every other step image, because jetbridge hostPath step volumes are kubelet-created root:root 0755 and fsGroup is ignored for hostPath (§8.5 scoping, 2026-07-09) |
| Gate | the image's contract-test kit MUST pass against the built image before push ("push on green") |

## CI job template

`deploy/concourse-pipeline.yml` job `build-mcp-dev-image` is the copyable
template: DinD pod → `docker build` → run the container against the repo
workspace → run the contract kit against it (`go test ./agent/devmcp/e2e/
-run TestLiveImageContract` with `DEV_MCP_ENDPOINT` set) → `docker push` on
green. New sidecar images copy that job, swap the Dockerfile and the
contract-kit invocation (platform-mcp and gateway ship their own kits per
the spec's testing approach).

## Known v1 limitations (mcp-dev-concourse)

- The image carries go + ginkgo + node/yarn but NO PostgreSQL: `run_tests`
  on the `atc`/`fly` components needs a reachable Postgres (CLAUDE.md), so
  the CI job exercises the self-contained `ci-agent` component instead.
  Full-suite gates against atc run where Postgres exists (harvest pods can
  mount one later; out of dev-mcp scope).
- `lint(web)` (`yarn run analyse`) requires `node_modules` installed in the
  workspace; run `yarn install` in the workspace first if you need it.
```

- [ ] Verify the Dockerfile references only files that exist: `ls ci-agent/cmd/dev-mcp/main.go deploy/Dockerfile.mcp-dev-concourse` — both present. (An actual `docker build` is not possible on this machine — Colima is usually down; the build is verified on theborg by Task 13's CI job.)
- [ ] Commit: `git add deploy/Dockerfile.mcp-dev-concourse deploy/MCP_IMAGES.md && git commit -m "feat(devmcp): mcp-dev-concourse Dockerfile and sidecar image packaging convention doc" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 13: CI job `build-mcp-dev-image` in the cicd pipeline

The §8.5 copyable template job: build the image in a DinD pod (the verified `tag-push-release` pattern at `deploy/concourse-pipeline.yml:520-676`), run the contract kit against the running container, push to GHCR on green.

> **Amended 2026-07-09 (F21 follow-through):** the smoke `docker run` gains `-w /workspace` — with the bare-binary ENTRYPOINT (Task 12 as amended) the container takes the workspace from CWD, so the CI job must set the working directory the way the owning exec sets the pod's sidecar `WorkingDir`. This also makes the smoke run a standing check that the image genuinely has no hardcoded workspace path: it only finds `dev-mcp.yml` because `-w` put it there.

**Files:**
- Modify: `deploy/concourse-pipeline.yml:677` (append job at end of file)

**Steps:**

- [ ] Append the job to `deploy/concourse-pipeline.yml`:

```yaml
- name: build-mcp-dev-image
  plan:
  - get: repo
    trigger: false
    params: {depth: 1}
  - task: build-test-push
    privileged: true
    config:
      platform: linux
      rootfs_uri: docker:///registry.home/concourse-test-runner:v5
      inputs:
      - name: repo
      params:
        GITHUB_TOKEN: ((github-token))
      run:
        path: sh
        args:
        - -exc
        - |
          cd repo
          SHORT_SHA=$(git rev-parse --short HEAD)
          TAG=$(git describe --tags --exact-match 2>/dev/null || echo "${SHORT_SHA}")
          cd ..
          IMAGE="ghcr.io/tdmtrader/mcp-dev-concourse"

          BUILDER_POD="mcp-image-build-$$"
          cleanup_builder() { kubectl delete pod -n cicd ${BUILDER_POD} --grace-period=0 --force 2>/dev/null || true; }
          trap cleanup_builder EXIT
          kubectl delete pod -n cicd -l app=mcp-image-build --grace-period=0 --force 2>/dev/null || true

          echo "=== Creating DinD pod ==="
          cat <<PODEOF | kubectl apply -n cicd -f -
          apiVersion: v1
          kind: Pod
          metadata:
            name: ${BUILDER_POD}
            namespace: cicd
            labels:
              app: mcp-image-build
          spec:
            containers:
            - name: dind
              image: docker:dind
              securityContext:
                privileged: true
              env:
              - name: DOCKER_TLS_CERTDIR
                value: ""
              command: ["dockerd", "--host=unix:///var/run/docker.sock"]
          PODEOF
          kubectl wait --for=condition=ready pod/${BUILDER_POD} -n cicd --timeout=120s
          for i in $(seq 1 30); do
            if kubectl exec -n cicd ${BUILDER_POD} -- docker info >/dev/null 2>&1; then break; fi
            sleep 2
          done

          echo "=== Copying repo into builder pod ==="
          kubectl cp repo cicd/${BUILDER_POD}:/build

          echo "=== Building ${IMAGE}:${TAG} ==="
          kubectl exec -n cicd ${BUILDER_POD} -- \
            docker build -f /build/deploy/Dockerfile.mcp-dev-concourse \
              -t ${IMAGE}:${TAG} /build

          echo "=== Starting the built image against the repo workspace ==="
          # -w /workspace emulates the exec-set WorkingDir (§8.5 CWD
          # convention): the bare-binary ENTRYPOINT takes the workspace
          # from CWD, so the smoke run must set it like the pod spec does.
          kubectl exec -n cicd ${BUILDER_POD} -- \
            docker run -d --name devmcp -v /build:/workspace -w /workspace ${IMAGE}:${TAG}
          kubectl exec -n cicd ${BUILDER_POD} -- sh -c '
            for i in $(seq 1 30); do
              if docker run --rm --network container:devmcp curlimages/curl:8.7.1 \
                   -fsS http://127.0.0.1:7780/healthz >/dev/null 2>&1; then exit 0; fi
              sleep 2
            done
            echo "dev-mcp container never became healthy"; docker logs devmcp; exit 1'

          echo "=== Running the contract-test kit against the image ==="
          kubectl exec -n cicd ${BUILDER_POD} -- \
            docker run --rm --network container:devmcp \
              -v /build:/build -w /build \
              -e DEV_MCP_ENDPOINT=http://127.0.0.1:7780/mcp \
              golang:1.25-bookworm \
              go test ./agent/devmcp/e2e/ -run TestLiveImageContract -v -count=1 -timeout 30m

          echo "=== Pushing on green ==="
          echo "${GITHUB_TOKEN}" | kubectl exec -i -n cicd ${BUILDER_POD} -- docker login ghcr.io -u tdmtrader --password-stdin
          kubectl exec -n cicd ${BUILDER_POD} -- docker push ${IMAGE}:${TAG}
          echo "=== Published ${IMAGE}:${TAG} ==="
```

- [ ] Validate the pipeline config locally: `go run ./cmd/concourse ... ` is overkill — use fly's validator against the live target instead: `fly -t theborg validate-pipeline -c deploy/concourse-pipeline.yml` — expect `looks good`. (If no fly target is configured in this session, run `go build ./fly && ./fly validate-pipeline -c deploy/concourse-pipeline.yml && rm -f fly` — `validate-pipeline` works offline.)
- [ ] Deploy and run once on theborg (requires the live `cicd` target; see memory note `reference_theborg_cicd_live_concourse.md` for login):

```bash
fly -t theborg set-pipeline -p concourse -c deploy/concourse-pipeline.yml -n
fly -t theborg trigger-job -j concourse/build-mcp-dev-image -w
```

Expect the job to build, pass `TestLiveImageContract`, and push `ghcr.io/tdmtrader/mcp-dev-concourse:<short-sha>`. If the kit fails inside the job, the image or config is genuinely broken — fix and re-trigger; do not push manually.

- [ ] Commit: `git add deploy/concourse-pipeline.yml && git commit -m "ci(devmcp): build-mcp-dev-image job — build, contract-test, push on green (packaging-convention template)" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

### Task 14: Full verification and workstream close-out

**Files:**
- (no new files; verification only)

**Steps:**

- [ ] Check PostgreSQL is up (`pg_isready`), then run the full relevant suites in order, per CLAUDE.md (never `--race`):

```bash
ginkgo ./agent/devmcp/                      # client + types (Ginkgo)
cd ci-agent && go test ./... -count=1 -timeout 5m && cd ..   # whole ci-agent module incl. devmcp
make test-dev-mcp                           # kit + e2e (plain go tests)
make test-unit                              # regression: nothing else broke
```

All must pass. `make test-unit` will not pick up the plain-test packages — that is what `test-dev-mcp` is for (wired into `test-quick`).

- [ ] Scope audit against the charter — verify each maps to landed code: interface finalization → Task 1 addendum + Tasks 2-7; contract-test kit → Task 10; Go client → Task 3 (plus `devmcpfakes` for harvest); reference implementation → Tasks 4-9; second fixture repo → Task 11; sidecar packaging convention + CI job → Tasks 12-13. Confirm nothing from scope_out was implemented (no pod-spec/sidecar-mount code, no gate-policy language, no platform/gateway tools).
- [ ] Confirm the working tree is clean (`git status`) and every commit message follows the plan.
- [ ] Final commit if any stragglers: `git add -A && git commit -m "chore(devmcp): close out dev-mcp workstream verification" -m "Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"`

---

## Execution notes

**Running this workstream's tests:**
- `ginkgo ./agent/devmcp/` — contract types + Go client (Ginkgo; included in `make test-unit`).
- `cd ci-agent && go test ./devmcp/... -count=1` — server, config, runner, tools, repo-config check (included in `make test-ci-agent`).
- `make test-dev-mcp` (new) — contract kit unit tests + the e2e suite (`go test ./agent/devmcp/... -count=1 -timeout 10m`). The e2e suite `go build`s `ci-agent/cmd/dev-mcp` in `TestMain` — first cold run needs the module cache populated (`cd ci-agent && go mod download` if offline errors appear).
- **Gotcha:** `make test-unit` runs `ginkgo -r`, which skips packages without Ginkgo suites — the contracttest and e2e packages are plain `testing` (deliberately, since external repos consume the kit via vanilla `go test`). They only run via `make test-dev-mcp` / `make test-quick`. Do not "fix" this by converting the kit to Ginkgo.

**Live/theborg requirements:**
- Task 13 needs the live theborg `cicd` target: `fly -t theborg login` per the memory note `reference_theborg_cicd_live_concourse.md`; the job needs the existing `((github-token))` secret (classic PAT with `write:packages` — see the GHCR-classic-PAT failure mode in `project_jetbridge_release_pipeline.md`).
- Docker/Colima is usually down on the dev machine — never attempt `docker build` locally; the image build is verified exclusively by the theborg job.
- The DinD job runs on a single-node cluster; pod-loss mid-run manifests as an errored (not failed) build — re-trigger before debugging (known failure mode).

**Rollback notes for the risky diffs:**
- `deploy/concourse-pipeline.yml` (Task 13) is the only change touching live CI. The job has `trigger: false` and shares no `serial_groups` with existing jobs, so it cannot affect `build-and-vet`/`unit-tests`/release jobs; rollback = `git revert` the commit and re-run `fly set-pipeline`. It never force-pushes git refs (unlike `tag-push-release`) — worst case is a stray `mcp-image-build-*` pod in `cicd` (cleaned by the trap; manually: `kubectl delete pod -n cicd -l app=mcp-image-build`).
- `Makefile` (Task 11) adds a target and extends `test-quick`; if `test-dev-mcp` proves flaky in some environment, drop it from the `test-quick` dependency list without deleting the target.
- Everything else is purely additive new packages/files (`agent/devmcp`, `ci-agent/devmcp`, `ci-agent/cmd/dev-mcp`, `dev-mcp.yml`, fixture testdata, Dockerfile, docs) — rollback is deleting them; no existing code paths import them in wave 1.
- The 00-shared-contracts.md addendum (Task 1) is append-only to the amendment log; consumers (harvest-step, agent-step) read it at their wave start — if a decision there must change after wave 1, append a superseding entry rather than editing in place.
