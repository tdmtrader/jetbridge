package disk

// The client half of the disk store: the wire adapters over the store's
// authenticated HTTP server. NewClient exposes only non-destructive
// operations; NewDeleteClient is the separate conditional-deletion capability,
// whose callers are fixed by hangar/architecture_test.go.

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

// ClientConfig names the disk store a client reaches and the role credential it
// presents.
type ClientConfig = disktransport.Config

type client struct{ transport *disktransport.Transport }

// NewClient opens the create/stat/read/list adapter. It carries no delete.
func NewClient(config ClientConfig) (objectstore.Client, error) {
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

type deleteClient struct{ transport *disktransport.Transport }

// NewDeleteClient opens the conditional-deletion capability. The server
// authorizes it per role and namespace; this constructor's callers are fixed by
// hangar/architecture_test.go.
func NewDeleteClient(config ClientConfig) (objectstore.DeleteClient, error) {
	t, err := disktransport.New(config)
	if err != nil {
		return nil, err
	}
	return &deleteClient{t}, nil
}

func (c *deleteClient) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	if generation <= 0 {
		return objectstore.Attrs{}, objectstore.ErrPreconditionFailed
	}
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
func (c *deleteClient) DeleteExact(ctx context.Context, bucket, key string, generation int64) error {
	if generation <= 0 {
		return objectstore.ErrPreconditionFailed
	}
	query := disktransport.Query(bucket, key)
	query.Set("generation", strconv.FormatInt(generation, 10))
	response, err := c.transport.Request(ctx, http.MethodDelete, "delete", query, nil, nil)
	if err != nil {
		return err
	}
	return response.Body.Close()
}
