package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// direct is any way to write to the process's stdout or stderr without the redacting writer.
var direct = regexp.MustCompile(`\bos\.(Stdout|Stderr)\b|\blog\.(Print|Fatal|Panic|Default|Writer|Output|Set)\w*\(|\bfmt\.Print|\bprint(ln)?\(|^\s*"log"\s*$`)

var _ = Describe("process output", func() {
	It("is wired in one place: no other file writes to stdout, stderr or the default logger", func() {
		_, self, _, _ := runtime.Caller(0)
		root := filepath.Join(filepath.Dir(self), "..", "..")
		var found []string
		scanned := 0
		Expect(filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
				path == filepath.Join(root, "cmd", "queue", "main.go") || path == filepath.Join(root, "cmd", "queue-resource", "main.go") {
				return err
			}
			scanned++
			b, err := os.ReadFile(path)
			for i, l := range strings.Split(string(b), "\n") {
				if direct.MatchString(l) {
					found = append(found, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(l)))
				}
			}
			return err
		})).To(Succeed())
		Expect(found).To(BeEmpty())
		Expect(scanned).To(BeNumerically(">=", 19), "the scan must see the production files")
	})
})
