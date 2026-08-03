# Jetbridge First-User Findings

Status: `v0.2.227` rollout complete; reference-node reasoning accepted, typed publication blockers fixed locally and awaiting rollout
Date: 2026-08-03
Target: `home` / team `main`

This is a running record from authoring, importing, executing, and iterating on
reusable Jetbridge nodes. Observations describe behavior seen directly;
inferences and proposed follow-ups are labeled as such.

## Trial Log

### Orientation and discovery

- The polished `docs/platform-guide.html` explains typed workflow nodes but
  does not document the reusable-node catalog or its import/run/release CLI.
  It calls standalone extraction an “undocumented capability.” The actual user
  guide is `docs/operations/reusable-node-definitions.md`. A first user reading
  only the platform guide would not discover the supported node lifecycle.
- The reusable-node guide is unusually clear about immutable versions, exact
  workflow references, unreleased direct testing, compatibility, and explicit
  adoption. Those boundaries make node-level experimentation feel safer.
- The repository has one reusable-node sample, `code-review-node-v1`, alongside
  several workflow seeds. There is no reusable log-diagnosis sample even though
  its workflow contract and snapshot types are implemented.
- The benchmark corpus has 34 curated cases: six code-review cases and five
  log-diagnosis cases are directly relevant. Signatures vary across historical
  cases, so case count alone overstates how many fixtures fit a current node
  contract without adaptation.
- Some benchmark comments say `log-bundle/v1` and `diagnosis/v1` are proposed
  or absent, but both are now compiled into the snapshot registry and used by
  the log-diagnosis workflow seed. Corpus commentary can become stale as the
  platform catches up.

### Live-target access

- `fly targets` reported every saved target as `n/a: invalid token` even after
  authentication, while an actual authenticated API request succeeded. The
  status display is therefore not a reliable readiness check in this setup.
- The sandboxed first request to `http://concourse.home` failed DNS resolution.
  The identical `fly` command succeeded when allowed to use the host network.
  This is environmental rather than a Jetbridge defect, but an agent-first user
  needs to distinguish target authentication from execution-sandbox networking.
- The live reusable-node catalog initially returned `[]`. These imports are the
  first node versions on the target.
- The live snapshot catalog initially contained one `work-item/v1` snapshot.
- The target is shared: node versions and runs appeared between this trial's
  commands. Never assume “next version” is current+1; always use the exact
  version returned by import. This trial's log-diagnosis imports were allocated
  versions 3, 5, and 7 because other imports filled the intervening versions.

### Shipped code-review sample

- The unmodified `agent/workflow/seeds/code-review-node-v1` package imported
  successfully as `code-review` version 1 with content hash
  `6b94033ed21c62912e1cc830c1ac4321b2a7336cce75d45f84e5cc9641756662`.
  The package is complete: its prompt and selected review skill are both frozen
  into the immutable definition.
- Import validates the node definition but does not establish that capability
  images are pullable. Version 1 froze
  `registry.example/dev-mcp@sha256:aaaa...` plus
  `DEV_MCP_MCP_URL=http://127.0.0.1:8080/mcp`. The guide presents the same value
  as sample YAML without warning that it is a deliberately non-runnable
  placeholder. A first user can get a successful import and only discover the
  unusable image after paying the setup cost of a direct run.
- The sample pins `model: claude-sonnet` without explaining whether that model
  exists on a target or whether omitting it delegates selection to the
  deployment. The portable revision removes the deployment-specific selector.
- Local Go verification initially failed before compilation because the
  execution sandbox could not write the user's normal Go build cache. Pointing
  `GOCACHE` at a task-specific `/tmp` directory resolved that environmental
  issue. This is agent-harness friction rather than Jetbridge behavior.
- The useful test boundary is the compiled node record: it can prove that a
  source package freezes only intended ports, assets, model selection, and
  sidecars. Grepping prompt or skill prose would only test wording, so behavioral
  quality is evaluated through the live benchmark run instead.

### Authored log-diagnosis node

- A new atomic `log-diagnosis` package compiles the current contract
  `(logs log-bundle/v1, optional deployment deployment-snapshot/v1) ->
  diagnosis/v1`, freezes one prompt plus one diagnosis skill, selects no model,
  and requires no mutable sidecar capability.
- Exact version 3 froze correctly but failed before inference because its USD
  slice became an unsupported Claude CLI flag. Version 5 removed that runtime
  incompatibility and completed in 50 turns for about $0.18, but its invented
  record shape was rejected. Version 7 added a complete schema template and
  completed in 17 turns for about $0.09, yet did not read the bundled skill and
  again invented authority values and extra fields. The typed seal rejected the
  invalid digest, which is the correct safety behavior.
