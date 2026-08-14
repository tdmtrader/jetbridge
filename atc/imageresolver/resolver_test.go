package imageresolver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/google/go-containerregistry/pkg/authn"
)

func pushImage(t *testing.T, registry *imageresolvertesting.Registry, repo, tag string) string {
	t.Helper()

	digest, err := registry.Push(repo, tag)
	if err != nil {
		t.Fatalf("push image: %v", err)
	}
	return digest
}

func startRegistry(t *testing.T) *imageresolvertesting.Registry {
	t.Helper()
	registry := imageresolvertesting.NewRegistry()
	t.Cleanup(registry.Close)
	return registry
}

func TestResolver_TagToDigest(t *testing.T) {
	registry := startRegistry(t)
	expectedDigest := pushImage(t, registry, "myrepo/myimage", "v1.0")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)
	digest, err := resolver.Resolve(context.Background(), registry.Host()+"/myrepo/myimage", "v1.0", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if digest != expectedDigest {
		t.Errorf("got digest %q, want %q", digest, expectedDigest)
	}
}

func TestResolver_DefaultTag(t *testing.T) {
	registry := startRegistry(t)
	expectedDigest := pushImage(t, registry, "myrepo/myimage", "latest")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)
	digest, err := resolver.Resolve(context.Background(), registry.Host()+"/myrepo/myimage", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if digest != expectedDigest {
		t.Errorf("got digest %q, want %q", digest, expectedDigest)
	}
}

func TestResolver_AlreadyPinnedDigest(t *testing.T) {
	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	digest, err := resolver.Resolve(
		context.Background(),
		"myregistry.io/repo@sha256:abc123def456",
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if digest != "sha256:abc123def456" {
		t.Errorf("got digest %q, want %q", digest, "sha256:abc123def456")
	}
}

func TestResolver_BasicAuth(t *testing.T) {
	registry := startRegistry(t)
	expectedDigest := pushImage(t, registry, "private/image", "v2.0")
	registry.RequireBasicAuth("testuser", "testpass")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	// Without auth should fail.
	_, err := resolver.Resolve(context.Background(), registry.Host()+"/private/image", "v2.0", nil)
	if err == nil {
		t.Fatal("expected error without auth, got nil")
	}

	// With auth should succeed.
	digest, err := resolver.Resolve(context.Background(), registry.Host()+"/private/image", "v2.0", &imageresolver.BasicAuth{
		Username: "testuser",
		Password: "testpass",
	})
	if err != nil {
		t.Fatalf("unexpected error with auth: %v", err)
	}

	if digest != expectedDigest {
		t.Errorf("got digest %q, want %q", digest, expectedDigest)
	}
}

func TestResolver_EmptyRepository(t *testing.T) {
	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	_, err := resolver.Resolve(context.Background(), "", "latest", nil)
	if err == nil {
		t.Fatal("expected error for empty repository, got nil")
	}

	if !strings.Contains(err.Error(), "empty repository") {
		t.Errorf("expected 'empty repository' error, got: %v", err)
	}
}

func TestResolver_ImageNotFound(t *testing.T) {
	registry := startRegistry(t)

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	_, err := resolver.Resolve(context.Background(), registry.Host()+"/nonexistent/image", "v1.0", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent image, got nil")
	}
}

func TestResolver_CancelledContext(t *testing.T) {
	registry := startRegistry(t)
	pushImage(t, registry, "myrepo/myimage", "v1.0")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := resolver.Resolve(ctx, registry.Host()+"/myrepo/myimage", "v1.0", nil)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestResolver_NilKeychainUsesGCPMultiKeychain(t *testing.T) {
	// When keychain is nil, NewResolver should use a multi-keychain that
	// includes google.Keychain + authn.DefaultKeychain. This test verifies
	// that the nil-keychain path works correctly against a plain registry
	// (the multi-keychain falls through to anonymous when no GCP creds exist).
	registry := startRegistry(t)
	expectedDigest := pushImage(t, registry, "myrepo/gcptest", "v1.0")

	resolver := imageresolver.NewResolver(nil) // nil → GCP multi-keychain
	digest, err := resolver.Resolve(context.Background(), registry.Host()+"/myrepo/gcptest", "v1.0", nil)
	if err != nil {
		t.Fatalf("unexpected error with nil keychain: %v", err)
	}

	if digest != expectedDigest {
		t.Errorf("got digest %q, want %q", digest, expectedDigest)
	}
}

func TestResolver_MultipleTags(t *testing.T) {
	registry := startRegistry(t)
	digestV1 := pushImage(t, registry, "myrepo/app", "v1")
	digestV2 := pushImage(t, registry, "myrepo/app", "v2")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	got1, err := resolver.Resolve(context.Background(), registry.Host()+"/myrepo/app", "v1", nil)
	if err != nil {
		t.Fatalf("resolve v1: %v", err)
	}

	got2, err := resolver.Resolve(context.Background(), registry.Host()+"/myrepo/app", "v2", nil)
	if err != nil {
		t.Fatalf("resolve v2: %v", err)
	}

	if got1 != digestV1 {
		t.Errorf("v1: got %q, want %q", got1, digestV1)
	}
	if got2 != digestV2 {
		t.Errorf("v2: got %q, want %q", got2, digestV2)
	}
}
