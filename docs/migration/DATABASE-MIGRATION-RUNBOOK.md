# Database Migration Runbook: Concourse → JetBridge

This runbook guides operators through migrating a Concourse v8.0.x database to JetBridge Edition. JetBridge is forked from Concourse v8.0.1 and its earliest migration is a baseline of the v8.0.x schema, so migration is a direct database upgrade — not an export/import.

A database older than v8.0.x (below migration `1765921815`) is refused with a message naming JetBridge 0.3.1. Upgrade it with JetBridge 0.3.1 (or Concourse v8.0.x) first, following this runbook as it shipped with that release, then return here.

## Prerequisites

- **Source version:** Concourse v8.0.0 or v8.0.1, or JetBridge 0.3.1 or later. Older databases go through JetBridge 0.3.1 first.
- **PostgreSQL:** 13 or later (JetBridge requirement)
- **JetBridge binary:** The `concourse` binary from a JetBridge release
- **Database access:** A PostgreSQL user with schema modification privileges
- **pg_dump / pg_restore:** Available on the migration host
- **Downtime window:** Plan for Concourse downtime during migration

## Overview

```
┌─────────────────────────────────────────────────────┐
│  1. Pre-flight   → Validate source DB               │
│  2. Backup       → pg_dump the database              │
│  3. Migrate      → Apply pending migrations          │
│  4. Validate     → Verify data integrity             │
│  5. Cleanup      → Remove Garden-era stale data      │
│  6. Boot         → Start JetBridge against the DB    │
└─────────────────────────────────────────────────────┘
```

---

## Step 1: Pre-flight Validation

Run the pre-flight script to validate the source database:

```bash
./docs/migration/migrate-preflight.sh \
  --host <pg-host> \
  --port 5432 \
  --dbname concourse \
  --user concourse \
  --password <password>
```

The script checks:
- Database connectivity and PostgreSQL version
- Current schema version and detected Concourse release
- Migration path validity (is the source at or past the baseline `1765921815`?)
- Data integrity: row counts, orphaned records, failed migrations

**Do not proceed if the pre-flight reports any FAIL results.**

---

## Step 2: Backup

