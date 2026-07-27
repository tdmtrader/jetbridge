package migration_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A migration is a permanent statement about a schema at one point in time. A
// runtime package is the opposite: it is free to change its types, rename its
// constants, and delete a decoder the day the feature it served is retired.
// The moment a migration reaches into one, an already-applied migration's
// meaning becomes a function of today's code, and the migration chain stops
// being replayable — the exact failure the retired workflow-source decoder was
// quarantined to avoid.
//
// So the whole migration tree, tests included, is frozen off the agentic
// runtime and the step executors. Migrations that need to understand stored
// payloads carry their own private decoder; migration tests that need a
// fixture write the literal bytes.
//
// This replaces the four hard-coded file paths the workflow-signature spec
// used to check, which went stale the moment those files were deleted.
var forbiddenImportPrefixes = []string{
	"github.com/concourse/concourse/agent",
	"github.com/concourse/concourse/atc/exec",
	"github.com/concourse/concourse/atc/worker",
}

var _ = Describe("migration package isolation", func() {
	It("never imports the agentic runtime or the step executors", func() {
		fset := token.NewFileSet()

		var offenders []string
		err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}

			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}

			for _, imported := range file.Imports {
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				for _, forbidden := range forbiddenImportPrefixes {
					if importPath == forbidden || strings.HasPrefix(importPath, forbidden+"/") {
						offenders = append(offenders, path+" imports "+importPath)
					}
				}
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(offenders).To(BeEmpty(),
			"migrations must stay replayable without the runtime: inline the payload knowledge instead")
	})
})
