# Jetbridge First-User Blocker Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the reusable-node lifecycle complete successfully from typed repository input creation through a budget-capped run and independently sealed typed output.

**Architecture:** Repair the two repository ingress paths first, using immutable capture identity and closed public validation reasons. Then make output-builder readiness a real MCP protocol check, make the runner's Claude flags an image-level capability contract, expose safe build correlations in Fly, and prove the assembled node-level experience with fresh post-rollout runs of the exact unreleased versions returned by content deduplication on the live target.

**Tech Stack:** Go 1.25, PostgreSQL, Concourse ATC/Fly, JSON-RPC 2.0 and MCP `2024-11-05`, Docker/OCI registries, Claude Code `2.1.212`, Markdown/HTML documentation, and the `home` Jetbridge target.

## Global Constraints

- Work only in `.worktrees/agentic-platform-rebase` on `codex/agentic-platform-rebase`; do not modify the main checkout or the old foundations worktree.
- Read `docs/superpowers/specs/2026-08-01-jetbridge-first-user-blocker-remediation-design.md` before every task. Its trust boundaries and exact public messages are authoritative.
- Require prerequisite commits `a24e0771c2` and `021b17d51e`; verify their behavior and do not duplicate their archive, prompt-fallback, or node-package changes.
- Preserve schema-v3-only execution, snapshot-keyed authority, the final sealer as the sole output authority, and the workflow-run reconciler as the sole terminalizer.
- Never omit `--max-budget-usd` for a positive slice, expose an arbitrary dependency error, accept a mutable runtime tag, or release a node version without a fresh post-rollout run and inspected valid output.
- Do not add workflow composition, new snapshot types, automatic skill invocation, or node-reasoning rubric changes.
- Use one Terra implementer for each shared task and a separate blocking reviewer. Do not run two implementers against the same files.
- Run PostgreSQL-backed suites serially; they share the fixed 5434–5442 test range.
- Start each task from the smallest existing behavioral failure that proves the defect. Add a test only for load-bearing behavior that is not already covered; when a new test is necessary, observe it fail for the actual defect before implementation. Never add a duplicate or artificial test solely to manufacture another red signal. Use focused tests before broad tests, end with `gofmt` where Go changed and `git diff --check`, and record evidence in `.superpowers/sdd/2026-08-01-jetbridge-first-user-blocker-remediation/progress.md`.
- Limit every task to three blocking review rounds. Record an unresolved third-round blocker rather than broadening scope or cycling indefinitely.
- Do not push, open a pull request, merge, change external home-infra, or release a node as an incidental implementation action.

---

### Task 1: Repair exact resource-capture template identity

**Files:**

- Modify: `agent/resourcecapture/capture.go`
- Modify: `agent/resourcecapture/capture_test.go`
- Modify: `agent/resourcecapture/persisted_selection_test.go`
- Modify: `agent/resourcecapture/template_store.go`
- Modify: `agent/resourcecapture/template_store_test.go`
- Modify: `agent/api/snapshots/resource_handler.go`
- Modify: `agent/api/snapshots/resource_handler_test.go`
- Modify: `atc/db/agent_snapshots_factory.go`
- Modify: `atc/db/agent_snapshots_factory_test.go`
- Modify: `atc/db/agent_resource_capture_outputs.go`

**Interfaces:**

- Consumes: `workflow.TargetConfigHash(atc.Config) (string, error)` and `workflowrun.TemplateSaver.SaveOrReuse`, whose template name invariant is `-<workflowrun.ImmutableTemplateSpec.FullHash[:12]>`.
- Produces: capture template names with the exact grammar `agent-resource-capture-<24 lowercase operation hex>-<12 lowercase target-config hex>`.
- Applies the same shared template-spec constructor to both `Capturer.Capture` and `Capturer.CapturePersistedSelection`; the latter retains its server-derived identity and drift checks.
- Produces: `resourcecapture.ErrTemplateConflict`, a stable domain category mapped to HTTP 409; `workflowrun.ErrPlatformFailure` remains hidden and maps through `resourcecapture.ErrUnavailable` to HTTP 503.
- Preserves: `TemplateSpec.FullHash` as the raw canonical JSON SHA-256 checked by `ImmutableTemplateStore`; it does not become the domain-separated target-config hash.

- [ ] **Step 1: Pin both constructor failures with load-bearing unit tests**

  Change the successful-capture assertion in `capture_test.go` to compute the
  target hash from the exact config handed to the fake store:

  ```go
  targetHash, err := workflow.TargetConfigHash(spec.Config)
  if err != nil {
      t.Fatal(err)
  }
  wantName := "agent-resource-capture-" + result.OperationKey[:24] + "-" + targetHash[:12]
  if spec.Name != wantName {
      t.Fatalf("template name = %q, want %q", spec.Name, wantName)
  }
  ```

  Add assertions that changing the task image or resolved source changes both
  the target hash suffix and the operation identity, while retrying the same
  request produces the same full name.

  In `persisted_selection_test.go`, add a successful persisted-selection case
  beside the existing substituted-identity rejection. Compute the expected
  suffix from the exact `TemplateSpec.Config` received by the fake store,
  assert the full name uses `selection.CaptureOperationKey[:24]`, and repeat
  the identical selection to prove the same spec name and execution identity
  are reused. Preserve the assertion that drift is rejected before any
  template save.

- [ ] **Step 2: Run the resource-capture test and observe the old-name failure**

  Run:

  ```bash
  go test ./agent/resourcecapture -run 'TestCapture|TestCapturePersistedSelection' -count=1
  ```

  Expected: both successful constructor cases fail because their actual names
  lack the final hyphen and 12-character target hash.

- [ ] **Step 3: Construct both immutable names from the rendered config hash**

  In `capture.go`, extract one small template-spec constructor that computes the
  domain-separated hash after canonicalization and keeps the existing raw
  digest separately:

  ```go
  func newTemplateSpec(resolved ResolvedResource, operationKey string, config atc.Config, canonical []byte) (TemplateSpec, error) {
      targetHash, err := workflow.TargetConfigHash(config)
      if err != nil {
          return TemplateSpec{}, fmt.Errorf("resource capture: hash template config: %w", err)
      }
      configDigest := sha256.Sum256(canonical)
      return TemplateSpec{
          TeamID: resolved.TeamID, TeamName: resolved.TeamName,
          Name: templateNamePrefix + operationKey[:24] + "-" + targetHash[:12],
          OperationKey: operationKey,
          FullHash: hex.EncodeToString(configDigest[:]),
          CanonicalJSON: canonical,
          Config: config,
      }, nil
  }
  ```

  Call this constructor from both `Capture` and `CapturePersistedSelection`,
  passing their already verified operation key. Import `agent/workflow`; do not
  duplicate the target-hash algorithm locally or weaken persisted-selection
  compatibility/idempotency checks.

- [ ] **Step 4: Add a real TemplateSaver compatibility regression**

  Extend `template_store_test.go` with fakes satisfying
  `workflowrun.TemplateTeamFinder` and `WorkflowRunTemplateStore`, construct a
  real `workflowrun.TemplateSaver`, and pass it to `NewTemplateStore`. Assert
  that the capture-generated spec is accepted and that deleting or altering
  the 12-character suffix returns `workflowrun.ErrImmutableTemplateCollision`.

  The central assertion is:

  ```go
  core, err := workflowrun.NewTemplateSaver(teamFinder, templateBackend)
  if err != nil {
      t.Fatal(err)
  }
  store, err := resourcecapture.NewTemplateStore(core)
  if err != nil {
      t.Fatal(err)
  }
  if _, err := store.SaveOrReuse(context.Background(), capturedSpec); err != nil {
      t.Fatalf("real TemplateSaver rejected capture spec: %v", err)
  }
  ```

- [ ] **Step 5: Pin both DB authorization predicates to the new grammar**

  Update the `FindResourceCaptureOutput` and
  `ListPendingResourceCaptureOutputs` fixtures in
  `agent_snapshots_factory_test.go` to use a name ending in 12 lowercase hex
  characters. Add negative rows for a missing suffix, uppercase suffix,
  11-character suffix, and correct prefix with the wrong operation fragment.
  Each negative row must remain undiscoverable.

  Run the focused DB tests serially:

  ```bash
  ginkgo --procs=1 --focus='exact authorized resource-capture output' ./atc/db
  ```

  Expected before the SQL change: the valid suffixed template is not returned.

