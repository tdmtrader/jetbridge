# Agent-Delivered Diff Storage Design

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../plans/2026-07-21-agentic-functions-program.md) are authoritative. This diff-storage design targeted the ticket review model; diffs are now recorded against workflow-run outcomes under the snapshot layer — see the current delivery-outcomes shape.

- **Date:** 2026-07-20
- **Status:** Proposed for implementation
- **Scope:** Make JetBridge's ticket review diff work without a Git mirror or local Git directory on the web node. Automated merge detection and human-touch outcome capture are deliberately deferred.

## Problem

The ticket page presents `base_sha..pushed_sha` as its primary in-app review surface, but the corresponding API is enabled only when `--agent-outcome-git-dir` configures a bare-mirror cache in the web process. This couples a core review capability to an optional post-delivery outcome watcher and makes the otherwise stateless web node own Git credentials, network fetches, and local repository state.

The detailed delivery-outcomes plan made that coupling intentionally so one mirror could answer both diff and merge-detection queries. The later UX4 work promoted the diff to the primary review surface without revisiting the mirror's off-by-default deployment contract. This design separates the two concerns while preserving the shipped API and UI semantics.

## Decision

The in-app diff is an immutable record of what harvest delivered: the patch from the merge base of the target branch (`base_sha`) to the exact commit pushed by harvest (`pushed_sha`). It is not a live view of later human commits on the agent branch.

Harvest will compute a bounded, structured review diff while its worker container has the authoritative committed checkout. It will write that diff to the existing flight output. `exec.HarvestStep` will validate and persist the artifact in Postgres using server-verified ticket/run identity. `GET /api/v1/agent/tickets/:ticket_id/diff` will read Postgres instead of requiring the outcome watcher's mirror.

The external forge comparison link remains the route to current branch state. The outcome watcher may continue to use a Git mirror when explicitly enabled, but its configuration will no longer control the diff API.

## Goals

- The ticket's delivered diff works with `--agent-outcome-git-dir` unset.
- Web replicas remain stateless with respect to Git.
- The existing `gitcheck.DiffPage` JSON contract and Elm renderer remain compatible.
- Every successful re-delivery is retained as a separate attempt; the ticket endpoint returns the newest successful delivery.
- Diff storage is explicitly bounded and reports truncation rather than silently dropping content.
- Diff capture and ingestion never expose repository credentials to the web process.
- Existing tickets can use the configured mirror as a temporary compatibility fallback, but no backfill is required.

## Non-goals

- Live tracking of commits pushed after harvest.
- Merge, squash-merge, branch-deletion, or closed-unmerged detection.
- Human-touch delta computation.
- Reconstructing a repository or preserving commit metadata; the stored patch is a review projection, not a Git bundle.
- Automatic backfill of historical tickets.
- Changing the existing Elm diff presentation or adding in-app pagination controls.

## Data model

Add an `agent_delivery_diffs` table. Migration numbering is selected from the actual target branch immediately before implementation; the design does not reserve a stale number.

```sql
CREATE TABLE agent_delivery_diffs (
    build_id          INTEGER     NOT NULL,
    plan_id           TEXT        NOT NULL,
    ticket_id         INTEGER     NOT NULL,
    pipeline_run_id   INTEGER,
    repo              TEXT        NOT NULL,
    target_branch     TEXT        NOT NULL,
    delivered_branch  TEXT        NOT NULL,
    base_sha          TEXT        NOT NULL,
    pushed_sha        TEXT        NOT NULL,
    diff              JSONB       NOT NULL,
    total_files       INTEGER     NOT NULL,
    captured_files    INTEGER     NOT NULL,
    byte_len          INTEGER     NOT NULL,
    truncated         BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (build_id, plan_id)
);

CREATE INDEX agent_delivery_diffs_ticket
    ON agent_delivery_diffs (ticket_id, build_id DESC);
```

