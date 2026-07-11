# Dogfood findings & pain points

A running log of friction hit while dogfooding the agentic-platform plans on
jetbridge (concourse.home). This is the raw material the process-intelligence
loop (plan 14) will eventually mine automatically; for now it's hand-kept.
Newest first.

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
