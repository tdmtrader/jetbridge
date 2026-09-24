// Package diskdelete exposes the conditional-deletion capability separately.
// Only the reclaimer constructs this client.
package diskdelete

import (
	"context"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/hangar/internal/disktransport"
	"github.com/concourse/concourse/hangar/objectstore"
)

type Config = disktransport.Config
type client struct{ transport *disktransport.Transport }

func New(config Config) (objectstore.DeleteClient, error) {
	t, err := disktransport.New(config)
	if err != nil {
		return nil, err
	}
	return &client{t}, nil
}
func (c *client) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
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
func (c *client) DeleteExact(ctx context.Context, bucket, key string, generation int64) error {
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
