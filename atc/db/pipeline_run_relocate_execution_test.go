package db_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// An execution is admitted on a node before its step pod exists. A pod that
// is never bound there -- the node is full or gone -- moves the execution to
// another node, and only that execution: one that started stays put.
var _ = Describe("Relocating an unstarted Run execution", func() {
	var f *closureCaptures

	BeforeEach(func() { f = newClosureCaptures() })

	admit := func(build db.Build, plan atc.PlanID) db.RunExecutionAdmission {
		GinkgoHelper()
		var admission db.RunExecutionAdmission
		Expect(f.inTx(func(tx db.Tx) error {
			var owned bool
			var err error
			admission, owned, err = f.factory.AdmitRunExecution(f.ctx, tx, db.RunExecutionRequest{
				BuildID: build.ID(), PlanID: plan, Kind: db.ContainerTypeTask, NodeName: "node", NodeUID: "node-uid",
			})
			Expect(owned).To(BeTrue())
			return err
		})).To(Succeed())
		return admission
	}

	relocate := func(a db.RunExecutionAdmission, from, fromUID, to, toUID string) (db.RunExecutionAdmission, error) {
		var moved db.RunExecutionAdmission
		err := f.inTx(func(tx db.Tx) error {
			var err error
			moved, err = f.factory.RelocateRunExecution(f.ctx, tx, db.RunExecutionRelocation{
				BuildID: a.BuildID, PlanID: a.PlanID, Identity: a.Identity,
				FromName: from, FromUID: fromUID, ToName: to, ToUID: toUID,
			})
			return err
		})
		return moved, err
	}

	stored := func(a db.RunExecutionAdmission) db.RunExecutionAdmission {
		GinkgoHelper()
		var found db.RunExecutionAdmission
		Expect(f.inTx(func(tx db.Tx) error {
			var ok bool
			var err error
			found, ok, err = f.factory.RunExecution(f.ctx, tx, a.BuildID, a.PlanID)
			Expect(ok).To(BeTrue())
			return err
		})).To(Succeed())
		return found
	}

	It("moves the node and keeps the identity", func() {
		a := admit(f.review, "task-step")

		moved, err := relocate(a, "node", "node-uid", "other", "other-uid")
		Expect(err).NotTo(HaveOccurred())
		Expect(moved.Identity).To(Equal(a.Identity))
		Expect(moved.NodeName).To(Equal("other"))
		Expect(moved.NodeUID).To(Equal("other-uid"))

		Expect(stored(a).NodeName).To(Equal("other"))
		Expect(stored(a).Identity).To(Equal(a.Identity))

		By("answering a replay with the moved admission")
		again, err := relocate(a, "node", "node-uid", "other", "other-uid")
		Expect(err).NotTo(HaveOccurred())
		Expect(again).To(Equal(moved))

		By("admitting the start check on the new node and refusing the old one")
		Expect(f.inTx(func(tx db.Tx) error {
			admission, _, err := f.factory.AdmitRunExecution(f.ctx, tx, db.RunExecutionRequest{
				BuildID: a.BuildID, PlanID: a.PlanID, Kind: db.ContainerTypeTask, NodeName: "other", NodeUID: "other-uid",
			})
			Expect(admission.Identity).To(Equal(a.Identity))
			return err
		})).To(Succeed())
		Expect(f.inTx(func(tx db.Tx) error {
			_, _, err := f.factory.AdmitRunExecution(f.ctx, tx, db.RunExecutionRequest{
				BuildID: a.BuildID, PlanID: a.PlanID, Kind: db.ContainerTypeTask, NodeName: "node", NodeUID: "node-uid",
			})
			return err
		})).To(MatchError(output.ErrConflict))
	})

	It("never moves an execution that started", func() {
		a := admit(f.review, "task-step")
		f.witness(a, executioncontrol.AcknowledgementStart, nil)

		_, err := relocate(a, "node", "node-uid", "other", "other-uid")
		Expect(err).To(MatchError(output.ErrConflict))
		Expect(stored(a).NodeName).To(Equal("node"))

		By("refusing it in the database, whoever asks")
		_, err = dbConn.Exec(`UPDATE pipeline_run_executions SET node_name='other', node_uid='other-uid' WHERE build_id=$1 AND plan_id=$2`,
			a.BuildID, string(a.PlanID))
		Expect(err).To(MatchError(ContainSubstring("its node is immutable")))
	})

	It("refuses a move from a node the execution is not on", func() {
		a := admit(f.review, "task-step")
		_, err := relocate(a, "elsewhere", "elsewhere-uid", "other", "other-uid")
		Expect(err).To(MatchError(output.ErrConflict))
		Expect(stored(a).NodeName).To(Equal("node"))
	})

	It("keeps every other column of an admission immutable", func() {
		a := admit(f.review, "task-step")
		_, err := dbConn.Exec(`UPDATE pipeline_run_executions SET kind='get', node_name='other' WHERE build_id=$1 AND plan_id=$2`,
			a.BuildID, string(a.PlanID))
		Expect(err).To(MatchError(ContainSubstring("Run output evidence is immutable")))
		_, err = dbConn.Exec(`DELETE FROM pipeline_run_executions WHERE build_id=$1 AND plan_id=$2`, a.BuildID, string(a.PlanID))
		Expect(err).To(HaveOccurred())
	})

	It("moves a capturing execution's pending capture with it", func() {
		a := f.start(f.review, f.reviewPlan)

		moved, err := relocate(a, "node", "node-uid", "other", "other-uid")
		Expect(err).NotTo(HaveOccurred())
		Expect(moved.Capture).To(Equal(a.Capture))

		var node, nodeUID, state string
		Expect(dbConn.QueryRow(`SELECT node, node_uid, state FROM hangar_captures WHERE execution_id=$1 AND output_name=$2`,
			string(a.Capture.ExecutionID), string(a.Capture.Output)).Scan(&node, &nodeUID, &state)).To(Succeed())
		Expect([]string{node, nodeUID, state}).To(Equal([]string{"other", "other-uid", "pending"}))

		By("letting a replayed admission find the capture on its new node")
		Expect(f.inTx(func(tx db.Tx) error {
			admission, _, err := f.factory.AdmitRunExecution(f.ctx, tx, db.RunExecutionRequest{
				BuildID: a.BuildID, PlanID: a.PlanID, Kind: db.ContainerTypeTask,
				NodeName: "other", NodeUID: "other-uid", Capture: a.Capture,
			})
			Expect(admission.Identity).To(Equal(a.Identity))
			return err
		})).To(Succeed())
	})

	It("never moves a capture bound to a pod", func() {
		a := f.start(f.review, f.reviewPlan)
		_, err := dbConn.Exec(`UPDATE hangar_captures SET pod_uid='pod-uid' WHERE execution_id=$1 AND output_name=$2`,
			string(a.Capture.ExecutionID), string(a.Capture.Output))
		Expect(err).NotTo(HaveOccurred())

		_, err = relocate(a, "node", "node-uid", "other", "other-uid")
		Expect(err).To(MatchError(output.ErrConflict))
		Expect(stored(a).NodeName).To(Equal("node"))

		_, err = dbConn.Exec(`UPDATE hangar_captures SET node='other', node_uid='other-uid' WHERE execution_id=$1 AND output_name=$2`,
			string(a.Capture.ExecutionID), string(a.Capture.Output))
		Expect(err).To(MatchError(ContainSubstring("moves node only while pending and bound to no pod")))
	})
})
