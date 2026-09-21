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

// Credential delivery checks the result producer's image against an operator
// pin. The image it checks has to be the one the Run snapshotted, not what the
// template says now: a member who re-sets the template must not change what
// an existing Run's credentials reach, and a Run created after the re-set must
// show the new image so the pin can refuse it.
var _ = Describe("Run credential target worker image", func() {
	const producerID = "e5c35b74-c6ad-4ad5-b4bd-8a703759301d"
	pinned := "docker:///registry.example/review-worker@sha256:" + strings.Repeat("ab", 32)
	attacker := "docker:///attacker.example/worker@sha256:" + strings.Repeat("cd", 32)
	owner := runinput.PrincipalDigest("local:owner")

	config := func(rootfs string, override string) atc.Config {
		task := &atc.TaskStep{
			Name: "review", TaskID: producerID,
			RunResult: &atc.RunResult{Name: "findings", Output: "result"},
			Config: &atc.TaskConfig{Platform: "linux", RootfsURI: rootfs,
				Run: atc.TaskRunConfig{Path: "true"}, Outputs: []atc.TaskOutputConfig{{Name: "result"}}},
			ImageArtifactName: override,
		}
		return atc.Config{Template: true, Jobs: atc.JobConfigs{{Name: "review", PlanSequence: []atc.Step{{Config: task}}}}}
	}

	It("reports the image each Run snapshotted", func() {
		ctx := context.Background()
		consumer, err := db.HangarConsumerPrefixHeld("credential-image-test")
		Expect(err).NotTo(HaveOccurred())
		hangarActivateEpoch(ctx, db.NewHangarOutputRepository(consumer))
		_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
		Expect(err).NotTo(HaveOccurred())

		factory := db.NewPipelineRunFactory(dbConn, lockFactory)
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "review"}, config(pinned, ""), 0, false)
		Expect(err).NotTo(HaveOccurred())
		create := func(key string) db.RunCreation {
			GinkgoHelper()
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			creation, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "owner", db.RunCreationOpts{
				ActivationEpoch: 1,
				Invocation:      &db.RunInvocationIdentity{PrincipalDigest: owner, KeyDigest: strings.Repeat(key, 64)},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())
			return creation
		}
		target := func(number int) db.RunCredentialTarget {
			GinkgoHelper()
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			target, err := db.LoadRunCredentialTarget(ctx, tx, template.ID(), number, owner, "findings", 1, false)
			Expect(err).NotTo(HaveOccurred())
			return target
		}

		first := create("a")
		Expect(target(first.Run.Number()).WorkerImage).To(Equal(pinned))

		// A member re-sets the template's worker image.
		template, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "review"}, config(attacker, ""), template.ConfigVersion(), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(target(first.Run.Number()).WorkerImage).To(Equal(pinned), "an admitted Run keeps its snapshotted image")
		second := create("b")
		Expect(target(second.Run.Number()).WorkerImage).To(Equal(attacker), "a later Run exposes the new image to the pin")

		// An image artifact replaces rootfs_uri at run time, so nothing is pinnable.
		template, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "review"}, config(pinned, "some-artifact"), template.ConfigVersion(), false)
		Expect(err).NotTo(HaveOccurred())
		third := create("c")
		Expect(target(third.Run.Number()).WorkerImage).To(BeEmpty())
	})
})
