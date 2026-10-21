package imageresolver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/google"
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
// The provided keychain is used for authentication. If nil, a multi-keychain
// is used that tries GCP credentials (Workload Identity, Application Default
// Credentials) first, then falls back to the Docker config file.
func NewResolver(keychain authn.Keychain, options ...remote.Option) Resolver {
	if keychain == nil {
		keychain = authn.NewMultiKeychain(google.Keychain, authn.DefaultKeychain)
	}
	return &registryResolver{
		keychain: keychain,
		options:  options,
	}
}

// resolveTimeout bounds one resolve. The registry transport has no response
// timeout, and some callers resolve under a context with no deadline that
// nothing can cancel, such as an image fetched for an in-memory check build,
// so a registry that accepts the connection and never answers would otherwise
// hold them forever. A minute is the default resource checking interval; a
// caller with a shorter bound, such as a check timeout, keeps it.
var resolveTimeout = time.Minute

func (r *registryResolver) Resolve(ctx context.Context, repository string, tag string, auth *BasicAuth) (string, error) {
	if repository == "" {
		return "", errors.New("empty repository")
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

	imageRef, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parsing image reference %q: %w", ref, err)
	}

	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

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
