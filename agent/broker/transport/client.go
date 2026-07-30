// Package transport defines the private broker-worker to ATC authority wire
// contract. ATC owns the matching handlers; this package deliberately does
// not register server routes.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
)

const (
	AdmitPath    = "/api/v1/internal/agent-child-executions/admit"
	phasePath    = "/api/v1/internal/agent-child-executions/%s/phase"
	updatePath   = "/api/v1/internal/agent-child-executions/%s/update"
	terminalPath = "/api/v1/internal/agent-child-executions/%s/terminal"
	sealPath     = "/api/v1/internal/agent-child-executions/%s/seal"
)

type Config struct {
	Endpoint            string
	BootstrapCapability string
	HTTPClient          *http.Client
}

type Client struct {
	endpoint, capability string
	http                 *http.Client
}

// The following request/response types are the stable private wire contract
// for the Task 8 ATC handlers. They intentionally carry no credential field.
type AdmitRequest struct {
	IdempotencyKey string          `json:"idempotency_key"`
	Tool           broker.Tool     `json:"tool"`
	Selector       broker.Selector `json:"selector"`
	ProfileID      string          `json:"profile_id"`
	ProfileDigest  string          `json:"profile_digest"`
	InputDigest    string          `json:"input_digest"`
	Attachments    []string        `json:"attachments"`
}
type AdmitResponse struct {
	ExecutionID string `json:"execution_id"`
}
type PhaseRequest struct {
	Phase string `json:"phase"`
}
type UpdateRequest struct {
	Update broker.RunUpdate `json:"update"`
}
type TerminalRequest struct {
	Terminal broker.Terminal `json:"terminal"`
}
type SealRequest struct {
	Request broker.SealRequest `json:"request"`
}

func NewClient(config Config) (*Client, error) {
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("broker authority: endpoint must be an absolute HTTP URL without user info, query, or fragment")
	}
	if strings.TrimSpace(config.BootstrapCapability) == "" {
		return nil, fmt.Errorf("broker authority: bootstrap capability is required")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	// The bootstrap bearer is authority-scoped. Never follow a redirect that
	// could carry it to an unintended endpoint, including a same-host path.
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoint: strings.TrimRight(config.Endpoint, "/"), capability: config.BootstrapCapability, http: httpClient}, nil
}

func (c *Client) Admit(ctx context.Context, request broker.AdmissionRequest) (string, error) {
	var response AdmitResponse
	input := AdmitRequest{IdempotencyKey: request.IdempotencyKey, Tool: request.Tool, Selector: request.Selector, ProfileID: request.ProfileID, ProfileDigest: request.ProfileDigest, InputDigest: request.InputDigest, Attachments: request.Attachments}
	if err := c.post(ctx, AdmitPath, input, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.ExecutionID) == "" {
		return "", fmt.Errorf("broker authority: admit response is missing execution ID")
	}
	return response.ExecutionID, nil
}
func (c *Client) Phase(ctx context.Context, id, phase string) error {
	return c.post(ctx, fmt.Sprintf(phasePath, id), PhaseRequest{Phase: phase}, nil)
}
func (c *Client) Update(ctx context.Context, id string, update broker.RunUpdate) error {
	return c.post(ctx, fmt.Sprintf(updatePath, id), UpdateRequest{Update: update}, nil)
}
func (c *Client) Terminal(ctx context.Context, id string, terminal broker.Terminal) error {
	return c.post(ctx, fmt.Sprintf(terminalPath, id), TerminalRequest{Terminal: terminal}, nil)
}
func (c *Client) Seal(ctx context.Context, request broker.SealRequest) (snapshot.SnapshotRef, error) {
	var response snapshot.SnapshotRef
	if err := c.post(ctx, fmt.Sprintf(sealPath, request.ExecutionID), SealRequest{Request: request}, &response); err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if response.ID <= 0 {
		return snapshot.SnapshotRef{}, fmt.Errorf("broker authority: seal response is invalid")
	}
	return response, nil
}
func (c *Client) post(ctx context.Context, path string, input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("broker authority: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("broker authority: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.capability)
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("broker authority: request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("broker authority: request rejected")
	}
	if output != nil && json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output) != nil {
		return fmt.Errorf("broker authority: decode response")
	}
	return nil
}

var _ broker.Authority = (*Client)(nil)
