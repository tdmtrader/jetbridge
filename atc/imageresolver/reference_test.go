package imageresolver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/imageresolver/imageresolvertesting"
)

func TestReferenceFromSource(t *testing.T) {
	ref, err := imageresolver.ReferenceFromSource(atc.Source{
		"repository": "registry.example/team/tool",
		"tag":        "v2",
		"username":   "user",
		"password":   "secret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Repository != "registry.example/team/tool" || ref.Tag != "v2" {
		t.Fatalf("unexpected reference %+v", ref)
	}
	if ref.Auth == nil || ref.Auth.Username != "user" || ref.Auth.Password != "secret" {
		t.Fatalf("unexpected auth %+v", ref.Auth)
	}
}

func TestReferenceFromSource_NoTagNoAuth(t *testing.T) {
	ref, err := imageresolver.ReferenceFromSource(atc.Source{"repository": "tool"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Tag != "" {
		t.Fatalf("tag should stay empty for the resolver to default, got %q", ref.Tag)
	}
	if ref.Auth != nil {
		t.Fatalf("a source without a username carries no auth, got %+v", ref.Auth)
	}
}

func TestReferenceFromSource_EmptyUsernameIsNoAuth(t *testing.T) {
	ref, err := imageresolver.ReferenceFromSource(atc.Source{"repository": "tool", "username": "", "password": "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Auth != nil {
		t.Fatalf("an empty username carries no auth, got %+v", ref.Auth)
	}
}

func TestReferenceFromSource_MissingRepository(t *testing.T) {
	for name, source := range map[string]atc.Source{
		"absent":     {"tag": "v1"},
		"empty":      {"repository": ""},
		"not-string": {"repository": 7},
	} {
		if _, err := imageresolver.ReferenceFromSource(source); !errors.Is(err, imageresolver.ErrMissingRepository) {
			t.Errorf("%s: want ErrMissingRepository, got %v", name, err)
		}
	}
}

func TestParseReference(t *testing.T) {
	for image, want := range map[string]imageresolver.Reference{
		"redis:7":                            {Repository: "redis", Tag: "7"},
		"myrepo/myimage":                     {Repository: "myrepo/myimage", Tag: "latest"},
		"registry:5000/repo":                 {Repository: "registry:5000/repo", Tag: "latest"},
		"registry:5000/repo:tag":             {Repository: "registry:5000/repo", Tag: "tag"},
		"docker:///myrepo/myimage:v1":        {Repository: "myrepo/myimage", Tag: "v1"},
		"raw:///registry.example/tool":       {Repository: "registry.example/tool", Tag: "latest"},
		"host/repo@sha256:ab":                {Repository: "host/repo", Digest: "sha256:ab"},
		"docker:///host:5000/repo@sha256:ab": {Repository: "host:5000/repo", Digest: "sha256:ab"},
	} {
		if got := imageresolver.ParseReference(image); got != want {
			t.Errorf("%q: got %+v, want %+v", image, got, want)
		}
	}
}

func TestPin(t *testing.T) {
	resolver := &imageresolvertesting.FakeResolver{}
	resolver.ResolveStub(func(_ context.Context, repository, tag string, auth *imageresolver.BasicAuth) (string, error) {
		if auth != nil {
			t.Fatalf("a bare reference carries no auth, got %+v", auth)
		}
		return "sha256:" + repository + "-" + tag, nil
	})

	pinned, err := imageresolver.Pin(context.Background(), resolver, "docker:///redis:7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pinned != "redis@sha256:redis-7" {
		t.Fatalf("got %q", pinned)
	}
}

func TestPin_AlreadyPinnedIsReturnedUntouched(t *testing.T) {
	resolver := &imageresolvertesting.FakeResolver{}
	image := "docker:///myrepo/mydb@sha256:abc123"
	pinned, err := imageresolver.Pin(context.Background(), resolver, image)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pinned != image {
		t.Fatalf("got %q, want %q", pinned, image)
	}
	if resolver.ResolveCallCount() != 0 {
		t.Fatalf("the registry was asked %d times about a pinned reference", resolver.ResolveCallCount())
	}
}

func TestPin_ResolveFailure(t *testing.T) {
	resolver := &imageresolvertesting.FakeResolver{}
	resolver.ResolveReturns("", errors.New("UNAUTHORIZED"))
	if _, err := imageresolver.Pin(context.Background(), resolver, "private/tool:1"); err == nil || err.Error() != "UNAUTHORIZED" {
		t.Fatalf("want the resolver's error, got %v", err)
	}
}

func TestReferenceWithDigestResolvesWithoutTheRegistry(t *testing.T) {
	resolver := &imageresolvertesting.FakeResolver{}
	digest, err := imageresolver.ParseReference("host/repo@sha256:ab").Resolve(context.Background(), resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if digest != "sha256:ab" {
		t.Fatalf("got %q", digest)
	}
	if resolver.ResolveCallCount() != 0 {
		t.Fatalf("the registry was asked %d times about a digest", resolver.ResolveCallCount())
	}
}
