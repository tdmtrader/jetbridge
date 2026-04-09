# Plan: DaemonSet Artifact Security Hardening

## Phase 1: Daemon TLS Server + mTLS Middleware

**Files:** `cmd/artifact-daemon/main.go`, `cmd/artifact-daemon/server.go`, NEW `cmd/artifact-daemon/tls.go`

- [x] Write test: daemon starts HTTPS when `--tls-cert`, `--tls-key`, `--tls-ca-cert` provided
- [x] Write test: daemon starts HTTP when no TLS flags (backwards compat)
- [x] Write test: mTLS middleware returns 401 on protected paths without client cert
- [x] Write test: mTLS middleware allows protected paths with valid client cert
- [x] Write test: exempt paths (`/healthz`, `/resolve`, `/resolve-batch`) work without client cert
- [x] Implement `tls.go` — config builder reusing `atc.DefaultTLSConfig()`, `VerifyClientCertIfGiven`
- [x] Implement `requireClientCert` middleware in `server.go` — wraps protected routes, checks `r.TLS.PeerCertificates`
- [x] Add TLS flags to `main.go`, conditional `ListenAndServeTLS` vs `ListenAndServe`

## Phase 2: ATC DaemonClient mTLS

**Files:** `atc/worker/jetbridge/daemon_client.go`, `atc/worker/jetbridge/config.go`, `atc/atccmd/command.go`

- [x] Write test: DaemonClient uses HTTPS with client cert when TLS config present
- [x] Write test: DaemonClient uses HTTP when no TLS config (backwards compat)
- [x] Add `ArtifactDaemonTLSCert`, `ArtifactDaemonTLSKey`, `ArtifactDaemonTLSCACert` to `Config`
- [x] Update `NewDaemonClient` to build `http.Transport` with mTLS when configured
- [x] Update URL construction: `https://` when TLS, `http://` otherwise
- [x] Add `--kubernetes-artifact-daemon-tls-{cert,key,ca-cert}` flags to `atc/atccmd/command.go`
- [x] Wire flags to Config and DaemonClient construction

## Phase 3: Peer Resolver mTLS

**Files:** `cmd/artifact-daemon/peers.go`

- [x] Write test: peer probe uses mTLS when TLS configured
- [x] Write test: peer fetch uses mTLS when TLS configured
- [x] Write test: peers fall back to HTTP when no TLS config
- [x] Update `NewPeerResolver` to accept TLS config
- [x] Build mTLS transport for both probe client (10s) and fetch client (3m)
- [x] Update peer URL construction: `https://` when TLS

## Phase 4: Init Container HTTPS with CA Cert Mount

**Files:** `atc/worker/jetbridge/storage_daemonset.go`, `atc/worker/jetbridge/config.go`

- [x] Write test: init container command uses `https://` when TLS enabled
- [x] Write test: init container has CA cert volume mount and `SSL_CERT_FILE` env var when TLS enabled
- [x] Write test: init container command uses `http://` and no cert mount when TLS disabled
- [x] Add `ArtifactDaemonTLSEnabled bool` and `ArtifactDaemonTLSCACert string` to Config
- [x] Update `BuildFetchInitContainers` — add CA cert volume mount and `SSL_CERT_FILE` env var
- [x] Update `daemonResolveCommand` — conditional `https://` URL scheme
- [x] Update `daemonResolveBatchCommand` — same change
- [x] Update `BuildCleanupInitContainer` — verified: does not call daemon, no TLS changes needed

## Phase 5: Helm Chart — TLS Secret & Wiring

**Files:** `deploy/chart/values.yaml`, NEW `deploy/chart/templates/artifact-daemon-tls-secret.yaml`, `deploy/chart/templates/artifact-daemon-daemonset.yaml`, `deploy/chart/templates/web-deployment.yaml`

- [x] Add `artifactDaemon.tls.enabled`, `artifactDaemon.tls.existingSecret` to values.yaml
- [x] Create TLS secret template — `genCA` + `genSignedCert` with `lookup` for persistence
- [x] Update DaemonSet template — mount TLS secret, pass `--tls-*` flags, `scheme: HTTPS` on probes
- [x] Update web deployment template — mount TLS secret (client cert portion), pass `--kubernetes-artifact-daemon-tls-*` flags
- [x] Write Helm template test: TLS enabled renders secret + volume mounts + flags
- [x] Write Helm template test: TLS disabled renders unchanged

## Phase 6: SecurityContext & NetworkPolicy

**Files:** `deploy/chart/templates/artifact-daemon-daemonset.yaml`, NEW `deploy/chart/templates/artifact-daemon-networkpolicy.yaml`

- [x] Add pod SecurityContext to DaemonSet (runAsNonRoot, runAsUser 65534, fsGroup 65534)
- [x] Add container SecurityContext (allowPrivilegeEscalation false, drop ALL, seccomp RuntimeDefault)
- [ ] Verify daemon works as non-root with hostPath writes
- [x] Create artifact daemon NetworkPolicy template (ingress from Concourse-labeled pods)
- [x] Add `artifactDaemon.networkPolicy.enabled` value

## Phase 7: Integration Testing

- [ ] Deploy to KinD with TLS enabled, run pipeline with cross-node artifact passing
- [ ] Verify `curl --cert/--key/--cacert` to protected endpoints succeeds
- [ ] Verify `curl` without client cert to protected endpoints gets 401
- [ ] Verify `curl` without client cert to `/resolve` succeeds (exempt path, server cert verified)
- [ ] Verify health probes work with `scheme: HTTPS`
- [ ] Verify init container wget verifies server cert against mounted CA (fails with wrong CA)
- [ ] Deploy without TLS flags — verify unchanged HTTP behavior
