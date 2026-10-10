package cli_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"regexp"

	cmd "github.com/concourse/concourse/atc/db/migration/cli/command"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Migration CLI", func() {

	Context("generate migration files", func() {
		var (
			migrationDir  string
			command       *cmd.GenerateCommand
			migrationName = "create_new_table"
			err           error
		)

		BeforeEach(func() {
			migrationDir, err = os.MkdirTemp("", "")
			DeferCleanup(func() {
				os.RemoveAll(migrationDir)
			})
			Expect(err).ToNot(HaveOccurred())

		})

		Context("sql migrations", func() {

			It("generates up and down migration files", func() {
				command = cmd.NewGenerateCommand(migrationDir, migrationName, "sql")
				sqlMigrationPattern := "^(\\d+)_(.*).(down|up).sql$"

				err := command.GenerateSQLMigration()
				Expect(err).ToNot(HaveOccurred())

				ExpectGeneratedFilesToMatchSpecification(migrationDir, sqlMigrationPattern,
					migrationName, func(migrationID, direction string, actualFileContents string) {
						if direction == "up" {
							Expect(actualFileContents).To(BeEmpty())
						} else {
							Expect(actualFileContents).To(Equal(fmt.Sprintf("DO $$ BEGIN RAISE EXCEPTION 'migration %s is forward-only: restore the pre-upgrade database backup instead'; END $$;\n", migrationID)))
						}
					})
			})
		})

		Context("go migration", func() {
			It("generates up and down migration files", func() {
				command = cmd.NewGenerateCommand(migrationDir, migrationName, "go")
				goMigrationPattern := "^(\\d+)_(.*).(down|up).go$"

				err := command.GenerateGoMigration()
				Expect(err).ToNot(HaveOccurred())

				ExpectGeneratedFilesToMatchSpecification(migrationDir, goMigrationPattern,
					migrationName, func(migrationID, direction string, actualFileContents string) {
						_, err := parser.ParseFile(token.NewFileSet(), "", actualFileContents, parser.AllErrors)
						Expect(err).NotTo(HaveOccurred())
						if direction == "up" {
							Expect(actualFileContents).To(ContainSubstring("func Up_%s() error", migrationID))
							Expect(actualFileContents).To(ContainSubstring("return nil"))
						} else {
							Expect(actualFileContents).To(ContainSubstring("func Down_%s() error", migrationID))
							Expect(actualFileContents).To(ContainSubstring(`import "errors"`))
							Expect(actualFileContents).To(ContainSubstring(`return errors.New("migration %s is forward-only: restore the pre-upgrade database backup instead")`, migrationID))
							Expect(actualFileContents).NotTo(ContainSubstring("return nil"))
						}
					})
			})

		})
	})
})

func ExpectGeneratedFilesToMatchSpecification(migrationDir, fileNamePattern, migrationName string,
	checkContents func(migrationID, direction string, actualFileContents string)) {
	GinkgoHelper()

	files, err := os.ReadDir(migrationDir)
	Expect(err).ToNot(HaveOccurred())
	var migrationFilesCount = 0
	regex := regexp.MustCompile(fileNamePattern)
	for _, migrationFile := range files {
		migrationFileName := migrationFile.Name()
		if !regex.MatchString(migrationFileName) {
			continue
		}
		matches := regex.FindStringSubmatch(migrationFileName)

		Expect(matches).To(HaveLen(4))
		Expect(matches[2]).To(Equal(migrationName))

		fileContents, err := os.ReadFile(path.Join(migrationDir, migrationFileName))
		Expect(err).ToNot(HaveOccurred())
		checkContents(matches[1], matches[3], string(fileContents))
		migrationFilesCount++
	}
	Expect(migrationFilesCount).To(Equal(2))
}
