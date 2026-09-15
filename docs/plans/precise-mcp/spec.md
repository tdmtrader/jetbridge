# Precise MCP tools and capability explanations

## Overview

JetBridge's authentication is shipped, but MCP currently exposes only
`pipeline_status`. The owner selected precise resource groups after a local
six-shape benchmark, and requested executable tools restricted to entitlement,
with useful explanations when an action is unavailable.

Deliver a first operational slice that supports finding a pipeline, diagnosing a
failed build, applying supplied configuration, and triggering/stopping a build.
Agents should know whether they need new connection consent, an administrator,
or a capability that this MCP deployment has not implemented. Metadata must not
be mistaken for executable access or a guarantee about an unspecified target.

Baseline: freshly fetched `origin/core`, `62afc93a6b5ef371bc36d65a8c0b4acf800ba2b3`.
This continues the completed auth proposal without changing its released tracks.
The owner requested **specification and planning** in this pass; implementation,
commits and deployment are not part of this planning delivery.

## Requirements

### R1 — First operational slice and compatibility

Expose precise operation branches within `pipeline`, `build` and `job` tools.
Use stable canonical operation IDs in `request.operation`, with exact arguments
in `request.arguments`. Keep the shipped `pipeline_status(team, pipeline)` tool
compatible, including its fields, bounds and consent behavior.

| Tool | Operation | Required consent | User-visible behavior |
|---|---|---|---|
| pipeline | pipelines_list | read | Search visible pipelines by optional team/name text; return complete identities in bounded pages. |
| pipeline | pipeline_get | read | Read exact pipeline identity and state. |
| pipeline | pipeline_config_get | read | Read configuration and its version. |
| pipeline | pipeline_config_set | pipelines:write | Apply supplied configuration with a required atomic version precondition. |
| pipeline | pipeline_pause / pipeline_unpause | pipelines:write | Change scheduling state; authorize the two actions independently. |
| build | builds_list | read | List builds within an exact pipeline, with optional job/status filters applied before pagination. |
| build | build_get | read | Read metadata for a numeric build ID. |
| build | build_logs_read | read | Read a bounded, resumable page of build events/output. |
| build | build_abort | builds:write | Request abort of the exact build; acknowledge acceptance, not completed termination. |
| job | job_trigger | builds:write | Trigger an existing configured job once and return its build receipt. |

Pipeline identity is `{team, pipeline, instance_vars}`; `{}` selects the
non-instanced pipeline. New grouped operations require the complete identity
when selecting a pipeline. The compatibility alias keeps its current meaning.
Build listing in this slice requires an exact pipeline; team-only or global
build listing is not advertised.

### R2 — Executable catalogs reflect current eligibility

Only implemented, enabled operations with the connection's exact consent and no
provable account-wide exclusion appear as executable schema branches. Remove
forbidden alternatives from the precise union and omit groups with no remaining
operations. Derive descriptions and effect annotations from the remaining branches.

Preserve the five independent categories. `admin` is not a wildcard. Pipeline
writes do not authorize reads or configured-job triggers; build writes do not
authorize configuration-capable runs; hijack does not imply read.

Account eligibility must use existing custom roles, role inheritance and explicit
account-admin rules. Public reads can remain usable without team membership.
Do not claim to evaluate target/payload policy without the target/payload. Catalog
presence means an operation may be used on permitted targets, not that every
argument accepted by its schema is authorized.

### R3 — Every call preserves authorization and useful failures

Recheck the chosen operation's consent and all existing API target, custom-role,
policy, archive and audit behavior on every execution. Listing, descriptions and
schema caching confer no authority. Invalid/revoked grants fail authentication
before argument details. A recognized implemented operation attempted without
consent returns the precise existing OAuth scope challenge even if its branch is
absent from the current schema. Wrong-group, unknown, unimplemented and malformed
operations never gain accidental execution or misleading consent requests.
For grouped calls, validate the envelope, resource/discriminator pairing and
arguments against the complete declared branch schema before a scope challenge;
validation must not look up a target. The compatibility alias retains its shipped
preflight behavior.

Account-pruned operations return a safe account-access failure; hidden and
nonexistent targets retain indistinguishable unavailable/not-permitted responses.
Do not explain denials by probing target existence. Role freshness remains the
existing API's documented cache behavior; MCP grant revocation remains immediate.

### R4 — Capabilities explain why an action is unavailable

Expose one non-executing `capabilities_explain` tool to every valid authenticated
grant, including grants without application `read`. Browse a resource family,
optionally select an exact canonical operation, and return at most 20 compact
metadata entries plus a continuation cursor. No `include_unavailable` switch,
semantic search service or generic executor is required.

Each known entry distinguishes MCP support in this deployment, missing consent,
whether an executable branch is present, known account exclusions versus
target-dependent checks, and a useful next step. It returns no executable input
schema, target IDs/existence, other teams, raw claims or private policy details.
Branch presence describes server eligibility; a client's own approval policy
may still block a call. More OAuth consent does not resolve a client-policy block.

