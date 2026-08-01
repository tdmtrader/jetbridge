# Jetbridge First-User Findings

Status: completed first-user node trial; live blockers documented  
Date: 2026-08-01  
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
- No version was released. Import/freeze behavior is proven, but releasing a
  version that has never produced a valid typed output would violate the trial's
  lifecycle rule.
- After restoring the bounded slice, the final packages imported immutably as
  `code-review@9` (`abdabc1c464e…`) and `log-diagnosis@9`
  (`2b2c6824feda…`). These exact final versions remain unreleased and were not
  rerun because the same deployment-level budget flag would deterministically
  fail before inference.

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
- The shipped `fly agent snapshots create --from=<ordinary-directory>` is
  broken for nested trees. The Fly archiver writes directory headers with names
  such as `.claude/`; the server canonicalizer rejects that exact header as
  `snapshot: archive path ".claude/" has a trailing separator`. The public CLI
  collapses the cause to `400 invalid_archive: snapshot archive is invalid`, so
  a user cannot distinguish this client/server canonical-form mismatch from a
  corrupt file, unsupported mode, or contract failure. A client-side fix that
  emits `TypeDir` headers without the suffix now passes a real
  Fly-archiver/server-canonicalizer regression test and advances the live
  request past archive admission.
- After that compatibility fix, the same clean repository reaches typed
  validation but the live server rejects it as `422 validation_failed:
  snapshot does not satisfy its declared type`. The local current
  `repository/v1` validator accepts both the source and its canonicalized Fly
  archive, including after reducing `.git/config` to the three core settings
  needed by a normal work tree. The API suppresses the validator's reason, so a
  first user cannot tell whether the deployed validator differs from the client
  checkout or which repository invariant failed.
- The documented fallback, exact resource capture, found the requested retained
  Git version but terminated with only `500 internal_error`. Together with the
  opaque 422 above, both documented doors to a `repository/v1` input can fail
  without actionable diagnostics, blocking live code-review execution even
  though the node itself imported successfully.
- Adding `budget_slice_usd: 5` produced a valid imported node but its live run
  failed before the first model turn: the deployed Claude CLI rejects the
  runner's `--max-budget-usd` option. The node/run summary reported only
  `failed`; the actionable `unknown option '--max-budget-usd'` existed only in
  the underlying build log. Omitting the optional slice let the same node run
  under the deployment's existing max-turn/account controls and exposed the
  next failure, but zero means uncapped to the runner, so the final packages
  restore the $5 slice and remain unrunnable until the deployment is corrected.
  This is a runtime image/runner version-skew defect, not a safe authoring
  workaround or a reasoning failure.
- The managed `output-builder` MCP reported `status: failed` in every observed
  agent run. More seriously, when its server-owned activation bit is present,
  the runner previously replaced the sealed-record authority preamble with the
  builder instructions. A failed builder therefore left the model with neither
  the tool nor the exact input digest/output schema required for a valid
  fallback. The agent invented placeholders and sealing correctly rejected
  them. The local fix always includes exact authority and treats it as the
  builder's fallback.
- A failed direct run can be `failed` (CLI invocation) or `errored` (output
  sealing) but `nodes show-run --json` exposes neither the cause nor a link or
  suggested command for logs. A first user must know to take `planned_build_id`
  and run `fly watch -b`.

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
  acceptance cannot begin until that reviewed handoff completes. The dispatcher
  remains paused; the corrected commit, push, and pipeline retry are pending,
  and no rollout success is claimed.
- The corrected CI-fixture commit
  `ae40bf0d2b0ac4e7268260c9388c7f80e4375e72` was subsequently pushed. Set-self
  `645323`, build-and-vet `645324`, unit `645338`, k8s-runtime `645353`, tag-rc
  `645362`, build-image `645373`, self-upgrade `645388`, verify-upgrade
  `645391`, and k8s-live-tests `645394` succeeded. The web advanced to
  `0.2.221-rc`, but manual runner-image build `645354` checksum-verified and
  downloaded Claude `2.1.212` and built the required binaries before its
  build-time smoke exited 1. It did not push an image or publish a digest, so
  the runner remains old and the dispatcher remains paused.
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
  diff checks passed; independent final Task 4 review round 3 passed. The exact
  amd64 retry/digest evidence remains pending.
- Pipeline orchestration is also a platform pain point: runner-image is manual
  and not a dependency of self-upgrade or release, so the web RC advanced while
  the matching runner failed. Manual compatibility-window controls prevented a
  final mismatch: after the `0.2.221-rc` live tests, both `self-upgrade` and
  final `release` were manually paused before the next push; a status-only jobs
  query verified each has `paused:true` and `next_build:null`. This prevents a
  smoke-fix RC from deploying before matching runner activation. Pipeline
  dependency wiring is a follow-up candidate, not silently changed in this
  scoped remediation.
- Promotion gates remain paused. Web RC build `645496` for the now-superseded
  commit succeeded but was not promoted.

## Post-Trial Blocker Trace

The remediation-track audit converted four of the deployment symptoms into
exact repository defects and narrowed the remaining live 422 without claiming
an unproven cause:

- The exact resource capture did not fail while finding the Git version. It
  failed before creating its build because both the public `Capture` path and
  composition-only `CapturePersistedSelection` path in
  `agent/resourcecapture/capture.go` name the server template
  `agent-resource-capture-<operation-key[:24]>`, but
  `agent/workflowrun.TemplateSaver.SaveOrReuse` admits an immutable template
  only when its name ends in `-<target-config-hash[:12]>`. The permissive fake
  behind the capture unit test did not exercise that generic saver invariant.
  `FindResourceCaptureOutput` and the background finalizer also reconstruct the
  old name in SQL, so both constructors and both authorization queries must
  move together.
