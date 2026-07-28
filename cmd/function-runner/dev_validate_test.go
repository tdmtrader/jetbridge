package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyCandidatePreservesContainedSymlinkAndRejectsEscapingOne(t *testing.T) {
	for name, target := range map[string]string{
		"contained": "file.txt",
		"escaping":  "../../outside",
	} {
		t.Run(name, func(t *testing.T) {
			source, destination := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("safe"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(source, "link")); err != nil {
				t.Fatal(err)
			}
			err := copyCandidate(context.Background(), source, destination)
			if name == "escaping" {
				if err == nil {
					t.Fatal("escaping candidate symlink copied")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.Readlink(filepath.Join(destination, "link"))
			if err != nil || got != target {
				t.Fatalf("copied symlink = %q, %v", got, err)
			}
		})
	}
}

func TestExactRepositoryBaseRejectsMissingAndAmbiguousRepositoryBases(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"base-a", "base-b"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	for name, options := range map[string]devValidateOptions{
		"missing":   {bases: []string{"base-a"}, baseRefs: []string{"base-a=1,opaque/v1," + digest}},
		"ambiguous": {bases: []string{"base-a", "base-b"}, baseRefs: []string{"base-a=1,repository/v1," + digest, "base-b=2,repository/v1," + digest}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := exactRepositoryBase(options, root); err == nil {
				t.Fatal("invalid base binding accepted")
			}
		})
	}
}
