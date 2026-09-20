package artifactwire

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// streamHeaderTimeout bounds how long a daemon may take to say anything at all
// about a stream or a restore. The transfer itself is bounded by the caller's
// context, never by a whole-request timeout: http.Client.Timeout covers
// reading the entire body, which severs a long tar stream mid-read and
// surfaces as "unexpected EOF" at the consumer.
const streamHeaderTimeout = 30 * time.Second

// restoreDialTimeout bounds connecting for a restore, so a black-holed pod IP
// cannot eat a whole warm budget before the next candidate is tried.
const restoreDialTimeout = 3 * time.Second

// refusalBodyLimit is how much of a refusal's body is kept for the error.
const refusalBodyLimit = 512

// Client reaches artifact daemons. One Client serves every daemon in a
// cluster: the port and the TLS triple are cluster-wide, and the host is
// supplied per call because every caller already holds it as a loop variable
// or a resolved node IP.
type Client struct {
	scheme string
	port   int

	// misconfigured, when set, is why this client can reach nothing: the
	// operator asked for mTLS and the triple could not be loaded. Every
	// operation returns it. This is deliberately not a plaintext fallback: a
	// client that quietly dropped to http when its certificate was missing
	// would be the unauthenticated path the certificate exists to close.
	misconfigured error

	// plain carries requests whose answer is a status: register, mirror,
	// probes, delete, capture class. It has no whole-request timeout; the
	// caller's context bounds it.
	plain *http.Client
	// streaming carries tar bodies in either direction.
	streaming *http.Client
	// restore is streaming with a short dial, for the durable tier.
	restore *http.Client
}

// NewClient builds a Client for daemons on port with the given TLS triple. A
// zero port means DefaultPort. When the triple is configured its files are
// loaded here, once, and a triple that cannot be loaded is an error rather
// than a silent plaintext client: the caller decides what to do about it.
func NewClient(port int, tlsTriple TLS) (*Client, error) {
	if port == 0 {
		port = DefaultPort
	}
	scheme := "http"
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsTriple.Configured() {
		tlsConfig, err := tlsTriple.load()
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = tlsConfig
		scheme = "https"
	}

	streaming := transport.Clone()
	streaming.ResponseHeaderTimeout = streamHeaderTimeout

	restore := streaming.Clone()
	restore.DialContext = (&net.Dialer{Timeout: restoreDialTimeout}).DialContext

	return &Client{
		scheme:    scheme,
		port:      port,
		plain:     &http.Client{Transport: transport},
		streaming: &http.Client{Transport: streaming},
		restore:   &http.Client{Transport: restore},
	}, nil
}

// Misconfigured is a Client for a daemon that was asked for over mTLS when
// the triple could not be loaded. It keeps the https scheme, so the shell
// prelude still renders what the deployment asked for, and refuses every
// request with reason, so the misconfiguration surfaces at the first call
// naming the certificate rather than as a plaintext request the daemon
// turns away.
func Misconfigured(port int, reason error) *Client {
	if port == 0 {
		port = DefaultPort
	}
	return &Client{scheme: "https", port: port, misconfigured: reason}
}

// Scheme is "https" when the client presents a certificate, else "http".
func (c *Client) Scheme() string { return c.scheme }

// Port is the daemon port this client dials.
func (c *Client) Port() int { return c.port }

// URL is the address of path on the daemon at host. Keys are appended raw:
// the daemon validates them, and a key such as "steps/x" is meant to keep its
// slash.
func (c *Client) URL(host, path string) string {
	return c.scheme + "://" + net.JoinHostPort(host, strconv.Itoa(c.port)) + path
}

// Register names an alias on the daemon at host. A 201 is success; anything
// else is a Refusal, and a 404 is the daemon saying the path is not on its
// node, which a caller fanning out over daemons treats as "try the next".
func (c *Client) Register(ctx context.Context, host string, req RegisterRequest) error {
	resp, err := c.postJSON(ctx, c.plain, host, Register.Path, req)
	if err != nil {
		return err
	}
	return expect(resp, http.StatusCreated)
}

// Mirror asks the daemon at host to copy key to its peers. A 202 is success:
// the mirror runs after this returns.
func (c *Client) Mirror(ctx context.Context, host, key string) error {
	resp, err := c.postJSON(ctx, c.plain, host, Mirror.Path, MirrorRequest{Key: key})
	if err != nil {
		return err
	}
	return expect(resp, http.StatusAccepted)
}

// Restore asks the daemon at host to pull an object out of the durable tier
// and register it locally, so that it may then be read from that daemon as if
// it had been there all along. A 200 or 201 is success; a 404 is the shared
// bucket's answer, not this pod's.
func (c *Client) Restore(ctx context.Context, host string, req DurableRestoreRequest) error {
	resp, err := c.postJSON(ctx, c.restore, host, DurableRestore.Path, req)
	if err != nil {
		return err
	}
	return expect(resp, http.StatusOK, http.StatusCreated)
}

