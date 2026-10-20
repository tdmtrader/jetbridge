package creds

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/tracing"
	"github.com/patrickmn/go-cache"
	"go.opentelemetry.io/otel/attribute"
)

type SecretCacheConfig struct {
	Enabled          bool          `long:"secret-cache-enabled" description:"Enable in-memory cache for secrets"`
	Duration         time.Duration `long:"secret-cache-duration" default:"1m" description:"If the cache is enabled, secret values will be cached for not longer than this duration (it can be less, if underlying secret lease time is smaller)"`
	DurationNotFound time.Duration `long:"secret-cache-duration-notfound" default:"10s" description:"If the cache is enabled, secret not found responses will be cached for this duration"`
	PurgeInterval    time.Duration `long:"secret-cache-purge-interval" default:"10m" description:"If the cache is enabled, expired items will be removed on this interval"`
}

type CachedSecrets struct {
	secrets     Secrets
	cacheConfig SecretCacheConfig
	cache       *cache.Cache

	// keyedOnParams is set when the provider's value for a path depends on
	// the caller's SecretLookupParams (idtoken signs a token naming the
	// caller's team, pipeline, instance vars and job under the bare path
	// "token"). Such entries are keyed on the path and the params, so one
	// caller is never served another's value.
	keyedOnParams bool
}

type cacheEntry struct {
	value      any
	expiration *time.Time
	found      bool
}

func NewCachedSecrets(secrets Secrets, cacheConfig SecretCacheConfig) *CachedSecrets {
	// Create a cache with:
	// - default expiration time for entries set to 'cacheConfig.Duration'
	// - purges expired items regularly, on every `cacheConfig.PurgeInterval` after creation
	return &CachedSecrets{
		secrets:       secrets,
		cacheConfig:   cacheConfig,
		cache:         cache.New(cacheConfig.Duration, cacheConfig.PurgeInterval),
		keyedOnParams: providerReadsParams(secrets),
	}
}

// providerReadsParams reports whether the provider beneath any retry wrapper
// implements SecretsWithParams itself. A provider that only implements Get
// never sees the params, so its value depends on the path alone and its
// entries can be shared by every caller asking for that path.
func providerReadsParams(secrets Secrets) bool {
	for {
		switch wrapper := secrets.(type) {
		case *RetryableSecrets:
			secrets = wrapper.secrets
		default:
			_, readsParams := secrets.(SecretsWithParams)
			return readsParams
		}
	}
}

// cacheKey is the entry a lookup reads and writes; ok is false when the
// params cannot be encoded, and the lookup then goes uncached.
func (cs *CachedSecrets) cacheKey(secretPath string, lookupCtx SecretLookupParams) (string, bool) {
	if !cs.keyedOnParams {
		return secretPath, true
	}
	params, err := json.Marshal(lookupCtx)
	if err != nil {
		return "", false
	}
	return secretPath + "\x00" + string(params), true
}

func (cs *CachedSecrets) Get(secretPath string) (any, *time.Time, bool, error) {
	return cs.GetWithParams(secretPath, SecretLookupParams{})
}

func (cs *CachedSecrets) NewSecretLookupPaths(teamName string, pipelineName string, allowRootPath bool) []SecretLookupPath {
	return cs.NewSecretLookupPathsWithParams(SecretLookupParams{Team: teamName, Pipeline: pipelineName}, allowRootPath)
}

func (cs *CachedSecrets) GetWithParams(secretPath string, lookupCtx SecretLookupParams) (any, *time.Time, bool, error) {
	ctx, span := tracing.StartSpan(context.Background(), "creds.lookup", tracing.Attrs{
		"secret.path": secretPath,
	})
	_ = ctx

	key, cacheable := cs.cacheKey(secretPath, lookupCtx)
	if !cacheable {
		value, expiration, found, err := GetWithParams(cs.secrets, secretPath, lookupCtx)
		tracing.End(span, err)
		return value, expiration, found, err
	}

	// if there is a corresponding entry in the cache, return it
	entry, found := cs.cache.Get(key)
	if found {
		result := entry.(cacheEntry)
		span.SetAttributes(attribute.String("cache.hit", "true"))
		tracing.End(span, nil)
		return result.value, result.expiration, result.found, nil
	}

	span.SetAttributes(attribute.String("cache.hit", "false"))

	// otherwise, let's make a request to the underlying secret manager
	value, expiration, found, err := GetWithParams(cs.secrets, secretPath, lookupCtx)

	// we don't want to cache errors, let the errors be retried the next time around
	if err != nil {
		tracing.End(span, err)
		return nil, nil, false, err
	}

	span.SetAttributes(attribute.String("secret.found", fmt.Sprintf("%t", found)))

	// here we want to cache secret value, expiration, and found flag too
	// meaning that "secret not found" responses will be cached too!
	entry = cacheEntry{value: value, expiration: expiration, found: found}

	if found {
		// take default cache ttl
		duration := cs.cacheConfig.Duration
		if expiration != nil {
			// if secret lease time expires sooner, make duration smaller than default duration
			// also if the duration is less than or equal to 0, use default duration (it would cache forever)
			itemDuration := time.Until(*expiration)
			if itemDuration < duration && itemDuration > 0 {
				duration = itemDuration
			}
		}
		cs.cache.Set(key, entry, duration)
	} else {
		cs.cache.Set(key, entry, cs.cacheConfig.DurationNotFound)
	}

	tracing.End(span, nil)
	return value, expiration, found, nil
}

func (cs *CachedSecrets) NewSecretLookupPathsWithParams(context SecretLookupParams, allowRootPath bool) []SecretLookupPath {
	return NewSecretLookupPathsWithParams(cs.secrets, context, allowRootPath)
}
