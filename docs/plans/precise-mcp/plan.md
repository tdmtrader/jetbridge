# Implementation plan: precise MCP tools and capability explanations

## Baseline and working boundary

Planning baseline is freshly fetched `origin/core` at `62afc93a6b`. Implement in
an isolated `codex/` worktree from the then-current release branch, reconciling
material changes first. Preserve the shared checkout, benchmark evidence and
other worktrees. The owner approved implementation on a clean worktree after
self-review. Deployment remains a separate action.

The first release has three resource tools, one metadata diagnostic and the
compatibility alias, with only eligible branches listed. The scope and acceptance
IDs are in [spec.md](spec.md). The two substantial backend milestones are strict
configuration saves and bounded build events; neither is an MCP-only workaround.

## Phase 1 — Operation contracts and authority classification

Own `atc/mcp/`, the existing route/DTO references, and the smallest shared
authorization-classification helper needed by API wrappers and catalog filtering.

- [x] Inventory each first-slice operation's existing action, route, input/output,
  effective custom role, wrapper authorization kind and retry/effect behavior.
  Reuse `atc.PipelineRef.QueryParams()` for instance identity and existing API DTOs.
- [x] Add a thin operation descriptor table initially owned by `atc/mcp`: resource,
  canonical ID, public description, exact schemas, API binding, explicit scope,
  adapter/enabled state, effect/retry semantics and compatibility aliases. Derive
  catalog, capability metadata and action-to-scope lookup from this table.
  Keep CLI/API as the shared execution contract; do not build a generated CLI/UI
  framework or copy the existing role hierarchy into the descriptor table.
- [x] Reuse `AccessFactory.Create`, `EffectiveRole`, `IsAdmin` and `TeamNames` with
  authenticated trusted claims. Extract/share authorization-kind classification
  from API wrapping where necessary; default roles alone are insufficient.
  Preserve public-read routes. Do not call payload policy with invented inputs.
- [x] Register known follow-on capabilities descriptively without executable
  adapters. Support must derive from actual MCP bindings/feature state, not from
  the existence of a CLI command or an OAuth consent label.
- [x] Add focused tests for all declared active operations and meaningful unknown
  cases. Assert nonempty coverage and independent authority; do not use a brittle
  exact registry-size assertion as a safety check.
- [x] Before phases 3–5, run a small schema feasibility check using the pinned
  official Go SDK and the actual reference/Codex clients over an isolated
  authenticated HTTP fixture. List and call two exact read branches, re-list a
  pruned union, and inspect mixed read/write approval presentation. Fixture-only
  adapters must not be advertised in production. Record client/schema versions;
  if the client rejects the union or needs unsupported registration/transport,
  resolve that concrete finding before substantial backend work. This is a
  compatibility experiment, not production authorization evidence.

First-slice API bindings:

| MCP operation | Existing API action / planned reuse |
|---|---|
| pipelines_list | `ListAllPipelines`; add optional team/text filters and bounded pagination to this action. |
| pipeline_get; compatibility alias | `GetPipeline`; preserve alias output and non-instanced meaning. |
| pipeline_config_get / set | `GetConfig` / strict save route retaining canonical `SaveConfig` authority; phase 3. |
| pipeline_pause / unpause | `PausePipeline` / `UnpausePipeline`; separately classified and pruned. |
| builds_list | `ListPipelineBuilds` only; exact PipelineRef required, job/status are filters on this same action. |
| build_get / logs_read / abort | `GetBuild` / `BuildEvents` / `AbortBuild`; output uses the separate log-access check. |
| job_trigger | `CreateJobBuild`; never substitute a configuration-capable run. |

Filters must not silently select a different API action with different custom
roles. In particular, optional job filtering stays under `ListPipelineBuilds` in
this slice. Do not bind `ListTeamBuilds`: the audited route is authentication-only
and its DB query lacks caller visibility filtering. Broader build listing waits
for a shared API repair; add a regression that this adapter does not select it.

Checkpoint: descriptor/authorization review maps every first-slice action to an
existing authority; AC1/R1/R2 contracts are testable before adding mutations.
The early client experiment satisfies AC8's feasibility gate; final real-operation
compatibility and authorization evidence still belongs to phase 6.

Phase 1 is approved. The owner explicitly overrides Forge checkpoint pauses and
authorizes all remaining implementation and reviews, leaving the work unmerged. See [phase-1.md](phase-1.md) for evidence and client limitations.

