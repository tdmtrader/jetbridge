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
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
)

const checkpointCaptureResponseLimit = 64 << 10

// CheckpointCaptureClient is the narrow exact-node daemon transport required
// by checkpoint capture. It deliberately has no quiescence, restore, retry,
// or generation-commit authority.
type CheckpointCaptureClient interface {
	PrepareCheckpoint(context.Context, string, checkpoint.ArchiveRequest) (checkpoint.PreparedArchive, error)
	UploadCheckpoint(context.Context, string, checkpoint.PreparedArchive, checkpoint.ObjectUploadTicket) (checkpoint.ArchiveResult, error)
}

var _ CheckpointCaptureClient = (*DaemonClient)(nil)

func (d *DaemonClient) PrepareCheckpoint(ctx context.Context, nodeName string, request checkpoint.ArchiveRequest) (checkpoint.PreparedArchive, error) {
	if err := request.Validate(); err != nil {
		return checkpoint.PreparedArchive{}, err
	}
	endpoint, err := d.checkpointEndpoint(ctx, nodeName)
	if err != nil {
		return checkpoint.PreparedArchive{}, err
	}
	var prepared checkpoint.PreparedArchive
	if err := d.checkpointJSON(ctx, endpoint, "/checkpoints/v1/prepare", request, http.StatusCreated, &prepared); err != nil {
		return checkpoint.PreparedArchive{}, err
	}
	if err := prepared.Validate(); err != nil {
		return checkpoint.PreparedArchive{}, fmt.Errorf("checkpoint daemon prepare response: %w", err)
	}
	return prepared, nil
}

func (d *DaemonClient) UploadCheckpoint(ctx context.Context, nodeName string, prepared checkpoint.PreparedArchive, ticket checkpoint.ObjectUploadTicket) (checkpoint.ArchiveResult, error) {
	if err := prepared.Validate(); err != nil {
		return checkpoint.ArchiveResult{}, err
	}
	if err := validateCheckpointUploadTicket(ticket, prepared); err != nil {
		return checkpoint.ArchiveResult{}, err
	}
	endpoint, err := d.checkpointEndpoint(ctx, nodeName)
	if err != nil {
		return checkpoint.ArchiveResult{}, err
	}
	var result checkpoint.ArchiveResult
	if err := d.checkpointJSON(ctx, endpoint, "/checkpoints/v1/upload/"+prepared.Handle, ticket, http.StatusOK, &result); err != nil {
		return checkpoint.ArchiveResult{}, err
	}
	if err := validateCheckpointArchiveResult(result, prepared, ticket); err != nil {
		return checkpoint.ArchiveResult{}, err
	}
	return result, nil
}

func (d *DaemonClient) checkpointEndpoint(ctx context.Context, nodeName string) (DaemonEndpoint, error) {
	if strings.TrimSpace(nodeName) == "" {
		return DaemonEndpoint{}, errors.New("checkpoint daemon node name is required")
	}
	var endpoints []DaemonEndpoint
	var err error
	if d == nil {
		return DaemonEndpoint{}, errors.New("checkpoint daemon client is required")
	}
	if d.checkpointEndpoints != nil {
		endpoints, err = d.checkpointEndpoints(ctx)
	} else {
		endpoints, err = d.DaemonEndpoints(ctx)
	}
	if err != nil {
		return DaemonEndpoint{}, err
	}
	var matched []DaemonEndpoint
	for _, endpoint := range endpoints {
		if endpoint.NodeName == nodeName {
			matched = append(matched, endpoint)
		}
	}
	if len(matched) != 1 || strings.TrimSpace(matched[0].Address) == "" {
		return DaemonEndpoint{}, fmt.Errorf("checkpoint daemon endpoint for node %q is missing or ambiguous", nodeName)
	}
	return matched[0], nil
}

func (d *DaemonClient) checkpointJSON(ctx context.Context, endpoint DaemonEndpoint, route string, input any, expectedStatus int, output any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := d.snapshotHTTPClient()
	if err != nil {
		return err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal checkpoint daemon request: %w", err)
	}
	if len(body) > checkpointCaptureResponseLimit {
		return errors.New("checkpoint daemon request exceeds bound")
	}
	requestURL := (&url.URL{Scheme: d.scheme, Host: net.JoinHostPort(endpoint.Address, strconv.Itoa(d.port)), Path: route}).String()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("checkpoint daemon request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		io.Copy(io.Discard, io.LimitReader(response.Body, checkpointCaptureResponseLimit))
		return fmt.Errorf("checkpoint daemon %s returned HTTP %d", route, response.StatusCode)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, checkpointCaptureResponseLimit+1))
	if err != nil {
		return fmt.Errorf("read checkpoint daemon response: %w", err)
	}
	if len(responseBody) > checkpointCaptureResponseLimit {
		return errors.New("checkpoint daemon response exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode checkpoint daemon response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("checkpoint daemon response contains trailing JSON")
		}
		return fmt.Errorf("checkpoint daemon response trailing data: %w", err)
	}
	return nil
}

func validateCheckpointUploadTicket(ticket checkpoint.ObjectUploadTicket, prepared checkpoint.PreparedArchive) error {
	if ticket.ObjectID <= 0 || ticket.StagedCheckpointID <= 0 || ticket.Kind != hangar.KindCheckpoint || ticket.Digest != prepared.Digest {
		return errors.New("checkpoint upload ticket does not match prepared archive")
	}
	key, err := hangar.Key(hangar.KindCheckpoint, ticket.Digest)
	if err != nil || ticket.Key != key {
		return errors.New("checkpoint upload ticket key is invalid")
	}
	if ticket.AlreadyAvailable {
		if ticket.AvailableGeneration <= 0 || ticket.UploadToken != "" {
			return errors.New("checkpoint available ticket is invalid")
		}
		return nil
	}
	if strings.TrimSpace(ticket.UploadToken) == "" || ticket.AvailableGeneration != 0 {
		return errors.New("checkpoint upload ticket is invalid")
	}
	return nil
}

func validateCheckpointArchiveResult(result checkpoint.ArchiveResult, prepared checkpoint.PreparedArchive, ticket checkpoint.ObjectUploadTicket) error {
	ref := result.Object.Ref
	if ref.Kind != hangar.KindCheckpoint || ref.Digest != ticket.Digest || ref.Key != ticket.Key || ref.Generation <= 0 || result.Files != prepared.Files || result.Bytes != prepared.Bytes {
		return errors.New("checkpoint daemon upload response does not match prepared archive")
	}
	if ticket.AlreadyAvailable {
		if ref.Generation != ticket.AvailableGeneration || result.Object.UncompressedBytes != 0 || result.Object.CompressedBytes != 0 {
			return errors.New("checkpoint daemon available response does not match ticket")
		}
		return nil
	}
	if result.Object.UncompressedBytes != prepared.Bytes || result.Object.CompressedBytes <= 0 {
		return errors.New("checkpoint daemon upload accounting does not match prepared archive")
	}
	return nil
}
