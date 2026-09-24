// Package diskclient exposes only non-destructive disk object operations.
package diskclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/hangar/internal/disktransport"
	"github.com/concourse/concourse/hangar/objectstore"
)

type Config = disktransport.Config
type client struct{ transport *disktransport.Transport }

func New(config Config) (objectstore.Client, error) {
	t, err := disktransport.New(config)
	if err != nil {
		return nil, err
	}
	return &client{t}, nil
}

func (c *client) CreateAbsent(ctx context.Context, bucket, key string, metadata map[string]string, body io.Reader) (objectstore.Attrs, error) {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	headers := http.Header{"X-Hangar-Metadata": {base64.StdEncoding.EncodeToString(encoded)}}
	response, err := c.transport.Request(ctx, http.MethodPost, "create", disktransport.Query(bucket, key), body, headers)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	var attrs objectstore.Attrs
	err = disktransport.Decode(response, &attrs)
	return attrs, err
}
func (c *client) StatCurrent(ctx context.Context, bucket, key string) (objectstore.Attrs, error) {
	return c.stat(ctx, bucket, key, 0)
}
func (c *client) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	if generation <= 0 {
		return objectstore.Attrs{}, objectstore.ErrPreconditionFailed
	}
	return c.stat(ctx, bucket, key, generation)
}
func (c *client) stat(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	query := disktransport.Query(bucket, key)
	query.Set("generation", strconv.FormatInt(generation, 10))
	response, err := c.transport.Request(ctx, http.MethodGet, "stat", query, nil, nil)
	if err != nil {
		return objectstore.Attrs{}, err
	}
	var attrs objectstore.Attrs
	err = disktransport.Decode(response, &attrs)
	return attrs, err
}
func (c *client) OpenExact(ctx context.Context, bucket, key string, generation int64) (io.ReadCloser, error) {
	if generation <= 0 {
		return nil, objectstore.ErrPreconditionFailed
	}
	query := disktransport.Query(bucket, key)
	query.Set("generation", strconv.FormatInt(generation, 10))
	response, err := c.transport.Request(ctx, http.MethodGet, "read", query, nil, nil)
	if err != nil {
		return nil, err
	}
	return &disktransport.Body{Response: response}, nil
}
func (c *client) List(ctx context.Context, bucket string, request objectstore.ListRequest) (objectstore.Page, error) {
	query := disktransport.Query(bucket, "")
	query.Set("prefix", request.Prefix)
	query.Set("after", request.After)
	query.Set("after_generation", strconv.FormatInt(request.AfterGeneration, 10))
	query.Set("page_size", strconv.Itoa(request.PageSize))
	response, err := c.transport.Request(ctx, http.MethodGet, "list", query, nil, nil)
	if err != nil {
		return objectstore.Page{}, err
	}
	var page objectstore.Page
	err = disktransport.Decode(response, &page)
	return page, err
}
