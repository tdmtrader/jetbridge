#!/usr/bin/env bash
#
# migrate-preflight.sh — Pre-flight validation for migrating a Concourse database to JetBridge
#
# Usage:
#   ./migrate-preflight.sh --host <host> --port <port> --dbname <db> --user <user> [--password <pass>]
#
# Environment variables (alternative to flags):
#   PGHOST, PGPORT, PGDATABASE, PGUSER, PGPASSWORD
#
set -euo pipefail

# --- Configuration -----------------------------------------------------------

# Known Concourse release versions mapped to their last migration number.
# Format: "release_name:migration_number" (one per line)
KNOWN_VERSIONS="
v6.8.0:1601993582
v7.0.0:1612565824
v7.1.0:1612565824
v7.10.0:1653924132
v7.11.0:1653924132
v7.14.3:1746768931
v8.0.0:1765921815
v8.0.1:1765921815
"

# JetBridge target version
JETBRIDGE_VERSION=1773105503

# Minimum supported source version (v6.x)
MIN_SUPPORTED_VERSION=1601993582

# --- Argument Parsing ---------------------------------------------------------

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"
PGDATABASE="${PGDATABASE:-concourse}"
PGUSER="${PGUSER:-concourse}"
PGPASSWORD="${PGPASSWORD:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)     PGHOST="$2"; shift 2 ;;
    --port)     PGPORT="$2"; shift 2 ;;
    --dbname)   PGDATABASE="$2"; shift 2 ;;
    --user)     PGUSER="$2"; shift 2 ;;
    --password) PGPASSWORD="$2"; shift 2 ;;
    --help|-h)
      echo "Usage: $0 --host <host> --port <port> --dbname <db> --user <user> [--password <pass>]"
      echo ""
      echo "Or set PGHOST, PGPORT, PGDATABASE, PGUSER, PGPASSWORD environment variables."
      exit 0
      ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

export PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD

# --- Helpers ------------------------------------------------------------------

PASS=0
WARN=0
FAIL=0

pass() { echo "  [PASS] $1"; PASS=$((PASS + 1)); }
warn() { echo "  [WARN] $1"; WARN=$((WARN + 1)); }
fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }
info() { echo "  [INFO] $1"; }

run_sql() {
  psql -At -c "$1" 2>/dev/null
}

# Look up a release name from a migration version number.
# Returns the release name or "unknown".
detect_release() {
  local version="$1"
  echo "$KNOWN_VERSIONS" | while IFS=: read -r release migration; do
    release=$(echo "$release" | tr -d ' ')
    migration=$(echo "$migration" | tr -d ' ')
    if [[ -n "$migration" ]] && [[ "$migration" -eq "$version" ]]; then
      echo "$release"
      return
    fi
  done
}

# --- Pre-flight Checks -------------------------------------------------------

echo ""
echo "=========================================="
echo "  Concourse → JetBridge Migration Pre-flight"
echo "=========================================="
echo ""
echo "Target: ${PGUSER}@${PGHOST}:${PGPORT}/${PGDATABASE}"
echo ""

# 1. Database Connectivity
echo "--- Database Connectivity ---"
if run_sql "SELECT 1" > /dev/null 2>&1; then
  pass "Connected to PostgreSQL"
else
  fail "Cannot connect to PostgreSQL at ${PGHOST}:${PGPORT}/${PGDATABASE}"
  echo ""
  echo "RESULT: Pre-flight FAILED — cannot connect to database."
  exit 1
fi

PG_VERSION=$(run_sql "SHOW server_version;" | cut -d. -f1)
if [[ "$PG_VERSION" -ge 13 ]]; then
  pass "PostgreSQL version ${PG_VERSION} (>= 13 required)"
else
  warn "PostgreSQL version ${PG_VERSION} — JetBridge recommends PostgreSQL 13+"
fi

# 2. Schema Version Detection
echo ""
echo "--- Schema Version ---"

HAS_MIGRATIONS_HISTORY=$(run_sql "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'migrations_history');")
HAS_SCHEMA_MIGRATIONS=$(run_sql "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'schema_migrations');")
HAS_MIGRATION_VERSION=$(run_sql "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'migration_version');")

