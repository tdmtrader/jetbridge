# Agent Execution Broker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:executing-plans` and execute this plan task by task with
> red-green-refactor checkpoints.

**Goal:** Ship synchronous `request_review` and `consult_agent` MCP tools,
backed by deterministic node-scoped profiles, a durable ATC execution ledger,
verified Git workspace capture, and Claude Code, Codex, and Cursor CLI
adapters.

**Architecture:** A managed broker-worker sidecar owns workspace capture,
native harness invocation, event normalization, and fixed-contract
validation. ATC owns admission, exact profile verification, lifecycle
persistence, result sealing, and inspection. Agent-facing selectors remain
provider agnostic; the frozen workflow revision resolves them exactly.

**Tech Stack:** Go, PostgreSQL, schema-v3 workflow compilation, MCP Streamable
HTTP, native harness JSONL protocols, Kubernetes/Helm, and the existing
agent-snapshot validator/sealer.

## Global constraints

- The design in
  `docs/superpowers/specs/2026-07-29-agent-execution-broker-design.md` is
  authoritative.
- Preserve schema-v3-only execution, team-owned snapshots, the existing
  primary `agent/provider.Adapter`, and the workflow-run reconciler's terminal
  authority.
- The new child ledger is separate from `agent_run_attempts`.
- Do not restore agent principals, generic privileged sidecars, gateway roles,
  or a provider/model selector in authored workflow YAML.
- Do not let a child inherit the parent transcript, user config, MCP servers,
  writable parent workspace, or authoritative validation tools.
- Run PostgreSQL suites serially because their fixed test-port ranges collide.
- Every task begins with a failing focused test, ends with focused passing
  tests, `gofmt`, and `git diff --check`, and is committed independently.

## Migration allocation

| Version | Purpose |
| --- | --- |
| `1773106155` | Durable child execution ledger, events, and immutable identity |

---

### Task 1: Add the fixed consultation record contract

**Files:**

- Create: `agent/snapshot/contracts/consultation.go`
- Create: `agent/snapshot/contracts/consultation_test.go`
- Create: `agent/snapshot/contracts/schemas/consultation.v1.rev1.json`
- Modify: `agent/snapshot/contracts/{registry,record_prototypes}.go`
- Modify schema registry/parity fixtures selected by failing tests.

**Steps:**

1. Add failing tests for required answer, unique claim IDs, optional valid
   evidence anchors, nonblank assumptions/uncertainties/recommendations,
   subject policy, declared-schema parity, and registry lookup.
2. Implement `ConsultationBody`, `ConsultationClaim`, semantic validation, and
   the composed seal/read validator.
3. Declare every wire leaf in revision 1 of the frozen schema and update
   registry/prototype lists.
4. Run
   `go test ./agent/snapshot/contracts -run 'Consultation|Registry|Schema' -count=1`.

### Task 2: Implement the neutral profile catalog and frozen resolution

**Files:**

- Create: `agent/broker/profile.go`
- Create: `agent/broker/profile_test.go`
- Create: `agent/workflow/broker_profiles.go`
- Create: `agent/workflow/broker_profiles_test.go`
- Modify: `agent/workflow/{function_config,parse,compile,hash,typecheck}.go`
- Modify exact schema-v3 golden fixtures selected by failing tests.

**Steps:**

1. Add failing domain tests for the exact tier/effort/tool enums, duplicate
   selectors, incomplete operator profiles, unpinned images, missing
   instruction digests, unsupported adapter capability, and deterministic
   lookup.
2. Implement strict deployment catalog parsing and immutable profile digests.
   Keep provider, model, harness, credential slot, and controls operator-only.
3. Add source-local node selectors and compiled-only exact broker profiles.
   Admission copies only referenced mappings and hashes all authority-bearing
   fields.
4. Reject provider/model/harness names in agent-authored selectors and reject
   calls outside the frozen node subset.
5. Run `go test ./agent/broker ./agent/workflow -run 'Broker|Profile' -count=1`.

### Task 3: Add the durable child execution ledger

**Files:**

- Create:
  `atc/db/migration/migrations/1773106155_create_agent_child_executions.{up,down}.sql`
