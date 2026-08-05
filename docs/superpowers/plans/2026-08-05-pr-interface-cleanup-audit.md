# PR Interface Cleanup — Azure removal and a GitHub/PR-process audit

**Date:** 2026-08-05
**Scope:** finding #1 of the [Jetbridge agentic complexity audit](#appendix-a--source-of-the-findings) — the provider-native pull-request stack.
**Method:** six parallel subsystem readers over `agent/pullrequest`, `agent/publisher`, `agent/workflowrun`, `agent/resourcecapture`, `atc/atccmd` and `atc/db`, each structural claim then put through an adversarial refutation pass. Corrections from that pass are folded in below and marked where they change the picture.

---

## Part 1 — Azure DevOps removal (landed)

Azure DevOps is gone from the tree. It was the largest single piece of the PR stack's disproportion: the adapter was **43% larger than the GitHub one** (2,657 src / 2,425 test vs. 1,859 / 1,108), and its branch compare-and-swap implementation was bypassed at its only composition site — a decision recorded in the design doc *before the code was written*.

**Deleted outright**
- `agent/pullrequest/azuredevops/` — the whole package: `client.go`, `wire.go`, `observe.go`, `mutate.go`, both test files, and 8 REST fixtures + README (~5,430 lines). Its `NewObserver` had no caller anywhere in the repo.
- `atc/atccmd/agent_pr_mutator.go` — the Azure switch arm, `azureRepositoryIdentity`, `canonicalAzureSegment`, `validateAzureRepositoryURL`, and `providerBranchWriter` (which existed *solely* because Azure's REST ref-update API cannot publish a newly materialized local object; GitHub passes its mutator straight through).
- `gittransport`/`directgit` bearer authentication — `AuthenticationBearer`, `bearerGitConfig`, and the now-constant `gitConfig` plumbing. **This was a judgement call, called out explicitly.** The `[http] extraHeader = Authorization: Bearer` transport existed only for Azure Git OAuth; with Azure gone it had zero producers. `GIT_CONFIG_GLOBAL=/dev/null` is now unconditional, which is the security property that plumbing was protecting.

**Narrowed**
`pullrequest.Provider`, `publisher.PRProvider`, `publisher.AdapterKind`, and every switch over them — in `types.go`, `monitor.go`, `resource/protocol.go`, `resource/dependencies.go`, `publisher/policy.go`, `publisher/pr_actions.go`, `gittransport/ref_lease.go`, `gittransport/verified_branch_writer.go`, `atccmd/agent_publisher.go`, and `atc/db/agent_pr_bindings_factory.go`.

**Tests: retargeted rather than deleted, where the assertion was still worth something**
- `conformance/suite.go` derived its "wrong provider" locator by *flipping between the two providers*. Deleting the flip naively would have made four `mustReject` assertions claim that **valid** locators are rejected — the test would have silently stopped testing anything. It now uses a synthetic `Provider("unsupported-provider")`.
- `policy_test.go`'s multi-rule selection test proved provider-based routing; it now proves repository-based routing across two GitHub rules. The `adapter/provider mismatch` negative case now uses `AdapterGateway` (a real, wrong adapter) instead of a deleted constant.
- The cross-routing rejection test now varies the repository, not the provider — with one provider, a provider mismatch fails locator validation before it ever reaches policy resolution, which would have tested a different thing.
- `ref_lease_test.go`'s Azure-looking tokens and remotes were testing *opaque bytes*, not Azure; renamed and retargeted rather than dropped.

**Deliberately left alone**

`atc/db/migration/migrations/1773106151_create_agent_pr_bindings.up.sql:5` still reads `CHECK (provider IN ('github', 'azure-devops'))`.

There is **no migration checksum or content-immutability test in this repo** — the runner records version numbers only (`migration.go:278`, `:415`; `parser.go:72`; `bindata.go` is a stub). So editing that file in place would pass every test in the tree *and* silently diverge from the live theborg database, which is already past `1773106151` and would never re-run it. There would be no detector.

The alternative — a new forward migration `1773106160` narrowing the CHECK — needs lockstep bumps of `jetbridgeHeadMigration` (`legacy_upgrade_test.go:37`) and `JETBRIDGE_VERSION` (`migrate-preflight.sh:82`), and **hard-fails on any live DB holding an `azure-devops` binding row**. Since no code path can produce that value any more, the permissive CHECK is inert. Doing nothing is the correct call; if you later want it narrowed, do it as its own commit with a preflight row count.

Also untouched, and verified unrelated: `go.mod`'s `github.com/Azure/go-ansiterm` / `go-ntlmssp` (docker terminal and LDAP, indirect), `atc/worker/jetbridge/process.go`'s `kubernetes.azure.com/scalesetpriority` AKS spot label, and a `dev.azure.com` git URI used as a generic remote in an image-resolution test.

**Sealed records were never at risk.** No frozen record descriptor enumerates provider values: `pull-request.v1.rev2/rev3.json:25` declares `body/provider` as a bounded `"kind": "string"`, while `body/state` in the *same* file is a real `"kind": "enum"` with explicit values. The dialect can express a closed set and deliberately does not for `provider`. No descriptor digest moved; no schema revision was needed.

**Verification:** `go build ./...` and `go vet ./...` clean; `agent/pullrequest/...`, `agent/publisher/...`, `atc/atccmd/...` all pass. Full `make test-unit` result recorded at the bottom of this document.

---

## Part 2 — Audit: is watching a PR a snapshot input to another workflow?

**Short answer: architecturally yes, structurally no.** The intended shape is exactly the one you described. But the implementation builds a private copy of the platform's input machinery rather than using it, and it does that in six specific, individually fixable places.

### What is genuinely right

Watching a PR does not poll from the server. It renders **an ordinary Concourse pipeline** (`agent/pullrequest/pipeline.go:279-304`) containing exactly:

- one resource type — `forge-pr`, digest-pinned;
- one resource — `pull-request`, with `check_every: <poll_interval>`;
- one job — `admit`, whose entire plan is a single `get` with `trigger: true, version: {every: true}`.

Change detection is ordinary Concourse resource checking. The `forge-pr` resource's `in` writes a sealed `pull-request/v1` `record.json` plus two verified git checkouts; `agent/resourcecapture` seals that into a typed snapshot; the snapshot becomes an input to a workflow run. That chain is complete in code. The observation is a real sealed record, the workflow that consumes it (`pr-monitor-v3`) is an ordinary workflow, and the resource never mutates anything — `out` is a hard error, and all provider writes happen in a separate ATC-side executor with its own credentials.

That is the right instinct, executed. The problem is everything wrapped around it.

### Divergence 1 — the seed does not declare the PR as a source at all

This is the clearest place the design didn't take its own advice.

`agent/workflow/seeds/pr-monitor-v3/workflow.yaml:9-21` declares the observation as a plain `inputs:` port. The snapshot IDs are pushed in by a bespoke launcher — `Binder.LaunchMonitor` (`agent/workflowrun/binder.go:170-226`) hand-builds a `BindRequest.Inputs` map with four hard-coded keys. The workflow author-facing surface never says "this run is triggered by a pull request."

Meanwhile the platform *has* a declarative construct for exactly this: `resource_sources:` on a schema-v3 definition (`agent/workflow/resource_source.go:16-22`, keys `name/resource/type/trigger/version`, `passed:` explicitly rejected). It is validated, wired, and promoted into a standing `admit` pipeline by `RenderResourceSourcePipeline`.

**The adversarial pass corrected me here, and the correction is worse than the original finding:** `resource_sources:` has **zero production users**. `grep -rl resource_sources agent/workflow/seeds/` returns nothing. All ten shipped seeds take external state through `inputs:` ports filled by adapters. There are now **four** such adapters — ticket dispatch (`agent/dispatch/dispatch.go:236-252`), resource capture (`fly agent-snapshots capture-resource` + REST), raw tar upload (`agent/api/snapshots/handler.go:136-205`), and PR-monitor launch — plus one declarative construct nobody uses.

So the real finding is not "the PR stack should have used `resource_sources`." It is: **there are five ways to get an external thing into a workflow run, and the one that was designed to be canonical is the one with no users.** Consolidating that is a bigger decision than the PR stack, and the PR stack is the forcing function for making it.

### Divergence 2 — the generic admission machinery was forked, not reused

`atc/db` now carries two parallel store surfaces. `WorkflowResourceSourceBindingAdmissionStore` and `WorkflowResourceSourceBindingBuildStore` (`agent_workflow_resource_source_runtime_types.go:192-203`, `:250-297`) mirror every generic method with a `bindingID` parameter: `ClaimBindingBuild`, `BindBindingSelection`, `BindBindingCapture`, `BindingReady`, `BindingCapturing`, `FailBindingAdmission`, `ExactBindingInputMapping`. Two of the pairs are ~290 LOC of near-verbatim SQL; the rest are thin wrappers. Both are implemented by the *same* struct and reached through an always-true runtime type assertion (`agent/workflowrun/source_build_reconciler.go:263`).

The cost lands on live code: the generic path is now `WHERE pr_binding_id IS NULL` throughout, and the discriminator has leaked into ten executing queries across `agent_workflow_resource_source_admissions_factory.go`, `agent_experiments_factory.go`, and `…admission_store.go`. Migration `1773106151` also destructively replaced `UNIQUE(team_id, workflow_definition_id)` on the *live* `agent_workflow_resource_source_pipelines` table with three partial indexes keyed on `pr_binding_id`.

The fork exists for a real reason: the generic path is **definition-owned** (one pipeline per workflow version) and a PR pipeline is **binding-owned** (one per PR). That is a genuine modelling difference — but it wants a nullable owner column on one surface, not a duplicated surface.

### Divergence 3 — PR identity is hard-coded inside otherwise generic code

`agent/workflowrun/source_admission.go:338-353` and `source_build_reconciler.go:442-502` assert `declaration.SourceName == pullrequest.MonitorSourceName`, `ResourceName == MonitorResourceName`, `SnapshotType == "pull-request/v1"`, and even `len(binding.Version) == 8`. `agent/workflowrun/binder.go` imports `agent/pullrequest` for input names at `:211` and `:500`.

The generic reconciler knows what a pull request is. Anything else that ever wants this shape has to either impersonate a PR or fork again.

### Divergence 4 — mutable acknowledgement state lives in the resource's `source:`

`MonitorCheckState` — binding ID, binding revision, acknowledged cursor, last reconciled target/time, active action digest, paused/terminated flags — is projected into `atc.Source` as a `monitor: {…}` block (`pipeline.go:251-262`, `:583-593`).

The consequence: **the pipeline config hash covers the acknowledgement state**, so every binding revision bump rewrites the pipeline — including the bump `ReserveLaunch` performs on itself (`agent_pr_bindings_factory.go:300-310`). Steady state is a `set_pipeline`-equivalent write per action, and a check already in flight against the old config produces a version that will be rejected as stale. It also forces a private in-place live-pipeline config update path (`updateProtectedMonitorPipelineConfig`, ~115 LOC) that the generic path — immutable per promotion — does not need.

The stated reason for this design is sound: the resource's previous version must never be acknowledgement authority (`resource/check.go:26-27`). **But the adversarial pass refuted the stronger claim the package doc makes.** `resource/protocol.go:3` says "a Concourse version is merely an ordering token"; the write path contradicts it. The sealed `pull-request/v1` body has no cursor field, so `Version.Cursor` is the *sole* carrier of the new cursor from the observation into `acknowledged_cursor` — on the direct-terminal path (`monitor.go:634` → `MarkDirectTerminal`) it is copied off the version with no server-side re-derivation and no reservation pin.

So the cursor already round-trips through the version. What is actually barred is only the **prior** version. That materially weakens the case for pushing state through the pipeline config.

### Divergence 5 — one standing pipeline per watched PR

The unit of scale is a `pipelines` row, a resource, a job, and a checker per PR. Counting components, tables, binaries, images, seeds, and flags, **roughly 35 distinct named artifacts must be correct and mutually consistent for one pull request to be watched.**

### Divergence 6 — the write-back half is the genuinely new thing, and it is the smallest

Everything above is the *watch* loop. Opening a PR needs five things: a branch on the remote, one `POST /repos/{repo}/pulls`, a server-resolved write credential, a policy rule authorizing the destination, and an idempotence record. That's it.

Two things worth knowing:

- **The branch push is provider-neutral plain git smart HTTP on both paths.** No provider API is used for any ref write anywhere in the tree. The provider REST API is strictly necessary only for objects git has no concept of: the PR object, commit statuses, and review replies.
- **A simpler working write-back path already exists and predates this stack** — `publisher.ModeBranch` over `directgit`, using a provider-neutral marker ref (`refs/concourse/publications/<sha256hex>`) for idempotence, recoverable via `ls-remote`, needing no approval and no forge API. The PR path instead uses a provider-specific HTML comment in the PR body plus a list-and-match scan. Two idempotence designs for one concept.

### The one primitive the generic construct genuinely lacks

**A feedback loop.** A generic resource source is fire-and-forget: the run's outcome never influences what the source selects next. Cursor/acknowledgement — reserve exactly-once, launch, inspect the run's durable publication evidence, then acknowledge or park into `attention_required` — is the real primitive the PR path adds. It is the part worth keeping, and the part worth generalizing rather than leaving PR-shaped.

### And: none of it can run

Reachability, re-verified at HEAD rather than taken from the earlier audit:

| Claim | Verdict |
|---|---|
| `--agent-publisher-pull-requests-enabled` makes web refuse to boot (`agent_publisher.go:20`, `:83`, `:156`) | **CONFIRMED** |
| No production `MonitorPipelinePolicyResolver` exists; both callers pass zero (`workflow_resource_sources.go:187`, `command.go:1865`, `agent_experiments.go:237`) | **CONFIRMED** |
| `db.NewAgentPRBindingsFactory` is inside that same dead branch | **CONFIRMED** |
| 7 of 22 exported interfaces in `agent/pullrequest` have zero production implementations; an 8th has zero production call sites | **CONFIRMED** |
| `BindingStore` declares 16 methods; 6 have zero non-test callers; there is no `atc/api` surface for PR bindings at all | **CONFIRMED** |
| `db.NewAgentPRMonitorRunsFactory` has zero call sites anywhere, **including tests** | **CONFIRMED** |

Two corrections worth carrying, because they change what "dead" means:

1. **The `forge-pr` resource binary is production-wired, just unscheduled.** `cmd/forge-pr-resource` is built by `deploy/forge-pr-resource.Dockerfile`, published by job `build-forge-pr-resource-image` (`deploy/concourse-pipeline.yml:1087`, guarded by a test), and the chart renders `--agent-publisher-pr-resource-image`. The observation half genuinely ships as an image; nothing renders a pipeline that would run it.
2. **Provider I/O happens in `in`, not just `check`.** `resource/in.go:80` re-observes the provider and re-derives the action, then does two authenticated `git fetch` operations (`:129-159`). Because the rendered job's only step is a triggering `get`, `in` runs on every monitoring build and does substantially more network work than `check`. It also *fails* if the PR moved between `check` and `in` — no stale replay, but also no idempotent re-fetch of an old version.

---

## Part 3 — Recommended tightening, ranked

Ordered by (structural clarity gained) ÷ effort. Nothing here is implemented — this is the proposal.

### 1. Decide the parking question first, because everything else is downstream

`incompletePRAuthoritySpineError` names four specific gaps: authoritative impact evaluation, action-bound initial publication composition, approved-baseline materialization/advancement, and lifecycle wiring. That is an honest, well-scoped list — this is parked work, not abandoned work.

But it is parked **on `main`**, where it grows a tax: `pr_binding_id` is in ten live queries, the DDL ships in every deployment, and the subsystem greps as wired (`NewAgentPRBindingsFactory` really is constructed at `workflow_resource_sources.go:189` — inside the dead branch). The cleanup cost rises monotonically with every new query that acquires the discriminator.

Three honest options:

- **Branch it.** Costs nothing today, stops the tax immediately, keeps every line recoverable. The right answer if the four gaps are a this-quarter target.
- **Finish it** — but only after items 2–4 below, so you're finishing the tightened design rather than the current one.
- **Delete it and keep the sealed contracts.** `pull-request/v1`, `pull-request-response/v1`, and `publish-impact/v1` are good record types; `publish-impact/v1` is publication-generic by shape. Keeping the contracts and dropping the machinery preserves the design work at ~5% of the line count.

### 2. Make the observation a declared source, not a pushed input

Give `pr-monitor-v3` a `resource_sources:` entry for `pull-request` and let the generic admission path bind it. This deletes the `MonitorSourceName`/`MonitorResourceName`/`pull-request/v1` assertions from `source_admission.go` and `source_build_reconciler.go`, removes the `agent/pullrequest` import from `binder.go`, and makes the seed self-describing.

It also forces the consolidation question from Divergence 1 into the open, which is a feature: you'd be deciding whether `resource_sources:` is the canonical input construct or whether the adapter-fills-`inputs:` pattern is, rather than continuing to ship both.

### 3. Move the acknowledgement cursor out of the pipeline config

The cursor is already version-carried (Divergence 4); what must not be trusted is the *prior* version. So the server does not need to rewrite the pipeline to deliver acknowledgement state — it needs a durable per-source cursor it hands to `check` out of band, keyed by `(pipeline, resource)` and written only by the platform on run acknowledgement.

Concretely: lift `acknowledged_cursor` and the attention/pause flags out of `agent_pr_bindings` into the **resource-source registry row**, and stop projecting them into `atc.Source`. That deletes `updateProtectedMonitorPipelineConfig` (~115 LOC), makes the monitor pipeline immutable-per-binding like every other source pipeline, removes the stale-version-in-flight race, and — most importantly — turns "acknowledged cursor" into the generic feedback primitive the platform is missing, rather than a PR-specific column.

This is the highest-leverage item architecturally. It's also the one that makes a second stateful source type (an issue tracker, a chat thread, a review queue) cheap instead of another fork.

### 4. Collapse the binding-scoped store twins

Merge the binding interfaces into the base with `bindingID *int64`, drop the runtime type assertion in favour of a compile-time check, and delete the dead `NewWorkflowResourceSourceAdmissionsFactory` constructor. ~370 LOC, plus it turns a runtime downcast into a compile-time guarantee. `FailBindingAdmission` is net-new behaviour with no generic counterpart and survives the merge unpaired — that's fine, it's a real capability.

### 5. Delete what has no implementation and no caller

- The 7 zero-implementation interfaces: `ApprovedBaselineAuthorityResolver`, `ImpactPolicyResolver`, `AuthoritativeImpactEvaluator`, `InitialPRFinalSnapshotInspector`, `InitialPRImpactVerifier`, `InitialPRObservationSealer`, `MonitorPipelinePolicyResolver`. Each has exactly one test double and one call site. Take the concrete type as a parameter until a second implementation exists.
- `atc/db/agent_pr_monitor_runs_factory.go` (300 lines, a `var _` compile-time assertion, and **zero references outside its own file, including tests**).
- The 6 `BindingStore` methods with no callers (`AttachRun`, `RequestObservation`, `Pause`, `Resume`, `Terminate`, `ListAudit`) — or, better, give them the API surface they were clearly written for. Right now operator control of a watched PR is SQL-only, which is its own finding.
- The five-times-duplicated `Protected()` / shadow-copy / `reflect.DeepEqual` idiom (~700 LOC across 24 call sites) defends in-process Go values against a caller mutating a struct between validation and use. The pattern is sound; five verbatim copies is not. One generic helper, or accept that the DB re-validation immediately downstream already closes it.

### 6. Reduce the initial-PR path to its five parts

`NewInitialPRCoordinator` mixes "open a PR" with "bootstrap the watch loop" — steps `:552-646` (re-observe → seal → reopen → create binding) are purely the latter. Split them. If the watch loop is branched or deleted, "agent opens a PR with its work" should remain a ~200-line path over the existing `ModeBranch` push plus one REST call, not a casualty.

And pick **one** idempotence design. The provider-neutral marker ref already works, is recoverable via `ls-remote`, and doesn't depend on parsing an HTML comment out of a mutable PR body.

---

## Appendix A — source of the findings

The complexity audit that produced finding #1 ("Provider-native PR publish + monitor — ~18.0k src / 17.2k test, 3 migrations, unreachable twice over") was run in the *Jetbridge complexity audit* session on 2026-08-04 (68 agents, 54 findings raised, 25 killed by adversarial refutation). Its report is not checked in; the ranked-findings table and the "complex but justified — do not delete these by mistake" list are worth preserving somewhere durable before that session ages out.

## Appendix B — verification

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./agent/pullrequest/... ./agent/publisher/... ./atc/atccmd/...` — all pass.
- `make test-unit` — see the session record; run after the removal with PostgreSQL up.
