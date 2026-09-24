package db_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strconv"
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

// runCheckFixture is one running v2 Run whose payload has a resource and a
// resource type, with a signer for the execution witnesses a node would send.
type runCheckFixture struct {
	ctx           context.Context
	factory       db.PipelineRunFactory
	template      db.Pipeline
	run           db.PipelineRun
	entryBuild    db.Build
	resource      db.Resource
	resourceTypes db.ResourceTypes
	signer        *executioncontrol.AcknowledgementSigner
	verifier      hangaroutput.ControlKeyRing
}

func newRunCheckFixture(name string) runCheckFixture {
	GinkgoHelper()
	return newRunCheckFixtureWithRetention(name, nil)
}

func newRunCheckFixtureWithRetention(name string, retention *atc.RunRetentionConfig) runCheckFixture {
	GinkgoHelper()
	ctx := context.Background()
	consumer, err := db.HangarConsumerPrefixHeld("check-retention-test")
	Expect(err).NotTo(HaveOccurred())
	hangarActivateEpoch(ctx, db.NewHangarOutputRepository(consumer))
	_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
	Expect(err).NotTo(HaveOccurred())

	template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: name}, atc.Config{
		Template:      true,
		RunRetention:  retention,
		Jobs:          atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source"}}}}},
		Resources:     atc.ResourceConfigs{{Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"}}},
		ResourceTypes: atc.ResourceTypes{{Name: "custom", Type: "some-base-resource-type", Source: atc.Source{"repository": "custom"}}},
	}, 0, false)
	Expect(err).NotTo(HaveOccurred())
	factory := db.NewPipelineRunFactory(dbConn, lockFactory)
	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	creation, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{ActivationEpoch: 1, HangarEpoch: 1})
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())
	payload, found, err := factory.InstancePipeline(creation.Run)
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
	return runCheckFixture{
		ctx: ctx, factory: factory, template: template, run: creation.Run, entryBuild: creation.EntryBuilds[0], resource: resource, resourceTypes: resourceTypes, signer: signer,
		verifier: hangaroutput.ControlKeyRing{ActivationEpoch: 1, Keys: []hangaroutput.ControlKeyEntry{{Epoch: 1, PublicKey: base64.StdEncoding.EncodeToString(public)}}},
	}
}

type checkExecution int

const (
	unexecuted checkExecution = iota
	executedOpen
	executedClosed
	// executedClosedWithImageGet is a closed check whose custom image was
	// fetched by a get inside the check build, as FetchImage does on a
	// resource cache miss for an image that is not a registry-image.
	executedClosedWithImageGet
)

// check creates one Run check. An unexecuted or closed check is finished; an
// open one is left unfinished, as a build cannot finish before its closure.
func (f runCheckFixture) check(checkable db.Checkable, execution checkExecution) (db.Build, db.RunExecutionAdmission) {
	GinkgoHelper()
	check, created, err := checkFactory.TryCreateCheck(f.ctx, checkable, f.resourceTypes, nil, true, false, false)
	Expect(err).NotTo(HaveOccurred())
	Expect(created).To(BeTrue())
	Expect(check.SaveEvent(event.Log{Payload: "check output"})).To(Succeed())
	var admission db.RunExecutionAdmission
	if execution == executedClosedWithImageGet {
		f.execute(check, check.PrivatePlan().ID+"/image-get", db.ContainerTypeGet, true)
	}
	if execution != unexecuted {
		admission = f.execute(check, check.PrivatePlan().ID, db.ContainerTypeCheck, execution != executedOpen)
	}
	if execution != executedOpen {
		Expect(check.Finish(db.BuildStatusSucceeded)).To(Succeed())
	}
	return check, admission
}