Cross-aggregate identifiers deliberately have no foreign keys, matching the existing agent-table convention: build and pipeline-run rows may be reaped while review evidence remains.

`diff` stores the captured file list as JSON. Counts, bounds, identity, and truncation state remain in relational columns so ingestion can validate them without trusting duplicated JSON metadata. The public endpoint assembles and returns the existing fields:

```json
{
  "files": [{"path": "atc/foo.go", "patch": "...", "truncated": false}],
  "offset": 0,
  "limit": 50,
  "total_files": 1,
  "has_more": false
}
```

The relational SHA and identity columns are authoritative and queryable; the JSON document is the bounded presentation payload.

## Capture contract

After gates pass and the branch push succeeds, harvest computes `base_sha..pushed_sha` from the same committed workspace it pushed. The advisory judge's verdict does not control capture or delivery. Harvest writes `flight/diff.json` alongside `results.json`, `manifest.json`, and `review.json`.

The artifact has a versioned envelope:

```json
{
  "schema_version": "delivery-diff/1",
  "base_sha": "...",
  "pushed_sha": "...",
  "delivered_branch": "agent/ticket-42",
  "files": [{"path": "atc/foo.go", "patch": "...", "truncated": false}],
  "total_files": 1,
  "captured_files": 1,
  "byte_len": 1234,
  "truncated": false
}
```

The artifact uses these fixed bounds:

- at most 200 captured files;
- at most 64 KiB of patch text per file, preserving the existing API cap;
- at most 2 MiB of patch text across the delivery;
- deterministic path ordering from Git;
- explicit `truncated` flags at file and delivery level;
- `total_files` records the complete changed-file count even when fewer files are captured.

The patch is generated for review, using the same ordinary unified-diff semantics as the existing mirror-backed endpoint. It is not required to be apply-able and does not promise complete binary content.

No row is produced for a failed gate, failed push, dirty workspace, or no-op delivery. Those attempts remain visible through their run metrics, transcript, and build logs, but they did not produce a delivered branch for review.

If diff generation fails after a successful push, harvest records a visible diagnostic in its results metadata and log, while the delivery itself remains successful. The ticket page retains the external comparison link. Diff generation failure must not relabel successfully pushed code as an agent or platform failure.

## Server-side ingestion

`exec.HarvestStep` receives an optional delivery-diff store through the existing step-factory option pattern. Flight ingestion reads `diff.json` with a hard 5 MiB input limit and validates:

- the results document is a successful pushed delivery;
- `base_sha` and `pushed_sha` are non-empty and exactly match `results.json` metadata;
- the delivered branch matches the harvest plan;
- file counts and patch sizes satisfy the capture bounds;
- ticket and pipeline-run identity come from `verifiedIDs`, never pod-written fields;
- repo and target branch come from the server-side plan.

The upsert key `(build_id, plan_id)` makes restart/resume ingestion idempotent. A later ticket attempt inserts a new row rather than overwriting earlier evidence. Ingestion errors are logged and do not alter the build result, consistent with metrics, transcript, and evidence ingestion.

Although patch contents are produced inside the harvest pod, the API renders every line as escaped text. The server does not interpret patch HTML, paths, or terminal control sequences as markup.

## Read path

Introduce a delivery-diff store interface with:

```go
type Store interface {
    Upsert(DeliveryDiff) error
    GetLatest(ticketID int) (DeliveryDiff, bool, error)
}
```

`GetLatest` selects the newest row by `build_id DESC`. Only successful pushed deliveries enter the table, so the newest row is the ticket's current delivered review artifact after a send-back/re-dispatch cycle.

The existing diff handler will:

1. validate `ticket_id`, `offset`, and `limit` as today;
2. read the newest stored delivery;
3. slice the captured file list for the requested window and return `gitcheck.DiffPage`;
4. if no stored row exists and a legacy mirror provider is configured, use the existing mirror path;
5. otherwise return the existing not-found response.

