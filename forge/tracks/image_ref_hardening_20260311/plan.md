# Implementation Plan: Image Ref Hardening

## Phase 1: Registry Resolver Package [checkpoint: 4aea88637]

- [x] Add `github.com/google/go-containerregistry` to go.mod and verify GCP default keychain works with existing transitive deps pending
- [x] Write tests for Resolver interface: tag-to-digest resolution, auth variants (anonymous, basic auth, GCP keychain), error cases (network failure, auth failure, image not found) pending
- [x] Implement Resolver using go-containerregistry: HEAD manifest to get digest, keychain for auth (google default + basic auth from source + anonymous fallback) pending
- [x] Handle edge cases: already-pinned digests (no-op), missing tag (default to "latest"), unresolvable images (return error) pending
- [ ] Phase 1 Manual Verification

---

## Phase 2: Lidar Native Resource Type Scanning [checkpoint: 4aea88637]

- [x] Write tests for scanner resource type scanning: concurrent resolution, interval respect, error handling, skip `image:` field types pending
- [x] Implement `scanResourceTypes()` in scanner.go: iterate resource types, call resolver, store versions via `SaveVersions()` pending
- [x] Wire resolver into scanner via `NewScanner()` constructor pending
- [x] Add resource type scan to `scanner.Run()` after existing resource scan pending
- [x] Write tests for version storage: correct scope lookup, SaveVersions with digest format, duplicate version handling pending
- [x] Implement scope lookup for resource types: `FindOrCreateResourceConfig` + `FindOrCreateScope`, then `SaveVersions({"digest": "sha256:..."})` pending
- [x] Ensure `LastCheckEndTime` is updated correctly for interval tracking pending
- [ ] Phase 2 Manual Verification

---

## Phase 3: Remove Pod-Based Resource Type Checks [checkpoint: 6f3666ce4]

- [x] Write tests confirming resource types no longer produce check builds pending
- [x] Modify `TryCreateCheck` or `scanner.check()` to skip resource types (now handled by native scan pass) pending
- [x] Ensure manually-triggered resource type checks still work (via resolver, not pods) pending
- [x] Write tests for simplified `ImageForType`: all resource types resolve to ImageRef (digest from DB), no nested CheckPlan/GetPlan generation pending
- [x] Modify `ImageForType()` to look up resolved digest from DB and return `TypeImage{ImageRef: "repo@sha256:..."}` for resource types pending
- [x] Remove nested `FetchImagePlan` generation for resource type image resolution pending
- [x] Write tests confirming check steps for resource types use ImageRef path pending
- [x] Remove or simplify the FetchImage path in check_step.go for nested resource type image resolution pending
- [x] Clean up `build_step_delegate.go` `metadataFetchImage` if no longer needed pending
- [ ] Phase 3 Manual Verification

---

## Phase 4: Integration Testing [checkpoint: c6bd56e95]

- [x] Verify all existing unit tests pass (`make test-unit`) pending
- [x] Add scanner integration test: end-to-end resource type resolution with fake registry pending
- [x] Update `custom_resource_types` behavioral tests for native resolution pending
- [x] Verify resource checks still spawn pods correctly pending
- [x] Verify resource type checks do NOT spawn pods pending
- [ ] Phase 4 Manual Verification

---
