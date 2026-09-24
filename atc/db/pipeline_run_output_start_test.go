package db_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Run output capture deadlines", func() {
	DescribeTable("preserves supported terms at microsecond precision", func(term time.Duration) {
		ctx := context.Background()
		consumer, err := db.HangarConsumerPrefixHeld("capture-deadline-test")
		Expect(err).NotTo(HaveOccurred())
		hangarActivateEpoch(ctx, db.NewHangarOutputRepository(consumer))
		_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
		Expect(err).NotTo(HaveOccurred())
		task := &atc.TaskStep{
			Name: "produce", TaskID: uuid.NewString(), RunResult: &atc.RunResult{Name: "result", Output: "result"},
			Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Outputs: []atc.TaskOutputConfig{{Name: "result"}}},
		}
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "capture-deadline"}, atc.Config{
			Template: true, Jobs: atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{{Config: task}}}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		factory := db.NewPipelineRunFactory(dbConn, lockFactory)
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		creation, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{ActivationEpoch: 1, HangarEpoch: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		tx, err = dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		var now time.Time
		Expect(tx.QueryRow(`SELECT now()`).Scan(&now)).To(Succeed())
		plan := atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunResult: task.RunResult, Config: task.Config}
		record, err := factory.PredeclareOutputTask(ctx, tx, creation.EntryBuilds[0].ID(), plan, 1, term, "node", "node-uid")
		Expect(err).NotTo(HaveOccurred())
		Expect(record.CaptureDeadline.Time.Sub(now)).To(Equal(term.Truncate(time.Microsecond)))
		Expect(tx.Commit()).To(Succeed())
	},
		Entry("minimum hour", output.MinCaptureDeadline),
		Entry("fractional microseconds", time.Hour+123456*time.Microsecond+789*time.Nanosecond),
		Entry("default day", output.DefaultCaptureDeadline),
		Entry("maximum week", output.MaxCaptureDeadline),
	)
})
