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

	It("defaults actions to active and reports the switch as unset", func() {
		mode, found, err := settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(mode).To(BeEmpty())
	})

	It("engages and releases the switch, recording its own provenance", func() {
		Expect(settings.SetActionsMode(db.ActionsModeSuppressed, "tdm")).To(Succeed())

		mode, found, err := settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(mode).To(Equal(db.ActionsModeSuppressed))

		gotMode, updatedAt, updatedBy, found, err := settings.GetActionsSetting()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(gotMode).To(Equal(db.ActionsModeSuppressed))
		Expect(updatedBy).To(Equal("tdm"))
		Expect(updatedAt).ToNot(BeZero())

		Expect(settings.SetActionsMode(db.ActionsModeActive, "ada")).To(Succeed())
		mode, _, err = settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(mode).To(Equal(db.ActionsModeActive))

		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_settings`).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))
	})

	It("rejects an invalid actions mode before touching the DB", func() {
		err := settings.SetActionsMode("halt", "tdm")
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, db.ErrInvalidActionsMode)).To(BeTrue())

		_, found, err := settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	// The two settings share one row but must not overwrite each other's
	// meaning. Engaging the switch must NOT invent a dispatcher mode: any
	// value would make the dispatcher's "no row -> boot flag" fallback stop
	// applying and silently change dispatch behavior on a live cluster.
	It("keeps the dispatcher setting unset when only the switch is engaged", func() {
		Expect(settings.SetActionsMode(db.ActionsModeSuppressed, "tdm")).To(Succeed())

		_, found, err := settings.GetDispatcherMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	// The two reads answer DIFFERENT questions and split here on purpose.
	// GetActionsSetting answers "did an admin decide something, and who?" — so
	// a row the dispatcher created reports the switch as unset. GetActionsMode
	// answers "is the brake engaged?" — and the column is NOT NULL DEFAULT
	// 'active', so the row's own value is always a complete answer. Keying the
	// hot read on provenance instead would fail OPEN for any mode written
	// without provenance (see the break-glass spec below).
	It("keeps the switch unset but the mode readable when only the dispatcher mode is set", func() {
		Expect(settings.SetDispatcherMode(db.DispatcherModeActive, "tdm")).To(Succeed())

		mode, found, err := settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(mode).To(Equal(db.ActionsModeActive))

		_, _, _, found, err = settings.GetActionsSetting()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())

		dispatcherMode, found, err := settings.GetDispatcherMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(dispatcherMode).To(Equal(db.DispatcherModeActive))
	})

	// BREAK GLASS: engaging the brake by direct SQL is the operator's recourse
	// when the API is unreachable — precisely the incident the switch exists
	// for. Such an UPDATE leaves actions_updated_at NULL, so the publisher's hot
	// read must key on the mode column and NOT on provenance, or the emergency
	// brake silently fails open.
	It("engages for a direct SQL update that carries no provenance", func() {
		Expect(settings.SetDispatcherMode(db.DispatcherModeActive, "tdm")).To(Succeed())
		_, err := dbConn.Exec(`UPDATE agent_settings SET actions_mode = 'suppressed' WHERE id = 1`)
		Expect(err).ToNot(HaveOccurred())

		var updatedAt any
		Expect(dbConn.QueryRow(`SELECT actions_updated_at FROM agent_settings WHERE id = 1`).Scan(&updatedAt)).To(Succeed())
		Expect(updatedAt).To(BeNil(), "the break-glass UPDATE must leave provenance NULL for this spec to mean anything")

		mode, found, err := settings.GetActionsMode()
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(mode).To(Equal(db.ActionsModeSuppressed))
	})
})
