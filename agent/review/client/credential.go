package client

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
)

func (c *Client) CredentialSession(ctx context.Context, handle Handle, result string) (atc.RunCredentialSession, error) {
	return c.credentialRequest(ctx, handle, result, nil)
}

// HandoffCredentials sends a single owner-selected stream. It does not retry
// delivery. A caller resolves a lost response through CredentialSession.
func (c *Client) HandoffCredentials(ctx context.Context, handle Handle, result string, input io.ReadCloser) (atc.RunCredentialSession, error) {
	if input == nil {
		return atc.RunCredentialSession{}, errors.New("credential input is required")
	}
	defer input.Close()
	endpoint, _ := url.Parse(c.base)
	host := endpoint.Hostname()
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	if endpoint.Scheme != "https" && !loopback {
		return atc.RunCredentialSession{}, errors.New("credential handoff requires HTTPS or a local loopback connection")
	}
	return c.credentialRequest(ctx, handle, result, input)
}

func (c *Client) credentialRequest(ctx context.Context, handle Handle, result string, input io.ReadCloser) (atc.RunCredentialSession, error) {
	var state atc.RunCredentialSession
	endpoint, err := c.templateEndpoint("v2", handle.Team, handle.Template)
	if err != nil {
		return state, err
	}
	if handle.Number < 1 || result == "" {
		return state, errors.New("a positive Run number and named result are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	method := http.MethodGet
	if input != nil {
		method = http.MethodPost
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint+"/runs/"+strconv.Itoa(handle.Number)+"/credentials/"+url.PathEscape(result), input)
	if err != nil {
		return state, err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if err = c.sendAdmission(request, &state, 4096, http.StatusOK); err != nil {
		return state, err
	}
	if state.RunID < 1 || state.Result != result {
		return atc.RunCredentialSession{}, errors.New("JetBridge returned a different credential session")
	}
	switch state.Status {
	case "waiting", "available", "claimed", "ready":
		return state, nil
	default:
		return atc.RunCredentialSession{}, errors.New("JetBridge returned an invalid credential session state")
	}
}
