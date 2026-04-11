# Plan: Standard OIDC JWT Validation & Refresh Tokens

## Phase 1: JWKS-Based JWT Verification

Replace the DB-backed opaque token verifier with standard JWT signature validation.

- [x] Implement JWKS fetcher that caches Dex's public keys from `/sky/issuer/keys`
- [x] Write tests for JWKS fetcher (key caching, rotation, fetch failure)
- [x] Replace `verifier.go` to validate JWT signature (RS256) and extract claims from token
- [x] Write tests for new verifier (valid token, expired, bad signature, wrong audience, missing claims)
- [x] Verify existing RBAC accessor works unchanged with claims from verified JWT

## Phase 2: Pass Through Dex's ID Token & Refresh Token

Replace `StoreAccessToken` interception with direct token pass-through. Dex manages refresh token storage internally via its own Postgres-backed storage.

- [x] Remove `StoreAccessToken` middleware from the `/sky/issuer/token` handler chain
- [x] Update `skyserver.go` callback to set Dex's ID token directly in the auth cookie
- [x] Store Dex's refresh token in a separate cookie (HttpOnly, Secure, SameSite=Lax)
- [x] Update cookie middleware to handle JWT-sized tokens
- [x] Write tests for the updated callback flow (ID token in auth cookie, refresh token in refresh cookie)
- [x] Configure `IDTokensValidFor` in `dexserver.go` to use configured duration
- [x] Wire JWKS verifier in `constructTokenVerifier` using Dex's `/sky/issuer/keys` endpoint

## Phase 3: Token Refresh Endpoint

Add `/sky/token/refresh` that proxies refresh requests to Dex. Dex handles refresh token validation, rotation, and upstream Microsoft refresh internally.

- [x] Implement `/sky/token/refresh` endpoint in `skyserver.go`
- [x] Write tests for refresh endpoint (happy path, expired refresh, revoked refresh, Dex error, method not allowed)

## Phase 4: Web UI Transparent Refresh

- [x] Add fetch-based interceptor in `elm-setup.js` that attempts refresh on 401 before redirecting to login

## Phase 5: Fly CLI Transparent Refresh

- [x] Add `RefreshToken` field to `TargetToken` struct in `fly/rc/targets.go`
- [x] Implement `refreshingTokenSource` that checks JWT expiry and calls `/sky/token/refresh`
- [x] Wire refreshing token source into `defaultHttpClient` when refresh token is available
- [x] Update `passwordGrant` to capture and store refresh token + `offline_access` scope
- [x] Update fly integration tests for new scope

## Phase 6: Logout & Cleanup

- [x] Simplified logout to just clear cookies (auth, CSRF, refresh) — no DB operations
- [x] Replaced `StoreAccessToken` with `EnsureUser` middleware (user creation only, no token replacement)
- [x] Removed `claimsCacher` and `dbAccessTokenFactory` from wiring
- [x] Added `offline_access` scope to OAuth config for refresh token support

## Phase 7: Integration Testing & Migration

- [x] All JWKS verifier tests pass (10 specs)
- [x] All skyserver tests pass (90 specs including refresh endpoint)
- [x] All token package tests pass (14 specs)
- [x] All fly login integration tests pass (32 specs)
- [x] Full project compiles cleanly
- [x] go vet clean (no new warnings)