- Both reasoning runs found the central observable relationship: the value in
  the Nodes API error is the artifact-daemon pod IP and failures occur only on
  cache hits. Against the withheld rubric, that is a useful symptom-level
  diagnosis but not the complete root cause: the log-only exposure cannot prove
  which code writes the pod IP into a node-name field or trace its read-back.
- No version was released during this initial pass. Import/freeze behavior was
  proven, but releasing a
  version that has never produced a valid typed output would violate the trial's
  lifecycle rule.
- After restoring the bounded slice, the final packages imported immutably as
  `code-review@9`
  (`abdabc1c464e69fd3c38cbb8270d120ef4f9604433defa1a9a6b9b6d45761b12`)
  and `log-diagnosis@9`
  (`2b2c6824feda0a9d76728aeb6e91a4265885925b24d98bc202fe93aadaca41ea`).
  These exact final versions remained unreleased. They were not rerun in this
  initial pass because the then-deployed budget flag would deterministically
  fail before inference; later rollout and dogfood evidence is recorded below.

## What Works Well

- Importing a directory is genuinely one command. The server returns the exact
  allocated version and a useful hash prefix, and `nodes show --json` exposes
  both source and fully frozen compiled authority for inspection.
- The node compiler packages the selected skill tree and prompt into the
  immutable version. There is no hidden dependency on the author's local files
  after import.
- Direct node runs provide durable IDs and exact definition/input hashes
  immediately, and `show-run` cleanly preserves the terminal state. The
  underlying build ID makes detailed `fly watch -b` diagnosis possible even
  when the node-run summary itself has no error text.
- Snapshot download verifies and retrieves the exact canonical tar in one
  command. This made it easy to inspect the live log fixture without mixing in
  benchmark ground truth.

## Pain Points

- Import success is weaker than runnable-version readiness: an obviously
  placeholder capability registry/image is accepted and frozen without a
  warning.
- In the initial trial, the shipped
  `fly agent snapshots create --from=<ordinary-directory>` was broken for
  nested trees. The Fly archiver wrote directory headers with names
  such as `.claude/`; the server canonicalizer rejects that exact header as
  `snapshot: archive path ".claude/" has a trailing separator`. The public CLI
  collapses the cause to `400 invalid_archive: snapshot archive is invalid`, so
  a user cannot distinguish this client/server canonical-form mismatch from a
  corrupt file, unsupported mode, or contract failure. A client-side fix that
  emits `TypeDir` headers without the suffix now passes a real
  Fly-archiver/server-canonicalizer regression test and advances the live
  request past archive admission.
- The same canonical-path contract had a second producer mismatch on output:
  artifact-daemon serialized valid `TypeDir` entries as `candidate-1/`, while
  the snapshot canonicalizer accepts only the bare name `candidate-1` plus the
  type bit. Both final reference nodes reached this seam after good model work,
  and both terminal runs correctly errored without publishing a partial output.
  Commit `377e88983b` changes the real daemon `GET /artifacts` producer to bare
  directory names and preserves nested-file coverage; its focused and package
  tests plus independent review passed. This is distinct from the earlier Fly
  upload-side fix: every tar producer must honor the same canonical dialect.
- After that compatibility fix, the same clean repository reached typed
  validation but the then-live server rejected it as `422 validation_failed:
  snapshot does not satisfy its declared type`. The local current
  `repository/v1` validator accepts both the source and its canonicalized Fly
  archive, including after reducing `.git/config` to the three core settings
  needed by a normal work tree. The API suppresses the validator's reason, so a
  first user cannot tell whether the deployed validator differs from the client
  checkout or which repository invariant failed.
- The documented fallback, exact resource capture, found the requested retained
  Git version but then terminated with only `500 internal_error`. At that
  point, together with the
  opaque 422 above, both documented doors to a `repository/v1` input can fail
  without actionable diagnostics, blocking live code-review execution even
  though the node itself imported successfully.
- Adding `budget_slice_usd: 5` produced a valid imported node but its initial
  live run failed before the first model turn: the deployed Claude CLI rejected the
  runner's `--max-budget-usd` option. The node/run summary reported only
  `failed`; the actionable `unknown option '--max-budget-usd'` existed only in
  the underlying build log. Omitting the optional slice let the same node run
  under the deployment's existing max-turn/account controls and exposed the
  next failure, but zero meant uncapped to the runner, so the final packages
  restored the $5 slice rather than silently weakening the limit. This was a
  runtime image/runner version-skew defect, not a safe authoring workaround or
  a reasoning failure; the later 2.1.212 runner rollout closed it.
- The managed `output-builder` MCP reported `status: failed` in every initial
  agent run. More seriously, when its server-owned activation bit was present,
  the runner previously replaced the sealed-record authority preamble with the
  builder instructions. A failed builder therefore left the model with neither
  the tool nor the exact input digest/output schema required for a valid
  fallback. The agent invented placeholders and sealing correctly rejected
  them. The current fix track retains exact authority as the builder fallback,
  negotiates the pinned client's MCP version, and derives readiness from the
  provider-visible initialization event rather than the runner's private
  preflight alone.
