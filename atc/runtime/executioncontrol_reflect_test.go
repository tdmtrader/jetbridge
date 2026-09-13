package runtime_test

import (
	"reflect"
	"testing"
)

// structFieldNames is the reflect half of TestTheBaseEnvelopeNamesNoOutputConcept.
func structFieldNames(t *testing.T, value any) []string {
	t.Helper()

	typ := reflect.TypeOf(value)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%T is not a struct", value)
	}
	names := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		names = append(names, typ.Field(i).Name)
	}

	return names
}
