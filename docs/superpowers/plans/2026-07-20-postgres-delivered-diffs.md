# Postgres-Backed Delivered Diffs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist every successfully pushed ticket delivery's bounded `base_sha..pushed_sha` review diff in Postgres and serve it without configuring Git storage on the web node.

**Architecture:** A new leaf package, `agent/deliverydiff`, owns the versioned flight artifact, bounds, validation, and real-Git capture. Harvest writes `flight/diff.json` only after a successful push; `exec.HarvestStep` ingests it after the ticket transition through a new DB-backed store. The existing diff handler reads the newest stored attempt first and uses the outcome mirror only as a compatibility fallback for historical tickets.

**Tech Stack:** Go, PostgreSQL/JSONB, Ginkgo/Gomega for ATC DB and exec suites, standard `testing` plus real Git fixtures for the leaf package and harvest runner.

## Global Constraints

- Execute from a clean feature branch based on `main` at or after `2cc4c3b675` (`v0.2.205`); do not implement against the older `integration/audit-all` code snapshot.
- Preserve the public `gitcheck.DiffPage` JSON fields and the existing Elm decoder/rendering contract.
- The review path must work with every `--agent-outcome-*` flag unset.
- Outcome detection and human-touch computation remain unchanged and optional.
- Diff capture is bounded to 200 files, 64 KiB per file, and 2 MiB total stored patch text.
- Missing, invalid, or unpersisted diff evidence never changes a successfully pushed build result.
- Cross-aggregate identifiers use no SQL foreign keys.
- PostgreSQL must be ready before DB suites (`pg_isready`); do not run tests with `--race`.
- Migration `1773106095` follows the `1773106092`–`1773106094` migrations already on `main`; update both migration-head constants to `1773106095`.

---

## File Structure

| File | Responsibility |
|---|---|
| `agent/deliverydiff/diff.go` | Artifact/domain types, limits, validation, paging, and real-Git capture. |
| `agent/deliverydiff/diff_test.go` | Real-Git capture, truncation, validation, and paging tests. |
| `agent/harvest/runner.go` | Capture and write `diff.json` after a successful push; expose a diagnostic on capture failure. |
| `agent/harvest/flight.go` | Return errors from flight JSON writes so diff-write failures are observable. |
| `agent/harvest/runner_test.go` / `flight_test.go` | Runner emission and flight-write behavior. |
| `atc/db/migration/migrations/1773106095_create_agent_delivery_diffs.{up,down}.sql` | Durable delivery-diff schema and ticket/latest index. |
| `atc/db/agent_delivery_diff_factory.go` | Store implementation: idempotent upsert and latest-by-ticket read. |
| `atc/db/agent_delivery_diff_factory_test.go` | PostgreSQL round-trip, multiple attempts, and latest selection. |
| `agent/deliverydiff/deliverydifffakes/fake_store.go` | Generated store fake used by exec tests. |
| `atc/db/migration/legacy_upgrade_test.go` / `docs/migration/migrate-preflight.sh` | Migration head `1773106095`. |
| `atc/exec/harvest_step.go` | Successful-delivery ingestion with server-pinned identity and SHA validation. |
| `atc/exec/harvest_step_test.go` | Ingestion trust, invalid artifact, and non-fatal behavior specs. |
| `atc/engine/step_factory.go` | Thread the delivery-diff store into harvest steps. |
| `agent/api/outcomes/diff_handler.go` | Stored-first read path with legacy mirror fallback. |
| `agent/api/outcomes/diff_handler_test.go` | Stored-first, fallback, paging, and 404 tests. |
| `atc/api/handler.go` / `atc/atccmd/command.go` | Construct the DB store for API and engine; narrow Git-dir configuration to outcomes. |

---

### Task 1: Delivery-diff domain, capture, validation, and paging

**Files:**
- Create: `agent/deliverydiff/diff.go`
- Create: `agent/deliverydiff/diff_test.go`

**Interfaces:**
- Produces: `Artifact`, `DeliveryDiff`, `Store`, `Capture`, `Artifact.Validate`, and `DeliveryDiff.Page`.
- Consumed by: harvest runner, DB factory, exec ingestion, and outcomes diff handler.