- [ ] **Step 6: Change the output and finalizer SQL together**

  Replace the old equality predicates with an anchored grammar while retaining
  every ownership, run, production, type, and metadata predicate:

  ```sql
  AND template.name ~ (
    '^agent-resource-capture-' || left($3, 24) || '-[0-9a-f]{12}$'
  )
  ```

  In `ListPendingResourceCaptureOutputs`, use the server-authored operation key
  expression in place of `$3`:

  ```sql
  AND template.name ~ (
    '^agent-resource-capture-' ||
    left(production.source_metadata ->> 'operation_key', 24) ||
    '-[0-9a-f]{12}$'
  )
  ```

  Do not remove the exact full operation-key comparison from
  `FindResourceCaptureOutput` or the exact metadata regex from the finalizer.

- [ ] **Step 7: Classify and log capture construction failures**

  Add this domain sentinel in `capture.go`:

  ```go
  ErrTemplateConflict = errors.New("resource capture immutable template conflicts with existing state")
  ```

  In `ImmutableTemplateStore.SaveOrReuse`, translate only known saver classes:

  ```go
  if errors.Is(err, workflowrun.ErrImmutableTemplateCollision) {
      return TemplateRef{}, fmt.Errorf("%w: %v", ErrTemplateConflict, err)
  }
  if errors.Is(err, workflowrun.ErrPlatformFailure) {
      return TemplateRef{}, fmt.Errorf("%w: immutable template store: %v", ErrUnavailable, err)
  }
  ```

  Convert `writeResourceCaptureError` into a `HandlerFactory` method, call
  `factory.logger.Error("resource-capture-request-failed", err)`, map
  `ErrTemplateConflict` to `409 conflict` with message `resource capture
  conflicts with immutable state`, and retain bounded fixed messages for every
  other class. Add a test with `database secret /tmp/private` proving the log
  receives the cause and the response contains neither `secret` nor `/tmp`.

- [ ] **Step 8: Run the complete Task 1 verification**

  Run, with the DB package last and serial:

  ```bash
  gofmt -w agent/resourcecapture/capture.go agent/resourcecapture/capture_test.go agent/resourcecapture/persisted_selection_test.go agent/resourcecapture/template_store.go agent/resourcecapture/template_store_test.go agent/api/snapshots/resource_handler.go agent/api/snapshots/resource_handler_test.go atc/db/agent_snapshots_factory.go atc/db/agent_snapshots_factory_test.go atc/db/agent_resource_capture_outputs.go
  go test ./agent/resourcecapture ./agent/workflowrun ./agent/api/snapshots -count=1
  ginkgo --procs=1 --focus='exact authorized resource-capture output' ./atc/db
  git diff --check
  ```

  Expected: all commands pass; no test accepts the legacy unsuffixed name.

- [ ] **Step 9: Record, review, and commit Task 1**

  Add the test commands and review disposition to the track ledger. After a
  blocking review accepts the slice, commit only the listed files and ledger:

  ```bash
  git add agent/resourcecapture/capture.go agent/resourcecapture/capture_test.go agent/resourcecapture/persisted_selection_test.go agent/resourcecapture/template_store.go agent/resourcecapture/template_store_test.go agent/api/snapshots/resource_handler.go agent/api/snapshots/resource_handler_test.go atc/db/agent_snapshots_factory.go atc/db/agent_snapshots_factory_test.go atc/db/agent_resource_capture_outputs.go .superpowers/sdd/2026-08-01-jetbridge-first-user-blocker-remediation/progress.md
  git commit -m "fix(agent): repair resource capture template identity"
  ```

### Task 2: Add safe repository validation diagnostics and a full upload contract

**Files:**

- Modify: `agent/snapshot/validator.go`
- Modify: `agent/snapshot/validator_test.go`
- Modify: `agent/snapshot/contracts/repository.go`
- Modify: `agent/snapshot/contracts/repository_test.go`
- Modify: `agent/api/snapshots/types.go`
- Modify: `agent/api/snapshots/types_test.go`
- Modify: `agent/api/snapshots/handler.go`
- Modify: `agent/api/snapshots/handler_test.go`
- Modify: `fly/commands/agent_snapshots_test.go`

**Interfaces:**

- Produces: `snapshot.ValidationFailureReason`, the seven constants from the design, and `snapshot.NewPublicValidationFailure(reason, cause) error`.
- Produces: `(*snapshot.PublicValidationFailure).Reason() ValidationFailureReason` and `PublicMessage() string`; the raw cause is available only through `Error`/`Unwrap` for logs.
- Extends: `snapshotsapi.ErrorResponse` with a string field named `Reason`
  carrying the JSON tag `reason,omitempty`; existing responses remain
  wire-compatible when no safe reason exists.
- Consumes: the real Fly archiver, `snapshot.Canonicalizer`, `contracts.NewRegistry`, and the exact `repository/v1` validator.

- [ ] **Step 1: Write snapshot-domain tests for the closed reason set**

  Add a table test in `validator_test.go` that constructs all seven design
  reasons and asserts exact public messages. Add a negative test proving an
  unrecognized reason cannot manufacture public text:

  ```go
  cause := errors.New("git stderr contains /tmp/private and token=secret")
  err := snapshot.NewPublicValidationFailure(snapshot.RepositoryDirty, cause)
  var public *snapshot.PublicValidationFailure
  if !errors.As(err, &public) || public.Reason() != snapshot.RepositoryDirty {
      t.Fatalf("public validation failure = %#v, %v", public, err)
  }
  if public.PublicMessage() != "repository work tree and index must be clean" {
      t.Fatalf("public message = %q", public.PublicMessage())
  }
  if !errors.Is(err, cause) {
      t.Fatal("internal cause was not retained for logs")
  }
  ```

- [ ] **Step 2: Run the new domain test and observe missing types**

  Run:

  ```bash
  go test ./agent/snapshot -run 'TestPublicValidationFailure' -count=1
  ```

  Expected: compilation fails because the typed reason API does not exist.

- [ ] **Step 3: Implement the closed public-failure type**

  Add the type, constants, and a private reason-to-message switch in
  `validator.go`. The constructor must reject unknown reasons by returning an
  ordinary wrapped internal error, never a `PublicValidationFailure`:

  ```go
  type ValidationFailureReason string

  const (
      RepositoryMetadataMissing         ValidationFailureReason = "repository_metadata_missing"
      RepositoryMetadataUnsafe          ValidationFailureReason = "repository_metadata_unsafe"
      RepositoryHistoryIncomplete       ValidationFailureReason = "repository_history_incomplete"
      RepositoryObjectFormatUnsupported ValidationFailureReason = "repository_object_format_unsupported"
      RepositoryGitlinksUnsupported     ValidationFailureReason = "repository_gitlinks_unsupported"
      RepositoryDirty                   ValidationFailureReason = "repository_dirty"
      RepositoryInvalid                 ValidationFailureReason = "repository_invalid"
  )

  func NewPublicValidationFailure(reason ValidationFailureReason, cause error) error {
      message, found := publicValidationMessage(reason)
      if !found || cause == nil {
          return fmt.Errorf("snapshot: invalid public validation failure: %w", cause)
      }
      return &PublicValidationFailure{reason: reason, message: message, cause: cause}
  }
  ```

  `Error()` may include the cause for structured server logs; only
  `PublicMessage()` is allowed onto the wire.

- [ ] **Step 4: Pin repository failure classification at each semantic boundary**

  In `repository_test.go`, add exact cases for missing `.git`, unsafe config,
  shallow history, unsupported object format, gitlink, dirty tree, and broken
  object graph. For each, assert `errors.As` finds the expected public reason
  and that the internal error still explains the local failure. Add a malformed
  `os.Root`/context case that remains an ordinary non-public error.

  Run:

  ```bash
  go test ./agent/snapshot/contracts -run 'TestRepository.*PublicFailure' -count=1
  ```

  Expected before implementation: cases return ordinary errors with no public
  reason.

