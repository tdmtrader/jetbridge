# Implementation Plan: Native resolver honors `insecure` and `ca_certs`

> Spun off from `native_registry_image_check_resolution_20260324` (closed 2026-06-07).
> TDD: write the failing resolver test first, then plumb options through the call sites.

## Phase 1: Resolver-level support (TLS transport + insecure)

- [ ] Task: Add `ResolveOptions{Insecure bool, CACerts []string}` (or functional opts) and a `Resolve` variant that accepts them in `atc/imageresolver/resolver.go`; keep the existing `Resolve(ctx, repo, tag, auth)` behavior for the no-options default
- [ ] Task: When `Insecure`, parse the ref with `name.Insecure`; when `CACerts` non-empty, build an `x509.CertPool` and apply `remote.WithTransport` with a `tls.Config{RootCAs: pool}`
- [ ] Task: Red→Green unit tests — insecure plain-HTTP registry resolves; self-signed-TLS registry resolves with a valid CA and fails with an invalid/empty CA; no-options default path unchanged (use `httptest` registries)

## Phase 2: Plumb options from source through native call sites

- [ ] Task: `CheckStep.resolveNatively` (`check_step.go`) — extract `source["insecure"]` and `source["ca_certs"]`, pass to the resolver
- [ ] Task: Lidar `resolveResource` and `resolveResourceType` (`scanner.go`) — same extraction from source
- [ ] Task: Decide `ca_certs` source shape (string vs []string) to match legacy `registry-image-resource` pipeline config; normalize in one helper
- [ ] Task: Unit tests for each call site verifying the options reach the resolver (fake resolver capturing opts)

## Phase 3: Verification

- [ ] Task: Confirm public registries + GAR (Workload Identity) still resolve with no options set (no regression)
- [ ] Task: `username`/`password` + `insecure`/`ca_certs` combine correctly
- [ ] Task: Run `imageresolver`, `atc/exec` (check_step), and `atc/lidar` unit suites green
- [ ] Task: (Optional, live) verify against a self-signed registry on theborg if one is available

## Key code locations

| File | What |
|------|------|
| `atc/imageresolver/resolver.go` | `Resolver.Resolve` (~48), `NewResolver` (~38) |
| `atc/exec/check_step.go` | `resolveNatively` (~352) |
| `atc/lidar/scanner.go` | `resolveResource` (~334), `resolveResourceType` (~138) |
