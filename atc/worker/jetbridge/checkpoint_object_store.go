package jetbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
)

const (
	checkpointObjectInspectRoute = "/checkpoints/v1/objects/inspect"
	checkpointObjectDeleteRoute  = "/checkpoints/v1/objects/delete"
	checkpointObjectBodyLimit    = 64 << 10
)

// CheckpointObjectStore is the ATC's durable checkpoint-object authority. The
// ATC holds no Hangar credentials, so every durable read and delete goes
// through a daemon; unlike capture, reclamation is not tied to the node that
// produced the content, because a Hangar object belongs to no node at all.
type CheckpointObjectStore struct {
	daemon *DaemonClient
}

var _ checkpoint.DurableObjectStore = (*CheckpointObjectStore)(nil)

func NewCheckpointObjectStore(daemon *DaemonClient) *CheckpointObjectStore {
	return &CheckpointObjectStore{daemon: daemon}
}

// InspectObject reports the stored generation of one durable checkpoint
// object, or hangar.ErrNotFound when storage proves it is not there.
//
// Only a proven absence is reported as absence. An unreachable daemon, one
// without a durable store, or any other inconclusive answer is an error,
// because the caller acts on absence by retiring the database row that is the
// object's last remaining name.
func (store *CheckpointObjectStore) InspectObject(ctx context.Context, kind hangar.Kind, digest hangar.Digest) (hangar.ObjectRef, error) {
	if kind != hangar.KindCheckpoint {
		return hangar.ObjectRef{}, fmt.Errorf("jetbridge: object kind %q is not a checkpoint", kind)
	}
	if _, err := hangar.Key(kind, digest); err != nil {
		return hangar.ObjectRef{}, fmt.Errorf("jetbridge: inspect checkpoint object: %w", err)
	}

	var observed hangar.ObjectRef
	err := store.request(ctx, checkpointObjectInspectRoute, checkpointObjectDigestRequest{Digest: digest},
		func(status int, body []byte) error {
			switch status {
			case http.StatusOK:
				if err := decodeCheckpointObjectJSON(body, &observed); err != nil {
					return err
				}
				if observed.Kind != hangar.KindCheckpoint || observed.Digest != digest {
					return fmt.Errorf("jetbridge: daemon answered about checkpoint object %q, not %q", observed.Digest, digest)
				}
				return observed.Validate()
			case http.StatusNotFound:
				return fmt.Errorf("%w: %s", hangar.ErrNotFound, digest)
			default:
				return checkpointObjectStatusError(checkpointObjectInspectRoute, status)
			}
		})
	if err != nil {
		return hangar.ObjectRef{}, err
	}
	return observed, nil
}

// DeleteObject releases one exact durable checkpoint object. The reference is
// sent verbatim so the daemon can pin the delete to the generation the caller
// judged; a generation that no longer matches comes back as hangar.ErrConflict
// rather than as a success.
func (store *CheckpointObjectStore) DeleteObject(ctx context.Context, ref hangar.ObjectRef) error {
	if ref.Kind != hangar.KindCheckpoint {
		return fmt.Errorf("jetbridge: object kind %q is not a checkpoint", ref.Kind)
	}
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("jetbridge: delete checkpoint object: %w", err)
	}
	return store.request(ctx, checkpointObjectDeleteRoute, ref, func(status int, _ []byte) error {
		switch status {
		case http.StatusNoContent:
			return nil
		case http.StatusConflict:
			return fmt.Errorf("%w: checkpoint object %s changed before deletion", hangar.ErrConflict, ref.Digest)
		default:
			return checkpointObjectStatusError(checkpointObjectDeleteRoute, status)
		}
	})
}

