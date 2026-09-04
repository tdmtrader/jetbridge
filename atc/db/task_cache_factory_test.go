package db_test

import (
	"strings"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// countingTaskCacheTx records every statement a task-cache transaction issues,
// so a test can assert how many round trips one creation costs.
type countingTaskCacheTx struct {
	db.Tx
	statements *[]string
}

func (tx countingTaskCacheTx) QueryRow(query string, args ...any) sq.RowScanner {
	*tx.statements = append(*tx.statements, query)
	return tx.Tx.QueryRow(query, args...)
}

type countingTaskCacheConn struct {
	db.DbConn
	statements *[]string
}

func (conn countingTaskCacheConn) Begin() (db.Tx, error) {
	tx, err := conn.DbConn.Begin()
	if err != nil {
		return nil, err
	}
	return countingTaskCacheTx{Tx: tx, statements: conn.statements}, nil
}

var _ = Describe("TaskCacheFactory", func() {

	Describe("FindOrCreate", func() {
		Context("when there is no existing task cache", func() {
			It("creates resource cache in database", func() {
				usedTaskCache, err := taskCacheFactory.FindOrCreate(
					atc.TaskCacheIdentity{JobID: defaultJob.ID()},
					"some-step",
					"some-path",
				)
				Expect(err).ToNot(HaveOccurred())
				Expect(usedTaskCache.ID()).ToNot(BeNil())
			})
		})

		Context("when there is existing task cache", func() {
			var (
				usedTaskCache db.UsedTaskCache
				err           error
			)

			BeforeEach(func() {
				usedTaskCache, err = taskCacheFactory.FindOrCreate(
					atc.TaskCacheIdentity{JobID: defaultJob.ID()},
					"some-step",
					"some-path",
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("creates a new task cache for another task", func() {
				otherTaskCache, err := taskCacheFactory.FindOrCreate(
					atc.TaskCacheIdentity{JobID: defaultJob.ID()},
					"some-other-step",
					"some-path",
				)
				Expect(err).ToNot(HaveOccurred())
				Expect(otherTaskCache.ID()).ToNot(Equal(usedTaskCache.ID()))
			})
		})
	})

	Describe("Find", func() {
		Context("when there is no existing task cache", func() {
			It("returns no found", func() {
				usedTaskCache, found, err := taskCacheFactory.Find(
					atc.TaskCacheIdentity{JobID: defaultJob.ID()},
					"some-step",
					"some-path",
				)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(usedTaskCache).To(BeNil())
			})
		})

		Context("when there is existing task cache", func() {
			var (
				usedTaskCache db.UsedTaskCache
				err           error
			)

			BeforeEach(func() {
				usedTaskCache, err = taskCacheFactory.FindOrCreate(
					atc.TaskCacheIdentity{JobID: defaultJob.ID()},
					"some-step",
					"some-path",
				)
				Expect(err).ToNot(HaveOccurred())
			})

			It("finds task cache in database", func() {
				utc, found, err := taskCacheFactory.Find(atc.TaskCacheIdentity{JobID: defaultJob.ID()}, "some-step", "some-path")
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(utc.ID()).To(Equal(usedTaskCache.ID()))
			})
		})
	})

	Describe("run task cache identity", func() {
		It("shares one row for materialized jobs from the same template", func() {
			// This fails if an ephemeral payload job ID leaks into run cache identity.
			template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "cache-template"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
			Expect(err).NotTo(HaveOccurred())
			identity := atc.TaskCacheIdentity{TeamID: defaultTeam.ID(), TemplatePipelineID: template.ID(), RunJobName: "deploy-staging"}
			first, err := taskCacheFactory.FindOrCreate(identity, "task", "cache")
			Expect(err).NotTo(HaveOccurred())
			second, err := taskCacheFactory.FindOrCreate(identity, "task", "cache")
			Expect(err).NotTo(HaveOccurred())
			Expect(second.ID()).To(Equal(first.ID()))
			Expect(second.Identity()).To(Equal(identity))
		})

		It("takes the created row's identity from its own upsert", func() {
			// This fails if creation re-reads a row the INSERT ... RETURNING
			// already identified: the extra read costs a round trip on every
			// task-cache initialisation and, because its found flag is
			// discarded, a miss would hand the caller a nil UsedTaskCache.
			template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "upsert-cache-template"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
			Expect(err).NotTo(HaveOccurred())

			var statements []string
			factory := db.NewTaskCacheFactory(countingTaskCacheConn{DbConn: dbConn, statements: &statements})
			identity := atc.TaskCacheIdentity{TeamID: defaultTeam.ID(), TemplatePipelineID: template.ID(), RunJobName: "deploy-staging"}

			created, err := factory.FindOrCreate(identity, "task", "cache")
			Expect(err).NotTo(HaveOccurred())
			Expect(created).NotTo(BeNil())
			Expect(created.ID()).To(BeNumerically(">", 0))
			Expect(created.Identity()).To(Equal(identity))

			reads := 0
			for _, statement := range statements {
				if strings.HasPrefix(statement, "SELECT") {
					reads++
				}
			}
			Expect(reads).To(Equal(1), "creation must read once to look for the row, then trust its own upsert")
		})

		It("keeps template and materialized job names separate", func() {
			// This fails if distinct shared scopes collapse into one cache row.
			firstTemplate, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "first-cache-template"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
			Expect(err).NotTo(HaveOccurred())
			secondTemplate, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "second-cache-template"}, atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "entry"}}}, 0, false)
			Expect(err).NotTo(HaveOccurred())
			first, err := taskCacheFactory.FindOrCreate(atc.TaskCacheIdentity{TeamID: defaultTeam.ID(), TemplatePipelineID: firstTemplate.ID(), RunJobName: "deploy-staging"}, "task", "cache")
			Expect(err).NotTo(HaveOccurred())
			otherTemplate, err := taskCacheFactory.FindOrCreate(atc.TaskCacheIdentity{TeamID: defaultTeam.ID(), TemplatePipelineID: secondTemplate.ID(), RunJobName: "deploy-staging"}, "task", "cache")
			Expect(err).NotTo(HaveOccurred())
			otherName, err := taskCacheFactory.FindOrCreate(atc.TaskCacheIdentity{TeamID: defaultTeam.ID(), TemplatePipelineID: firstTemplate.ID(), RunJobName: "deploy-production"}, "task", "cache")
			Expect(err).NotTo(HaveOccurred())
			Expect(otherTemplate.ID()).NotTo(Equal(first.ID()))
			Expect(otherName.ID()).NotTo(Equal(first.ID()))
		})
	})
})
