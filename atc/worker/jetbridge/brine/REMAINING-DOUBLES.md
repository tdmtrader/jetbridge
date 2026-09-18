# Remaining no-doubles work

Current action inventory, 2026-09-17. The migration is **not complete**.
This replaces informal estimates based only on Ginkgo spec counts. Native Go
tests also run and must be accounted for. This is not a certification that
matching imports or names exhaust every possible behavioral substitute.

## Passing full gate, 2026-09-17

The final local/live coverage command exited zero: 589/589 pass (455 local,
134 live), 2,035/2,471 production statements (82.355322%), 46m50.212s summed
CLI runtime. All 589 recorder drains complete and all 133 owned namespaces
are independently absent. The roster, unchanged source and daemon checksum
are verified in /tmp/brine-closure-final.3sJT9C/evidence.json.

The migration remains incomplete for the explicit open decisions/gates in
[V5-MIGRATION.md](V5-MIGRATION.md#current-closure-status--2026-09-17): native
baseline compatibility, the historical OOM timeout, authenticated CI and its
matching immutable runner. The 2026-09-18 audit snapshot is packaged on
`core-brine-audit-20260918`; see the migration document for its scope and
evidence portability. No additional runtime source change, toolchain upgrade,
runner publication or deployment is implied by the passing Brine result.
Older checkpoints below are historical.

## Daemon budget correction, 2026-09-17

The post-eviction-fix acceptance attempt passed all 455 local cases, then
failed the first handoff because alias registration and reads encountered
ECONNREFUSED. It was intentionally cancelled after 41 live cases to recover
buffered diagnostics. All 41 namespaces are independently absent, including
the cancelled active scenario whose final cleanup receipt was not emitted.
This partial run is not acceptance evidence.

A longer focused run captured the actual kubelet Evicted event: the daemon's
103.86 MiB executable exceeded its unchanged 96 MiB ephemeral-storage budget.
The process handled termination and exited 0; Pod Succeeded did not exclude
an eviction. The same-source 73.42 MiB stripped build passes the equivalent
75-second-lifetime handoff control and the final guarded fixture. The earlier
short unstripped control also passed, explaining why short runs missed this.

CI now matches README's existing -ldflags='-s -w' build. The binary guard and
both daemon emptyDir bounds derive from the same pod budget; the executable
must leave 8 MiB for data/logs. The real oversized binary is rejected before
namespace creation. All six reproduction namespaces are independently absent.
No production behavior, artifact assertion, timeout or cluster quota changed.
A fresh full gate, historical OOM investigation, CI and review handoff remain.
Evidence: /tmp/brine-merge-gate.1NcEzo/cancelled-evidence.json and
daemon-budget/evidence.json in that directory.

## Mirroring closure checkpoint, 2026-09-17

The four remaining mirroring cases now use the actual node's production
daemon and an independent peer pod/store. The opt-in --peer-discovery flag
preserves existing defaults and avoids --node-name's label ownership. Only
the producer daemon receives namespace-local EndpointSlice list access.
The peer's endpoint is controller-generated, and assertions read its disk
before HTTP delivery. The get case retains real producer-mount writes;
producer cleanup before peer startup keeps the existing four-pod quota.

All four live controls and all 455 local scenarios pass. Both old and new
assertions reject the same five production faults: request reordering,
first-output-only recording, fatal mirror refusal, missing cache mirroring,
and a daemon that accepts mirroring but never copies. Three adapter suites
pass through Ginkgo; default/live vet passes. All 10 owned live namespaces,
including the inactive-fault attempt, are independently absent.

The shared-engine fault attempt and an inactive refusal mutation are excluded
from passing evidence. Fault runs now use individually owned engines and must
fail after a successful action at the expected distinguishing assertion.
The production storage implementation is unchanged.

The last explicit test-side Node status writer is removed. Four scenarios
moved, none were added; the obsolete get-step phrase was removed. Inventory:
589 scenarios (455 local / 134 live), 1,071 step definitions. Independent peers
on this single physical node do not establish node-failure isolation.

Evidence: /tmp/brine-mirror-closure.k7iajP/evidence.json
SHA256: 626461154468fee2eb7661b15de838d21c72d7c17bff1f391c4dc7ef1d064fc9

Remaining closure work: OOM-priority reliability, a fresh full coverage gate,
CI runner alignment/acceptance and the reviewable branch handoff. No new full
coverage claim is made here; no branch was pushed or pipeline deployed.

## Full acceptance and eviction correction, 2026-09-17

The unchanged-source full run passed 455/455 local and 133/134 live cases.
The OOM-priority case passed; its historical timeout remains unexplained,
not fixed by retries. Summed CLI time: 47m45.152s. All 589 recorder drains
completed and all 133 owned namespaces are independently absent. Diagnostic
coverage is 2,025/2,471 (81.950627%); the gate exited 1, not an acceptance pass.

The sole failure was the eviction fixture requiring a Running observation
and exact writer log before accepting a real volume-limit eviction. A delayed
first observation reproduces it; post-eviction logs and termination metadata
proved unreliable too. The correction removes that incidental timing premise.
It retains the capped 24MiB writer into a 16MiB volume, same pod UID, actual
Failed/Evicted volume message and matching UID-scoped kubelet warning event.
All original production error-classification and diagnostic assertions remain.
No timeout was increased and no scenario was added or removed.

The coverage-instrumented delayed-observation regression passes with the fix.
All four existing eviction/recovery consumers pass in 3m30.266s; all eight
investigation namespaces are independently absent. The exact tested fixture
is applied to the worktree. This still requires a fresh full acceptance run.
Evidence: /tmp/brine-oom-closure.yt4GPA/full-evidence.json,
eviction/evidence.json and eviction/consumers-evidence.json in that directory.

## Full acceptance attempt, 2026-09-16

The unchanged measured source ran all 589 scenarios: 459/459 local and 129/130
live passed. The one failure was `An OOM kill is reported ahead of the crash
loop it caused`: its fixture exhausted the 90-second context while polling,
ending with a client rate-limiter deadline error. This is not a diagnosed
rate-limiter defect. The prior focused case passed in 17.604s; the full-run
failure did not record the last pod state. Timeout errors now include the last
observed real pod state, with the original deadline and assertions unchanged.
Normal and coverage-instrumented focused reproductions both pass (38.291s and
36.502s); this does not establish the cause or repair the original failure.
The pure diagnostic-format table, three adapter suites and default/live vet
checks pass. Both reproduction namespaces are independently absent. Evidence:
`/tmp/brine-full-final.FKHnXG/oom-reproduction-evidence.json`.

The ordinary coverage gate correctly exited nonzero. Diagnostic extraction
of its raw counters measures **2,030/2,471 = 82.1529745%** Brine-only production
JetBridge statement coverage. This exceeds 50% numerically but is **not a
passing acceptance measurement**. Both complete scenario rosters, all recorder
drains and the unchanged source fingerprint were checked; all 129 owned live
namespaces are independently absent. All 177 native functions and 20 root
Ginkgo cases pass. Summed CLI time was 47m06.642s (local 1m55.289s; live
45m11.353s), excluding compilation and final coverage processing. Live recorder
cleanup accounts for 20m54.744s, with an 11.039s median per case; no lifecycle
semantics were changed to reduce that cost.

CI source now supplies four explicit approval/node/port variables, builds
the static live daemon from its owning root module, and allows 60 minutes.
Task extraction and shell syntax checks pass. No runner image was published
and no pipeline was deployed; the matching immutable runner tag and CI access
still need coordination. The historical fixed-scope `cmd/brine-census` names
deleted files and is not used to certify current consolidation. Current
vocabulary guards still require every definition to be used and every step
to resolve unambiguously.

Evidence: `/tmp/brine-full-final.FKHnXG/failed-run-evidence.json`;
full log/raw data: `/tmp/brine-coverage.doOtQn`.
The OOM failure and final sign-off remain open. The four mirroring cases
are now closed by the checkpoint above.

## Status-injection history through 2026-09-16

At that checkpoint one call to `UpdateStatus` remained, in
`steps/real_discovery.go`. The shared artifact-recording/peer fixture in
`real_peer.go` still published addresses through it. Resolver and warm-placement setup use Create only;
DaemonPlan and RemoteArtifact now read the actual node without status writes.

Four recording/lookup cases now use the actual named node and production
artifact daemon. The task/get layout cases write through their real producer
pod mounts; output handles, database identity, exact raw bytes and request
patterns remain asserted. One live setup and one task/get refinement phrase
reuse the existing recording checks; no scenario is added. The owned-storage
file writer is shared with DaemonPlan, preserving its containment checks.

All nine paired production faults fail at the same assertions before and after;
normal/inactive controls pass. All 459 local scenarios, three adapter suites
and default/live vet checks pass. All 14 owned live namespaces are independently
absent. Inventory: 589 scenarios (459 local / 130 live), 1,072 definitions.
The address-removal diagnostic passes 37 of 41 cases; only the four mirroring
cases still depend on supplied Node addresses. Production storage/volume code
is unchanged. This is not a fresh full-suite coverage measurement.
Evidence: `/tmp/brine-recorded-live-t1WmxB/evidence.json`.

The original diagnostic (36 passing, nine failing) remains in
`/tmp/brine-recording-node-72IVCN/findings.json`. Peer-only resolve/probe
cases do not need supplied addresses.

Before the 2026-09-17 approval, the mirror replacement needed a scope decision: production daemon peer wiring
is gated on `--node-name`, which also patches readiness labels at startup and
removes them on shutdown. Do not run that configuration against the shared
node. Options are a production startup change that separates peer discovery
from label ownership while preserving current defaults, or a dedicated
disposable cluster. Only discovery-only startup and namespace-local read access were subsequently
approved; shared Node modifications remain out of scope.

Use real runtime state for I/O behavior. Impossible status combinations can
be literal inputs to pure policy tests, provided the original distinctions
and real runtime wiring remain covered.

The five remote-artifact read cases now use actual named-node resolution and
owned real producer/peer pods. TCP faults only close or forward connections;
unreadable owned storage produces the real HTTP 500. Raw bytes, gzip delivery,
retry counts and peer-fallback diagnostics remain covered. The node-read check
is shared with DaemonPlan, and the forgotten-node write case stays local.
Six paired production faults fail at the same assertions before and after;
normal/inactive controls pass. All 463 local scenarios, three adapter suites
and default/live vet checks pass. All 21 owned namespaces, including the
initial setup-failure run, are independently absent. Inventory remains 589
scenarios (463 local / 126 live), 1,070 definitions: no cases or phrases added.
Production volume/resolver code is unchanged. Full coverage now checks live
approvals/configuration before building or running; eight missing/invalid
prerequisite checks pass. This is not a fresh full-suite coverage measurement.
Evidence: `/tmp/brine-remote-node-mYXuap/evidence.json`.

The eight named-producer daemon cases now run on a real scheduled daemon,
using its actual Node name and published address. All original raw-byte,
missing-artifact, gzip, filtering, empty-archive and producer-precedence
assertions remain. File seeding goes through the owned observer mount; a
passive check requires the production constructor to read the Nodes API.
No production volume/resolver code changed. Nine paired faults fail at the
same assertions before and after; inactive controls pass. All 468 local
scenarios, 177 native functions, 20 root cases, three adapter suites and
build/vet checks pass. All 18 live namespaces are independently absent.
That checkpoint had 589 scenarios (468 local / 121 live), 1,070 definitions.
Evidence: `/tmp/brine-daemon-node-rPGjEF/evidence.json`.

Resolver address selection now uses one four-row pure policy table. The
successful/cache case moved to a read-only live-node scenario: it observes the
published InternalIP, closes only an owned transparent TCP route, proves a
fresh API request fails, and checks both cached answers plus one actual read.
Local cases retain missing-node, API-default empty-address and typed IP-name
refusals. No shared Node or RBAC object is modified, and production resolver
code is unchanged. All seven paired faults are rejected; all 476 local cases,
177 native functions, 20 root cases, three adapter suites and build/vet checks
pass. That checkpoint had 589 cases (476 local / 113 live), 1,069 definitions.
Evidence: `/tmp/brine-node-policy-TIM5tB/evidence.json`.

The failure/status group removed the other three status writers. Both runtime
paths share `podStateFor`; nine literal pod-policy rows and two node-formatting
rows preserve the rare status combinations. Actual main/sidecar image pulls,
eviction and pod deletion cover runtime reporting, typed errors and node reads.
The three affected local features shrink from 39 to 23 scenarios; the existing
live image-pull outline gains two sidecar rows. Net inventory: 589 scenarios
(477 local / 112 live), 1,069 definitions, down from 603 / 1,076.

All 21 paired production faults are rejected by the old and replacement
assertions; inactive controls pass. All 176 native functions, 20 root Ginkgo
cases, three adapter suites, vet/default/live builds and 21 adjacent real OOM,
compatibility and pause-recovery cases pass. All 41 owned live namespaces are
independently absent. Evidence: `/tmp/brine-failure-policy-yvglJO/evidence.json`.
This is targeted migration evidence, not a fresh full-suite coverage result.

The unreported recovery phase is no longer written to Kubernetes. A pure
`recoverExecProcess` policy preserves that exact literal input, immediate refusal
and diagnostic text; the real API-default Pending case proves Attach wiring.
Five paired faults retain the original distinctions, including phase-only
acceptance/wrong-error and refusal delayed until Wait. Real completed commands
still recover exit 0/3 and survive restart without re-execution. All four owned
live namespaces are independently absent. Evidence:
`/tmp/brine-recovery-policy-V5Dv8a/evidence.json`.

## Completed expired-watch group

The existing expiry scenario moved to the live tier. The kubelet supplies
Pending/Running/Succeeded; a second owned, scheduling-gated pod supplies only
metadata traffic until the actual API emits terminal Expired/410 and closes.
No shared configuration or etcd compaction is used. A fresh metadata checkpoint
on the completed pod preserves the original fresh-version premise; the phase
remains the kubelet's. First fallback checks UID/version/phase, then exact
checkpoint replay is required after UID-scoped pod deletion.

All four paired faults remain detected: ignore expiry, stale resume version,
unsafe Status-to-Pod cast, and fresh object with cached initial phase. Normal
and inactive controls pass, as do three local and five other live watch cases,
root/native/adapter suites, vet and default/live builds. All 17 owned live
namespaces (including prototypes) are independently absent. No production
watcher change or additional scenario/definition. Evidence:
`/tmp/brine-watch-expiry-RctO6k/evidence.json`.

Follow-up finding, not repaired here: an idle completed pod can retain an
object resourceVersion older than the live watch cache. A fallback Get returns
that old version again; after deletion the next fallback returns NotFound.
Both the initial prototype and its inactive-fault control reproduced this.
The migrated contract explicitly supplies a fresh metadata version, matching
the original fresh-version replay premise; it does not claim to fix idle-object
recovery. The prototype source and logs are retained with the evidence above.

## Native test doubles

The default root tests no longer import client-go fake clients or contain the
daemon HTTP stand-ins from storage_daemonset_test.go and
daemonset_integration_test.go. The routing transport and imitation tar server
are removed. This is a specific retirement, not an import-count certification
that every possible behavioral substitute is gone.

`nameOnlyWorker` is removed. Volume layout takes worker identity and execution
configuration directly; the production Worker delegates to that calculation.
Thirteen native functions are now one 12-row table. All 13 paired faults retain
the original failures; real-worker Brine rejects lost worker/executor binding.
All 173 native functions, root/adapter suites and 26 artifact-recording cases
pass, as do default/live compilation and vet. No Brine cases or phrases added.
Evidence: `/tmp/brine-worker-layout-99eaUt/evidence.json`.

The unused `noopDelegate` is also deleted: all six callers pass nil, the
production method never reads that parameter, and default/live builds pass.
Do not delete native assertions merely because an adjacent Brine case passes.

The executor constructor now uses the actual Kubernetes client and one
four-configuration table. It makes no API call and needs no fake API server.
Client/config identity and host preservation have paired mutation evidence.

## Completed peer group

`features/live/peer-read.feature` replaces the three tests formerly in
`volume_daemonset_restored_test.go` with one three-row outline and two
definitions. It uses the production daemon on the approved node port, an
independent peer pod/emptyDir, controller-generated EndpointSlices, and passive
recording of real socket writes. No supplied Kubernetes status or HTTP response
is used. Raw bytes, exact peer paths, missing-peer behavior and request counts
are preserved.

Evidence: `/tmp/brine-peer-read-UQcg1G/evidence.json`. The original and new
tests reject five grouped production faults; inactive and unrelated controls
pass. The separate constructor cleanup rejects three paired wiring faults.
Historical disposition entries remain in `DISPOSITION-jetbridge.md`.

## Completed volume retry group

Four mocked HTTP tests in `behavioral_volume_test.go` now share the existing
remote-volume scenarios: raw-byte identity, retry success, failed open, and
HTTP 500 diagnostics. Four paired production faults catch the same original
assertions. No Brine scenario or definition was added. The unused URL-rewriting
transport, fake-node factory, fake construction client and no-op executor are
removed; construction checks use actual client/executor types. The worker substitute was subsequently removed by the layout consolidation
above. The shared node-status fixture remains open. The artifact substitute
was removed in the construction group below.

Evidence: `/tmp/brine-volume-retry-ZxRIbF/evidence.json`.

## Completed node-resolution group

The four fake-client tests in `node_ip_resolver_test.go` now use the existing
Brine discovery scenarios. Passive observation records only the resolver's
real API traffic, preserving the zero-request guarantee for all five original
IPv4/IPv6 spellings. One redundant private-IPv4 row was absorbed by the
IP-named-node case. Five paired faults cover address selection, API failure,
cached values, typed refusal and an unnecessary lookup before refusal.

Evidence: `/tmp/brine-node-resolution-OCKb1W/evidence.json`. The shared
node-status writer remains open; this retirement does not remove that fixture.

## Completed construction group

Four custom artifact substitutes across six test files now use one helper
constructing the production deferred volume and SPDY executor. These callers
inspect pod layout, affinity and fetch commands, not streaming; the production
paths do not type-assert the additional volume interfaces. The construction-only
permutation client is real, and the unused cache stub is deleted.

All 167 native checks remain and pass before and after. Wrong artifact-key and
missing-input-mount mutations fail the same named checks in both versions;
inactive controls, root Ginkgo, live-tag compilation and vet pass. No Brine
scenario or definition was added. Production source is unchanged.

Evidence: `/tmp/brine-construction-cleanup.4ZlfhU/evidence.json`.

## Completed daemon-probe group

Nine mocked tests (six cache probes and three step-probe misses) now use the
existing real-daemon Brine cases. Successful cache probes name the exact holder
and fetch its bytes; misses preserve empty addresses and durable capability.
Passive socket observation rejects wrong routes and resolve-on-miss, without
supplying responses or requiring duplicate requests for repeated discovery.
Nine paired production faults verify the original assertions. No scenarios or
definitions were added. The two positive step-probe and five mirror-trigger
native tests were subsequently retired in the client-contract group below.

Evidence: `/tmp/brine-probe-retirement.SCVSe0/evidence.json`.

## Completed daemon-client contract group

The remaining seven tests in `daemon_client_test.go` are retired. Two table-driven
Brine scenarios cover two positive-probe topologies and five mirror conditions.
They preserve exact returned addresses, real streamed bytes, method/path/body,
single-request behavior and best-effort errors. Existing end-to-end fallback
and peer-copy scenarios remain. Twelve paired faults catch the original
assertions; normal, inactive and unrelated controls pass.

The daemon produces a real 400 for an invalid mirror key, versus the old mock
500. Both exercise the client's same non-202 branch; the status-propagation fault
fails both. No fabricated 500 response or extra production seam was introduced.

Final checks: 52 affected local cases and all three live peer-read cases pass;
the three owned live namespaces are independently verified absent. Root and
adapter suites, default/live vet and live-tag compilation pass. Inventory is
600 Brine cases (491 local, 109 live), with 1,078 definitions. This is not a
fresh full-suite coverage result.

Evidence: `/tmp/brine-client-contracts.GLhnTD/evidence.json`.

## Completed producer-recording group

Four mocked recording tests now use the existing three peer-copy scenarios.
Actual TCP writes preserve exact counts, keys and output/cache request order;
peer-disk arrival remains independently checked. Nine paired faults and controls
pass. The shared pure-layout backend no longer creates a fake Node. All 41
retained native storage checks pass, with paired locator-fault coverage.

Final checks: 74 local Brine cases, three live peer cases, root/adapter suites,
vet and live compilation pass; owned namespaces are independently absent.
Inventory remains 600 cases and 1,078 definitions. Other storage mocks and
status-injection fixtures above remain open; this is not full-suite coverage.
Evidence: `/tmp/brine-recording-GJX1aI/evidence.json`.

## Completed lookup group

Four mocked lookup tests and three old Brine scenarios share one six-row
real-daemon outline. It preserves exact binding, zero probes, raw bytes and
database identity, including all three ordinary-handle spellings. Two client
wiring tests are one actual-client constructor table; the fake daemon server
and seeded-discovery helper are gone. Paired runtime, consolidation and
constructor mutations pass. All 36 retained native storage functions and
77 local Brine cases pass, plus root/adapter suites, vet and live compilation.
Inventory: 603 cases (494 local, 109 live), 1,074 definitions. Coverage still
needs the final full-suite run. Evidence: `/tmp/brine-lookup-mgHIZq/evidence.json`.

## Completed native-daemon group

Four remaining native daemon stand-ins are retired. Existing real peer-copy
and live producer-death cases preserve the meaningful contracts. One new
real-refusal recording case uses a noncanonical but contained handle: alias
registration succeeds, mirror returns 400, and the output remains readable.
The former synthetic 500 exercises the same client's non-202 branch.

Seven paired faults and three demonstrated native registration blind spots
pass, along with all 78 retained storage/integration native functions, 78 local
Brine cases and three live peer rows. All eight namespaces are independently
absent. Root/adapter suites, vet and default/live compilation pass. The unused
no-op delegate and its six call sites are cleaned up without removing tests.
Inventory: 604 cases, 1,076 definitions. Full-suite coverage remains open.
Evidence: `/tmp/brine-native-daemons-SEc6ib/evidence.json`.




## Documented fault-injection exception

`steps/scan_images.go` still contains `panicImageResolver`, used only by the
resolver panic-isolation scenario. The migration journal records this as an
explicitly approved exception; ordinary resolution uses the real resolver.
It must be accounted for in the final policy audit, not reported as removed
or treated as permission for other substitutes.

## Final completion still requires

- Resolve the open families above without losing distinguishing assertions.
- Consolidate overlapping scenarios, helpers and vocabulary.
- Re-run the full Brine-only production coverage gate (50%).
- Verify v5 runner/CI image provenance and a measured, sufficient job timeout.

The last full coverage measurement is historical, not proof for today's tree.
A green import guard does not prove that status injection or custom doubles
are gone. Existing real-traffic observers and the owned OCI registry must be
judged by their actual behavior, not by a blanket ban on transport wrappers
or the `httptest` listener utility.
