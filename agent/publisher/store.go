package publisher

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

var (
	ErrInvalidResult     = errors.New("publisher: invalid result")
	ErrOperationNotFound = errors.New("publisher: operation not found")
	ErrOperationConflict = errors.New("publisher: operation conflict")
)

type Status string

const (
	StatusPending        Status = "pending"
	StatusSucceeded      Status = "succeeded"
	StatusFailed         Status = "failed"
	StatusStaleBase      Status = "stale_base"
	StatusRebaseRequired Status = "rebase_required"
)

func (status Status) terminal() bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusStaleBase, StatusRebaseRequired:
		return true
	default:
		return false
	}
}

type Result struct {
	Status     Status `json:"status"`
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
	BaseSHA    string `json:"base_sha,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

func (result Result) Validate() error {
	if !result.Status.terminal() {
		return fmt.Errorf("%w: status must be terminal", ErrInvalidResult)
	}
	for name, value := range map[string]string{
		"external_id": result.ExternalID, "url": result.URL, "head_sha": result.HeadSHA,
		"base_sha": result.BaseSHA,
	} {
		if value != "" && !boundedText(value, 4096, false) {
			return fmt.Errorf("%w: %s is invalid", ErrInvalidResult, name)
		}
	}
	if len(result.Detail) > 64<<10 || strings.IndexByte(result.Detail, 0) >= 0 {
		return fmt.Errorf("%w: detail is invalid", ErrInvalidResult)
	}
	if result.Status != StatusSucceeded && strings.TrimSpace(result.Detail) == "" {
		return fmt.Errorf("%w: non-success result requires detail", ErrInvalidResult)
	}
	return nil
}

type Publication struct {
	ID           snapshot.DatabaseID `json:"id,omitempty"`
	OperationKey string              `json:"operation_key"`
	Request      Request             `json:"request"`
	Status       Status              `json:"status"`
	Attempt      int                 `json:"attempt"`
	LeaseUntil   time.Time           `json:"lease_until,omitempty"`
	Result       Result              `json:"result,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

func (publication Publication) Clone() Publication {
	publication.Request = publication.Request.Clone()
	return publication
}

type Store interface {
	Acquire(context.Context, Request, time.Duration) (Publication, bool, error)
	Complete(context.Context, string, int, Result) (Publication, error)
	Get(context.Context, string) (Publication, bool, error)
}

var operationKeyPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
