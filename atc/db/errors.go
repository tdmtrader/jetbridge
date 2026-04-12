package db

import (
	"errors"
	"strings"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// IsForeignKeyViolation returns true if the error is a PostgreSQL foreign key
// violation (SQLSTATE 23503). This is used to detect races where a referenced
// row (e.g. resource_config_scope) was deleted between a check and an insert.
//
// The primary detection uses errors.As to find a *pgconn.PgError. As a
// fallback, the error string is checked for the SQLSTATE code, which handles
// edge cases where the error type may not be directly extractable (e.g.,
// certain database/sql wrapping paths).
func IsForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgerrcode.ForeignKeyViolation
	}

	// Fallback: check error string for the SQLSTATE code. This handles
	// edge cases where errors.As cannot extract the *pgconn.PgError
	// (e.g., serialized errors from subprocess boundaries or certain
	// database/sql wrapping paths).
	return strings.Contains(err.Error(), "SQLSTATE 23503")
}