- [ ] **Step 5: Wrap only the allow-listed repository categories**

  Add a private helper in `repository.go`:

  ```go
  func publicRepositoryFailure(reason snapshot.ValidationFailureReason, err error) error {
      return snapshot.NewPublicValidationFailure(reason, err)
  }
  ```

  Wrap missing/non-directory `.git` as metadata missing or unsafe, config and
  containment rejection as metadata unsafe, shallow state as history
  incomplete, unsupported object format as its exact reason, gitlinks and dirty
  state as their exact reasons, and unresolved/broken Git objects as repository
  invalid. Leave cancellation, root misuse, and execution infrastructure
  failures non-public.

- [ ] **Step 6: Extend the bounded API envelope without changing generic errors**

  Add the optional field to `ErrorResponse` and update its wire-shape test so a
  generic response still marshals without `reason`, while a known failure
  marshals exactly one stable reason. In `writeSnapshotError`, put the
  `errors.As` branch before the generic `ErrValidation` branch:

  ```go
  var public *snapshot.PublicValidationFailure
  switch {
  case errors.As(err, &public):
      writeJSON(w, http.StatusUnprocessableEntity, ErrorResponse{
          Error: "validation_failed",
          Message: public.PublicMessage(),
          Reason: string(public.Reason()),
      })
  case errors.Is(err, snapshot.ErrValidation):
      writeError(w, http.StatusUnprocessableEntity, "validation_failed", "snapshot does not satisfy its declared type")
  // existing cases remain in their current order
  }
  ```

  Update `writeError` to omit `Reason` by default.

- [ ] **Step 7: Prove safe and unsafe handler behavior**

  Extend `handler_test.go` with one public repository failure wrapping a cause
  containing Git stderr, a config value, and a temp path. Assert status 422,
  exact reason/message, and absence of every cause fragment. Retain the current
  generic validation test and assert that it has no `reason` field.

  Run:

  ```bash
  go test ./agent/api/snapshots -run 'Test(CreateMaps|ErrorResponse|PublicValidation)' -count=1
  ```

  Expected: known reasons are actionable and unknown causes are still generic.

- [ ] **Step 8: Add the missing Fly-to-validator vertical regression**

  In `agent_snapshots_test.go`, create a clean nested Git worktree under
  `t.TempDir()`, archive it with `writeAgentSnapshotTarFromRoot`, canonicalize
  with a real `snapshot.Canonicalizer`, open the extracted root, resolve
  `repository/v1` from `contracts.NewRegistry`, and call `AdmitForSeal`:

  ```go
  var archive bytes.Buffer
  archiveAgentSnapshotDirectory(t, repositoryDir, &archive)
  tree, err := (snapshot.Canonicalizer{
      MaxEntries: snapshot.DefaultMaxSnapshotEntries,
      MaxContentBytes: snapshot.DefaultMaxSnapshotContentBytes,
      TempDir: t.TempDir(),
  }).Capture(context.Background(), bytes.NewReader(archive.Bytes()))
  if err != nil {
      t.Fatal(err)
  }
  defer tree.Close()
  root, err := tree.OpenRoot()
  if err != nil {
      t.Fatal(err)
  }
  defer root.Close()
  validationContext, err := snapshot.NewValidationContext(nil, nil)
  if err != nil {
      t.Fatal(err)
  }
  registry, err := contracts.NewRegistry()
  if err != nil {
      t.Fatal(err)
  }
  validator, err := registry.Lookup(snapshot.TypeRef("repository/v1"))
  if err != nil {
      t.Fatal(err)
  }
  result, err := validator.AdmitForSeal(context.Background(), root, validationContext)
  if err != nil {
      t.Fatalf("Fly archive rejected by repository/v1: %v", err)
  }
  if err := result.Validate(); err != nil {
      t.Fatal(err)
  }
  ```

  Use the existing canonicalizer result API and test helpers rather than adding
  a production extraction bypass. Include nested `.git/objects` directories so
  the test would fail with the pre-`a24e0771c2` trailing-separator archive.

- [ ] **Step 9: Run the complete Task 2 verification**

  Run:

  ```bash
  gofmt -w agent/snapshot/validator.go agent/snapshot/validator_test.go agent/snapshot/contracts/repository.go agent/snapshot/contracts/repository_test.go agent/api/snapshots/types.go agent/api/snapshots/types_test.go agent/api/snapshots/handler.go agent/api/snapshots/handler_test.go fly/commands/agent_snapshots_test.go
  go test ./agent/snapshot/contracts ./agent/snapshot ./agent/api/snapshots ./fly/commands -count=1
  git diff --check
  ```

  Expected: all tests pass, including the real archive/validator boundary and
  the existing secret non-disclosure suite.

- [ ] **Step 10: Record, review, and commit Task 2**

  Record verification and the blocking review in the ledger, then commit:

  ```bash
  git add agent/snapshot/validator.go agent/snapshot/validator_test.go agent/snapshot/contracts/repository.go agent/snapshot/contracts/repository_test.go agent/api/snapshots/types.go agent/api/snapshots/types_test.go agent/api/snapshots/handler.go agent/api/snapshots/handler_test.go fly/commands/agent_snapshots_test.go .superpowers/sdd/2026-08-01-jetbridge-first-user-blocker-remediation/progress.md
  git commit -m "fix(agent): return safe snapshot validation reasons"
  ```

### Task 3: Make the managed output builder protocol-ready

**Files:**

- Modify: `agent/outputbuilder/adapters.go`
- Modify: `agent/outputbuilder/adapters_test.go`
- Modify: `agent/schema/event_payloads.go`
- Modify: `agent/schema/event_payloads_test.go`
- Modify: `agent/schema/SCHEMA.md`
- Modify: `agent/runner/runner.go`
- Modify: `agent/runner/output_builder_test.go`
- Modify: `agent/runner/runner_test.go`

**Interfaces:**

- Produces: output-builder MCP protocol version `2024-11-05`, server name `concourse-output-builder`, and server version `1`.
- Produces: exactly three described tool definitions: `describe_output`, `validate_output`, and `write_output`, each with an object JSON Schema matching its strict decoder.
- Produces: `preflightManagedOutputBuilder(context.Context, *http.Client, string) error`, called only when `outputBuilderEnabled` is true.
- Produces: `finishBeforeModelPlatformError(*schema.EventWriter, string, string, time.Time, error) (int, error)`, shared by health and protocol failures so both persist the same bounded result/event sequence.
- Produces: `schema.EventMCPReady` (`mcp.ready`) with bounded `MCPReadyData{Server, ProtocolVersion, Tools}`; exactly one is written after successful managed-builder preflight and ingested into durable event counts.
- Preserves: the current loopback endpoint, strict authority-free tool requests, ordinary `/healthz` polling, and independent final sealing.

- [ ] **Step 1: Replace direct tool calls in the adapter test with a real MCP lifecycle**

  Extend `TestMCPLoopbackOnlyServesBuilderTools` to send, in order:

  ```json
  {"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1"}}}
  ```

  ```json
  {"jsonrpc":"2.0","method":"notifications/initialized"}
  ```

  ```json
  {"jsonrpc":"2.0","id":2,"method":"tools/list"}
  ```

  Assert the initialize result's exact protocol/server/capability fields, HTTP
  204 for the initialized notification, exact lexically sorted tool names,
  non-empty descriptions, and object schemas with the required fields from
  `mcpOutput` and `WriteRequest`. Keep the existing write call after discovery.

- [ ] **Step 2: Add the load-bearing negative protocol cases**

  Add cases for an unsupported protocol version, an initialized notification
  carrying an ID, an arbitrary `notifications/ready` method, and a tool request
  with authority fields. All must fail without calling the builder.

  Run:

  ```bash
  go test ./agent/outputbuilder -run 'TestMCPLoopbackOnlyServesBuilderTools' -count=1
  ```

  Expected: initialize currently returns JSON-RPC method-not-found.

- [ ] **Step 3: Implement the fixed MCP handshake and tool definitions**

  Add protocol request/result types local to `outputbuilder`, mirroring the
  minimal wire shape in `atc/api/mcpserver/protocol.go`. Handle exact methods:

  ```go
  switch request.Method {
  case "initialize":
      server.initialize(w, request)
  case "notifications/initialized":
      if len(request.ID) != 0 {
          writeMCPError(w, request.ID, -32600, "invalid request")
          return
      }
      w.WriteHeader(http.StatusNoContent)
  case "ping":
      writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Result: map[string]any{}})
  case "tools/list":
      writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: request.ID, Result: toolsListResult{Tools: outputBuilderTools()}})
  case "tools/call":
      server.call(w, r.Context(), request)
  default:
      writeMCPError(w, request.ID, -32601, "method not found")
  }
  ```

  `write_output` requires `output`, `subjects`, and `body`; `content` is an
  optional array. Both single-output tools require only `output`. Set
  `additionalProperties: false` in every schema to match `strictJSON`.

