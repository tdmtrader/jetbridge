package contracts

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

// recordPrototypes is the zero value of every record type's complete wire
// shape, envelope included. It exists so a declared schema document can be
// checked for completeness against the Go types themselves rather than against
// a hand-maintained list of field paths, which would go stale in the same
// commit that made it wrong.
var recordPrototypes = map[snapshot.TypeRef]any{
	"review/v1":                Record[ReviewBody]{},
	"diagnosis/v1":             Record[DiagnosisBody]{},
	"validation/v1":            Record[validationBodyRevision3Declared]{},
	"repository-change/v1":     Record[RepositoryChangeBody]{},
	"measurements/v1":          Record[MeasurementsBody]{},
	"pull-request/v1":          Record[PullRequestBody]{},
	"pull-request-response/v1": Record[PullRequestResponseBody]{},
	"publish-impact/v1":        Record[PublishImpactBody]{},
}

const maxRecordFieldDepth = 32

// recordLeaf is one value-bearing field of a record type, with the two facts
// about its Go representation that a declared schema has to agree with: what kind
// of value it holds, and whether absence is distinguishable from the zero value.
//
// pointer is the whole reason `absent_image` exists in the dialect. An explicit 0
// in a *float64 is a real, distinguishable value; an explicit 0 in a float64 is
// indistinguishable from a missing key, and no rule can tell them apart.
type recordLeaf struct {
	path    string
	kind    reflect.Kind
	pointer bool
	element reflect.Kind
}

// recordLeafFields enumerates every value-bearing field of a record type.
//
// Only leaves are enumerated. Objects and arrays of objects are structure, not
// claims: "body/findings" carries no value of its own, and giving the container
// a declaration would invite a second, weaker answer to a question the leaves
// already answer. Their shape is the schema's business.
//
// A slice of scalars IS a leaf — "body/actions/*/addresses" is one value, a set
// of ids — because the grammar has no way to address an element of it: scalars
// have no id, and positions are not addresses.
func recordLeafFields(recordType reflect.Type) ([]recordLeaf, error) {
	var leaves []recordLeaf
	if err := appendLeafFields(recordType, "", &leaves, 0); err != nil {
		return nil, err
	}
	return leaves, nil
}

func appendLeafFields(fieldType reflect.Type, prefix string, leaves *[]recordLeaf, depth int) error {
	if depth > maxRecordFieldDepth {
		return fmt.Errorf("field %q exceeds the maximum record field depth of %d", prefix, maxRecordFieldDepth)
	}
	pointer := false
	for fieldType.Kind() == reflect.Pointer {
		fieldType = fieldType.Elem()
		pointer = true
	}
	switch fieldType.Kind() {
	case reflect.Struct:
		for index := 0; index < fieldType.NumField(); index++ {
			field := fieldType.Field(index)
			if !field.IsExported() {
				continue
			}
			name, encoded := jsonFieldName(field)
			if !encoded {
				continue
			}
			child := name
			if prefix != "" {
				child = prefix + fieldPathSeparator + name
			}
			if err := appendLeafFields(field.Type, child, leaves, depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		element := fieldType.Elem()
		for element.Kind() == reflect.Pointer {
			element = element.Elem()
		}
		if element.Kind() == reflect.Struct {
			return appendLeafFields(element, prefix+fieldPathSeparator+fieldPathWildcard, leaves, depth+1)
		}
		*leaves = append(*leaves, recordLeaf{path: prefix, kind: fieldType.Kind(), element: element.Kind()})
		return nil
	case reflect.Map, reflect.Interface, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return fmt.Errorf("field %q has kind %s, which the frozen field-path grammar cannot address", prefix, fieldType.Kind())
	default:
		*leaves = append(*leaves, recordLeaf{path: prefix, kind: fieldType.Kind(), pointer: pointer})
		return nil
	}
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	tag, tagged := field.Tag.Lookup("json")
	if !tagged {
		return field.Name, true
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", false
	}
	if name == "" {
		return field.Name, true
	}
	return name, true
}
