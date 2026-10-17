# Garden-Era Stale Data Reference

After migrating to JetBridge's Kubernetes runtime, several tables contain data that is no longer relevant. This document catalogs them and provides cleanup guidance.

## Tables: Garden rows only before first boot

These tables tracked Garden-specific per-worker caching. JetBridge writes them too (resource and task caches on its volumes), so before JetBridge's first boot every row is Garden-era and they may be truncated; on a running install delete only rows whose `worker_name` is not the synthetic worker.

| Table | Purpose (Garden) | K8s Use | Cleanup |
|-------|------------------|---------|---------|
| `worker_base_resource_types` | Cached resource type images per Garden worker | None | `TRUNCATE` |
| `worker_resource_caches` | Per-worker resource cache entries | Resource caches on JetBridge volumes | `TRUNCATE` before first boot only |
| `worker_task_caches` | Per-worker task step cache paths | Task caches on JetBridge volumes | `TRUNCATE` before first boot only |

## Tables: Partially Stale (Row-Level Cleanup)

These tables are still used by JetBridge but contain rows referencing Garden workers.

| Table | Garden Rows | K8s Rows | Cleanup Strategy |
|-------|------------|----------|-----------------|
| `workers` | Rows with `addr` pointing to Garden/TSA endpoints | the synthetic worker registered by `k8s_worker_registrar` | Delete rows whose `name` is not `k8s-<namespace>` |
| `containers` | Rows with `worker_name` referencing Garden workers | K8s-created containers | GC will naturally clean these; or delete where `worker_name` references deleted workers |
| `volumes` | Rows with `worker_name` referencing Garden workers | K8s-created volumes | GC will naturally clean these; or delete where `worker_name` references deleted workers |
| `worker_artifacts` | Rows from Garden builds | Rows from K8s builds | No action needed; build history preserved |

## Tables: Unchanged (No Cleanup Needed)

Core pipeline data tables are structurally identical and fully preserved:

- `teams` — unchanged
- `pipelines` — unchanged
- `jobs` — unchanged
- `builds` — unchanged (build history preserved)
- `resources` — unchanged
- `resource_types` — unchanged
- `resource_configs` — unchanged
- `resource_config_versions` — unchanged (versions preserved)
- `resource_config_scopes` — unchanged
- `build_events` — unchanged (build logs preserved)

## Cleanup SQL

See the [Database Migration Runbook](DATABASE-MIGRATION-RUNBOOK.md), Step 6, for executable cleanup queries.

## What Happens If You Don't Clean Up

Leaving Garden-era data in place is **safe** — it causes no runtime errors. The data is simply unused:

- Old worker rows will have expired `expires` timestamps and won't be selected by the scheduler
- Old container/volume rows will be garbage-collected naturally by the GC components
- Worker cache tables consume disk space but don't affect queries

Cleanup is recommended for large instances to reclaim disk space and simplify operational queries.
