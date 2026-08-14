package imageresolvertesting_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
	"github.com/google/go-containerregistry/pkg/authn"
)

func TestRegistryResolvesPushedImage(t *testing.T) {
	registry := imageresolvertesting.NewRegistry()
	t.Cleanup(registry.Close)

	expectedDigest, err := registry.Push("repo/image", "v1")
	if err != nil {
		t.Fatalf("push image: %v", err)
	}
	registry.DrainRequests()

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)
	digest, err := resolver.Resolve(context.Background(), registry.Host()+"/repo/image", "v1", nil)
	if err != nil {
		t.Fatalf("resolve image: %v", err)
	}
	if digest != expectedDigest {
		t.Fatalf("digest = %q, want %q", digest, expectedDigest)
	}

	requests := registry.DrainRequests()
	if !containsRequest(requests, imageresolvertesting.Request{
		Method:       http.MethodHead,
		Path:         "/v2/repo/image/manifests/v1",
		HasBasicAuth: false,
	}) {
		t.Fatalf("requests = %#v, want anonymous HEAD for pushed manifest", requests)
	}
}

func TestRegistryPushesDistinctManifestsPerReference(t *testing.T) {
	registry := imageresolvertesting.NewRegistry()
	t.Cleanup(registry.Close)

	firstDigest, err := registry.Push("repo/first", "v1")
	if err != nil {
		t.Fatalf("push first image: %v", err)
	}
	secondDigest, err := registry.Push("repo/second", "v2")
	if err != nil {
		t.Fatalf("push second image: %v", err)
	}

	if firstDigest == secondDigest {
		t.Fatalf("distinct references produced the same digest %q", firstDigest)
	}
}

func TestRegistryRequiresBasicAuth(t *testing.T) {
	registry := imageresolvertesting.NewRegistry()
	t.Cleanup(registry.Close)

	expectedDigest, err := registry.Push("private/image", "v2")
	if err != nil {
		t.Fatalf("push image: %v", err)
	}
	registry.RequireBasicAuth("testuser", "testpass")
	registry.DrainRequests()

	resolver := imageresolver.NewResolver(authn.DefaultKeychain)
	_, err = resolver.Resolve(context.Background(), registry.Host()+"/private/image", "v2", nil)
	if err == nil {
		t.Fatal("resolve without credentials succeeded, want authentication failure")
	}

	digest, err := resolver.Resolve(
		context.Background(),
		registry.Host()+"/private/image",
		"v2",
		&imageresolver.BasicAuth{Username: "testuser", Password: "testpass"},
	)
	if err != nil {
		t.Fatalf("resolve with credentials: %v", err)
	}
	if digest != expectedDigest {
		t.Fatalf("digest = %q, want %q", digest, expectedDigest)
	}

	requests := registry.DrainRequests()
	if !containsRequest(requests, imageresolvertesting.Request{
		Method:       http.MethodHead,
		Path:         "/v2/private/image/manifests/v2",
		HasBasicAuth: true,
	}) {
		t.Fatalf("requests = %#v, want authenticated HEAD for protected manifest", requests)
	}
}

func containsRequest(requests []imageresolvertesting.Request, expected imageresolvertesting.Request) bool {
	for _, request := range requests {
		if request == expected {
			return true
		}
	}
	return false
}
