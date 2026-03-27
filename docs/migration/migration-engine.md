# Concourse Migration Engine

This document describes how Concourse discovers and applies database migrations, and how to run them independently of the full server.

## How Migrations Work

### Discovery

Migration files are embedded in the Concourse binary at compile time using Go's `//go:embed` directive. They live in `atc/db/migration/migrations/` and follow the naming convention:

```
{UNIX_TIMESTAMP}_{description}.{up|down}.{sql|go}
```

Examples:
- `1773105500_drop_component_interval_and_last_ran.up.sql`
- `1579713199_migrate_job_configs_to_job_inputs_and_outputs.up.go`

The version number is the Unix timestamp prefix. Migrations are sorted by version and applied in order.

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

Concourse handles upgrades from older schema formats automatically:

1. **`migration_version` table** (Concourse <= 3.6.0) — Must be at version 189. Migrated to `schema_migrations`.
2. **`schema_migrations` table** (Concourse 4.x–7.x) — Read and migrated to `migrations_history` format. Fails if `dirty = true`.
3. **`migrations_history` table** (current) — Used directly.

## Running Migrations Independently

### Using `concourse migrate`

The `concourse migrate` subcommand runs migrations without starting the full server:

```bash
# Check current database version
concourse migrate \
  --postgres-host=localhost \
  --postgres-port=5432 \
  --postgres-database=concourse \
  --postgres-user=concourse \
  --postgres-password=concourse \
  --current-db-version

# Check the latest supported version
concourse migrate \
  --postgres-host=localhost \
  ... \
  --supported-db-version

# Migrate to latest version
concourse migrate \
  --postgres-host=localhost \
  ... \
  --migrate-to-latest-version

# Migrate to a specific version
concourse migrate \
  --postgres-host=localhost \
  ... \
  --migrate-db-to-version 1773105501
```

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
- **Downgrade support:** You can migrate down to a specific version with `--migrate-db-to-version`, but this is rarely needed. The rollback procedure (pg_restore from backup) is safer.
