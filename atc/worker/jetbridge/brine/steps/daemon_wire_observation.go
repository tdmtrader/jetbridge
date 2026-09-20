package steps

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// Capture bytes actually accepted by real TCP sockets. This never redirects a
// connection, parses a response, supplies an error, or rewrites a request.
type daemonWireObservation struct {
	sequence    atomic.Uint64
	mu          sync.Mutex
	connections []*daemonWireConnection
}
type daemonWireConnection struct {
	net.Conn
	mu      sync.Mutex
	written bytes.Buffer
	trace   *daemonWireObservation
	writes  []daemonWireWrite
}

type daemonWireWrite struct {
	offset   int
	sequence uint64
}

func (c *daemonWireConnection) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.mu.Lock()
	if n > 0 {
		c.writes = append(c.writes, daemonWireWrite{offset: c.written.Len(), sequence: c.trace.sequence.Add(1)})
		c.written.Write(p[:n])
	}
	c.mu.Unlock()
	return n, err
}

// Production constructors clone DefaultTransport. Install the observation only
// while constructing their clients, then restore it before any I/O. Brine's
// scenario adapter executes these setup steps serially. No production seam is
// added solely for the test.
func observeDaemonConstruction(addresses map[string]bool, construct func()) (*daemonWireObservation, error) {
	return observeDaemonTraffic(addresses, construct)
}

// observeDaemonTraffic also covers actions that lazily construct HTTP clients.
// Calls are serial in the scenario adapter; the default is restored on panic too.
func observeDaemonTraffic(addresses map[string]bool, action func()) (*daemonWireObservation, error) {
	original, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("expected standard HTTP transport, got %T", http.DefaultTransport)
	}
	trace := new(daemonWireObservation)
	transport := original.Clone()
	dial := transport.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil || !addresses[address] {
			return conn, err
		}
		observed := &daemonWireConnection{Conn: conn, trace: trace}
		trace.mu.Lock()
		trace.connections = append(trace.connections, observed)
		trace.mu.Unlock()
		return observed, nil
	}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = original }()
	action()
	return trace, nil
}

type daemonWireRequest struct {
	Address  string
	Method   string
	Path     string
	Body     []byte
	sequence uint64
}

// capturedRequests parses only bytes accepted by actual sockets. Keeping bodies
// here lets the mirror contract share the read/probe observer without supplying
// an HTTP response or replacing the real daemon.
func (t *daemonWireObservation) capturedRequests() ([]daemonWireRequest, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var requests []daemonWireRequest
	for _, conn := range t.connections {
		conn.mu.Lock()
		data := append([]byte(nil), conn.written.Bytes()...)
		writes := append([]daemonWireWrite(nil), conn.writes...)
		conn.mu.Unlock()
		raw := bytes.NewReader(data)
		reader := bufio.NewReader(raw)
		for {
			offset := len(data) - raw.Len() - reader.Buffered()
			req, err := http.ReadRequest(reader)
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("parse actual daemon request: %w", err)
			}
			body, err := io.ReadAll(req.Body)
			req.Body.Close()
			if err != nil {
				return nil, err
			}
			var sequence uint64
			for _, write := range writes {
				if write.offset > offset {
					break
				}
				sequence = write.sequence
			}
			if sequence == 0 {
				return nil, fmt.Errorf("request has no accepted write at offset %d", offset)
			}
			requests = append(requests, daemonWireRequest{
				sequence: sequence,
				Address:  conn.RemoteAddr().String(), Method: req.Method,
				Path: req.URL.RequestURI(), Body: body,
			})
		}
	}
	// Synchronous calls may alternate new alias sockets and a reused mirror
	// socket. Connection grouping is not request order. Preserve accepted-write
	// order across sockets; requests sharing one write keep their parse order.
	sort.SliceStable(requests, func(i, j int) bool { return requests[i].sequence < requests[j].sequence })
	return requests, nil
}

func (t *daemonWireObservation) requests() (map[string][]string, error) {
	captured, err := t.capturedRequests()
	if err != nil {
		return nil, err
	}
	requests := map[string][]string{}
	for _, req := range captured {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			return nil, fmt.Errorf("unexpected artifact read method %s", req.Method)
		}
		requests[req.Address] = append(requests[req.Address], req.Method+" "+req.Path)
	}
	return requests, nil
}
