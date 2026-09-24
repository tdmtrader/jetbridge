// Package gcsdelete constructs the separate exact-delete capability. Only the
// reclaimer links it; publisher and inventory clients cannot delete objects.
package gcsdelete

import (
	"context"
	"fmt"

	"cloud.google.com/go/storage"

	"github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/internal/gcsclient"
	"github.com/concourse/concourse/hangar/objectstore"
)

// NewDeleteClient opens the delete capability's own client and owns it.
//
// It is the only constructor of objectstore.DeleteClient over a real cloud
// client in this repository, and it takes an endpoint rather than a client
// because handing the client to the caller hands the caller the capability:
// `client.Bucket(b).Object(k).Delete(ctx)` needs neither this package nor any
// import at all. The returned closer is the caller's to defer.
func NewDeleteClient(ctx context.Context, endpoint string) (objectstore.DeleteClient, func() error, error) {
	client, err := gcsclient.New(ctx, endpoint)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: opening the delete client: %v",
			objectstore.ErrInfrastructure, err)
	}

	return deleteClient{client: client}, client.Close, nil
}

type deleteClient struct{ client *storage.Client }

func (client deleteClient) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	if err := objectstore.ValidateGeneration(generation); err != nil {
		return objectstore.Attrs{}, err
	}
	attrs, err := client.client.Bucket(bucket).Object(key).Generation(generation).Attrs(ctx)
	if err != nil {
		return objectstore.Attrs{}, gcs.TranslateObjectError(err)
	}
	return gcs.OutputAttrs(attrs), nil
}

func (client deleteClient) DeleteExact(ctx context.Context, bucket, key string, generation int64) error {
	if err := objectstore.ValidateGeneration(generation); err != nil {
		return err
	}
	return gcs.TranslateObjectError(client.client.Bucket(bucket).Object(key).If(storage.Conditions{GenerationMatch: generation}).Delete(ctx))
}