- [ ] **Step 4: Add the runner protocol-preflight regression**

  In `output_builder_test.go`, serve a handler whose `/healthz` returns 200 but
  whose `/mcp` rejects initialize. Run the runner with the server-owned builder
  marker and a recording provider adapter. Assert exit 2, a platform-error
  result, and zero adapter starts. Add a conformant fake that records the exact
  initialize → notification → list sequence and permits one adapter start.
  Decode `flight/events.ndjson` for the conformant case and require exactly one
  `mcp.ready` event between `step.start` and provider execution, with server
  `output-builder`, protocol `2024-11-05`, and the exact sorted three-tool set.
  The broken case must contain no `mcp.ready` event.

  The broken-server assertion is:

  ```go
  if adapter.starts != 0 {
      t.Fatalf("provider started %d times against a protocol-broken managed builder", adapter.starts)
  }
  if !strings.Contains(readResults(t, flightDir).Summary, "managed output builder protocol preflight failed") {
      t.Fatalf("results did not retain the bounded platform cause")
  }
  ```

- [ ] **Step 5: Run the runner preflight test and observe false health**

  Run:

  ```bash
  go test ./agent/runner -run 'Test.*OutputBuilder.*Preflight' -count=1
  ```

  Expected before implementation: the provider starts because only `/healthz`
  is checked.

- [ ] **Step 6: Implement managed-only protocol preflight**

  Add `preflightManagedOutputBuilder` beside `waitForSidecars`. It must POST the
  exact initialize request, require HTTP 200 and protocol `2024-11-05`, POST the
  initialized notification and require 204, then call `tools/list` and compare
  the exact three-name set and object schema type. Bound every response with
  the runner-local `managedOutputBuilderResponseLimit` and close bodies on all
  paths.

  Immediately after `waitForSidecars`, add:

  ```go
  if outputBuilderEnabled {
      client := &http.Client{Timeout: sidecarHealthInterval}
      if err := preflightManagedOutputBuilder(ctx, client, mcpServers[outputBuilderMCPName]); err != nil {
          return finishBeforeModelPlatformError(events, cfg.FlightDir, cfg.StepName, start, fmt.Errorf("managed output builder protocol preflight failed: %w", err))
      }
      writeEvent(events, schema.EventMCPReady, schema.MCPReadyData{
          Server: outputBuilderMCPName,
          ProtocolVersion: managedOutputBuilderProtocolVersion,
          Tools: []string{"describe_output", "validate_output", "write_output"},
      })
  }
  ```

  Extract the current sidecar failure's event/results sequence into the exact
  helper signature declared above and use it for both paths. Define
  `managedOutputBuilderResponseLimit` as `1 << 20` in `agent/runner`; bound
  every preflight response with that local constant and close bodies on all
  paths. Do not call this preflight for authored MCP names or the managed
  broker. Define `EventMCPReady` and `MCPReadyData` in `agent/schema`, document
  the safe payload in `SCHEMA.md`, and add a JSON round-trip test. The event
  contains no endpoint, request/response body, token, prompt, output content,
  or arbitrary error text.

- [ ] **Step 7: Run the complete Task 3 verification**

  Run:

  ```bash
  gofmt -w agent/outputbuilder/adapters.go agent/outputbuilder/adapters_test.go agent/schema/event_payloads.go agent/schema/event_payloads_test.go agent/runner/runner.go agent/runner/output_builder_test.go agent/runner/runner_test.go
  go test ./agent/schema ./agent/outputbuilder ./cmd/agent-output ./agent/runner -count=1
  git diff --check
  ```

  Expected: all tests pass; a superficially healthy but protocol-broken managed
  builder prevents provider start, while a conformant preflight persists one
  safe `mcp.ready` event before the provider starts.

- [ ] **Step 8: Record, review, and commit Task 3**

  Record verification and review in the ledger, then commit:

  ```bash
  git add agent/outputbuilder/adapters.go agent/outputbuilder/adapters_test.go agent/schema/event_payloads.go agent/schema/event_payloads_test.go agent/schema/SCHEMA.md agent/runner/runner.go agent/runner/output_builder_test.go agent/runner/runner_test.go .superpowers/sdd/2026-08-01-jetbridge-first-user-blocker-remediation/progress.md
  git commit -m "fix(agent): make output builder MCP protocol-ready"
  ```

### Task 4: Make the runner CLI contract immutable and publish its digest

**Files:**

- Create: `deploy/agent-runner/smoke.sh`
- Modify: `deploy/agent-runner/Dockerfile`
- Modify: `deploy/agent_runner_dockerfile_test.go`
- Modify: `Makefile`
- Modify: `deploy/concourse-pipeline.yml`
- Modify: `docs/agentic/V3_CUTOVER_DEPLOY.md`

**Interfaces:**

- Consumes: Claude Code linux-x64 release `2.1.212` with SHA-256 `044a88cf3a5180776617fd3da1238dcbf9141ddec449a39cf7d2af1ac78e684e`, matching `deploy/agent-broker/Dockerfile`.
- Produces: `/usr/local/bin/agent-runner-image-smoke`, which exits nonzero unless the packaged version and all load-bearing flags are present.
- Produces: Make targets `build-agent-runner-image` and `test-agent-runner-smoke`, gated by `CONCOURSE_AGENT_RUNNER_SMOKE=1`.
- Produces: pipeline evidence `CONCOURSE_AGENT_STEP_IMAGE=<repository>@sha256:<64 lowercase hex>` only after an explicit linux/amd64 build, the registry push response, and a registry-pulled immutable reference inspected as exactly `linux/amd64`.

- [ ] **Step 1: Strengthen the Dockerfile contract test first**

  Extend `TestAgentRunnerDockerfile` to assert that the runner no longer uses
  npm or `@anthropic-ai/claude-code@2.0.1`, contains the exact release URL and
  checksum above, installs `/usr/local/bin/claude` mode 0555, copies the smoke
  script mode 0555, and runs it during the image build. Read
  `agent-broker/Dockerfile` in the test and assert both images contain the same
  Claude release URL and checksum.

  Run:

  ```bash
  go test ./deploy -run 'TestAgentRunnerDockerfile' -count=1
  ```

  Expected: failure because the runner still pins npm package `2.0.1`.

- [ ] **Step 2: Add the executable image smoke contract**

  Create `deploy/agent-runner/smoke.sh` with this closed check:

  ```sh
  #!/bin/sh
  set -eu

  version=$(claude --version 2>&1)
  case "$version" in
    2.1.212*) ;;
    *) echo "unexpected Claude Code version: $version" >&2; exit 1 ;;
  esac

  help=$(claude --help 2>&1)
  for flag in --max-budget-usd --mcp-config --strict-mcp-config --max-turns \
    --append-system-prompt --output-format --verbose --dangerously-skip-permissions
  do
    printf '%s\n' "$help" | grep -Fq -- "$flag" || {
      echo "Claude Code is missing required flag $flag" >&2
      exit 1
    }
  done

  command -v agent-runner >/dev/null
  command -v function-runner >/dev/null
  command -v agent-output >/dev/null
  ```

  Run `sh -n deploy/agent-runner/smoke.sh` and require success.

- [ ] **Step 3: Replace the stale npm CLI with the checksum-pinned native binary**

  Add a download stage based on the same pinned Debian base used by the broker,
  download the exact URL, run `sha256sum --check --strict`, and install the
  binary mode 0555. Copy it into the final runner image. Retain Git,
  certificates, curl if still used at runtime, the Go toolchain, root execution,
  `IS_SANDBOX=1`, and all three Concourse binaries.

  The download check is exact:

  ```dockerfile
  RUN curl --fail --location --silent --show-error \
        https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/2.1.212/linux-x64/claude \
        --output /tmp/claude \
      && echo "044a88cf3a5180776617fd3da1238dcbf9141ddec449a39cf7d2af1ac78e684e  /tmp/claude" | sha256sum --check --strict \
      && install -D -m 0555 /tmp/claude /out/claude
  ```

  Copy the smoke script and execute it only after every required binary is in
  its final location.

