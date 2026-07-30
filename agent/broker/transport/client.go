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
	"sync"
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
	endpoint, bootstrap string
	http                *http.Client
	mu                  sync.RWMutex
	executions          map[string]string
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
	ExecutionID         string                  `json:"execution_id"`
	ExecutionCapability string                  `json:"execution_capability"`
	Succeeded           *broker.SucceededReplay `json:"succeeded,omitempty"`
	Terminal            *broker.Terminal        `json:"terminal,omitempty"`
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
	return &Client{endpoint: strings.TrimRight(config.Endpoint, "/"), bootstrap: config.BootstrapCapability, http: httpClient, executions: make(map[string]string)}, nil
}

func (c *Client) Admit(ctx context.Context, request broker.AdmissionRequest) (broker.Admission, error) {
	var response AdmitResponse
	input := AdmitRequest{IdempotencyKey: request.IdempotencyKey, Tool: request.Tool, Selector: request.Selector, ProfileID: request.ProfileID, ProfileDigest: request.ProfileDigest, InputDigest: request.InputDigest, Attachments: request.Attachments}
	if err := c.post(ctx, AdmitPath, input, &response, c.bootstrap); err != nil {
		return broker.Admission{}, err
	}
	if response.Succeeded != nil && response.Terminal != nil || (response.Succeeded != nil || response.Terminal != nil) && response.ExecutionCapability != "" {
		return broker.Admission{}, fmt.Errorf("broker authority: admit response has conflicting terminal fields")
	}
	if strings.TrimSpace(response.ExecutionID) == "" || (response.Succeeded == nil && response.Terminal == nil && strings.TrimSpace(response.ExecutionCapability) == "") {
		return broker.Admission{}, fmt.Errorf("broker authority: admit response is missing execution ID")
	}
	if response.ExecutionCapability != "" {
		c.mu.Lock()
		c.executions[response.ExecutionID] = response.ExecutionCapability
		c.mu.Unlock()
	}
	return broker.Admission{ExecutionID: response.ExecutionID, Succeeded: response.Succeeded, Terminal: response.Terminal}, nil
}
func (c *Client) Phase(ctx context.Context, id, phase string) error {
	return c.post(ctx, fmt.Sprintf(phasePath, url.PathEscape(id)), PhaseRequest{Phase: phase}, nil, c.executionCapability(id))
}
func (c *Client) Update(ctx context.Context, id string, update broker.RunUpdate) error {
	return c.post(ctx, fmt.Sprintf(updatePath, url.PathEscape(id)), UpdateRequest{Update: update}, nil, c.executionCapability(id))
}
func (c *Client) Terminal(ctx context.Context, id string, terminal broker.Terminal) error {
	return c.post(ctx, fmt.Sprintf(terminalPath, url.PathEscape(id)), TerminalRequest{Terminal: terminal}, nil, c.executionCapability(id))
}
func (c *Client) Seal(ctx context.Context, request broker.SealRequest) (snapshot.SnapshotRef, error) {
	var response snapshot.SnapshotRef
	if err := c.post(ctx, fmt.Sprintf(sealPath, url.PathEscape(request.ExecutionID)), SealRequest{Request: request}, &response, c.executionCapability(request.ExecutionID)); err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if response.ID <= 0 {
		return snapshot.SnapshotRef{}, fmt.Errorf("broker authority: seal response is invalid")
	}
	return response, nil
}
func (c *Client) executionCapability(id string) string {
	// Keep terminal capabilities for the client lifetime: callers may replay a
	// terminal request after an ambiguous transport failure. The capability is
	// independently short-lived and exact-execution scoped.
	c.mu.RLock()
	capability := c.executions[id]
	c.mu.RUnlock()
	return capability
}

func (c *Client) post(ctx context.Context, path string, input, output any, capability string) error {
	if strings.TrimSpace(capability) == "" {
		return fmt.Errorf("broker authority: execution capability is unavailable")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("broker authority: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("broker authority: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+capability)
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("broker authority: request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("broker authority: request rejected")
	}
	if output != nil {
		decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if decoder.Decode(output) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return fmt.Errorf("broker authority: decode response")
		}
	}
	return nil
}

var _ broker.Authority = (*Client)(nil)
