# Mock-Free API Access Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the API suite's blanket fake accessor with production route-specific authorization backed by real bearer tokens, team roles, and PostgreSQL.

**Architecture:** Export the sole concrete access factory and wire every route through `wrappa.NewAccessorWrappa`, matching production. The API suite keeps one migrated database for the suite, truncates it per spec, and creates anonymous/member/admin/system request profiles; endpoint tests select a profile and grant real team roles, then assert HTTP status, response data, and mutations.

**Tech Stack:** Go 1.25, Ginkgo v2/Gomega, PostgreSQL, `httptest`, Concourse access-token verifier, Rata route wrappers, production display-user-ID generator.

**Spec:** `docs/superpowers/specs/2026-08-14-zero-mocks-design.md`

## Global Constraints

- Execute every multi-command shell block with fail-fast semantics; stop on the first non-zero status even when a snippet does not repeat `set -e`.
- Exercise each route's real `atc` action and `accessor.DefaultRoles`/custom role, not one suite-wide audited action.
- Use persisted access tokens and team auth. “Authenticated but unauthorized” means a valid token with no matching team role; “authorized” means the relevant team carries that identity and role.
- Assert status, body, visible teams/resources, build creator, or mutation. Remove `IsAdmin`/`IsAuthorized` call assertions and callback-driven authorization.
- Retain the production `Access` interface as the request-context boundary, but remove its directive and every test implementation.
- Make the sole factory implementation concrete as `*accessor.Factory`.
- Amortize PostgreSQL startup and use `Truncate` per spec; do not create/drop a database 788 times.
- Run the API package with `ginkgo ./atc/api`; never plain `go test` concurrently with other DB packages.
- Do not modify the two untracked review documents.

---

### Task 1: Export the Concrete Access Factory and Test the Handler by Outcome

**Files:**
- Modify: `atc/api/accessor/accessor_factory.go`
- Rewrite relevant contexts: `atc/api/accessor/handler_test.go`
- Verify only: `atc/api/accessor/accessor.go`, `atc/api/accessor/handler.go`, `atc/wrappa/accessor_wrappa.go`, `atc/api/auth/auth_suite_test.go`, `atc/atccmd/command.go:2220-2270`

**Interfaces:**
- Consumes: `accessor.Access`, `TokenVerifier`, `TeamFetcher`, and the real auditor/logger.
- Produces: `type Factory struct` and `NewAccessFactory(...) *Factory`; the temporary `AccessFactory` interface remains until all API consumers stop using its generated fake in Task 4.

- [ ] **Step 1: Write a real role-resolution handler scenario**

Remove `recordingAccessFactory`. Create a persisted team whose test identity has only `viewer`, persist a token, set `Authorization`, and wrap a handler that writes `204` only when `accessor.GetAccessor(r).IsAuthorized(team.Name())` is true. With action `atc.SaveConfig`, assert the default member role produces `403`; with `customRoles[atc.SaveConfig] = accessor.ViewerRole`, assert `204`.

The downstream handler should be:

```go
http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	if accessor.GetAccessor(r).IsAuthorized(team.Name()) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusForbidden)
})
```

- [ ] **Step 2: Replace auditor recording with real log output**

Construct `auditor.NewAuditor` with a `lagertest.TestLogger`, send an authenticated request through the handler, and assert the logger contains an `audit` entry whose data has the real action and token user name. Make the downstream handler encode `accessor.GetAccessor(r).Claims()` and assert the response JSON; do not retain requests/accessors in a slice.

- [ ] **Step 3: Drop the disconnected-database constructor-error case**

Delete the handler/factory scenario that closes PostgreSQL solely to force `GetTeams` to fail. A failed database query is outside the approved likely-error set, and keeping it would require a test-only seam.

- [ ] **Step 4: Export the sole implementation without breaking remaining consumers**

In `accessor_factory.go`:

