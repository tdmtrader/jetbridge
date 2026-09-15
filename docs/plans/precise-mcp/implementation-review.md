# Precise MCP implementation review

Implemented on `codex/mcp-precise-implementation` in the isolated worktree, based
on `origin/core` `62afc93a6b`. The owner approved all implementation and reviews
without intermediate Forge checkpoints. This branch is not merged or deployed.

## Delivered

- Three precise resource groups (`pipeline`, `build`, `job`), metadata-only
  `capabilities_explain`, and the existing `pipeline_status` alias. Closed branch
  schemas are pruned by real adapter support, enabled state, exact consent and
  provable account restrictions. Per-call authorization still crosses the full
  API role, custom-role, policy, target, archived-pipeline and audit wrappers.
- Shared strict configuration API and transactional domain method; exact
  create/update preconditions, typed conflicts and committed version receipts.
  Go client method and opt-in fly flag fail closed on older replicas. Default
  fly and internal saves retain legacy behavior.
- Shared additive list pages and finite build events. Exact pipeline instances,
  pre-limit filters, ID keysets, private-job output checks, bounded UTF-8 chunks,
  explicit retention/size/continuation failures and cancellation. SSE is unchanged.
- Trigger/abort/pause/unpause adapters and a generic reference-client `call`
  command. Trigger and strict-write ambiguous outcomes never cause an automatic
  mutation retry. Only accepted abort is promised.
- Reusable six-shape mocked benchmark harness and eight precise-scope diagnostic
  sessions. Earlier 192-session comparison remains preserved in its original
  benchmark worktree and an unchanged portable baseline archive out of tree in `~/jetbridge-evidence/precise-mcp-20260915/` (sha256 in its `MANIFEST.sha256`, out of tree);
  this follow-up is not a controlled before/after experiment.

## Review findings and resolutions

Repository reviewer Euclid checked shared API parity, canonical actions, strict
configuration semantics and list adapters. Design reviewer Epicurus checked
schema/dispatch behavior and finite-event design. Client researcher Wegener
reviewed client compatibility and independently regraded the eight benchmark
sessions. Author self-review covered integration and error handling.

| Finding | Resolution and evidence |
| --- | --- |
| An entirely pruned group produced an unhelpful unknown-tool error. | SDK receiving middleware classifies known groups after protocol decoding without listing hidden executable tools; real HTTP tests exercise unsupported and malformed guesses. |
| Disabled compatibility alias could bypass canonical disablement. | Alias and precise operation share the disable flag; real SDK test asserts `DISABLED`. |
| Eager catalog evaluation blocked unsupported/unknown metadata during an unrelated access outage. | Evaluate executable eligibility for listing and selected calls; metadata support is checked first. HTTP tests distinguish temporary access failure. |
| Error/result copies and JSON escaping could exceed the intended budget. | Bounded error text and final serialized MCP envelope accounting, plus escaping tests and actual log reconstruction through HTTP. |
| Event IDs can commit out of order after completion looks terminal. | One repeatable-read snapshot validates committed prefix counts, page and completion state; always preserve a cursor; late commits require restart and replacement. Real controlled-transaction test passes. |
| Heavy JSON escaping could produce a non-advancing empty page; PostgreSQL JSON functions reject escaped NUL. | Fit output by reducing UTF-8 log chunks; decode raw bounded JSON in Go. Unicode/NUL reconstruction and progress tests pass. |
| Fetching 129 large payloads could transfer excessive data when pgx drains rows. | Fetch lightweight IDs/sizes, load only needed payloads, and cap aggregate stored-payload transfer at 8 MiB per page. No streaming-parser framework. |
| Terminal in-memory checks and overlapping legacy event IDs had compatibility gaps. | Retained terminal resource identity remains readable; ambiguous old/new duplicate IDs fail explicitly. Real storage fixtures pass. |
| JSON Schema integer forms such as `1e3` silently became zero in typed adapters. | Normalize exact integers after full branch validation and reject out-of-range values before consent preflight/execution. Regression tests preserve requested limits. |
| Pipeline iteration errors could masquerade as the last page. | Propagate `rows.Err()` from shared pipeline scanning. |
| Architecture and route guards rejected the new integration placement and conditional action. | Keep real eligibility tests in the MCP layer, classify the schema probe and shared classifier explicitly, and extend the route-effect/audit contracts. Corrected focused checks and full CI unit tests pass. |
| Archived OAuth probe sources entered main-module package discovery. | A nested module boundary keeps the immutable source evidence outside production package discovery; the probe still builds through its documented Brine module. Full CI unit tests pass. |

## Verification

- Real PostgreSQL strict-save races: one winner for competing creates/updates,
  controlled delete/update lock overlap, rollback after a later job write fails,
  archived compatibility, instance separation and immutable version receipts.
- Real wrapped API tests assert canonical SaveConfig custom-role, policy and audit
  identity, malformed versions, receipts and typed conflicts.
- Real fly executable: legacy update, strict create/update, conflict and an older
  replica. Go client additionally checks lost responses and invalid receipts.
