package imageresolver_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func pushImage(t *testing.T, registryHost, repo, tag string) string {
	t.Helper()

	// Create a minimal OCI image with a config layer so it has a stable digest.
	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)

	ref, err := name.ParseReference(fmt.Sprintf("%s/%s:%s", registryHost, repo, tag))
	if err != nil {
		t.Fatalf("parse ref: %v", err)
	}

	err = remote.Write(ref, img)
	if err != nil {
		t.Fatalf("push image: %v", err)
	}

	// Get the digest we just pushed.
	desc, err := remote.Head(ref)
	if err != nil {
		t.Fatalf("head after push: %v", err)
	}

	return desc.Digest.String()
}

func startRegistry(t *testing.T) string {
	t.Helper()
	reg := registry.New()
	server := httptest.NewServer(reg)
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

func TestResolver_TagToDigest(t *testing.T) {
	host := startRegistry(t)
	expectedDigest := pushImage(t, host, "myrepo/myimage", "v1.0")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)
	digest, err := resolver.Resolve(context.Background(), host+"/myrepo/myimage", "v1.0", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if digest != expectedDigest {
		t.Errorf("got digest %q, want %q", digest, expectedDigest)
	}
}

func TestResolver_DefaultTag(t *testing.T) {
	host := startRegistry(t)
	expectedDigest := pushImage(t, host, "myrepo/myimage", "latest")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)
	digest, err := resolver.Resolve(context.Background(), host+"/myrepo/myimage", "", nil)
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
	// Set up a registry that requires basic auth.
	reg := registry.New()
	authedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "testuser" || pass != "testpass" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		reg.ServeHTTP(w, r)
	})
	server := httptest.NewServer(authedHandler)
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")

	// Push image (without auth for setup, directly to underlying reg).
	setupServer := httptest.NewServer(reg)
	t.Cleanup(setupServer.Close)
	setupHost := strings.TrimPrefix(setupServer.URL, "http://")
	expectedDigest := pushImage(t, setupHost, "private/image", "v2.0")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	// Without auth should fail.
	_, err := resolver.Resolve(context.Background(), host+"/private/image", "v2.0", nil)
	if err == nil {
		t.Fatal("expected error without auth, got nil")
	}

	// With auth should succeed.
	digest, err := resolver.Resolve(context.Background(), host+"/private/image", "v2.0", &imageresolver.BasicAuth{
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
	host := startRegistry(t)

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	_, err := resolver.Resolve(context.Background(), host+"/nonexistent/image", "v1.0", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent image, got nil")
	}
}

func TestResolver_CancelledContext(t *testing.T) {
	host := startRegistry(t)
	pushImage(t, host, "myrepo/myimage", "v1.0")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := resolver.Resolve(ctx, host+"/myrepo/myimage", "v1.0", nil)
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestResolver_NilKeychainUsesGCPMultiKeychain(t *testing.T) {
	// When keychain is nil, NewResolver should use a multi-keychain that
	// includes google.Keychain + authn.DefaultKeychain. This test verifies
	// that the nil-keychain path works correctly against a plain registry
	// (the multi-keychain falls through to anonymous when no GCP creds exist).
	host := startRegistry(t)
	expectedDigest := pushImage(t, host, "myrepo/gcptest", "v1.0")

	resolver := imageresolver.NewResolver(nil) // nil → GCP multi-keychain
	digest, err := resolver.Resolve(context.Background(), host+"/myrepo/gcptest", "v1.0", nil)
	if err != nil {
		t.Fatalf("unexpected error with nil keychain: %v", err)
	}

	if digest != expectedDigest {
		t.Errorf("got digest %q, want %q", digest, expectedDigest)
	}
}

func TestResolver_MultipleTags(t *testing.T) {
	host := startRegistry(t)
	digestV1 := pushImage(t, host, "myrepo/app", "v1")
	digestV2 := pushImage(t, host, "myrepo/app", "v2")

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)

	got1, err := resolver.Resolve(context.Background(), host+"/myrepo/app", "v1", nil)
	if err != nil {
		t.Fatalf("resolve v1: %v", err)
	}

	got2, err := resolver.Resolve(context.Background(), host+"/myrepo/app", "v2", nil)
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
