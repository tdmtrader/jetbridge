package imageresolver

import (
	"context"
	"errors"
	"strings"

	"github.com/concourse/concourse/atc"
)

// ErrMissingRepository is returned when a registry-image source names no
// repository, so there is nothing to ask a registry about.
var ErrMissingRepository = errors.New("missing repository in source")

// Reference is an image by repository and either a tag to resolve (empty for
// "latest") or, for a reference parsed from "repository@sha256:…", the digest
// it already carries. Auth is the basic auth a registry-image source carries,
// nil when it carries none.
type Reference struct {
	Repository string
	Tag        string
	Digest     string
	Auth       *BasicAuth
}

// ReferenceFromSource reads the registry-image source conventions once for
// every native resolution path: repository, tag, and username/password. The
// source must already have had its ((var)) references evaluated.
func ReferenceFromSource(source atc.Source) (Reference, error) {
	repository, _ := source["repository"].(string)
	if repository == "" {
		return Reference{}, ErrMissingRepository
	}

	tag, _ := source["tag"].(string)

	var auth *BasicAuth
	if username, ok := source["username"].(string); ok && username != "" {
		password, _ := source["password"].(string)
		auth = &BasicAuth{
			Username: username,
			Password: password,
		}
	}

	return Reference{Repository: repository, Tag: tag, Auth: auth}, nil
}

// Resolve asks the resolver for the digest the reference currently names. A
// reference that already carries a digest is its own answer.
func (ref Reference) Resolve(ctx context.Context, resolver Resolver) (string, error) {
	if ref.Digest != "" {
		return ref.Digest, nil
	}
	return resolver.Resolve(ctx, ref.Repository, ref.Tag, ref.Auth)
}

// ParseReference reads an image reference such as "redis:7", "myrepo/myimage",
// "registry:5000/repo" or "host/repo@sha256:…" into its parts, having stripped
// any Concourse-internal prefix. A reference with a digest keeps it and has no
// tag; one without a tag defaults to "latest".
func ParseReference(image string) Reference {
	image = atc.StripImageRefPrefix(image)

	if repository, digest, found := strings.Cut(image, "@"); found {
		return Reference{Repository: repository, Digest: digest}
	}

	// Handle images with ports in registry (e.g. "registry:5000/repo:tag")
	lastColon := strings.LastIndex(image, ":")
	lastSlash := strings.LastIndex(image, "/")

	if lastColon > lastSlash && lastColon > 0 {
		return Reference{Repository: image[:lastColon], Tag: image[lastColon+1:]}
	}
	return Reference{Repository: image, Tag: "latest"}
}

// Pin returns a digest-pinned reference, "repository@sha256:…", for the image.
//
// An image that already carries a digest is returned exactly as given, prefix
// included, and the registry is not asked. Otherwise the Concourse-internal
// prefix is stripped, the tag is resolved to the digest it currently names,
// and the two are rejoined.
func Pin(ctx context.Context, resolver Resolver, image string) (string, error) {
	if strings.Contains(image, "@sha256:") {
		return image, nil
	}

	ref := ParseReference(image)
	digest, err := ref.Resolve(ctx, resolver)
	if err != nil {
		return "", err
	}

	return ref.Repository + "@" + digest, nil
}