- Finite storage: idle/completed snapshots, Unicode/NUL and escaping reconstruction,
  legitimate ID gaps, late commits, retention, wrong-build cursors, cancellation,
  legacy/current ID compatibility, terminal in-memory checks and explicit bounds.
- Brine precise operations: 3/3 real OAuth/SDK/API/PostgreSQL workflows, including
  all first-slice operations, duplicate-effect checks, write-only configuration,
  independent custom roles, changed authority, private-job logs on a public pipeline,
  missing consent, unsupported calls, disabled alias and access-service outages.
- Existing Brine auth regressions: 6/6 renewable fly/browser scenarios and 19/19
  MCP-auth scenarios passed after rebuilding the adapter. Together with the new
  operations feature, **28/28 real acceptance scenarios passed**.
- Focused checks passed: strict/finite PostgreSQL tests (15), real fly strict-save
  tests (5), MCP/API/auth/auditor/Go-client packages, and benchmark harness tests
  (18 baseline and 7 follow-up).
- **CI build/vet passed**, build 843710 at `1361a9a4be`. **Full CI unit tests
  passed**, build 843732 at `8bf06cf8ed`. The intervening commit changes only this
  decision documentation and the archived probe module boundary; production code
  is identical. The first CI unit attempt (843721) stopped at package discovery;
  the module boundary fixed that failure before the passing rerun.
- The local full tier ran 109 suites in 25m22s. It failed only the root architecture,
  ATC route-effect and auditor suites compiled before their corrections. Subsequent
  focused checks passed for each affected suite, and the final full CI unit run
  passed. This is not a claim that the original local invocation was green.

Verification commands and raw local logs are retained in `/tmp/mcp-*.log` for this
session. The committed benchmark and OAuth evidence have their own provenance and
hash manifests; temporary logs are supplementary, not required to read the review.

Local cost measurement (PostgreSQL 14.19, this Mac): a near-8 MiB stored log event
reconstructed in 128 pages, 7.04 seconds total, slowest page 74.9 ms. A 100,000-event
committed-prefix revalidation plus team-table old/new-column predicate took
14.9 ms. These are coarse local measurements, not production latency guarantees;
partial pages reread the stored event and prefix work grows with retained history.

## Evidence boundaries and remaining limits

The hard limits are **8 MiB JSON per stored event**, **8 MiB aggregate stored
payload transfer per page**, and **64 KiB indivisible metadata**. A larger stored
event returns `STORED_EVENT_TOO_LARGE`; smaller `max_bytes` cannot resolve it.
The existing SSE endpoint remains available unchanged.

The eight synthetic diagnosis runs all passed (14 metadata calls and two requested
triggers). Usage totaled 183,758 tokens, including 104,704 cached input tokens;
median 22,850.5 per run. They do not establish a statistical success rate or token
savings. See [benchmark results](../../../benchmarks/tool-shapes/RESULTS.md).

The actual Codex OAuth proof and earlier model schema probe are separate from
mocked model runs and real API mutation tests. See [client probe](../../../hack/mcp-oauth-probe/README.md).
Declared server profile is SDK v1.6.1/MCP 2025-11-25; Codex CLI 0.153.1 negotiated
2025-06-18. Mixed groups have whole-tool client approval behavior. Interactive
approval UI, seamless same-process re-enrollment, other clients and all-client
operation compatibility remain unproven.

No protocol upgrade, dynamic registration or client-ID metadata document is
required. Codex enrollment needs a pre-registered ID and exact configured loopback
callback/listener port. Resource/run/hijack/admin adapters remain future slices.
The existing broad ListTeamBuilds authorization issue stays separate; MCP binds
build discovery only to the exact-pipeline route.

## Rollout and rollback

After merge approval, build and roll out the server with the strict route and MCP
adapters together. Existing grants remain restricted to their selected categories;
only `read` is selected by default. Re-list tools after rollout or new consent.
A separate Codex registration should use its exact callback. Validate read-only
and write-only workflows on a nonproduction pipeline before broader adoption.
Rollback uses the previous binary: no new database migration is introduced by
this slice. Strict clients fail closed if routed to an older binary, and legacy
CLI/API behavior remains available. Merge and deployment are not performed here.

## Review disposition

Loupe: [implementation](http://localhost:6170/#/jetbridge-hearth/hearth/track?id=20260914T1753_precise_mcp_tools_and_capability_explanations),
[benchmark](http://localhost:6170/#/jetbridge-hearth/hearth/track?id=20260914T1624_mcp_tool_shape_coarse_local_benchmark).
The selected decision is `20260914T1251_jetbridge_mcp_discovery_and_operation_surface`.

Author self-review and the repository, design and client/benchmark reviews are
satisfied after the fixes above. All six approved implementation phases are done.
The decision record is decided; benchmark and precise implementation reviews are
recorded in Loupe. Anvil's final `completed` transition means shipped and checks
merge evidence, so the implementation tracks remain at the reviewed pre-merge
stage. This is intentional: the owner requested finished work and reviews without
merging. No implementation question or unresolved review finding remains; the
client and operational limits above remain explicit.
