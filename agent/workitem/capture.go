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

type WorkflowSelection struct {
	Name         string `json:"name,omitempty"`
	Version      *int   `json:"version,omitempty"`
	DefinitionID *int   `json:"definition_id,omitempty"`
}

type SpecRevision struct {
	Version            int      `json:"version"`
	Title              string   `json:"title"`
	Body               string   `json:"body"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Links              []Link   `json:"links"`
	SubmittedBy        string   `json:"submitted_by"`
	CreatedAt          int64    `json:"created_at"`
}

type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type TaskRevision struct {
	Ordering  int    `json:"ordering"`
	Title     string `json:"title"`
	Detail    string `json:"detail,omitempty"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updated_at"`
}

type PlanRevision struct {
	Version int            `json:"version"`
	Tasks   []TaskRevision `json:"tasks"`
}

// CommentRevision retains both the mutable comment/question text and its
// first-writer answer. Revision is local to this comment and increments when
// the answer is recorded; the enclosing ticket Revision increments in the
// same mutation transaction.
type CommentRevision struct {
	ID         int    `json:"id"`
	Revision   int64  `json:"revision"`
	Body       string `json:"body"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  int64  `json:"created_at"`
	Answer     string `json:"answer,omitempty"`
	AnsweredBy string `json:"answered_by,omitempty"`
	AnsweredAt int64  `json:"answered_at,omitempty"`
}

// Revision is all mutable work-item state read from one source snapshot.
type Revision struct {
	TicketID   int
	Revision   int64
	UpdatedAt  time.Time
	Adapter    string
	ExternalID string
	Title      string
	Body       string
	State      string
	Workflow   WorkflowSelection
	Spec       *SpecRevision
	Plan       *PlanRevision
	Comments   []CommentRevision
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
		"title": revision.Title, "body": revision.Body, "state": revision.State,
	} {
		if strings.TrimSpace(value) == "" {
			return CapturedRevision{}, fmt.Errorf("%w: %s is required", ErrInvalidRevision, label)
		}
	}

	document := contracts.WorkItemDocument{
		SchemaVersion: "1.0.0", Adapter: revision.Adapter, ExternalID: revision.ExternalID,
		Revision:   strconv.FormatInt(revision.Revision, 10),
		CapturedAt: revision.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Title:      revision.Title, Body: revision.Body, State: revision.State,
		Workflow: &contracts.WorkItemWorkflowSelection{
			Name: revision.Workflow.Name, Version: cloneInt(revision.Workflow.Version),
			DefinitionID: cloneInt(revision.Workflow.DefinitionID),
		},
	}
	if revision.Spec != nil {
		content, err := json.Marshal(revision.Spec)
		if err != nil {
			return CapturedRevision{}, fmt.Errorf("%w: encode spec: %v", ErrInvalidRevision, err)
		}
		document.Spec = &contracts.WorkItemRevision{Revision: strconv.Itoa(revision.Spec.Version), Content: string(content)}
	}
	if revision.Plan != nil {
		content, err := json.Marshal(revision.Plan)
		if err != nil {
			return CapturedRevision{}, fmt.Errorf("%w: encode plan: %v", ErrInvalidRevision, err)
		}
		document.Plan = &contracts.WorkItemRevision{Revision: strconv.Itoa(revision.Plan.Version), Content: string(content)}
	}
	if len(revision.Comments) > 0 {
		document.Comments = make([]contracts.WorkItemCommentRevision, 0, len(revision.Comments))
		for _, comment := range revision.Comments {
			if comment.ID <= 0 || comment.Revision <= 0 {
				return CapturedRevision{}, ErrInvalidRevision
			}
			content, err := json.Marshal(comment)
			if err != nil {
				return CapturedRevision{}, fmt.Errorf("%w: encode comment: %v", ErrInvalidRevision, err)
			}
			document.Comments = append(document.Comments, contracts.WorkItemCommentRevision{
				ExternalID: strconv.Itoa(comment.ID), Revision: strconv.FormatInt(comment.Revision, 10), Content: string(content),
			})
		}
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

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
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
