# Schema Delta: Upstream Concourse to JetBridge

This document catalogs every migration between upstream Concourse releases and JetBridge HEAD, documenting what each changes and whether it is destructive or additive.

## Version Boundaries

| Version | Last Migration | Total Migrations |
|---------|---------------|-----------------|
| v6.8.0 (last v6.x) | `1601993582` | ~114 |
| v7.0.0 | `1612565824` | 124 |
| v7.14.3 (last v7.x) | `1746768931` | 148 |
| v8.0.0 | `1765921815` | 150 |
| v8.0.1 | `1765921815` | 150 |
| JetBridge HEAD | `1773105509` | 160 |

## Migration Gap by Source Version

### From v7.14.3 to JetBridge HEAD (10 migrations)

| Migration | Name | Type | Destructive? |
|-----------|------|------|-------------|
| `1747084615` | `switch_md5_to_sha256` | **Schema + Data** | **Yes** — renames `version_md5` columns to `version_digest`, adds `version_sha256` column, rehashes all `resource_config_versions` rows from MD5 to SHA256. Requires `pgcrypto` extension. |
| `1765921815` | `rerun_of_bigint` | **Schema + Data** | **Yes** — renames `rerun_of` to `rerun_of_old`, adds new `rerun_of bigint` column, recreates indexes. Runs `ANALYZE builds` to update stats. |
| `1773104944` | `simplify_worker_cache_triggers` | **Schema** | **Yes** — drops the JSON-payload `notify_trigger()` function and worker/container triggers, replaces with simpler bare-NOTIFY trigger functions. |
| `1773105500` | `drop_component_interval_and_last_ran` | **Schema** | **Yes** — drops `interval` and `last_ran` columns from `components` table. |
| `1773105501` | `drop_component_paused` | **Schema** | **Yes** — drops `paused` column from `components` table. |
| `1773105505` | `add_pipeline_template_runs` | **Schema** | No — additive; creates `pipeline_runs`, adds template/params/retention columns to `pipelines`, 9 triggers |
| `1773105506` | `add_pipeline_run_build_identity` | **Schema** | No — additive, but see the risk note below: three full scans of `builds` under ACCESS EXCLUSIVE |
| `1773105507` | `add_run_task_cache_identity` | **Schema** | No — additive |
| `1773105508` | `skip_run_payload_event_partitions` | **Schema** | No — replaces `on_pipeline_insert()` so run payloads get no per-pipeline event partition |
| `1773105509` | `guard_run_payload_deletion` | **Schema** | No — adds a `BEFORE DELETE` guard on `pipelines` |

### From v8.0.1 to JetBridge HEAD (8 migrations — JetBridge-only)

| Migration | Name | Type | Destructive? |
|-----------|------|------|-------------|
| `1773104944` | `simplify_worker_cache_triggers` | **Schema** | **Yes** — replaces notify triggers |
| `1773105500` | `drop_component_interval_and_last_ran` | **Schema** | **Yes** — drops columns |
| `1773105501` | `drop_component_paused` | **Schema** | **Yes** — drops column |
| `1773105505` | `add_pipeline_template_runs` | **Schema** | No — additive; creates `pipeline_runs`, adds template/params/retention columns to `pipelines`, 9 triggers |
| `1773105506` | `add_pipeline_run_build_identity` | **Schema** | No — additive, but see the risk note below: three full scans of `builds` under ACCESS EXCLUSIVE |
| `1773105507` | `add_run_task_cache_identity` | **Schema** | No — additive |
| `1773105508` | `skip_run_payload_event_partitions` | **Schema** | No — replaces `on_pipeline_insert()` so run payloads get no per-pipeline event partition |
| `1773105509` | `guard_run_payload_deletion` | **Schema** | No — adds a `BEFORE DELETE` guard on `pipelines` |

### From v8.0.0 to JetBridge HEAD (8 migrations)

Same as v8.0.1 — v8.0.0 and v8.0.1 share identical migration sets.

### From v7.0.0 to JetBridge HEAD (29 migrations)

In addition to the 5 listed above, v7.0.0 users must apply 24 intermediate migrations covering:
- Job/pipeline cascade deletes and ordering indexes
- Resource config scope last_check tracking
- Prototypes table
- Build comments
- Worker cache triggers (original version)
- Job/pipeline pause tables
- Container cleanup (removing image check/get columns)
- `int` to `bigint` conversions for IDs
- Resource config scope bigint conversion
- Worker resource cache `invalid_since` column
- Signing keys table

## Detailed Migration Descriptions

### `1746768931` — Add Signing Keys (present in v7.14.3+)

```sql
CREATE TABLE signing_keys (
    kid text PRIMARY KEY,
    kty text NOT NULL,
    jwk json NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
```

