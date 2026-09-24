// Package gcsclient opens this repository's one Cloud Storage client, and it is
// importable only from within hangar/.
//
// Raw SDK clients remain inside Hangar's storage adapters. Returning one to a
// command would let that command call arbitrary SDK operations without adding
// an import that the dependency guards could detect. Go's internal-package
// boundary and the adapters' restricted interfaces preserve that separation.
package gcsclient

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

// New opens a storage client for an endpoint.
//
// An empty endpoint is Google, reached through Application Default Credentials:
// on GKE that is Workload Identity, and no key exists to leak. A non-empty one
// is this repository's existing emulator or explicitly unauthenticated proxy
// convention, shared with cmd/artifact-daemon/durable.NewGCS.
func New(ctx context.Context, endpoint string) (*storage.Client, error) {
	if endpoint == "" {
		return storage.NewClient(ctx)
	}
	normalized, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	return storage.NewClient(ctx, option.WithEndpoint(normalized),
		option.WithoutAuthentication(), storage.WithJSONReads())
}

// NormalizeEndpoint puts an endpoint on the JSON API base the SDK expects.
//
// It hands out no capability -- it is a string in and a string out -- which is
// why hangar/gcs may re-export it for a harness that has to point a client of
// its own at the same emulator while New itself stays unreachable.
func NormalizeEndpoint(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("hangar: invalid GCS endpoint")
	}
	switch strings.TrimSuffix(parsed.Path, "/") {
	case "":
		parsed.Path = "/storage/v1/"
	case "/storage/v1":
		parsed.Path = "/storage/v1/"
	default:
		return "", fmt.Errorf("hangar: GCS endpoint path must be empty or /storage/v1/")
	}

	return parsed.String(), nil
}
