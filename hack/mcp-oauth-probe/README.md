# Real Codex OAuth probe

Codex CLI **0.153.1** passed real OAuth enrollment, grouped reads, automatic refresh,
refresh after process restart, revocation, and new-consent re-listing against the
existing isolated Dex/PostgreSQL/API fixture. The final-candidate cohort ran
September 15, 2026, 04:52:32–04:53:05 UTC (September 14, 21:52–21:53 PDT), after
the production source was frozen. Its recorded build-input hashes matched that
frozen tree after verification.

These are **direct Codex app-server client calls**, with ephemeral tasks and no
model turns or inference tokens. They complement the earlier actual-model HTTP
schema probe and the synthetic scope-diagnosis benchmark; they do not replace them.

| Boundary | Observed result |
|---|---|
| Client registration | One pre-registered public client, exact callback `http://127.0.0.1:64275/callback` |
| Authorization | Real fixture-owner Dex login and MCP consent, S256 PKCE, matching resource on code and refresh exchanges |
| Initial grant | Only `read`; precise pipeline group omitted write branches and advertised `readOnlyHint=true` |
| Group execution | Four successful `pipeline_get` calls returned the real private `auth-team/private` pipeline |
| Expiry | Server test clock advanced 16 minutes; client received 401, refreshed, and retried successfully |
| Saved renewal | A new app-server process loaded saved credentials; another 16-minute server-clock advance produced a second successful refresh with rotation |
| Revocation | Real grants-management POST revoked the grant; next call returned 401, refresh returned `invalid_grant`, and Codex returned `Auth required` |
| New consent | A fresh app-server process enrolled with `read` and `pipelines:write`; re-list added config-set/pause/unpause branches and advertised `readOnlyHint=false` |
| Protocol | Codex requested and negotiated `2025-06-18`; server remained on its declared SDK v1.6.1 / `2025-11-25` compatibility profile |
| Cleanup | Both fixture grants revoked, Codex test-entry logout exited 0, temporary fixture/database resources disposed |

No DCR, client-ID metadata document, protocol upgrade, bypass token, live deployment,
or owner interaction was used. The callback URL and listener port were both set
explicitly through per-invocation Codex overrides. Existing MCP entries were disabled
and the runner verified that only the fixture connection was enabled. No `config.toml`
edits or HOME/CODEX_HOME replacement were needed. Codex managed its own synthetic
credential in the normal file store; the probe never opened credential files.

The fixture observer records selected metadata, schemas, HTTP statuses and refresh
rotation booleans. It never emits authorization codes, cookies, CSRF values, access
credentials, refresh credentials, fixture passwords, or full authorization URLs.
Raw child diagnostics are discarded. The saved evidence contains only fixture
pipeline data and these redacted observations.

Evidence lives outside the repository in the `precise-mcp-20260915` evidence archive, which is held by the maintainer and not published with this repository (141 files; its `MANIFEST.sha256` hashes each one and itself hashes to `1f01774d208039524db30cc7aee81213c14ea7a8f0d849d53782c7a83f2d9151`),
under `hack/mcp-oauth-probe/evidence/`. The final-candidate run is `codex-final-20260915/evidence.json`;
`verification.json` records the deterministic checks. The earlier
`codex-20260915/` cohort remains intact as historical evidence from an
earlier implementation snapshot. `source/` preserves the three runtime source
files exactly as executed, and build-input hashes were checked before and after
compilation. The verifier has its own saved source and hash, separate from the
runtime-source record. No model benchmark sessions were repeated for this final
OAuth check.

From the repository root:

```sh
python3 -B hack/mcp-oauth-probe/run.py --binary /private/tmp/jb-mcp-oauth-probe --out /private/tmp/jb-codex-oauth-new-cohort
python3 -B hack/mcp-oauth-probe/verify.py /private/tmp/jb-codex-oauth-new-cohort
```

The runner builds through the existing Brine module and uses its resource factories
and private fixture helpers through one dedicated exported probe helper. It needs
the same local Go dependencies and PostgreSQL apparatus as the authentication
fixtures. It creates no worker or Kubernetes resources.

Limitations: this verifies `pipeline_get`, not every operation or client. Whole-tool
approval was configured for this fixture; no interactive approval dialog was tested.
A preliminary attempt to re-enroll in the same app-server process stalled at the
callback after successful login/refresh/revocation. The final cohort verifies
recovery with a fresh app-server process; seamless same-process re-enrollment remains
unverified. That incomplete preliminary evidence remains outside the repository
under `/private/tmp/jb-codex-real-oauth-20260915-attempt2`. An initial configuration
preflight stopped before login, and
an intermediate successful cohort was strengthened with the second expiry after
restart. The final-candidate cohort repeats that verified flow against the frozen
implementation.

Client behavior was checked against installed CLI help/schema generation and the
matching [Codex 0.153.1 OAuth implementation](https://github.com/openai/codex/blob/rust-v0.153.1/codex-rs/rmcp-client/src/perform_oauth_login.rs).
Current [MCP configuration documentation](https://learn.chatgpt.com/docs/extend/mcp)
describes pre-registered IDs and explicit callback settings. The evidence above is
the compatibility claim for this fixture and this installed client version.