The compatibility fallback allows pre-migration tickets to remain reviewable for deployments that already enabled the mirror. New deliveries do not depend on it. After an agreed retention window, the fallback can be removed separately.

The Elm client needs no wire or rendering change. Its existing polling naturally discovers the diff shortly after harvest ingestion completes.

## Configuration changes

`--agent-outcome-git-dir` becomes an outcome-watcher switch only. Its help text and code comments must stop describing it as the diff API master switch.

The API always receives a Postgres delivery-diff store. The optional mirror provider is passed only as a legacy fallback and to the independently enabled outcome watcher. No new web-node filesystem, volume, token, or URL-template setting is required for review.

## Retention and lifecycle

Delivered diffs are durable review/audit evidence, like transcripts and reviews. Initial implementation retains all attempts. This is acceptable for the current small-team deployment because every artifact is bounded.

Automatic retention, archival, or compression is deferred until observed database size justifies it. Any later retention policy must operate at the whole-attempt level and retain the latest delivery for every non-terminal or recently terminal ticket.

## Failure and compatibility behavior

- New deployment with no outcome Git flag: new successful deliveries have in-app diffs.
- Existing deployment with the mirror enabled: stored diff is preferred; mirror serves historical rows without stored artifacts.
- Historical ticket without stored diff and no mirror: the endpoint remains 404 and the forge link remains visible.
- Stored artifact malformed or over bounds: reject ingestion, log the exact reason, preserve the successful delivery, and fall back as above.
- Postgres write failure: log it; do not fail or retry the push. Restart/resume may re-ingest the artifact idempotently if the flight volume remains available.
- Human commits after delivery: do not mutate the stored diff. The forge link shows current state.

## Testing strategy

1. **Harvest unit and real-Git tests**
   - successful push writes the exact bounded `base_sha..pushed_sha` artifact;
   - deterministic file ordering;
   - per-file, aggregate-byte, and file-count truncation;
   - failed/no-op/unpushed runs write no delivered diff.

2. **Database tests**
   - migration up/down and migration-head consistency;
   - idempotent upsert for one build/plan;
   - multiple attempts retained;
   - latest attempt selected by build ID;
   - JSON and relational metadata round-trip.

3. **Exec ingestion tests**
   - verified identities and server-pinned repo/branch values;
   - SHA mismatch, oversized artifact, malformed JSON, and unverified ticket rejected;
   - successful ingestion is non-fatal and idempotent;
   - absent optional artifact does not affect existing metrics/review ingestion.

4. **Handler tests**
   - stored diff works with a nil mirror provider;
   - stored data wins when both sources exist;
   - historical ticket falls back to the mirror;
   - no row/no mirror remains 404;
   - offset/default/capped-limit behavior remains compatible.

5. **Integration verification**
   - run the relevant Go suites and Postgres-backed database suites;
   - deploy with `--agent-outcome-git-dir` absent;
   - dispatch a ticket, let harvest push it, and verify the ticket page renders the delivered diff;
   - restart the web pod and verify the diff remains available;
   - push a human commit and verify the stored in-app diff remains unchanged while the forge comparison reflects current state.

## Deferred outcome-capture decision

This change does not endorse or remove the current Git-mirror outcome watcher. Merge detection and human-touch metrics need a separate design decision among forge events/APIs, worker-managed reconciliation, or a dedicated Git mirror service. That decision must not reintroduce an outcome-storage prerequisite into the review path.

## Acceptance criteria

- A successful ticket delivery renders an in-app diff when every `--agent-outcome-*` flag is unset.
- The web pod requires no writable Git directory and no repository-read credential for the review path.
- The diff remains available across web restarts and after the source branch is merged or deleted.
- A send-back followed by a new successful delivery shows the new diff while retaining the prior attempt in Postgres.
- Existing clients decode the response unchanged.
- Outcome capture can remain disabled without degrading ticket creation, execution, transcript viewing, step DAGs, workflow pages, or delivered-diff review.