- [ ] **Step 1: Write failing real-Git tests**

Create a temporary bare origin and checkout using the fixture style in `agent/harvest/workspace_test.go`. Cover:

```go
func TestCapture(t *testing.T) {
    repo, base, head := fixtureRepo(t)
    got, err := deliverydiff.Capture(repo, base, head, "agent/ticket-42")
    if err != nil { t.Fatal(err) }
    if got.SchemaVersion != deliverydiff.SchemaVersion { t.Fatalf("schema = %q", got.SchemaVersion) }
    if got.BaseSHA != base || got.PushedSHA != head { t.Fatalf("shas = %s..%s", got.BaseSHA, got.PushedSHA) }
    if got.TotalFiles != 2 || got.CapturedFiles != 2 { t.Fatalf("counts = %d/%d", got.CapturedFiles, got.TotalFiles) }
    if got.Files[0].Path != "a.txt" || got.Files[1].Path != "nested/b.txt" { t.Fatalf("order = %#v", got.Files) }
}

func TestCaptureBoundsAndPage(t *testing.T) {
    repo, base, head := oversizedFixtureRepo(t)
    got, err := deliverydiff.Capture(repo, base, head, "agent/ticket-42")
    if err != nil { t.Fatal(err) }
    if !got.Truncated || got.CapturedFiles > deliverydiff.MaxFiles || got.ByteLen > deliverydiff.MaxTotalPatchBytes {
        t.Fatalf("bounds not enforced: %#v", got)
    }
    for _, file := range got.Files {
        if len(file.Patch) > deliverydiff.MaxPatchBytesPerFile { t.Fatalf("%s too large", file.Path) }
    }
    row := deliverydiff.DeliveryDiff{Files: got.Files, TotalFiles: got.TotalFiles, CapturedFiles: got.CapturedFiles, Truncated: got.Truncated}
    page := row.Page(0, 50)
    if page.Limit != 50 || page.TotalFiles != got.TotalFiles || page.HasMore != (got.TotalFiles > len(page.Files)) {
        t.Fatalf("page = %#v", page)
    }
}
```

Also test malformed SHA/branch values, count mismatch, byte-length mismatch, over-limit files, negative paging, and an offset past the captured list.

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./agent/deliverydiff/`

Expected: FAIL because the package and symbols do not exist.

- [ ] **Step 3: Implement the domain and store contract**

Create `agent/deliverydiff/diff.go` with these public shapes:

```go
package deliverydiff

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

import "github.com/concourse/concourse/agent/gitcheck"

const (
    SchemaVersion        = "delivery-diff/1"
    MaxFiles             = 200
    MaxPatchBytesPerFile = 64 << 10
    MaxTotalPatchBytes   = 2 << 20
)

type Artifact struct {
    SchemaVersion   string              `json:"schema_version"`
    BaseSHA         string              `json:"base_sha"`
    PushedSHA       string              `json:"pushed_sha"`
    DeliveredBranch string              `json:"delivered_branch"`
    Files           []gitcheck.DiffFile `json:"files"`
    TotalFiles      int                 `json:"total_files"`
    CapturedFiles   int                 `json:"captured_files"`
    ByteLen         int                 `json:"byte_len"`
    Truncated       bool                `json:"truncated"`
}

type DeliveryDiff struct {
    BuildID, TicketID, PipelineRunID int
    PlanID, Repo, TargetBranch, DeliveredBranch string
    BaseSHA, PushedSHA string
    Files []gitcheck.DiffFile
    TotalFiles, CapturedFiles, ByteLen int
    Truncated bool
}

