package imageresolvertesting

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	ociregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type Request struct {
	Method       string
	Path         string
	HasBasicAuth bool
}

type Registry struct {
	server  *httptest.Server
	handler http.Handler

	mutex       sync.Mutex
	requests    []Request
	requireAuth bool
	username    string
	password    string
}

func NewRegistry() *Registry {
	registry := &Registry{
		handler: ociregistry.New(ociregistry.Logger(log.New(io.Discard, "", 0))),
	}
	registry.server = httptest.NewServer(http.HandlerFunc(registry.serveHTTP))
	return registry
}

func (registry *Registry) Host() string {
	return strings.TrimPrefix(registry.server.URL, "http://")
}

func (registry *Registry) Push(repository, tag string) (string, error) {
	ref, err := name.ParseReference(
		fmt.Sprintf("%s/%s:%s", registry.Host(), repository, tag),
		name.Insecure,
	)
	if err != nil {
		return "", fmt.Errorf("parse image reference: %w", err)
	}

	image := mutate.Annotations(empty.Image, map[string]string{
		"org.opencontainers.image.ref.name": repository + ":" + tag,
	}).(v1.Image)
	image = mutate.MediaType(image, types.OCIManifestSchema1)
	if err := remote.Write(ref, image); err != nil {
		return "", fmt.Errorf("push image: %w", err)
	}

	descriptor, err := remote.Head(ref)
	if err != nil {
		return "", fmt.Errorf("resolve pushed image: %w", err)
	}
	return descriptor.Digest.String(), nil
}

func (registry *Registry) RequireBasicAuth(username, password string) {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()
	registry.requireAuth = true
	registry.username = username
	registry.password = password
}

func (registry *Registry) DrainRequests() []Request {
	registry.mutex.Lock()
	defer registry.mutex.Unlock()

	requests := append([]Request(nil), registry.requests...)
	registry.requests = nil
	return requests
}

func (registry *Registry) Close() {
	registry.server.Close()
}

func (registry *Registry) serveHTTP(w http.ResponseWriter, request *http.Request) {
	username, password, hasBasicAuth := request.BasicAuth()

	registry.mutex.Lock()
	registry.requests = append(registry.requests, Request{
		Method:       request.Method,
		Path:         request.URL.Path,
		HasBasicAuth: hasBasicAuth,
	})
	requireAuth := registry.requireAuth
	expectedUsername := registry.username
	expectedPassword := registry.password
	registry.mutex.Unlock()

	if requireAuth && (!hasBasicAuth || username != expectedUsername || password != expectedPassword) {
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	registry.handler.ServeHTTP(w, request)
}
