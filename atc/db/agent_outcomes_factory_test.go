package db_test

import (
	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentOutcomesFactory", func() {
	var factory db.AgentOutcomesFactory

	BeforeEach(func() {
		factory = db.NewAgentOutcomesFactory(dbConn)
		_, err := dbConn.Exec("DELETE FROM agent_outcomes")
		Expect(err).NotTo(HaveOccurred())
	})

	It("ensures, gets, and refreshes only open rows", func() {
		Expect(factory.Ensure(&outcomes.Outcome{
			TicketID: 501, Repo: "tdmtrader/concourse", Branch: "agent/ticket-501",
			PushedSha: "aaa", BaseSha: "bbb",
		})).To(Succeed())

		got, found, err := factory.Get(501)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got.MergeState).To(Equal(outcomes.MergeOpen))
		Expect(got.PushedSha).To(Equal("aaa"))
		Expect(got.CreatedAt).To(BeNumerically(">", 0))

		// re-push refreshes an open row
		Expect(factory.Ensure(&outcomes.Outcome{
			TicketID: 501, Repo: "tdmtrader/concourse", Branch: "agent/ticket-501",
			PushedSha: "ccc", BaseSha: "ddd",
		})).To(Succeed())
		got, _, _ = factory.Get(501)
		Expect(got.PushedSha).To(Equal("ccc"))
		Expect(got.BaseSha).To(Equal("ddd"))
	})

	It("records a merge, leaves ListOpen, and rejects a second merge", func() {
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 502, Repo: "r", Branch: "b", PushedSha: "p", BaseSha: "z"})).To(Succeed())

		open, err := factory.ListOpen()
		Expect(err).NotTo(HaveOccurred())
		Expect(open).To(HaveLen(1))

		Expect(factory.RecordMerge(502, outcomes.MergeResult{
			State: outcomes.MergedWithFixes, MergedSha: "m",
			HumanCommitCount: 2, HumanLinesAdded: 9, HumanLinesDeleted: 1,
		})).To(Succeed())

		got, _, _ := factory.Get(502)
		Expect(got.MergeState).To(Equal(outcomes.MergedWithFixes))
		Expect(got.HumanCommitCount).To(Equal(2))
		Expect(got.MergedAt).To(BeNumerically(">", 0))

		open, _ = factory.ListOpen()
		Expect(open).To(BeEmpty())

		Expect(factory.RecordMerge(502, outcomes.MergeResult{State: outcomes.Merged})).To(Equal(outcomes.ErrNotOpen))
		Expect(factory.RecordMerge(9999, outcomes.MergeResult{State: outcomes.Merged})).To(Equal(outcomes.ErrOutcomeNotFound))
	})

	It("exposes terminal rows to the optional generic-outcome reconciler", func() {
		terminalLister, ok := factory.(outcomes.TerminalLister)
		Expect(ok).To(BeTrue())
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 511, Repo: "r", Branch: "open"})).To(Succeed())
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 512, Repo: "r", Branch: "merged"})).To(Succeed())
		Expect(factory.RecordMerge(512, outcomes.MergeResult{State: outcomes.Merged, MergedSha: "abc"})).To(Succeed())

		terminal, err := terminalLister.ListTerminal()
		Expect(err).NotTo(HaveOccurred())
		Expect(terminal).To(ConsistOf(HaveField("TicketID", 512)))
	})

	It("closes an open row on disposition but keeps a terminal merge_state", func() {
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 503, Repo: "r", Branch: "b"})).To(Succeed())
		Expect(factory.SetDisposition(503, outcomes.DispositionInput{
			Disposition: "abandoned", Reason: "superseded", Notes: "dupe", By: "tdmtrader",
		})).To(Succeed())
		got, _, _ := factory.Get(503)
		Expect(got.MergeState).To(Equal(outcomes.ClosedUnmerged))
		Expect(got.Disposition).To(Equal("abandoned"))
		Expect(got.DisposedBy).To(Equal("tdmtrader"))

		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 504, Repo: "r", Branch: "b", BaseSha: "z"})).To(Succeed())
		Expect(factory.RecordMerge(504, outcomes.MergeResult{State: outcomes.Merged, MergedSha: "x"})).To(Succeed())
		Expect(factory.SetDisposition(504, outcomes.DispositionInput{Disposition: "sent_back", Reason: "other", By: "x"})).To(Succeed())
		got, _, _ = factory.Get(504)
		Expect(got.MergeState).To(Equal(outcomes.Merged)) // terminal state preserved
		Expect(got.Disposition).To(Equal("sent_back"))
	})

	It("re-arms a sent_back row on Ensure but leaves an abandoned row terminal (F6)", func() {
		// sent_back → Ensure reopens with fresh shas + cleared disposition
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 506, Repo: "r", Branch: "agent/ticket-506", PushedSha: "p1", BaseSha: "b1"})).To(Succeed())
		Expect(factory.SetDisposition(506, outcomes.DispositionInput{Disposition: "sent_back", Reason: "incomplete", By: "u"})).To(Succeed())
		got, _, _ := factory.Get(506)
		Expect(got.MergeState).To(Equal(outcomes.ClosedUnmerged))

		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 506, Repo: "r", Branch: "agent/ticket-506", PushedSha: "p2", BaseSha: "b2"})).To(Succeed())
		got, _, _ = factory.Get(506)
		Expect(got.MergeState).To(Equal(outcomes.MergeOpen))
		Expect(got.PushedSha).To(Equal("p2"))
		Expect(got.BaseSha).To(Equal("b2"))
		Expect(got.Disposition).To(BeEmpty())
		open, _ := factory.ListOpen()
		Expect(open).To(HaveLen(1)) // the re-armed row is back on the work-list

		// abandoned → Ensure must NOT re-arm; row stays closed and shas frozen
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 507, Repo: "r", Branch: "agent/ticket-507", PushedSha: "q1"})).To(Succeed())
		Expect(factory.SetDisposition(507, outcomes.DispositionInput{Disposition: "abandoned", Reason: "not_needed", By: "u"})).To(Succeed())
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 507, Repo: "r", Branch: "agent/ticket-507", PushedSha: "q2"})).To(Succeed())
		got, _, _ = factory.Get(507)
		Expect(got.MergeState).To(Equal(outcomes.ClosedUnmerged))
		Expect(got.PushedSha).To(Equal("q1"))
	})

	It("closes an open row as concluded and never re-arms it (flow-decoupling 2026-07-09)", func() {
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 508, Repo: "r", Branch: "agent/ticket-508", PushedSha: "s1", BaseSha: "t1"})).To(Succeed())
		Expect(factory.SetDisposition(508, outcomes.DispositionInput{
			Disposition: "concluded", Reason: "research_complete", Notes: "spike findings in ticket", By: "tdmtrader",
		})).To(Succeed())
		got, _, _ := factory.Get(508)
		Expect(got.MergeState).To(Equal(outcomes.MergeConcluded), "concluded closes as 'concluded', not closed_unmerged")
		Expect(got.Disposition).To(Equal("concluded"))
		Expect(got.DispositionReason).To(Equal("research_complete"))

		// concluded is terminal: the sent_back re-arm WHERE must never match it
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 508, Repo: "r", Branch: "agent/ticket-508", PushedSha: "s2", BaseSha: "t2"})).To(Succeed())
		got, _, _ = factory.Get(508)
		Expect(got.MergeState).To(Equal(outcomes.MergeConcluded))
		Expect(got.PushedSha).To(Equal("s1"), "shas stay frozen — the watcher never re-arms a concluded row")
		Expect(got.Disposition).To(Equal("concluded"))
		open, _ := factory.ListOpen()
		Expect(open).To(BeEmpty(), "concluded rows leave the watcher work-list permanently")
	})

	It("stamps last_checked_at on Touch", func() {
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 505, Repo: "r", Branch: "b"})).To(Succeed())
		Expect(factory.Touch(505)).To(Succeed())
		got, _, _ := factory.Get(505)
		Expect(got.LastCheckedAt).To(BeNumerically(">", 0))
	})

	It("Close sweeps only open rows and never touches disposition columns", func() {
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 509, Repo: "r", Branch: "agent/ticket-509", PushedSha: "c1", BaseSha: "c2"})).To(Succeed())
		Expect(factory.Close(509, outcomes.ClosedUnmerged)).To(Succeed())
		got, _, _ := factory.Get(509)
		Expect(got.MergeState).To(Equal(outcomes.ClosedUnmerged))
		Expect(got.Disposition).To(BeEmpty())
		Expect(got.DispositionReason).To(BeEmpty())
		Expect(got.DispositionNotes).To(BeEmpty())
		Expect(got.DisposedBy).To(BeEmpty())

		// a merged row is untouched by Close
		Expect(factory.Ensure(&outcomes.Outcome{TicketID: 510, Repo: "r", Branch: "b", PushedSha: "m1"})).To(Succeed())
		Expect(factory.RecordMerge(510, outcomes.MergeResult{State: outcomes.Merged, MergedSha: "m"})).To(Succeed())
		Expect(factory.Close(510, outcomes.MergeConcluded)).To(Succeed())
		got, _, _ = factory.Get(510)
		Expect(got.MergeState).To(Equal(outcomes.Merged))

		// invalid terminal state rejected
		Expect(factory.Close(509, outcomes.Merged)).To(HaveOccurred())
	})
})
