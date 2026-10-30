package imageresolver_test

import (
	"errors"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/imageresolver"
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
