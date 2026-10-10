package db_test

import (
	"context"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runinput"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Run invocation replay", func() {
	It("replays running and reclaimed Runs and refuses changed remaining intent", func() {
		ctx := context.Background()
		keep := 1
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "replayed"}, atc.Config{
			Template: true, Params: []atc.ParamSchema{{Name: "target", Type: atc.ParamTypeString, Default: "original"}},
			RunRetention: &atc.RunRetentionConfig{KeepLast: &keep}, Jobs: atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		factory := db.NewPipelineRunFactory(dbConn, lockFactory)
		create := func(key string, params atc.RunParams) (db.RunCreation, error) {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			result, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{Vars: params}, "owner", db.RunCreationOpts{
				Invocation: &db.RunInvocationIdentity{PrincipalDigest: runinput.PrincipalDigest("owner"), KeyDigest: strings.Repeat(key, 64)},
			})
			if err != nil {
				return result, err
			}
			return result, tx.Commit()
		}
		first, err := create("a", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Replayed).To(BeFalse())
		verify := func() {
			GinkgoHelper()
			replay, err := create("a", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(replay.Replayed).To(BeTrue())
			Expect(replay.Run.ID()).To(Equal(first.Run.ID()))
			for _, params := range []atc.RunParams{{"target": "changed"}, {"target": "original"}, {"target": nil}} {
				_, err = create("a", params)
				Expect(err).To(MatchError(db.ErrRunInvocationConflict))
			}
		}
		verify()
		finish := func(creation db.RunCreation) {
			GinkgoHelper()
			Expect(creation.EntryBuilds).To(HaveLen(1))
			started, err := creation.EntryBuilds[0].Start(atc.Plan{})
			Expect(err).NotTo(HaveOccurred())
			Expect(started).To(BeTrue())
			Expect(creation.EntryBuilds[0].Finish(db.BuildStatusFailed)).To(Succeed())
			payload, found, err := defaultTeam.Pipeline(atc.PipelineRef{
				Name: template.Name(), InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			entry, found, err := payload.Job("entry")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			consumeObservedSchedule(entry)
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			done, err := factory.FinalizeOutputRun(ctx, tx, creation.Run.ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(done).To(BeTrue())
			Expect(tx.Commit()).To(Succeed())
		}
		finish(first)
		verify()
		second, err := create("b", nil)
		Expect(err).NotTo(HaveOccurred())
		finish(second)
		reclaimed, err := db.NewPipelineRunReclaimLifecycle(dbConn).DestroyReclaimableRun(first.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(reclaimed).To(BeTrue())
		verify()
		terminal, found, err := factory.TerminalResult(ctx, first.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(terminal.Status).To(Equal(atc.RunStatusFailed))
	})
})