- Create: `atc/db/migration/agent_child_executions_test.go`
- Create: `atc/db/agent_child_executions_factory.go`
- Create: `atc/db/agent_child_executions_factory_test.go`
- Modify: migration preflight/head regression files selected by failing tests.

**Steps:**

1. Add failing migration tests for fresh install, upgrade from 6148, all state
   checks, immutable identity, sequence uniqueness, team foreign keys,
   idempotency, result ownership, and rollback.
2. Add failing factory tests for create-or-read idempotency, input drift,
   cross-team denial, exact-profile drift, monotonic sequence updates, valid
   state transitions, lease expiry, terminal immutability, and retrieval.
3. Implement the migration and a transactionally fenced factory. Store prompt
   digests and attachment identities, never raw prompt text or credentials.
4. Add reconciliation that terminalizes expired nonterminal executions as
   `broker_lost`.
5. Run serially:
   `go test ./atc/db/migration -run 'ChildExecution|LegacyUpgrade' -count=1`
   and `go test ./atc/db -run ChildExecution -count=1`.

### Task 4: Implement exact dirty-workspace capture

**Files:**

- Create: `agent/broker/workspace/capture.go`
- Create: `agent/broker/workspace/capture_test.go`
- Create: `agent/broker/workspace/git.go`

**Steps:**

1. Add failing table tests for clean, unstaged, staged, deleted, renamed,
   executable, symlink, nonignored untracked, ignored, binary, and submodule
   states.
2. Add failing tests for caller-index preservation, base mismatch,
   size/entry limits, symlink non-traversal, mutation retry/failure, and
   reapplying the patch to the base to reproduce the result tree.
3. Implement argv-only Git execution with a temporary index and clean
   verification checkout. Never write `.git/index` or follow symlinks.
4. Emit a capture manifest containing base commit/tree, result tree, patch
   digest, excluded-file policy revision, and stable file observations.
5. Run `go test ./agent/broker/workspace -count=1`.

### Task 5: Implement the broker adapter contract and three native adapters

**Files:**

- Create: `agent/broker/adapter/{adapter,events,process,version}.go`
- Create: `agent/broker/adapter/*_test.go`
- Create: `agent/broker/adapter/{codex,claude,cursor}.go`
- Create: JSONL fixtures under `agent/broker/adapter/testdata/`.

**Steps:**

1. Add failing contract tests for exact argv, clean environment allowlists,
   credential injection/redaction, version preflight, cancellation and
   process-tree cleanup, terminal output selection, and unavailable usage.
2. Add fixture-driven event tests for each native stream, malformed/truncated
   JSONL, provider errors, and terminal-result conflicts.
3. Implement Codex invocation with ephemeral/ignored configuration,
   read-only/no-approval controls, JSONL, native effort, and output schema.
4. Implement Claude Code print/stream-JSON invocation with exact
   tool/permission/MCP controls, turn bound, model, and structured output when
   supported.
5. Implement Cursor print/stream-JSON invocation without `--force`, recording
   its weaker native enforcement as capabilities rather than claiming parity.
6. Run `go test ./agent/broker/adapter -count=1`.

### Task 6: Implement broker orchestration and strict terminal validation

**Files:**

- Create: `agent/broker/{broker,request,result,error,attachments}.go`
- Create: `agent/broker/*_test.go`
- Create: `agent/broker/output/{decode,review,consultation}.go`
- Create: `agent/broker/output/*_test.go`

**Steps:**

1. Add failing orchestration tests for admission-before-capture, exact profile
   verification, fresh context assembly, attachment allowlisting, concurrent
   calls, phase order, deadline/cancellation, and every stable error code.
2. Add failing output tests for exactly one JSON value, duplicate keys,
   unknown fields, size limits, invalid evidence subjects, review static-only
   provenance, and raw-prose rejection.
3. Implement `Broker` behind narrow `AuthorityClient`, `Capturer`, `Adapter`,
   `CredentialResolver`, and `Sealer` interfaces with no package-global
   catalog or credentials.
4. Retain normalized transcript events for the authority client while
   redacting environment values and provider error bodies.
5. Run `go test ./agent/broker/... -count=1` and `go test -race
   ./agent/broker/... -count=1`.