- The output-builder's `/healthz` endpoint proves only that its HTTP process is
  listening. Its MCP adapter handles `tools/list`, `tools/call`, and `ping`, but
  not `initialize` or `notifications/initialized`; its tool declarations also
  have neither descriptions nor input schemas. A real Claude MCP client can
  therefore mark the server failed even while the runner's health poll passes.
  The runner needs a managed-builder protocol preflight before starting a paid
  model session.
- The budget failure is an image contract mismatch, not an argument-construction
  bug. `agent/runner/runner.go` correctly emits `--max-budget-usd` for every
  positive slice, while `deploy/agent-runner/Dockerfile` installs Claude Code
  2.0.1. The repository already checksum-pins a native Claude 2.1.212 binary in
  the broker image. Converging on that artifact and smoking every load-bearing
  runner flag is safer than either dropping the cap or checking Dockerfile text
  alone.
- The live direct-upload 422 still has no proven semantic cause. The current
  local validator accepts both the clean source and the real Fly archive after
  server canonicalization, which makes deployed-code/runtime skew plausible but
  not established. The sealer retains the validator cause internally;
  `HandlerFactory.writeSnapshotError` intentionally replaces it with a fixed
  generic message. The safe repair is a closed allow-list of repository failure
  categories plus a full Fly-archive-to-real-validator regression, never raw Git
  stderr or configuration text.
- The log-correlation data already exists. `RunSummary.PlannedBuildID` is on the
  API response, and both node and workflow `show-run` commands share
  `printAgentWorkflowRunDetail`; that plain renderer simply omits the field.
  Printing the build ID and exact selected-target `fly watch -b` command is a
  bounded diagnostic improvement that does not expose the deliberately
  redacted database error message.
- `fly targets` does not authenticate before printing expiry. It labels any
  token format its local JWT expiry parser cannot decode as `invalid token`, so
  an opaque but usable bearer token is misreported. The honest local result is
  “expiry unavailable” with `fly -t <target> status` as the authenticated check.

The implementation design and task-by-task plan are recorded at
`docs/superpowers/specs/2026-08-01-jetbridge-first-user-blocker-remediation-design.md`
and
`docs/superpowers/plans/2026-08-01-jetbridge-first-user-blocker-remediation.md`.

## Effective Node-Authoring Patterns

- Keep typed-output mechanics in the prompt and reasoning method in a bundled
  skill. However, a bundled skill is advisory: in two live runs the model did
  not read the selected skill before authoring output. Load-bearing record shape
  cannot live only in `SKILL.md`; either the managed builder must be reliable or
  the exact fallback shape must be in the initial prompt/otherwise guaranteed.
- Use an unreleased exact version for direct testing; release only after a
  successful typed output has been inspected.
- Tell agents that record-authority names in the initial platform preamble are
  literal prompt values, not shell environment variables. Without that warning,
  one earlier run spent 40 turns searching `env` and the filesystem for values
  the runner intentionally surfaces in the prompt.
- Log-only diagnosis should distinguish a strongly supported symptom-level
  mechanism from a code-level root cause. The tested model correctly inferred
  pod-IP/node-name confusion, but without repository input it overclaimed
  `identified` and could not name the write/read seam required by the benchmark.
  For this corpus, a useful log node should emit `suspected` plus a bounded code
  inspection action; a deeper RCA node needs an optional or required repository
  input.

## Documentation Findings

- Add a visible reusable-node section or link to the platform guide. The current
  “undocumented capability” wording conflicts with the complete supported CLI
  lifecycle in the operations guide.
- State how to obtain or create suitable typed input snapshots for a node run.
  The node guide starts from snapshot IDs `101` and `102`, but a first user must
  discover `fly agent snapshots create` or `capture-resource` elsewhere.
- Add an end-to-end nested-directory case for `fly agent snapshots create`.
  Current Fly archive tests and server canonicalization tests cover their own
  formats but do not prove they agree on directory-header spelling.
- Explain whether a node author should normally omit `model`, rely on an
  operator-selected broker profile, or pin a model selector. The sample uses
  `claude-sonnet` without explaining portability across deployments.
- Document the failure-diagnosis path from `show-run.planned_build_id` to
  `fly watch -b BUILD-ID`, or surface the terminal build error directly in the
  node-run response.
- Document whether `budget_slice_usd` requires a minimum Claude CLI version and
  add runtime-image compatibility verification before a deployment advertises
  agent nodes as ready.
- State whether selected skills are automatically invoked or merely made
  discoverable. The live model did not reliably read them, which materially
  changes where an author must place contract-critical instructions.

## Corpus and Evaluation Findings

- `review-jb-003` is the cleanest first code-review fixture because its declared
  `before`/`after` → `review` signature exactly matches the current seed.
- Corpus harness metadata must stay out of the node inputs. Case titles,
  grading blocks, notes, and ground truth frequently disclose the answer by
  design; only the declared exposure should be materialized.

## Follow-Up Opportunities

- Make direct snapshot validation return the validator's safe reason instead of
  a generic 422, and make resource-capture terminal errors similarly actionable.
- Add a deployment readiness check covering: nested snapshot upload,
  `repository/v1` admission, managed output-builder health, Claude CLI budget
  flag compatibility, and one sealed record output.

## Verification and disposition

- `go test ./agent/workflow ./fly/commands -count=1` passed.
- `go test ./agent/runner -count=1` passed outside the filesystem/network
  sandbox required by its localhost HTTP tests.
- `git diff --check` passed.
- No reusable-node version was released. The final source packages retain a
  bounded $5 slice and therefore correctly fail on the version-skewed live
  runtime rather than silently running uncapped.
