// Package client is the implement adapter over the workload-neutral Run
// client. It fixes what makes a submission an implementation: the sealed
// snapshot, the input and result names, and the verified change it trusts on
// read-back. The human CLI and the local stdio MCP server share it.
package client

import (
	"context"
	"io"
	"net/http"

	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/agent/runclient"
)

const (
	// SnapshotInput is the run_inputs name the snapshot is uploaded under.
	SnapshotInput = "snapshot"
	// ChangeResult is the result the implement template publishes and whose
	// producer, the author task, receives the owner's credentials.
	ChangeResult = "change"
	// ValidationResult is the result the template's validate task publishes:
	// the operator's command run against the change. It never receives
	// credentials.
	ValidationResult = "validation"
)

// Workload returns the implement workload. It is a function so no caller can
// change the value every submission uses.
func Workload() runclient.Workload {
	return runclient.Workload{Name: "implement", Input: SnapshotInput, CredentialResult: ChangeResult, Load: loadSnapshot}
}

func loadSnapshot(dir string) (runclient.Input, error) {
	snapshot, err := implement.LoadSnapshot(dir)
	if err != nil {
		return nil, err
	}
	return snapshotInput{snapshot}, nil
}

type snapshotInput struct{ snapshot *implement.Snapshot }

func (s snapshotInput) Digest() string { return s.snapshot.Digest }
func (s snapshotInput) WriteArchive(ctx context.Context, w io.Writer) error {
	return s.snapshot.WriteRunInputArchive(ctx, w)
}

type (
	Handle        = runclient.Handle
	Submission    = runclient.Submission
	SubmitOptions = runclient.SubmitOptions
)

// Client is the Run client bound to the implement workload. Status,
// input upload, Run admission and credential sessions are the shared ones.
type Client struct{ *runclient.Client }

func New(base string, transport *http.Client) (*Client, error) {
	shared, err := runclient.New(base, transport)
	if err != nil {
		return nil, err
	}
	return &Client{shared}, nil
}

// Submit submits a captured snapshot. See runclient.Client.Submit.
func (c *Client) Submit(ctx context.Context, options SubmitOptions) (Submission, error) {
	return c.Client.Submit(ctx, Workload(), options)
}
