package gc_test

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CheckCollector", func() {
	var (
		collector GcCollector
		scope     db.ResourceConfigScope
		plan      atc.Plan
	)

	exists := func(b db.Build) bool {
		found, err := b.Reload()
		Expect(err).NotTo(HaveOccurred())
		return found
	}

	// A completed check build only becomes collectable once a *newer* completed
	// check exists for the same scope -- the most recent one is kept so the last
	// check result stays readable (check_lifecycle.go:27-40).
	finishedCheck := func() db.Build {
		build, created, err := usedResource.CreateBuild(context.Background(), true, plan)
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		_, err = scope.UpdateLastCheckStartTime(build.ID(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(build.Finish(db.BuildStatusSucceeded)).To(Succeed())
		_, err = scope.UpdateLastCheckEndTime(true)
		Expect(err).NotTo(HaveOccurred())

		return build
	}

	BeforeEach(func() {
		collector = gc.NewChecksCollector(db.NewCheckLifecycle(dbConn))
		plan = atc.Plan{ID: "some-plan", Check: &atc.CheckPlan{Name: "wreck"}}

		resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
			usedResource.Type(), usedResource.Source(), nil,
		)
		Expect(err).NotTo(HaveOccurred())
		scope, err = resourceConfig.FindOrCreateScope(intptr(usedResource.ID()))
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("Run", func() {
		It("keeps the only completed check for a scope", func() {
			build := finishedCheck()

			Expect(collector.Run(context.Background())).To(Succeed())

			Expect(exists(build)).To(BeTrue(), "the most recent check must survive")
		})

		It("removes a completed check once a newer one has completed", func() {
			superseded := finishedCheck()
			latest := finishedCheck()

			Expect(collector.Run(context.Background())).To(Succeed())

			Expect(exists(superseded)).To(BeFalse(), "superseded check should have been collected")
			Expect(exists(latest)).To(BeTrue(), "the most recent check must survive")
		})
	})
})