- [ ] **Step 4: Add explicit local image targets**

  Add `AGENT_RUNNER_IMAGE ?= concourse-agent-runner:dev`, both target names to
  `.PHONY`, and these behaviors to `Makefile`:

  ```make
  build-agent-runner-image:
	  docker build --platform linux/amd64 \
	    --file deploy/agent-runner/Dockerfile \
	    --tag "$(AGENT_RUNNER_IMAGE)" \
	    .

  test-agent-runner-smoke:
	  @test "$$CONCOURSE_AGENT_RUNNER_SMOKE" = "1" || { echo "ERROR: set CONCOURSE_AGENT_RUNNER_SMOKE=1 to run the runner smoke gate"; exit 1; }
	  docker run --rm --platform linux/amd64 --entrypoint /usr/local/bin/agent-runner-image-smoke "$(AGENT_RUNNER_IMAGE)"
  ```

  Preserve the existing broker targets unchanged.

- [ ] **Step 5: Make the pipeline smoke before push and report an immutable digest**

  Change the runner job to tag the image with `SHORT_SHA=$(git rev-parse
  --short=12 HEAD)` and build that commit tag with the platform explicit:

  ```sh
  SHORT_SHA=$(git rev-parse --short=12 HEAD)
  IMAGE="${GHCR}:${SHORT_SHA}"
  kubectl exec -n cicd "${BUILDER_POD}" -- \
    docker build --platform linux/amd64 \
      --tag "${IMAGE}" \
      --file /tmp/src/deploy/agent-runner/Dockerfile /tmp/src
  kubectl exec -n cicd "${BUILDER_POD}" -- \
    docker run --rm --platform linux/amd64 \
      --entrypoint /usr/local/bin/agent-runner-image-smoke "${IMAGE}"
  ```

  Run the smoke inside that exact image, then push the commit tag. Parse the
  registry response with the same bounded grammar already used by the broker
  job, pull the resulting immutable reference back from the registry, and
  inspect its platform before accepting or printing the digest:

  ```sh
  PUSH_OUTPUT=$(kubectl exec -n cicd "${BUILDER_POD}" -- docker push "${IMAGE}")
  printf '%s\n' "${PUSH_OUTPUT}"
  DIGEST=$(printf '%s\n' "${PUSH_OUTPUT}" | sed -n 's/.*digest: \(sha256:[a-f0-9]\{64\}\).*/\1/p' | tail -1)
  if ! printf '%s\n' "${DIGEST}" | grep -Eq '^sha256:[a-f0-9]{64}$'; then
      echo "FATAL: registry push returned no exact lowercase sha256 digest"
      exit 1
  fi
  IMAGE_REPOSITORY=${IMAGE%:*}
  IMMUTABLE_IMAGE="${IMAGE_REPOSITORY}@${DIGEST}"
  kubectl exec -n cicd "${BUILDER_POD}" -- docker pull --platform linux/amd64 "${IMMUTABLE_IMAGE}"
  PUSHED_PLATFORM=$(kubectl exec -n cicd "${BUILDER_POD}" -- docker image inspect --format '{{.Os}}/{{.Architecture}}' "${IMMUTABLE_IMAGE}")
  if test "${PUSHED_PLATFORM}" != "linux/amd64"; then
      echo "FATAL: pushed runner platform is ${PUSHED_PLATFORM}, want linux/amd64"
      exit 1
  fi
  echo "CONCOURSE_AGENT_STEP_IMAGE=${IMMUTABLE_IMAGE}"
  ```

  Mutable version/latest pushes may remain, but the job must fail if the
  commit-tag push has no registry-reported digest or the registry-resolved
  immutable image is not exactly `linux/amd64`.

- [ ] **Step 6: Correct the deployment runbook**

  Update `V3_CUTOVER_DEPLOY.md` so every deploy pauses new dispatch, builds and
  smokes the same-commit runner, records the printed digest, updates external
  `CONCOURSE_AGENT_STEP_IMAGE` through its reviewed path, deploys the matching
  web, verifies the configured digest, reimports definitions, and only then
  resumes dispatch. State that a positive budget slice is unsupported unless
  the smoke gate proves `--max-budget-usd`.

- [ ] **Step 7: Run repository and shell verification**

  Run:

  ```bash
  sh -n deploy/agent-runner/smoke.sh
  go test ./deploy ./agent/runner -count=1
  git diff --check
  ```

  Expected: all pass and the Dockerfile contract proves pin parity with the
  broker image.

- [ ] **Step 8: Run the image gate where Docker is available**

  Run locally or on Borg:

  ```bash
  make build-agent-runner-image
  CONCOURSE_AGENT_RUNNER_SMOKE=1 make test-agent-runner-smoke
  ```

  Expected: the image builds and the smoke exits zero. If the local Docker
  daemon is unavailable, record that once in the ledger and attach the exact
  successful pipeline/Borg build and smoke evidence before accepting the task;
  green Go tests alone do not satisfy this gate.

- [ ] **Step 9: Record, review, and commit Task 4**

  Record the immutable pin, image evidence, and review in the ledger, then
  commit:

  ```bash
  git add deploy/agent-runner/smoke.sh deploy/agent-runner/Dockerfile deploy/agent_runner_dockerfile_test.go Makefile deploy/concourse-pipeline.yml docs/agentic/V3_CUTOVER_DEPLOY.md .superpowers/sdd/2026-08-01-jetbridge-first-user-blocker-remediation/progress.md
  git commit -m "fix(deploy): enforce agent runner CLI compatibility"
  ```

### Task 5: Surface safe operator diagnostics and document the node lifecycle

**Files:**

- Modify: `fly/commands/agent_workflow_runs.go`
- Create: `fly/commands/agent_workflow_runs_test.go`
- Modify: `fly/commands/agent_nodes.go`
- Modify: `fly/commands/agent_nodes_test.go`
- Modify: `fly/commands/targets.go`
- Modify: `fly/integration/targets_test.go`
- Modify: `docs/operations/reusable-node-definitions.md`
- Modify: `docs/platform-guide.html`

**Interfaces:**

- Changes: `printAgentWorkflowRunDetail(targetName rc.TargetName, detail workflowrunsapi.RunDetail, jsonOutput bool) error`.
- Produces: plain output lines `planned build: <id>` and `inspect logs: fly -t <target> watch -b <id>` when `PlannedBuildID != nil`; JSON remains byte-for-byte governed by the API type.
- Changes: `getExpirationFromString(targetName rc.TargetName, ttoken *rc.TargetToken) string`; undecodable local token expiry becomes `expiry unavailable (run fly -t <target> status)` without making a network request.

- [ ] **Step 1: Write renderer tests for the exact build hint**

  Add tests capturing stdout from `printAgentWorkflowRunDetail`. With
  `PlannedBuildID` set to 418 and target `home`, require:

  ```text
  planned build: 418
  inspect logs: fly -t home watch -b 418
  ```

  With no planned build, require neither line. With `jsonOutput=true`, decode
  the JSON and assert `planned_build_id: 418` with no human hint embedded.

- [ ] **Step 2: Run the renderer tests and observe the missing correlation**

  Run:

  ```bash
  go test ./fly/commands -run 'Test.*WorkflowRunDetail.*Build' -count=1
  ```

  Expected: failure because plain rendering currently drops `PlannedBuildID`.

- [ ] **Step 3: Thread the target alias through every renderer call**

  Change the renderer signature and all workflow/node call sites to pass
  `Fly.Target`. Add after `printAgentWorkflowRun`:

  ```go
  if detail.PlannedBuildID != nil {
      fmt.Printf("planned build: %d\n", *detail.PlannedBuildID)
      fmt.Printf("inspect logs: fly -t %s watch -b %d\n", targetName, *detail.PlannedBuildID)
  }
  ```

  Reject an empty target alias in the renderer only when it needs to print the
  hint; tests and normal CLI execution always supply the selected alias. Do not
  print `ErrorMessage` or any DB-stored terminal cause.