```go
func NewAccessFactory(
	tokenVerifier TokenVerifier,
	teamFetcher TeamFetcher,
	systemClaimKey string,
	systemClaimValues []string,
	displayUserIdGenerator atc.DisplayUserIdGenerator,
) *Factory {
	return &Factory{
		tokenVerifier:          tokenVerifier,
		teamFetcher:            teamFetcher,
		systemClaimKey:         systemClaimKey,
		systemClaimValues:      systemClaimValues,
		displayUserIdGenerator: displayUserIdGenerator,
	}
}

type Factory struct {
	tokenVerifier          TokenVerifier
	teamFetcher            TeamFetcher
	systemClaimKey         string
	systemClaimValues      []string
	displayUserIdGenerator atc.DisplayUserIdGenerator
}

func (a *Factory) Create(req *http.Request, role string) (Access, error) {
	teams, err := a.teamFetcher.GetTeams()
	if err != nil {
		return nil, fmt.Errorf("fetch teams: %w", err)
	}
	return NewAccessor(a.verifyToken(req), role, a.systemClaimKey, a.systemClaimValues, teams, a.displayUserIdGenerator), nil
}
```

Rename `accessFactory` to exported `Factory` and make the constructor return `*Factory`, but keep the `AccessFactory` interface, both generation directives, and existing handler/wrappa/atccmd parameter types for now. A `*Factory` satisfies that interface, so production and migrated tests compile while the API suite still has `FakeAccessFactory` consumers. Do not delete either generated file in this task.

- [ ] **Step 5: Verify the transitional compile boundary**

Confirm `type Access interface`, `type AccessFactory interface`, both Counterfeiter directives, and both generated files still exist. They are deliberately removed together in Task 4 after the API endpoint migration. This is a temporary compile-safe boundary, not the final architecture.

- [ ] **Step 6: Format, verify, and commit the concrete factory**

Run:

```bash
set -e
gofmt -w atc/api/accessor/accessor_factory.go atc/api/accessor/handler_test.go
ginkgo ./atc/api/accessor
ginkgo ./atc/api/auth
go test ./atc/atccmd -run '^$'
if git grep -n -E 'recordingAccessFactory|recordingAuditor' -- 'atc/api/accessor/*.go'; then false; else test $? -eq 1; fi
```

Expected: suites pass and the search has no matches. `AccessFactory` and its generated implementation still compile at this checkpoint.

```bash
git add atc/api/accessor/accessor_factory.go atc/api/accessor/handler_test.go
git commit -m "refactor(accessor): export the concrete access factory"
```

### Task 2: Give the API Suite a Fast Real-Authorization Fixture

**Files:**
- Modify: `atc/api/api_suite_test.go`
- Modify: `atc/api/real_db_test.go`
- Create: `atc/api/access_profiles_test.go`

**Interfaces:**
- Consumes: `postgresrunner.InitializeRunnerForGinkgo`, `FinalizeRunnerForGinkgo`, `Runner.Truncate`, `db.AccessTokenFactory`, `infoserver.DBPinger`, `*accessor.Factory`, and `wrappa.NewAccessorWrappa`.
- Produces: suite globals `apiDB *realDB`, `apiProfileTransport *profileTransport`, `anonymousProfile`, `memberProfile`, `adminProfile`, `systemProfile`; helpers `useProfile(requestProfile)`, `dialWebsocket(string)`, and `grantProfile(db.Team, requestProfile, string)`.

- [ ] **Step 1: Replace per-Describe database clones with one suite database**

In `real_db_test.go`, replace `postgresrunner.GinkgoRunner` with an `ifrit.Process` and explicit suite lifecycle:

```go
var postgresProcess ifrit.Process

var _ = BeforeSuite(func() {
	postgresrunner.InitializeRunnerForGinkgo(&postgresRunner, &postgresProcess)
	postgresRunner.CreateTestDBFromTemplate()
})

var _ = AfterSuite(func() {
	postgresRunner.DropTestDB()
	postgresrunner.FinalizeRunnerForGinkgo(&postgresRunner, &postgresProcess)
})
```

Split current `useRealDB` into `openRealDB`, which opens connections/factories but does not create/drop the database, and:

```go
func useRealDB() *realDB {
	GinkgoHelper()
	Expect(apiDB).NotTo(BeNil())
	return apiDB
}
```