// request asks daemons in a stable order until one gives a definitive answer.
//
// Every daemon reaches the same Hangar bucket, so which one answers is an
// availability question rather than a correctness one — but only for answers
// that carry no information about the object. A transport failure or an
// unavailable durable store says nothing and is worth asking elsewhere; a
// proven absence, a generation conflict, or a rejected request is the same
// everywhere, and re-asking could only turn a definitive answer into a
// different one for the wrong reason.
func (store *CheckpointObjectStore) request(ctx context.Context, route string, input any, interpret func(int, []byte) error) error {
	if store == nil || store.daemon == nil {
		return errors.New("jetbridge: checkpoint object daemon client is required")
	}
	endpoints, err := store.daemon.checkpointObjectEndpoints(ctx)
	if err != nil {
		return err
	}
	if len(endpoints) == 0 {
		return errors.New("jetbridge: no live artifact daemon can reach durable checkpoint storage")
	}

	var attempts []error
	for _, endpoint := range endpoints {
		if err := ctx.Err(); err != nil {
			return err
		}
		status, body, err := store.daemon.checkpointObjectRequest(ctx, endpoint, route, input)
		if err != nil {
			attempts = append(attempts, fmt.Errorf("%s: %w", endpoint.NodeName, err))
			continue
		}
		if checkpointObjectAnswerIsInconclusive(status) {
			attempts = append(attempts, fmt.Errorf("%s: %w", endpoint.NodeName, checkpointObjectStatusError(route, status)))
			continue
		}
		return interpret(status, body)
	}
	return fmt.Errorf("jetbridge: no artifact daemon answered %s: %w", route, errors.Join(attempts...))
}

// checkpointObjectAnswerIsInconclusive reports statuses that describe the
// daemon rather than the object. These are the only statuses another daemon
// may be asked about.
func checkpointObjectAnswerIsInconclusive(status int) bool {
	return status == http.StatusServiceUnavailable ||
		status == http.StatusBadGateway ||
		status == http.StatusGatewayTimeout ||
		status == http.StatusTooManyRequests ||
		status == http.StatusInternalServerError
}

func checkpointObjectStatusError(route string, status int) error {
	return fmt.Errorf("checkpoint object daemon %s returned HTTP %d", route, status)
}

func (d *DaemonClient) checkpointObjectEndpoints(ctx context.Context) ([]DaemonEndpoint, error) {
	if d == nil {
		return nil, errors.New("jetbridge: checkpoint object daemon client is required")
	}
	var endpoints []DaemonEndpoint
	var err error
	if d.checkpointEndpoints != nil {
		endpoints, err = d.checkpointEndpoints(ctx)
	} else {
		endpoints, err = d.DaemonEndpoints(ctx)
	}
	if err != nil {
		return nil, err
	}
	live := make([]DaemonEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Address != "" {
			live = append(live, endpoint)
		}
	}
	// A stable order keeps one daemon carrying reclamation traffic instead of
	// spreading identical requests across the fleet on every pass.
	sort.Slice(live, func(i, j int) bool { return live[i].NodeName < live[j].NodeName })
	return live, nil
}

func (d *DaemonClient) checkpointObjectRequest(ctx context.Context, endpoint DaemonEndpoint, route string, input any) (int, []byte, error) {
	client, err := d.snapshotHTTPClient()
	if err != nil {
		return 0, nil, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal checkpoint object request: %w", err)
	}
	if len(body) > checkpointObjectBodyLimit {
		return 0, nil, errors.New("checkpoint object request exceeds bound")
	}
	requestURL := (&url.URL{
		Scheme: d.scheme,
		Host:   net.JoinHostPort(endpoint.Address, strconv.Itoa(d.port)),
		Path:   route,
	}).String()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("checkpoint object daemon request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, checkpointObjectBodyLimit+1))
	if err != nil {
		return 0, nil, fmt.Errorf("read checkpoint object daemon response: %w", err)
	}
	if len(responseBody) > checkpointObjectBodyLimit {
		return 0, nil, errors.New("checkpoint object daemon response exceeds bound")
	}
	return response.StatusCode, responseBody, nil
}

// checkpointObjectDigestRequest mirrors the daemon's inspect request. The
// caller names content and nothing else; the daemon derives the key and
// applies its own byte ceiling.
type checkpointObjectDigestRequest struct {
	Digest hangar.Digest `json:"digest"`
}

func decodeCheckpointObjectJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode checkpoint object daemon response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("checkpoint object daemon response contains trailing JSON")
		}
		return fmt.Errorf("checkpoint object daemon response trailing data: %w", err)
	}
	return nil
}