- [ ] **Step 4: Correct the target-expiry claim with integration coverage**

  Change the invalid-token fixture expectation in `targets_test.go` to include
  its target alias and the status command. Change
  `getExpirationFromString` to accept the alias:

  ```go
  func getExpirationFromString(targetName rc.TargetName, targetToken *rc.TargetToken) string {
      if targetToken == nil || targetToken.Type == "" || targetToken.Value == "" {
          return "n/a"
      }
      expiry, err := token.Factory{}.ParseExpiry(targetToken.Value)
      if err != nil {
          return fmt.Sprintf("expiry unavailable (run fly -t %s status)", targetName)
      }
      return expiry.UTC().Format(time.RFC1123)
  }
  ```

  Pass `targetName` from the existing `for targetName, targetValues := range
  targets` loop. Keep the nil/empty-token result exactly `n/a`.

  Keep valid JWT expiry formatting unchanged and do not add an authenticated
  request to `fly targets`.

- [ ] **Step 5: Document the complete first-user node path**

  Update `reusable-node-definitions.md` with:

  - direct typed input creation using `agent snapshots create --type ... --from ...`;
  - exact retained resource capture using `capture-resource -p ... -r ... -v key:value`;
  - exact import version capture, unreleased run, `show-run`, build-log command,
    output download/inspection, then release;
  - the positive budget slice's dependency on the deployment's runner smoke
    gate and the fact that zero is uncapped to the runner;
  - model omission as the portable default unless the target's broker catalog
    deliberately requires a selector;
  - a warning that `registry.example/...@sha256:aaaa...` is non-runnable sample
    syntax;
  - bundled skills are immutable and discoverable, not guaranteed to be read,
    so contract-critical record mechanics belong in initial authority/builder.

  Update the reusable-node section of `platform-guide.html` to remove the
  “undocumented capability” claim, summarize the lifecycle, and link the
  operations guide rather than duplicating it.

- [ ] **Step 6: Run Task 5 verification**

  Run:

  ```bash
  gofmt -w fly/commands/agent_workflow_runs.go fly/commands/agent_workflow_runs_test.go fly/commands/agent_nodes.go fly/commands/agent_nodes_test.go fly/commands/targets.go fly/integration/targets_test.go
  go test ./fly/commands -count=1
  ginkgo -r --keep-going --focus='targets' ./fly/integration/
  rg -n 'undocumented capability|n/a: invalid token' docs/platform-guide.html docs/operations/reusable-node-definitions.md fly/commands fly/integration
  git diff --check
  ```

  Expected: Go tests pass; the residue search returns no user-facing stale
  wording, apart from an explicitly named historical assertion if one is
  required by a compatibility test.

- [ ] **Step 7: Record, review, and commit Task 5**

  Record verification and review in the ledger, then commit:

  ```bash
  git add fly/commands/agent_workflow_runs.go fly/commands/agent_workflow_runs_test.go fly/commands/agent_nodes.go fly/commands/agent_nodes_test.go fly/commands/targets.go fly/integration/targets_test.go docs/operations/reusable-node-definitions.md docs/platform-guide.html .superpowers/sdd/2026-08-01-jetbridge-first-user-blocker-remediation/progress.md
  git commit -m "docs(agent): expose the first-user diagnostic path"
  ```

### Task 6: Deploy the same-commit runtime and repeat the node-level dogfood trial

**Files:**

- Modify: `JETBRIDGE_FIRST_USER_FINDINGS.md`
- Modify: `.superpowers/sdd/2026-08-01-jetbridge-first-user-blocker-remediation/progress.md`
- Modify production code only if the acceptance pass exposes a regression that is already within this design; reopen the owning task and its focused tests before editing.

**Interfaces:**

- Consumes: a web artifact and runner image built from the same accepted commit, plus the registry-reported runner digest.
- Consumes: target `home`, team `main`, the `concourse/repo` Git resource, `agent/workflow/seeds/code-review-node-v1`, and `agent/workflow/seeds/log-diagnosis-node-v1`.
- Produces: package re-import dispositions, fresh post-rollout runs, exact snapshot/run/build IDs, one durable `mcp.ready` event count per successful managed-builder run, downloaded typed outputs, and a release decision backed by inspected output.
- Consumes the exact node version returned by byte-content import; because the
  packages are unchanged, content deduplication may correctly return
  `code-review@9` and `log-diagnosis@9`. Pre-rollout runs of those versions are
  not acceptance evidence.
- Does not consume: benchmark ground truth, case metadata, notes, or withheld
  fixtures as model inputs.

- [ ] **Step 1: Re-run the repository gates at the deployment commit**

  From a clean task commit, run:

  ```bash
  go test ./agent/resourcecapture ./agent/workflowrun ./agent/snapshot/contracts ./agent/snapshot ./agent/api/snapshots ./agent/outputbuilder ./cmd/agent-output ./agent/runner ./fly/commands ./deploy -count=1
  ginkgo --procs=1 --focus='exact authorized resource-capture output' ./atc/db
  git diff --check
  ```

  Record the exact commit. Do not deploy a worktree with uncommitted production
  changes.

- [ ] **Step 2: Build, smoke, and capture the immutable runner digest**

  Trigger `build-agent-runner-image` for the exact commit or run the approved
  Borg equivalent. Require the log to show a successful
  `/usr/local/bin/agent-runner-image-smoke` and capture the printed line:

  ```text
  CONCOURSE_AGENT_STEP_IMAGE=<repository>@sha256:<64 lowercase hex>
  ```

  Require the pipeline log to include the registry pull and exact
  `linux/amd64` inspection of that immutable reference. Verify the web artifact
  is built from the same full commit. Record the platform and both identities
  in the ledger before rollout.

- [ ] **Step 3: Roll out through the normal external deployment path**

  Pause new dispatch, update the external deployment's
  `CONCOURSE_AGENT_STEP_IMAGE` to the exact captured digest through its reviewed
  mechanism, deploy the matching web artifact, and inspect the running web
  arguments/pod specification to verify that exact digest. Resume dispatch only
  after:

  ```bash
  fly -t home status
  ```

  succeeds. No repository task may mutate home-infra directly without its
  separate authority.

- [ ] **Step 4: Prove direct nested repository upload**

  Materialize a clean, ancestor-only test repository in a task-specific
  `mktemp -d` directory, including its real `.git` object graph, and run the
  freshly built Fly:

  ```bash
  DOGFOOD_ROOT=$(mktemp -d /tmp/jetbridge-first-user.XXXXXX)
  DOGFOOD_REPOSITORY="$DOGFOOD_ROOT/direct-repository"
  DOGFOOD_DOWNLOAD="$DOGFOOD_ROOT/downloads"
  mkdir -p "$DOGFOOD_DOWNLOAD"
  DOGFOOD_COMMIT=$(git rev-parse HEAD)
  git clone --no-hardlinks . "$DOGFOOD_REPOSITORY"
  git -C "$DOGFOOD_REPOSITORY" checkout --detach "$DOGFOOD_COMMIT"
  git -C "$DOGFOOD_REPOSITORY" for-each-ref --format='%(refname)' > "$DOGFOOD_ROOT/direct-refs"
  while IFS= read -r ref; do
    test -z "$ref" || git -C "$DOGFOOD_REPOSITORY" update-ref -d "$ref"
  done < "$DOGFOOD_ROOT/direct-refs"
  git -C "$DOGFOOD_REPOSITORY" reflog expire --expire=now --all
  git -C "$DOGFOOD_REPOSITORY" gc --prune=now
  test -z "$(git -C "$DOGFOOD_REPOSITORY" status --porcelain)"
  go build -o /tmp/fly-jetbridge-first-user ./fly
  DIRECT_REPOSITORY_JSON=$(/tmp/fly-jetbridge-first-user -t home agent snapshots create --type repository/v1 --from "$DOGFOOD_REPOSITORY" --json)
  DIRECT_REPOSITORY_SNAPSHOT_ID=$(printf '%s\n' "$DIRECT_REPOSITORY_JSON" | jq -er '.id')
  printf '%s\n' "$DIRECT_REPOSITORY_SNAPSHOT_ID" | grep -Eq '^[1-9][0-9]*$'
  /tmp/fly-jetbridge-first-user -t home agent snapshots download "$DIRECT_REPOSITORY_SNAPSHOT_ID" --to "$DOGFOOD_DOWNLOAD/direct-repository.tar"
  ```

  Record `DIRECT_REPOSITORY_SNAPSHOT_ID`, the response reason/message on any
  422, and the server/web commit. Keep `DOGFOOD_ROOT` until every Task 6 output
  has been inspected; delete only that exact `mktemp` directory after the
  evidence commit.