Add `AccessTokenFactory db.AccessTokenFactory`, `WorkerConn db.DbConn`, and `HealthConn db.DbConn` to `realDB`. Give it three private `sync.Once` fields and methods `disconnect()`, `disconnectWorker()`, and `disconnectHealth()`; each method wraps the corresponding `DbConn.Close()` expectation in its own `Once`, following `realTeamFixture.disconnect` in the accessor suite. Register those methods—not raw `Close` calls—with `DeferCleanup`, because later health specs deliberately disconnect the worker/health connection and `db.DbConn.Close` is not idempotent. Add `accessTokenFactory db.AccessTokenFactory` and `dbPinger infoserver.DBPinger` to `apiDBDeps`. `openRealDB` opens `WorkerConn` and `HealthConn` as independent connections to the same per-suite database, builds `workerFactory` over `WorkerConn`, initializes the access-token factory and authorization/team factories over `Conn`, and sets `dbPinger: HealthConn`. Keep the local buffered channel passed to `db.NewCheckFactory` solely so an in-memory check can never block this fixture; API manual-check and pinned-job handlers call the factory with `toDB=true`, so their observable is the persisted build returned by the route, reloaded through `BuildFactory`, not that channel. The independent health pinger is required because disconnecting the authorization connection would make `AccessorWrappa` return 500 before the health handler can produce its JSON contract.

- [ ] **Step 2: Truncate and open the real fixture at the start of every spec**

After `logger` is initialized and before the server is constructed in the root `BeforeEach` in `api_suite_test.go`:

```go
postgresRunner.Truncate()
apiDB = openRealDB()
createRequestProfiles()
server = newAPIServer(apiDB.Deps)
```

Remove the old `server = newAPIServer(apiDBDeps{})` assignment. Keep connection cleanup in `openRealDB`; root `AfterEach` closes the HTTP server before Ginkgo runs those `DeferCleanup` nodes. Existing nested calls to `useRealDB()` now reuse the same fixture rather than attempting a second database clone.

Existing endpoint fixtures sometimes pass a partial `apiDBDeps` to `newAPIServer` while the recorder cleanup is still in progress. Add `func (base apiDBDeps) withOverrides(overrides apiDBDeps) apiDBDeps` that copies `base` and replaces each non-nil interface field from `overrides`, including the token factory and DB pinger. At the top of `newAPIServer`, use `deps = apiDB.Deps.withOverrides(deps)`. This keeps all unmentioned dependencies real during the staged migration; it is a composition helper, not a recording double.

- [ ] **Step 3: Create persisted request profiles**

In `access_profiles_test.go`, define:

```go
type requestProfile struct {
	authorization string
	connector     string
	userID        string
}

var (
	anonymousProfile requestProfile
	memberProfile    requestProfile
	adminProfile     requestProfile
	systemProfile    requestProfile
)
```

Implement `createRequestProfiles` and call it from the root `BeforeEach` at the point shown in Step 2. Persist three distinct bearer tokens through `apiDB.AccessTokenFactory` with audience `api-test`, future expiry, distinct `sub`, top-level `name`, and `preferred_username`, plus `federated_claims: {"connector_id": "test", "user_id": profile.userID}`. The verifier reconstructs connector/user identity from `federated_claims`; top-level `connector` or `user_id` claims do not populate role matching, and omitting `name` leaves audit/creator fields empty.

Give `adminProfile` owner role on a persisted team, set that team's `admin` column to true with the targeted SQL update, then reacquire it with `apiDB.Deps.teamFactory.FindTeam(team.Name())` so both halves of `Access.IsAdmin` are real. Give `systemProfile` the configured system subject. Create the production display-ID generator with `skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})`.

Implement `grantProfile` by copying/updating the target team's `atc.TeamAuth` with `role: {"users": {"test:" + profile.userID}}` and calling `team.UpdateProviderAuth(auth)`. That production method updates the database and refreshes the receiver; `db.Team` has no `Reload` method. It must reject `anonymousProfile` and `systemProfile` because neither represents a team member.

