package db_test

import (
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Component", func() {
	var (
		err       error
		found     bool
		component db.Component
	)

	BeforeEach(func() {
		_, err = dbConn.Exec("INSERT INTO components (name) VALUES ('scheduler') ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name")
		Expect(err).NotTo(HaveOccurred())

		component, found, err = componentFactory.Find("scheduler")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
	})

	Describe("Reload", func() {
		It("reloads the component from the database", func() {
			reloaded, err := component.Reload()
			Expect(reloaded).To(BeTrue())
			Expect(err).NotTo(HaveOccurred())
			Expect(component.Name()).To(Equal("scheduler"))
		})
	})
})