## Phase 2 — Precise catalogs, diagnostics and grouped dispatch

Own `atc/mcp/handler.go`, `scopes.go`, protocol-server construction in
`atc/api/mcpserver/`, and minimal wiring in `atc/atccmd/`.

- [x] Replace the two-server read/empty selection with per-principal schema
  construction: prune union branches by support, consent and provable account
  exclusions; omit empty groups; derive conservative annotations from survivors.
  Start without a cross-request catalog cache. Reuse existing API role-cache
  semantics and check MCP grant validity on every request.
  Metadata-only writes must not affect annotations. Codex applies approval at
  the outer resource-tool level: a mixed group can require approval even for a
  read branch. Document this separately from OAuth consent and server eligibility.
- [x] Keep the root schema an object containing `request.oneOf`; each branch has
  an exact operation constant and exact argument object. Validate outputs as
  structured results with compact text fallback and bounded result capture.
  Group successes include `{operation, result}` with operation-specific result
  schemas. Keep tool failures outside a success-only output schema (`isError`
  plus safe structured text), unless an explicit error union is defined and
  validated. Preserve the compatibility alias's existing output shape.
  Apply the final byte budget to the serialized envelope including both content
  forms and JSON escaping; reserve continuation/metadata space in paged results.
- [x] Generalize the current bounded HTTP preflight to classify group plus
  discriminator against the complete descriptor registry. Authenticate first;
  recognized implemented operations lacking consent use `RequireScope` before
  the pruned SDK union can reject them. Unknown, wrong-group, malformed,
  unimplemented or disabled calls never trigger a misleading scope challenge.
  Account-pruned calls return a safe tool error. This classifier never executes.
  Share the branch validator: grouped envelope/discriminator/arguments must be
  valid before `RequireScope`; do not use the permission-pruned union for this
  preflight or perform target reads. Preserve the alias's original preflight.
- [x] Recheck consent inside the selected handler, then invoke the full wrapped
  API using `accessor.WithTrustedClaims`. Forward no caller cookies, bearer or
  routing headers. Preserve the existing cookie-isolation regression.
- [x] Implement `capabilities_explain` as authenticated connection/product
  metadata, independently of application `read` consent. Reuse the registry and
  eligibility evaluator; perform no application target lookup. Browse by resource
  and optional exact operation, with 20-entry pages and context-bound cursors.
- [x] Implement deterministic diagnostic precedence: unknown → not implemented →
  disabled → known account exclusion → missing consent → usable subject to target
  checks. Missing scopes remain visible when account denial is also known, but
  the next step does not suggest consent as a complete remedy. Temporary failures
  remain errors. Instructions point here only when an action is absent.
- [x] Preserve the compatibility alias through shared code and add real HTTP/SDK
  tests for 401/403, branch pruning, stale schemas, unsupported guessed calls,
  diagnostics without `read`, cross-principal isolation and immediate revocation.

Checkpoint: catalog/diagnostic behavior satisfies AC1–AC3 and AC8's alias contract.
Expose only adapters actually completed; registry metadata never prematurely
advertises later-phase operations as implemented.

## Phase 3 — Atomic strict configuration writes

Own `atc/api/configserver/`, `atc/api/errormap/`, the relevant `atc/db` save
transaction, `go-concourse/concourse/configs.go`, and fly's set-pipeline helper.

- [x] Characterize current callers first: HTTP missing/empty version header,
  explicit zero, positive version, fly create/update, internal `set_pipeline` and
  direct `SavePipeline` uses. Record the intended compatibility boundary in tests.
- [x] Add `PUT /api/v1/teams/:team_name/pipelines/:pipeline_name/config/conditional`
  for strict saves, requiring the existing `X-Concourse-Config-Version` header.
  Parse the complete decimal value in `0`–`2,147,483,647`, matching the existing
  PostgreSQL integer column; reject absent, empty, signed, trailing-data and
  overflow values. The
  existing `/config` route and its missing/empty-header behavior stay unchanged.
  Use a distinct route identifier but explicitly alias its effective role,
  authorization kind, policy action and audit action to canonical `SaveConfig`;
  existing custom-role overrides and policy denials must govern both paths.
- [x] Strict Go/fly clients call only the conditional endpoint. An older replica
  returns an unsupported-route failure without performing a legacy save; never
  fall back to `/config`. Test old servers and mixed-version replicas. An ignored
  request header or a capability probe followed by a write to a different replica
  is insufficient protection against downgrade.