- After version negotiation was deployed, Claude 2.1.212 could discover all
  three output-builder tools but every real call still returned MCP `-32602`.
  The logical arguments (`{"output":"diagnosis"}` and
  `{"output":"review"}`) were correct; strict decoding rejected the client's
  standards-compatible outer `params._meta.progressToken`/`params.task`
  envelope before arguments were examined. Commit `b5c7982d4f` makes only
  transport envelopes lenient while keeping tool arguments and CLI write input
  strict; real-builder regressions prove valid calls reach the builder and an
  unknown authority-bearing argument remains rejected. Its full package suite
  and independent security review passed.
- Provider-visible readiness had a separate exact-name mismatch. The runner
  allowed only `mcp__output_builder__...`, while the pinned provider emitted
  `mcp__output-builder__...`; runs therefore used the tools but recorded no
  `mcp.ready`. Commit `1ed9ef0a2b` recognizes exactly the three observed
  hyphenated tool names, retains legacy underscore forms, rejects lookalikes,
  and emits at most one event. Its full runner suite and independent review
  passed.
- In the initial CLI, a failed direct run could be `failed` (CLI invocation) or
  `errored` (output sealing), but `nodes show-run --json` exposed neither the
  cause nor a suggested command. That diagnostic gap was later closed; the
  historical workaround was to take `planned_build_id` and run `fly watch -b`.

## Remediation Rollout Gate

- The dispatcher was paused at `2026-08-01T19:21:33Z` for exact pushed/ref
  commit `72b831de8a8a482f4dbcf4afea60928423663de9`. Set-self build `645222` and
  build-and-vet build `645223` succeeded; unit-tests build `645231` consumed
  that exact ref and stopped the rollout before runtime image publication or
  deployment.
- The unit gate's `go test ./atc/exec -count=1` run passed 689/693 specs and
  exposed four shared-fixture failures. The new `mcp.ready` event at index 1
  had shifted later events while positional fixture truncations and mutations
  still used the old locations, including cost cases that had become latent
  false-greens. Production ingestion was not at fault. Named semantic event
  indexes repaired the fixture; the full package then passed 693/693
  (`ok github.com/concourse/concourse/atc/exec`), `git diff --check` passed,
  and independent Task 6 blocking review round 1 passed with no blocking
  findings.
- This is positive pipeline safety behavior: the full package gate caught
  shared-fixture semantic drift before deployment. It is also a node-platform
  pain point because focused feature tests did not expose that shared-fixture
  dependency.
- The repository pipeline can publish and verify the immutable runner digest,
  but external home-infra/ArgoCD owns its activation. Same-commit node
  acceptance cannot begin until that reviewed handoff completes. At this point
  in the historical sequence, the dispatcher remained paused and the corrected
  commit/pipeline retry was pending; later digest evidence is recorded below.
- The corrected CI-fixture commit
  `ae40bf0d2b0ac4e7268260c9388c7f80e4375e72` was subsequently pushed. Set-self
  `645323`, build-and-vet `645324`, unit `645338`, k8s-runtime `645353`, tag-rc
  `645362`, build-image `645373`, self-upgrade `645388`, verify-upgrade
  `645391`, and k8s-live-tests `645394` succeeded. The web advanced to
  `0.2.221-rc`, but manual runner-image build `645354` checksum-verified and
  downloaded Claude `2.1.212` and built the required binaries before its
  build-time smoke exited 1. It did not push an image or publish a digest, so
  at that point the runner remained old and the dispatcher remained paused.
- The runner failure was an instruction-order defect: its root smoke ran before
  `ENV IS_SANDBOX=1`, and `set -e` with captured Claude help made the root
  refusal silent. The repair first observed a RED Dockerfile-ordering test,
  then moved the unchanged sandbox contract before smoke and added bounded
  status-only Claude version/help errors plus named flag/binary diagnostics.
  The remote image smoke remains valuable executable acceptance evidence; static
  Dockerfile checks cannot replace a successful built-image smoke, immutable
  digest pull, and `linux/amd64` inspection.
- A retry against exact commit `47aaaf7b3efa4dded22bbba685e53bd678dce509`
  reached Dockerfile smoke twice (build `645476`) and failed before push/digest
  with bounded `ERROR: Claude help is missing required flag --max-turns`. The
  binary registers this option as hidden from top-level help and includes the
  expected missing-argument parser diagnostic. A `--version` probe was a false
  green because an unknown flag also exits 0. The replacement
  `claude --print --max-turns </dev/null` probe does zero work and distinguishes
  the registered missing-argument diagnostic from an unknown option without
  printing captured output. Shell syntax, focused regression, full deploy, and
  diff checks passed; independent final Task 4 review round 3 passed. This
  historical retry/digest gap was closed by the subsequent successful build.
