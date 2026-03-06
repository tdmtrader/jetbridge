# CGX: Configurable Base Resource Types + Metadata-Only FetchImage

## Frustrations & Friction

_(Capture friction points during implementation)_

## Good Patterns to Encode

- FetchImage analysis revealed it's purely a container image reference mechanism on K8s — never downloads content
- `imageURLFromSource` already does the URL construction; just needs to be called without the pod chain
- Lidar scanner already populates `resource_cache_versions` — metadata-only FetchImage just reads from that cache

## Anti-Patterns to Prevent

- Spawning pods for operations that are pure metadata on K8s
- Hardcoding resource type lists without operator override capability

## Missing Capabilities

_(Capture during implementation)_

## Improvement Candidates

_(Capture during implementation)_
