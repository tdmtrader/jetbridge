package steps

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	frames "github.com/moby/spdystream/spdy"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Forward real Kubernetes exec upgrades over a private mutually authenticated
// TLS listener. Capture only SPDY bodies, never credentials or TLS secrets.
// Neither commands nor responses are supplied by this observer.
type volumeWireRoute struct {
	config    *rest.Config
	client    *kubernetes.Clientset
	server    *http.Server
	mu        sync.Mutex
	allowed   map[string]bool
	exchanges []*volumeWireExchange
	handlers  sync.WaitGroup
	closing   bool
	served    chan error
	checked   int
}

type volumeWireExchange struct {
	io.ReadWriteCloser
	query            url.Values
	path             string
	mu               sync.Mutex
	sent, received   []byte
	active           int
	closed, overflow bool
	done             chan struct{}
	once             sync.Once
}

func (w *volumeWireExchange) finish() {
	if w.closed && w.active == 0 {
		w.once.Do(func() { close(w.done) })
	}
}
func (w *volumeWireExchange) Read(p []byte) (int, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, net.ErrClosed
	}
	w.active++
	w.mu.Unlock()
	n, err := w.ReadWriteCloser.Read(p)
	w.mu.Lock()
	if len(w.received)+n <= 1<<20 {
		w.received = append(w.received, p[:n]...)
	} else {
		w.overflow = true
	}
	w.active--
	w.finish()
	w.mu.Unlock()
	return n, err
}
func (w *volumeWireExchange) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, net.ErrClosed
	}
	w.active++
	w.mu.Unlock()
	n, err := w.ReadWriteCloser.Write(p)
	w.mu.Lock()
	if len(w.sent)+n <= 1<<20 {
		w.sent = append(w.sent, p[:n]...)
	} else {
		w.overflow = true
	}
	w.active--
	w.finish()
	w.mu.Unlock()
	return n, err
}
func (w *volumeWireExchange) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	err := w.ReadWriteCloser.Close()
	w.mu.Lock()
	w.finish()
	w.mu.Unlock()
	return err
}

func newVolumeWireRoute(rec *brine.Recorder, upstream *rest.Config, namespace string) (*volumeWireRoute, error) {
	target, err := url.Parse(upstream.Host)
	if err != nil || target.Scheme != "https" || upstream.Insecure {
		return nil, fmt.Errorf("wire route requires verified HTTPS upstream")
	}
	material, err := mintMTLSMaterial("brine-volume-wire", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(material.serverCert, material.serverKey)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(material.caPEM) {
		return nil, fmt.Errorf("wire CA could not be loaded")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &volumeWireRoute{allowed: map[string]bool{}, served: make(chan error, 1)}
	// Every failure is collected rather than raised at the first one: the
	// listener, the served goroutine, the hijacked exchanges and the handler
	// group are four separate things to release, and stopping at the first
	// one that will not release leaves the other three running.
	TrackDisposer(rec, "the volume wire route", func() error {
		r.mu.Lock()
		r.closing = true
		r.mu.Unlock()
		var failures []error
		if r.server != nil {
			if err := r.server.Close(); err != nil {
				failures = append(failures, err)
			}
			select {
			case err := <-r.served:
				if err != nil && !errors.Is(err, http.ErrServerClosed) {
					failures = append(failures, err)
				}
			case <-time.After(10 * time.Second):
				failures = append(failures, errors.New("wire server did not stop"))
			}
		} else if err := releasedIfAlreadyClosed(listener.Close()); err != nil {
			failures = append(failures, err)
		}
		r.mu.Lock()
		exchanges := append([]*volumeWireExchange(nil), r.exchanges...)
		r.mu.Unlock()
		for _, e := range exchanges {
			if err := releasedIfAlreadyClosed(e.Close()); err != nil {
				failures = append(failures, err)
			}
		}
		done := make(chan struct{})
		go func() { r.handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			failures = append(failures, errors.New("wire proxy handlers did not finish"))
		}
		return errors.Join(failures...)
	})
	upConfig := rest.CopyConfig(upstream)
	upConfig.NextProtos = []string{"http/1.1"}
	transport, err := rest.TransportFor(upConfig)
	if err != nil {
		return nil, err
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.Host = target.Host
			p.Out.Header.Del("Authorization")
		},
		Transport: transport,
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode != http.StatusSwitchingProtocols {
				return nil
			}
			body, ok := resp.Body.(io.ReadWriteCloser)
			if !ok {
				return fmt.Errorf("real upgrade has no duplex body")
			}
			e := &volumeWireExchange{ReadWriteCloser: body, query: resp.Request.URL.Query(), path: resp.Request.URL.Path, done: make(chan struct{})}
			r.mu.Lock()
			r.exchanges = append(r.exchanges, e)
			r.mu.Unlock()
			resp.Body = e
			return nil
		},
	}
	prefix := "/api/v1/namespaces/" + namespace + "/pods/"
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		if r.closing {
			r.mu.Unlock()
			http.Error(w, "route closed", http.StatusServiceUnavailable)
			return
		}
		r.handlers.Add(1)
		r.mu.Unlock()
		defer r.handlers.Done()
		name := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, prefix), "/exec")
		r.mu.Lock()
		allowed := r.allowed[name]
		r.mu.Unlock()
		if req.Method != http.MethodPost || req.URL.Path != prefix+name+"/exec" || !allowed {
			http.Error(w, "outside owned exec route", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, req)
	})
	r.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, TLSConfig: &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, NextProtos: []string{"http/1.1"},
	}}
	tlsListener := tls.NewListener(listener, r.server.TLSConfig)
	go func() { r.served <- r.server.Serve(tlsListener) }()
	r.config = rest.AnonymousClientConfig(upstream)
	r.config.Host = "https://" + listener.Addr().String()
	r.config.Proxy = nil
	r.config.WrapTransport = nil
	r.config.Transport = nil
	r.config.TLSClientConfig = rest.TLSClientConfig{CAData: material.caPEM, CertData: material.clientCert, KeyData: material.clientKey, NextProtos: []string{"http/1.1"}}
	r.client, err = kubernetes.NewForConfig(r.config)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (r *volumeWireRoute) allowPod(name string) {
	r.mu.Lock()
	r.allowed[name] = true
	r.mu.Unlock()
}