- [x] Add a shared domain save-precondition path that atomically checks both
  existence and version: zero creates only, positive updates only. Reuse the save
  transaction and uniqueness/locking behavior; do not perform an MCP preflight
  read or implement parallel save logic. Preserve internal callers' legacy mode.
- [x] Map strict precondition failures to stable HTTP 409 typed conflicts. Return
  the resulting version from the committed mutation via the response version
  header, preserving warnings/body compatibility. No post-write read is permitted
  just to obtain the receipt version.
- [x] Add an opt-in Go client method and a fly `--strict-config-write` flag using
  its known/fetched version. Preserve fly's default legacy behavior, including
  archived-pipeline restore. In strict mode, map a missing target's empty version
  to `"0"`; an existing archived row must conflict with strict create-only zero.
  A concurrent deletion becomes a conflict, not accidental recreation. Never
  silently fall back or automatically retry a conflict. Preserve internal callers.
- [x] Wire MCP config-get/set to the authorized API. Require version in MCP input,
  enforce configuration byte bounds and retain validation/template/run guards.
  Use `config_yaml` as a UTF-8 YAML string; config-get returns this plus `version`.
  Reuse `atc.UnmarshalConfig`/the existing API validator and serialize the returned
  `atc.Config` for reads. Preserve unresolved credential/template references;
  do not add implicit file loading, interpolation or `check_creds`. Test semantic
  round-tripping and write-only supplied configuration.
  Translate only known conflicts to `VERSION_CONFLICT`; preserve target
  non-disclosure and safe error text.
- [x] Use real PostgreSQL concurrency tests for competing creates/updates,
  update/delete races, rollback and receipt versions. Exercise API/Go client/fly
  conflict handling, strict/legacy compatibility, old-server/mixed-replica
  fail-closed behavior, canonical SaveConfig policy/role aliasing, and write-only
  MCP grants.

Checkpoint: AC5 passes across shared boundaries before config-set is enabled.

## Phase 4 — Bounded visible lists and build events

Own the relevant pipeline/build HTTP handlers and DB queries, the build-event
reader, `go-concourse` paging support as needed, and MCP read adapters.

- [x] Implement `pipeline_get` and `build_get` through their exact wrapped API
  actions, using bounded field projections. Test full instance identity, public
  metadata versus private output, foreign build IDs and missing targets. Reuse
  the pipeline reader behind the compatibility alias without changing its result.
- [x] Add opt-in bounded filtering/pagination to `ListAllPipelines`, preserving
  its existing unpaged response for callers that omit paging. Apply visibility,
  optional team and name-query filters before limit. Use additive `format=page` with `{items,next_cursor}` while preserving legacy
  arrays when omitted; project compact MCP fields.
  Define stable identity keyset ordering, not offsets. Document live membership
  under concurrent changes; do not introduce a database snapshot/session service.
- [x] Add optional job/status filtering before pagination and an additive `format=page`
  chronological mode to `ListPipelineBuilds`. Use descending build-ID order
  consistently in page queries and older/newer probes; preserve legacy
  rerun-grouped ordering by default. MCP translates only validated cursor values,
  never arbitrary URLs. Bind cursor identity to PipelineRef and all filters.
  Test newest-failure selection across pages, including a recent failed rerun of
  an old build, and same-named instances.
  Add insert/delete/status-change cases that assert keyset continuation and
  honest live-view completion instead of asserting a frozen cross-call snapshot.
- [x] Add an additive bounded JSON mode on `BuildEvents`, e.g. `format=json`,
  under the same action and full output-specific authorization/policy/audit
  wrappers. Preserve default SSE behavior. Use a finite DB event-page reader,
  not the current indefinitely waiting SSE handler through `boundedResponse`.
  Preserve the existing old/new build-ID storage compatibility in this reader.
- [x] Return typed event envelopes with IDs and step/origin metadata, capped
  decoded log bytes, event count and total encoded size. Represent continuation
  as event ID plus an offset inside an oversized log event; split UTF-8 safely.
  A non-log event that cannot fit is an explicit bounded error, not silently lost.
- [x] Distinguish finished, caught-up and truncated states; retain a useful cursor
  on empty live output. Bound waiting to two seconds, close DB/event resources on
  every path, propagate cancellation, and return explicit retention/cursor errors.
  Bind cursors to build and page format, and reauthorize every page.