- **Type:** Additive (new table)
- **Risk:** None — purely additive
- **Duration:** Instant

### `1747084615` — Switch MD5 to SHA256 (present in v8.0.0+)

- Adds `version_sha256` column to `resource_config_versions`
- Renames `version_md5` to `version_digest` in 5 tables: `build_resource_config_version_inputs`, `build_resource_config_version_outputs`, `next_build_inputs`, `resource_caches`, `resource_disabled_versions`
- Rehashes **all** `resource_config_versions` rows from MD5 to SHA256 using `pgcrypto`
- Recreates unique constraints and indexes on the new column

- **Type:** Schema change + full table data migration
- **Risk:** HIGH — full table scan and update on `resource_config_versions`. Requires `pgcrypto` extension.
- **Duration:** Proportional to `resource_config_versions` row count. Can take minutes on large instances (millions of rows).
- **Reversible:** Yes — down migration recalculates MD5 digests from SHA256

### `1765921815` — Rerun of Bigint (present in v8.0.0+)

- Renames `builds.rerun_of` (integer) to `rerun_of_old`
- Adds new `builds.rerun_of` as `bigint`
- Recreates ordering indexes to reference both columns
- Runs `ANALYZE builds` to update query planner statistics

- **Type:** Schema change + index rebuild
- **Risk:** Medium — index recreation on `builds` table
- **Duration:** Proportional to `builds` table size
- **Reversible:** Yes

### `1773104944` — Simplify Worker Cache Triggers (JetBridge-only)

- Drops the generic `notify_trigger()` function that built JSON payloads
- Creates two simple functions: `notify_worker_event()` and `notify_container_event()` that fire bare `pg_notify`
- Recreates triggers on `workers` and `containers` tables

- **Type:** Schema change (triggers/functions)
- **Risk:** Low — only changes notification behavior
- **Duration:** Instant
- **Reversible:** Yes — down migration restores JSON-payload trigger

### `1773105500` — Drop Component Interval and Last Ran (JetBridge-only)

```sql
ALTER TABLE components DROP COLUMN IF EXISTS interval;
ALTER TABLE components DROP COLUMN IF EXISTS last_ran;
```

- **Type:** Destructive column drop
- **Risk:** Low — these columns are unused in JetBridge (component runner uses hardcoded intervals)
- **Duration:** Instant
- **Reversible:** Yes — down migration adds columns back with defaults

### `1773105501` — Drop Component Paused (JetBridge-only)

```sql
ALTER TABLE components DROP COLUMN IF EXISTS paused;
```

- **Type:** Destructive column drop
- **Risk:** Low — unused in JetBridge
- **Duration:** Instant
- **Reversible:** Yes — down migration adds column back with default `false`

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
- **Reversible:** **No, once used.** Every `.down.sql` calls
  `ensure_pipeline_template_runs_empty()` and raises if any template, run payload
  or run header exists. Down from an unused install works; down from a used one
  is a restore-from-backup operation. See Key Observation 3.

## Key Observations

1. **The schema delta is no longer small.** Eight migrations are JetBridge-specific. Five of
   them (`1773105505`–`1773105509`) add pipeline templates and numbered runs, and one of
   those, `1773105506`, is the second most expensive migration in the set — see Observation 6.
2. **The md5→sha256 migration is the most expensive** — it rehashes every row in `resource_config_versions`. Plan for this on large instances.
3. **Migrations are reversible up to `1773105501`.** The five pipeline-template
   migrations (`1773105505`–`1773105509`) are **one-way once the feature is used**:
   each `.down.sql` calls `ensure_pipeline_template_runs_empty()`, which raises if
   any template, run payload or run header exists. This is deliberate — the down
   path refuses rather than silently discarding run history. Rolling a binary back
   after a template has been created means restoring the database from backup.
4. **v7.x users have the biggest gap** — up to 29 migrations depending on the exact v7.x version.
5. **v8.0.0 and v8.0.1 are identical** in terms of migrations — 8 JetBridge-specific migrations need to apply.
6. **`1773105506` locks `builds` ACCESS EXCLUSIVE for three full scans.** In one transaction it
   validates a new foreign key on `builds.pipeline_run_id`, verifies a new CHECK constraint, and
   builds a non-concurrent index. Because the migration runner wraps each file in a single
   transaction, none of these can be deferred with `NOT VALID` or `CONCURRENTLY` without splitting
   the file. Duration scales with `builds` row count; every in-flight build's status write and event
   insert blocks for the whole transaction. **Measure on a restored copy before upgrading a busy
   cluster.** No `ANALYZE builds` is run afterwards, unlike `1765921815`.
