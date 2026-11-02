package event

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/concourse/concourse/atc"
)

type eventTable map[atc.EventType]eventVersions
type eventVersions map[atc.EventVersion]eventParser
type eventParser func([]byte) (atc.Event, error)

var events = eventTable{}

func unmarshaler(e atc.Event) func([]byte) (atc.Event, error) {
	return func(payload []byte) (atc.Event, error) {
		val := reflect.New(reflect.TypeOf(e))
		err := json.Unmarshal(payload, val.Interface())
		return val.Elem().Interface().(atc.Event), err
	}
}

func RegisterEvent(e atc.Event) {
	versions, found := events[e.EventType()]
	if !found {
		versions = eventVersions{}
		events[e.EventType()] = versions
	}

	if _, taken := versions[e.Version()]; taken {
		panic(fmt.Sprintf("event %s version %s is already registered", e.EventType(), e.Version()))
	}

	versions[e.Version()] = unmarshaler(e)
}

func init() {
	RegisterEvent(Initialize{})
	RegisterEvent(Start{})
	RegisterEvent(Finish{})
	RegisterEvent(InitializeCheck{})
	RegisterEvent(InitializeTask{})
	RegisterEvent(StartTask{})
	RegisterEvent(FinishTask{})
	RegisterEvent(InitializeGet{})
	RegisterEvent(StartGet{})
	RegisterEvent(FinishGet{})
	RegisterEvent(InitializePut{})
	RegisterEvent(StartPut{})
	RegisterEvent(FinishPut{})
	RegisterEvent(SetPipelineChanged{})
	RegisterEvent(Status{})
	RegisterEvent(WaitingForWorker{})
	RegisterEvent(SelectedWorker{})
	RegisterEvent(Log{})
	RegisterEvent(Error{})
	RegisterEvent(ImageCheck{})
	RegisterEvent(ImageGet{})
	RegisterEvent(AcrossSubsteps{})
	RegisterEvent(Sidecar{})
}

type Message struct {
	Event atc.Event
}

type Envelope struct {
	Data    *json.RawMessage `json:"data"`
	Event   atc.EventType    `json:"event"`
	Version atc.EventVersion `json:"version"`
	EventID string           `json:"event_id"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	var envelope Envelope

	payload, err := json.Marshal(m.Event)
	if err != nil {
		return nil, err
	}

	envelope.Data = (*json.RawMessage)(&payload)
	envelope.Event = m.Event.EventType()
	envelope.Version = m.Event.Version()

	return json.Marshal(envelope)
}

func (m *Message) UnmarshalJSON(bytes []byte) error {
	var envelope Envelope

	err := json.Unmarshal(bytes, &envelope)
	if err != nil {
		return err
	}

	if envelope.Data == nil {
		return MissingEventDataError{
			Type:    envelope.Event,
			Version: envelope.Version,
		}
	}

	event, err := ParseEvent(envelope.Version, envelope.Event, *envelope.Data)
	if err != nil {
		return err
	}

	m.Event = event

	return nil
}

type UnknownEventTypeError struct {
	Type atc.EventType
}

func (err UnknownEventTypeError) Error() string {
	return fmt.Sprintf("unknown event type: %s", err.Type)
}

type MissingEventDataError struct {
	Type    atc.EventType
	Version atc.EventVersion
}

func (err MissingEventDataError) Error() string {
	return fmt.Sprintf("missing event data: %s version %s", err.Type, err.Version)
}

type UnknownEventVersionError struct {
	Type          atc.EventType
	Version       atc.EventVersion
	KnownVersions []string
}

func (err UnknownEventVersionError) Error() string {
	return fmt.Sprintf(
		"unknown event version: %s version %s (supported versions: %s)",
		err.Type,
		err.Version,
		strings.Join(err.KnownVersions, ", "),
	)
}

func ParseEvent(version atc.EventVersion, typ atc.EventType, payload []byte) (atc.Event, error) {
	versions, found := events[typ]
	if !found {
		return nil, UnknownEventTypeError{typ}
	}

	knownVersions := []string{}
	for v, parser := range versions {
		knownVersions = append(knownVersions, string(v))

		if v.IsCompatibleWith(version) {
			return parser(payload)
		}
	}

	return nil, UnknownEventVersionError{
		Type:          typ,
		Version:       version,
		KnownVersions: knownVersions,
	}
}
