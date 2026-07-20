# Dogfood findings & pain points

A running log of friction hit while dogfooding the agentic-platform plans on
jetbridge (concourse.home). This is the raw material the process-intelligence
loop (plan 14) will eventually mine automatically; for now it's hand-kept.
Newest first.

## UX4 native-build enablement (2026-07-20, filing S-1..S-6 + W-items as tickets)

- **UI/Elm work is NOT loop-buildable yet — the runner image has no Elm toolchain
  and no workflow runs the elm-build gate. This blocks dispatching every UX ticket
  (S-1 DAG, S-2 transcript, S-3 IA, S-4 diff, S-5 web-loop, S-6 workflows, W-5/10/11/13).**
  WF-2 (merged this session, live at 5daa0678e3) added `elm 0.19.1` to
  `deploy/agent-runner/Dockerfile` and an `elm-build` harvest gate to `agent/harvest`,
  but three things are still missing before an agent can actually ship an Elm change:
  (1) the runner image was never rebuilt/bumped — it is still `agent-runner:v0.2.196`
  with no `elm` binary (WF-2 Task 6 is post-merge ops, deliberately deferred);
  (2) `uglify-js` was intentionally left OUT of the Dockerfile because the *gate* only
  needs `elm make` — but an *agent implementing a change* needs `elm make` **and**
  `uglifyjs` to run `hack/build-web.sh` and regenerate the committed
  `web/public/elm.min.js`; without uglifyjs the agent cannot produce the very bundle
  the gate diff-checks for, so the gate would fail-closed on every Elm ticket;
  (3) neither `develop` (v2, opus) nor `develop-fable` (v4) declares an `elm-build`
  gate, so even on a fixed image the stale-bundle guard would not run.
  → *Enablement checklist to make UI tickets loop-buildable:* add `uglify-js` to
  `deploy/agent-runner/Dockerfile` (next to the elm layer); rebuild+push the
  agent-runner image; bump `CONCOURSE_AGENT_STEP_IMAGE` in home-infra
  `apps/concourse.yaml`; import a `develop-elm` workflow version whose gate_policy
  includes `- gate: elm-build`. Until all four land, UI tickets can be *filed* but not
  *dispatched* — filing them anyway (drafts) is correct so the backlog is visible.

- **The `develop`/`develop-fable` gate command map is Go-only (`go build/test/lint`).**
  The fixed `gateCommands` in `agent/harvest/gates.go` plus these two workflows cover
  Go slices well (that is why the backend tickets #43/#44 and S-6's server half are
  loop-ready today), but there is no front-end build/test in the loop at all until the
  elm enablement above lands. Backend-only tickets dispatch cleanly now; anything that
  touches `web/elm/**` does not.

- **needs_review backlog is real friction, exactly as the audit predicted.** #41, #42,
  #45, #46 all have their code merged + deployed to prod, yet the *tickets* still sit in
  `needs_review` (their agent runs finished days ago). Dispositioning is a manual
  `fly agent tickets` step with no nudge. #41 is a special case: its original agent run
  correctly STOPPED at the status-CHECK-constraint migration gate, and the actual fix was
  implemented out-of-band this session (migration 1773106092), so the ticket must be
  *concluded as implemented elsewhere*, not merged from an `agent/ticket-41` branch that
  never carried the fix. → *Signal:* a ticket whose work lands via a different path than
  its own agent branch has no clean disposition verb; "concluded" is the least-wrong.

- **Migration coordination is now load-bearing for the ticket queue.** #41 consumed slot
  `1773106092`; S-6 (workflow lifecycle) and any future schema ticket must number ABOVE it
  and merge in ascending order (the migrator is version-pointer based — a lower number
  merged after a higher one deploys is silently never applied; see the migration-merge-order
  rule below). File migration-bearing tickets with an explicit "claim the next free slot
  above the current head; do not hard-code a number from a stale plan doc" instruction.

