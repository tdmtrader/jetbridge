package postgresrunner_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/concourse/concourse/atc/postgresrunner"
)

func TestExtractQueriesParsesTopLevelQuery(t *testing.T) {
	query := `WITH abc AS (
  SELECT def FROM (SELECT 1)
), ghi AS (
  SELECT blah
  UNION
  SELECT bloo
)
SELECT who, cares
FROM something
JOIN other ON col1 = (SELECT other_col FROM something)
	union
SELECT something_else FROM a_table`

	want := []string{
		`WITH abc AS (...), ghi AS (...)
SELECT who, cares
FROM something
JOIN other ON col1 = (...)`,
		`SELECT something_else FROM a_table`,
		`SELECT other_col FROM something`,
		`SELECT def FROM (...)`,
		`SELECT blah`,
		`SELECT bloo`,
		`SELECT 1`,
	}
	got := postgresrunner.ExtractQueries(query)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queries = %#v, want %#v", got, want)
	}
}
