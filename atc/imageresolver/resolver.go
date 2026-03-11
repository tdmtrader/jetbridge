package imageresolver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Resolver resolves OCI image references to their digest (SHA256).
type Resolver interface {
	// Resolve takes a repository and optional tag, returning the digest.
	// If tag is empty, "latest" is assumed.
	// If ref is already a digest (contains @sha256:), it is returned as-is.
	Resolve(ctx context.Context, repository string, tag string, auth *BasicAuth) (string, error)
}

// BasicAuth holds optional username/password credentials for private registries.
type BasicAuth struct {
	Username string
	Password string
}

// registryResolver implements Resolver using go-containerregistry.
type registryResolver struct {
	keychain authn.Keychain
	options  []remote.Option
}

// NewResolver creates a Resolver that queries OCI registries directly.
// The provided keychain is used for authentication (e.g., GCP default
// credentials). If nil, authn.DefaultKeychain is used.
func NewResolver(keychain authn.Keychain, options ...remote.Option) Resolver {
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}
	return &registryResolver{
		keychain: keychain,
		options:  options,
	}
}

func (r *registryResolver) Resolve(ctx context.Context, repository string, tag string, auth *BasicAuth) (string, error) {
	if repository == "" {
		return "", fmt.Errorf("empty repository")
	}

	// If repository already contains a digest, return it directly.
	if idx := strings.Index(repository, "@sha256:"); idx >= 0 {
		return repository[idx+1:], nil
	}

	// Build the image reference string.
	ref := repository
	if tag == "" {
		tag = "latest"
	}
	ref = ref + ":" + tag

	// Parse the reference.
	imageRef, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parsing image reference %q: %w", ref, err)
	}

	// Build remote options.
	opts := make([]remote.Option, 0, len(r.options)+2)
	opts = append(opts, remote.WithContext(ctx))

	if auth != nil && auth.Username != "" {
		// Use explicit basic auth credentials.
		opts = append(opts, remote.WithAuth(&authn.Basic{
			Username: auth.Username,
			Password: auth.Password,
		}))
	} else {
		// Use keychain (GCP default credentials, Docker config, etc.).
		opts = append(opts, remote.WithAuthFromKeychain(r.keychain))
	}

	opts = append(opts, r.options...)

	// Get the image descriptor (HEAD request for digest).
	desc, err := remote.Head(imageRef, opts...)
	if err != nil {
		return "", fmt.Errorf("resolving digest for %q: %w", ref, err)
	}

	return desc.Digest.String(), nil
}