- [x] Test real event storage and authorized HTTP handlers: complete reconstruction,
  oversized Unicode, private-job logs on public pipelines, idle/completed builds,
  wrong-build cursors, deletion/retention, cancellation and goroutine/resource
  cleanup. Retain all existing SSE regressions.
  Verify the final encoded MCP size with escaping and text fallback, including
  pages near the byte limit; reconstruct all split events without lost content.

Checkpoint: AC4/AC6 pass; pipeline/build lists and logs are enabled only after
their bounded contracts are proven.

## Phase 5 — Remaining mutations and complete workflows

Own precise pipeline pause/unpause, configured-job trigger and build-abort adapters,
plus documentation and shared DTO reuse.

- [x] Wire exact instance/job/build identities to the corresponding wrapped API.
  Keep each action's custom-role/policy check even when it shares a resource tool.
- [x] Return compact accepted receipts. Treat a trigger failure after possible
  admission as `OUTCOME_UNKNOWN`; do not introduce automatic retries/idempotency
  claims. Abort reports requested, and conflicts never cause blind overwrite.
- [x] Exercise full diagnosis, supplied config update, trigger/abort and partial
  write-without-read workflows. Assert actual state and extra/duplicate effects,
  not only final response strings. Include authority changes after tools/list.

Checkpoint: AC7 and the full first-slice catalog pass; follow-on operations remain
descriptive only.

## Phase 6 — Real-client verification and review handoff

- [x] Add `features/mcp-operations.feature` and reuse Brine's real auth fixture,
  reference client and wrapped API steps. Use real PostgreSQL/HTTP/issuer for
  production authorization and mutation tests; the synthetic benchmark remains a
  separate model-usage experiment.
- [x] Rebuild `.build/brine-adapter-jetbridge` before Brine checks/runs. Run existing
  authentication/MCP-auth regressions and the new operations feature. Use the
  configured CI environment when envtest assets/runtime requirements demand it.
- [x] Extend the reference client's list/call support only as needed for these
  contracts, then test the actual pinned Codex client against the authenticated
  HTTP test deployment, building on phase 1's feasibility evidence. Preserve
  exact callbacks; add a distinct registration
  only when the client needs it. Verify new-consent re-listing and schema pruning,
  refresh/revocation, and read/mutation calls through the real reference client.
  The pinned Codex OAuth probe verifies grouped reads and changed mixed-group
  annotations; its fixture configures whole-tool approval. Interactive approval UI
  and seamless same-process re-enrollment remain explicit adoption experiments,
  not passed checks.
- [x] Repeat the four benchmark scope-diagnosis cases twice on the selected precise
  shape with the diagnostic (eight local sessions). Keep original prompts where
  valid, classify unsupported outcomes honestly, and store a new cohort with
  token/call traces; no six-shape rerun or large eval framework.
- [x] Run appropriate focused package/DB/API/client/fly tests, then required unit
  and pre-merge CI checks from `CLAUDE.md` (`hack/ci-check.sh <committed-ref>` before
  offering a merge). Do not use Go subtest syntax to focus Ginkgo or run K3s tiers
  on this Mac. Elm tests/build are needed only if frontend source changes.
- [x] Update `docs/mcp-auth.md`, capability coverage and client usage examples with
  implemented versus deferred operations and exact supported client versions.
  Attach evidence, remaining limits and a rollout/rollback proposal in Loupe.
  Code review and merge/deployment remain separate from this planning approval.

Checkpoint: AC8 compatibility evidence and its approval-UI limits are documented;
AC9 verification passes. See the final review for exact CI refs, the corrected
local failures and direct Loupe links. No universal-client or full-parity claim.

## Review focus and remaining decisions

The shape decision is settled. Review the first-slice boundary, the additive
strict-config compatibility contract, and the bounded-event contract before
implementation. Numeric bounds are proposed first defaults, not measured
production maxima; revise with evidence while keeping byte limits explicit.

The known `ListTeamBuilds` authorization issue must be tracked for separate shared
API repair before broader build discovery is enabled. This plan's exact-pipeline
binding prevents adding that exposure through MCP; it does not claim to repair
the existing API route.

After this track, add independent slices for resource actions, parameterized runs,
administration and finite hijack, addressing their distinct authority and
lifecycle contracts. Keep the operation coverage ledger honest throughout.

Implementation verification is recorded in [implementation-review.md](implementation-review.md). Owner checkpoint pauses were waived; merging and deployment remain excluded.
