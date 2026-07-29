package db_test

import (
	"context"
	"encoding/json"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunEventsFactory", func() {
	It("appends bounded recovery events to a stable execution head", func() {
		factory := db.NewAgentRunEventsFactory(dbConn)
		identity := checkpoint.Identity{BuildID: 7124, PlanID: "event-plan", FunctionID: "review"}
		event := checkpoint.RunEvent{
			Identity: identity, ExecutionAttempt: 1, Type: checkpoint.EventCheckpointCommitted,
			Reason: "safe_boundary", CheckpointGeneration: 3, Details: json.RawMessage(`{"bytes":512,"files":4}`),
		}
		Expect(factory.Record(context.Background(), event)).To(Succeed())

		events, err := factory.List(context.Background(), identity)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(1))
		Expect(events[0].Type).To(Equal(checkpoint.EventCheckpointCommitted))
		Expect(events[0].Details).To(MatchJSON(`{"bytes":512,"files":4}`))

		By("rejecting invalid and overlarge event details before they reach the append-only table")
		Expect(factory.Record(context.Background(), checkpoint.RunEvent{
			Identity: identity, ExecutionAttempt: 1, Type: checkpoint.EventInterrupted, Details: json.RawMessage(`[]`),
		})).ToNot(Succeed())
		Expect(factory.Record(context.Background(), checkpoint.RunEvent{
			Identity: identity, ExecutionAttempt: 1, Type: checkpoint.EventInterrupted,
			Details: json.RawMessage(`{"details":"` + string(make([]byte, checkpoint.MaxEventDetailsBytes+1)) + `"}`),
		})).ToNot(Succeed())
	})
})
