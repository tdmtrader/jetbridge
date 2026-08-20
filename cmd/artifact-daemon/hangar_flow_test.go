package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
)

type strictGCSObject struct {
	name           string
	data           []byte
	metadata       map[string]string
	generation     int64
	metageneration int64
	created        time.Time
}

type strictGCSFake struct {
	mu              sync.Mutex
	baseURL         string
	object          *strictGCSObject
	pendingName     string
	pendingMetadata map[string]string
	nextGeneration  int64
	requests        []string
}

func newStrictGCSFake(t *testing.T) (*strictGCSFake, *httptest.Server) {
	t.Helper()
	fake := &strictGCSFake{nextGeneration: 1}
	server := httptest.NewServer(fake)
	fake.baseURL = server.URL
	t.Cleanup(server.Close)
	return fake, server
}

func (fake *strictGCSFake) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.requests = append(fake.requests, request.Method+" "+request.URL.String())

	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/upload/storage/v1/b/bucket/o":
		if request.URL.Query().Get("ifGenerationMatch") != "0" {
			http.Error(writer, "strict create precondition required", http.StatusBadRequest)
			return
		}
		uploadType := request.URL.Query().Get("uploadType")
		if uploadType != "resumable" && uploadType != "multipart" {
			http.Error(writer, "unsupported upload type", http.StatusBadRequest)
			return
		}
		name := request.URL.Query().Get("name")
		if fake.object != nil && fake.object.name == name {
			writeGCSError(writer, http.StatusPreconditionFailed, "object already exists")
			return
		}
		var attrs struct {
			Name     string            `json:"name"`
			Metadata map[string]string `json:"metadata"`
		}
		if uploadType == "multipart" {
			mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
			if err != nil || mediaType != "multipart/related" {
				http.Error(writer, "invalid multipart upload", http.StatusBadRequest)
				return
			}
			parts := multipart.NewReader(request.Body, parameters["boundary"])
			metadataPart, err := parts.NextPart()
			if err != nil || json.NewDecoder(metadataPart).Decode(&attrs) != nil {
				http.Error(writer, "invalid upload metadata", http.StatusBadRequest)
				return
			}
			mediaPart, err := parts.NextPart()
			if err != nil {
				http.Error(writer, "missing upload media", http.StatusBadRequest)
				return
			}
			data, err := io.ReadAll(mediaPart)
			if err != nil {
				http.Error(writer, "read upload media", http.StatusBadRequest)
				return
			}
			object := fake.commit(name, data, attrs.Metadata)
			writeGCSObject(writer, object)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&attrs); err != nil {
			http.Error(writer, "invalid upload metadata", http.StatusBadRequest)
			return
		}
		if attrs.Name != "" && attrs.Name != name {
			http.Error(writer, "object name mismatch", http.StatusBadRequest)
			return
		}
		fake.pendingName = name
		fake.pendingMetadata = cloneStringMap(attrs.Metadata)
		writer.Header().Set("Location", fake.baseURL+"/upload-session")
		writer.WriteHeader(http.StatusOK)

	case request.Method == http.MethodPut && request.URL.Path == "/upload-session":
		if fake.pendingName == "" {
			http.Error(writer, "no pending upload", http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "read upload", http.StatusBadRequest)
			return
		}
		object := fake.commit(fake.pendingName, data, fake.pendingMetadata)
		fake.pendingName = ""
		fake.pendingMetadata = nil
		writeGCSObject(writer, object)

	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.EscapedPath(), "/storage/v1/b/bucket/o/"):
		name, err := url.PathUnescape(strings.TrimPrefix(request.URL.EscapedPath(), "/storage/v1/b/bucket/o/"))
		if err != nil {
			http.Error(writer, "invalid object name", http.StatusBadRequest)
			return
		}
		generation, err := strconv.ParseInt(request.URL.Query().Get("generation"), 10, 64)
		if err != nil || fake.object == nil || fake.object.name != name || fake.object.generation != generation {
			writeGCSError(writer, http.StatusNotFound, "exact generation not found")
			return
		}
		if request.URL.Query().Get("alt") == "media" {
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.Header().Set("Content-Length", strconv.Itoa(len(fake.object.data)))
			writer.Header().Set("X-Goog-Generation", strconv.FormatInt(fake.object.generation, 10))
			writer.Header().Set("X-Goog-Metageneration", strconv.FormatInt(fake.object.metageneration, 10))
			writer.Header().Set("X-Goog-Stored-Content-Length", strconv.Itoa(len(fake.object.data)))
			_, _ = writer.Write(fake.object.data)
			return
		}
		writeGCSObject(writer, fake.object)

	default:
		http.Error(writer, fmt.Sprintf("unexpected GCS request: %s %s", request.Method, request.URL.String()), http.StatusBadRequest)
	}
}

