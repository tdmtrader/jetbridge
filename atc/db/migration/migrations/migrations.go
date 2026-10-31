package migrations

import (
	"database/sql"
	"reflect"

	"github.com/concourse/concourse/atc/db/encryption"
)

func NewMigrations(tx *sql.Tx, key *encryption.Key) *migrations {
	return &migrations{tx, key}
}

type migrations struct {
	*sql.Tx
	*encryption.Key
}

func (m *migrations) Run(name string) error {

	res := reflect.ValueOf(m).MethodByName(name).Call(nil)

	ret := res[0].Interface()

	if ret != nil {
		return ret.(error)
	}

	return nil
}
