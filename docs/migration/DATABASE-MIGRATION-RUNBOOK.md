# Database Migration Runbook: Concourse → JetBridge

This runbook guides operators through migrating a legacy Concourse database (v6.8.0 through v8.0.1) to JetBridge Edition. JetBridge is forked from Concourse v8.0.1 with a small number of incremental schema changes, so migration is a direct database upgrade — not an export/import.

## Prerequisites

- **Source Concourse version:** v6.8.0 or later (v7.x, v8.0.0, v8.0.1)
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
- Migration path validity (is the source version supported?)
- `pgcrypto` extension availability (required for md5→sha256 migration)
- Data integrity: row counts, orphaned records, failed migrations
- Estimated md5→sha256 rehash time for large databases

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

### Option C: Migrate to a Specific Version

If you want to apply migrations incrementally (e.g., to isolate the md5→sha256 migration):

```bash
# First, apply everything up to (but not including) the md5→sha256 migration
concourse migrate \
  --postgres-host=<pg-host> \
  ... \
  --migrate-db-to-version 1746768931

# Then apply the md5→sha256 migration separately
concourse migrate \
  --postgres-host=<pg-host> \
  ... \
  --migrate-db-to-version 1747084615

# Finally, apply remaining migrations
concourse migrate \
  --postgres-host=<pg-host> \
  ... \
  --migrate-to-latest-version
```

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

### 5.3 SHA256 Migration Verification

If migrating from v7.x (pre-sha256):

```sql
-- All resource_config_versions should now have sha256 digests
SELECT count(*) AS total,
       count(version_sha256) AS has_sha256,
       count(version_md5) AS has_md5
FROM resource_config_versions;

-- Expected: total = has_sha256. has_md5 should equal total for historical rows.
```

### 5.4 Component Table Verification

```sql
-- Verify dropped columns are gone
SELECT column_name
FROM information_schema.columns
WHERE table_name = 'components'
ORDER BY ordinal_position;

-- Should NOT include: interval, last_ran, paused
```

### 5.5 Signing Keys Table

```sql
-- New table should exist (empty until first JetBridge boot)
SELECT count(*) FROM signing_keys;
-- Expected: 0 rows (populated on first boot)
```

### 5.6 Trigger Verification

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

### 5.7 Spot-Check Sample Data

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
4. **Signing keys:** A new signing key is generated and stored in the `signing_keys` table.
5. **Pipeline resumption:** Existing pipelines resume executing. Resources begin checking.

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

Every fork migration after upstream `1765921815` is forward-only. A downgrade
fails even on an empty database. Recovery restores the pre-upgrade database;
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

Check migration logs and effective database version with the matching binary's
`migrate --current-db-version`. Verify login, decryption, pipelines, retained
build history and access to required artifacts. Keep scheduling paused until
those checks pass. Resume Argo only after its desired state matches the
restored deployment; resume self-upgrade only after preventing it from
immediately redeploying the rejected image. Take a new backup before retrying
an upgrade.

---

## Version-Specific Notes

### Migrating from v7.x (v7.0.0 through v7.14.3)

- **Migration count:** 5–29 migrations depending on exact v7.x version
- **Key migration:** `1747084615_switch_md5_to_sha256` — rehashes all `resource_config_versions` rows. Duration is proportional to row count. For databases with millions of rows, plan a maintenance window of 5–15 minutes for this migration alone.
- **Key migration:** `1653924132_int_to_bigint` — converts integer IDs to bigint across multiple tables. Can be slow on large `builds` tables.
- **pgcrypto:** Required for the md5→sha256 migration. Install it before migrating if not already present: `CREATE EXTENSION IF NOT EXISTS pgcrypto;`
- **Intermediate upgrades:** You do NOT need to upgrade to v8.0.1 first. JetBridge applies all intermediate migrations automatically.

### Migrating from v8.0.0

- **Migration count:** 58 (JetBridge-only migrations)
- **Duration:** scales with the `builds` table (`1773105506` scans it three times under ACCESS EXCLUSIVE; see schema-delta.md)
- **Risk:** Medium — measure on a restored copy first

### Migrating from v8.0.1

- **Migration count:** 58 (identical to v8.0.0)
- **Duration:** as for v8.0.0
- **Risk:** Medium — measure on a restored copy first
- **Note:** v8.0.0 and v8.0.1 share identical migration sets

### MD5 → SHA256 Migration Details

The `1747084615_switch_md5_to_sha256` migration is the most significant schema change:

1. **What it does:** Adds a `version_sha256` column to `resource_config_versions` and computes SHA256 digests for every existing row using `pgcrypto`.
2. **Column renames:** `version_md5` is renamed to `version_digest` in 5 related tables (inputs, outputs, next_build_inputs, resource_caches, disabled_versions).
3. **Index changes:** Drops old MD5 uniqueness constraint, creates new SHA256 uniqueness constraint, preserves MD5 lookup index for backward compatibility.
4. **Duration:** Proportional to `resource_config_versions` row count:
   - < 100K rows: seconds
   - 100K–1M rows: 10–60 seconds
   - 1M+ rows: minutes (plan accordingly)
5. **Verification:** After migration, confirm `version_sha256 IS NOT NULL` for all rows:
   ```sql
   SELECT count(*) FROM resource_config_versions WHERE version_sha256 IS NULL;
   -- Expected: 0
   ```

---

## Troubleshooting

### "cannot begin migration: database is in a dirty state"

The `schema_migrations` table has `dirty = true`, meaning a previous migration attempt failed partway through. Investigate:

```sql
SELECT * FROM schema_migrations;
```

If the version looks correct and you're confident the data is consistent, fix it:

```sql
UPDATE schema_migrations SET dirty = false;
```

### "must upgrade from db version 189"

The database has the very old `migration_version` table from Concourse <= 3.6.0. You must upgrade to at least Concourse v6.x first.

### Migration hangs (no progress)

Another process may hold the migration advisory lock:

```sql
SELECT pid, usename, query, state
FROM pg_stat_activity
WHERE datname = 'concourse';
```

Check for connections from other Concourse instances or migration processes.

### "pgcrypto extension not available"

Install it as a superuser:

```sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;
```

On managed PostgreSQL (RDS, Cloud SQL), pgcrypto is available but may need enabling through the provider's console.

### Post-migration: resource checks fail

Resource checks may fail initially until JetBridge registers its synthetic worker. Wait 30–60 seconds for the registrar (`k8s_worker_registrar`) to register it and for the scanner to pick it up.
