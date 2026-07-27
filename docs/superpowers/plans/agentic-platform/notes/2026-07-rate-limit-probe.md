# Rate-limit probe — decision memo

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../../2026-07-21-agentic-functions-program.md) are authoritative. This decision memo (shared rate-limit window) remains factually valid; it is retained here as a historical note under the abandoned ticket-centric wave-plan directory.

- **Status:** ANSWERED (owner-confirmed, 2026-07-11). No live probe run required.
- **Question (charter, plan 02 Task 3):** Does headless Claude usage (the
  `CLAUDE_CODE_OAUTH_TOKEN` path) share the owner's *interactive* rate-limit
  window, or does it draw from a separate budget?

## Answer: SHARED

Headless usage **shares** the owner's interactive rate-limit window. The
`anthropic-api-key` secret configured on concourse.home is in fact a Claude Code
**OAuth/subscription token** (`sk-ant-oat…`, from `claude setup-token`), not a
standalone API key — so every headless run (dogfood, agent-review, future
platform runs) consumes the same window as the owner's interactive Claude use.

This was confirmed directly by the owner rather than via the live probe harness
(plan 02 Task 2), so the live `agent/credentials/live_rate_limit_probe_test.go`
does not need to be executed to reach a decision. The harness remains useful as
a regression check if the credential model changes (e.g. a move to a true API
key, or per-user vaulted tokens once plan 02 Tasks 4–6 land).

## Design consequences (the charter's "if SHARED, redesign" branch)

1. **Budget defaults assume a shared ceiling.** There is no separate headless
   budget to spend freely overnight. The global daily cap
   (`budget.Config.GlobalDailyCapUSD`, plan 02 Task 7) must be sized against the
   owner's *total* Claude headroom, not an independent pool.
2. **Dispatch concurrency stays conservative.** Parallel/batch dispatch competes
   with the owner's interactive usage and with other runs for the same window.
   The dispatcher (plan 11) should default to low concurrency and treat the
   daily cap as the primary throttle — not assume it can fan out freely.
3. **Dogfooding spends the owner's window.** Every dogfood run draws from the
   same ceiling as interactive use, so dogfood dispatch is cost-and-headroom
   sensitive: prefer one run at a time; avoid speculative re-runs.
4. **Per-user vaulted tokens (Tasks 4–6) inherit this.** When work is attributed
   to a triggering user's own vaulted token, that run competes with *that user's*
   interactive window — the same sharing property, per user.

## Follow-ups unblocked by this answer

- Budget/daily-cap defaults can be set now (shared-ceiling sizing).
- The live probe harness (Task 2) is optional to merge; its value is now a
  future regression check, not a discovery tool.
