package gcs

import (
	"context"
	"fmt"

	"cloud.google.com/go/storage"

	"github.com/concourse/concourse/hangar/internal/gcsclient"
	"github.com/concourse/concourse/hangar/objectstore"
)

// NewDeleteClient opens the exact-delete capability's own client and owns it.
//
// It is the only constructor of objectstore.DeleteClient over a real cloud
// client in this repository, and it takes an endpoint rather than a client
// because handing the client to the caller hands the caller the capability.
// Which packages may call it is fixed by hangar/architecture_test.go: the
// output reclaimer, and the artifact daemon's fail-open cache tier against its
// own dedicated cache bucket. The returned closer is the caller's to defer.
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
		return objectstore.Attrs{}, translate(err)
	}
	return outputAttrs(attrs), nil
}

func (client deleteClient) DeleteExact(ctx context.Context, bucket, key string, generation int64) error {
	if err := objectstore.ValidateGeneration(generation); err != nil {
		return err
	}
	return translate(client.client.Bucket(bucket).Object(key).If(storage.Conditions{GenerationMatch: generation}).Delete(ctx))
}

// CheckBucket reports whether a bucket exists and is reachable through the
// client's credentials. It opens its own short-lived client so no caller holds
// a raw SDK handle.
func CheckBucket(ctx context.Context, endpoint, bucket string) error {
	client, err := gcsclient.New(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("%w: opening the GCS client: %v", objectstore.ErrInfrastructure, err)
	}
	defer client.Close()
	_, err = client.Bucket(bucket).Attrs(ctx)
	return translate(err)
}