- [ ] **Step 4: Inject only the real Authorization header at transport time**

Implement a mutex-protected `profileTransport` whose `RoundTrip` clones the request, sets or deletes `Authorization`, and delegates to its real `*http.Transport`. In the root setup, assign `apiProfileTransport = &profileTransport{base: &http.Transport{}}` and `client = &http.Client{Transport: apiProfileTransport}`. Give the transport an `authorizationHeader() http.Header` snapshot method that holds the same mutex and returns a fresh header containing the selected profile's `Authorization` value, or an empty header for anonymous access. Add a suite `dialWebsocket(url string)` helper that calls a zero-value `websocket.Dialer.Dial(url, apiProfileTransport.authorizationHeader())`, and replace the direct nil-header dial in `containers_test.go` with `conn, response, err = dialWebsocket(wsURL.String())`. WebSocket hijack requests do not traverse the suite HTTP client's transport, so transport-only injection would leave those contracts anonymous. `useProfile` changes only the selected profile on `apiProfileTransport`. Neither helper records requests or exposes a call API. Set `anonymousProfile` at the start of every spec.

- [ ] **Step 5: Wire route-specific access exactly like production**

Inside `newAPIServer`, after merging the real base dependencies and any transitional overrides, create:

```go
displayID, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{})
Expect(err).NotTo(HaveOccurred())
accessFactory := accessor.NewAccessFactory(
	accessor.NewVerifier(deps.accessTokenFactory, []string{"api-test"}),
	deps.teamFactory,
	"sub",
	[]string{"api-system"},
	displayID,
)
```

Append `wrappa.NewAccessorWrappa(logger, accessFactory, newAuditor(), map[string]string{})` to `apiWrapper` after `NewAPIAuthWrappa`, matching production wrapper order. Delete `auditedAction`, `auditingACopy`, and the outer blanket `accessor.NewHandler` from `api_suite_test.go`; make `newAuditor` return `auditor.NewAuditor(...)` directly with the existing category flags. Route wrapping now happens after Rata has established parameters, so request cloning is unnecessary. Keep the `fakeAccess`/`fakeAccessor` globals, setup, and `accessorfakes` import as an unwired compile shim: not-yet-migrated endpoint specs still reference them even though the server now uses only the real factory. Remove that shim atomically with the generated files in Task 4.

Pass `deps.dbPinger` as the final argument to `api.NewHandler` instead of `nil`; the later health-route migration uses this same full HTTP fixture.

- [ ] **Step 6: Prove the fixture with four route-level contracts**

Add these four specs under a uniquely named `Describe("Real API access profiles", ...)`, use real routes, and assert:

```text
anonymous -> authenticated endpoint returns 401
member without the target role -> team endpoint returns 403/404 as its contract defines
member granted the target role -> same endpoint returns its success status and data
admin/system -> their respective privileged routes return success
```

Run: `ginkgo -ginkgo.focus='Real API access profiles' ./atc/api`

Expected: PASS through `NewAccessorWrappa`; the focused specs do not configure or consult the unwired fake globals.

- [ ] **Step 7: Measure fixture cost and commit**

Run: `/usr/bin/time -p ginkgo -ginkgo.focus='Real API access profiles' ./atc/api`

Expected: the persistent DB/truncate fixture avoids per-spec create/drop overhead.

```bash
git add atc/api/api_suite_test.go atc/api/real_db_test.go atc/api/access_profiles_test.go
git commit -m "test(api): run routes with real access profiles"
```

### Task 3: Migrate Endpoint Authorization in Reviewable Groups

**Files:**
- Modify group A: `atc/api/info_test.go`, `atc/api/log_level_test.go`, `atc/api/users_test.go`, `atc/api/wall_test.go`
- Modify group B: `atc/api/artifacts_test.go`, `atc/api/cc_test.go`, `atc/api/config_test.go`, `atc/api/containers_test.go`, `atc/api/volumes_test.go`
- Modify group C: `atc/api/builds_test.go`, `atc/api/jobs_test.go`, `atc/api/pipelines_test.go`, `atc/api/resources_test.go`, `atc/api/versions_test.go`
- Modify group D: `atc/api/teams_test.go`, `atc/api/workers_test.go`, `atc/api/team_scoped_handler_factory_test.go`

