package db_test

import (
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Component Notifications", func() {
	// listenFor subscribes to the given channel and returns a function that
	// waits for a notification. The returned function unlistens when done and
	// returns true if a notification arrived within the timeout.
	listenFor := func(channel string) func() bool {
		ch, err := dbConn.Bus().Listen(channel, 10)
		Expect(err).NotTo(HaveOccurred())

		return func() bool {
			defer dbConn.Bus().Unlisten(channel, ch)
			select {
			case <-ch:
				return true
			case <-time.After(2 * time.Second):
				return false
			}
		}
	}

	// createResourceScope creates a resource config scope for a resource,
	// using the same pattern as the existing tests.
	createResourceScope := func(resource db.Resource) db.ResourceConfigScope {
		rc, err := resourceConfigFactory.FindOrCreateResourceConfig(
			resource.Type(),
			resource.Source(),
			nil,
		)
		Expect(err).NotTo(HaveOccurred())

		scope, err := rc.FindOrCreateScope(intptr(resource.ID()))
		Expect(err).NotTo(HaveOccurred())
		return scope
	}

	createResourceTypeScope := func(rt db.ResourceType) db.ResourceConfigScope {
		rc, err := resourceConfigFactory.FindOrCreateResourceConfig(
			rt.Type(),
			rt.Source(),
			nil,
		)
		Expect(err).NotTo(HaveOccurred())

		scope, err := rc.FindOrCreateScope(nil)
		Expect(err).NotTo(HaveOccurred())
		return scope
	}

	Describe("LidarScanner notifications", func() {
		var (
			scenario *dbtest.Scenario
			resource db.Resource
			scope    db.ResourceConfigScope
		)

		BeforeEach(func() {
			scenario = dbtest.Setup(
				builder.WithPipeline(atc.Config{
					Resources: atc.ResourceConfigs{
						{
							Name:   "some-resource",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "source"},
						},
					},
					Jobs: atc.JobConfigs{
						{
							Name: "some-job",
							PlanSequence: []atc.Step{
								{
									Config: &atc.GetStep{
										Name: "some-resource",
									},
								},
							},
						},
					},
				}),
			)

			resource = scenario.Resource("some-resource")
			scope = createResourceScope(resource)
		})

		Describe("Resource.SetResourceConfigScope", func() {
			It("notifies the scanner", func() {
				received := listenFor(atc.ComponentLidarScanner)

				err := resource.SetResourceConfigScope(scope)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after SetResourceConfigScope")
			})
		})

		Describe("Resource.PinVersion", func() {
			It("notifies the scanner", func() {
				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				rcv, found, err := scope.FindVersion(atc.Version{"ver": "1"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				received := listenFor(atc.ComponentLidarScanner)

				_, err = resource.PinVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after PinVersion")
			})
		})

		Describe("Resource.UnpinVersion", func() {
			It("notifies the scanner", func() {
				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				rcv, found, err := scope.FindVersion(atc.Version{"ver": "1"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				_, err = resource.PinVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				received := listenFor(atc.ComponentLidarScanner)

				err = resource.UnpinVersion()
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after UnpinVersion")
			})
		})

		Describe("Resource.DisableVersion", func() {
			It("notifies the scanner", func() {
				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				rcv, found, err := scope.FindVersion(atc.Version{"ver": "1"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				received := listenFor(atc.ComponentLidarScanner)

				err = resource.DisableVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after DisableVersion")
			})
		})

		Describe("Resource.EnableVersion", func() {
			It("notifies the scanner", func() {
				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				rcv, found, err := scope.FindVersion(atc.Version{"ver": "1"})
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				err = resource.DisableVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				received := listenFor(atc.ComponentLidarScanner)

				err = resource.EnableVersion(rcv.ID())
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after EnableVersion")
			})
		})

		Describe("ResourceConfigScope.SaveVersions", func() {
			It("notifies the scanner", func() {
				received := listenFor(atc.ComponentLidarScanner)

				err := scope.SaveVersions(db.SpanContext{}, []atc.Version{{"ver": "1"}})
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after SaveVersions")
			})
		})

		Describe("ResourceConfigScope.UpdateLastCheckEndTime", func() {
			It("notifies the scanner", func() {
				received := listenFor(atc.ComponentLidarScanner)

				_, err := scope.UpdateLastCheckEndTime(true)
				Expect(err).NotTo(HaveOccurred())

				Expect(received()).To(BeTrue(), "expected scanner notification after UpdateLastCheckEndTime")
			})
		})
	})

	Describe("ResourceType scanner notifications", func() {
		It("notifies the scanner on SetResourceConfigScope", func() {
			scenario := dbtest.Setup(
				builder.WithPipeline(atc.Config{
					ResourceTypes: atc.ResourceTypes{
						{
							Name:   "some-type",
							Type:   "some-base-resource-type",
							Source: atc.Source{"some": "type-source"},
						},
					},
				}),
			)

			rt := scenario.ResourceType("some-type")
			scope := createResourceTypeScope(rt)

			received := listenFor(atc.ComponentLidarScanner)

			err := rt.SetResourceConfigScope(scope)
			Expect(err).NotTo(HaveOccurred())

			Expect(received()).To(BeTrue(), "expected scanner notification after ResourceType.SetResourceConfigScope")
		})
	})
})

