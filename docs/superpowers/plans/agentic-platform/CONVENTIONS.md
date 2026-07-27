# Cross-plan conventions (normative)

> **SUPERSEDED — historical implementation record (not executable).** The approved [Agentic Workflows as Functions over Snapshots design](../../specs/2026-07-21-agentic-workflows-as-functions-design.md) and [Agentic Functions program plan](../2026-07-21-agentic-functions-program.md) are authoritative. These cross-plan rules governed the abandoned ticket-centric wave plans; check current code and `CLAUDE.md` for the conventions actually in force.

Recurring rules every agentic-platform plan task MUST follow. Plans reference
these checklists with one-line pointers instead of restating them; each rule
exists because an executing agent already got it wrong once (see
`ci/dogfood/FINDINGS.md` for the incidents).

## C1. Add-a-route six-touchpoint checklist

Adding an entry to `atc/routes.go` touches SIX places, all in the SAME commit.
Two of them are exhaustive switches that PANIC on any unlisted route — the
panics are the enforcement, so run `go test ./atc/wrappa/... ./atc/auditor/...`
before considering the task done.

- [ ] `atc/routes.go` — route-name constant + route-table entry
- [ ] `atc/wrappa/api_auth_wrappa.go` — the auth-tier switch (authenticated /
      authorized / pass-through case, per the route's contract tier)
- [ ] `atc/wrappa/reject_archived_wrappa.go` — exhaustive switch (panics
      "how do archived pipelines affect your endpoint?" on a miss)
- [ ] `atc/auditor/auditor.go` — `ValidateAction` switch (panics on a miss;
      NOT covered by `ginkgo ./atc/api/ ./atc/wrappa/` — test it explicitly)
- [ ] `atc/api/accessor/roles.go` — `DefaultRoles` entry (required for
      authorized-tier routes)
- [ ] `atc/api/handler.go` — name→handler map, PLUS its two `NewHandler` call
      sites: `atc/api/api_suite_test.go` and `atc/atccmd/command.go`

## C2. Migration-head-bump dual-constant rule

The head-migration version is hardcoded in TWO places. Any task that adds a
migration bumps BOTH to the same new value, in that task's commit:

- [ ] `atc/db/migration/legacy_upgrade_test.go` — `jetbridgeHeadMigration`
- [ ] `docs/migration/migrate-preflight.sh` — `JETBRIDGE_VERSION`

Only the Go constant is exercised by the test suite; the preflight script is
operational tooling and drifts silently if forgotten. Related ordering rule:
migrations must merge lowest-version-first (the migrator is version-pointer
based and silently skips a lower-numbered migration merged after a higher one).

## C3. ADD, never REPLACE

When a plan step says to add an entry to a list, switch, SQL column set,
`ON CONFLICT ... DO UPDATE SET` clause, or params list, ADD it alongside the
existing entries — never substitute it for one. After every such edit, diff
and confirm no pre-existing line was dropped. (A prior run replaced
`review = EXCLUDED.review,` with the new column and shipped a data-loss bug
the gate could not catch.)

Corollary for upsert specs: assert EVERY `ON CONFLICT` column that matters,
with values that differ between the first insert and the conflicting upsert —
a spec that reuses the same payload across the conflict cannot detect a
dropped `SET` entry.