- **`fly agent workflows import` validates the gate vocabulary CLIENT-SIDE, and
  `fly sync` is broken — so an out-of-date local fly cannot import a workflow that
  uses a just-shipped gate.** Importing `develop-elm` (which declares `- gate: elm-build`)
  from a local fly v0.2.195 failed with `gate must be build|test|lint, got "elm-build"`
  even though the deployed web (6d4b4811ff) already accepts it — fly parses the YAML
  itself before POSTing. `fly -t home sync` then 500s: the web image does not ship
  `fly-assets/fly-*.tgz`. → *Workaround:* build fly from the target commit locally
  (`go build -o /tmp/fly ./fly`) and import with that. → *Leftward fix:* either make
  workflow import server-validated only, or bundle the fly assets in the release image so
  `fly sync` works.

- **Enabling front-end tickets took THREE new pieces, not one.** WF-2 shipped the
  `elm-build` gate + `elm` in the runner image, but making a UI ticket actually
  loop-buildable also needed: (1) `uglify-js` in the runner image (the agent runs
  `hack/build-web.sh` = `elm make` + `uglifyjs`; WF-2 deliberately omitted uglify because
  the *gate* only needs `elm make`); (2) a `develop-elm` workflow whose gate_policy adds
  `elm-build` AND whose prompt tells the agent to regenerate + commit `elm.min.js`; (3) a
  separate `develop-gated` workflow for BACKEND tickets, because the prompt is per-workflow
  — a front-end prompt ("work in web/elm, rebuild the bundle") is actively wrong for a Go
  ticket, and the base `develop` workflow has NO gate at all while `develop-fable` is
  fable-only. → *Signal:* "add a gate" and "add a toolchain" are not enough to make a new
  work-kind loop-buildable; the workflow prompt is the third leg and it is work-kind-specific.

- **WF-5 (this session) proven live:** `fly agent tickets queue --id 43 --workflow develop-gated`
  assigned the workflow and queued in one step, then `dispatch --id 43` ran it — the
  empty-workflow dead-end the audit found is closed end-to-end.