// execute admits one execution of a Run build and records its start witness
// and, when closed, its finish.
func (f runCheckFixture) execute(build db.Build, planID atc.PlanID, kind db.ContainerType, closed bool) db.RunExecutionAdmission {
	GinkgoHelper()
	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	admission, owned, err := f.factory.AdmitRunExecution(f.ctx, tx, db.RunExecutionRequest{
		BuildID: build.ID(), PlanID: planID, Kind: kind,
		Epoch: 1, NodeName: "node", NodeUID: "node-uid",
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(owned).To(BeTrue())
	start, err := f.signer.Sign(executioncontrol.Acknowledgement{
		ProtocolVersion: executioncontrol.ProtocolVersion, Kind: executioncontrol.AcknowledgementStart,
		Identity: admission.Identity, ActivationEpoch: 1, LedgerSequence: 1,
		NodeUID: "node-uid", PodUID: "pod-uid", ProcessIdentity: executioncontrol.ProcessIdentity(string(kind) + "-process"),
		ObservedAt: output.NewTimestamp(time.Now()),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(f.factory.RecordRunExecutionWitness(f.ctx, tx, build.ID(), admission.PlanID, start, f.verifier)).To(Succeed())
	if closed {
		finish := start
		finish.Kind = executioncontrol.AcknowledgementFinish
		finish.LedgerSequence++
		finish.Outcome = &executioncontrol.ExitOutcome{ExitCode: 0}
		finish, err = f.signer.Sign(finish)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.factory.RecordRunExecutionWitness(f.ctx, tx, build.ID(), admission.PlanID, finish, f.verifier)).To(Succeed())
	}
	Expect(tx.Commit()).To(Succeed())
	return admission
}

// checkRows counts every row check collection must remove with a check;
// events only say whether any remain.
func checkRows(buildID int) map[string]int {
	GinkgoHelper()
	rows := map[string]int{}
	for name, query := range map[string]string{
		"builds":             `SELECT count(*) FROM builds WHERE id=$1`,
		"executions":         `SELECT count(*) FROM pipeline_run_executions WHERE build_id=$1`,
		"starts":             `SELECT count(*) FROM pipeline_run_execution_starts s JOIN pipeline_run_executions e USING(execution_id,execution_fence) WHERE e.build_id=$1`,
		"check_build_events": `SELECT least(count(*),1) FROM check_build_events WHERE build_id=$1`,
	} {
		var count int
		Expect(dbConn.QueryRow(query, buildID).Scan(&count)).To(Succeed())
		rows[name] = count
	}
	return rows
}

func orphanedWitnesses(admission db.RunExecutionAdmission) int {
	GinkgoHelper()
	var count int
	Expect(dbConn.QueryRow(`SELECT (SELECT count(*) FROM pipeline_run_execution_starts WHERE execution_id=$1)
		+ (SELECT count(*) FROM pipeline_run_execution_closures WHERE execution_id=$1)`, string(admission.Identity.ExecutionID)).Scan(&count)).To(Succeed())
	return count
}

var _ = Describe("Run check collection", func() {
	It("keeps only the newest closed executed check per scope and collects the rest with their evidence", func() {
		f := newRunCheckFixture("closed-checks")
		var stale []db.Build
		var staleAdmissions []db.RunExecutionAdmission
		var newest []db.Build
		for _, checkable := range []db.Checkable{f.resource, f.resourceTypes[0]} {
			for i := range 3 {
				check, admission := f.check(checkable, executedClosed)
				if i < 2 {
					stale = append(stale, check)
					staleAdmissions = append(staleAdmissions, admission)
				} else {
					newest = append(newest, check)
				}
			}
		}
		ordinary, created, err := defaultResource.CreateBuild(f.ctx, true, atc.Plan{Check: &atc.CheckPlan{Name: defaultResource.Name()}})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		Expect(ordinary.Finish(db.BuildStatusSucceeded)).To(Succeed())

		Expect(db.NewCheckLifecycle(dbConn).DeleteCompletedChecks(logger)).To(Succeed())

		for i, check := range stale {
			Expect(checkRows(check.ID())).To(Equal(map[string]int{"builds": 0, "executions": 0, "starts": 0, "check_build_events": 0}),
				"a closed, superseded check execution is inert and is collected with its build and events")
			Expect(orphanedWitnesses(staleAdmissions[i])).To(BeZero())
		}
		for _, check := range newest {
			Expect(checkRows(check.ID())).To(Equal(map[string]int{"builds": 1, "executions": 1, "starts": 1, "check_build_events": 1}),
				"the newest check of each scope is kept while the Run is live")
		}
		_, found, err := buildFactory.Build(ordinary.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "unrelated checks are collected as before")
	})

	It("collects a superseded check whose image was fetched by a get inside it", func() {
		f := newRunCheckFixture("image-get-checks")
		var stale []db.Build
		for _, checkable := range []db.Checkable{f.resource, f.resourceTypes[0]} {
			check, _ := f.check(checkable, executedClosedWithImageGet)
			stale = append(stale, check)
			f.check(checkable, executedClosed)
		}
		var executions []string
		for _, check := range stale {
			rows, err := dbConn.Query(`SELECT execution_id FROM pipeline_run_executions WHERE build_id=$1 ORDER BY kind`, check.ID())
			Expect(err).NotTo(HaveOccurred())
			var kinds int
			for rows.Next() {
				var id string
				Expect(rows.Scan(&id)).To(Succeed())
				executions = append(executions, id)
				kinds++
			}
			Expect(rows.Close()).To(Succeed())
			Expect(kinds).To(Equal(2), "the check build carries its check and its image get")
		}

		Expect(db.NewCheckLifecycle(dbConn).DeleteCompletedChecks(logger)).To(Succeed())

		for _, check := range stale {
			Expect(checkRows(check.ID())).To(Equal(map[string]int{"builds": 0, "executions": 0, "starts": 0, "check_build_events": 0}),
				"a closed image get is as inert as the closed check it served")
		}
		for _, id := range executions {
			Expect(orphanedWitnesses(db.RunExecutionAdmission{Identity: executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(id)}})).To(BeZero())
		}
	})

	It("keeps a check build that a Run output start names", func() {
		f := newRunCheckFixture("output-start-checks")
		named, _ := f.check(f.resource, executedClosedWithImageGet)
		f.check(f.resource, executedClosed)
		// No production path starts a Run output on a check build; only a
		// direct write can, and collection must not delete what it names.
		_, err := dbConn.Exec(`ALTER TABLE pipeline_run_output_starts DISABLE TRIGGER ALL`)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`INSERT INTO pipeline_run_output_starts(run_id,build_id,task_id,result_name,task_name,node_name,node_uid,handoff_id)
			VALUES ($1,$2,gen_random_uuid(),'result','task','node','node-uid',gen_random_uuid())`, f.run.ID(), named.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`ALTER TABLE pipeline_run_output_starts ENABLE TRIGGER ALL`)
		Expect(err).NotTo(HaveOccurred())

		Expect(db.NewCheckLifecycle(dbConn).DeleteCompletedChecks(logger)).To(Succeed())

		Expect(checkRows(named.ID())).To(Equal(map[string]int{"builds": 1, "executions": 2, "starts": 2, "check_build_events": 1}))
	})

	It("never collects an open check execution", func() {
		f := newRunCheckFixture("open-checks")
		open, _ := f.check(f.resource, executedOpen)
		// Finishing refuses a build whose execution is open, so only a direct
		// write can produce this; collection must not trust it either.
		completedOpen, _ := f.check(f.resource, executedOpen)
		_, err := dbConn.Exec(`UPDATE builds SET status='succeeded', completed=true WHERE id=$1`, completedOpen.ID())
		Expect(err).NotTo(HaveOccurred())
		f.check(f.resource, executedClosed)
		f.check(f.resource, executedClosed)

		Expect(db.NewCheckLifecycle(dbConn).DeleteCompletedChecks(logger)).To(Succeed())

		for _, check := range []db.Build{open, completedOpen} {
			Expect(checkRows(check.ID())).To(Equal(map[string]int{"builds": 1, "executions": 1, "starts": 1, "check_build_events": 1}))
		}
	})

	It("skips a check it cannot delete and still collects the rest of the batch", func() {
		f := newRunCheckFixture("stubborn-checks")
		refused, _ := f.check(f.resource, executedClosed)
		locked, _ := f.check(f.resource, executedClosed)
		collected, _ := f.check(f.resource, executedClosed)
		f.check(f.resource, executedClosed)
		_, err := dbConn.Exec(`CREATE FUNCTION refuse_one_check() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'refused by the test'; END $$`)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`CREATE TRIGGER refuse_one_check BEFORE DELETE ON builds FOR EACH ROW
			WHEN (OLD.id = ` + strconv.Itoa(refused.ID()) + `) EXECUTE FUNCTION refuse_one_check()`)
		Expect(err).NotTo(HaveOccurred())
		holder, err := openRunLifecycleConn().Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(holder)
		var id int
		Expect(holder.QueryRow(`SELECT id FROM builds WHERE id=$1 FOR UPDATE`, locked.ID()).Scan(&id)).To(Succeed())

		done := make(chan error, 1)
		go func() { done <- db.NewCheckLifecycle(dbConn).DeleteCompletedChecks(logger) }()
		Eventually(done).WithTimeout(10*time.Second).Should(Receive(BeNil()), "a locked check is skipped, not waited for")

		Expect(checkRows(refused.ID())["executions"]).To(Equal(1), "a refused deletion is rolled back whole")
		Expect(checkRows(refused.ID())["builds"]).To(Equal(1))
		Expect(checkRows(locked.ID())["builds"]).To(Equal(1))
		Expect(checkRows(collected.ID())["builds"]).To(BeZero(), "one refusal does not abort the batch")
	})

	It("reads a detached check's logs from the check event table", func() {
		f := newRunCheckFixture("detached-check-logs")
		check, _ := f.check(f.resource, unexecuted)
		// The retained state a reclaimed Run leaves a check in.
		_, err := dbConn.Exec(`UPDATE builds SET pipeline_id=NULL, resource_id=NULL, resource_type_id=NULL WHERE id=$1`, check.ID())
		Expect(err).NotTo(HaveOccurred())
		detached, found, err := buildFactory.Build(check.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		page, err := detached.EventPage(f.ctx, atc.BuildEventPageRequest{})
		Expect(err).NotTo(HaveOccurred())
		var logged string
		for _, record := range page.Events {
			logged += string(record.Data)
		}
		Expect(logged).To(ContainSubstring("check output"), "a detached check keeps its logs")
	})

	It("settles discovered cancellation work for checks collected afterwards", func() {
		f := newRunCheckFixture("collected-checks")
		unexecutedCheck, _ := f.check(f.resource, unexecuted)
		executedCheck, admission := f.check(f.resource, executedClosed)
		f.check(f.resource, executedClosed)

		// Cancellation discovers every build and execution of the Run before
		// collection removes the superseded checks.
		_, err := f.factory.RequestRunCancellation(f.ctx, f.run.ID(), "canceller", nil)
		Expect(err).NotTo(HaveOccurred())
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		lease, leased, err := f.factory.ClaimRunCancellationLease(f.ctx, tx, "check-retention-test", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(leased).To(BeTrue())
		_, err = f.factory.DiscoverRunCancellation(f.ctx, tx, lease, f.run.ID(), 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		Expect(db.NewCheckLifecycle(dbConn).DeleteCompletedChecks(logger)).To(Succeed())
		for _, check := range []db.Build{unexecutedCheck, executedCheck} {
			found, err := check.Reload()
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		}

		wanted := map[string]bool{
			string(db.CancelBuild) + "/" + strconv.Itoa(unexecutedCheck.ID()):                                                             true,
			string(db.CancelBuild) + "/" + strconv.Itoa(executedCheck.ID()):                                                               true,
			string(db.CancelExecution) + "/" + string(admission.Identity.ExecutionID) + "/" + strconv.Itoa(int(admission.Identity.Fence)): true,
		}
		settled := 0
		Eventually(func() int {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			op, claimed, err := f.factory.ClaimRunCancellationOperation(f.ctx, tx, lease, f.run.ID())
			Expect(err).NotTo(HaveOccurred())
			Expect(claimed).To(BeTrue())
			Expect(tx.Commit()).To(Succeed())
			if !wanted[string(op.Kind)+"/"+op.Subject] {
				return settled
			}
			delete(wanted, string(op.Kind)+"/"+op.Subject)
			if op.Kind == db.CancelBuild {
				debt, err := f.factory.ExecuteCancellationFinality(f.ctx, lease, op)
				Expect(err).NotTo(HaveOccurred(), "a collected check is a preserved terminal build, not unavailable work")
				Expect(debt).To(Equal(db.CancellationDone))
			} else {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)
				execution, err := f.factory.CancellationRunExecution(f.ctx, tx, lease, op)
				Expect(err).NotTo(HaveOccurred(), "a collected check execution was closed, not external work")
				Expect(execution.Closed).To(BeTrue())
			}
			settled++
			return settled
		}).WithTimeout(10*time.Second).Should(Equal(3), "cancellation must have discovered both collected checks and the execution")
	})
})