- The subsequent runner-image build `645573` consumed exact commit
  `e8fe3fe2aa19ce3304c6f3329c7bb16b6814f847`, passed its Dockerfile and exact
  commit-tagged linux/amd64 smoke, then completed registry push, immutable pull,
  and platform-equality proof. It printed
  `CONCOURSE_AGENT_STEP_IMAGE=ghcr.io/tdmtrader/agent-runner@sha256:b677c8dd12efaaac383dafd38784988b5df8862c71bf9eec9b5b33f062d6beb7`.
  This closed the runner image capability gate; activating that digest remained
  outside the repository and inside the pending compatibility window.
- Pipeline orchestration is also a platform pain point: runner-image is manual
  and not a dependency of self-upgrade or release, so the web RC advanced while
  the matching runner failed. Manual compatibility-window controls prevented a
  final mismatch: after the `0.2.221-rc` live tests, both `self-upgrade` and
  final `release` were manually paused before the next push; a status-only jobs
  query verified each has `paused:true` and `next_build:null`. This prevents a
  smoke-fix RC from deploying before matching runner activation. Pipeline
  dependency wiring is a follow-up candidate, not silently changed in this
  scoped remediation.
- At that point promotion gates remained paused. Web RC build `645496` for the
  now-superseded commit succeeded but was not promoted.
- On the digest-producing exact commit, set-self `645540`, build-and-vet
  `645541`, unit `645556`, and k8s-runtime `645572` succeeded; matching web
  build `645591` succeeded, and `v0.2.221-rc` pointed at that exact commit.
  Self-upgrade and release were still paused. The deployment-commit package
  gate and focused serial DB spec (1/1) were green outside the sandbox because
  they required, respectively, loopback and shared memory.
- Read-only live evidence then showed Argo app `concourse` Synced/Healthy at
  chart revision `e8fe3fe...`, while the deployment still used old runner
  `registry.home/agent-runner@sha256:5551b...` and web tag `v0.2.221-rc`.
  Home-infra main `8dc7550...` owned that runner value. No direct `kubectl`
  mutation was made; the normal reviewed home-infra writeback remained the
  rollout authority.

## Rollout Writeback Iteration — 2026-08-01

- Live unit failures `646858` and `646895` exposed a test-fixture portability
  defect: a bare Git remote depended on its local default `HEAD` branch. The
  regression now explicitly reproduces a `master` default, advertises `main`,
  and asserts the resulting checkout; the correction was independently
  reviewed and passed its test suite.
- Runner build `646969` built, smoked, and pushed the runner image. Its helper
  committed home-infra change `3df501c`, but authoritative `home-infra/main`
  remained `f185d35`: the update task changed only an input and declared no
  modified repository output, so the native `put` consumed the original input
  and correctly reported `Everything up-to-date`.
- This was a useful safety result, not a successful promotion. Paused
  `self-upgrade` and `release` gates, together with checking the authoritative
  remote rather than task-local state, prevented a false-green rollout.
- The corrected design gave `home-infra-updated` its own output: it copied the
  checkout (including `.git`), the helper committed in that output, and a
  rebase-only native `put` consumed it.
- The deployable code commit was
  `a11caa172a422f48a5ed36b54e6c260bbd4b21fa`; `ccffd367e57e` was a docs-only
  audit head and was intentionally ignored by the repository resource. Local
  and final hermetic deploy tests, the broader deployment gate, and serial DB
  regressions passed. Automatic live build/vet, unit, runtime, and web-image
  gates also cleared.
- The final runner rerun was runner job build `#17`, global build `647509`, at
  that exact `a11caa...` source. Its image build/smoke, registry publication,
  and immutable digest verification succeeded at
  `sha256:41601273383151c877c9ca3a8586da80d26f130d3b9d371dd66795c0e5ba4bf4`;
  the reviewed writeback then converged `home-infra` at `113abb...`. This
  closes the earlier pending claim.
- Operational friction included a Fly token expiring mid-monitor while browser
  login required user-owned local credentials. Linked worktrees kept concurrent
  edits isolated. Reusable patterns were to reproduce CI-default assumptions
  hermetically, treat task output and the authoritative remote as distinct
  states, preserve paused gates until both agree, and use isolated worktrees
  for external-repository changes.

## Final Rollout and Renewed Dogfood — 2026-08-02

- Release replay job build `#152` (global build `653259`) deployed the already
  tested `v0.2.223` artifact from exact source
  `5240e3341a12a1f2a27a8a1d993e44fecdd46cad`. Its post-deploy writeback failed
  while replaying a stale home-infra checkout, even though the separately
  authoritative deployment converged. This is a truthful deployed-with-failed-
  replay result, not a failed release artifact. `origin/jetbridge` now contains
  the replay-safe correction at
  `76d4d0400f4bc17a253724dd69ab8df20c79519d`; the live `v0.2.223` deployment
  remains on the earlier tested source.
