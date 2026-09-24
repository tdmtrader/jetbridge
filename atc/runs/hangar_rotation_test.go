package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The Run contract's activation epoch is its own (amendment M-2 decision 3).
// A Run carries the Run epoch it was born under; its captures carry the Hangar
// epoch they were admitted under. Rotating the Hangar epoch -- disabling the
// one a Run was admitted under and enabling the next -- must not strand that
// Run: it still finalizes, and its invocation key still replays it.
var _ = Describe("a Hangar output epoch rotation", func() {
	const (
		runEpoch    int64 = 5
		rotatedFrom       = 1
		rotatedTo         = 2
	)

	var (
		ctx        context.Context
		resultsRef runs.TemplateRef
		portAt     func(hangarEpoch int64) runs.Admitter
	)

	BeforeEach(func() {
		ctx = context.Background()
		atc.PipelineRunActivationEpoch = runEpoch
		_, err := dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=$1, admission_enabled=true WHERE singleton`, runEpoch)
		Expect(err).NotTo(HaveOccurred())

		// A template that declares a result, so admission needs the output
		// plane and the Run is one whose finalization reads Hangar claims.
		task := &atc.TaskStep{
			Name: "produce", TaskID: uuid.NewString(), RunResult: &atc.RunResult{Name: "result", Output: "result"},
			Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Outputs: []atc.TaskOutputConfig{{Name: "result"}}},
		}
		savePipeline(defaultTeam, "results", atc.Config{
			Template: true, Jobs: atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{{Config: task}}}},
		})
		resultsRef = runs.TemplateRef{Team: defaultTeam.Name(), Pipeline: atc.PipelineRef{Name: "results"}}

		displayUserIds, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
		Expect(err).NotTo(HaveOccurred())
		// A web node configured for one Hangar epoch.
		portAt = func(hangarEpoch int64) runs.Admitter {
			port := runs.NewAdmitter(dbConn, runFactory, teamFactory, displayUserIds, nil)
			port.SetOutputEpoch(hangarEpoch)
			return port
		}
	})

	admit := func(port runs.Admitter, key string) (runs.Run, bool, error) {
		GinkgoHelper()
		tx, err := port.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		run, replayed, err := port.AdmitVersionedRun(ctx, tx, runs.Admission{
			Template: resultsRef, Principal: memberPrincipal, ContractKey: key,
		}, runEpoch)
		if err != nil {
			return run, replayed, err
		}
		Expect(tx.Commit()).To(Succeed())
		return run, replayed, nil
	}

	rotate := func() {
		GinkgoHelper()
		_, err := dbConn.Exec(`UPDATE hangar_output_activation_epochs
			SET output_state='disabled', base_state='disabled', revision=revision+1, updated_at=now()
			WHERE epoch_id=$1`, rotatedFrom)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO hangar_output_activation_epochs
				(epoch_id, base_state, output_state, base_attestation, output_attestation,
				 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
				 materialization_key_id, bucket_fingerprint, derived_namespace)
			VALUES ($1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-2',
				now() - interval '1 day', now() + interval '30 days',
				'materialize-key-2', 'gs://output-bucket', 'deployment/ns')`, rotatedTo)
		Expect(err).NotTo(HaveOccurred())
	}

	status := func(id int) (string, bool) {
		GinkgoHelper()
		var s string
		var completed bool
		Expect(dbConn.QueryRow(`SELECT status, completed_at IS NOT NULL FROM pipeline_runs WHERE id=$1`, id).Scan(&s, &completed)).To(Succeed())
		return s, completed
	}

	It("finalizes a running Run and replays its invocation key after the Hangar epoch it was admitted under is disabled", func() {
		first, replayed, err := admit(portAt(rotatedFrom), "rotation-key")
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(BeFalse())
		var born int64
		Expect(dbConn.QueryRow(`SELECT activation_epoch FROM pipeline_runs WHERE id=$1`, first.ID).Scan(&born)).To(Succeed())
		Expect(born).To(Equal(runEpoch), "a Run is born under the Run contract's epoch, not the Hangar epoch")

		rotate()

		// Admission of a result template still needs an enabled output
		// epoch: the node still configured for the disabled one refuses it,
		// and one on the new epoch admits it.
		_, _, err = admit(portAt(rotatedFrom), "fresh-on-old-epoch")
		Expect(err).To(MatchError(atc.ErrRunResultsUnavailable))
		fresh, replayed, err := admit(portAt(rotatedTo), "fresh-on-new-epoch")
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(BeFalse())
		Expect(fresh.ID).NotTo(Equal(first.ID))

		// The key replays the running Run from a node on either epoch.
		for _, epoch := range []int64{rotatedFrom, rotatedTo} {
			again, replayed, err := admit(portAt(epoch), "rotation-key")
			Expect(err).NotTo(HaveOccurred(), "replay from a node on Hangar epoch %d", epoch)
			Expect(replayed).To(BeTrue())
			Expect(again.ID).To(Equal(first.ID))
		}

		// Its work ends; the finalizer publishes the terminal header though
		// the Hangar epoch it was admitted under is disabled.
		_, err = dbConn.Exec(`UPDATE builds SET status='failed', completed=true, end_time=now() WHERE pipeline_run_id=$1 AND NOT completed`, first.ID)
		Expect(err).NotTo(HaveOccurred())
		// The scheduler has seen its payload job: no scheduling is owed.
		_, err = dbConn.Exec(`UPDATE jobs SET last_scheduled=now() WHERE pipeline_id=$1`, first.PayloadPipelineID)
		Expect(err).NotTo(HaveOccurred())
		Expect((&runs.ResultFinalizer{Conn: dbConn, Factory: runFactory}).Run(ctx)).To(Succeed())
		s, completed := status(first.ID)
		Expect(s).To(Equal(string(atc.RunStatusFailed)))
		Expect(completed).To(BeTrue())

		// And the key still replays the finished Run.
		again, replayed, err := admit(portAt(rotatedTo), "rotation-key")
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(BeTrue())
		Expect(again.ID).To(Equal(first.ID))
	})
})
