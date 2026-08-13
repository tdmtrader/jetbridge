package event_test

import (
	"encoding/json"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type fakeEvent struct {
	Hello      string `json:"hello"`
	AddedField string `json:"added"`
}

func (fakeEvent) EventType() atc.EventType  { return "fake" }
func (fakeEvent) Version() atc.EventVersion { return "5.1" }

var _ = Describe("ParseEvent", func() {
	BeforeEach(func() {
		event.RegisterEvent(fakeEvent{})
	})

	It("can parse an older event type with a compatible major version", func() {
		e, err := event.ParseEvent("5.0", "fake", []byte(`{"hello":"sup"}`))
		Expect(err).ToNot(HaveOccurred())
		Expect(e).To(Equal(fakeEvent{Hello: "sup"}))
	})

	It("can parse a newer event type with a compatible major version", func() {
		e, err := event.ParseEvent("5.3", "fake", []byte(`{"hello":"sup","future":"field"}`))
		Expect(err).ToNot(HaveOccurred())
		Expect(e).To(Equal(fakeEvent{Hello: "sup"}))
	})

	It("fails to parse if the type is unknown", func() {
		_, err := event.ParseEvent("4.0", "fake-unknown", []byte(`{"hello":"sup"}`))
		Expect(err).To(Equal(event.UnknownEventTypeError{
			Type: "fake-unknown",
		}))
	})

	It("fails to parse if the version is incompatible", func() {
		_, err := event.ParseEvent("4.0", "fake", []byte(`{"hello":"sup"}`))
		Expect(err).To(Equal(event.UnknownEventVersionError{
			Type:          "fake",
			Version:       "4.0",
			KnownVersions: []string{"5.1"},
		}))
	})

	DescribeTable("should register all non-deprecated events successfully",
		func(eventType atc.Event) {
			parsedEvent, err := event.ParseEvent(eventType.Version(), eventType.EventType(), []byte("{}"))
			Expect(err).ToNot(HaveOccurred())
			Expect(parsedEvent).To(BeAssignableToTypeOf(eventType))
		},

		Entry("InitializeCheck", event.InitializeCheck{}),
		Entry("InitializeTask", event.InitializeTask{}),
		Entry("StartTask", event.StartTask{}),
		Entry("FinishTask", event.FinishTask{}),
		Entry("InitializeGet", event.InitializeGet{}),
		Entry("StartGet", event.StartGet{}),
		Entry("FinishGet", event.FinishGet{}),
		Entry("InitializePut", event.InitializePut{}),
		Entry("StartPut", event.StartPut{}),
		Entry("FinishPut", event.FinishPut{}),
		Entry("SetPipelineChanged", event.SetPipelineChanged{}),
		Entry("Status", event.Status{}),
		Entry("WaitingForWorker", event.WaitingForWorker{}),
		Entry("SelectedWorker", event.SelectedWorker{}),
		Entry("Log", event.Log{}),
		Entry("Error", event.Error{}),
		Entry("ImageCheck", event.ImageCheck{}),
		Entry("ImageGet", event.ImageGet{}),
		Entry("AcrossSubsteps", event.AcrossSubsteps{}),
	)
})

var _ = Describe("Message", func() {
	It("can round-trip an event", func() {
		payload, err := json.Marshal(event.Message{Event: event.Log{Payload: "sup"}})
		Expect(err).ToNot(HaveOccurred())

		var message event.Message
		Expect(json.Unmarshal(payload, &message)).To(Succeed())
		Expect(message.Event).To(Equal(event.Log{Payload: "sup"}))
	})

	It("fails to unmarshal an envelope with no data, rather than panicking", func() {
		var message event.Message
		err := json.Unmarshal([]byte(`{"event":"log","version":"5.1"}`), &message)
		Expect(err).To(MatchError("missing event data: log version 5.1"))
	})

	It("fails to unmarshal an envelope with null data, rather than panicking", func() {
		var message event.Message
		err := json.Unmarshal([]byte(`{"data":null,"event":"some-event","version":"1.0"}`), &message)
		Expect(err).To(MatchError("missing event data: some-event version 1.0"))
	})
})
