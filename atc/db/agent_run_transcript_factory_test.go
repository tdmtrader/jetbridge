package db_test

import (
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunTranscriptFactory", func() {
	var factory db.AgentRunTranscriptFactory

	BeforeEach(func() {
		factory = db.NewAgentRunTranscriptFactory(dbConn)
	})

	It("upserts on (build_id, plan_id) and reads back by ticket+build", func() {
		ticket := 43
		nd := `{"type":"system","subtype":"init"}` + "\n" + `{"type":"result","total_cost_usd":0.4}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: 4242, PlanID: "aa11", TicketID: &ticket, StepName: "implement",
			NDJSON: nd, ByteLen: len(nd), Truncated: false,
		})).To(Succeed())

		got, found, err := factory.Get(ticket, 4242)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(nd))

		// second write of the same key overwrites, does not duplicate
		nd2 := nd + `{"type":"extra"}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: 4242, PlanID: "aa11", TicketID: &ticket, StepName: "implement",
			NDJSON: nd2, ByteLen: len(nd2), Truncated: true,
		})).To(Succeed())

		got, found, err = factory.Get(ticket, 4242)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(nd2))
	})

	It("prefers the implement/agent step over the harvest step for the same build", func() {
		ticket := 44
		implND := `{"type":"result","step":"implement"}` + "\n"
		harvestND := `{"type":"result","step":"harvest"}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: 5252, PlanID: "harvest-plan", TicketID: &ticket, StepName: "harvest",
			NDJSON: harvestND, ByteLen: len(harvestND),
		})).To(Succeed())
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: 5252, PlanID: "implement-plan", TicketID: &ticket, StepName: "implement",
			NDJSON: implND, ByteLen: len(implND),
		})).To(Succeed())

		got, found, err := factory.Get(ticket, 5252)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(implND))
	})

	It("returns found=false when no transcript exists for the build", func() {
		_, found, err := factory.Get(99, 999999)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("finds a verifier-rejected (NULL ticket_id) row by build", func() {
		nd := `{"type":"result"}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: 6363, PlanID: "p1", TicketID: nil, StepName: "implement",
			NDJSON: nd, ByteLen: len(nd),
		})).To(Succeed())

		got, found, err := factory.Get(77, 6363)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(nd))
	})
})