func decodeWireFrames(data []byte) ([]frames.Frame, bool, error) {
	reader := bytes.NewReader(data)
	framer, err := frames.NewFramer(io.Discard, reader)
	if err != nil {
		return nil, false, err
	}
	var result []frames.Frame
	for {
		offset := len(data) - reader.Len()
		frame, err := framer.ReadFrame()
		if errors.Is(err, io.EOF) && offset == len(data) {
			return result, false, nil
		}
		if err != nil {
			tail := data[offset:]
			// A closing connection may end part-way through a control frame.
			// Callers may accept this only after the selected DATA stream FIN.
			if (errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)) && len(tail) >= 2 && tail[0] == 0x80 && tail[1] == 0x03 {
				return result, true, nil
			}
			return nil, false, fmt.Errorf("incomplete or invalid data frame at %d of %d: %w", offset, len(data), err)
		}
		result = append(result, frame)
	}
}

// payload requires a complete artifact DATA stream. A truncated closing control
// frame is harmless only after this stream's FIN; truncated DATA always fails.
func wirePayload(sent, received []byte, kind string) ([]byte, error) {
	client, _, err := decodeWireFrames(sent)
	if err != nil {
		return nil, fmt.Errorf("client frames: %w", err)
	}
	server, _, err := decodeWireFrames(received)
	if err != nil {
		return nil, fmt.Errorf("server frames: %w", err)
	}
	var selected frames.StreamId
	for _, frame := range client {
		if syn, ok := frame.(*frames.SynStreamFrame); ok {
			for key, values := range syn.Headers {
				if strings.EqualFold(key, "streamType") && len(values) == 1 && values[0] == kind {
					if selected != 0 {
						return nil, fmt.Errorf("duplicate %s stream", kind)
					}
					selected = syn.StreamId
				}
			}
		}
	}
	if selected == 0 {
		return nil, fmt.Errorf("no declared %s stream", kind)
	}
	direction := server
	if kind == "stdin" {
		direction = client
	}
	var payload []byte
	finished := false
	for _, frame := range direction {
		if data, ok := frame.(*frames.DataFrame); ok && data.StreamId == selected {
			if finished {
				return nil, fmt.Errorf("DATA after %s FIN", kind)
			}
			payload = append(payload, data.Data...)
			finished = data.Flags&frames.DataFlagFin != 0
		}
	}
	if !finished {
		return nil, fmt.Errorf("%s has no FIN", kind)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("%s payload is empty", kind)
	}
	return payload, nil
}

func (e *volumeWireExchange) payload(kind string) ([]byte, error) {
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("wire exchange did not close")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.overflow {
		return nil, fmt.Errorf("wire capture exceeded 1 MiB fixture bound")
	}
	return wirePayload(e.sent, e.received, kind)
}

// take binds each operation to its independently declared route and exact
// request options, and checks the number of actual upgrades since the last
// operation. The caller supplies bytes independently of these captures.
func (r *volumeWireRoute) take(bindings []volumeExecBinding, members, kinds []string) ([][]byte, error) {
	r.mu.Lock()
	pending := append([]*volumeWireExchange(nil), r.exchanges[r.checked:]...)
	r.mu.Unlock()
	if len(pending) != len(bindings) {
		return nil, fmt.Errorf("wire transfer count: got %d, want %d", len(pending), len(bindings))
	}
	results := make([][]byte, len(bindings))
	for i, binding := range bindings {
		kind, member := kinds[i], members[i]
		want := url.Values{"container": {binding.container}, kind: {"true"}}
		if kind == "stdin" {
			want["command"] = expectedStreamInCommand(binding.mount, filepath.Join(binding.mount, member))
		} else {
			want["command"] = []string{"tar", "cf", "-", "-C", binding.mount, member}
		}
		route := "/api/v1/namespaces/" + binding.namespace + "/pods/" + binding.pod + "/exec"
		matched := -1
		for j, e := range pending {
			if e != nil && e.path == route && reflect.DeepEqual(e.query, want) {
				if matched >= 0 {
					return nil, fmt.Errorf("duplicate wire transfer to %s", route)
				}
				matched = j
			}
		}
		if matched < 0 {
			return nil, fmt.Errorf("no wire transfer to %s with options %v", route, want)
		}
		payload, err := pending[matched].payload(kind)
		if err != nil {
			return nil, err
		}
		results[i] = payload
		pending[matched] = nil
	}
	r.mu.Lock()
	r.checked += len(bindings)
	r.mu.Unlock()
	return results, nil
}

func (r *volumeWireRoute) requireAllChecked() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.checked == 0 || r.checked != len(r.exchanges) {
		return fmt.Errorf("checked %d of %d wire transfers", r.checked, len(r.exchanges))
	}
	return nil
}

func requireSameVolumeBytes(got, want []byte) error {
	if !bytes.Equal(got, want) {
		return fmt.Errorf("volume wire bytes differ: got %d bytes sha256=%x, want %d bytes sha256=%x", len(got), sha256.Sum256(got), len(want), sha256.Sum256(want))
	}
	return nil
}