// ResourceCacheProbe is what one daemon said about a resource cache key.
type ResourceCacheProbe struct {
	// Found means these bytes are on that node's disk right now, which is
	// what makes the daemon worth binding to. The durable tier is not
	// consulted: every daemon sees the same bucket, and consulting it would
	// make every one of them say yes.
	Found bool
	// DurableCapable is read on every status, not only 200: a daemon that
	// answers 404 for this key is still the daemon that can warm it.
	DurableCapable bool
}

// HeadResourceCache asks the daemon at host whether it holds a resource cache
// key on local disk. A status is an answer, so only transport failure errors.
func (c *Client) HeadResourceCache(ctx context.Context, host, key string) (ResourceCacheProbe, error) {
	resp, err := c.do(ctx, c.plain, http.MethodHead, host, ResourceCachesPrefix+key, nil, "")
	if err != nil {
		return ResourceCacheProbe{}, err
	}
	resp.Body.Close()
	return ResourceCacheProbe{
		Found:          resp.StatusCode == http.StatusOK,
		DurableCapable: resp.Header.Get(DurableTierHeader) != "",
	}, nil
}

// HeadArtifact asks the daemon at host whether it holds key. A status is an
// answer, so only transport failure errors.
func (c *Client) HeadArtifact(ctx context.Context, host, key string) (bool, error) {
	resp, err := c.do(ctx, c.plain, http.MethodHead, host, ArtifactsPrefix+key, nil, "")
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// StreamOut opens the tar of key from the daemon at host. The caller closes
// the body. A 404 is ErrNotFound.
func (c *Client) StreamOut(ctx context.Context, host, key string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, c.streaming, http.MethodGet, host, ArtifactsPrefix+key, nil, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, refusalOf(resp)
	}
	return resp.Body, nil
}

// StreamIn sends a tar to be extracted under key on the daemon at host. A 201
// is success.
func (c *Client) StreamIn(ctx context.Context, host, key string, body io.Reader) error {
	resp, err := c.do(ctx, c.streaming, http.MethodPut, host, StreamInPrefix+key, body, "application/octet-stream")
	if err != nil {
		return err
	}
	return expect(resp, http.StatusCreated)
}

// Delete removes key from the daemon at host. A 2xx is done; a 404 is also
// done, since the bytes are gone either way; a 409 is ErrHeld, and the caller
// that could come back and try again must not forget the key.
func (c *Client) Delete(ctx context.Context, host, key string) error {
	resp, err := c.do(ctx, c.plain, http.MethodDelete, host, ArtifactsPrefix+key, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return refusalOf(resp)
}

// CaptureClass asks the daemon at host what the output ledger says about a
// step handle. A daemon that does not answer 200 is an error and not
// "unmanaged": every caller is about to do something destructive, and an
// unreadable ledger is not an empty one.
func (c *Client) CaptureClass(ctx context.Context, host, handle string) (CaptureClassResponse, error) {
	resp, err := c.do(ctx, c.plain, http.MethodGet, host, CaptureHeldStepsPrefix+handle, nil, "")
	if err != nil {
		return CaptureClassResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CaptureClassResponse{}, refusalOf(resp)
	}
	var answer CaptureClassResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&answer); err != nil {
		return CaptureClassResponse{}, fmt.Errorf("decoding the capture class from %s: %w", host, err)
	}
	if answer.Class == "" {
		return CaptureClassResponse{}, fmt.Errorf("the daemon on %s named no class for %s", host, handle)
	}
	return answer, nil
}

func (c *Client) postJSON(ctx context.Context, via *http.Client, host, path string, body any) (*http.Response, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding the %s body: %w", path, err)
	}
	return c.do(ctx, via, http.MethodPost, host, path, bytes.NewReader(encoded), "application/json")
}

func (c *Client) do(ctx context.Context, via *http.Client, method, host, path string, body io.Reader, contentType string) (*http.Response, error) {
	url := c.URL(host, path)
	if c.misconfigured != nil {
		return nil, fmt.Errorf("%s %s: artifact daemon mTLS: %w", method, url, c.misconfigured)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := via.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, url, err)
	}
	return resp, nil
}

// expect closes the body and returns nil when the status is one of the wanted
// ones, else the Refusal.
func expect(resp *http.Response, wanted ...int) error {
	defer resp.Body.Close()
	for _, status := range wanted {
		if resp.StatusCode == status {
			return nil
		}
	}
	return refusalOf(resp)
}

// refusalOf reads a bounded body into a Refusal and closes the response.
func refusalOf(resp *http.Response) *Refusal {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, refusalBodyLimit))
	return &Refusal{
		Method: resp.Request.Method,
		URL:    resp.Request.URL.String(),
		Status: resp.StatusCode,
		Body:   string(bytes.TrimSpace(body)),
	}
}

// WithTransport replaces the transport behind every request this client
// makes. It exists for tests that route host:port pairs to test servers; it is
// not a configuration surface, and it keeps the scheme and port the client
// was built with.
func (c *Client) WithTransport(rt http.RoundTripper) *Client {
	c.plain = &http.Client{Transport: rt}
	c.streaming = &http.Client{Transport: rt}
	c.restore = &http.Client{Transport: rt}
	return c
}