**Interfaces:**
- Consumes: the four real request profiles and `grantProfile` from Task 2.
- Produces: every endpoint authorization scenario expressed through HTTP and persisted data with no fake access configuration.

- [ ] **Step 1: Apply the mechanical translation rules**

Use these exact replacements while preserving endpoint-specific expectations:

| Old fake setup | Real setup |
| --- | --- |
| no setup / `IsAuthenticatedReturns(false)` | `useProfile(anonymousProfile)` |
| `IsAuthenticatedReturns(true)` with authorization false | `useProfile(memberProfile)` and do not grant the target team |
| `IsAuthenticatedReturns(true)` plus `IsAuthorizedReturns(true)` | `grantProfile(targetTeam, memberProfile, accessor.MemberRole)` then `useProfile(memberProfile)` |
| `IsAdminReturns(true)` | `useProfile(adminProfile)` |
| `IsSystemReturns(true)` | `useProfile(systemProfile)` |
| `TeamNamesReturns([]string{team.Name()})` | grant the profile a real role on that team |
| `UserInfoReturns(...)` | persist those display/user claims in a dedicated profile token and assert the resulting build/job/pipeline row |

Use `accessor.ViewerRole`, `MemberRole`, `OwnerRole`, or a configured custom role according to the route's actual action in `accessor.DefaultRoles`; do not grant every case owner.

- [ ] **Step 2: Migrate group A and run it**

Remove fake setup from the four system/user endpoint files. Rename the misleading top-level `Describe("Pipelines API", ...)` in `info_test.go` to `Describe("Info API", ...)`, then preserve public info behavior, log-level admin gates, real `GetUser` JSON, admin user listing, and wall authorization.

Run: `ginkgo -ginkgo.focus='Info API|Log Level API|Users API|Wall API' ./atc/api`

Expected: PASS, then commit:

```bash
git add atc/api/info_test.go atc/api/log_level_test.go atc/api/users_test.go atc/api/wall_test.go
git commit -m "test(api): authorize system routes with real tokens"
```

- [ ] **Step 3: Migrate group B and run it**

Preserve artifact lookup, save-config mutation, container hijack/check behavior, and volume visibility. Delete the `containers_test.go` assertion that `IsAdmin` was not called and the `volumes_test.go` assertion capturing the team argument; the response/mutation contract must protect each case.

Run: `ginkgo -ginkgo.focus='ArtifactRepository API|cc[.]xml|Config API|Containers API|Volumes API' ./atc/api`

Expected: PASS, then commit:

```bash
git add atc/api/artifacts_test.go atc/api/cc_test.go atc/api/config_test.go atc/api/containers_test.go atc/api/volumes_test.go
git commit -m "test(api): authorize team mutations with real roles"
```

- [ ] **Step 4: Migrate group C and run it**

For list endpoints, create at least one visible/granted team and one hidden/ungranted team and assert only the authorized rows appear. For build creation/rerun and pipeline pause, assert the display ID from the persisted token claims is written to the database. Keep endpoint-specific anonymous and unauthorized status contracts, but deduplicate identical matrices within the same route family.

Run: `ginkgo -ginkgo.focus='Builds|Jobs|Pipelines|Resources|Versions' ./atc/api`

Expected: PASS, then commit:

```bash
git add atc/api/builds_test.go atc/api/jobs_test.go atc/api/pipelines_test.go atc/api/resources_test.go atc/api/versions_test.go
git commit -m "test(api): verify scoped visibility through real roles"
```

- [ ] **Step 5: Migrate group D and run it**

Replace the `teams_test.go` `IsAuthorizedCalls` callback with two persisted teams carrying the member identity and a third team without it; assert the response includes exactly the two authorized names. Exercise worker team visibility with persisted roles and keep system-worker contracts under `systemProfile`.

Run: `ginkgo -ginkgo.focus='Teams API|Workers API|TeamScopedHandlerFactory' ./atc/api`

