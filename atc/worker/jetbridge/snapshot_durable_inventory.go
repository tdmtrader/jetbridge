package jetbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

const (
	durableInventoryPath      = "/snapshots/v1/durable-objects"
	durableInventoryMaxBytes  = 64 << 20
	durableInventoryTimeParse = time.RFC3339Nano
)

var _ snapshot.DurableInventory = (*SnapshotContentStore)(nil)

// ListDurableObjects reads the shared durable store's inventory through one
// daemon. The Hangar bucket is global rather than per-node, so a single
// endpoint answers for the whole store and asking every daemon would only
// multiply identical listings.
func (store *SnapshotContentStore) ListDurableObjects(
	ctx context.Context,
	visit func(snapshot.DurableObject) error,
) error {
	if visit == nil {
		return fmt.Errorf("durable inventory visitor is required")
	}
	endpoint, err := store.anyDaemonEndpoint(ctx)
	if err != nil {
		return err
	}
	target, err := store.durableInventoryURL(endpoint, durableInventoryPath, "")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	client, err := store.daemon.snapshotHTTPClient()
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("list durable snapshot inventory on %s: %w", endpoint.NodeName, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf(
			"list durable snapshot inventory on %s: daemon status %d", endpoint.NodeName, response.StatusCode,
		)
	}
	var inventory struct {
		Objects []struct {
			Digest     string `json:"digest"`
			Key        string `json:"key"`
			Generation int64  `json:"generation"`
			Bytes      int64  `json:"bytes"`
			CreatedAt  string `json:"created_at"`
		} `json:"objects"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, durableInventoryMaxBytes)).Decode(&inventory); err != nil {
		return fmt.Errorf("decode durable snapshot inventory from %s: %w", endpoint.NodeName, err)
	}
	objects := make([]snapshot.DurableObject, 0, len(inventory.Objects))
	for _, listed := range inventory.Objects {
		createdAt, parseErr := time.Parse(durableInventoryTimeParse, listed.CreatedAt)
		if parseErr != nil {
			// Leave CreatedAt zero rather than substituting a time. The sweep
			// refuses an object it cannot age, which is the safe outcome.
			createdAt = time.Time{}
		}
		objects = append(objects, snapshot.DurableObject{
			Digest:     snapshot.Digest(listed.Digest),
			Key:        listed.Key,
			Generation: listed.Generation,
			Bytes:      listed.Bytes,
			CreatedAt:  createdAt.UTC(),
		})
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Digest < objects[j].Digest })
	for _, object := range objects {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(object); err != nil {
			return err
		}
	}
	return nil
}

// DeleteDurableObject removes exactly the generation that was judged.
func (store *SnapshotContentStore) DeleteDurableObject(ctx context.Context, object snapshot.DurableObject) error {
	if err := object.Validate(); err != nil {
		return err
	}
	endpoint, err := store.anyDaemonEndpoint(ctx)
	if err != nil {
		return err
	}
	target, err := store.durableInventoryURL(
		endpoint,
		durableInventoryPath+"/"+object.Digest.String(),
		url.Values{"generation": []string{strconv.FormatInt(object.Generation, 10)}}.Encode(),
	)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	client, err := store.daemon.snapshotHTTPClient()
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("delete durable snapshot %s: %w", object.Digest, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotFound {
		return fmt.Errorf(
			"delete durable snapshot %s: daemon status %d", object.Digest, response.StatusCode,
		)
	}
	return nil
}

// durableInventoryURL addresses a daemon control route rather than the
// artifact namespace, so it cannot reuse the artifact key builder.
func (store *SnapshotContentStore) durableInventoryURL(
	endpoint DaemonEndpoint,
	path string,
	query string,
) (string, error) {
	if strings.TrimSpace(endpoint.Address) == "" || strings.TrimSpace(endpoint.NodeName) == "" {
		return "", fmt.Errorf("daemon endpoint requires node name and address")
	}
	target, err := url.Parse(store.daemon.routeURL(endpoint.Address, path))
	if err != nil {
		return "", err
	}
	target.RawQuery = query
	return target.String(), nil
}

func (store *SnapshotContentStore) anyDaemonEndpoint(ctx context.Context) (DaemonEndpoint, error) {
	endpoints, err := store.daemon.DaemonEndpoints(ctx)
	if err != nil {
		return DaemonEndpoint{}, err
	}
	if len(endpoints) == 0 {
		return DaemonEndpoint{}, fmt.Errorf("no identified artifact daemon endpoints available")
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].NodeName < endpoints[j].NodeName })
	return endpoints[0], nil
}