| Condition | Required guidance |
|---|---|
| Unknown operation | Unknown capability; clarify the request, not a claim that the product cannot do it. |
| Known operation without an MCP adapter | Not implemented in MCP; more consent cannot enable it. |
| Implemented but disabled | Deployment/operator action is required. |
| Provable account restriction | Contact an administrator; consent alone will not help. Report missing consent too when both blockers exist. |
| Implemented, missing consent, no known account exclusion | Name the exact category and request a new user-approved consent flow; target access is still conditional. |
| Implemented and consent present | Use the available tool, with target authorization checked on call; re-list after new consent/access changes. |
| Authorization information unavailable | A temporary diagnostic failure, not a permanent unsupported/denied claim. |

Refreshing an existing grant cannot broaden it. The diagnostic cannot grant
permissions or automatically retry an action under different credentials.

Known follow-on capabilities include resource checks/pins, parameterized/one-off
runs, finite container execution and administration. Until their MCP adapters
exist, they appear only as capability descriptions with honest support status.
Descriptions and executable support must share one declaration.

### R5 — Configuration updates have an atomic, reusable contract

Grouped config-set takes `config_yaml`, a UTF-8 YAML document string, plus an explicit version
as a decimal string in `0`–`2,147,483,647`, matching the existing database column.
Version `"0"` means create only; a positive version
means update that existing version only. A stale version, missing/deleted update
target or competing creation produces a typed conflict without recreation or
overwrite. Strict operations must be atomic against concurrent creation, update
and deletion, and reusable through the HTTP API and Go client/fly.

Client-side substitutions are supplied by the caller; normal credential/template
references such as `((secret))` remain intact. MCP adds no secret expansion,
local-file loading or credential-check step. Config-get returns `config_yaml`
and `version` from the authorized API representation; YAML formatting/comments
need not round-trip. Use the existing config parser and validator.

No implicit read/diff is allowed for a write-only grant. Return the new version
from the accepted mutation, without a subsequent read. Keep validation warnings
and existing pipeline/template/run restrictions. Preserve legacy API/internal
save behavior unless the caller explicitly opts into the strict contract; the
plan gives fly an explicit strict-write opt-in while preserving its default
legacy archive/restore behavior and compatibility for older API clients.
Strict clients must fail before mutation when a server does not implement the
strict contract, including during mixed-version rollout; no silent downgrade to
legacy save behavior is allowed.

### R6 — Read results are bounded and complete within explicit pages

Pipeline/build lists default to 20 entries and cap at 100. Visibility and all
requested filters precede pagination. Never label an incomplete scan as complete
or return the wrong latest failed build because a status filter was applied only
to one page. Cursors are opaque and bound to the selected filters/identity.
Lists are live keyset views, not transaction snapshots across calls. Use stable
identity ordering (descending build ID for builds), with filters applied at each
request. Concurrent status/visibility changes can change membership; completion
means no more matching rows in that traversal, not a frozen historical result.
Never promise that a reported latest failure remains latest after the read.

Log reads default to 32 KiB of decoded UTF-8 log text and cap at 64 KiB; event
count and total encoded response size are also bounded. Preserve event type and
step/origin metadata, and distinguish finished, caught-up and truncated states.
An idle live build returns within two seconds. Continuation supports splitting an
oversized event at a valid text boundary without losing or duplicating content.
Retention gaps, stale or wrong-build cursors must be explicit; never silently skip
missing output. Every page reauthorizes using the output-specific permission.
The finite page preserves each event version. Event IDs can commit out of order,
so cursors validate the committed prefix count in the same repeatable-read
snapshot as the page and completion/reaping state. `STREAM_CHANGED` requires
restarting and replacing accumulated output. Ordinary numeric ID gaps are valid.
`caught_up` means caught up with this committed snapshot; `finished` additionally
means the build is marked completed. Neither promises immutable writer completion.
Always retain a revalidation cursor, including terminal-looking pages. This keeps
the reader additive without changing existing event writers or SSE behavior.
The finite reader accepts stored event JSON up to 8 MiB and indivisible event
metadata up to 64 KiB. Larger stored events return `STORED_EVENT_TOO_LARGE`;
reducing the requested page size cannot fix that limit. Raw JSON is decoded in
Go so valid log control characters survive. Fetch bounded IDs/sizes first and
only load payloads needed for the page; do not prefetch 129 large event bodies.
A partial page rereads its stored event, so measure the largest accepted event
across the complete traversal as well as prefix-count cost.

Keep the existing 1 MiB MCP request limit. Supplied/returned configuration content
is capped at 128 KiB UTF-8; total encoded results are capped at 1 MiB. Reject an
oversized indivisible value rather than silently truncating it. Limits and
validation errors must tell the client the supported bounds.
The total cap includes the serialized MCP result envelope, structured content
and text fallback together; apply it after encoding, accounting for JSON escaping.
Paged results must leave room for metadata and continuation rather than discard
an already selected event or silently lose its remainder.

### R7 — Mutations report uncertainty accurately

