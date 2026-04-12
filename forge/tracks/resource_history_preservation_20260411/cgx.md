# CGX: resource_history_preservation_20260411

## Session Log

### 2026-04-11 — Discovery and spec creation
- User identified core problem: changing resource type or source params destroys all version history
- Researched `resource_config_scope` lifecycle, CASCADE deletion mechanics, build-version linkage
- Key finding: build inputs reference versions by `(resource_id, version_digest)`, resolved through `resources.resource_config_scope_id = versions.resource_config_scope_id`
- Copying versions to new scope with matching digests makes build history resolve correctly
- Pins survive because `resource_pins` stores version JSON keyed by resource_id only
- Chose soft-delete + explicit fly command approach over implicit set-pipeline magic
- `next_build_inputs` FK staleness is transient (scheduler self-heals)
