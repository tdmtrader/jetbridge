package main

import "testing"

// The symlink-target rule, exercised directly. The end-to-end behaviour lives
// in containment_test.go; this pins the decision boundary itself, including the
// cases that are easy to get wrong by one level.
func TestValidateSymlinkTarget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		entryName string
		linkname  string
		wantErr   bool
	}{
		{"sibling file", "link", "file.txt", false},
		{"into subdirectory", "link", "sub/file.txt", false},
		{"shared dep between trees", "app/node_modules", "../shared", false},
		{"up then back down, still inside", "a/b/link", "../../c/d", false},
		{"dot target", "link", ".", false},
		{"explicit relative", "link", "./file.txt", false},
		{"deep entry climbing to root of dest", "a/b/c/link", "../../..", false},

		{"escapes by one level", "link", "..", true},
		{"escapes from nested entry", "a/link", "../..", true},
		{"escapes far", "link", "../../../../etc/passwd", true},
		{"absolute unix path", "link", "/etc/passwd", true},
		{"absolute root", "link", "/", true},
		{"empty target", "link", "", true},
		{"climbs out through the middle", "a/link", "../../b/../..", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSymlinkTarget(tc.entryName, tc.linkname)
			if tc.wantErr && err == nil {
				t.Errorf("validateSymlinkTarget(%q, %q) = nil, want error", tc.entryName, tc.linkname)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateSymlinkTarget(%q, %q) = %v, want nil", tc.entryName, tc.linkname, err)
			}
		})
	}
}
