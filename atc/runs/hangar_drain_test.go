package runs_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/skymarshal/skycmd"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The Run contract's activation epoch is its own (amendment M-2 decision 3).
// Taking the Hangar output plane out of service -- the first step of a drain,
// hangar_enabled off -- refuses new admission of work that needs it, and must
// not strand a Run admitted before: it still finalizes, and its invocation key
// still replays it.
var _ = Describe("a Hangar output plane drain", func() {
	const (
		runEpoch    int64 = 5
		hangarEpoch       = 1
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
		// A web node with an output plane configured.
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

	inService := func(enabled bool) {
		GinkgoHelper()
		_, err := db.SetHangarEnabled(ctx, dbConn, enabled)
		Expect(err).NotTo(HaveOccurred())
	}

	status := func(id int) (string, bool) {
		GinkgoHelper()
		var s string
		var completed bool
		Expect(dbConn.QueryRow(`SELECT status, completed_at IS NOT NULL FROM pipeline_runs WHERE id=$1`, id).Scan(&s, &completed)).To(Succeed())
		return s, completed
	}

	It("refuses new admission, and still finalizes a running Run and replays its invocation key, once the plane is out of service", func() {
		inService(true)
		first, replayed, err := admit(portAt(hangarEpoch), "drain-key")
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(BeFalse())
		var born int64
		Expect(dbConn.QueryRow(`SELECT activation_epoch FROM pipeline_runs WHERE id=$1`, first.ID).Scan(&born)).To(Succeed())
		Expect(born).To(Equal(runEpoch), "a Run is born under the Run contract's epoch, not the Hangar epoch")

		inService(false)

		// Admission of a result template needs the output plane in service:
		// out of service it is refused.
		_, _, err = admit(portAt(hangarEpoch), "fresh-while-draining")
		Expect(err).To(MatchError(atc.ErrRunResultsUnavailable))

		// The key still replays the running Run.
		again, replayed, err := admit(portAt(hangarEpoch), "drain-key")
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(BeTrue())
		Expect(again.ID).To(Equal(first.ID))

		// Its work ends; the finalizer publishes the terminal header though
		// the plane is out of service.
		_, err = dbConn.Exec(`UPDATE builds SET status='failed', completed=true, end_time=now() WHERE pipeline_run_id=$1 AND NOT completed`, first.ID)
		Expect(err).NotTo(HaveOccurred())
		// The scheduler has seen its payload job: no scheduling is owed.
		_, err = dbConn.Exec(`UPDATE jobs SET last_scheduled=now() WHERE pipeline_id=$1`, first.PayloadID)
		Expect(err).NotTo(HaveOccurred())
		Expect((&runs.ResultFinalizer{Conn: dbConn, Factory: runFactory}).Run(ctx)).To(Succeed())
		s, completed := status(first.ID)
		Expect(s).To(Equal(string(atc.RunStatusFailed)))
		Expect(completed).To(BeTrue())

		// And the key still replays the finished Run.
		again, replayed, err = admit(portAt(hangarEpoch), "drain-key")
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(BeTrue())
		Expect(again.ID).To(Equal(first.ID))

		// Back in service, a fresh Run is admitted.
		inService(true)
		fresh, replayed, err := admit(portAt(hangarEpoch), "fresh-in-service")
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(BeFalse())
		Expect(fresh.ID).NotTo(Equal(first.ID))
	})
})
