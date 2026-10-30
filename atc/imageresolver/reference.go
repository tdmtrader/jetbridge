package imageresolver

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
)

// ErrMissingRepository is returned when a registry-image source names no
// repository, so there is nothing to ask a registry about.
var ErrMissingRepository = errors.New("missing repository in source")

// Reference is the image a registry-image source names: its repository, the
// tag to resolve (empty for "latest") and the basic auth the source carries,
// nil when it carries none.
type Reference struct {
	Repository string
	Tag        string
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

// Resolve asks the resolver for the digest the reference currently names.
func (ref Reference) Resolve(ctx context.Context, resolver Resolver) (string, error) {
	return resolver.Resolve(ctx, ref.Repository, ref.Tag, ref.Auth)
}
