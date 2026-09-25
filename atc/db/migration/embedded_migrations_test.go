package migration_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/atc/db/migration"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("embedded migration definitions", func() {
	It("matches a fresh parse of every migration shipped in the source tree", func() {
		fresh, err := migration.NewMigratorForMigrations(nil, nil, os.DirFS("migrations")).Migrations()
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh).NotTo(BeEmpty())
		embedded, err := migration.NewMigrator(nil, nil).Migrations()
		Expect(err).NotTo(HaveOccurred())
		Expect(embedded).To(Equal(fresh))
	})

	It("does not let a caller change another migrator's definitions", func() {
		first, err := migration.NewMigrator(nil, nil).Migrations()
		Expect(err).NotTo(HaveOccurred())
		Expect(first).NotTo(BeEmpty())
		original := first[0]
		first[0].Name = "changed by caller"
		first[0].Version = -1
		first[0].Direction = "changed by caller"
		first[0].Statements = "changed by caller"
		first[0].Strategy = migration.Strategy(-1)

		next, err := migration.NewMigrator(nil, nil).Migrations()
		Expect(err).NotTo(HaveOccurred())
		Expect(next[0]).To(Equal(original))
	})

	It("rereads caller-supplied filesystems after changes and parse errors", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "1000_example.up.sql")
		Expect(os.WriteFile(path, []byte("SELECT 1;"), 0600)).To(Succeed())
		migrator := migration.NewMigratorForMigrations(nil, nil, os.DirFS(dir))
		first, err := migrator.Migrations()
		Expect(err).NotTo(HaveOccurred())
		Expect(first).NotTo(BeEmpty())
		Expect(first[0].Statements).To(Equal("SELECT 1;"))

		Expect(os.WriteFile(path, []byte("SELECT 2;"), 0600)).To(Succeed())
		next, err := migrator.Migrations()
		Expect(err).NotTo(HaveOccurred())
		Expect(next[0].Statements).To(Equal("SELECT 2;"))

		invalid := filepath.Join(dir, "1001_invalid.sql")
		Expect(os.WriteFile(invalid, []byte("SELECT 3;"), 0600)).To(Succeed())
		_, err = migrator.Migrations()
		Expect(err).To(MatchError(ContainSubstring(migration.ErrCouldNotParseDirection.Error())))
		Expect(os.Remove(invalid)).To(Succeed())
		recovered, err := migrator.Migrations()
		Expect(err).NotTo(HaveOccurred())
		Expect(recovered).To(Equal(next))
	})
})

func BenchmarkEmbeddedMigrations(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		definitions, err := migration.NewMigrator(nil, nil).Migrations()
		if err != nil {
			b.Fatal(err)
		}
		if len(definitions) == 0 {
			b.Fatal("embedded migration discovery returned no definitions")
		}
	}
}
