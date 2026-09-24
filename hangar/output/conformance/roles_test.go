package conformance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/hangar/output/inventory"
	"github.com/concourse/concourse/hangar/output/publisher"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

// The role boundary, checked at the type level.
//
// TestEachRoleIssuesOnlyItsOwnRPCs asserts what the code *did* on one run.
// This asserts what it *can* do at all: a role's store interface has the
// methods of its role and no others, so the call a reviewer worries about is
// not merely unwritten but unwritable.
//
// Read the two together and neither is enough alone. The call log would pass on
// a role that had a delete method it happened not to reach; the type check
// would pass on a role that called something through a different seam. What
// neither proves is any IAM binding -- no fake enforces one, and AC 16 is
// real-GCS evidence gathered in Phase 9 with its date and project.

func methodNames(t *testing.T, prototype any) []string {
	t.Helper()

	// An interface prototype arrives as (*Iface)(nil) and a concrete one as
	// (*Type)(nil); Elem() gives the interface in the first case and the
	// pointer type in the second, and NumMethod is the method set either way.
	// Both shapes are wanted: an interface states what a role MAY do, and a
	// concrete type states what the process actually holds.
	typ := reflect.TypeOf(prototype)
	if typ == nil || typ.Kind() != reflect.Ptr {
		t.Fatalf("%v is not a typed nil pointer", typ)
	}
	if typ.Elem().Kind() == reflect.Interface {
		typ = typ.Elem()
	}

	names := make([]string, 0, typ.NumMethod())
	for index := 0; index < typ.NumMethod(); index++ {
		names = append(names, typ.Method(index).Name)
	}
	sort.Strings(names)

	return names
}

func TestEachRolesStoreInterfaceHasOnlyItsRolesMethods(t *testing.T) {
	for role, expectation := range map[string]struct {
		prototype any
		want      []string
		forbidden []string
		because   string
	}{
		"publisher": {
			prototype: (*publisher.Store)(nil),
			want:      []string{"CreateAbsent", "OpenExact", "StatCurrent", "StatExact"},
			forbidden: []string{"DeleteExact", "List"},
			because:   "publishers create and read without deletion or enumeration",
		},
		"inventory": {
			prototype: (*inventory.Store)(nil),
			want:      []string{"List", "StatExact"},
			forbidden: []string{"CreateAbsent", "DeleteExact", "OpenExact"},
			because:   "inventory inspects metadata without reading or changing bodies",
		},
		"reclaimer": {
			prototype: (*reclaimer.Store)(nil),
			want:      []string{"DeleteExact", "StatExact"},
			forbidden: []string{"CreateAbsent", "OpenExact", "List"},
			because:   "reclaimers delete exact registered objects without publication authority",
		},
	} {
		got := methodNames(t, expectation.prototype)

		if strings.Join(got, ",") != strings.Join(expectation.want, ",") {
			t.Errorf("%s has methods %v, expected exactly %v.\n\n%s",
				role, got, expectation.want, expectation.because)
		}
		for _, forbidden := range expectation.forbidden {
			for _, name := range got {
				if name == forbidden {
					t.Errorf("%s offers %s.\n\n%s", role, forbidden, expectation.because)
				}
			}
		}
	}
}

// TestNoRolePackageIsImportedByTheOtherRoles keeps the three principals from
// becoming one library with three entry points.
func TestNoRolePackageIsImportedByTheOtherRoles(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..")

	roles := []string{"publisher", "inventory", "reclaimer"}
	fileSet := token.NewFileSet()

	checked := 0
	for _, role := range roles {
		sources, err := filepath.Glob(filepath.Join(root, role, "*.go"))
		if err != nil {
			t.Fatalf("globbing %s: %v", role, err)
		}
		if len(sources) == 0 {
			t.Fatalf("the %s package has no Go file; this rule would pass vacuously", role)
		}

		for _, source := range sources {
			parsed, err := parser.ParseFile(fileSet, source, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parsing %s: %v", source, err)
			}
			checked++

			ast.Inspect(parsed, func(node ast.Node) bool {
				spec, ok := node.(*ast.ImportSpec)
				if !ok {
					return true
				}
				path := strings.Trim(spec.Path.Value, `"`)
				for _, other := range roles {
					if other == role {
						continue
					}
					if strings.HasSuffix(path, "hangar/output/"+other) {
						t.Errorf("%s/%s imports the %s role. Each role is a separate binary "+
							"with a separate service account; an import between them is how one "+
							"process ends up linking two identities' worth of capability",
							role, filepath.Base(source), other)
					}
				}

				return true
			})
		}
	}
	if checked < 4 {
		t.Fatalf("only %d role source files were scanned", checked)
	}
}
