package main

import "testing"

func TestEqualSetsRequiresExactMembership(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		actual   []string
		expected []string
		wantErr  bool
	}{
		{name: "same set", actual: []string{"second", "first"}, expected: []string{"first", "second"}},
		{name: "missing failure", actual: []string{"first"}, expected: []string{"first", "second"}, wantErr: true},
		{name: "collateral failure", actual: []string{"first", "second"}, expected: []string{"first"}, wantErr: true},
		{name: "different failure", actual: []string{"second"}, expected: []string{"first"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := equalSets(tc.actual, tc.expected)
			if tc.wantErr && err == nil {
				t.Fatal("equalSets unexpectedly accepted different failure sets")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("equalSets rejected equivalent failure sets: %v", err)
			}
		})
	}
}
