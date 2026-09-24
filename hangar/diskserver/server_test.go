package diskserver_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/disk"
	"github.com/concourse/concourse/hangar/diskclient"
	"github.com/concourse/concourse/hangar/diskdelete"
	"github.com/concourse/concourse/hangar/diskserver"
	"github.com/concourse/concourse/hangar/objectstore"
)

func TestMetadataHeavyInventoryMakesCursorProgress(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	pub := f.client(t, "publisher")
	metadata := map[string]string{"large": strings.Repeat("x", 64<<10-32)}
	const count = 130
	for i := 0; i < count; i++ {
		if _, err := pub.CreateAbsent(ctx, "outputs", fmt.Sprintf("tree-%03d", i), metadata, strings.NewReader("body")); err != nil {
			t.Fatal(err)
		}
	}
	inv := f.client(t, "inventory")
	request := objectstore.ListRequest{PageSize: 1000}
	seen := 0
	for {
		page, err := inv.List(ctx, "outputs", request)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Objects) == 0 {
			t.Fatal("inventory stopped making progress")
		}
		for _, attrs := range page.Objects {
			if attrs.Key != fmt.Sprintf("tree-%03d", seen) || attrs.Metadata["large"] != metadata["large"] {
				t.Fatalf("unexpected object at position %d", seen)
			}
			seen++
		}
		if page.Done {
			break
		}
		if seen >= count {
			t.Fatal("inventory did not finish")
		}
		last := page.Objects[len(page.Objects)-1]
		request.After, request.AfterGeneration = page.LastKey, last.Generation
	}
	if seen != count {
		t.Fatalf("listed %d objects, want %d", seen, count)
	}
	for _, invalid := range []int{0, 1001} {
		if _, err := inv.List(ctx, "outputs", objectstore.ListRequest{PageSize: invalid}); !errors.Is(err, hangar.ErrLimitExceeded) {
			t.Fatalf("invalid page size %d: %v", invalid, err)
		}
	}
}

type fixture struct {
	server   *httptest.Server
	root, ca string
	tokens   map[string]string
}

