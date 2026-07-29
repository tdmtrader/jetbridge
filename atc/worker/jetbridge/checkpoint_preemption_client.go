package jetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	checkpointPreemptionNoticeWait  = 25 * time.Second
	checkpointPreemptionResponseMax = 4 << 10
)

type checkpointPreemptionNotice struct {
	Sequence   uint64    `json:"sequence"`
	ObservedAt time.Time `json:"observed_at"`
}

// CheckpointPreemptionNoticeSource reads the latched warning from the exact
// scheduled node's daemon. It has no fan-out or retry authority for an
// ambiguous response.
type CheckpointPreemptionNoticeSource struct {
	daemon   *DaemonClient
	nodeName string

	mu    sync.Mutex
	after uint64
}

func NewCheckpointPreemptionNoticeSource(daemon *DaemonClient, nodeName string) (*CheckpointPreemptionNoticeSource, error) {
	if daemon == nil {
		return nil, errors.New("checkpoint preemption daemon client is required")
	}
	if daemon.scheme != "https" {
		return nil, errors.New("checkpoint preemption source requires daemon mTLS")
	}
	if _, err := daemon.snapshotHTTPClient(); err != nil {
		return nil, err
	}
	if nodeName == "" || strings.TrimSpace(nodeName) != nodeName {
		return nil, errors.New("checkpoint preemption source requires an exact scheduled node")
	}
	return &CheckpointPreemptionNoticeSource{daemon: daemon, nodeName: nodeName}, nil
}

// WaitForNodePreemption repeats only the daemon's definitive no-notice (204)
// interval. Transport failures and all other responses are returned directly,
// because retrying them could duplicate an ambiguous request.
func (source *CheckpointPreemptionNoticeSource) WaitForNodePreemption(ctx context.Context) (time.Time, error) {
	if source == nil || source.daemon == nil {
		return time.Time{}, errors.New("checkpoint preemption source is not configured")
	}
	source.mu.Lock()
	defer source.mu.Unlock()

	for {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		endpoint, err := source.daemon.checkpointEndpoint(ctx, source.nodeName)
		if err != nil {
			return time.Time{}, err
		}
		client, err := source.daemon.snapshotHTTPClient()
		if err != nil {
			return time.Time{}, err
		}
		target, err := url.Parse(source.daemon.routeURL(endpoint.Address, "checkpoints/v1/preemption-notice"))
		if err != nil {
			return time.Time{}, err
		}
		query := target.Query()
		query.Set("after", strconv.FormatUint(source.after, 10))
		query.Set("wait", checkpointPreemptionNoticeWait.String())
		target.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return time.Time{}, err
		}
		response, err := client.Do(request)
		if err != nil {
			return time.Time{}, fmt.Errorf("wait for checkpoint preemption notice: %w", err)
		}

		switch response.StatusCode {
		case http.StatusNoContent:
			err := discardAndClosePreemptionResponse(response.Body)
			if err != nil {
				return time.Time{}, err
			}
			continue
		case http.StatusOK:
			notice, decodeErr := decodeCheckpointPreemptionNotice(response.Body)
			closeErr := response.Body.Close()
			if decodeErr != nil || closeErr != nil {
				return time.Time{}, errors.Join(decodeErr, closeErr)
			}
			if notice.Sequence <= source.after || notice.ObservedAt.IsZero() || notice.ObservedAt.Location() != time.UTC {
				return time.Time{}, errors.New("checkpoint preemption daemon returned a stale or invalid notice")
			}
			source.after = notice.Sequence
			return notice.ObservedAt, nil
		default:
			_ = discardAndClosePreemptionResponse(response.Body)
			return time.Time{}, fmt.Errorf("checkpoint preemption daemon returned HTTP %d", response.StatusCode)
		}
	}
}

func discardAndClosePreemptionResponse(body io.ReadCloser) error {
	_, readErr := io.Copy(io.Discard, io.LimitReader(body, checkpointPreemptionResponseMax+1))
	return errors.Join(readErr, body.Close())
}

func decodeCheckpointPreemptionNotice(body io.Reader) (checkpointPreemptionNotice, error) {
	raw, err := io.ReadAll(io.LimitReader(body, checkpointPreemptionResponseMax+1))
	if err != nil {
		return checkpointPreemptionNotice{}, fmt.Errorf("read checkpoint preemption notice: %w", err)
	}
	if len(raw) > checkpointPreemptionResponseMax {
		return checkpointPreemptionNotice{}, fmt.Errorf("checkpoint preemption notice exceeds %d bytes", checkpointPreemptionResponseMax)
	}
	var notice checkpointPreemptionNotice
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&notice); err != nil {
		return checkpointPreemptionNotice{}, fmt.Errorf("decode checkpoint preemption notice: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return checkpointPreemptionNotice{}, errors.New("checkpoint preemption notice contains trailing JSON")
		}
		return checkpointPreemptionNotice{}, fmt.Errorf("checkpoint preemption notice trailing data: %w", err)
	}
	return notice, nil
}