- **A turn-capped run pushes an EMPTY branch and is marked needs_review — the ticket-loop
  harvest has no no-op guard.** #43 (flight-events, a six-touchpoint + persistence + tests
  feature) dispatched on `develop-gated` (max_turns 100) hit the cap: the run log shows
  `{"type":"result","subtype":"error_max_turns","num_turns":100,"is_error":false,"total_cost_usd":5.98}`.
  The implement agent committed NOTHING before the cap; because claude-code reports max-turns
  as `is_error:false`, the harvest treated the run as clean, ran the `build` gate against the
  UNCHANGED workspace (`base_sha == head_sha == 6d4b4811ff`) where it passed trivially, and
  pushed `agent/ticket-43` at the base sha — an empty branch — with summary "1 gate(s) ok;
  pushed agent/ticket-43" and ticket → needs_review. $5.98 spent, zero work delivered, looks
  successful. The dogfood-pipeline.yml runner has an explicit empty-branch guard ("A blocked
  agent fails loudly"); the ticket-loop harvest (agent/harvest) does NOT. → *Two leftward
  fixes:* (1) harvest must FAIL (errored, not needs_review) when `head_sha == base_sha` /
  zero commits — a gate that runs against an unchanged tree is meaningless; (2) treat
  `error_max_turns` as a run failure (or at least surface it), not a clean finish.
  → *Workflow-level mitigations applied here:* bumped develop-gated/develop-elm max_turns to
  250 and added "commit after each logical chunk" so partial work survives a cap and the
  branch is never empty. Re-dispatched #43.

- **These S-track tickets may be too big for one run.** #43 is a whole plan-doc feature; the
  agent burned 100 turns (8.2M cache-read tokens) without finishing. The proven loop strike
  zone (cf. #16–40) was smaller slices. If the higher turn cap still caps out, the S-tickets
  need slicing (each plan doc → 2–3 sub-tickets by task range, like ci/dogfood/dispatch.sh's
  task-range model).

- **CONFIRMED SYSTEMATIC: the ticket loop pushed TWO empty no-op branches for #43 and both
  read as successful — the loop is not reliably materializing agent work into the pushed
  branch.** Run 42 (develop-gated v1, sonnet): `error_max_turns` at 100 turns, $5.98, empty
  branch. Run 43 (develop-gated v2, sonnet, max_turns 250 + commit-incrementally): terminated
  `subtype:"success"` at only 48 turns / $2.15 / ~30k output tokens, yet ALSO pushed
  `base_sha == head_sha` (empty). So the second failure is NOT the turn cap and NOT lack of
  commits-guidance — the agent did ~30k tokens of work that never reached the `outputs:
  [workspace]` dir the harvest pushes. Prime suspect: the resolve-once workspace protocol
  (cp repo→$AGENT_OUTPUT_WORKSPACE, work + commit in WS) is still fragile under sonnet — the
  agent likely works/commits in `repo/` or a mis-resolved path, leaving the WS output at base.
  This is the #16 empty-expansion class the develop-v2 comment claims to have fixed; it is not
  fixed for this path. **Debugging is blocked by the absence of transcript persistence** — the
  build log carries only the flight-recorder `result` event, not the agent's tool calls, so
  there is no way to see WHERE it wrote (ironically the exact capability tickets #43/#49
  would add). ~$8 spent on two empty runs. → *Load-bearing leftward fixes, in priority order:*
  (1) harvest MUST fail a run whose pushed head == base (no-op guard) — this alone stops the
  money-burning "successful empty run"; (2) persist the agent transcript so workspace-protocol
  failures are debuggable at all (#43/#49); (3) make the workspace materialization robust
  (have the platform own the repo→WS copy, or have the harvest read the agent's actual cwd,
  rather than trusting the agent to hand-resolve $AGENT_OUTPUT_WORKSPACE). Until (1)+(3) land,
  the ticket loop cannot be trusted to build these tickets — build them the way Waves A/B/#41
  were built (directly, adversarially reviewed) OR harden the loop first.

- **The WF-2 elm pre-warm broke the agent-runner image build — the no-op guard + transcript
  capture were never actually deployed until this was fixed.** `build-agent-runner-image` #8
  (intended v0.2.200) and #9 (v0.2.201) both FAILED at the Dockerfile step
  `cd /tmp/elm-prewarm/web/elm && elm make --optimize ...`: the build pod has no egress to
  package.elm-lang.org, so `elm make` cannot resolve the repo's Elm deps (exactly WF-2 Open
  Decision #4's warning). The `elm` binary (curl from github) and `uglify-js` (npm) installs
  SUCCEEDED — only the package-registry pre-warm failed. CONSEQUENCE: the registry has no
  v0.2.200/v0.2.201; home-infra pointed CONCOURSE_AGENT_STEP_IMAGE at a non-existent tag, so
  dispatched run 44 errored with `failed to pull and unpack image: NotFound` (22s). The web
  deploy was unaffected (it uses jetbridge:latest, not agent-runner), which masked the failure
  — the poll had read the *attempted* tag out of the build log, not a successful push. LESSON:
  after triggering build-agent-runner-image, VERIFY the tag actually landed in the registry
  (`/v2/agent-runner/tags/list`) before bumping home-infra — a build-log tag string is not proof
  of a push. → *Fix applied:* removed the pre-warm layer (keep elm+uglify binaries); a UI ticket
  will need run-time egress to package.elm-lang.org or a vendored ELM_HOME (deferred).

## Plan gaps the agents found (leftward candidates)

- **Agent replace-instead-of-add error in exactly the code the gate can't test.**
  Task 5 (build `run/1`) told the agent to *add* `submitted_by = EXCLUDED.submitted_by,`
  to `agent_reviews_factory.go`'s ON CONFLICT SET list; the agent *replaced*
  `review = EXCLUDED.review,` with it instead — re-submitted reviews would keep
  the stale payload. The full gate passed anyway because the factory spec is
  Postgres-backed (can't run in the gate image), and the existing upsert spec
  used an identical payload for both upserts so it couldn't have caught the drop
  even locally. Caught in human review; fixed and the spec strengthened to vary
  payload + submitted_by across the conflict (verified: the strengthened spec
  fails against the agent's version). → *Signals:* (1) the human local-verify
  rule for Postgres-backed slices is load-bearing, not ceremonial; (2) upsert
  specs must assert every ON CONFLICT column that matters, not just one.

- **Adding an API route touches SIX places, not four — the plans list ~four.**
  Dogfooding agent-identity Task 4 (build 525330) failed the gate on
  `atc/auditor` `TestAuditor` ("all routes are handled and does not panic"):
  `atc/auditor/auditor.go`'s `ValidateAction` switch panics on any action not
  explicitly cased, and the three new principal routes weren't added. The agent
  also independently hit + fixed a second missing touchpoint —
  `atc/wrappa/reject_archived_wrappa.go`'s exhaustive switch (same panic
  pattern). Neither is in Task 4's file list; the agent's plan-stated test
  commands (`ginkgo ./atc/api/ ./atc/wrappa/`) didn't cover `atc/auditor`, so
  the gate's full `go test` caught what the agent's narrower run missed — the
  gate doing exactly its job. **The complete add-a-route checklist for this
  fork:** (1) `atc/routes.go` rata entry, (2) `atc/api/handler.go` name→handler,
  (3) `atc/wrappa/api_auth_wrappa.go` auth switch, (4) `atc/wrappa/reject_archived_wrappa.go`
  switch, (5) `atc/auditor/auditor.go` `ValidateAction` switch, (6) `atc/api/accessor/roles.go`
  DefaultRoles (for authorized routes). → *Leftward fix:* every route-adding
  task (agent-identity T4, credentials T11/T13, ticket-core, pipeline-runs,
  costs/credentials handlers, …) must list all six; amend those plans, or add a
  single `add-a-route` convention note they reference. Note: build 525330's
  branch did NOT push (gate failed correctly), so Task 4 needs re-dogfooding
  after the plan is amended.

- **Migration-head bumps must also touch `docs/migration/migrate-preflight.sh`.**
  Dogfooding agent-identity Task 2 (build 525203), the agent found that
  `migrate-preflight.sh` hardcodes its own `JETBRIDGE_VERSION` constant — a
  duplicate of `jetbridgeHeadMigration` (`legacy_upgrade_test.go`) that the
  migration test suite checks. Every plan task that bumps the head migration
  (agent-identity T2, credentials T5/T8/T10, ticket-core, …) must sync BOTH, but
  only the Go constant is documented. The F1–F40 review couldn't catch this — it's
  test-infra state, not code logic. Verified locally (15/15 specs pass with the
  fix). → *Leftward fix:* amend every migration-bearing plan's head-bump step to
  touch both constants, or add a single `migration-head-bump` convention note all
  such tasks reference. Prevents each future migration task rediscovering it.

## Loop / harness friction

- **Pushing to `jetbridge` mid-dogfood-run restarts web and double-spends the
  agent.** Build 525330's log shows the implement task ran TWICE (two full
  Claude sessions, two `dogfood-implement: pass` results) plus three gate
  executions. Cause: every push to `jetbridge` triggers the self-release chain
  (build-and-vet → build-image → release → self-upgrade), and self-upgrade
  restarts the web node ~10-12 min after the push. A restart mid-build is
  survived (build resumes rather than errors — the build-survival work doing
  its job) but the resumed build re-runs the implement step from scratch
  because the worked-repo volume died with the old task pod. The morning docs
  push (`748a797a1b`) landed minutes before Task 4 was dispatched; its
  self-upgrade restarted web at 15:58Z, mid-gate, and the whole
  implement+gate sequence re-ran. Every such restart costs a full agent run
  out of the owner's shared rate-limit window.
  → *Operational rule:* batch pushes BEFORE dispatching; never dispatch until
  the release chain triggered by your push has finished self-upgrade and the
  web pod is stable (`fly builds` shows no jetbridge/* in flight; check
  self-upgrade completed).
  → *Leftward fix candidates:* (a) path-filter the release chain's git
  resource to ignore docs-only commits (`docs/**`, `ci/dogfood/FINDINGS.md`),
  so plan/log commits stop triggering deploys; (b) make the resumed build
  reuse the implement step's committed output instead of re-running it
  (worked-repo is gone with the pod, but the dogfood.json/summary artifacts
  could be persisted); (c) teach dispatch.sh to refuse/warn when a
  self-upgrade is pending.

- **UI (Elm) work is not dogfoodable on the current gate.** The dogfood test
  gate runs `go test` only; Elm needs `elm make` / `elm-test` plus in-browser
  visual verification. Workstream 15 (platform-home) is therefore human-built,
  or the pipeline needs an Elm build/test capability first.
  → *Leftward fix:* add an optional Elm gate to `dogfood-pipeline.yml` (detect
  `web/elm` changes, run `elm-test`); still can't auto-verify visuals.

- **Review acted as a hard gate and blocked good branches.** A single-task
  slice legitimately looks "incomplete" to the diff reviewer (e.g. a library
  referencing a type an out-of-range later task implements), so the review
  returned `fail` and failed the whole build — no branch pushed, despite the
  code passing build+test. Fixed: review is now **advisory** (publishes to the
  build page, never gates); build + test-quick are the only hard gates
  (`ed712898eb`). Matches the platform design (judge/review informs, human
  decides).
  → *Signal:* per-slice review needs whole-workstream context to score fairly;
  worth feeding the review the full plan, not just the diff, later.

- **`fly trigger-job` reuses the SERVER-STORED pipeline config.** Editing
  `dogfood-pipeline.yml` and re-triggering did nothing — task params + guard
  scripts come from the last `set-pipeline`. `dispatch.sh` re-runs
  `set-pipeline` every time, so always dispatch through it; only hand-trigger
  after a manual `set-pipeline`. (Cost me one wasted run against the shared
  window.)

- **Migrations must merge in ascending version order.** The migrator is
  version-pointer based: a lower-numbered migration merged/deployed AFTER a
  higher one is silently skipped. Don't dogfood migration tasks ad-hoc — build
  and merge them lowest-number-first. Postgres-backed migration/factory tasks
  also can't be verified by the Go gate (no postgres in the test image), so
  verify locally with `pg_isready` before merge.

## Security

- **Credential leak into build logs (fixed).** The credential-routing WARN
  echo embedded `((anthropic-api-key))` literally, so Concourse interpolated
  the OAuth token into the build-log *script display* of every build. Fixed to
  a literal message (`ed712898eb`). Lesson: never put a `((secret))` var inside
  a task's `run:` script body — pass it as a param (redacted) and reference the
  env var. Exposed builds: 522770/522862/522910/522977/524905/524907 (owner
  accepts the risk on a private-LAN instance; not rotating).

## Runtime / environment

- **Headless claude write permissions (F25, confirmed empirically).** The first
  *write* workload hit claude's headless permission wall — every `Write` denied
  ("you haven't granted it yet"), zero commits, yet the phase reported `pass`.
  Fixed: opt-in `--dangerously-skip-permissions` via `AGENT_SKIP_PERMISSIONS`
  (review-phase read-only path unchanged) + `IS_SANDBOX=1` (root image needs it
  for claude to accept the flag) + the implement task now fails loudly on zero
  commits (`2f0f3d1071`). This validated review-finding F25 before any platform
  code shipped.

- **Rate-limit window is SHARED (owner-confirmed).** The configured
  `anthropic-api-key` is an OAuth/subscription token; headless usage shares the
  owner's interactive window. So dogfood dispatch spends the owner's Claude
  budget → strictly one run at a time, no speculative re-runs, conservative
  dispatch concurrency. See `notes/2026-07-rate-limit-probe.md`.

## What's working well

- The loop faithfully executes a reviewed plan verbatim — code quality tracks
  plan quality (the F1–F40 review effort pays off at execution). Real-code
  proof: `agent/api/principals` (build 522977) and `agent/budget` (build
  525048) — both gate-verified and human-reviewed clean.
- The zero-commit guard + push-task refusal caught the blocked-agent case
  (defense in depth) before any bad branch could be pushed.
- `verify-state-not-transcripts` in practice: the CI gate running the agent's
  tests (not the agent's self-report) is what makes a green build trustworthy.
