# Schema Delta: Upstream Concourse to JetBridge

This document catalogs every migration between upstream Concourse releases and JetBridge HEAD, documenting what each changes and whether it is destructive or additive.

Since JetBridge 0.4.0 the 300 upstream migration files up to and including
`1765921815` are one baseline, `1765921815_baseline.up.sql`: the upstream
Concourse 8.0.x schema as `pg_dump --schema-only` writes it. It runs only on an
empty database. A database at or past `1765921815` already has it; one below it
is refused with a message naming JetBridge 0.3.1, the last release that carries
the replaced files. Upgrade such a database with JetBridge 0.3.1 (or upstream
Concourse 8.0.x) first.

Every migration from the baseline on is forward-only: none has a down, and the
migrator refuses to reverse one on empty and populated databases alike.
Recovery requires restoring the pre-upgrade backup with its matching image and
encryption key.

## Version Boundaries

| Version | Last Migration | Upgrades directly to JetBridge HEAD? |
|---------|---------------|-----------------|
| v6.8.0 (last v6.x) | `1601993582` | No — through JetBridge 0.3.1 |
| v7.0.0 | `1612565824` | No — through JetBridge 0.3.1 |
| v7.14.3 (last v7.x) | `1746768931` | No — through JetBridge 0.3.1 |
| v8.0.0 | `1765921815` | Yes |
| v8.0.1 | `1765921815` | Yes |
| JetBridge 0.3.1 | `1773105509` | Yes |
| JetBridge HEAD | `1789793165` | — (baseline + 62 fork migrations) |

## Migration Gap by Source Version

### Baseline

| Migration | Name | Type | Destructive? |
|-----------|------|------|-------------|
| `1765921815` | `baseline` | **Schema** | No — creates the upstream 8.0.x schema on an empty database; never runs on an existing one |

It replaced `1510262030_initial_schema` through `1765921815_rerun_of_bigint`
(6,233 lines). Migrating an empty database through the baseline and through
those files produces byte-identical `pg_dump --schema-only` output, both at
`1765921815` and at `1789793164`.

### From a database below the baseline

Refused before anything changes, whichever table records its version
(`migrations_history`, `schema_migrations` or `migration_version`). Run
JetBridge 0.3.1 (or Concourse 8.0.x) against it first; it applies the replaced
migrations, including the expensive md5→sha256 rehash (`1747084615`), and then
this release applies the fork migrations below.

### From v8.0.x to JetBridge HEAD (61 migrations — JetBridge-only)

| Migration | Name | Type | Destructive? |
|-----------|------|------|-------------|
| `1773104944` | `simplify_worker_cache_triggers` | **Schema** | **Yes** — replaces notify triggers |
| `1773105500` | `drop_component_interval_and_last_ran` | **Schema** | **Yes** — drops columns |
| `1773105501` | `drop_component_paused` | **Schema** | **Yes** — drops column |
| `1773105503` | `add_deprecated_scope_columns` | **Schema** | No — additive; adds `deprecated_at` and `deprecated_from_resource_id` to `resource_config_scopes` with two partial indexes |
| `1773105504` | `add_resource_cache_durable_key` | **Schema** | No — additive; adds a nullable `durable_key` to `resource_caches` |
| `1773105505` | `add_pipeline_template_runs` | **Schema** | No — additive; creates `pipeline_runs`, adds template/params/retention columns to `pipelines`, 7 triggers |
| `1773105506` | `add_pipeline_run_build_identity` | **Schema** | No — additive, but see the risk note below: three full scans of `builds` under ACCESS EXCLUSIVE |
| `1773105507` | `add_run_task_cache_identity` | **Schema** | No — additive |
| `1773105508` | `skip_run_payload_event_partitions` | **Schema** | No — replaces `on_pipeline_insert()` so run payloads get no per-pipeline event partition |
| `1773105509` | `guard_run_payload_deletion` | **Schema** | No — adds a `BEFORE DELETE` guard on `pipelines` |

Migrations `1773105510` through `1789793165` (52 of them) are not catalogued here; read them in `atc/db/migration/migrations`.

v8.0.0 and v8.0.1 share identical migration sets. A JetBridge 0.3.1 database
applies only the migrations after its own head.

## Detailed Migration Descriptions

### `1765921815` — Baseline

- Creates the upstream 8.0.x schema, including the `pgcrypto` extension, on an
  empty database
- **Type:** Schema (empty database only)
- **Risk:** None on an existing database: it never runs there
- **Reversible:** No — there is no down

### `1773104944` — Simplify Worker Cache Triggers (JetBridge-only)

- Drops the generic `notify_trigger()` function that built JSON payloads
- Creates two simple functions: `notify_worker_event()` and `notify_container_event()` that fire bare `pg_notify`
- Recreates triggers on `workers` and `containers` tables