CURRENT_VERSION=0
SCHEMA_FORMAT="unknown"

if [[ "$HAS_MIGRATIONS_HISTORY" == "t" ]]; then
  SCHEMA_FORMAT="migrations_history"
  CURRENT_VERSION=$(run_sql "SELECT version FROM migrations_history WHERE status != 'failed' ORDER BY tstamp DESC LIMIT 1;")
  CURRENT_VERSION=${CURRENT_VERSION:-0}
  pass "Schema tracking: migrations_history (modern format)"

  # Check for dirty state
  DIRTY=$(run_sql "SELECT dirty FROM migrations_history WHERE status = 'failed' ORDER BY tstamp DESC LIMIT 1;")
  if [[ "$DIRTY" == "t" ]]; then
    fail "Database has a failed migration — resolve before proceeding"
  fi

elif [[ "$HAS_SCHEMA_MIGRATIONS" == "t" ]]; then
  SCHEMA_FORMAT="schema_migrations"
  CURRENT_VERSION=$(run_sql "SELECT version FROM schema_migrations LIMIT 1;")
  DIRTY=$(run_sql "SELECT dirty FROM schema_migrations LIMIT 1;")
  if [[ "$DIRTY" == "t" ]]; then
    fail "Database is in a dirty state (schema_migrations.dirty = true) — resolve before proceeding"
  else
    pass "Schema tracking: schema_migrations (legacy format, will be auto-upgraded)"
  fi

elif [[ "$HAS_MIGRATION_VERSION" == "t" ]]; then
  SCHEMA_FORMAT="migration_version"
  CURRENT_VERSION=$(run_sql "SELECT version FROM migration_version LIMIT 1;")
  if [[ "$CURRENT_VERSION" -eq 189 ]]; then
    pass "Schema tracking: migration_version (very old format, version 189 — will be auto-upgraded)"
  else
    fail "migration_version = ${CURRENT_VERSION} — must be 189 (Concourse 3.6.0) to auto-upgrade"
  fi
else
  fail "No schema tracking table found — is this a Concourse database?"
fi

info "Current migration version: ${CURRENT_VERSION}"
info "JetBridge target version:  ${JETBRIDGE_VERSION}"

# Map version to release
DETECTED_RELEASE=$(detect_release "$CURRENT_VERSION")
DETECTED_RELEASE=${DETECTED_RELEASE:-unknown}
info "Detected Concourse release: ${DETECTED_RELEASE}"

# 3. Version Path Validation
echo ""
echo "--- Migration Path ---"

if [[ "$CURRENT_VERSION" -eq "$JETBRIDGE_VERSION" ]]; then
  pass "Database is already at JetBridge version — no migration needed"
elif [[ "$CURRENT_VERSION" -gt "$JETBRIDGE_VERSION" ]]; then
  fail "Database version (${CURRENT_VERSION}) is ahead of JetBridge (${JETBRIDGE_VERSION}) — downgrade not supported"
elif [[ "$CURRENT_VERSION" -lt "$MIN_SUPPORTED_VERSION" ]]; then
  fail "Database version (${CURRENT_VERSION}) is too old — minimum supported is v6.8.0 (${MIN_SUPPORTED_VERSION})"
  info "Upgrade to Concourse v6.8.0 or later first, then migrate to JetBridge"
else
  MIGRATION_GAP=$((JETBRIDGE_VERSION - CURRENT_VERSION))
  pass "Migration path is valid: ${CURRENT_VERSION} → ${JETBRIDGE_VERSION}"

  if [[ "$CURRENT_VERSION" -le 1746768931 ]]; then
    warn "Source is v7.x or earlier — the md5→sha256 migration (1747084615) will rehash all resource_config_versions rows. This can be slow on large databases."
  fi
fi

# 4. Check for pgcrypto extension (needed by md5→sha256 migration)
echo ""
echo "--- Extension Requirements ---"