- Read-only deployment evidence confirms the current web source annotation is
  that exact `5240e334...` commit, the web image is
  `registry.home/jetbridge@sha256:d5584dc11df417f21d8d36c5b6605f31f2a2540d500d38cee771e57c7951ce18`,
  and the configured runner image is
  `registry.home/agent-runner@sha256:23f35a3ad9525afcfab50a04b45de517e8928984fb5d6ae9f24947310b516995`.
  These exact authorities supersede the historical RC and pending-digest
  statements above.
- Final dogfood deliberately reused the immutable imports `code-review@9`
  (`abdabc1c464e69fd3c38cbb8270d120ef4f9604433defa1a9a6b9b6d45761b12`)
  and `log-diagnosis@9`
  (`2b2c6824feda0a9d76728aeb6e91a4265885925b24d98bc202fe93aadaca41ea`).
  A new `log-bundle/v1` upload became snapshot ID `15`, digest
  `sha256:588a4b55b3bb3b932d735a27d559d220ae48681e9b7f7043ab1d372a9270f386`.
- `log-diagnosis` run `23` / build `653297` errored. The execution image no
  longer received the managed sealed-record authority, the provider reported
  `output-builder` as `pending`, and the runner nevertheless emitted its own
  readiness event. The model correctly refused to invent missing type, digest,
  and schema values; final sealing then failed because `record.json` was
  absent. This disproved the private-preflight-as-readiness assumption.
- The bounded repair track addresses all three root causes together: fallback
  authority is retained even with managed output-builder enabled
  (`acfbce576e`), the pinned Claude 2.1.212 initialize request negotiates down
  to the implemented MCP version (`d046eee3c4`, tightened by `29edfb3dd4`),
  and readiness is emitted only after the provider-visible initialization
  event (`806a5f1570`, with the bounded readiness-tool allow-list correction
  `d8d5645f11`). None of
  these later fixes is claimed as deployed by the `v0.2.223` evidence above.
- `repository/v1` dogfood established that the snapshot must contain a real
  Git working tree, including `.git`; a clone-generated
  `remote.origin.tagOpt` entry is rejected by the typed validator. This is a
  useful strictness boundary, but the user-facing reason must remain visible.
- Two concurrent direct uploads of roughly 225 MiB each exceeded the web
  request's 30-second response window while the 1 GiB fake-GCS pod was OOM
  killed. One content-addressed Hangar object with digest prefix `d53...`
  survived without its corresponding metadata. A later `Ensure` observes the
  object as already present and does not repair that missing metadata edge.
  No manual metadata repair or upload retry was performed; the observation is
  preserved as a durability defect rather than hidden by operator mutation.

### Source-bound `v0.2.227` and reference-node acceptance — 2026-08-03

- The final source-bound pipeline consumed exact commit
  `f0254d48c4ac5f473503f95e5f79bd4a103cf5c3`. Unit build `940`, Kubernetes
  runtime build `796`, runner-image build `#28` / global `654147`, web-image
  build `496` / global `654181`, self-upgrade `291`, verify-upgrade `264`, and
  live-test build `687` all succeeded before release build `#156` / global
  `654254` succeeded in 10m17s.
- Release `v0.2.227` deployed
  `registry.home/jetbridge@sha256:2e8ca0837ea16ecb31d97e6bc4a4909078dd69fc5d0be3072a3f341353ad9af6`.
  Both `concourse-web` and `concourse-artifact-daemon` carry that immutable
  image plus source annotation `f0254d48...`. The configured node runner is
  `registry.home/agent-runner@sha256:8434a0b74af80d8905050a2654035fa63021e20abaaa3bb3c6613bcafbf48e3a`.
  Argo applications `root` and `concourse` were independently observed
  `Synced/Healthy`.
- The release task restarted with the final ATC rollout, reattached, recognized
  the already-published stable tag/image, revalidated the source-addressed
  artifact, found Git main/tag and home-infra writeback already current, and
  completed successfully. This is the desired idempotent replay behavior for a
  deployment job that can outlive the server process coordinating it.
- Code-review input preparation produced real Git snapshots: `before` snapshot
  `17`, digest
  `sha256:030de599ca6d4ba5ee70f257e385259d2014c0c939080ca14c2c79b489bd14dd`,
  and `after` snapshot `18`, digest
  `sha256:db4478b725e91d1cc7a146be212002969609c97b8bff4ac0e14105bc8663faee`.
  The sole change replaces exact team equality with an unanchored prefix test,
  allowing team `dev` to administer `dev-prod`; existing tests intentionally
  remain green.
