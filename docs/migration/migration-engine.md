# Concourse Migration Engine

This document describes how Concourse discovers and applies database migrations, and how to run them independently of the full server.

## How Migrations Work

### Discovery

Migration files are embedded in the Concourse binary at compile time using Go's `//go:embed` directive. They live in `atc/db/migration/migrations/` and follow the naming convention:

```
{UNIX_TIMESTAMP}_{description}.up.{sql|go}
```

Examples:
- `1765921815_baseline.up.sql`
- `1773105500_drop_component_interval_and_last_ran.up.sql`

The version number is the Unix timestamp prefix. Migrations are sorted by version and applied in order.

### The Baseline

The earliest migration a binary carries is its baseline. Here it is
`1765921815_baseline.up.sql`, the upstream Concourse 8.0.x schema as
`pg_dump --schema-only` writes it, which replaced the 300 upstream files from
`1510262030_initial_schema` on. It runs only on an empty database. A database
whose recorded version is at or past it already has it. One whose version is
below it is refused before anything changes, with a message naming
JetBridge 0.3.1, the last release that carries the replaced files: upgrade it
with that release (or Concourse 8.0.x) first.

### Automatic Application on Startup

When Concourse starts, the database connection flow triggers migrations automatically:

1. `db.Open()` calls `migration.NewOpenHelper(...).Open()`
2. `OpenHelper.Open()` calls `NewMigrator(db, lockFactory).Up(newKey, oldKey)`
3. `Migrator.Up()` finds the highest migration version and calls `Migrate()` to apply all pending migrations

**This means starting a JetBridge binary against a legacy database will automatically apply all pending migrations.**

### Locking

Concourse uses PostgreSQL advisory locks to prevent concurrent migration runs. The `acquireLock()` method retries every 1 second until the lock is acquired. This is safe for multi-instance deployments — only one instance will run migrations; others wait.

### Transaction Safety

Each migration runs in its own database transaction. If a migration fails:
- The transaction is rolled back
- A `failed` entry is recorded in `migrations_history`
- The error is returned and Concourse startup aborts

### History Tracking

Applied migrations are tracked in the `migrations_history` table:

```sql
CREATE TABLE migrations_history (
    version   bigint,
    tstamp    timestamp with time zone,
    direction varchar,    -- 'up' or 'down'
    status    varchar,    -- 'passed' or 'failed'
    dirty     boolean
);
```

The current version is determined by:
```sql
SELECT version, direction FROM migrations_history
WHERE status != 'failed'
ORDER BY tstamp DESC LIMIT 1;
```

### Legacy Schema Handling

A database that records its version only in `migration_version` (Concourse
3.6 and earlier) or `schema_migrations` (before migration `1532706545`) predates
the baseline, and is refused without being converted. JetBridge 0.3.1 still
converts both. A `schema_migrations` table left beside `migrations_history` is
ignored.

## Running Migrations Independently

### Using `concourse migrate`

The `concourse migrate` subcommand runs migrations without starting the full server:

```bash
# Migrate to latest version
concourse migrate \
  --postgres-host=localhost \
  --postgres-port=5432 \
  --postgres-database=concourse \
  --postgres-user=concourse \
  --postgres-password=concourse \
  --migrate-to-latest-version
```

There is no flag to print the version or to migrate to a chosen one. Read the
current version with the query under [History Tracking](#history-tracking).

### Encryption Key Rotation

Migrations can also handle encryption key rotation:

```bash
# Encrypt previously plaintext data
concourse migrate --postgres-host=... --encryption-key <new-key>

# Rotate encryption key
concourse migrate --postgres-host=... --old-encryption-key <old> --encryption-key <new>

# Decrypt to plaintext
concourse migrate --postgres-host=... --old-encryption-key <old>
```

### Important Notes

- **Automatic on startup:** If you simply start JetBridge against the database, it will apply all pending migrations automatically. The `concourse migrate` command is useful for running migrations separately (e.g., in a maintenance window before starting the server).
- **No `--dry-run`:** There is no dry-run mode. Use a database backup to test migrations.
- **Migrations are forward-only:** A down file is optional, and none ships:
  the baseline and every fork migration after it have none, and the generator
  writes only the up file. Asked to reverse a migration without a down, the
  migrator refuses with `migration <version> is forward-only: restore the
  pre-upgrade database backup instead`, before reversing anything, so the
  schema, data, history and effective version are unchanged.
- **Image rollback is not a compatibility check:** Older binaries embed their
  own migration files. They may retain historical downs or start against a
  newer schema without reversing it. Restore the matching database and
  encryption key before starting an older image; follow the
  [recovery procedure](DATABASE-MIGRATION-RUNBOOK.md#rollback-procedure).