Do not blindly repeat a configured-job trigger after a response may have been
lost following creation. Return an uncertain outcome without inventing an
idempotency guarantee. Abort reports requested/accepted. Configuration conflict
never causes automatic reread-and-overwrite. Read-only and write-only workflows
must be independently usable within their granted categories.

### R8 — Compatibility and evidence

Retain official Go SDK v1.6.1, MCP 2025-11-25, the current stateless Streamable HTTP
profile, existing endpoint/certificate, exact client registrations and auth
renewal/revocation behavior. No dynamic registration, protocol upgrade or new
consent category is required.

Verify the shipped reference client and the selected Codex client against real
authenticated handlers and the exact pruned schemas. An additional production
client gets its own exact callback registration only if required; do not infer
compatibility from the stdio mock benchmark. Re-list after obtaining a new grant.
Run a small authenticated HTTP schema/client feasibility check before substantial
backend work. A client incompatibility must become a concrete design finding at
that checkpoint, not an untested assumption carried until the final phase.
Record each client's negotiated protocol version separately from the declared
server profile. Synthetic bearer/schema probes do not establish OAuth enrollment,
refresh, revocation or interactive approval-dialog compatibility.

## Acceptance criteria

- **AC1:** Real authenticated list/call tests prove independent consent, custom-role
  pause/unpause pruning, public-read behavior, empty-group omission, known-account
  denial and per-principal isolation. No unclassified operation executes.
- **AC2:** Raw calls to pruned branches produce the correct 401/403 or safe account
  error; malformed/wrong-group/unsupported calls cannot dispatch. Revocation and
  changed authority between list and call prevent subsequent unauthorized work.
  A recognized grouped operation with malformed arguments gets a validation
  failure, not a misleading consent challenge, even when its scope is missing.
- **AC3:** Every diagnostic outcome above is demonstrated, including write-only,
  hijack-only and admin-only grants, both blockers, unsupported-first precedence,
  unknown IDs, temporary access failure and no target/application reads.
- **AC4:** Same-named pipeline instances stay distinct; pipeline and build filters
  plus pagination return only visible matching records. Pipeline-scoped build
  listing never calls the unsafe team-wide listing path.
  Chronological build paging includes recent reruns of old builds across page
  boundaries; legacy rerun-grouped ordering remains unchanged by default.
  Concurrent insert/delete/status changes exercise the documented live-view
  semantics without claiming snapshot completeness.
- **AC5:** Strict config tests cover zero-on-existing, positive-on-missing/deleted,
  stale versions, simultaneous creates, competing updates, update/delete races,
  malformed/overflow versions, rollback and new-version receipts. API, Go client,
  fly and MCP preserve the conflict meaning; legacy/internal saves regress cleanly.
  Strict clients against old or mixed-version servers cannot accidentally perform
  a legacy write or recreate a deleted pipeline.
  Fly's default archived-pipeline restore still works; strict mode maps a missing
  target's empty version to `"0"` and rejects zero on an existing archived row.
  Config round-trips preserve semantic content and unresolved credential
  references without requiring application read for a supplied write.
- **AC6:** Bounded log tests cover private-job output, idle/completed builds,
  oversized Unicode events, exact reconstruction, wrong-build/stale/retained-gap
  cursors, cancellation/cleanup and revocation between pages. Existing SSE clients
  continue to work.
- **AC7:** End-to-end workflows find the latest failed build and its complete
  failure output; apply supplied config with write-only consent; trigger once;
  abort exactly; and report partial/uncertain outcomes without extra mutations.
- **AC8:** The compatibility alias retains its existing contract. Real reference
  client and Codex evidence records SDK/client versions, schema listing/calls,
  new-consent re-listing, refresh/revocation and mixed-group approval presentation.
  The early feasibility checkpoint records acceptance of the nested union before
  the strict-write and bounded-event backend work starts.
- **AC9:** Appropriate package/Brine regressions and the repository's required
  pre-merge CI checks pass. A separate small benchmark cohort verifies that the
  four missing-scope scenarios can now explain their outcomes using diagnostics.
  Mock scores alone do not satisfy production authorization or compatibility.

## Out of scope and follow-ons

This slice does not claim full CLI/API/MCP parity. Global/team-only build lists,
resource mutations, parameterized/one-off builds, pipeline archive/delete, job
pause/unpause, finite or persistent hijack and administration require later
operation slices. The metadata catalog tracks those gaps explicitly.

No arbitrary API passthrough, server search/describe/execute chain, UI rewrite,
general operation framework, machine credentials, automatic consent expansion,
new role/policy semantics or terminal-session protocol is introduced. The existing
team-wide build-list authorization issue is recorded for separate repair; this
slice does not expose that route through MCP.

## Records

Anvil track: `20260914T1753_precise_mcp_tools_and_capability_explanations`.
Selected contract: [precise access](../../architecture/mcp-precise-access.md).
Benchmark: [local benchmark review](http://localhost:6170/#/jetbridge-hearth/hearth/track?id=20260914T1624_mcp_tool_shape_coarse_local_benchmark).
Release boundary: [MCP auth](../../mcp-auth.md).
