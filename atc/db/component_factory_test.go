package db_test

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ComponentFactory", func() {
	Describe("Find", func() {
		var (
			err          error
			found        bool
			component    db.Component
			expectedName = "scheduler"
		)

		BeforeEach(func() {
			_, err = dbConn.Exec("INSERT INTO components (name) VALUES ('scheduler') ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name")
			Expect(err).NotTo(HaveOccurred())
		})

		JustBeforeEach(func() {
			component, found, err = componentFactory.Find(expectedName)
			Expect(found).To(BeTrue())
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns the db component", func() {
			Expect(component.Name()).To(Equal(expectedName))
			Expect(component.Paused()).To(Equal(false))
		})
	})

	Describe("CreateOrUpdate", func() {
		It("creates a component", func() {
			componentName := "some-component"

			createdComponent, err := componentFactory.CreateOrUpdate(atc.Component{
				Name: componentName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(createdComponent.ID()).ToNot(BeZero())
			Expect(createdComponent.Name()).To(Equal(componentName))

			foundComponent, found, err := componentFactory.Find(componentName)
			Expect(found).To(BeTrue())
			Expect(err).NotTo(HaveOccurred())
			Expect(foundComponent.ID()).To(Equal(createdComponent.ID()))
			Expect(foundComponent.Name()).To(Equal(componentName))
		})
	})
})