### Task 7: Expose the two synchronous MCP tools

**Files:**

- Create: `agent/broker/mcp/server.go`
- Create: `agent/broker/mcp/server_test.go`
- Create: `cmd/agent-broker/main.go`
- Create: `cmd/agent-broker/main_test.go`
- Reuse: `atc/api/mcpserver`.

**Steps:**

1. Add failing wire tests for only `request_review` and `consult_agent`, strict
   schemas, neutral catalog resource, synchronous terminal responses, child
   execution progress, concurrent calls, disconnect behavior, and safe errors.
2. Implement the loopback Streamable HTTP server with the existing heartbeat
   transport and immutable tool descriptions generated from the node catalog.
3. Add startup configuration for ATC authority endpoint, workspace mount,
   scratch root, adapter binaries, and credential slots; reject unknown
   configuration.
4. Run `go test ./agent/broker/mcp ./cmd/agent-broker -count=1`.

### Task 8: Add the ATC authority API and result sealing

**Files:**

- Create: `atc/api/agentchildexecutions/{handler,types,auth}.go`
- Create: `atc/api/agentchildexecutions/*_test.go`
- Modify: `atc/routes.go`, `atc/atccmd/command.go`
- Modify snapshot sealing assembly at the narrow seam identified by the
  failing integration test.

**Steps:**

1. Add failing route tests for execution-scoped authority, create/admit,
   monotonic event update, heartbeat lease, typed-result seal, terminal read,
   team isolation, exact-profile mismatch, and replay.
2. Implement short-lived execution authority derived from build, plan, node,
   team, and frozen profile identity. Do not reuse publisher or main-container
   credentials.
3. Validate and seal `review/v1` or `consultation/v1` with server-derived
   subjects and provenance, then atomically bind the snapshot before success.
4. Wire the factory, reconciler, and read-only inspection route into ATC.
5. Run `go test ./atc/api/agentchildexecutions ./atc -run
   'AgentChild|Broker' -count=1`.

### Task 9: Inject the trusted managed broker sidecar

**Files:**

- Create: `atc/runtime/managed_agent_broker.go`
- Create: `atc/runtime/managed_agent_broker_test.go`
- Modify: `atc/runtime/types.go`
- Modify: `atc/exec/agent_step.go`
- Modify: `atc/worker/jetbridge/` pod construction and security tests.

**Steps:**

1. Add failing tests proving the broker name/image/port/authority/mount layout
   is server-owned, image-digest pinned, inaccessible to workflow sidecars,
   and present only for nodes with frozen broker profiles.
2. Add broker-specific read-only workspace sharing, private scratch, exact
   authority projection, and sidecar-only secret refs. Do not weaken the
   existing main-only `PrivateFileMounts` or `SecretMounts` contracts.
3. Inject the loopback MCP URL into the parent agent only. Do not expose it to
   child processes materialized by the broker.
4. Add pod-security tests for read-only parent mount, non-root execution,
   dropped capabilities, no privilege escalation, seccomp, and bounded
   emptyDir.
5. Run `go test ./atc/runtime ./atc/exec ./atc/worker/jetbridge -run
   'ManagedAgentBroker|AgentBroker' -count=1`.

### Task 10: Package, document, and verify the vertical slice

**Files:**

- Create/modify broker Dockerfile and Make target following current deploy
  conventions.
- Modify Helm values/templates and chart tests for optional broker image,
  profiles, credential slots, resources, and network policy.
- Create: `docs/agent-execution-broker.md`
- Create: `docs/superpowers/plans/2026-07-29-agent-execution-broker-report.md`

**Steps:**

1. Add failing image-content and Helm-render tests before packaging changes.
2. Add environment-gated smoke tests using fake harness binaries for all three
   adapters and a real ATC/PostgreSQL ledger.
3. Document operator profile configuration, agent-visible semantics, exact
   resolution, static-review limitation, inspection, and troubleshooting.
4. Run all focused suites above, `go test ./agent/broker/... -race -count=1`,
   affected ATC/database/workflow suites serially, `git diff --check`, and the
   repository's broker image/chart gates.
5. Record exact commands/results, deferred live-provider/Kubernetes gates,
   residue searches, and remaining risks in the implementation report.
