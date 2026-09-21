package db_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Pipeline run reclamation with durable executions", func() {
	It("collects Run checks like ordinary checks and deletes the rest with the payload", func() {
		ctx := context.Background()
		consumer, err := db.HangarConsumerPrefixHeld("reclaim-test")
		Expect(err).NotTo(HaveOccurred())
		hangarActivateEpoch(ctx, db.NewHangarOutputRepository(consumer))
		_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
		Expect(err).NotTo(HaveOccurred())

		keepLast := 1
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "retained-checks"}, atc.Config{
			Template:      true,
			RunRetention:  &atc.RunRetentionConfig{KeepLast: &keepLast},
			Jobs:          atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source"}}}}},
			Resources:     atc.ResourceConfigs{{Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"}}},
			ResourceTypes: atc.ResourceTypes{{Name: "custom", Type: "some-base-resource-type", Source: atc.Source{"repository": "custom"}}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		factory := db.NewPipelineRunFactory(dbConn, lockFactory)
		create := func() db.RunCreation {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			creation, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{ActivationEpoch: 1})
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())
			return creation
		}
		victim := create()
		payload, found, err := factory.InstancePipeline(victim.Run)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		resource, found, err := payload.Resource("source")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		resourceTypes, err := payload.ResourceTypes()
		Expect(err).NotTo(HaveOccurred())
		Expect(resourceTypes).To(HaveLen(1))

		public, private, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		signer, err := executioncontrol.NewAcknowledgementSigner(private)
		Expect(err).NotTo(HaveOccurred())
		verifier := hangaroutput.ControlKeyRing{ActivationEpoch: 1, Keys: []hangaroutput.ControlKeyEntry{{Epoch: 1, PublicKey: base64.StdEncoding.EncodeToString(public)}}}
		var admissions []db.RunExecutionAdmission
		var executed, collected []db.Build
		var newestTypeCheck db.Build
		for _, checkable := range []db.Checkable{resource, resourceTypes[0]} {
			check, created, err := checkFactory.TryCreateCheck(ctx, checkable, resourceTypes, nil, true, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeTrue())
			Expect(check.ID()).NotTo(BeZero(), "Run checks must use persisted builds")
			_, err = dbConn.Exec(`UPDATE builds SET pipeline_id=NULL, resource_id=NULL, resource_type_id=NULL WHERE id=$1`, check.ID())
			Expect(err).To(MatchError(ContainSubstring("builds_pipeline_run_identity_complete")), "active checks cannot become detached evidence")
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			admission, owned, err := factory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
				BuildID: check.ID(), PlanID: check.PrivatePlan().ID, Kind: db.ContainerTypeCheck,
				Epoch: 1, NodeName: "node", NodeUID: "node-uid",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(owned).To(BeTrue())
			Expect(admission.RunID).To(Equal(victim.Run.ID()))
			start, err := signer.Sign(executioncontrol.Acknowledgement{
				ProtocolVersion: executioncontrol.ProtocolVersion, Kind: executioncontrol.AcknowledgementStart,
				Identity: admission.Identity, ActivationEpoch: 1, LedgerSequence: 1,
				NodeUID: "node-uid", PodUID: "pod-uid", ProcessIdentity: "check-process",
				ObservedAt: output.NewTimestamp(time.Now()),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(factory.RecordRunExecutionWitness(ctx, tx, check.ID(), admission.PlanID, start, verifier)).To(Succeed())
			finish := start
			finish.Kind = executioncontrol.AcknowledgementFinish
			finish.LedgerSequence++
			finish.Outcome = &executioncontrol.ExitOutcome{ExitCode: 0}
			finish, err = signer.Sign(finish)
			Expect(err).NotTo(HaveOccurred())
			Expect(factory.RecordRunExecutionWitness(ctx, tx, check.ID(), admission.PlanID, finish, verifier)).To(Succeed())
			Expect(tx.Commit()).To(Succeed())
			Expect(check.SaveEvent(event.Log{Payload: "executed check output"})).To(Succeed())
			Expect(check.Finish(db.BuildStatusSucceeded)).To(Succeed())
			admissions = append(admissions, admission)
			executed = append(executed, check)
			// Supersede the executed check with checks that never executed.
			// Both kinds follow the ordinary rules once closed.
			for i := range 2 {
				if _, isType := checkable.(db.ResourceType); isType && i == 1 {
					// The newest type check fetched its custom image with a
					// get inside the check build; reclamation takes both.
					f := runCheckFixture{ctx: ctx, factory: factory, run: victim.Run, resourceTypes: resourceTypes, signer: signer, verifier: verifier}
					newestTypeCheck, _ = f.check(checkable, executedClosedWithImageGet)
					continue
				}
				check, created, err := checkFactory.TryCreateCheck(ctx, checkable, resourceTypes, nil, true, false, false)
				Expect(err).NotTo(HaveOccurred())
				Expect(created).To(BeTrue())
				Expect(check.SaveEvent(event.Log{Payload: "unexecuted check output"})).To(Succeed())
				Expect(check.Finish(db.BuildStatusSucceeded)).To(Succeed())
				collected = append(collected, check)
			}
		}
		ordinary, created, err := defaultResource.CreateBuild(ctx, true, atc.Plan{Check: &atc.CheckPlan{Name: defaultResource.Name()}})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(ordinary.Finish(db.BuildStatusSucceeded)).To(Succeed())
		Expect(db.NewCheckLifecycle(dbConn).DeleteCompletedChecks(logger)).To(Succeed())
		_, found, err = buildFactory.Build(ordinary.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "retained checks must not block unrelated check cleanup")
		for _, check := range append(executed[1:], collected...) {
			found, err := check.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "a superseded Run check, closed or never executed, is collected like any other check")
		}
		// The unexecuted resource checks after it had no scope to keep them,
		// so the closed executed one is now its resource's newest and stays.
		found, err = executed[0].Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "a resource's newest check is kept while the Run is live")
		found, err = newestTypeCheck.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "the newest check of a live Run's resource type is kept, as for any resource type")

		entry, found, err := payload.Job("entry")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		build := victim.EntryBuilds[0]
		started, err := build.Start(atc.Plan{})
		Expect(err).NotTo(HaveOccurred())
		Expect(started).To(BeTrue())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		consumeObservedSchedule(entry)
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		completed, err := factory.FinalizeOutputRun(ctx, tx, victim.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeTrue())
		Expect(tx.Commit()).To(Succeed())
		before, found, err := factory.TerminalResult(ctx, victim.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(before.Status).To(Equal(atc.RunStatusSucceeded))
		Expect(before.Version).NotTo(BeEmpty())
		create() // Move the completed Run outside the keep-last window.

		destroyed, err := db.NewPipelineRunReclaimLifecycle(dbConn).DestroyReclaimableRun(victim.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeTrue())
		expectPipelineExists(payload.ID(), false)
		found, err = newestTypeCheck.Reload()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "reclamation deletes checks with the payload, as pipeline deletion does")
		Expect(checkRows(newestTypeCheck.ID())["executions"]).To(BeZero(), "a closed image get goes with its check")
		Expect(db.NewCheckLifecycle(dbConn).DeleteCompletedChecks(logger)).To(Succeed())
		var orphaned int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM check_build_events WHERE build_id=$1`, newestTypeCheck.ID()).Scan(&orphaned)).To(Succeed())
		Expect(orphaned).To(BeZero(), "a deleted check's events are reaped")
		after, found, err := factory.TerminalResult(ctx, victim.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(after).To(Equal(before))
		_, err = dbConn.Exec(`INSERT INTO builds(name,status,completed,team_id,pipeline_run_id) VALUES ('check','succeeded',true,$1,$2)`, defaultTeam.ID(), victim.Run.ID())
		Expect(err).To(MatchError(ContainSubstring("Run check admission requires a resource or resource type")), "new checks cannot bypass live admission by starting detached")
		for _, admission := range admissions {
			_, found, err := buildFactory.Build(admission.BuildID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, found, err = factory.RunExecution(ctx, tx, admission.BuildID, admission.PlanID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "a closed check execution is inert and goes with its build")
			Expect(tx.Commit()).To(Succeed())
		}
		detached, found, err := buildFactory.Build(build.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "job builds are retained with the Run header")
		Expect(detached.PipelineID()).To(BeZero())
	})
})
