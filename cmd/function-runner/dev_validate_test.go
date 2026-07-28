package main

import (
	"context"
	"os"
	"path/filepath"
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
