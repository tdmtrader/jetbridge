package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/concourse/concourse/agent/snapshot"
)

// RawRecordCodec is the deliberately closed bridge from untrusted author JSON
// to the current built-in typed-record contracts. It is not seal authority.
type RawRecordCodec interface {
	Type() snapshot.TypeRef
	DecodeBody(json.RawMessage) (any, error)
	EncodeRecord([]Subject, any) ([]byte, error)
	ValidateBody([]Subject, any) error
}

type rawRecordCodec[T any] struct {
	ref      snapshot.TypeRef
	validate func([]Subject, T) error
}

func (codec rawRecordCodec[T]) Type() snapshot.TypeRef { return codec.ref }
func (codec rawRecordCodec[T]) DecodeBody(raw json.RawMessage) (any, error) {
	var body T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("snapshot contracts: decode raw %q body: %w", codec.ref, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("snapshot contracts: decode raw %q body: trailing JSON", codec.ref)
	}
	return body, nil
}
func (codec rawRecordCodec[T]) EncodeRecord(subjects []Subject, value any) ([]byte, error) {
	body, ok := value.(T)
	if !ok {
		return nil, fmt.Errorf("snapshot contracts: raw %q codec received body of type %T", codec.ref, value)
	}
	record, err := NewRecord(codec.ref, subjects, body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(record)
}
func (codec rawRecordCodec[T]) ValidateBody(subjects []Subject, value any) error {
	body, ok := value.(T)
	if !ok {
		return fmt.Errorf("snapshot contracts: raw %q codec received body of type %T", codec.ref, value)
	}
	return codec.validate(subjects, body)
}

var builtinRawRecordCodecs = map[snapshot.TypeRef]RawRecordCodec{
	reviewType: rawRecordCodec[ReviewBody]{ref: reviewType, validate: func(subjects []Subject, body ReviewBody) error {
		return reviewBody(Record[ReviewBody]{Subjects: subjects, Body: body})
	}},
	diagnosisType: rawRecordCodec[DiagnosisBody]{ref: diagnosisType, validate: func(subjects []Subject, body DiagnosisBody) error {
		return diagnosisBody(Record[DiagnosisBody]{Subjects: subjects, Body: body})
	}},
	validationType: rawRecordCodec[ValidationBody]{ref: validationType, validate: func(subjects []Subject, body ValidationBody) error {
		return validationBody(Record[ValidationBody]{Subjects: subjects, Body: body})
	}},
	repositoryChangeType: rawRecordCodec[RepositoryChangeBody]{ref: repositoryChangeType, validate: func(subjects []Subject, body RepositoryChangeBody) error {
		return repositoryChangeBody(Record[RepositoryChangeBody]{Subjects: subjects, Body: body})
	}},
	measurementsType: rawRecordCodec[MeasurementsBody]{ref: measurementsType, validate: func(subjects []Subject, body MeasurementsBody) error {
		return measurementsBody(Record[MeasurementsBody]{Subjects: subjects, Body: body})
	}},
}

func BuiltinRawRecordCodec(ref snapshot.TypeRef) (RawRecordCodec, bool) {
	codec, found := builtinRawRecordCodecs[ref]
	return codec, found
}
func BuiltinRawRecordTypes() []snapshot.TypeRef {
	return []snapshot.TypeRef{diagnosisType, measurementsType, repositoryChangeType, reviewType, validationType}
}
func NormalizeRawRecordBody(ref snapshot.TypeRef, subjects []Subject, raw json.RawMessage) (any, json.RawMessage, error) {
	codec, found := BuiltinRawRecordCodec(ref)
	if !found {
		return nil, nil, fmt.Errorf("snapshot contracts: %q has no built-in raw record codec", ref)
	}
	body, err := codec.DecodeBody(raw)
	if err != nil {
		return nil, nil, err
	}
	if err := codec.ValidateBody(subjects, body); err != nil {
		return nil, nil, err
	}
	normalized, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot contracts: encode raw %q body: %w", ref, err)
	}
	return body, normalized, nil
}
