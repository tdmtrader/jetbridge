# Spec: Release Versioning for Fly and JetBridge Image

**Track ID:** `version_bumps_20260327`
**Type:** feature

## Overview

The JetBridge fork currently ships binaries with `0.0.0-dev` baked in because the CI pipeline doesn't inject version via ldflags. The `init()` fallback in `versions.go` masks this locally, but built images and fly binaries don't carry a real version. This track establishes a proper semver release flow so that:

- Every release has a deterministic version baked into both `concourse` and `fly` binaries
- The web UI footer displays the correct version
- `fly` version sync works correctly against the deployed server
- Git commits on main are tagged with the release version

## Requirements

1. **VERSION file as source of truth** — A `VERSION` file at repo root contains the current semver. All version injection reads from this file.
2. **Build-time version injection** — CI pipeline reads VERSION and passes it via ldflags when building both `concourse` and `fly` binaries.
3. **versions.go cleanup** — Remove the `init()` override. `Version` should only be set by ldflags (defaulting to `0.0.0-dev` for local dev).
4. **Git tagging** — After successful image build and push, CI tags the commit on main as `v<version>`.
5. **Post-release version bump** — After tagging, CI bumps the patch version in VERSION and commits back to jetbridge.
6. **Helm chart sync** — `appVersion` in Chart.yaml updated to match VERSION.
7. **fly/server version parity** — Both binaries receive the same version string.

## Acceptance Criteria

- [ ] `VERSION` file exists at repo root with current semver
- [ ] `versions.go` no longer has `init()` override
- [ ] CI builds inject version via ldflags for both concourse and fly
- [ ] Built image reports correct version via `/api/v1/info`
- [ ] `fly --version` reports the same version as the server
- [ ] UI footer displays correct JetBridge version
- [ ] Git tag `v<version>` is created on main after successful build
- [ ] VERSION file is bumped to next patch after release
- [ ] Helm chart `appVersion` matches the release version
- [ ] Local dev builds still default to `0.0.0-dev`

## Out of Scope

- Changelog generation or release notes
- GitHub Releases (just git tags)
- Major/minor version bump automation (manual decision)
- fly sync download endpoint changes
