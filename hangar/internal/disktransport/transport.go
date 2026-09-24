// Package disktransport owns the private wire protocol shared by the disk
// clients and server. It is not a storage capability exposed to command roots.
package disktransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/objectstore"
)

const IdentityHeader = "X-Hangar-Store-ID"
const ErrorTrailer = "X-Hangar-Read-Error"
const MaxControlBytes = 8 << 20

type Config struct {
	Endpoint  string
	StoreID   string
	TokenFile string
	CACert    string
	Timeout   time.Duration
}

type Transport struct {
	endpoint string
	id       string
	token    string
	client   *http.Client
}

func New(config Config) (*Transport, error) {
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("disk endpoint must be an HTTPS origin")
	}
	if err := hangar.Scope(config.StoreID).Validate(); err != nil {
		return nil, fmt.Errorf("disk identity: %w", err)
	}
	if config.Timeout <= 0 {
		return nil, fmt.Errorf("disk timeout must be positive")
	}
	key, err := os.ReadFile(config.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("read disk credential: %w", err)
	}
	token := strings.TrimSpace(string(key))
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, fmt.Errorf("disk credential must contain 32..4096 printable bytes")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if config.CACert != "" {
		pem, err := os.ReadFile(config.CACert)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid disk CA")
		}
		tlsConfig.RootCAs = pool
	}
	httpTransport := http.DefaultTransport.(*http.Transport).Clone()
	httpTransport.TLSClientConfig = tlsConfig
	// Credentials must never follow redirects to another origin or operation.
	client := &http.Client{Transport: httpTransport, Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	transport := &Transport{endpoint: strings.TrimSuffix(config.Endpoint, "/"), id: config.StoreID, token: token, client: client}
	// Readiness must prove that the configured credential reaches this exact
	// initialized store, not merely that the endpoint string parses.
	response, err := transport.Request(context.Background(), http.MethodGet, "identity", nil, nil, nil)
	if err != nil {
		httpTransport.CloseIdleConnections()
		return nil, err
	}
	if err := response.Body.Close(); err != nil {
		httpTransport.CloseIdleConnections()
		return nil, err
	}
	return transport, nil
}

func (t *Transport) Request(ctx context.Context, method, operation string, query url.Values, body io.Reader, headers http.Header) (*http.Response, error) {
	// HTTP owns its request body, but the object-store caller owns the source.
	// In particular, tree publication must still close and remove its scratch file.
	if body != nil {
		body = io.NopCloser(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.endpoint+"/v1/"+operation+"?"+query.Encode(), body)
	if err != nil {
		return nil, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set(IdentityHeader, t.id)
	response, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", objectstore.ErrInfrastructure, err)
	}
	if response.Header.Get(IdentityHeader) != t.id {
		_ = response.Body.Close()
		return nil, fmt.Errorf("%w: disk identity mismatch", hangar.ErrConflict)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		var message struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&message); err != nil {
			return nil, objectstore.ErrInfrastructure
		}
		return nil, DecodeError(message.Error)
	}
	return response, nil
}

func Decode(response *http.Response, result any) error {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxControlBytes+1))
	if err != nil {
		return fmt.Errorf("%w: %w", objectstore.ErrInfrastructure, err)
	}
	if len(body) > MaxControlBytes {
		return hangar.ErrLimitExceeded
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("%w: invalid disk response", hangar.ErrCorrupt)
	}
	return nil
}

func ErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, objectstore.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, objectstore.ErrUnauthorized):
		return http.StatusForbidden, "unauthorized"
	case errors.Is(err, objectstore.ErrPreconditionFailed):
		return http.StatusPreconditionFailed, "precondition"
	case errors.Is(err, hangar.ErrLimitExceeded):
		return http.StatusRequestEntityTooLarge, "limit"
	case errors.Is(err, hangar.ErrCorrupt):
		return http.StatusUnprocessableEntity, "corrupt"
	case errors.Is(err, hangar.ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusRequestTimeout, "timeout"
	default:
		return http.StatusServiceUnavailable, "unavailable"
	}
}
func DecodeError(code string) error {
	switch code {
	case "not_found":
		return objectstore.ErrNotFound
	case "unauthorized":
		return objectstore.ErrUnauthorized
	case "precondition":
		return objectstore.ErrPreconditionFailed
	case "limit":
		return hangar.ErrLimitExceeded
	case "corrupt":
		return hangar.ErrCorrupt
	case "conflict":
		return hangar.ErrConflict
	case "timeout":
		return context.DeadlineExceeded
	default:
		return objectstore.ErrInfrastructure
	}
}

func Query(bucket, key string) url.Values { return url.Values{"bucket": {bucket}, "key": {key}} }

type Body struct{ Response *http.Response }

func (b *Body) Read(p []byte) (int, error) {
	n, err := b.Response.Body.Read(p)
	if err == io.EOF {
		if code := b.Response.Trailer.Get(ErrorTrailer); code != "ok" {
			return n, DecodeError(code)
		}
	}
	return n, err
}
func (b *Body) Close() error { return b.Response.Body.Close() }
