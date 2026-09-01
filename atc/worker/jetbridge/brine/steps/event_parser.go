package steps

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
)

type brineFakeEvent struct {
	Hello string `json:"hello"`
}

func (brineFakeEvent) EventType() atc.EventType  { return "brine-fake" }
func (brineFakeEvent) Version() atc.EventVersion { return "5.1" }

type EventParserObservation struct {
	Value string
}

func EventParserDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, EventParserObservation](
			"the production event parser handles profile {string}",
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder) (EventParserObservation, error) {
				profile, _ := p.GetString(0)
				value, err := observeEventParser(profile)
				return EventParserObservation{Value: value}, err
			},
		),
		CheckString[EventParserObservation]("the event parser result is {string}", "event parser result", func(in EventParserObservation) (string, error) {
			return in.Value, nil
		}),
	}
}

func observeEventParser(profile string) (string, error) {
	event.RegisterEvent(brineFakeEvent{})
	switch profile {
	case "compatible-older":
		parsed, err := event.ParseEvent("5.0", "brine-fake", []byte(`{"hello":"sup"}`))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%T:%s", parsed, parsed.(brineFakeEvent).Hello), nil
	case "compatible-newer":
		parsed, err := event.ParseEvent("5.3", "brine-fake", []byte(`{"hello":"sup","future":"field"}`))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%T:%s", parsed, parsed.(brineFakeEvent).Hello), nil
	case "unknown-type":
		_, err := event.ParseEvent("4.0", "brine-unknown", []byte(`{}`))
		return fmt.Sprintf("%T", err), nil
	case "incompatible-version":
		_, err := event.ParseEvent("4.0", "brine-fake", []byte(`{}`))
		return fmt.Sprintf("%T", err), nil
	case "message-round-trip":
		payload, err := json.Marshal(event.Message{Event: event.Log{Payload: "sup"}})
		if err != nil {
			return "", err
		}
		var message event.Message
		if err := json.Unmarshal(payload, &message); err != nil {
			return "", err
		}
		return fmt.Sprintf("%T:%s", message.Event, message.Event.(event.Log).Payload), nil
	case "missing-data":
		var message event.Message
		err := json.Unmarshal([]byte(`{"event":"log","version":"5.1"}`), &message)
		if err == nil {
			return "", fmt.Errorf("missing data was accepted")
		}
		return err.Error(), nil
	case "null-data":
		var message event.Message
		err := json.Unmarshal([]byte(`{"data":null,"event":"some-event","version":"1.0"}`), &message)
		if err == nil {
			return "", fmt.Errorf("null data was accepted")
		}
		return err.Error(), nil
	default:
		registered := map[string]atc.Event{
			"InitializeCheck": event.InitializeCheck{}, "InitializeTask": event.InitializeTask{},
			"StartTask": event.StartTask{}, "FinishTask": event.FinishTask{},
			"InitializeGet": event.InitializeGet{}, "StartGet": event.StartGet{}, "FinishGet": event.FinishGet{},
			"InitializePut": event.InitializePut{}, "StartPut": event.StartPut{}, "FinishPut": event.FinishPut{},
			"SetPipelineChanged": event.SetPipelineChanged{}, "Status": event.Status{},
			"WaitingForWorker": event.WaitingForWorker{}, "SelectedWorker": event.SelectedWorker{},
			"Log": event.Log{}, "Error": event.Error{}, "ImageCheck": event.ImageCheck{},
			"ImageGet": event.ImageGet{}, "AcrossSubsteps": event.AcrossSubsteps{},
		}
		exemplar, ok := registered[profile]
		if !ok {
			return "", fmt.Errorf("unknown event parser profile %q", profile)
		}
		parsed, err := event.ParseEvent(exemplar.Version(), exemplar.EventType(), []byte(`{}`))
		if err != nil {
			return "", err
		}
		if reflect.TypeOf(parsed) != reflect.TypeOf(exemplar) {
			return "", fmt.Errorf("expected %T, got %T", exemplar, parsed)
		}
		return "registered", nil
	}
}
