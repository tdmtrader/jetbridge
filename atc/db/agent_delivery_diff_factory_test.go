package db_test

import (
	"strings"

	"github.com/concourse/concourse/agent/deliverydiff"
	"github.com/concourse/concourse/agent/gitcheck"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentDeliveryDiffFactory", func() {
	var factory db.AgentDeliveryDiffFactory

	BeforeEach(func() {
		factory = db.NewAgentDeliveryDiffFactory(dbConn)
		_, err := dbConn.Exec("DELETE FROM agent_delivery_diffs")
		Expect(err).NotTo(HaveOccurred())
	})

	It("upserts attempts and returns the newest attempt for a ticket", func() {
		first := deliverydiff.DeliveryDiff{
			BuildID: 100, PlanID: "h1", TicketID: 42, PipelineRunID: 70,
			Repo: "tdmtrader/concourse", TargetBranch: "main", DeliveredBranch: "agent/ticket-42",
			BaseSHA: strings.Repeat("a", 40), PushedSHA: strings.Repeat("b", 40),
			Files:      []gitcheck.DiffFile{{Path: "a.txt", Patch: "first"}},
			TotalFiles: 1, CapturedFiles: 1, ByteLen: 5,
		}
		Expect(factory.Upsert(first)).To(Succeed())

		first.PushedSHA = strings.Repeat("c", 40)
		first.Files = []gitcheck.DiffFile{{Path: "a.txt", Patch: "replacement", Truncated: true}}
		first.ByteLen = len("replacement")
		first.Truncated = true
		Expect(factory.Upsert(first)).To(Succeed())

		got, found, err := factory.GetLatest(42)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(first))

		latest := deliverydiff.DeliveryDiff{
			BuildID: 101, PlanID: "h2", TicketID: 42, PipelineRunID: 71,
			Repo: "tdmtrader/concourse", TargetBranch: "main", DeliveredBranch: "agent/ticket-42",
			BaseSHA: strings.Repeat("a", 40), PushedSHA: strings.Repeat("d", 40),
			Files: []gitcheck.DiffFile{
				{Path: "a.txt", Patch: "new patch"},
				{Path: "nested/b.txt", Patch: "second patch"},
			},
			TotalFiles: 2, CapturedFiles: 2, ByteLen: len("new patch") + len("second patch"),
		}
		Expect(factory.Upsert(latest)).To(Succeed())

		got, found, err = factory.GetLatest(42)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(latest))
	})

	It("returns not found for an unknown ticket", func() {
		got, found, err := factory.GetLatest(999)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(got).To(BeZero())
	})
})
