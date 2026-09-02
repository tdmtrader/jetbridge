package db_test

import (
	"time"

	"github.com/concourse/concourse/atc"
	. "github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Worker", func() {
	var (
		atcWorker atc.Worker
	)

	BeforeEach(func() {
		atcWorker = atc.Worker{
			Ephemeral:        true,
			ActiveContainers: 140,
			ResourceTypes: []atc.WorkerResourceType{
				{
					Type:    "some-resource-type",
					Image:   "some-image",
					Version: "some-version",
				},
				{
					Type:    "other-resource-type",
					Image:   "other-image",
					Version: "other-version",
				},
			},
			Platform:  "some-platform",
			Tags:      atc.Tags{"some", "tags"},
			Name:      "some-name",
			StartTime: 55912945,
		}
	})

	Describe("FindContainer/CreateContainer", func() {
		var (
			containerOwner         ContainerOwner
			foundCreatingContainer CreatingContainer
			foundCreatedContainer  CreatedContainer
			worker                 Worker
		)

		expiries := ContainerOwnerExpiries{
			Min: 5 * time.Minute,
			Max: 1 * time.Hour,
		}

		BeforeEach(func() {
			var err error
			worker, err = workerFactory.SaveWorker(atcWorker, 5*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			resourceConfig, err := resourceConfigFactory.FindOrCreateResourceConfig(
				"some-resource-type",
				atc.Source{"some": "source"},
				nil,
			)
			Expect(err).ToNot(HaveOccurred())

			containerOwner = NewResourceConfigCheckSessionContainerOwner(
				resourceConfig.ID(),
				resourceConfig.OriginBaseResourceType().ID,
				expiries,
			)
		})

		JustBeforeEach(func() {
			var err error
			foundCreatingContainer, foundCreatedContainer, err = worker.FindContainer(containerOwner)
			Expect(err).ToNot(HaveOccurred())
		})

		Context("when there is no container", func() {
			It("returns nil", func() {
				Expect(foundCreatedContainer).To(BeNil())
				Expect(foundCreatingContainer).To(BeNil())
			})
		})
	})
})