Create a fresh full `pg_dump` immediately before **every** upgrade, including
self-upgrade. Existing dumps do not establish freshness or restorability.
For the single-web Kubernetes deployment, first suspend Argo reconciliation
and self-upgrade, quiesce builds and stop web as described under
[Rollback Procedure](#rollback-procedure), then dump the external PostgreSQL
database while writes remain stopped. Keep it stopped through migration.
The self-upgrade job does not take this backup automatically.

Record the pre-upgrade image digest, database migration version, dump filename
and checksum, and the version of the separate database-encryption Secret.
Securely preserve the matching key independently of the dump. A dump without
the key cannot recover encrypted configuration.

```bash
pg_dump \
  --host=<pg-host> \
  --port=5432 \
  --username=concourse \
  --dbname=concourse \
  --format=custom \
  --compress=zstd \
  --verbose \
  --file=concourse-backup-$(date +%Y%m%d-%H%M%S).dump
```

**Recommended flags:**
- `--format=custom` — Enables selective restore and parallel restore
- `--compress=zstd` — Fast compression; use `--compress=9` for gzip if zstd unavailable
- `--verbose` — Shows progress during dump

**Verify the backup:**

```bash
pg_restore --list <exact-backup-file.dump>
```

Restore the dump into an isolated test database and validate its migration
version, representative data and decryption using the matching image and key,
with scheduling and external access disabled. An archive listing alone does
not verify recovery. Store the dump and checksum securely; retain the key in
a separate protected location.

### Backup Size Estimate

The backup will be roughly proportional to `pg_database_size()`. The pre-flight script reports this. Ensure you have sufficient disk space (2x the database size is a safe margin).

---

## Step 3: Stop Legacy Concourse

**Stop all Concourse components** before running migrations:

```bash
# If running as systemd services
sudo systemctl stop concourse-web
sudo systemctl stop concourse-worker

# If running in Kubernetes
kubectl scale deployment concourse-web --replicas=0
kubectl scale deployment concourse-worker --replicas=0

# If running via Docker Compose
docker-compose stop
```

**Verify no active connections:**

```sql
SELECT pid, usename, application_name, state
FROM pg_stat_activity
WHERE datname = 'concourse'
  AND pid != pg_backend_pid();
```

Terminate any remaining connections if needed:

```sql
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE datname = 'concourse'
  AND pid != pg_backend_pid();
```

---

## Step 4: Apply Migrations

### Option A: Standalone Migration (Recommended)

Use the JetBridge `concourse migrate` command to apply migrations without starting the server:

```bash
concourse migrate \
  --postgres-host=<pg-host> \
  --postgres-port=5432 \
  --postgres-database=concourse \
  --postgres-user=concourse \
  --postgres-password=<password> \
  --migrate-to-latest-version
```

This applies all pending migrations up to the JetBridge target version: the highest migration in `atc/db/migration/migrations`, which `migrate-preflight.sh` prints as the JetBridge target version.

### Option B: Automatic on Startup

Simply start the JetBridge server. It will detect the schema version gap and apply all pending migrations automatically before accepting traffic.

```bash
concourse web \
  --postgres-host=<pg-host> \
  ...
```

**Note:** Option A is preferred for production migrations because it separates the migration step from the server startup, making it easier to diagnose and recover from failures.

### Monitoring Migration Progress

Watch the PostgreSQL logs for migration activity:

```bash
# If using pg_stat_activity
watch -n 1 "psql -h <pg-host> -U concourse -d concourse -c \"
  SELECT pid, state, query, now() - query_start AS duration
  FROM pg_stat_activity
  WHERE datname = 'concourse'
    AND query NOT LIKE '%pg_stat_activity%'
  ORDER BY query_start;
\""
```

---

## Step 5: Post-Migration Validation

After migrations complete, run validation queries to confirm data integrity.

### 5.1 Schema Version Check

```sql
-- Verify we're at JetBridge version
SELECT version, direction, status, tstamp
FROM migrations_history
ORDER BY tstamp DESC
LIMIT 5;

-- Expected: version = the pre-flight's JetBridge target version, direction = 'up', status = 'passed'
```

### 5.2 Row Count Comparison

Compare row counts against the pre-flight report to ensure no data loss:

```sql
SELECT 'teams' AS table_name, count(*) FROM teams
UNION ALL SELECT 'pipelines', count(*) FROM pipelines
UNION ALL SELECT 'jobs', count(*) FROM jobs
UNION ALL SELECT 'builds', count(*) FROM builds
UNION ALL SELECT 'resources', count(*) FROM resources
UNION ALL SELECT 'resource_types', count(*) FROM resource_types
UNION ALL SELECT 'resource_configs', count(*) FROM resource_configs
UNION ALL SELECT 'resource_config_versions', count(*) FROM resource_config_versions
ORDER BY table_name;
```

**All counts should match the pre-flight report exactly.** The migrations do not delete rows from these tables.

### 5.4 Component Table Verification

```sql
-- Verify dropped columns are gone
SELECT column_name
FROM information_schema.columns
WHERE table_name = 'components'
ORDER BY ordinal_position;

-- Should NOT include: interval, last_ran, paused
```

### 5.5 Trigger Verification

```sql
-- Verify simplified triggers are in place
SELECT trigger_name, event_manipulation, action_statement
FROM information_schema.triggers
WHERE event_object_table IN ('workers', 'containers')
ORDER BY event_object_table, trigger_name;

-- Expected: workers_notify_trigger (INSERT, UPDATE, DELETE)
-- containers_notify_trigger and notify_container_event() were retired in 1789793158.
SELECT to_regprocedure('notify_container_event()'); -- Expected: NULL
-- Should NOT see: workers_upsert_or_delete_trigger, containers_insert_or_delete_trigger
```

### 5.6 Spot-Check Sample Data

```sql
-- Verify a sample of builds have intact data
SELECT b.id, b.name, b.status, j.name AS job_name, p.name AS pipeline_name
FROM builds b
JOIN jobs j ON b.job_id = j.id
JOIN pipelines p ON j.pipeline_id = p.id
ORDER BY b.id DESC
LIMIT 10;

-- Verify pipeline configs are intact
SELECT id, name, team_id
FROM pipelines
WHERE archived = false
ORDER BY id;
```

---

## Step 6: Garden-Era Data Cleanup (Optional)

After validation, clean up stale Garden worker data. **This is optional** — leaving it in place is safe but wastes disk space.

### 6.1 Truncate Unused Cache Tables

```sql
-- Before first JetBridge boot only: every row here is Garden-era. JetBridge writes these tables once it runs.
TRUNCATE worker_task_caches CASCADE;
TRUNCATE worker_resource_caches CASCADE;
TRUNCATE worker_base_resource_types CASCADE;
```

### 6.2 Remove Stale Worker Records

```sql
-- Delete Garden workers (the synthetic worker is re-registered on boot)
-- First, check what workers exist
SELECT name, state, platform, addr FROM workers;

-- Delete all existing workers — JetBridge registers its synthetic worker on boot
DELETE FROM workers;
```

### 6.3 Clean Up Orphaned Containers and Volumes

```sql
-- Delete containers referencing workers that no longer exist
DELETE FROM containers
WHERE worker_name NOT IN (SELECT name FROM workers);

-- Delete volumes referencing workers that no longer exist
DELETE FROM volumes
WHERE worker_name NOT IN (SELECT name FROM workers);
```

### 6.4 Reclaim Disk Space

```sql
-- After bulk deletes, reclaim disk space
VACUUM FULL workers;
VACUUM FULL containers;
VACUUM FULL volumes;
VACUUM FULL worker_task_caches;
VACUUM FULL worker_resource_caches;
VACUUM FULL worker_base_resource_types;

-- Update statistics
ANALYZE;
```

---

## Step 7: Start JetBridge

Start the JetBridge server against the migrated database:

```bash
concourse web \
  --postgres-host=<pg-host> \
  --postgres-port=5432 \
  --postgres-database=concourse \
  --postgres-user=concourse \
  --postgres-password=<password> \
  ...
```

### What to Expect on First Boot

1. **Migration check:** JetBridge verifies the database is at the expected version. If you used `concourse migrate`, this is a no-op.
2. **Component initialization:** All components (scheduler, tracker, scanner, etc.) start running.
3. **Synthetic worker registration:** the registrar (component `k8s_worker_registrar`) creates the one worker record for the namespace.
4. **Pipeline resumption:** Existing pipelines resume executing. Resources begin checking.

### Expected Log Output

```
{"timestamp":"...","source":"atc","message":"atc.listening","data":{"address":"0.0.0.0:8080"}}
```

### Verify Operation

```bash
# Check the web UI is accessible
curl -s http://localhost:8080/api/v1/info | jq .

# Check workers are registered
fly -t <target> login ...
fly -t <target> workers

# Check pipelines are visible
fly -t <target> pipelines
```

---

## Rollback Procedure

Every migration from the baseline `1765921815` on is forward-only: none has a
down, and the migrator refuses a downgrade before changing anything, even on an
empty database. Recovery restores the pre-upgrade database;
it discards all ordinary database changes made since that snapshot, including
build history, configuration changes and new Runs. Older lossy downs could
retain some surviving rows; restore-only recovery does not retain later writes.

**An old image does NOT reliably refuse to start against a newer schema.**
Each binary embeds its own migrations; historical binaries retain historical
downs, and their migrate-to-latest path is not a compatibility guard.
Restore the matching database before starting the old image.

### Step R1: Freeze deployment and stop writers

This deployment has one web replica, a `Recreate` strategy, external PostgreSQL
and a separate database-encryption Secret. `Recreate` alone does not prevent
a reconciler or self-upgrade from starting the wrong image.

1. Pause the self-upgrade job/pipeline and wait for any active deploy build to
   finish or stop it. Suspend Argo automatic synchronization and self-heal,
   including any parent application that could re-enable them; stop in-flight
   synchronization. Record the desired image digests and replica count.
2. Scale the web Deployment to zero and wait for its pods (including migration
   init containers) to terminate. Quiesce step pods and other database writers.
   Confirm no application sessions remain on the external database. Keep Argo
   and self-upgrade paused throughout restoration.
3. Save the failed database separately if post-upgrade data must be examined.
   Select the exact pre-upgrade dump, its recorded image digest and its matching
   encryption Secret. Do not select a backup with a wildcard.

### Step R2: Recreate the external database

Connect to the maintenance database on the **external PostgreSQL server**,
confirm the target and owner, then drop and recreate only the application
database. This destroys post-backup database changes.

```sql
-- Connected to postgres, with application writers stopped:
DROP DATABASE concourse;
CREATE DATABASE concourse OWNER concourse;
```

### Step R3: Restore the exact backup

```bash
pg_restore \
  --host=<pg-host> \
  --port=5432 \
  --username=concourse \
  --dbname=concourse \
  --exit-on-error \
  --verbose \
  <exact-pre-upgrade-backup.dump>
```

Check the exit status and inspect the restored migration history before
starting web. A failed restore is not a usable database.

### Step R4: Restore the matching image and key

While replicas remain zero, restore the database-encryption Secret version
recorded with the backup and set the web and migration init to the same
recorded image digest. Preserve the chart's first-init migration ordering and
image coupling. Restore compatible artifact-daemon and related deployment
configuration from the same release. Database restoration does not restore
external artifact objects; verify the objects required by retained results.

Update Argo's desired configuration to this same image, Secret reference and
release configuration before allowing reconciliation. Check the rendered
Deployment: no init container may upgrade the restored database with the
newer image. Then scale web to its original single replica.

### Step R5: Verify and resume automation

Check migration logs and the effective database version (the query in
[5.1](#51-schema-version-check)). Verify login, decryption, pipelines, retained
build history and access to required artifacts. Keep scheduling paused until
those checks pass. Resume Argo only after its desired state matches the
restored deployment; resume self-upgrade only after preventing it from
immediately redeploying the rejected image. Take a new backup before retrying
an upgrade.

---

## Version-Specific Notes

### Migrating from v7.x or earlier

This release refuses the database: its version is below the baseline
`1765921815`. Upgrade it with JetBridge 0.3.1 (or Concourse v8.0.x) first. That
step runs `1747084615_switch_md5_to_sha256`, which rehashes every
`resource_config_versions` row and needs `pgcrypto`; see the runbook shipped
with JetBridge 0.3.1 for its duration and verification.

### Migrating from v8.0.0

- **Migration count:** 61 (JetBridge-only migrations)
- **Duration:** scales with the `builds` table (`1773105506` scans it three times under ACCESS EXCLUSIVE; see schema-delta.md)
- **Risk:** Medium — measure on a restored copy first

### Migrating from v8.0.1

- **Migration count:** 61 (identical to v8.0.0)
- **Duration:** as for v8.0.0
- **Risk:** Medium — measure on a restored copy first
- **Note:** v8.0.0 and v8.0.1 share identical migration sets

---

## Troubleshooting

### "database is at migration …, older than this release's baseline migration 1765921815"

Also "database records migration … in schema_migrations" or "… in
migration_version". The database predates Concourse v8.0.x. Nothing was
changed. Upgrade it with JetBridge 0.3.1 (or Concourse v8.0.x) first, then run
this release again.

### "migration … is forward-only: restore the pre-upgrade database backup instead"

Something asked the migrator to go below the database's version: usually an
older binary than the database. Nothing was reversed. Follow the
[rollback procedure](#rollback-procedure).

### Migration hangs (no progress)

Another process may hold the migration advisory lock:

```sql
SELECT pid, usename, query, state
FROM pg_stat_activity
WHERE datname = 'concourse';
```

Check for connections from other Concourse instances or migration processes.

### Post-migration: resource checks fail

Resource checks may fail initially until JetBridge registers its synthetic worker. Wait 30–60 seconds for the registrar (`k8s_worker_registrar`) to register it and for the scanner to pick it up.
