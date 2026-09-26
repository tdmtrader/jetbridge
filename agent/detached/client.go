// Package detached is the workload-neutral client for detached Runs, shared by
// every workload and by both the human CLI and the local stdio MCP server. It
// owns input upload, Run admission, credential handoff, the local submission
// receipt and the verify-then-parse result fetch. A workload supplies only
// what differs: the input it seals, the result whose producer receives
// credentials, and how its published result is parsed.
//
// Platform authentication is supplied by the caller's configured HTTP client.
package detached

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

	"github.com/concourse/concourse/atc"
)

type Handle struct {
	Team     string `json:"team"`
	Template string `json:"template"`
	Number   int    `json:"number"`
}

type Client struct {
	base string
	http *http.Client
}

func New(base string, transport *http.Client) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || transport == nil {
		return nil, errors.New("a JetBridge URL and authenticated HTTP client are required")
	}
	// API redirects are refusals, not permission to forward credentials or
	// a future upload to a different location. Preserve the caller's transport.
	httpClient := *transport
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(base, "/"), http: &httpClient}, nil
}

func (c *Client) Status(ctx context.Context, handle Handle) (atc.PipelineRun, error) {
	var run atc.PipelineRun
	endpoint, err := c.runEndpoint(handle)
	if err != nil {
		return run, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return run, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return run, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return run, fmt.Errorf("JetBridge returned HTTP %d", response.StatusCode)
	}
	const maxStatusBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxStatusBytes+1))
	if err != nil {
		return run, err
	}
	if len(body) > maxStatusBytes {
		return run, errors.New("JetBridge Run response exceeds 1 MiB")
	}
	if err = json.Unmarshal(body, &run); err != nil {
		return run, errors.New("JetBridge returned an invalid Run response")
	}
	if run.ID < 1 || run.Number != handle.Number {
		return run, errors.New("JetBridge returned a different Run identity")
	}
	return run, nil
}

func (c *Client) runEndpoint(handle Handle) (string, error) {
	if handle.Team == "" || handle.Template == "" || handle.Number < 1 {
		return "", errors.New("team, template and a positive Run number are required")
	}
	return c.base + "/api/v1/teams/" + url.PathEscape(handle.Team) + "/pipelines/" + url.PathEscape(handle.Template) + "/runs/" + strconv.Itoa(handle.Number), nil
}
