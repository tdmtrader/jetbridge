// Package client is the review adapter over the workload-neutral detached Run
// client. It fixes what makes a submission a review: the sealed bundle, the
// input and result names, and the typed report it trusts on read-back. The
// human CLI and the local stdio MCP server share it.
package client

import (
	"context"
	"io"
	"net/http"

	"github.com/concourse/concourse/agent/detached"
	"github.com/concourse/concourse/agent/review"
)

// FindingsResult is the result the review template publishes and whose
// producer receives the owner's credentials.
const FindingsResult = "findings"

var workload = detached.Workload{
	Name:             "review",
	Input:            "change",
	CredentialResult: FindingsResult,
	Load: func(dir string) (detached.Input, error) {
		bundle, err := review.LoadBundle(dir)
		if err != nil {
			return nil, err
		}
		return bundleInput{bundle}, nil
	},
}

type bundleInput struct{ bundle *review.Bundle }

func (b bundleInput) Digest() string { return b.bundle.Digest }
func (b bundleInput) WriteArchive(ctx context.Context, w io.Writer) error {
	return b.bundle.WriteRunInputArchive(ctx, w)
}

type (
	Handle        = detached.Handle
	Submission    = detached.Submission
	SubmitOptions = detached.SubmitOptions
)

// Client is the detached Run client bound to the review workload. Status,
// input upload, Run admission and credential sessions are the shared ones.
type Client struct{ *detached.Client }

func New(base string, transport *http.Client) (*Client, error) {
	shared, err := detached.New(base, transport)
	if err != nil {
		return nil, err
	}
	return &Client{shared}, nil
}

// Submit submits a captured review bundle. See detached.Client.Submit.
func (c *Client) Submit(ctx context.Context, options SubmitOptions) (Submission, error) {
	return c.Client.Submit(ctx, workload, options)
}