Expected: PASS, then commit:

```bash
git add atc/api/teams_test.go atc/api/workers_test.go atc/api/team_scoped_handler_factory_test.go
git commit -m "test(api): exercise real team and worker visibility"
```

- [ ] **Step 6: Search the whole API package after all groups**

Run:

```bash
if git grep -n -E 'fakeAccess|fakeAccessor|FakeAccess|FakeAccessFactory|IsAuthenticatedReturns|IsAuthorized(Returns|Calls|ArgsForCall)|IsAdmin(CallCount|Returns)|TeamNamesReturns|UserInfoReturns' -- 'atc/api/*.go' 'atc/api/**/*.go' ':(exclude)atc/api/api_suite_test.go' ':(exclude)atc/api/accessor/accessorfakes/**'; then false; else test $? -eq 1; fi
```

Expected: no matches in endpoint files. The explicitly excluded unwired shim and generated files remain for Task 4's atomic deletion.

### Task 4: Delete Generated Access Fakes and Verify Runtime

**Files:**
- Delete: `atc/api/accessor/accessorfakes/fake_access.go`
- Delete: `atc/api/accessor/accessorfakes/fake_access_factory.go`
- Modify: `atc/api/accessor/accessor.go`
- Modify: `atc/api/accessor/handler.go`
- Modify: `atc/api/api_suite_test.go`
- Modify: `atc/wrappa/accessor_wrappa.go`
- Modify: `atc/api/auth/auth_suite_test.go`
- Modify: `atc/atccmd/command.go:2200-2215`
- Modify only for remaining imports: API files named above.

**Interfaces:**
- Consumes: green endpoint groups and concrete `*accessor.Factory`.
- Produces: route-specific real authorization across the API suite with no access test implementation.

- [ ] **Step 1: Remove the access substitution boundary atomically**

Delete `AccessFactory` and its directive from `handler.go`; change `NewHandler` and `accessorHandler.accessFactory` to `*Factory`. Remove the `Access` directive from `accessor.go` and remove its package-level `go:generate` line once no directives remain. Change `NewAccessorWrappa` and `AccessorWrappa.accessFactory` to `*accessor.Factory`, `RunCommand.constructAPIHandler`'s `accessFactory` parameter in `atc/atccmd/command.go` to `*accessor.Factory`, and `realAccessFactory()` in the auth suite to return `*accessor.Factory`. Remove the unwired `fakeAccess`/`fakeAccessor` globals, their `BeforeEach` setup, and the `accessorfakes` import from `api_suite_test.go`.

In the same edit, delete both generated files and remove the empty directory. Do not run a compile between deleting the interface and changing all signatures; this step is one atomic compile boundary.

- [ ] **Step 2: Run focused access packages and the full API suite**

Run serially:

```bash
gofmt -w atc/api/accessor/accessor.go atc/api/accessor/handler.go atc/api/api_suite_test.go atc/api/auth/auth_suite_test.go atc/wrappa/accessor_wrappa.go atc/atccmd/command.go
ginkgo ./atc/api/accessor
ginkgo ./atc/api/auth
ginkgo ./atc/api
```

Expected: PASS.

- [ ] **Step 3: Re-measure the full API package**

Run: `/usr/bin/time -p ginkgo ./atc/api`

Expected: no unexplained material regression over the plan-set baseline. If route-specific real access adds time, compare the number of database clones/drops and confirm per-spec `Truncate` is functioning before changing test coverage.

- [ ] **Step 4: Verify there is no access substitute**

Run:

```bash
if git grep -n -E 'accessorfakes|FakeAccess|FakeAccessFactory|type AccessFactory interface|counterfeiter:generate . (Access|AccessFactory)' -- '*.go'; then false; else test $? -eq 1; fi
```

Expected: no matches; `type Access interface` remains.

- [ ] **Step 5: Commit the completed API migration**

```bash
git add atc/api atc/wrappa/accessor_wrappa.go atc/atccmd/command.go
git add -u atc/api/accessor/accessorfakes
git commit -m "test(api): exercise real route authorization"
```
