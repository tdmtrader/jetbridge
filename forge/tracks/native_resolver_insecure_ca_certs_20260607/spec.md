# Spec: Native resolver honors `insecure` and `ca_certs`

**Track ID:** `native_resolver_insecure_ca_certs_20260607`
**Type:** bug
**Status:** planned

## Overview

Now that `registry-image` checks resolve natively across all check paths
(`native_registry_image_check_resolution_20260324`, via `CheckStep.resolveNatively`
+ the Lidar scanner), the native OCI resolver — `atc/imageresolver/resolver.go` —
**ignores two `registry-image` source options that the check-pod path supported**:

- `source.insecure` (bool) — allow plain-HTTP / skip-TLS registries
- `source.ca_certs` ([]string) — trust custom/self-signed CA certificates

`registryResolver.Resolve(ctx, repository, tag, auth)` builds the reference with
`name.ParseReference(ref)` (defaults to HTTPS) and uses the default TLS transport.
There is no way to plumb `insecure` or `ca_certs` through. As a result, any
`registry-image` resource pointing at a private/self-signed/insecure registry now
**fails native resolution** (`resolving digest ...: x509: certificate signed by
unknown authority`, or HTTPS-required errors) — a regression versus the legacy
`concourse/registry-image-resource` check pod, which honored both options.

This is the residual gap spun off when closing
`native_registry_image_check_resolution_20260324`.

## Background — where the options must flow

| Site | Function | Action |
|------|----------|--------|
| `atc/exec/check_step.go` | `resolveNatively` (~352) | extract `source["insecure"]`, `source["ca_certs"]`, pass to resolver |
| `atc/lidar/scanner.go` | `resolveResource` (~334), `resolveResourceType` (~138) | same extraction from source |
| `atc/imageresolver/resolver.go` | `Resolver.Resolve` (~48) | apply `name.Insecure` and a custom-CA `remote.WithTransport` |

The resolver currently exposes only `Resolve(ctx, repository, tag, auth *BasicAuth)`.
The cleanest change is to thread the two options through (e.g. a small
`ResolveOptions{Insecure bool, CACerts []string}` param or functional options),
keeping the existing public keychain/auth behavior intact.

## Requirements

1. `source.insecure: true` makes native resolution use `name.Insecure` (and the
   corresponding HTTP scheme) so plain-HTTP / non-TLS registries resolve.
2. `source.ca_certs` (one or more PEM blocks) are added to a `x509.CertPool` and
   applied via a custom `http.Transport` (`remote.WithTransport`) so self-signed /
   private-CA registries verify.
3. Both options are honored on **all** native paths: `CheckStep.resolveNatively`,
   Lidar `resolveResource`, and `resolveResourceType`.
4. Existing behavior is unchanged when the options are absent (public registries,
   GCP Workload Identity via the multi-keychain, explicit `BasicAuth`).
5. `insecure` and `ca_certs` are mutually compatible with `username`/`password`.

## Acceptance Criteria

- [ ] A `registry-image` resource with `source.insecure: true` resolves against a
      plain-HTTP registry natively (no check pod)
- [ ] A `registry-image` resource with `source.ca_certs: [<pem>]` resolves against a
      self-signed-TLS registry natively
- [ ] Public registries and GAR (Workload Identity) still resolve with no options set
- [ ] `username`/`password` + `insecure`/`ca_certs` combine correctly
- [ ] Unit tests for the resolver cover: insecure on/off, valid/invalid CA, and the
      no-options default path
- [ ] All three native call sites pass the options through (check step + both scanner paths)
- [ ] No regression in existing `imageresolver`, `check_step`, `lidar` tests

## Out of Scope

- Per-registry mirror/proxy configuration
- mTLS client certs for registries (separate concern)
- Changing the keychain/credential chain (Workload Identity, Docker config) — unchanged
- The legacy `concourse/registry-image-resource` check-pod path (being phased out)

## Notes

- go-containerregistry: `name.ParseReference(ref, name.Insecure)` for insecure;
  `remote.WithTransport(&http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}})`
  for custom CAs. A pool built from `ca_certs` PEM via `x509.NewCertPool().AppendCertsFromPEM`.
- Verify how the legacy registry-image-resource parsed `ca_certs` (string vs []string)
  to keep pipeline-config compatibility.
