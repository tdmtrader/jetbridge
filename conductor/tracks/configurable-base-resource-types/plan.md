# Plan: Configurable Base Resource Types + Metadata-Only FetchImage

## Phase 1: CLI flag for base resource type overrides

Add a repeatable `--base-resource-type` flag that lets operators specify additional or replacement base resource type images. These merge into `DefaultResourceTypeImages`.

- [x] Write tests for `--base-resource-type` flag parsing
  - Test: `--base-resource-type git=my-registry/git` overrides the default git image
  - Test: `--base-resource-type custom-type=my-registry/custom` adds a new base type
  - Test: multiple flags merge correctly, last-wins for duplicates
  - Test: existing types without overrides retain their defaults
- [x] Implement `--base-resource-type` flag
  - Add flag to `ATCCommand` in `atc/atccmd/command.go`
  - Merge operator types into `Config.ResourceTypeImages` during startup
  - Pass merged map to jetbridge `Config` and worker registration

---

## Phase 2: Metadata-only FetchImage on K8s

Replace the pod-spawning FetchImage chain with a pure metadata operation. When a custom type's latest version is already cached in the DB (populated by lidar), construct the ImageURL and ResourceCache without pods.

- [x] Write tests for cached version lookup 3b00a64
  - Test: custom type with cached version returns correct digest from DB
  - Test: custom type with no cached version returns nil (triggers fallback)
  - Test: nested custom types (type A depends on type B) resolve recursively via DB
- [x] Implement cached version lookup 3b00a64
  - Add method to resolve latest cached version for a resource type from `resource_cache_versions`
  - Use the type's source config + base type to find the matching `resource_config` and its cached versions
- [x] Write tests for metadata-only FetchImage path 3b00a64
  - Test: FetchImage with cached version returns ImageSpec with ImageURL, no plans executed
  - Test: FetchImage without cached version falls back to running check+get plans
  - Test: returned ResourceCache is correctly chained (custom type → base type)
  - Test: imageURLFromSource works for `produces: registry-image` types
- [x] Implement metadata-only FetchImage 3b00a64
  - Add `metadataFetchImage` method to `buildStepDelegate`
  - Look up cached version from DB via ResourceConfigFactory → ResourceConfig → scope → LatestVersion
  - If found: construct ImageURL via `imageURLFromSource`, find/create ResourceCache, return immediately
  - If not found: fall back to existing `FetchImage` (run check+get plans)
  - Wire factories through DelegateFactory → stepperFactory for all delegate types

---

## Phase 3: Extend imageURLFromSource for custom types

Extend `imageURLFromSource` to construct URLs for any type that produces registry-compatible images, not just literal `registry-image` type.

- [ ] Write tests for extended imageURLFromSource
  - Test: type with `produces: registry-image` gets a docker:/// URL
  - Test: nested custom type resolving to registry-image gets correct URL
  - Test: type that does NOT produce registry-image returns empty string (uses ResourceType fallback)
- [ ] Implement extended imageURLFromSource
  - Accept `produces` field alongside `resourceType` parameter
  - Construct URL for any type whose output is registry-compatible
  - Wire `produces` metadata through the FetchImage call chain

---

## Phase 4: Integration and pipeline verification

- [ ] Write integration tests
  - Test: custom resource type check resolves image via metadata-only path (no image check/get pods)
  - Test: task step with `image_resource:` using custom type resolves via metadata-only path
  - Test: pipeline with nested custom types works correctly
  - Test: first-run scenario (empty cache) falls back to pod-based FetchImage then switches to metadata on next run
- [ ] Verify on concourse.home pipeline
  - Deploy with `--base-resource-type` flag for custom overrides
  - Confirm reduced pod count (no check/get pods for type image resolution)
  - Confirm resources and tasks work correctly with metadata-resolved images

---
