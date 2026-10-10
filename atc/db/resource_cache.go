package db

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/lock"
)

var ErrResourceCacheDisappeared = errors.New("resource-cache-disappeared")

// ResourceCache represents an instance of a ResourceConfig's version.
//
// A ResourceCache is created by a `get`, an `image_resource`, or a resource
// type in a pipeline.
//
// ResourceCaches are garbage-collected by gc.ResourceCacheCollector.

type ResourceCache interface {
	ID() int
	Version() atc.Version

	ResourceConfig() ResourceConfig

	// ContentKey names this cache by its content, for storage that has to
	// outlive the row. See resourceCacheContentKey. Empty for caches created before the
	// column existed; those use the legacy row-ID alias.
	ContentKey() string

	Destroy(Tx) (bool, error)
	BaseResourceType() *UsedBaseResourceType
}

type resourceCache struct {
	id             int
	resourceConfig ResourceConfig
	version        atc.Version
	contentKey     string

	lockFactory lock.LockFactory
	conn        DbConn
}

func (cache *resourceCache) ID() int                        { return cache.id }
func (cache *resourceCache) ResourceConfig() ResourceConfig { return cache.resourceConfig }
func (cache *resourceCache) Version() atc.Version           { return cache.version }
func (cache *resourceCache) ContentKey() string             { return cache.contentKey }

func (cache *resourceCache) Destroy(tx Tx) (bool, error) {
	rows, err := psql.Delete("resource_caches").
		Where(sq.Eq{
			"id": cache.id,
		}).
		RunWith(tx).
		Exec()
	if err != nil {
		return false, err
	}

	affected, err := rows.RowsAffected()
	if err != nil {
		return false, err
	}

	if affected == 0 {
		return false, ErrResourceCacheDisappeared
	}

	return true, nil
}

func (cache *resourceCache) BaseResourceType() *UsedBaseResourceType {
	if cache.resourceConfig.CreatedByBaseResourceType() != nil {
		return cache.resourceConfig.CreatedByBaseResourceType()
	}

	return cache.resourceConfig.CreatedByResourceCache().BaseResourceType()
}

// resourceCacheContentKey names a resource cache by what it holds rather than by which
// row happens to hold it.
//
// resource_caches.id is a sequence, and it is the wrong name for anything meant
// to outlive the row. CleanUpInvalidCaches deletes a cache the moment nothing
// references it — while node-local aliases can still exist — and
// the next build re-inserts the same tuple under a fresh id. Anything filed
// under the old id is then unreachable and unreclaimable. Restoring a database
// backup makes it worse: the sequence rewinds, and a re-minted id can name a
// different cache entirely, so a lookup returns the wrong bytes rather than no
// bytes. A UUID would fix the collision and not the miss: it is still minted per
// row, so it still changes on delete-and-recreate, leaving a safe store that
// never hits.
//
// The inputs are exactly what makes two caches interchangeable, so the key is
// stable across delete-and-recreate and identical on any cluster asking for the
// same thing.
//
// parent carries the custom resource type this cache was fetched with, and it
// contributes its own key recursively. Do not substitute BaseResourceType()
// here: that flattens the whole custom-type chain to its base, so two different
// versions of a custom type with identical source and params would collide —
// the same wrong-bytes bug by a shorter route.
func resourceCacheContentKey(resourceTypeName string, source atc.Source, params atc.Params, version atc.Version, parent ResourceCache) string {
	parentKey := ""
	if parent != nil {
		parentKey = parent.ContentKey()
		if parentKey == "" {
			// A parent predating the column cannot be named, so neither can
			// anything fetched with it. Better no key than an ambiguous one.
			return ""
		}
	}

	marshaledVersion, _ := json.Marshal(version)

	// NUL-separated: none of the components can contain one, so no arrangement
	// of a type name and a hash can be read as a different arrangement.
	identity := strings.Join([]string{
		parentKey,
		resourceTypeName,
		mapHash(source),
		fmt.Sprintf("%x", sha256.Sum256(marshaledVersion)),
		paramsHash(params),
	}, "\x00")

	return fmt.Sprintf("rc-%x", sha256.Sum256([]byte(identity)))
}

func mapHash(m map[string]any) string {
	j, _ := json.Marshal(m)
	return fmt.Sprintf("%x", sha256.Sum256(j))
}

func paramsHash(p atc.Params) string {
	if p != nil {
		return mapHash(p)
	}

	return mapHash(atc.Params{})
}