func setup(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	if err := disk.Initialize(root, "test-store"); err != nil {
		t.Fatal(err)
	}
	store, err := disk.Open(root, "test-store", 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tokens := map[string]string{"input": strings.Repeat("i", 32), "publisher": strings.Repeat("p", 32), "inventory": strings.Repeat("v", 32), "reclaimer": strings.Repeat("r", 32)}
	h, err := diskserver.New(store, diskserver.Config{StoreID: "test-store", InputNamespace: "inputs", OutputNamespace: "outputs", Credentials: tokens, MaxConcurrent: 4})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	return fixture{server: server, root: root, ca: ca, tokens: tokens}
}
func (f fixture) config(t *testing.T, role string) diskclient.Config {
	t.Helper()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(f.tokens[role]), 0600); err != nil {
		t.Fatal(err)
	}
	return diskclient.Config{Endpoint: f.server.URL, StoreID: "test-store", TokenFile: token, CACert: f.ca, Timeout: time.Second * 5}
}
func (f fixture) client(t *testing.T, role string) objectstore.Client {
	t.Helper()
	c, err := diskclient.New(f.config(t, role))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestAuthenticatedExactObjectsAndNamespaceIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	pub := f.client(t, "publisher")
	a, err := pub.CreateAbsent(ctx, "outputs", "tree", map[string]string{"marker": "one"}, strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := pub.OpenExact(ctx, "outputs", "tree", a.Generation)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || string(body) != "body" {
		t.Fatalf("read %q %v", body, err)
	}
	if _, err := pub.CreateAbsent(ctx, "outputs", "tree", nil, strings.NewReader("wrong")); !errors.Is(err, objectstore.ErrPreconditionFailed) {
		t.Fatalf("overwrite %v", err)
	}
	if _, err := pub.CreateAbsent(ctx, "inputs", "tree", nil, strings.NewReader("wrong")); !errors.Is(err, objectstore.ErrUnauthorized) {
		t.Fatalf("cross namespace %v", err)
	}
	inv := f.client(t, "inventory")
	page, err := inv.List(ctx, "outputs", objectstore.ListRequest{PageSize: 10})
	if err != nil || len(page.Objects) != 1 {
		t.Fatalf("list %+v %v", page, err)
	}
	if _, err := inv.OpenExact(ctx, "outputs", "tree", a.Generation); !errors.Is(err, objectstore.ErrUnauthorized) {
		t.Fatalf("inventory body %v", err)
	}
	if _, err := inv.CreateAbsent(ctx, "outputs", "other", nil, strings.NewReader("bad")); !errors.Is(err, objectstore.ErrUnauthorized) {
		t.Fatalf("inventory create %v", err)
	}
	deleter, err := diskdelete.New(f.config(t, "reclaimer"))
	if err != nil {
		t.Fatal(err)
	}
	if err := deleter.DeleteExact(ctx, "outputs", "tree", a.Generation+1); !errors.Is(err, objectstore.ErrPreconditionFailed) {
		t.Fatalf("stale delete %v", err)
	}
	if err := deleter.DeleteExact(ctx, "outputs", "tree", a.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err := pub.StatExact(ctx, "outputs", "tree", a.Generation); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("deleted stat %v", err)
	}
	input := f.client(t, "input")
	if _, err := input.CreateAbsent(ctx, "inputs", "tree", nil, strings.NewReader("input")); err != nil {
		t.Fatal(err)
	}
	if _, err := input.StatCurrent(ctx, "outputs", "tree"); !errors.Is(err, objectstore.ErrUnauthorized) {
		t.Fatalf("input output access %v", err)
	}
	pubDelete, err := diskdelete.New(f.config(t, "publisher"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pubDelete.DeleteExact(ctx, "outputs", "tree", a.Generation); !errors.Is(err, objectstore.ErrUnauthorized) {
		t.Fatalf("publisher delete %v", err)
	}
}
func TestIdentityAuthenticationAndStreamingFailure(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	config := f.config(t, "publisher")
	config.StoreID = "other"
	if _, err := diskclient.New(config); !errors.Is(err, hangar.ErrConflict) {
		t.Fatalf("identity %v", err)
	}
	response, err := f.server.Client().Get(f.server.URL + "/v1/stat?bucket=outputs&key=tree")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated %d", response.StatusCode)
	}
	pub := f.client(t, "publisher")
	if _, err := pub.CreateAbsent(ctx, "outputs", "large", nil, bytes.NewReader(make([]byte, 1025))); !errors.Is(err, hangar.ErrLimitExceeded) {
		t.Fatalf("limit %v", err)
	}
	a, err := pub.CreateAbsent(ctx, "outputs", "tree", nil, strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(f.root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(f.root, "blobs", file.Name()), []byte("evil"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := pub.OpenExact(ctx, "outputs", "tree", a.Generation)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	_ = r.Close()
	if !errors.Is(err, hangar.ErrCorrupt) {
		t.Fatalf("corruption trailer lost: %v", err)
	}
}
func TestNoUnconditionalDeleteOrCredentialRedirect(t *testing.T) {
	f := setup(t)
	pub := f.client(t, "publisher")
	if _, err := pub.CreateAbsent(context.Background(), "outputs", "tree", nil, strings.NewReader("body")); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodDelete, f.server.URL+"/v1/delete?"+url.Values{"bucket": {"outputs"}, "key": {"tree"}}.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.tokens["reclaimer"])
	req.Header.Set("X-Hangar-Store-ID", "test-store")
	r, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	if r.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("unconditional delete %d", r.StatusCode)
	}
	config := f.config(t, "publisher")
	config.Endpoint = "http://localhost:1234"
	if _, err := diskclient.New(config); err == nil {
		t.Fatal("plaintext credentials permitted")
	}
}
