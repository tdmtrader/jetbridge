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
| JetBridge HEAD | `1773105501` | 153 |

## Migration Gap by Source Version

### From v7.14.3 to JetBridge HEAD (5 migrations)

| Migration | Name | Type | Destructive? |
|-----------|------|------|-------------|
| `1747084615` | `switch_md5_to_sha256` | **Schema + Data** | **Yes** — renames `version_md5` columns to `version_digest`, adds `version_sha256` column, rehashes all `resource_config_versions` rows from MD5 to SHA256. Requires `pgcrypto` extension. |
| `1765921815` | `rerun_of_bigint` | **Schema + Data** | **Yes** — renames `rerun_of` to `rerun_of_old`, adds new `rerun_of bigint` column, recreates indexes. Runs `ANALYZE builds` to update stats. |
| `1773104944` | `simplify_worker_cache_triggers` | **Schema** | **Yes** — drops the JSON-payload `notify_trigger()` function and worker/container triggers, replaces with simpler bare-NOTIFY trigger functions. |
| `1773105500` | `drop_component_interval_and_last_ran` | **Schema** | **Yes** — drops `interval` and `last_ran` columns from `components` table. |
| `1773105501` | `drop_component_paused` | **Schema** | **Yes** — drops `paused` column from `components` table. |

### From v8.0.1 to JetBridge HEAD (3 migrations — JetBridge-only)

| Migration | Name | Type | Destructive? |
|-----------|------|------|-------------|
| `1773104944` | `simplify_worker_cache_triggers` | **Schema** | **Yes** — replaces notify triggers |
| `1773105500` | `drop_component_interval_and_last_ran` | **Schema** | **Yes** — drops columns |
| `1773105501` | `drop_component_paused` | **Schema** | **Yes** — drops column |

### From v8.0.0 to JetBridge HEAD (3 migrations)

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

## Key Observations

1. **The schema delta is small.** Only 3 migrations are JetBridge-specific, and they are all minor column/trigger changes.
2. **The md5→sha256 migration is the most expensive** — it rehashes every row in `resource_config_versions`. Plan for this on large instances.
3. **All migrations are reversible** — every `.up.sql` has a corresponding `.down.sql`.
4. **v7.x users have the biggest gap** — up to 29 migrations depending on the exact v7.x version.
5. **v8.0.0 and v8.0.1 are identical** in terms of migrations — only 3 JetBridge-specific migrations need to apply.
