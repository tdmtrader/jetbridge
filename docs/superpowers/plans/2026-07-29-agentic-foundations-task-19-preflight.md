# Task 19 preflight — final upgrade, behavior, and residue proof

Read this together with the semantic-rebase session context and Task 19 plan.
Use Terra agents by default. Run each broad suite once, serialize PostgreSQL
suites, and do not turn environmental setup failures into repeated diagnosis.

## Load-bearing addition

Add one focused `atc/db/migration` regression that proves both:

- a fresh database migrates to embedded head `1773106148`; and
- an exact `1773106138` database upgrades to `1773106148`.

Assert `CurrentVersion()`, `migrator.SupportedVersion()`, and the
`JETBRIDGE_VERSION` preflight target all equal `1773106148`. Task 14 should add
no migration, so that head remains authoritative.

Run the migration package serially. The shared fixed PostgreSQL port gets one
fresh availability-gated attempt. If suite setup fails before any spec runs,
record that exact environmental constraint once and retain the narrower
compile/migration evidence; do not repeat alternate-port experiments.

Also run:

```sh
bash docs/migration/migrate-preflight_test.sh
```

## Broad milestone — once, serially

Run in this order:

```sh
make test-unit
make test-dev-mcp
make test-fly-integration
make test-integration
helm lint deploy/chart
```

Do not run the PostgreSQL-bearing targets concurrently. After Task 14, use
focused suites only to localize a broad failure; do not rerun passing subsets
for additional evidence.

Focused semantic checkpoints are:

```sh
go test ./agent/publisher/... ./atc/exec ./atc/atccmd -count=1
go test ./agent/workflow ./atc/db -count=1
go test ./agent/outputbuilder ./cmd/agent-output ./agent/snapshot/... -count=1
go test ./atc/runtime ./atc/worker/jetbridge ./agent/runner ./cmd/agent-output ./deploy -count=1
```

## Hangar and Kubernetes evidence

Task 10 already proved the production GCS adapter against a temporary Borg
fake-GCS deployment, including immutable/idempotent writes, corruption and
truncation rejection, concurrent writers, and complete local-cache loss. Do
not rerun it merely to reproduce historical evidence.

The remaining live Borg proofs are narrower:

1. delete an artifact-daemon cache entry and restore it from its exact
   generation-pinned Hangar object; and
2. interrupt an agent pod/node in a preconfigured recovery environment and
   observe the documented capability-gated outcome.

Use only an already-approved image digest and preconfigured environment. Never
upload repository source, build or push a source-derived image, or persist one
in `registry.home` just to enable the proof. Make one bounded prerequisite
check. If the image/environment is absent, record the exact missing
prerequisite and mark only the live proof environment-pending.

Run local Kubernetes suites only when their prerequisites are already healthy:

```sh
make test-hangar-integration
make test-k8s-integration
make test-k8s-behavioral
```

The behavioral target is the highest-cost proof and runs at most once at the
final milestone. Record a concrete flaky spec rather than repeatedly rerunning
the whole target.

## Migration immutability

The semantic-rebase base is
`58a21a3cbdb5a51dfa92db621084e03d4c73dd82`. Prove the frozen base migrations
remain byte-for-byte unchanged:

```sh
git diff --exit-code 58a21a3cbdb5a51dfa92db621084e03d4c73dd82 -- \
  atc/db/migration/migrations/177310612{8,9}_* \
  atc/db/migration/migrations/177310613{0,1,2,3,4,5,6,7,8}_*
shasum -a 256 \
  atc/db/migration/migrations/17731061{28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48}_*.sql
```

The diff is the immutability gate. Record the checksums as evidence for the
appended migration block.

## Residue gates

Production residue must return no retired authority except the single
fail-closed Helm value tombstone described below:

```sh
rg -n -i \
  'agent_snapshot_grants|agent_principals|\bcap1\b|agent.?publisher.?gateway|publisher.?gateway|(^|/)agent/devmcp|ci-agent.*(phase|runner)|agent/functions/gates|retired.*(gate|function).*authorit|ticket.*terminaliz' \
  atc agent cmd deploy fly go-concourse ci-agent web \
  -g '!**/*_test.go' -g '!**/migrations/**'
```

After Task 14, the direct publisher cutover has its own exact-allowlist gate:

```sh
rg -n -i 'agent.?publisher.?gateway|publisher.?gateway|agent-publisher-gateway' \
  atc agent cmd deploy Dockerfile* docs/agentic docs/migration README.md \
  -g '!**/*_test.go'
```

The direct-publisher command must return exactly one logical, two-line
production-source tombstone in `deploy/chart/templates/web-deployment.yaml`:
`hasKey .Values "agentPublisherGateway"` immediately followed by the `fail`
that rejects the retired value with “has been removed; use agentPublisher.” Any
live gateway transport, service, flag, mount, or additional production-source
match fails the gate.

Other legitimate non-production matches are limited to:

- immutable migration SQL/fixtures for retired grants, principals, and cap1;
- rejection tests proving retired bearer tokens have no authority;
- retained `ci-agent/devmcp` (the forbidden path is root `agent/devmcp`);
- historical `docs/superpowers/**`, bench material, and old review artifacts;
- the workflow-run reconciler and ticket projector when auditing
  terminalization. No dispatcher/ticket loop may independently terminalize a
  run.

## Documentation and review

Update `docs/agentic/V3_CUTOVER_DEPLOY.md` from gateway delivery and head 6138
to direct in-ATC publication and head 6148. Update
`docs/migration/DATABASE-MIGRATION-RUNBOOK.md` to describe the complete appended
migration block rather than the older three-migration path.

Create a Task 19 evidence report with exact commands/results, migration
checksums, residue allowlist, Borg image/environment provenance, and every
bounded environmental skip.

The final independent review scope is:

```sh
git diff 58a21a3cbdb5a51dfa92db621084e03d4c73dd82...HEAD
```

Review it once by boundary: migrations/DB authority; snapshots/Hangar/builder;
workflow/source/validation; checkpoint/recovery; direct publication; API/Fly
and retained dev-MCP parity; and Helm/images/operator docs.
