# Spec: Auth Session Lifetime & Refresh Tokens

## Overview

Concourse users are forced to re-login frequently because the auth system issues one-shot opaque access tokens with no refresh mechanism. The current implementation discards Dex's OIDC tokens entirely, replacing them with custom DB-backed opaque tokens — essentially a bespoke session store that duplicates what OIDC already provides.

This track replaces the custom token system with standard OIDC JWT validation against Dex's JWKS, adds refresh token support via Dex's native refresh flow, and eliminates the `access_tokens` table, claims cache, custom token generator, and GC collector.

## Problem

- Users must re-authenticate after `--auth-duration` expires (default 24h)
- No refresh token flow — token expiry = full re-login via browser OAuth dance
- The current architecture replaces Dex's signed JWTs with custom opaque tokens stored in PostgreSQL, requiring a DB/cache lookup on every API request
- `UnsafeClaimsWithoutVerification` parses Dex's ID token without signature validation
- The only mitigation for short sessions is increasing `--auth-duration`, widening the stolen-token attack window
- OAuth state token has a hardcoded 1-hour timeout that cannot be configured
- Fly CLI sessions expire with no renewal path

## Architecture Change

### Current flow (DB-backed opaque tokens)

```
Dex issues ID token (signed JWT) + access token + refresh token
  → StoreAccessToken intercepts response
  → Parses ID token WITHOUT signature verification
  → Generates random opaque token (20 bytes + 8-byte expiry)
  → Stores opaque token + claims in access_tokens table
  → Replaces Dex's access token with opaque token
  → Client holds opaque token, every API call = DB/cache lookup
```

### New flow (standard OIDC JWT validation)

```
Dex issues ID token (signed JWT) + refresh token
  → Client receives Dex's ID token as the bearer token (auth cookie / ~/.flyrc)
  → Client receives Dex's refresh token in a separate cookie / ~/.flyrc
  → Dex stores refresh token internally (its own Postgres-backed storage)
  → Every API call: validate JWT signature against Dex's JWKS (/sky/issuer/keys)
  → Extract claims directly from JWT (no DB lookup)
  → RBAC: claims + team config (already fetched from DB) = roles
```

### Refresh flow

```
Access token (short-lived, e.g. 15min) expires
  → Client sends refresh token to /sky/token/refresh
  → ATC exchanges refresh token with Dex (grant_type=refresh_token)
  → Dex refreshes against Microsoft (if upstream token expired)
  → New ID token + new refresh token returned
  → Client gets new bearer token, continues without re-login
```

## Requirements

1. Validate Dex's ID tokens using JWKS signature verification (replace `UnsafeClaimsWithoutVerification`)
2. Use Dex's ID token directly as the bearer token (eliminate opaque token generation)
3. Dex manages refresh token storage internally (no Concourse-side refresh token table)
4. Client holds refresh token in cookie (web) or `~/.flyrc` (fly)
5. Add `/sky/token/refresh` endpoint that proxies refresh requests to Dex
6. Web UI should transparently refresh tokens before expiry (no user-visible re-login)
7. Fly CLI should transparently refresh tokens when the access token is near expiry
8. Logout revokes refresh token via Dex and clears client cookies
9. Short access token lifetime configurable via Dex's `IDTokensValidFor` (default: 15min)
10. Remove the `access_tokens` table, claims cache, custom token generator, and associated GC

## Key Files

| File | Current Role | Change |
|------|-------------|--------|
| `skymarshal/token/access_token.go` | Intercepts Dex, generates opaque tokens | Remove entirely |
| `skymarshal/skyserver/skyserver.go` | Login/logout/callback handlers | Pass through Dex's ID token; store refresh token in cookie; add refresh endpoint; revoke via Dex on logout |
| `skymarshal/token/middleware.go` | Cookie management | Store JWT in auth cookie + refresh token in separate cookie |
| `atc/api/accessor/verifier.go` | DB lookup per request | Replace with JWKS-based JWT signature validation |
| `atc/api/accessor/claims_cacher.go` | LRU cache for DB tokens | Remove entirely |
| `atc/db/access_token_factory.go` | CRUD for opaque tokens | Remove entirely (Dex manages refresh tokens) |
| `atc/db/access_token_lifecycle.go` | Expire opaque tokens | Remove entirely |
| `atc/gc/access_tokens_collector.go` | GC for opaque tokens | Remove entirely (Dex manages refresh token cleanup) |
| `skymarshal/dexserver/dexserver.go` | Dex config | Adjust `IDTokensValidFor` to short duration |
| `fly/commands/login.go` | Saves token to ~/.flyrc | Store both ID token and refresh token |

## Acceptance Criteria

- [ ] ID tokens are validated via JWKS signature verification (no `UnsafeClaimsWithoutVerification`)
- [ ] No DB lookup required for API request authentication (stateless JWT validation)
- [ ] Users stay logged in as long as Dex's refresh token is valid (configured in Dex/upstream provider)
- [ ] Access tokens are short-lived (15min default) reducing stolen-token attack window
- [ ] Web UI transparently refreshes without page reload or user interaction
- [ ] Fly CLI transparently refreshes without requiring `fly login` again
- [ ] Logout revokes refresh token via Dex and clears cookies
- [ ] `access_tokens` table, claims cache, custom token generator, and GC collector are all removed
- [ ] Backwards compatible — users on old tokens get a 401 and re-login once after upgrade
- [ ] RBAC continues to work identically (claims from JWT + team config from DB)

## Out of Scope

- Changes to Dex itself (we only configure it, still using `concourse/dex` fork)
- Migrating from `concourse/dex` fork to upstream `dexidp/dex` (separate follow-up track — the fork's `NewServerWithKey` is no longer needed once JWKS validation replaces `UnsafeClaimsWithoutVerification`, but the migration has its own blast radius)
- Multi-device session management
- Token binding to client fingerprint
- SSO single-logout across multiple Concourse instances
- Removing the team config DB lookup (still needed for dynamic team creation)
