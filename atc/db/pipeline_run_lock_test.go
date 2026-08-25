package db_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Job build admission locking", func() {
	// holdPipelineRow takes FOR NO KEY UPDATE on a pipelines row from its own
	// connection and returns a func that releases it.
	//
	// FOR NO KEY UPDATE is the discriminating probe, not FOR UPDATE: it
	// conflicts with lockJobBuildAdmission's `FOR UPDATE OF p` but NOT with the
	// FOR KEY SHARE lock that the builds->pipelines foreign key takes on the
	// parent row during the build INSERT. A plain FOR UPDATE probe would block
	// the INSERT either way and prove nothing.
	holdPipelineRow := func(pipelineID int) func() {
		GinkgoHelper()
		tx, err := openRunLifecycleConn().Begin()
		Expect(err).NotTo(HaveOccurred())
		var locked int
		Expect(tx.QueryRow(`SELECT id FROM pipelines WHERE id = $1 FOR NO KEY UPDATE`, pipelineID).Scan(&locked)).To(Succeed())
		return func() { _ = tx.Rollback() }
	}

	It("does not take the pipelines row lock for an ordinary job build", func() {
		// This fails if lockJobBuildAdmission keeps FOR UPDATE OF p for every job
		// build cluster-wide: any unrelated writer holding the pipelines row would
		// then serialize all build creation in that pipeline behind itself.
		release := holdPipelineRow(defaultPipeline.ID())
		DeferCleanup(release)

		done := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			_, err := defaultJob.CreateBuild("someone")
			done <- err
		}()

		var createErr error
		Eventually(done, 10*time.Second).Should(Receive(&createErr))
		Expect(createErr).NotTo(HaveOccurred())
	})

	It("still takes the payload pipeline row lock for a run build", func() {
		// This fails if the run case loses its pipelines row lock: the payload
		// could then be reclaimed out from under a build attaching to the run.
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "admission-template"}, atc.Config{
			Template: true,
			Jobs:     atc.JobConfigs{{Name: "deploy"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		creation, err := db.NewPipelineRunFactory(dbConn, lockFactory).CreateRun(context.Background(), template, db.RunParams{}, "creator")
		Expect(err).NotTo(HaveOccurred())

		payload, found, err := defaultTeam.Pipeline(atc.PipelineRef{
			Name:         "admission-template",
			InstanceVars: atc.InstanceVars{"run": float64(creation.Run.Number())},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		runJob, found, err := payload.Job("deploy")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		release := holdPipelineRow(payload.ID())

		done := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			_, err := runJob.CreateBuild("creator")
			done <- err
		}()

		Consistently(done, 150*time.Millisecond).ShouldNot(Receive())
		release()

		var buildErr error
		Eventually(done, 10*time.Second).Should(Receive(&buildErr))
		Expect(buildErr).NotTo(HaveOccurred())
	})
})
