# Task 7 — selected node-consumer HTTP and Fly surfaces

## Delivered

- Added exact-version consumer discovery with bounded tuple-keyset pagination.
  The API accepts only canonical `limit` and opaque canonical cursors, rejects
  malformed store pages, and projects workflow/node database IDs as quoted
  decimals so values above JavaScript's safe-integer range round trip exactly.
- Added selected-workflow upgrades with the strict body
  `{"workflows":[...]}`. Unknown query/body fields, duplicate selections,
  non-canonical versions, unsupported media types/encodings, and oversized
  requests fail before the upgrade service is called.
- Validated and sorted every per-workflow service result before returning it.
  The surface exposes only `created`, `unchanged`, `failed`, and
  `recomposition_required`; it contains no promotion operation.
- Registered both routes through the main-team authorization boundary:
  consumers require Viewer and upgrades require Member. Both bypass archived
  pipeline rejection because they are team-level agent routes.
- Added `fly agent nodes consumers NAME VERSION` with strict opaque cursor
  handling and `fly agent nodes upgrade NAME VERSION --workflow NAME`
  with repeatable, duplicate-free workflow selection. Both support JSON and
  deterministic table output.

## TDD evidence

- RED: the initial focused node-upgrade API test failed because
  `agent/api/nodeupgrades` had no production implementation.
- RED: route/role tests failed before the consumer and upgrade constants,
  explicit Viewer/Member roles, main-team wrapper entries, and archive
  pass-through entries existed.
- RED: Fly consumer/upgrade tests failed with undefined request helpers before
  the commands were implemented.
- GREEN: consumer tests now prove the exact node/version store request,
  canonical tuple cursor, bounded and exact store results, and quoted-decimal
  workflow/node IDs above `2^53`.
- GREEN: upgrade tests prove the body contains only distinct selected
  workflows, trusted identity reaches the service, results are deterministic,
  and unknown fields/queries are rejected without mutation.

## Verification

Passed on 2026-07-29:

`go test ./agent/api/nodeupgrades ./fly/commands -run 'Node.*(Consumer|Upgrade)' -count=1`

`go test ./agent/workflow ./agent/api/nodeupgrades ./fly/commands -count=1`

`go test ./atc/api/accessor ./atc/wrappa ./atc/api -count=1`

`go test ./atc/atccmd -count=1`

`git diff --check`

## Core handoff

This half consumes the locked `workflow.NodeUpgradeService` contract and
constructs it from the existing node/workflow stores. It does not modify
manifest rewriting, immutable import outcomes, consumer persistence, database
bindings, migrations, or promotion behavior.