- `log-diagnosis@9` run `24` / build `654317` consumed snapshot `15` and exact
  definition hash
  `2b2c6824feda0a9d76728aeb6e91a4265885925b24d98bc202fe93aadaca41ea`.
  The model loaded the required diagnosis skill, completed 17 turns in 177s
  for `$0.558532`, separated the cache-hit failure chain from retries, anchored
  evidence/counterevidence, and identified the pod-IP-as-node-name mechanism at
  confidence `0.8` with bounded next actions. That is an appropriate scoped
  proximal diagnosis under the accepted product definition; it does not claim
  a repository-level RCA.
- The log agent discovered the managed tools but their calls returned `-32602`,
  so it used the documented fallback and authored a structurally strong
  `diagnosis/v1` record under `candidate-1/record.json`. Platform capture then
  rejected the directory header `candidate-1/`. The agent metric row says its
  reasoning step passed, while the enclosing build and durable node run say
  `errored` and expose no outputs. That split is accurate: useful model work is
  not a published typed result.
- `code-review@9` run `25` / build `654337` consumed snapshots `17`/`18` and
  exact definition hash
  `abdabc1c464e69fd3c38cbb8270d120ef4f9604433defa1a9a6b9b6d45761b12`.
  In 17 turns / 105s / `$0.427366`, it found the seeded authorization bypass,
  classified it critical and blocking, cited `after/access/access.go:4-8`,
  explained the trigger and impact, and recommended delimiter-aware matching
  plus positive/negative regression cases. This is a high-quality node result.
  Its fallback record then hit the identical `finding-broken-access-control/`
  capture failure, so the durable run correctly errored with no output.
- Run `25` also exposed parameter ergonomics: the API bound
  `MINIMUM_SEVERITY=medium` into the durable parameterized configuration and
  agent environment, but the initial context did not render it and the sample
  discouraged environment discovery. The model therefore said no threshold
  was supplied. The result was unaffected because its finding was critical,
  but parameter presentation is still a platform contract defect. The
  implementation track at
  `docs/superpowers/plans/2026-08-03-agent-readable-node-parameters.md`
  projects resolved parameters as canonical JSON into the durably hashed
  initial context while retaining environment compatibility and public-value
  redaction.
- The archive, MCP-envelope, and provider-readiness corrections are committed
  as `377e88983b`, `b5c7982d4f`, and `1ed9ef0a2b`. Fresh combined verification
  passed `go test ./agent/outputbuilder`, `go test ./agent/runner`, and
  `go test ./cmd/artifact-daemon`; each commit also passed its own distinct
  Terra review. They are not described as deployed until a new source-bound
  rollout and successful typed-output reruns prove that state.

## Post-Trial Blocker Trace

The remediation-track audit converted four deployment symptoms into exact
repository defects and narrowed the then-live 422 without claiming an unproven
cause. The bullets preserve what was true at audit time; current disposition is
added where later evidence closed or refined a claim:

- The exact resource capture did not fail while finding the Git version. It
  failed before creating its build because both the public `Capture` path and
  composition-only `CapturePersistedSelection` path in
  `agent/resourcecapture/capture.go` name the server template
  `agent-resource-capture-<operation-key[:24]>`, but
  `agent/workflowrun.TemplateSaver.SaveOrReuse` admits an immutable template
  only when its name ends in `-<target-config-hash[:12]>`. The permissive fake
  behind the capture unit test did not exercise that generic saver invariant.
  `FindResourceCaptureOutput` and the background finalizer also reconstructed
  the old name in SQL, so both constructors and both authorization queries had
  to move together. The subsequent capture path fixes advanced dogfood to the
  typed-input coverage seam, which is now repaired on `jetbridge` at
  `6de633e6e1`.
- At audit time, output-builder `/healthz` proved only that its HTTP process was
  listening. Its MCP adapter lacked `initialize`,
  `notifications/initialized`, descriptions, and input schemas, so the real
  client could mark it failed while the runner health poll passed. Protocol
  preflight and initialize negotiation now exist; final run 23 proved that
  provider-visible readiness and retained prompt authority are also required.
- The budget failure was an image contract mismatch, not an
  argument-construction bug. `agent/runner/runner.go` correctly emitted
  `--max-budget-usd` while the old runner image installed Claude Code 2.0.1.
  Convergence on the checksummed 2.1.212 artifact plus executable smoke closed
  that mismatch; dropping the cap was never accepted as a workaround.
- The initial direct-upload 422 had no proven semantic cause. The local
  validator accepted both the clean source and real Fly archive after server
  canonicalization, making deployed-code/runtime skew plausible but not
  established. The sealer retained the validator cause internally;
  `HandlerFactory.writeSnapshotError` intentionally replaces it with a fixed
  generic message. The safe repair is a closed allow-list of repository failure
  categories plus a full Fly-archive-to-real-validator regression, never raw Git
  stderr or configuration text. Later structured reasons exposed the concrete
  `.git` and `remote.origin.tagOpt` repository invariants described above.