- **Type:** Schema change (triggers/functions)
- **Risk:** Low — only changes notification behavior
- **Duration:** Instant
- **Reversible:** No — restore the pre-upgrade backup

### `1773105500` — Drop Component Interval and Last Ran (JetBridge-only)

```sql
ALTER TABLE components DROP COLUMN IF EXISTS interval;
ALTER TABLE components DROP COLUMN IF EXISTS last_ran;
```

- **Type:** Destructive column drop
- **Risk:** Low — these columns are unused in JetBridge (component runner uses hardcoded intervals)
- **Duration:** Instant
- **Reversible:** No — restore the pre-upgrade backup

### `1773105501` — Drop Component Paused (JetBridge-only)

```sql
ALTER TABLE components DROP COLUMN IF EXISTS paused;
```

- **Type:** Destructive column drop
- **Risk:** Low — unused in JetBridge
- **Duration:** Instant
- **Reversible:** No — restore the pre-upgrade backup

### `1773105505`–`1773105509` — Pipeline templates and numbered runs (JetBridge-only)

Adds `pipelines.template`, `pipelines.params`, `pipelines.pipeline_run_id`,
`pipelines.last_run_number` and the run-retention columns; creates the
`pipeline_runs` table; adds run identity to `builds` and `jobs`; skips
build-event partition creation for run payloads; and installs the guard trigger
on payload deletion.

- **Type:** Additive schema, plus one trigger-function replacement
  (`on_pipeline_insert`) and nine triggers, two of which are CONSTRAINT triggers
- **Risk:** Medium — `1773105506` adds a validating CHECK on `builds`, which
  takes ACCESS EXCLUSIVE for a full heap scan; duration scales with build history
- **Duration:** Proportional to `builds` row count; measure on a restored copy first
- **Reversible:** No — restore the pre-upgrade backup, even for an unused install.

## Key Observations

1. **The schema delta is no longer small.** Sixty-one migrations are JetBridge-specific. Five of
   them (`1773105505`–`1773105509`) add pipeline templates and numbered runs, and one of
   those, `1773105506`, is the most expensive migration this release can run — see Observation 4.
2. **Every migration from the baseline on is forward-only.** None has a down;
   the migrator refuses a downgrade before changing anything, regardless of
   retained data. Restore the database backup before starting its matching
   older image; an older image does not reliably reject a newer schema.
3. **Databases older than v8.0.x upgrade in two steps** — JetBridge 0.3.1 (or
   Concourse 8.0.x) first, which runs the md5→sha256 rehash on v7.x sources,
   then this release.
4. **`1773105506` locks `builds` ACCESS EXCLUSIVE for three full scans.** In one transaction it
   validates a new foreign key on `builds.pipeline_run_id`, verifies a new CHECK constraint, and
   builds a non-concurrent index. Because the migration runner wraps each file in a single
   transaction, none of these can be deferred with `NOT VALID` or `CONCURRENTLY` without splitting
   the file. Duration scales with `builds` row count; every in-flight build's status write and event
   insert blocks for the whole transaction. **Measure on a restored copy before upgrading a busy
   cluster.** No `ANALYZE builds` is run afterwards.

## 1789793158 — retire container-count notifications (C5)

Drops `containers_notify_trigger` and `notify_container_event()` because the
worker cache no longer counts build containers. Worker INSERT/UPDATE/DELETE
notifications, container data and the pod-derived `ActiveContainers` API field
remain. Recovery requires the pre-upgrade database backup.

## 1789793159 — simplify_run_contract (C7)

Drops the Run activation table and monotonic trigger, the birth and causation
triggers/constraints, and `pipeline_runs.activation_epoch`,
`run_contract_version`, `caused_by_run`, `correlation` and
`terminal_observation_version`. Terminal shape and immutability remain, as do
the deferred exact-result/claim consistency checks. Reclamation history and
`reclaim_retry_after` remain unchanged.

The migration locks and refuses a nonempty `pipeline_run_invocations` table
before changing schema: removing fields changes the attested documents. It
does not rewrite guarded invocation rows. Stop all web nodes, reconcile
uncertain submissions and complete credential handoffs, back up the database,
then explicitly `TRUNCATE TABLE pipeline_run_invocations;` if it is nonempty.
Retries with old keys can then create new Runs; old credential handoffs lose
their invocation-owner evidence, and composition callers must not replay old
calls. See [the rollout procedure](../../deploy/chart/MIGRATING.md#c7-run-contract-simplification).
It has no down: the migrator refuses to reverse it. Restore the pre-upgrade backup to recover
the previous schema and discarded evidence.