//counterfeiter:generate . Store
type Store interface {
    Upsert(DeliveryDiff) error
    GetLatest(ticketID int) (DeliveryDiff, bool, error)
}
```

Implement `Validate` so every stored patch is within the constants, `CapturedFiles == len(Files)`, `ByteLen` equals the sum of stored patch bytes, `TotalFiles >= CapturedFiles`, required strings are non-empty, and `Truncated` is true whenever any file is truncated or not every changed file was captured.

Implement `Page(offset, limit int)` with defaults `offset=0`, `limit=50`, maximum `limit=200`, and `HasMore` based on the complete `TotalFiles`, not only the captured list.

- [ ] **Step 4: Implement deterministic real-Git capture**

Use `git diff --name-only -z <base>..<pushed>` to obtain path-safe deterministic ordering, then `git diff <base>..<pushed> -- <path>` for each captured file. Include the marker inside each byte cap:

```go
const truncatedMarker = "\n... [diff truncated]\n"

func boundedPatch(raw []byte, limit int) (string, bool) {
    if len(raw) <= limit { return string(raw), false }
    keep := limit - len(truncatedMarker)
    if keep < 0 { keep = 0 }
    return string(raw[:keep]) + truncatedMarker, true
}
```

Stop when the file or aggregate cap is reached, retain the complete `TotalFiles`, and validate the assembled artifact before returning it.

- [ ] **Step 5: Run tests and commit**

Run: `go test ./agent/deliverydiff/`

Expected: PASS.

Commit:

```bash
git add agent/deliverydiff
git commit -m "feat(agent): capture bounded delivered diffs"
```

---

### Task 2: Harvest emits `flight/diff.json` after a successful push

**Files:**
- Modify: `agent/harvest/flight.go`
- Modify: `agent/harvest/flight_test.go`
- Modify: `agent/harvest/runner.go`
- Modify: `agent/harvest/runner_test.go`

**Interfaces:**
- Consumes: `deliverydiff.Capture` and `deliverydiff.Artifact` from Task 1.
- Produces: versioned `flight/diff.json`; `results.metadata.diff_error` only on capture/write failure.

- [ ] **Step 1: Write failing runner tests**

Extend the successful-push real-Git runner fixture to read `diff.json` and assert its SHAs, branch, ordered files, and patch content. Add cases proving no artifact for `Push=false`, gate failure, dirty/no-op workspace, and failed push. Add a recorder test where the flight path cannot be written and assert the error is returned.

- [ ] **Step 2: Run the focused tests and verify failure**

Run: `go test ./agent/harvest/ -run 'Test.*(Diff|Flight|Push)'`

Expected: FAIL because `diff.json` is not written and `writeJSON` returns no error.

- [ ] **Step 3: Make flight writes observable**

Change the recorder method to:

```go
func (r *flightRecorder) writeJSON(name string, v any) error {
    if r == nil { return nil }
    data, err := json.MarshalIndent(v, "", "  ")
    if err != nil { return err }
    return os.WriteFile(filepath.Join(r.dir, name), append(data, '\n'), 0o644)
}
```

Update existing best-effort callers to `_ = rec.writeJSON(...)` so their behavior is unchanged.

- [ ] **Step 4: Capture only after the push succeeds**

After `facts.PushedBranch = cfg.Branch`, call `deliverydiff.Capture(workspaceDir, facts.BaseSHA, head, cfg.Branch)`. On success, write `diff.json`; on failure, set `facts.DiffErr`. Add `diff_error` to `runFacts.metadata` when non-empty. Do not change the exit status.

- [ ] **Step 5: Run tests and commit**

Run: `go test ./agent/harvest/`

Expected: PASS.

Commit:

```bash
git add agent/harvest
git commit -m "feat(harvest): emit delivered diff evidence"
```

---

### Task 3: Postgres schema and delivery-diff factory

**Files:**
- Create: `atc/db/migration/migrations/1773106095_create_agent_delivery_diffs.up.sql`
- Create: `atc/db/migration/migrations/1773106095_create_agent_delivery_diffs.down.sql`
- Modify: `atc/db/migration/legacy_upgrade_test.go`
- Modify: `docs/migration/migrate-preflight.sh`
- Create: `atc/db/agent_delivery_diff_factory.go`
- Create: `atc/db/agent_delivery_diff_factory_test.go`
- Create: `agent/deliverydiff/deliverydifffakes/fake_store.go` (generated)

**Interfaces:**
- Implements: `deliverydiff.Store`.
- Produces: `db.NewAgentDeliveryDiffFactory(DbConn)`.

- [ ] **Step 1: Verify migration preconditions and PostgreSQL**

Run:

```bash
pg_isready
ls atc/db/migration/migrations/177310609*.sql
```

Expected: PostgreSQL accepts connections; `1773106092`, `1773106093`, and `1773106094` exist; `1773106095` does not.

- [ ] **Step 2: Write the migration and failing DB specs**

Use the approved schema verbatim. The down migration is:

```sql
DROP TABLE agent_delivery_diffs;
```

Set both `jetbridgeHeadMigration` and `JETBRIDGE_VERSION` to `1773106095`.

Write Ginkgo specs that upsert attempt build 100, idempotently replace build 100/plan `h1`, insert build 101, and assert `GetLatest(ticketID)` returns build 101 with all metadata and files intact. Assert an unknown ticket returns `(zero, false, nil)`.

- [ ] **Step 3: Run DB tests and verify factory failure**

Run: `ginkgo --focus='AgentDeliveryDiff' ./atc/db/`

Expected: compile failure because the factory does not exist.

- [ ] **Step 4: Implement the factory**

Marshal `DeliveryDiff.Files` to JSON and upsert all mutable columns on `(build_id, plan_id)`. Read the newest row with:

```sql
SELECT build_id, plan_id, ticket_id, pipeline_run_id, repo, target_branch,
       delivered_branch, base_sha, pushed_sha, diff, total_files,
       captured_files, byte_len, truncated