func (fake *strictGCSFake) commit(name string, data []byte, metadata map[string]string) *strictGCSObject {
	object := &strictGCSObject{
		name: name, data: append([]byte(nil), data...), metadata: cloneStringMap(metadata),
		generation: fake.nextGeneration, metageneration: 1,
		created: time.Date(2026, 8, 20, 0, 0, int(fake.nextGeneration), 0, time.UTC),
	}
	fake.nextGeneration++
	fake.object = object
	return object
}

func writeGCSError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{"code": status, "message": message}})
}

func writeGCSObject(writer http.ResponseWriter, object *strictGCSObject) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"bucket": "bucket", "name": object.name,
		"generation":     strconv.FormatInt(object.generation, 10),
		"metageneration": strconv.FormatInt(object.metageneration, 10),
		"size":           strconv.Itoa(len(object.data)),
		"timeCreated":    object.created.Format(time.RFC3339Nano),
		"updated":        object.created.Format(time.RFC3339Nano),
		"metadata":       cloneStringMap(object.metadata),
	})
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func (fake *strictGCSFake) snapshot(t *testing.T) strictGCSObject {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.object == nil {
		t.Fatal("strict fake GCS has no object")
	}
	copy := *fake.object
	copy.data = append([]byte(nil), fake.object.data...)
	copy.metadata = cloneStringMap(fake.object.metadata)
	return copy
}

func (fake *strictGCSFake) restore(object strictGCSObject) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	copy := object
	copy.data = append([]byte(nil), object.data...)
	copy.metadata = cloneStringMap(object.metadata)
	fake.object = &copy
}

