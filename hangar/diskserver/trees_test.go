package diskserver_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/disk"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/publisher"
)

// The publisher role is the input publication's and the capture's one
// writer: it creates absent-only, reads and stats exactly, and a generation
// the store once issued is never issued again, even for the same key after
// its object is reclaimed.
func TestPublisherRoleIsAbsentOnlyExactAndAGenerationIsNeverReissued(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	pub := f.client(t, "publisher")
	first, err := pub.CreateAbsent(ctx, "outputs", "tree", nil, strings.NewReader("first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pub.CreateAbsent(ctx, "outputs", "tree", nil, strings.NewReader("second")); !errors.Is(err, objectstore.ErrPreconditionFailed) {
		t.Fatalf("a second create at an occupied key: %v", err)
	}
	if stat, err := pub.StatExact(ctx, "outputs", "tree", first.Generation); err != nil || stat.Generation != first.Generation {
		t.Fatalf("exact stat: %+v %v", stat, err)
	}
	if _, err := pub.StatExact(ctx, "outputs", "tree", first.Generation+1); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("stat at another generation: %v", err)
	}
	if _, err := pub.OpenExact(ctx, "outputs", "tree", first.Generation+1); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("open at another generation: %v", err)
	}
	body, err := pub.OpenExact(ctx, "outputs", "tree", first.Generation)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(content) != "first" {
		t.Fatalf("exact open %q %v", content, err)
	}
	deleter, err := disk.NewDeleteClient(f.config(t, "reclaimer"))
	if err != nil {
		t.Fatal(err)
	}
	if err := deleter.DeleteExact(ctx, "outputs", "tree", first.Generation); err != nil {
		t.Fatal(err)
	}
	second, err := pub.CreateAbsent(ctx, "outputs", "tree", nil, strings.NewReader("second"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("generation %d reissued after %d", second.Generation, first.Generation)
	}
	if _, err := pub.OpenExact(ctx, "outputs", "tree", first.Generation); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("the reclaimed generation answered: %v", err)
	}
}

func TestOutputPublisherUsesDiskNamespaceOverTLS(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	namespace, err := output.DeriveNamespace(output.NamespaceConfig{Store: output.StoreDisk, StoreID: "test-store", Bucket: "outputs", TenantID: "tenant"})
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