FROM agent_delivery_diffs
WHERE ticket_id = $1
ORDER BY build_id DESC, plan_id DESC
LIMIT 1
```

Unmarshal the `diff` JSON into `[]gitcheck.DiffFile` and return `found=false` on `sql.ErrNoRows`.

- [ ] **Step 5: Generate the fake**

Run:

```bash
go generate ./agent/deliverydiff
```

Expected: `agent/deliverydiff/deliverydifffakes/fake_store.go` is generated. Do not hand-edit generated methods.

- [ ] **Step 6: Verify and commit**

Run:

```bash
ginkgo ./atc/db/migration/
ginkgo --focus='AgentDeliveryDiff' ./atc/db/
```

Expected: PASS.

Commit:

```bash
git add agent/deliverydiff atc/db docs/migration/migrate-preflight.sh
git commit -m "feat(db): persist delivered ticket diffs"
```

---

### Task 4: Ingest successful delivery diffs and wire the engine

**Files:**
- Modify: `atc/exec/harvest_step.go`
- Modify: `atc/exec/harvest_step_test.go`
- Modify: `atc/engine/step_factory.go`
- Modify: `atc/atccmd/command.go`

**Interfaces:**
- Consumes: `deliverydiff.Store`, `Artifact.Validate`, and DB factory from Tasks 1 and 3.
- Produces: `WithHarvestDeliveryDiffStore` and `WithAgentDeliveryDiffStore` options.

- [ ] **Step 1: Write failing exec specs**

Using the existing fake worker/streamer fixture, add `diff.json` to the flight artifact and assert a successful exit-0 push upserts one row containing:

```go
deliverydiff.DeliveryDiff{
    BuildID: 1234, PlanID: "h1", TicketID: 42, PipelineRunID: 7,
    Repo: "tdmtrader/concourse", TargetBranch: "main",
    DeliveredBranch: "agent/ticket-42", BaseSHA: "def456", PushedSHA: "abc123",
}
```

Add specs for SHA mismatch, branch mismatch, malformed JSON, over-limit patch, unverified ticket, exit 1, and nil store. In every rejection case, assert the step's pre-existing result and ticket transition are unchanged.

- [ ] **Step 2: Run and verify failure**

Run: `ginkgo --focus='delivery diff' ./atc/exec/`

Expected: compile failure because the option/store field does not exist.

- [ ] **Step 3: Implement post-transition ingestion**

Add `deliveryDiffStore deliverydiff.Store` to `HarvestStep` and:

```go
func WithHarvestDeliveryDiffStore(s deliverydiff.Store) HarvestStepOption {
    return func(h *HarvestStep) { h.deliveryDiffStore = s }
}
```

On exit 0 with a non-empty pushed branch, preserve the existing order:

```go
step.transitionTicket(ctx, logger, "needs_review", branch, "")
step.seedOutcome(logger, results, branch)
step.ingestDeliveryDiff(ctx, logger, chosenWorker, volumeMounts, results, branch)
```

`ingestDeliveryDiff` must use a fresh 30-second `context.WithoutCancel` timeout, locate the existing flight artifact, read at most 5 MiB, validate the artifact, compare its SHAs and branch to validated results metadata and the plan, obtain ticket/run IDs only through `verifiedIDs`, and then upsert the server-pinned row. Log and return on every failure.

- [ ] **Step 4: Wire the core step factory and command engine**

Add `agentDeliveryDiffStore deliverydiff.Store` to `coreStepFactory`, a `WithAgentDeliveryDiffStore` option, and append `WithHarvestDeliveryDiffStore` only for harvest steps. In `constructEngine`, pass `db.NewAgentDeliveryDiffFactory(dbConn)` beside the outcomes and transcript stores.

- [ ] **Step 5: Verify and commit**

Run:

```bash
ginkgo ./atc/exec/
go test ./atc/engine/...
```

Expected: PASS.

Commit:

```bash
git add atc/exec atc/engine atc/atccmd/command.go
git commit -m "feat(exec): ingest delivered diff evidence"
```

---

### Task 5: Serve stored diffs first and decouple the outcome flag

**Files:**
- Modify: `agent/api/outcomes/diff_handler.go`
- Modify: `agent/api/outcomes/diff_handler_test.go`
- Modify: `atc/api/handler.go`
- Modify: `atc/atccmd/command.go`

**Interfaces:**
- Consumes: `deliverydiff.Store.GetLatest`.
- Preserves: `GET /api/v1/agent/tickets/:ticket_id/diff` response and legacy `MirrorProvider` fallback.

- [ ] **Step 1: Write failing handler tests**

Add these cases:

1. stored diff returns 200 with `provider=nil`;
2. stored diff wins when the mirror contains different content;
3. missing stored diff uses the mirror and outcome SHAs;
4. missing both remains 404;
5. stored paging honors offset/default/capped limit and reports the complete total;
6. DB read error returns 500 without consulting the mirror.

- [ ] **Step 2: Run and verify failure**

Run: `go test ./agent/api/outcomes/ -run Diff`

Expected: compile failure because `NewDiffHandler` has no delivery store parameter.

- [ ] **Step 3: Implement stored-first behavior**

Change construction to:

```go
func NewDiffHandler(outcomes Store, diffs deliverydiff.Store, fallback MirrorProvider) *DiffHandler
```

After parsing the ticket and window, call `diffs.GetLatest(id)` when the store is non-nil. Return `stored.Page(offset, limit)` when found. Only then read `agent_outcomes` and call the mirror fallback. A nil fallback now means “no historical fallback,” not “diff API disabled.”

- [ ] **Step 4: Wire the API and narrow configuration semantics**

Add a `deliveryDiffStore deliverydiff.Store` parameter to `atc/api.NewHandler`, construct `outcomesapi.NewDiffHandler(outcomesStore, deliveryDiffStore, outcomeDiffProvider)`, and pass `db.NewAgentDeliveryDiffFactory(dbConn)` from `constructAPIHandler`.

Change the flag description to:

```go
description:"Directory for the optional outcome watcher's bare Git mirrors. Empty disables automated Git-based outcome detection; delivered ticket diffs remain available from Postgres."
```

Update comments on `agentOutcomeMirrors`, `agentOutcomeDiffProvider`, and `MirrorCache` to call the diff use a historical compatibility fallback rather than a master switch.

- [ ] **Step 5: Run route/API tests and commit**

Run:

```bash
go test ./agent/api/outcomes/
ginkgo ./atc/api/ ./atc/wrappa/
```

Expected: PASS.

Commit:

```bash
git add agent/api/outcomes agent/outcomewatcher atc/api atc/atccmd/command.go
git commit -m "feat(agent): serve delivered diffs without Git mirrors"
```

---

### Task 6: Full verification and design-contract update

**Files:**
- Modify: `docs/superpowers/plans/agentic-platform/00-shared-contracts.md`
- Modify: `docs/superpowers/plans/agentic-platform/12-delivery-outcomes.md`
- Modify: `docs/superpowers/plans/agentic-platform/remainders/2026-07-17-delivery-outcomes.md`

**Interfaces:**
- Documents the superseding decision; no runtime interface changes.

- [ ] **Step 1: Amend the frozen contract additively**

Append a dated 2026-07-20 amendment stating:

```markdown
Delivered review diffs are captured by harvest and persisted in `agent_delivery_diffs`; `GetAgentTicketDiff` reads the newest successful delivery from Postgres. `--agent-outcome-git-dir` controls only Git-based outcome reconciliation. A configured mirror is a compatibility fallback for historical tickets without stored delivery evidence. This supersedes §1.11.1's “master switch” coupling without changing merge-detection heuristics or the public DiffPage wire contract.
```

Do not rewrite the historical plan text as though the original decision never existed; mark it superseded and link the approved design spec.

- [ ] **Step 2: Run formatting and focused suites**

Run:

```bash
gofmt -w agent/deliverydiff/diff.go agent/deliverydiff/diff_test.go agent/harvest/flight.go agent/harvest/flight_test.go agent/harvest/runner.go agent/harvest/runner_test.go atc/db/agent_delivery_diff_factory.go atc/db/agent_delivery_diff_factory_test.go atc/exec/harvest_step.go atc/exec/harvest_step_test.go atc/engine/step_factory.go agent/api/outcomes/diff_handler.go agent/api/outcomes/diff_handler_test.go atc/api/handler.go atc/atccmd/command.go
go test ./agent/deliverydiff/ ./agent/harvest/ ./agent/api/outcomes/
ginkgo ./atc/db/migration/ ./atc/db/ ./atc/exec/ ./atc/api/ ./atc/wrappa/
go test ./atc/engine/...
git diff --check
```

Expected: all PASS and no whitespace errors.

- [ ] **Step 3: Run repository verification**

Run:

```bash
pg_isready
make test-quick
make test-fly-integration
```

Expected: PostgreSQL ready; all unit, ci-agent, and fly integration suites PASS.

- [ ] **Step 4: Commit documentation and any verification-only fixes**

```bash
git add docs/superpowers/plans/agentic-platform
git commit -m "docs(agentic): decouple delivered diffs from outcomes"
```

- [ ] **Step 5: Record live verification as a deployment follow-up**

Do not mutate an external deployment as part of this code task. Hand off these exact checks:

1. deploy with `--agent-outcome-git-dir` absent;
2. dispatch and successfully harvest one ticket;
3. verify the ticket diff survives a web restart;
4. push a human commit and verify the stored diff is unchanged;
5. confirm the external compare link reflects current branch state.

---

## Plan Self-Review

- **Spec coverage:** capture, bounds, per-attempt persistence, server-pinned ingestion, stored-first API, historical fallback, configuration decoupling, retention stance, and deferred outcome work each map to a task above.
- **Migration consistency:** this plan targets current `main`, where migrations `1773106092`–`1773106094` exist even though the two documented head constants still read `1773106090`; Task 3 advances both directly to `1773106095`, covering the intervening migrations.
- **Type consistency:** `deliverydiff.Artifact` is the flight wire; `deliverydiff.DeliveryDiff` is the trusted persisted/read model; `deliverydiff.Store` is implemented by `db.AgentDeliveryDiffFactory` and consumed by exec/API.
- **Trust consistency:** artifact SHAs and branch are checked against validated results/plan; ticket, run, repo, and target branch are server-pinned.
- **Compatibility:** the Elm page and `gitcheck.DiffPage` JSON remain unchanged; only the handler's internal source changes.
