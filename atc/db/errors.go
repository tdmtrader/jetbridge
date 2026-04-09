package db

import (
	"errors"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// IsForeignKeyViolation returns true if the error is a PostgreSQL foreign key
// violation (SQLSTATE 23503). This is used to detect races where a referenced
// row (e.g. resource_config_scope) was deleted between a check and an insert.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgerrcode.ForeignKeyViolation
	}
	return false
}
