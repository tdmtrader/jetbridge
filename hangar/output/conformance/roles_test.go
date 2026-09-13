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

	hangargcs "github.com/concourse/concourse/hangar/gcs"
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
		"publisher's handle": {
			prototype: (*publisher.Handle)(nil),
			want:      []string{"Attrs", "Generation", "If", "NewReader", "NewWriter"},
			forbidden: []string{"Delete", "Update", "SetMetadata"},
			because: "the publisher creates and reads. A delete method here would be a delete " +
				"permission on the identity that also creates, and the marker's " +
				"immutability rests on there being no way to rewrite metadata after creation",
		},
		"publisher's store": {
			prototype: (*publisher.Store)(nil),
			want:      []string{"Object"},
			forbidden: []string{"List"},
			because: "objects.list is bucket-wide and cannot be narrowed by IAM; the publisher " +
				"has no reason to enumerate a bucket and every reason not to be able to",
		},
		"inventory's handle": {
			prototype: (*inventory.Handle)(nil),
			want:      []string{"Attrs", "Generation"},
			forbidden: []string{"Delete", "NewWriter", "NewReader", "If"},
			because:   "inventory lists and stats. It cannot create and it cannot delete",
		},
		"inventory's store": {
			prototype: (*inventory.Store)(nil),
			want:      []string{"List", "Object"},
			because:   "inventory is the one role that lists",
		},
		"reclaimer's handle": {
			prototype: (*reclaimer.Handle)(nil),
			want:      []string{"Attrs", "Delete", "Generation", "If"},
			forbidden: []string{"NewWriter", "NewReader"},
			because: "a reclaimer that could read could exfiltrate and one that could write " +
				"could resurrect. Delete is reachable only through a generation pin and a " +
				"precondition, because IAM cannot require one once delete permission exists",
		},
		"reclaimer's store": {
			prototype: (*reclaimer.Store)(nil),
			want:      []string{"Object"},
			forbidden: []string{"List"},
			because:   "a reclaimer deletes what it was told to delete; it does not go looking",
		},
		"the policy attestor's source": {
			// The PRODUCTION type, not a role-package interface. The interface
			// this used to name had no implementation and no consumer: the
			// wired composition reads bucket metadata through
			// hangar/gcs.BucketPolicySource directly, so an interface nothing
			// satisfied was a guard over a shape that could be correct while
			// the shape in the process was not.
			prototype: (*hangargcs.BucketPolicySource)(nil),
			want:      []string{"ReadLifetimePolicy", "ReadPrincipalBindings"},
			forbidden: []string{"Object", "List", "Delete", "NewWriter", "NewReader", "Attrs"},
			because: "the attestor is the workload whose word the plane trusts about whether the " +
				"bucket is safe. One that could also touch an object would be a workload whose " +
				"compromise costs the data rather than the assessment",
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

func TestThePolicyAttestorNamesNoObjectStoreAtAll(t *testing.T) {
	// The stronger statement, and the reason it is a source scan rather than a
	// type check: the attestor must not be able to reach an object *by any
	// route*, including one added later through a helper that takes a client.
	_, thisFile, _, _ := runtime.Caller(0)
	directory := filepath.Join(filepath.Dir(thisFile), "..", "policy")

	sources, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatalf("globbing the policy package: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("the policy package has no Go file; this rule would pass vacuously")
	}

	fileSet := token.NewFileSet()
	scanned := 0
	for _, source := range sources {
		parsed, err := parser.ParseFile(fileSet, source, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", source, err)
		}
		scanned++

		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if strings.Contains(path, "objectstore") ||
				strings.Contains(path, "hangar/gcs") ||
				strings.Contains(path, "cloud.google.com/go/storage") {
				t.Errorf("%s imports %s. The policy attestor reads bucket policy and IAM and "+
					"touches no object; an object-store import is the first half of an object "+
					"method", filepath.Base(source), path)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("nothing was scanned")
	}
}

// TestNoRolePackageIsImportedByTheOtherRoles keeps the four principals from
// becoming one library with four entry points.
func TestNoRolePackageIsImportedByTheOtherRoles(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..")

	roles := []string{"publisher", "inventory", "reclaimer", "policy"}
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
