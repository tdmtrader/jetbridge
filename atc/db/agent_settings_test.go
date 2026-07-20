package db_test

import (
	"errors"

	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("agent settings", func() {
	var settings db.AgentSettingsFactory

	BeforeEach(func() {
		settings = db.NewAgentSettingsFactory(dbConn)
	})

	It("reports absence without error when no row exists", func() {
		mode, found, err := settings.GetDispatcherMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(mode).To(BeEmpty())

		_, _, _, found, err = settings.GetDispatcherSetting()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("upserts the singleton row and reads it back hot", func() {
		Expect(settings.SetDispatcherMode(db.DispatcherModeActive, "tdm")).To(Succeed())

		mode, found, err := settings.GetDispatcherMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(mode).To(Equal(db.DispatcherModeActive))

		gotMode, updatedAt, updatedBy, found, err := settings.GetDispatcherSetting()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(gotMode).To(Equal(db.DispatcherModeActive))
		Expect(updatedBy).To(Equal("tdm"))
		Expect(updatedAt).ToNot(BeZero())

		// A second Set overwrites in place (no second row, id pinned to 1).
		Expect(settings.SetDispatcherMode(db.DispatcherModePaused, "ada")).To(Succeed())
		mode, _, err = settings.GetDispatcherMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(mode).To(Equal(db.DispatcherModePaused))

		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_settings`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})

	It("rejects an invalid mode before touching the DB", func() {
		err := settings.SetDispatcherMode("bogus", "tdm")
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, db.ErrInvalidDispatcherMode)).To(BeTrue())

		_, found, err := settings.GetDispatcherMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})
})