- [ ] **Step 5: Prove exact Git resource capture**

  Read the first enabled exact `ref` from `concourse/repo`, then pass that
  complete version back to capture:

  ```bash
  RESOURCE_VERSIONS_JSON=$(/tmp/fly-jetbridge-first-user -t home resource-versions -r concourse/repo --count 50 --json)
  EXACT_RESOURCE_REF=$(printf '%s\n' "$RESOURCE_VERSIONS_JSON" | jq -er '[.[] | select(.enabled == true) | .version.ref][0]')
  CAPTURE_JSON=$(/tmp/fly-jetbridge-first-user -t home agent snapshots capture-resource -p concourse -r repo -v "ref:$EXACT_RESOURCE_REF" --type repository/v1 --json)
  CAPTURE_REPOSITORY_SNAPSHOT_ID=$(printf '%s\n' "$CAPTURE_JSON" | jq -er '.snapshot.id')
  CAPTURE_PIPELINE_RUN_ID=$(printf '%s\n' "$CAPTURE_JSON" | jq -er '.execution.pipeline_run_id')
  /tmp/fly-jetbridge-first-user -t home agent snapshots show "$CAPTURE_REPOSITORY_SNAPSHOT_ID" --json
  /tmp/fly-jetbridge-first-user -t home agent snapshots download "$CAPTURE_REPOSITORY_SNAPSHOT_ID" --to "$DOGFOOD_DOWNLOAD/captured-repository.tar"
  ```

  Require non-empty exact ref, snapshot ID, and pipeline-run values. A returned
  accepted/running generation is polled by the command; do not start a second
  operation with a different identity.

- [ ] **Step 6: Create the log input without exposing benchmark answers**

  Create a staging directory containing only the user-facing files under
  `bench/corpus/rca-jb-004/task/evidence/`; exclude `case.yaml`, `notes.md`,
  every `ground_truth` path, and the task title if it discloses the conclusion.
  Upload it:

  ```bash
  DOGFOOD_LOG_BUNDLE="$DOGFOOD_ROOT/log-bundle"
  mkdir -p "$DOGFOOD_LOG_BUNDLE/evidence"
  cp bench/corpus/rca-jb-004/task/evidence/build-output.md "$DOGFOOD_LOG_BUNDLE/evidence/build-output.md"
  cp bench/corpus/rca-jb-004/task/evidence/cluster-observations.md "$DOGFOOD_LOG_BUNDLE/evidence/cluster-observations.md"
  find "$DOGFOOD_LOG_BUNDLE" -type f -print | sort > "$DOGFOOD_ROOT/log-exposure.txt"
  test "$(wc -l < "$DOGFOOD_ROOT/log-exposure.txt" | tr -d ' ')" = 2
  LOG_SNAPSHOT_JSON=$(/tmp/fly-jetbridge-first-user -t home agent snapshots create --type log-bundle/v1 --from "$DOGFOOD_LOG_BUNDLE" --json)
  LOG_SNAPSHOT_ID=$(printf '%s\n' "$LOG_SNAPSHOT_JSON" | jq -er '.id')
  ```

  Record `LOG_SNAPSHOT_ID` and the exact contents of `log-exposure.txt` in the
  ledger.

- [ ] **Step 7: Re-import log-diagnosis and start a fresh budget-capped run**

  Import after rollout, parse the exact returned version from stdout, and run
  that immutable version. A byte-identical import returning `@9` is expected;
  do not alter package bytes only to force a new integer:

  ```bash
  LOG_IMPORT=$(/tmp/fly-jetbridge-first-user -t home agent nodes import agent/workflow/seeds/log-diagnosis-node-v1)
  LOG_VERSION=$(printf '%s\n' "$LOG_IMPORT" | awk '$1 == "imported" && $2 == "log-diagnosis" && $3 == "version" {print $4}')
  printf '%s\n' "$LOG_VERSION" | grep -Eq '^[1-9][0-9]*$'
  LOG_NODE_JSON=$(/tmp/fly-jetbridge-first-user -t home agent nodes show log-diagnosis "$LOG_VERSION" --json)
  printf '%s\n' "$LOG_NODE_JSON" | jq -e '.. | objects | select(.budget_slice_usd? == 5)' >/dev/null
  LOG_RUN_JSON=$(/tmp/fly-jetbridge-first-user -t home agent nodes run log-diagnosis "$LOG_VERSION" --input "logs=$LOG_SNAPSHOT_ID" --idempotency-key="first-user-remediation-log-$LOG_VERSION" --json)
  LOG_RUN_ID=$(printf '%s\n' "$LOG_RUN_JSON" | jq -er '.workflow_run_id')
  while :; do
    LOG_DETAIL=$(/tmp/fly-jetbridge-first-user -t home agent nodes show-run log-diagnosis "$LOG_RUN_ID" --json)
    LOG_STATUS=$(printf '%s\n' "$LOG_DETAIL" | jq -er '.status')
    case "$LOG_STATUS" in
      succeeded|failed|errored|aborted) break ;;
      *) sleep 5 ;;
    esac
  done
  test "$LOG_STATUS" = succeeded
  LOG_BUILD_ID=$(printf '%s\n' "$LOG_DETAIL" | jq -er '.planned_build_id')
  LOG_OUTPUT_ID=$(printf '%s\n' "$LOG_DETAIL" | jq -er '.outputs[] | select(.port == "diagnosis") | .snapshot.id')
  LOG_WATCH=$(/tmp/fly-jetbridge-first-user -t home watch -b "$LOG_BUILD_ID")
  printf '%s\n' "$LOG_WATCH"
  if printf '%s\n' "$LOG_WATCH" | grep -Fq -- "unknown option '--max-budget-usd'"; then
    echo "runner image still rejects the positive budget cap" >&2
    exit 1
  fi
  LOG_METRICS=$(/tmp/fly-jetbridge-first-user -t home curl "/api/v1/builds/${LOG_BUILD_ID}/agent-metrics" -- --silent --show-error)
  printf '%s\n' "$LOG_METRICS" | jq -e '([.[].event_counts["mcp.ready"] // 0] | add) == 1' >/dev/null
  /tmp/fly-jetbridge-first-user -t home agent snapshots download "$LOG_OUTPUT_ID" --to "$DOGFOOD_DOWNLOAD/diagnosis.tar"
  tar -xOf "$DOGFOOD_DOWNLOAD/diagnosis.tar" record.json | jq -e '.type == "diagnosis/v1"' >/dev/null
  ```

  The build-scoped metrics response—not model prose or `/healthz`—must prove
  exactly one ingested `mcp.ready` event. Save that bounded JSON as acceptance
  evidence. Record the run's `parameterized_config_hash` beside the already
  verified deployment digest. Inspect the complete `record.json`, not just its
  type assertion, before considering release.

