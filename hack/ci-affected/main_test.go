package main

import (
	"reflect"
	"testing"
)

const mod = "example.com/m"

// root <- a <- b; c stands alone; deploy/chart/tests reads deploy/ files.
var (
	graph = map[string][]string{
		mod:                         nil,
		mod + "/a":                  {mod},
		mod + "/a/b":                {mod + "/a", mod},
		mod + "/c":                  nil,
		mod + "/deploy/chart/tests": nil,
	}
	dirs = map[string]string{
		".":                  mod,
		"a":                  mod + "/a",
		"a/b":                mod + "/a/b",
		"c":                  mod + "/c",
		"deploy/chart/tests": mod + "/deploy/chart/tests",
	}
)

func TestAffected(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed []string
		want    []string
		all     bool
	}{
		{"a change reaches its importers", []string{"a/x.go"}, []string{mod + "/a", mod + "/a/b"}, false},
		{"a leaf change stays put", []string{"c/y_test.go"}, []string{mod + "/c"}, false},
		{"a root file reaches everything importing root", []string{"versions.go"}, []string{mod, mod + "/a", mod + "/a/b"}, false},
		{"non-Go files in a package dir count", []string{"a/testdata/f.json"}, []string{mod + "/a", mod + "/a/b"}, false},
		{"docs select nothing", []string{"README.md", "docs/adr/1.md", "a/NOTES.md"}, nil, false},
		{"deploy files select the chart tests only", []string{"deploy/concourse-pipeline.yml"}, []string{mod + "/deploy/chart/tests"}, false},
		{"hack files select the root package, not its importers", []string{"hack/ci-check.sh"}, []string{mod}, false},
		{"go.mod cannot be bounded", []string{"a/x.go", "go.mod"}, nil, true},
		{"a file in no package cannot be bounded", []string{"web/elm/src/Main.elm"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, all := affected(tc.changed, mod, graph, dirs)
			if all != tc.all || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v all=%v, want %v all=%v", got, all, tc.want, tc.all)
			}
		})
	}
}
