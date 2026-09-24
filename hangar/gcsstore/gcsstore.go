// Package gcsstore constructs the artifact daemon's verified strict-input store.
package gcsstore

import (
	"context"
	"errors"

	"github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/internal/gcsclient"
	"github.com/concourse/concourse/hangar/treestore"
)

type GCSConfig = treestore.Config

type GCSStore struct {
	*treestore.Store
	bucketAttrs func(context.Context) error
}

func NewGCSStore(ctx context.Context, endpoint string, config GCSConfig) (*GCSStore, func() error, error) {
	objects, closeObjects, err := gcs.NewObjectClient(ctx, endpoint)
	if err != nil {
		return nil, nil, err
	}
	client, err := gcsclient.New(ctx, endpoint)
	if err != nil {
		_ = closeObjects()
		return nil, nil, err
	}
	closeAll := func() error { return errors.Join(closeObjects(), client.Close()) }
	store, err := treestore.New(objects, config)
	if err != nil {
		_ = closeAll()
		return nil, nil, err
	}
	return &GCSStore{Store: store, bucketAttrs: func(ctx context.Context) error { _, err := client.Bucket(config.Bucket).Attrs(ctx); return err }}, closeAll, nil
}

func (store *GCSStore) ValidateBucket(ctx context.Context) error { return store.bucketAttrs(ctx) }
