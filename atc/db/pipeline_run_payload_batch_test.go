package db_test

import (
	"context"
	"database/sql"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// payloadBatchCountingConn is a pass-through decorator over the real
// connection, in the style of the other db_test connection decorators: every
// statement still executes against Postgres, the decorator only counts the
// connection-level round trips the factory makes.
type payloadBatchCountingConn struct {
	db.DbConn
	queries int
}

func (c *payloadBatchCountingConn) Query(query string, args ...any) (*sql.Rows, error) {
	c.queries++
	return c.DbConn.Query(query, args...)
}

func (c *payloadBatchCountingConn) QueryRow(query string, args ...any) sq.RowScanner {
	c.queries++
	return c.DbConn.QueryRow(query, args...)
}

var _ = Describe("PipelineRunFactory payload batching", func() {
	It("resolves a whole page of payloads in one query and agrees with the per-run path", func() {
		// This fails if the listing path resolves payloads one run at a time.
		// The runs route is reachable by an unauthenticated viewer on an exposed
		// template, so per-run resolution is bounded only by the pagination cap.
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "payload-batch"}, atc.Config{
			Template: true,
			Jobs:     atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		factory := db.NewPipelineRunFactory(dbConn, lockFactory)
		var runs []db.PipelineRun
		for i := 0; i < 5; i++ {
			creation, createErr := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
			Expect(createErr).NotTo(HaveOccurred())
			runs = append(runs, creation.Run)
		}
		reclaimed := runs[2]
		reclaimRunPayloadForTest(template, reclaimed)

		counting := &payloadBatchCountingConn{DbConn: dbConn}
		payloads, err := db.NewPipelineRunFactory(counting, lockFactory).InstancePipelines(runs)
		Expect(err).NotTo(HaveOccurred())
		Expect(counting.queries).To(Equal(1), "a page of runs must cost one payload query, not one per run")

		for _, run := range runs {
			single, found, lookupErr := factory.InstancePipeline(run)
			Expect(lookupErr).NotTo(HaveOccurred())
			batched, inBatch := payloads[run.ID()]

			if run.ID() == reclaimed.ID() {
				Expect(found).To(BeFalse())
				Expect(inBatch).To(BeFalse())
				// The absent entry must read as the nil db.Pipeline the
				// presenter treats as reclaimed.
				Expect(payloads[run.ID()]).To(BeNil())
				continue
			}

			Expect(found).To(BeTrue())
			Expect(inBatch).To(BeTrue())
			Expect(batched.ID()).To(Equal(single.ID()))
			Expect(batched.Name()).To(Equal(single.Name()))
			Expect(batched.TeamName()).To(Equal(single.TeamName()))
			Expect(batched.Public()).To(Equal(single.Public()))
			Expect(batched.InstanceVars()).To(Equal(single.InstanceVars()))
			Expect(batched.InstanceVars()).To(Equal(atc.InstanceVars{"run": float64(run.Number())}))
		}
	})

	It("returns an empty map without querying for an empty page", func() {
		counting := &payloadBatchCountingConn{DbConn: dbConn}
		payloads, err := db.NewPipelineRunFactory(counting, lockFactory).InstancePipelines(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(payloads).To(BeEmpty())
		Expect(counting.queries).To(BeZero())
	})
})