- [ ] **Step 8: Re-import code-review and start a fresh run with two exact repositories**

  Materialize only the `before` and `after` repository refs declared by
  `bench/corpus/review-jb-003/case.yaml`; remove refs/reflogs that make later
  commits reachable, and do not copy case metadata, notes, task prose, or ground
  truth into either snapshot. The two exact case refs are
  `54b541a81e6235ca74256dfbd50666ec45a18d2c` and
  `199ab7497399aa157065b660537caa652373791c`. Materialize, prune, upload, and
  run them:

  ```bash
  BEFORE_REF=54b541a81e6235ca74256dfbd50666ec45a18d2c
  AFTER_REF=199ab7497399aa157065b660537caa652373791c
  for side in before after; do
    if test "$side" = before; then ref=$BEFORE_REF; else ref=$AFTER_REF; fi
    tree="$DOGFOOD_ROOT/review-$side"
    if git ls-tree -r --name-only "$ref" | grep -q '^bench/corpus/review-jb-003/'; then
      echo "benchmark harness data is reachable at $ref" >&2
      exit 1
    fi
    git clone --no-hardlinks . "$tree"
    git -C "$tree" checkout --detach "$ref"
    git -C "$tree" for-each-ref --format='%(refname)' > "$DOGFOOD_ROOT/$side-refs"
    while IFS= read -r reachable_ref; do
      test -z "$reachable_ref" || git -C "$tree" update-ref -d "$reachable_ref"
    done < "$DOGFOOD_ROOT/$side-refs"
    git -C "$tree" reflog expire --expire=now --all
    git -C "$tree" gc --prune=now
    test -z "$(git -C "$tree" status --porcelain)"
  done
  BEFORE_JSON=$(/tmp/fly-jetbridge-first-user -t home agent snapshots create --type repository/v1 --from "$DOGFOOD_ROOT/review-before" --json)
  BEFORE_SNAPSHOT_ID=$(printf '%s\n' "$BEFORE_JSON" | jq -er '.id')
  AFTER_JSON=$(/tmp/fly-jetbridge-first-user -t home agent snapshots create --type repository/v1 --from "$DOGFOOD_ROOT/review-after" --json)
  AFTER_SNAPSHOT_ID=$(printf '%s\n' "$AFTER_JSON" | jq -er '.id')
  REVIEW_IMPORT=$(/tmp/fly-jetbridge-first-user -t home agent nodes import agent/workflow/seeds/code-review-node-v1)
  REVIEW_VERSION=$(printf '%s\n' "$REVIEW_IMPORT" | awk '$1 == "imported" && $2 == "code-review" && $3 == "version" {print $4}')
  printf '%s\n' "$REVIEW_VERSION" | grep -Eq '^[1-9][0-9]*$'
  REVIEW_NODE_JSON=$(/tmp/fly-jetbridge-first-user -t home agent nodes show code-review "$REVIEW_VERSION" --json)
  printf '%s\n' "$REVIEW_NODE_JSON" | jq -e '.. | objects | select(.budget_slice_usd? == 5)' >/dev/null
  REVIEW_RUN_JSON=$(/tmp/fly-jetbridge-first-user -t home agent nodes run code-review "$REVIEW_VERSION" --input "before=$BEFORE_SNAPSHOT_ID" --input "after=$AFTER_SNAPSHOT_ID" --param MINIMUM_SEVERITY=medium --idempotency-key="first-user-remediation-review-$REVIEW_VERSION" --json)
  REVIEW_RUN_ID=$(printf '%s\n' "$REVIEW_RUN_JSON" | jq -er '.workflow_run_id')
  while :; do
    REVIEW_DETAIL=$(/tmp/fly-jetbridge-first-user -t home agent nodes show-run code-review "$REVIEW_RUN_ID" --json)
    REVIEW_STATUS=$(printf '%s\n' "$REVIEW_DETAIL" | jq -er '.status')
    case "$REVIEW_STATUS" in
      succeeded|failed|errored|aborted) break ;;
      *) sleep 5 ;;
    esac
  done
  test "$REVIEW_STATUS" = succeeded
  REVIEW_BUILD_ID=$(printf '%s\n' "$REVIEW_DETAIL" | jq -er '.planned_build_id')
  REVIEW_OUTPUT_ID=$(printf '%s\n' "$REVIEW_DETAIL" | jq -er '.outputs[] | select(.port == "review") | .snapshot.id')
  REVIEW_WATCH=$(/tmp/fly-jetbridge-first-user -t home watch -b "$REVIEW_BUILD_ID")
  printf '%s\n' "$REVIEW_WATCH"
  REVIEW_METRICS=$(/tmp/fly-jetbridge-first-user -t home curl "/api/v1/builds/${REVIEW_BUILD_ID}/agent-metrics" -- --silent --show-error)
  printf '%s\n' "$REVIEW_METRICS" | jq -e '([.[].event_counts["mcp.ready"] // 0] | add) == 1' >/dev/null
  /tmp/fly-jetbridge-first-user -t home agent snapshots download "$REVIEW_OUTPUT_ID" --to "$DOGFOOD_DOWNLOAD/review.tar"
  tar -xOf "$DOGFOOD_DOWNLOAD/review.tar" record.json | jq -e '.type == "review/v1"' >/dev/null
  ```

  Require exactly one durable `mcp.ready` event across the build's metrics,
  terminal success, and one independently sealed `review/v1` output. Save the
  bounded metrics JSON and record its `parameterized_config_hash` beside the
  verified deployment digest. Inspect the full review and evaluate quality
  separately from contract validity without opening `case.yaml`, `notes.md`,
  or `ground_truth` during the run.

- [ ] **Step 9: Exercise the safe failed-run diagnostic**

  Use the already imported `code-review@1`, whose immutable placeholder
  capability image deterministically fails after planning and before model
  inference. Run it with the valid review inputs and inspect its plain detail:

  ```bash
  FAILED_RUN_JSON=$(/tmp/fly-jetbridge-first-user -t home agent nodes run code-review 1 --input "before=$BEFORE_SNAPSHOT_ID" --input "after=$AFTER_SNAPSHOT_ID" --param MINIMUM_SEVERITY=medium --idempotency-key=first-user-remediation-log-hint --json)
  FAILED_RUN_ID=$(printf '%s\n' "$FAILED_RUN_JSON" | jq -er '.workflow_run_id')
  while :; do
    FAILED_DETAIL=$(/tmp/fly-jetbridge-first-user -t home agent nodes show-run code-review "$FAILED_RUN_ID" --json)
    FAILED_STATUS=$(printf '%s\n' "$FAILED_DETAIL" | jq -er '.status')
    case "$FAILED_STATUS" in
      succeeded|failed|errored|aborted) break ;;
      *) sleep 5 ;;
    esac
  done
  test "$FAILED_STATUS" != succeeded
  FAILED_BUILD_ID=$(printf '%s\n' "$FAILED_DETAIL" | jq -er '.planned_build_id')
  FAILED_PLAIN=$(/tmp/fly-jetbridge-first-user -t home agent nodes show-run code-review "$FAILED_RUN_ID")
  printf '%s\n' "$FAILED_PLAIN" | grep -Fx "planned build: $FAILED_BUILD_ID"
  printf '%s\n' "$FAILED_PLAIN" | grep -Fx "inspect logs: fly -t home watch -b $FAILED_BUILD_ID"
  /tmp/fly-jetbridge-first-user -t home watch -b "$FAILED_BUILD_ID"
  ```

  Confirm the log reaches the known placeholder-image failure. Do not inject a
  credential, token, or secret-looking database error merely to test
  redaction.

- [ ] **Step 10: Decide release from output evidence**

  After the blocking reviewer records that each downloaded record is valid and
  usable, release only the corresponding exact version variables:

  ```bash
  /tmp/fly-jetbridge-first-user -t home agent nodes release log-diagnosis "$LOG_VERSION" --compatibility=compatible
  /tmp/fly-jetbridge-first-user -t home agent nodes release code-review "$REVIEW_VERSION" --compatibility=compatible
  ```

  Omit the corresponding command for any failed or invalid version and record
  it as unreleased. An exact `@9` may be released only when its newly created
  post-rollout run supplies the proof; its old failed runs never do.

- [ ] **Step 11: Record exact evidence and commit the completed track**

  Update `JETBRIDGE_FIRST_USER_FINDINGS.md` with the repaired root causes,
  immutable image/web identities, snapshot/node/run/build/output IDs, bounded
  spend, release state, remaining pain points, and any inference clearly
  labeled. Mark ledger tasks complete only when their own gates passed.

  Run:

  ```bash
  git diff --check
  git status --short
  ```

  Confirm the unrelated semantic-rebase ledger edit remains untouched, then
  commit only this track's two evidence files:

  ```bash
  git add JETBRIDGE_FIRST_USER_FINDINGS.md .superpowers/sdd/2026-08-01-jetbridge-first-user-blocker-remediation/progress.md
  git commit -m "docs(agent): record first-user blocker remediation evidence"
  ```

## Final Track Gate

After Task 6, run one final blocking review against the design's completion
criteria and the exact commit series. The reviewer checks only Critical, High,
and acceptance-blocking findings, verifies no version was released without a
fresh post-rollout valid output, and confirms every live claim has an
ID/digest/log reference. At three review rounds, record any remaining blocker
and stop; do not weaken a budget, validation, or sealing boundary to declare
the track complete.
