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

# JetBridge target version.
#
# Must equal the highest migration in atc/db/migration/migrations. This script
# runs on an operator's laptop against a live database before an upgrade, so it
# cannot ask the binary; the "migrate-preflight.sh targets the head migration"
# spec in atc/db/migration/baseline_test.go keeps it honest.
JETBRIDGE_VERSION=1789793169

# The baseline: upstream Concourse 8.0.x's last migration, and the earliest
# migration JetBridge carries. A database below it is refused.
BASELINE_VERSION=1765921815

# The last release that carries the migrations the baseline replaced.
LAST_RELEASE_BEFORE_BASELINE="JetBridge 0.3.1"

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
  fail "Schema tracking: schema_migrations predates the baseline — upgrade with ${LAST_RELEASE_BEFORE_BASELINE} first"

elif [[ "$HAS_MIGRATION_VERSION" == "t" ]]; then
  SCHEMA_FORMAT="migration_version"
  CURRENT_VERSION=$(run_sql "SELECT version FROM migration_version LIMIT 1;")
  fail "Schema tracking: migration_version predates the baseline — upgrade with ${LAST_RELEASE_BEFORE_BASELINE} first"
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
elif [[ "$CURRENT_VERSION" -lt "$BASELINE_VERSION" ]]; then
  fail "Database version (${CURRENT_VERSION}) is older than the baseline (${BASELINE_VERSION}, Concourse v8.0.x)"
  info "Upgrade it with ${LAST_RELEASE_BEFORE_BASELINE} (or Concourse v8.0.x) first, then migrate to this release"
else
  pass "Migration path is valid: ${CURRENT_VERSION} → ${JETBRIDGE_VERSION}"
fi

# 4. Data Integrity Checks
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

# 5. Database Size
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