- The log-correlation data already existed. `RunSummary.PlannedBuildID` was on
  the API response, while both node and workflow `show-run` commands shared a
  plain renderer that omitted it. Printing the build ID and exact
  selected-target `fly watch -b` command was the bounded diagnostic improvement
  later implemented without exposing the deliberately redacted database error.
- `fly targets` does not authenticate before printing expiry. It labels any
  token format its local JWT expiry parser cannot decode as `invalid token`, so
  an opaque but usable bearer token is misreported. The honest local result is
  “expiry unavailable” with `fly -t <target> status` as the authenticated check.

The implementation design and task-by-task plan are recorded at
`docs/superpowers/specs/2026-08-01-jetbridge-first-user-blocker-remediation-design.md`
and
`docs/superpowers/plans/2026-08-01-jetbridge-first-user-blocker-remediation.md`.

## Effective Node-Authoring Patterns

- Keep reasoning method in a bundled skill, but keep typed-output mechanics in
  the platform. A bundled skill is advisory: in two live runs the model did not
  read the selected skill before authoring output. Load-bearing record shape
  therefore cannot live only in `SKILL.md`, and node authors must not duplicate
  an inline JSON schema in prompts. The managed builder/validator (or another
  platform-injected schema-building tool) must supply that authority, expose
  safe validation errors to the agent, and own the bounded final repair pass.
- Use an unreleased exact version for direct testing; release only after a
  successful typed output has been inspected.
- Tell agents that record-authority names in the initial platform preamble are
  literal prompt values, not shell environment variables. Without that warning,
  one earlier run spent 40 turns searching `env` and the filesystem for values
  the runner intentionally surfaces in the prompt.
- Log-only diagnosis should distinguish a strongly supported proximal
  mechanism from a repository-level root cause. `identified` is appropriate
  when immutable logs directly support a bounded causal claim such as a
  deadlock, a panic site, or the pod-IP/node-name mechanism seen here; it must
  not imply that an unobserved code write/read seam has been proven. A deeper
  RCA node needs a disposable writable checkout and may leave a failing
  reproducer plus a proposed repository change.

## Accepted Product Defaults

The dogfood findings produced the following product decisions. These are
defaults, not speculative follow-ups:

- Reusable nodes are first-class platform objects and directly runnable. The
  product ships reference samples and a deliberately minimal team-visible
  catalog rather than hiding node execution behind workflows.
- `latest` is an authoring convenience resolved once, server-side, to one exact
  immutable version. A run, workflow import, retry, or rerun stores and reuses
  that exact version; there is no automatic update when a newer node appears.
- Schemas are platform-owned and versioned. Node packages reference a known
  schema/type and cannot embed an inline schema. Extensibility is limited to a
  bounded `extra_details` field so relevant evidence always has a legitimate
  home. A platform tool exposes the current schema and helps construct valid
  records. Validation returns structured, safe reason codes to the agent; the
  platform may invoke one bounded stop/final-repair pass, but publication is
  atomic and the run fails unless every declared output validates after that
  pass. Schema evolution creates a new schema version.
- There are two skill classes: immutable node/function skills bundled with the
  definition, and platform-required execution/policy skills. Required skills
  are injected automatically and cannot be silently omitted by a caller. The
  durable contract is the frozen definition plus result, not evidence that the
  model opened or quoted a particular skill file.
- Platform snapshots are passed explicitly to nodes. IDs are immutable, team
  scoped, and authorized through the owning team/resource boundary; the server
  stores the exact input bindings and uses them for exact rerun rather than
  accepting caller substitutions. A node run does not create snapshots
  automatically: callers supply existing IDs through explicit input flags.
  The UI must at least expose immutable IDs; repository pickers are optional.
  Private Git inputs inherit the same protections as a team-owned Concourse Git
  resource. Exact rerun inputs remain server-side authority and need not be
  conveniently downloadable to a local workstation.
- Code review should take two explicit `repository/v1` snapshots (`before` and
  `after`) and use a server-produced diff as a convenience/index, never as a
  replacement for either immutable repository authority.
- A diagnosis node may identify a scoped proximal cause supported by its
  immutable evidence. A deeper RCA node receives a disposable writable checkout
  derived from a repository snapshot, may write tests and leave a failing
  reproducer, and may emit a `repository-change/v1`; it never mutates the source
  snapshot itself.
- The node author chooses one exact model and that identity is part of the
  immutable, tested node contract. Callers cannot override it; if it is
  unavailable, execution fails rather than silently substituting another. The
  node also declares a deliberately high, non-interfering default budget, which
  a caller may explicitly override up or down for one durable run. Compatibility
  includes model identity, schema versions, bundled advisory and
  platform-required skill contracts, runtime compatibility, and tool/protocol
  surfaces. Exact replay freezes the concrete prompt, skill tree, runtime, and
  tool hashes/digests; release compatibility compares their declared contract
  IDs/versions so a prose-only prompt/skill refinement creates a new immutable
  hash without automatically becoming breaking. Cost enforcement remains
  mechanically available, but ordinary defaults should not constrain normal
  operation and the product does not need estimated-versus-actual cost detail.
