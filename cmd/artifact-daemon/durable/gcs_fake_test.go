package durable_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// fakeGCS is a minimal Cloud Storage JSON+media endpoint.
//
// Same reasoning as fakeS3: the point is to run the real client library over
// the real wire protocol. A hand-written double of storage.Client would prove
// only that the double behaves as written.
type fakeGCS struct {
	srv *httptest.Server

	mu         sync.Mutex
	objects    map[string][]byte
	generation int64
	gens       map[string]int64
	failStatus int

	// listPrefix records what the last List asked the server to filter by, so
	// a test can prove the filter happens server-side rather than by the
	// client discarding rows it should never have fetched.
	listPrefix string
	listed     bool
}

func newFakeGCS(t *testing.T) *fakeGCS {
	t.Helper()

	f := &fakeGCS{objects: map[string][]byte{}, gens: map[string]int64{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeGCS) URL() string { return f.srv.URL }

func (f *fakeGCS) fail(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failStatus = status
}

func (f *fakeGCS) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]

	return ok
}

func (f *fakeGCS) store(t *testing.T, limit int64) durable.Store {
	t.Helper()

	return f.storeWithPrefix(t, "", limit)
}

func (f *fakeGCS) storeWithPrefix(t *testing.T, prefix string, limit int64) durable.Store {
	t.Helper()

	store, err := durable.NewGCS(t.Context(), durable.GCSConfig{
		Bucket:   "artifacts",
		Prefix:   prefix,
		Endpoint: f.URL(),
		Limit:    limit,
	})
	if err != nil {
		t.Fatalf("NewGCS: %v", err)
	}

	return store
}

func (f *fakeGCS) objectJSON(name string, body []byte, gen int64) map[string]any {
	return map[string]any{
		"kind":       "storage#object",
		"name":       name,
		"bucket":     "artifacts",
		"size":       strconv.Itoa(len(body)),
		"generation": strconv.FormatInt(gen, 10),
	}
}

// serve implements the handful of routes the client actually uses.
//
// Note that the client is not purely a JSON-API client: it uploads and reads
// metadata over the JSON API, but fetches object bodies over the XML API at
// /<bucket>/<object>. A fake that only served the JSON routes accepted every
// write and missed every read.
func (f *fakeGCS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	failStatus := f.failStatus
	f.mu.Unlock()

	if failStatus != 0 {
		http.Error(w, "injected failure", failStatus)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/"):
		f.upload(w, r)
	case strings.Contains(r.URL.Path, "/o/"):
		f.object(w, r)
	case strings.HasSuffix(r.URL.Path, "/o"):
		f.list(w, r)
	default:
		f.media(w, r)
	}
}

func (f *fakeGCS) upload(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}

	// A multipart upload wraps the payload in MIME parts; the last part is the
	// media. Splitting on the boundary is enough for a fake.
	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart/") {
		name, body = parseMultipart(ct, body)
	}

	f.mu.Lock()
	f.generation++
	gen := f.generation
	f.objects[name] = body
	f.gens[name] = gen
	f.mu.Unlock()

	writeJSON(w, f.objectJSON(name, body, gen))
}

func (f *fakeGCS) object(w http.ResponseWriter, r *http.Request) {
	idx := strings.Index(r.URL.Path, "/o/")
	name, err := decodePath(r.URL.Path[idx+3:])
	if err != nil {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	body, ok := f.objects[name]
	gen := f.gens[name]
	f.mu.Unlock()

	switch r.Method {
	case http.MethodDelete:
		if !ok {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		f.mu.Lock()
		delete(f.objects, name)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case http.MethodGet:
		if !ok {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
			return
		}
		writeJSON(w, f.objectJSON(name, body, gen))

	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func (f *fakeGCS) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")

	f.mu.Lock()
	f.listPrefix = prefix
	f.listed = true
	items := []map[string]any{}
	for name, body := range f.objects {
		if strings.HasPrefix(name, prefix) {
			items = append(items, f.objectJSON(name, body, f.gens[name]))
		}
	}
	f.mu.Unlock()

	writeJSON(w, map[string]any{"kind": "storage#objects", "items": items})
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// decodePath undoes the percent-encoding the client applies to object names.
func decodePath(raw string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '%' || i+2 >= len(raw) {
			out.WriteByte(raw[i])
			continue
		}
		n, err := strconv.ParseUint(raw[i+1:i+3], 16, 8)
		if err != nil {
			return "", fmt.Errorf("bad escape: %w", err)
		}
		out.WriteByte(byte(n))
		i += 2
	}

	return out.String(), nil
}

// parseMultipart pulls the object name out of the JSON part and the payload
// out of the media part.
func parseMultipart(contentType string, body []byte) (string, []byte) {
	_, params, found := strings.Cut(contentType, "boundary=")
	if !found {
		return "", body
	}
	boundary := "--" + strings.Trim(params, `"`)

	parts := strings.Split(string(body), boundary)
	var name string
	var media []byte

	for _, part := range parts {
		_, content, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		content = strings.TrimSuffix(content, "\r\n")

		if strings.Contains(part, "application/json") {
			var meta struct {
				Name string `json:"name"`
			}
			if json.Unmarshal([]byte(content), &meta) == nil {
				name = meta.Name
			}
			continue
		}
		media = []byte(content)
	}

	return name, media
}

// media serves the XML-API read: GET /<bucket>/<object>.
func (f *fakeGCS) media(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	_, name, found := strings.Cut(name, "/")
	if !found || r.Method != http.MethodGet {
		http.Error(w, "unsupported: "+r.URL.Path, http.StatusNotFound)
		return
	}

	decoded, err := decodePath(name)
	if err != nil {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	body, ok := f.objects[decoded]
	gen := f.gens[decoded]
	f.mu.Unlock()

	if !ok {
		http.Error(w, "NoSuchKey", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("x-goog-generation", strconv.FormatInt(gen, 10))
	_, _ = w.Write(body)
}

// lastListPrefix reports the prefix the client sent on its most recent list.
func (f *fakeGCS) lastListPrefix(t *testing.T) string {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.listed {
		t.Fatal("no list request reached the server")
	}

	return f.listPrefix
}
