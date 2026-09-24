package diskserver_test

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
	"github.com/concourse/concourse/hangar/treestore"
)

func TestStrictTreePublicationAndVerifiedExtractionThroughTLS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	if err := w.WriteHeader(&tar.Header{Name: "result", Mode: 0644, Size: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	canonicalizer := hangar.Canonicalizer{TempDir: t.TempDir(), MaxContentBytes: 1 << 20, MaxEntries: 100}
	captured, err := canonicalizer.Capture(ctx, bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close()
	store, err := treestore.New(f.client(t, "input"), treestore.Config{Bucket: "inputs", ScratchDir: t.TempDir(), ReadTimeout: time.Minute, WriteTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := os.Open(captured.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	attrs, created, err := store.EnsureTree(ctx, "strict-scope", captured.Digest, canonical, 1<<20)
	_ = canonical.Close()
	if err != nil || !created {
		t.Fatalf("publish: %v, created=%v", err, created)
	}
	body, opened, err := store.OpenTree(ctx, attrs.Ref, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	extracted, err := canonicalizer.Capture(ctx, body)
	_ = body.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer extracted.Close()
	if opened.Ref != attrs.Ref || extracted.Digest != captured.Digest {
		t.Fatal("tree identity changed")
	}
	content, err := os.ReadFile(filepath.Join(extracted.Root, "result"))
	if err != nil || string(content) != "hello" {
		t.Fatalf("extracted %q: %v", content, err)
	}
}

func TestOutputPublisherUsesDiskNamespaceOverTLS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	namespace, err := output.DeriveNamespace(output.NamespaceConfig{Store: output.StoreDisk, StoreID: "test-store", Bucket: "outputs", TenantID: "tenant", ActivationEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	p, err := publisher.New(namespace, publisher.Restrict(f.client(t, "publisher")), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	digest := hangar.Digest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	marker := namespace.MarkerFor("11111111-1111-4111-8111-111111111111", digest, output.NewTimestamp(time.Now().UTC()))
	object, err := p.EnsurePublication(ctx, marker, bytes.NewBufferString("canonical bytes"), 15)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := p.StatExactObject(ctx, object.Attributes.Ref)
	if err != nil || stat.Attributes.Ref != object.Attributes.Ref {
		t.Fatalf("exact stat: %v", err)
	}
	// The reader validates its lease before this storage call in production;
	// the actual exact-generation transfer must also remain intact over TLS.
	key, err := namespace.ObjectKey(digest)
	if err != nil {
		t.Fatal(err)
	}
	body, err := f.client(t, "publisher").OpenExact(ctx, "outputs", key, object.Attributes.Ref.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	content, err := io.ReadAll(body)
	if err != nil || string(content) != "canonical bytes" {
		t.Fatalf("read %q %v", content, err)
	}
}