- Diagnostics are agent-first: terminal surfaces expose safe structured reason
  codes and the exact next diagnostic command/tool, so an agent does not need
  private DB access or tribal knowledge to discover the underlying build.

## Documentation Findings

- Still open: add a visible reusable-node section or link to the platform
  guide. The current
  “undocumented capability” wording conflicts with the complete supported CLI
  lifecycle in the operations guide.
- Still open: state how to obtain or create suitable typed input snapshots for
  a node run.
  The node guide starts from snapshot IDs `101` and `102`, but a first user must
  discover `fly agent snapshots create` or `capture-resource` elsewhere.
- Closed in implementation: an end-to-end nested-directory archive regression
  now proves Fly and server canonicalization agree on directory headers; retain
  that behavior in user examples.
- Update model documentation to the accepted exact-selection default above:
  an agent node must declare the exact tested model, callers cannot override it,
  and an unavailable declared model fails rather than being silently
  substituted.
- Closed in the CLI: `show-run` now exposes the terminal reason/build
  correlation. Documentation should still show the direct
  `fly watch -b BUILD-ID` path as the next diagnostic tool.
- Closed as an image gate: the runner pins Claude 2.1.212 and executes a smoke
  for every load-bearing budget/MCP flag. Document that runtime compatibility
  requirement alongside `budget_slice_usd`.
- Replace the old ambiguity about skill discovery with the two accepted skill
  classes above. Required injection is platform behavior; proof that the model
  read a skill is not durable run authority.
- Document the platform-owned schema-builder/validator and its single bounded
  repair opportunity. Node examples should name output types and the optional
  `extra_details` home, not carry a second inline JSON schema.

## Corpus and Evaluation Findings

- `review-jb-003` is the cleanest first code-review fixture because its declared
  `before`/`after` → `review` signature exactly matches the current seed.
- Corpus harness metadata must stay out of the node inputs. Case titles,
  grading blocks, notes, and ground truth frequently disclose the answer by
  design; only the declared exposure should be materialized.

## Follow-Up Opportunities

- Repository validation now returns a closed safe reason instead of a generic
  422. Extend the same structured treatment to remaining record-body/archive
  failures and keep resource-capture terminal errors similarly actionable.
- Add a deployment readiness check covering: nested snapshot upload,
  `repository/v1` admission, managed output-builder health, Claude CLI budget
  flag compatibility, provider-visible `mcp.ready`, directory-valued output
  capture, and one sealed record output.
- Execute the approved snapshot durability track in
  `docs/superpowers/plans/2026-08-03-snapshot-upload-durability.md`; it preserves
  caller cancellation and team-scoped immutable authority while repairing only
  the exact metadata-less interrupted-upload state.
- Implement the agent-readable parameter track in
  `docs/superpowers/plans/2026-08-03-agent-readable-node-parameters.md`; its live
  acceptance probe requires an unpredictable bound value in the model's first
  actionable response before any tool call.
- Resolve the bind-once `latest` idempotency blocker recorded in
  `docs/superpowers/plans/2026-08-03-node-model-catalog-and-budget-contract.md`.
  The corrected draft now checks existing direct-run and identical
  workflow-import identities before any released-latest lookup. That track
  remains marked Human Review Required because this correction and the broader
  declared schema/skill/runtime/tool compatibility representation were not
  approved before its three-round review cap; no fourth agent review may
  substitute for the required human checkpoint.

## Verification and disposition

- `go test ./agent/workflow ./fly/commands -count=1` passed in the earlier
  authoring checkpoint.
- Fresh blocker-fix verification passed `go test ./agent/outputbuilder
  -count=1`, `go test ./agent/runner -count=1`, and `go test
  ./cmd/artifact-daemon -count=1`; each production fix also passed a distinct
  blocking review.
- `git diff --check` passed.
- No reusable-node version was released. Exact imports `code-review@9` and
  `log-diagnosis@9` remain the final dogfood authorities. The bounded $5 slice
  is supported by the deployed Claude 2.1.212 runner. Runs `24` and `25`
  completed high-quality model work but correctly ended `errored` with no
  outputs because the managed-tool envelope failed and the fallback output tar
  used a non-canonical trailing-separator directory header.
- Release `v0.2.227` and its exact web/runner authorities converged as recorded
  above. The three reviewed output-publication/readiness corrections remain
  local until the next source-bound rollout and successful typed-output reruns
  prove them deployed. The interrupted-upload orphan remains a preserved
  durability finding; it was not hidden by manual repair or retry.