if [[ "$CURRENT_VERSION" -lt 1747084615 ]]; then
  HAS_PGCRYPTO=$(run_sql "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pgcrypto');")
  if [[ "$HAS_PGCRYPTO" == "t" ]]; then
    pass "pgcrypto extension is installed (required for md5→sha256 migration)"
  else
    CAN_CREATE=$(run_sql "SELECT has_database_privilege(current_user, current_database(), 'CREATE');" 2>/dev/null || echo "f")
    if [[ "$CAN_CREATE" == "t" ]]; then
      warn "pgcrypto extension not installed — migration will auto-install it (CREATE EXTENSION IF NOT EXISTS pgcrypto)"
    else
      fail "pgcrypto extension not installed and user lacks CREATE privilege — install pgcrypto manually before migrating"
    fi
  fi
else
  pass "pgcrypto already applied (migration version past 1747084615)"
fi

# 5. Data Integrity Checks
echo ""
echo "--- Data Integrity ---"

# Row counts on key tables
for table in teams pipelines jobs builds resources resource_types resource_configs resource_config_versions workers containers volumes; do
  COUNT=$(run_sql "SELECT count(*) FROM ${table};" 2>/dev/null || echo "N/A")
  info "${table}: ${COUNT} rows"
done

# Check for orphaned records
ORPHANED_CONTAINERS=$(run_sql "SELECT count(*) FROM containers c LEFT JOIN workers w ON c.worker_name = w.name WHERE w.name IS NULL;" 2>/dev/null || echo "0")
if [[ "$ORPHANED_CONTAINERS" -gt 0 ]]; then
  warn "Found ${ORPHANED_CONTAINERS} containers referencing non-existent workers (will be GC'd)"
fi

ORPHANED_VOLUMES=$(run_sql "SELECT count(*) FROM volumes v LEFT JOIN workers w ON v.worker_name = w.name WHERE w.name IS NULL;" 2>/dev/null || echo "0")
if [[ "$ORPHANED_VOLUMES" -gt 0 ]]; then
  warn "Found ${ORPHANED_VOLUMES} volumes referencing non-existent workers (will be GC'd)"
fi

# Check for in-progress migrations
if [[ "$HAS_MIGRATIONS_HISTORY" == "t" ]]; then
  IN_PROGRESS=$(run_sql "SELECT count(*) FROM migrations_history WHERE status = 'failed';")
  if [[ "$IN_PROGRESS" -gt 0 ]]; then
    warn "Found ${IN_PROGRESS} failed migration(s) in history — review before proceeding"
  fi
fi

# Estimate md5→sha256 migration time if applicable
if [[ "$CURRENT_VERSION" -lt 1747084615 ]]; then
  echo ""
  echo "--- MD5→SHA256 Migration Estimate ---"
  RCV_COUNT=$(run_sql "SELECT count(*) FROM resource_config_versions;" 2>/dev/null || echo "0")
  info "resource_config_versions rows: ${RCV_COUNT}"
  if [[ "$RCV_COUNT" -gt 1000000 ]]; then
    warn "Large table — md5→sha256 rehash may take several minutes. Plan for maintenance window."
  elif [[ "$RCV_COUNT" -gt 100000 ]]; then
    info "Moderate table size — md5→sha256 rehash should complete in under a minute"
  else
    info "Small table — md5→sha256 rehash will be fast"
  fi
fi

# 6. Database Size
echo ""
echo "--- Database Size ---"
DB_SIZE=$(run_sql "SELECT pg_size_pretty(pg_database_size(current_database()));")
info "Total database size: ${DB_SIZE}"

# --- Summary ------------------------------------------------------------------

echo ""
echo "=========================================="
echo "  Pre-flight Summary"
echo "=========================================="
echo ""
echo "  Passed: ${PASS}"
echo "  Warnings: ${WARN}"
echo "  Failed: ${FAIL}"
echo ""

if [[ "$FAIL" -gt 0 ]]; then
  echo "RESULT: Pre-flight FAILED — resolve ${FAIL} failure(s) before proceeding."
  exit 1
elif [[ "$WARN" -gt 0 ]]; then
  echo "RESULT: Pre-flight PASSED with ${WARN} warning(s) — review warnings before proceeding."
  exit 0
else
  echo "RESULT: Pre-flight PASSED — safe to proceed with migration."
  exit 0
fi
