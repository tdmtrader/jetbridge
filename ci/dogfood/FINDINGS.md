# Dogfood findings & pain points

A running log of friction hit while dogfooding the agentic-platform plans on
jetbridge (concourse.home). This is the raw material the process-intelligence
loop (plan 14) will eventually mine automatically; for now it's hand-kept.
Newest first.

## Plan gaps the agents found (leftward candidates)

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
