// Package workitem captures mutable ticket state as immutable work-item/v1
// snapshots before workflow admission.
package workitem

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

var (
	ErrInvalidRevision = errors.New("work item: invalid revision")
	ErrCaptureFailed   = errors.New("work item: snapshot capture failed")
)

// Revision is the authored content of one work item read from one source
// snapshot: its identity, its title, and its markdown body.
//
// It deliberately carries no lifecycle state, no workflow selection, and no
// separate spec/plan documents. State and workflow belong to the consumer (the
// durable run records which function ran over which snapshot), and a work
// item's prose lives in exactly one place — the body.
type Revision struct {
	TicketID   int
	Revision   int64
	UpdatedAt  time.Time
	Adapter    string
	ExternalID string
	Title      string
	Body       string
}

type CapturedRevision struct {
	TicketID   int
	Revision   int64
	CapturedAt time.Time
	Document   []byte
}

func (capture CapturedRevision) Clone() CapturedRevision {
	capture.Document = append([]byte(nil), capture.Document...)
	return capture
}

func (capture CapturedRevision) Validate() error {
	if capture.TicketID <= 0 || capture.Revision <= 0 || capture.CapturedAt.IsZero() || len(capture.Document) == 0 {
		return ErrInvalidRevision
	}
	decoder := json.NewDecoder(bytes.NewReader(capture.Document))
	decoder.DisallowUnknownFields()
	var document contracts.WorkItemDocument
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRevision, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRevision, err)
	}
	if err := document.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRevision, err)
	}
	documentCapturedAt, err := time.Parse(time.RFC3339Nano, document.CapturedAt)
	if err != nil || !documentCapturedAt.Equal(capture.CapturedAt) ||
		document.ExternalID == "" || document.Revision != strconv.FormatInt(capture.Revision, 10) {
		return ErrInvalidRevision
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return err
}