func rawHostileAwareTree(t *testing.T, producer string) []byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(producer, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(producer, "nested", "run.sh"), []byte("#!/bin/sh\nprintf exact\\n\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(producer, "literal [x]"), []byte("payload\x00bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(producer, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested/run.sh", filepath.Join(producer, "latest")); err != nil {
		t.Fatal(err)
	}

	var raw bytes.Buffer
	tarWriter := tar.NewWriter(&raw)
	writeHeader := func(header *tar.Header, content []byte) {
		t.Helper()
		header.Uid, header.Gid = 1234, 5678
		header.Uname, header.Gname = "producer", "builders"
		header.ModTime = time.Unix(123456789, 0)
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(content) > 0 {
			if _, err := tarWriter.Write(content); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeHeader(&tar.Header{Name: "nested", Typeflag: tar.TypeDir, Mode: 0700}, nil)
	writeHeader(&tar.Header{Name: "nested/run.sh", Typeflag: tar.TypeReg, Mode: 0711, Size: 25}, []byte("#!/bin/sh\nprintf exact\\n\n"))
	writeHeader(&tar.Header{Name: "literal [x]", Typeflag: tar.TypeReg, Mode: 0666, Size: 14}, []byte("payload\x00bytes\n"))
	writeHeader(&tar.Header{Name: "empty", Typeflag: tar.TypeDir, Mode: 0700}, nil)
	writeHeader(&tar.Header{Name: "latest", Typeflag: tar.TypeSymlink, Mode: 0700, Linkname: "./nested/run.sh"}, nil)
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestHangarDaemonStrictGCSFullTreeFlowFailsClosed(t *testing.T) {
	producer := t.TempDir()
	raw := rawHostileAwareTree(t, producer)
	fake, fakeServer := newStrictGCSFake(t)
	client, err := hangar.NewStorageClient(context.Background(), fakeServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store, err := hangar.NewGCSStore(client, hangar.GCSConfig{
		Bucket: "bucket", Prefix: "deployment/blue", ScratchDir: t.TempDir(),
		ReadTimeout: time.Second, WriteTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, _, key := newHangarTestServer(t, store)
	handler := server.Handler(WithTLS())

	publish := httptest.NewRequest(http.MethodPost, "/hangar/v1/scopes/ci/trees", bytes.NewReader(raw))
	publish.Header.Set("Content-Type", "application/x-tar")
	publish.TLS = verifiedTestTLSState()
	published := httptest.NewRecorder()
	handler.ServeHTTP(published, publish)
	if published.Code != http.StatusCreated {
		fake.mu.Lock()
		requests := append([]string(nil), fake.requests...)
		fake.mu.Unlock()
		t.Fatalf("publish status=%d body=%q GCS requests=%v", published.Code, published.Body.String(), requests)
	}
	var attributes hangar.TreeAttributes
	if err := json.Unmarshal(published.Body.Bytes(), &attributes); err != nil {
		t.Fatal(err)
	}
	if err := attributes.Ref.Validate(); err != nil {
		t.Fatalf("published ref is invalid: %v", err)
	}
	if err := os.RemoveAll(producer); err != nil {
		t.Fatal(err)
	}

	digestHex := strings.TrimPrefix(string(attributes.Ref.Digest), "sha256:")
	open := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/hangar/v1/scopes/%s/trees/sha256/%s/generations/%d", attributes.Ref.Scope, digestHex, attributes.Ref.Generation), nil)
	open.TLS = verifiedTestTLSState()
	opened := httptest.NewRecorder()
	handler.ServeHTTP(opened, open)
	if opened.Code != http.StatusOK {
		t.Fatalf("exact generation GET status=%d body=%q", opened.Code, opened.Body.String())
	}
	assertCanonicalTreeArchive(t, opened.Body.Bytes())

	materialize := func(handle, volume string) *httptest.ResponseRecorder {
		t.Helper()
		signer, signErr := hangar.NewGrantSigner(key, time.Minute, nil)
		if signErr != nil {
			t.Fatal(signErr)
		}
		token, signErr := signer.Sign(attributes.Ref, handle, volume)
		if signErr != nil {
			t.Fatal(signErr)
		}
		body, marshalErr := json.Marshal(hangarMaterializationRequest{Items: []hangarMaterializationItem{{
			Ref: attributes.Ref, Handle: handle, Volume: volume, Grant: "Bearer " + token,
		}}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hangar/v1/materializations", bytes.NewReader(body)))
		return recorder
	}

	success := materialize("consumer", "input")
	if success.Code != http.StatusNoContent || success.Body.Len() != 0 {
		t.Fatalf("materialize status=%d body=%q", success.Code, success.Body.String())
	}
	assertMaterializedTree(t, filepath.Join(server.storagePath, "steps", "consumer", "input"), attributes.Ref)

	baseline := fake.snapshot(t)
	failures := []struct {
		name   string
		mutate func()
	}{
		{name: "absent", mutate: func() {
			fake.mu.Lock()
			fake.object = nil
			fake.mu.Unlock()
		}},
		{name: "corrupt stored bytes", mutate: func() {
			fake.mu.Lock()
			fake.object.data[len(fake.object.data)/2] ^= 0xff
			fake.mu.Unlock()
		}},
		{name: "corrupt metadata", mutate: func() {
			fake.mu.Lock()
			fake.object.metadata["concourse-uncompressed-bytes"] = "1"
			fake.object.metageneration++
			fake.mu.Unlock()
		}},
		{name: "replacement generation", mutate: func() {
			fake.mu.Lock()
			fake.object.generation = fake.nextGeneration
			fake.nextGeneration++
			fake.mu.Unlock()
		}},
	}
	if len(failures) == 0 {
		t.Fatal("failure injection matrix is empty")
	}
	for index, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			fake.restore(baseline)
			failure.mutate()
			handle := fmt.Sprintf("failed-%d", index)
			destination := filepath.Join(server.storagePath, "steps", handle, "input")
			response := materialize(handle, "input")
			if response.Code >= 200 && response.Code < 300 {
				t.Fatalf("injected backend failure returned %d", response.Code)
			}
			if _, err := os.Lstat(destination); !os.IsNotExist(err) {
				t.Fatalf("failure left partial or completed destination: %v", err)
			}
		})
	}
}

func verifiedTestTLSState() *tls.ConnectionState {
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
}

func assertCanonicalTreeArchive(t *testing.T, archive []byte) {
	t.Helper()
	type entry struct {
		kind byte
		mode int64
		body string
		link string
	}
	want := map[string]entry{
		"empty":         {kind: tar.TypeDir, mode: 0755},
		"latest":        {kind: tar.TypeSymlink, mode: 0777, link: "nested/run.sh"},
		"literal [x]":   {kind: tar.TypeReg, mode: 0644, body: "payload\x00bytes\n"},
		"nested":        {kind: tar.TypeDir, mode: 0755},
		"nested/run.sh": {kind: tar.TypeReg, mode: 0755, body: "#!/bin/sh\nprintf exact\\n\n"},
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	seen := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		expected, found := want[header.Name]
		if !found {
			t.Fatalf("unexpected canonical entry %q", header.Name)
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != expected.kind || header.Mode != expected.mode || string(body) != expected.body || header.Linkname != expected.link {
			t.Fatalf("canonical entry %q = type=%d mode=%#o body=%q link=%q", header.Name, header.Typeflag, header.Mode, body, header.Linkname)
		}
		delete(want, header.Name)
		seen++
	}
	if seen == 0 || len(want) != 0 {
		t.Fatalf("canonical archive matched %d entries; missing=%v", seen, want)
	}
}

func assertMaterializedTree(t *testing.T, root string, ref hangar.TreeRef) {
	t.Helper()
	assertPath := func(relative string, kind os.FileMode, mode os.FileMode, content string) {
		t.Helper()
		path := filepath.Join(root, relative)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeType != kind || info.Mode().Perm() != mode {
			t.Fatalf("%s mode=%v, want kind=%v permissions=%#o", relative, info.Mode(), kind, mode)
		}
		if kind == 0 {
			body, err := os.ReadFile(path)
			if err != nil || string(body) != content {
				t.Fatalf("%s body=%q err=%v", relative, body, err)
			}
		}
	}
	assertPath(".", os.ModeDir, 0555, "")
	assertPath("empty", os.ModeDir, 0555, "")
	assertPath("nested", os.ModeDir, 0555, "")
	assertPath("nested/run.sh", 0, 0444, "#!/bin/sh\nprintf exact\\n\n")
	assertPath("literal [x]", 0, 0444, "payload\x00bytes\n")
	assertPath(".hangar-materialized", 0, 0444, string(mustJSON(t, ref)))
	linkInfo, err := os.Lstat(filepath.Join(root, "latest"))
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("latest type=%v err=%v, want symlink", linkInfo, err)
	}
	target, err := os.Readlink(filepath.Join(root, "latest"))
	if err != nil || target != "nested/run.sh" {
		t.Fatalf("latest target=%q err=%v", target, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
