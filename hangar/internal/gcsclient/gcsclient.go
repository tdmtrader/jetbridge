// Package gcsclient opens this repository's one Cloud Storage client, and it is
// importable only from within hangar/.
//
// The package exists because of what a third round of one finding taught. Delete
// was first a method on the shared object handle; narrowing that closed the
// route that had been demonstrated and left open a cheaper one, because
// hangar/gcs.NewStorageClient was exported and handed the raw *storage.Client to
// three non-reclaimer command roots, each of which held it in a local variable.
// A method call on an already-typed value needs no import, so
//
//	storageClient.Bucket(bucket).Object("any/key").Delete(ctx)
//
// built in the daemon, the inventory controller and the attestor alike, added
// nothing to any dependency graph, and passed every architecture guard in the
// tree. That was demonstrated in all three.
//
// The lesson is that a guard which names a route guards that route, not the
// capability. So the capability stops being handed out: nothing outside hangar/
// can obtain a *storage.Client through this repository's own seam, because Go's
// internal rule is enforced by the toolchain rather than by a rule a reviewer
// has to read. Each capability package above this one -- hangar/gcs,
// hangar/gcsstore, hangar/gcsdelete -- opens and owns its own client behind a
// (ctx, endpoint) constructor and returns an interface with exactly the
// operations its role may issue.
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