func MarshalRevision(revision Revision) (CapturedRevision, error) {
	if revision.TicketID <= 0 || revision.Revision <= 0 || revision.UpdatedAt.IsZero() {
		return CapturedRevision{}, ErrInvalidRevision
	}
	for label, value := range map[string]string{
		"adapter": revision.Adapter, "external ID": revision.ExternalID,
		"title": revision.Title, "body": revision.Body,
	} {
		if strings.TrimSpace(value) == "" {
			return CapturedRevision{}, fmt.Errorf("%w: %s is required", ErrInvalidRevision, label)
		}
	}

	// work-item/v1 is the authored content at one revision and nothing else.
	// The ticket's lifecycle state and the workflow chosen to consume it belong
	// to the durable run, which already records which function ran over which
	// snapshot; freezing either here would mint a second copy that is stale the
	// moment the ticket moves.
	document := contracts.WorkItemDocument{
		SchemaVersion: "1.0.0", Adapter: revision.Adapter, ExternalID: revision.ExternalID,
		Revision:   strconv.FormatInt(revision.Revision, 10),
		CapturedAt: revision.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Title:      revision.Title, Body: revision.Body,
	}
	if err := document.Validate(); err != nil {
		return CapturedRevision{}, fmt.Errorf("%w: %v", ErrInvalidRevision, err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return CapturedRevision{}, fmt.Errorf("%w: encode document: %v", ErrInvalidRevision, err)
	}
	captured := CapturedRevision{
		TicketID: revision.TicketID, Revision: revision.Revision,
		CapturedAt: revision.UpdatedAt, Document: encoded,
	}
	if err := captured.Validate(); err != nil {
		return CapturedRevision{}, err
	}
	return captured, nil
}

type Source interface {
	CaptureRevision(context.Context, int) (CapturedRevision, bool, error)
}

type SnapshotCreator interface {
	Upload(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error)
}

type Authority struct {
	TeamID      int
	TeamName    string
	Actor       string
	DisplayName string
}

func (authority Authority) Validate() error {
	if authority.TeamID <= 0 {
		return errors.New("work item: capture team ID must be positive")
	}
	for label, value := range map[string]string{
		"team name": authority.TeamName, "actor": authority.Actor, "display name": authority.DisplayName,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 500 {
			return fmt.Errorf("work item: capture %s is invalid", label)
		}
	}
	return nil
}

type CaptureResult struct {
	TicketID int
	Revision int64
	Snapshot snapshot.Snapshot
}

type Capturer struct {
	source    Source
	creator   SnapshotCreator
	authority Authority
}

func NewCapturer(source Source, creator SnapshotCreator, authority Authority) (*Capturer, error) {
	if nilInterface(source) || nilInterface(creator) {
		return nil, ErrCaptureFailed
	}
	if err := authority.Validate(); err != nil {
		return nil, err
	}
	return &Capturer{source: source, creator: creator, authority: authority}, nil
}

func (capturer *Capturer) CaptureRevision(ctx context.Context, ticketID int) (CaptureResult, bool, error) {
	if capturer == nil || ctx == nil || ticketID <= 0 {
		return CaptureResult{}, false, ErrCaptureFailed
	}
	if err := ctx.Err(); err != nil {
		return CaptureResult{}, false, err
	}
	revision, found, err := capturer.source.CaptureRevision(ctx, ticketID)
	if err != nil {
		return CaptureResult{}, false, errors.Join(ErrCaptureFailed, err)
	}
	if !found {
		return CaptureResult{}, false, nil
	}
	if revision.TicketID != ticketID {
		return CaptureResult{}, true, ErrCaptureFailed
	}
	if err := revision.Validate(); err != nil {
		return CaptureResult{}, true, err
	}
	var document contracts.WorkItemDocument
	if err := json.Unmarshal(revision.Document, &document); err != nil {
		return CaptureResult{}, true, errors.Join(ErrCaptureFailed, err)
	}
	archive, err := revisionArchive(revision.Document)
	if err != nil {
		return CaptureResult{}, true, err
	}
	metadata, err := json.Marshal(struct {
		Adapter    string `json:"adapter"`
		ExternalID string `json:"external_id"`
		TicketID   int    `json:"ticket_id"`
		Revision   int64  `json:"revision"`
	}{Adapter: document.Adapter, ExternalID: document.ExternalID, TicketID: ticketID, Revision: revision.Revision})
	if err != nil {
		return CaptureResult{}, true, ErrCaptureFailed
	}
	manifest, err := capturer.creator.Upload(ctx, snapshot.UploadRequest{
		TeamID: capturer.authority.TeamID, TeamName: capturer.authority.TeamName,
		UploadedBy: capturer.authority.DisplayName, Actor: capturer.authority.Actor,
		IdempotencyKey: fmt.Sprintf("work-item:%d:revision:%d", ticketID, revision.Revision),
		Type:           "work-item/v1", SourceMetadata: metadata,
		OpenTar: func(openCtx context.Context) (io.ReadCloser, error) {
			if err := openCtx.Err(); err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(archive)), nil
		},
	})
	if err != nil {
		return CaptureResult{}, true, errors.Join(ErrCaptureFailed, err)
	}
	if err := manifest.Validate(); err != nil || manifest.Type != "work-item/v1" {
		return CaptureResult{}, true, ErrCaptureFailed
	}
	return CaptureResult{TicketID: ticketID, Revision: revision.Revision, Snapshot: manifest.Clone()}, true, nil
}

func revisionArchive(document []byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	header := &tar.Header{
		Name: "work-item.json", Mode: 0600, Size: int64(len(document)), Typeflag: tar.TypeReg,
		ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Unix(0, 0).UTC(), ChangeTime: time.Unix(0, 0).UTC(),
	}
	writeErr := writer.WriteHeader(header)
	if writeErr == nil {
		_, writeErr = writer.Write(document)
	}
	closeErr := writer.Close()
	if writeErr != nil || closeErr != nil {
		return nil, errors.Join(ErrCaptureFailed, writeErr, closeErr)
	}
	return buffer.Bytes(), nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
