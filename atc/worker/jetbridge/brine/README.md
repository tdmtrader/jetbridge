# Brine tests

The suite uses the v5 execution-document contract; step authoring remains v3.
The migration is in progress. See [V5-MIGRATION.md](V5-MIGRATION.md) for
validation evidence and remaining test doubles.
The current open-work inventory is [REMAINING-DOUBLES.md](REMAINING-DOUBLES.md);
Ginkgo spec counts alone do not include the native Go tests.

The container security cases now share one policy assertion over real API
readback, including unset RunAsNonRoot and RuntimeDefault seccomp. Requests-only
pods explicitly have no resource ceiling. Their nil-versus-empty in-memory map
contract lives in one pure Go test, because Kubernetes serialization erases that
distinction. The three overlapping mock-backed cases have been retired after
paired mutation validation; this does not complete the wider no-mocks migration.
Pull-secret checks also share one complete-list comparison: missing, extra and
duplicate names are rejected without imposing an order.
Sidecar cases now check ordered container identities, complete mount equality
and declared configuration fields. The exec-pod case wires the real SPDY
transport explicitly; other pod-spec cases retain no-exec compatibility
construction. Six overlapping mock-backed sidecar tests have been retired.
Returned input, output and cache volumes now share one complete path-table
check before Run; the same scenario checks binding afterwards. Three legacy
mock-backed cases and two redundant Brine lifecycle cases have been removed
after mutation validation. Named outputs retain their actual result/metadata
keys instead of testing only automatically generated output names.
Scratch and cache policies also reuse existing scenarios: scratch pods have
no init containers, keyless standalone caches use only ephemeral volumes, and
whole-volume mounts reject subPath and subPathExpr. Job metadata remains
independent of an explicitly absent runtime cache identity. Four more legacy
mock-backed cases have been retired without adding Brine scenarios.
Direct-mode construction and seccomp now share one scenario and the same
PodCreated state as other pod checks. Main and sidecar containers reuse exact
environment, command/arguments and working-directory checks. One legacy
mock-backed case and one duplicate Brine scenario have been removed.

The expired-watch and sidecar-log fallback regressions are fixed. Sidecar
routing now uses real Kubernetes execution in a separate live tier, not a
fake clientset. The migration is not complete: other test doubles remain.
See the migration journal for measured coverage, mutation evidence and
remaining work.

The step package no longer imports fake Kubernetes clients. The last factory,
its unused ClusterReady state and configuration options have been removed.
A recursive AST import guard rejects fake clients/reactors and tests its own
detection, including aliases. F23 now cuts a real task's exec connection while
it writes into owned node storage. Both growth observations occur after Wait
returns, and independent exec proves the same child is alive. The original
locator assertion is retained and also checks that a reachable real daemon
refuses the unregistered partial artifact. The injected EOF and reported
Running status are gone.

The vanished-pod case now cuts a real SPDY connection and deletes its owned
pod. It exposed a false-success bug: an empty remote status stream was treated
as exit zero. The approved production fix requires a status for Kubernetes
exec protocols v4/v5; genuine zero and nonzero exits remain supported. This
Kubernetes protocol version is independent of Brine's v5 adapter contract.

The OOM diagnostic case shares that live setup and interruption vocabulary.
It reuses an owned pod whose PID 1 allocates beyond its verified 64 MiB
cgroup limit, then requires the kubelet's real OOMKilled/137 status and
container identity before checking the runtime's diagnostics. Both cgroup
v1 and v2 limit files are supported; an unverified limit fails before arming
the allocator. No host settings, signals or Kubernetes status are fabricated.
The old severingExecutor and its now-unused setup steps have been removed.
The same real-OOM case now also diagnoses current termination through the
explicit direct compatibility Process. A 16 MiB-limited sidecar keeps the pod
Running after main dies, preserving the removed local case's phase and
current-termination premise without fabricating state. All three old checks
remain and their paired detector/explanation/diagnostic faults still fail.
This removes one reported-status case without adding a live case or net
definitions; the production exec-interruption assertions remain unchanged.

The OOM-versus-crash-loop priority case now shares the real bounded OOM
fixture too. A restarting main container and a running sidecar produce actual
Running/CrashLoopBackOff/last-OOMKilled state. The compatibility watcher must
name only the OOM; both original assertions reject their paired faults.
No scenario is added: one moves from the local to the live tier.

The two ordinary direct-compatibility exit cases now use real BusyBox commands
and kubelet terminal states. Their original exit/error distinction and cleanup
assertions remain, and all three paired mutations fail identically. One live
action replaces the status setter, with no extra scenarios or net definitions.
The fixture waits for terminal pod phase, which can lag container termination.

Both main-completion/sidecar-running rows now share that live fixture too,
retaining PostgreSQL/Redis and exits 0/42. The actual services respond before
and after main exits. Cleanup requires an already-requested deletion and then
actual API absence within 15 seconds; terminating alone never passes. Paired
production faults fail identically, and real finalizers holding deletion make
both absence checks fail at the bound before owned cleanup releases them.

The clean-sidecar/failing-main case now shares the compatibility fixture too.
A real BusyBox sidecar exits 0 while main remains gated; only then is main
released to exit 1. The kubelet must report Failed with the sidecar first in
the actual status array. Selecting that first container wrongly produces the
same old/new assertion failure. One local case moves live; no scenario or
definition is added.

The one- and two-failed-read cases now follow actual kubelet completion too.
The shared RBAC fixture still forwards genuine denials and restores the owned
namespace Role before retry; its completion status setter and status-write
permissions are gone. Stopping after two denials fails only the second row
for both old and new fixtures. The permanent-denial case remains API-only.
Both rows move to the existing live compatibility feature with no additional
scenarios or definitions.

Four terminal waiting-state rows now use actual kubelet refusals: an invalid
image reference, a missing required Secret, ErrImagePull and ImagePullBackOff.
Production pod construction supplies the inputs, and the direct compatibility
Process reads Pending/current-waiting status with no restart history. Pull
failures also require matching kubelet events for the actual image and pod UID.
The original assertions reject paired diagnostic-identity loss and false
success. Only the Pending/CrashLoopBackOff/no-history case remains reported.

The production startup-timeout case also uses real Kubernetes state now. It
shares the observability fixture's actual init-process gate, preserving the
200 ms deadline and both timeout/diagnostic assertions. The same init remains
running after Wait and main never executes. Both original init-gate consumers
pass, and dropping timeout diagnostics fails the old and new cases identically.
One local case moves into the existing live startup feature, with no extra
scenarios or definitions.

The approved scheduling-log fix includes PodScheduled even when Kubernetes
supplies no reason. Four reported image-pull diagnostic/metric cases now share
two real-pod cases, one per execution mode, through the existing startup helper.
They retain exact image identity, successful scheduling, failure reason,
counter delta and execution-path assertions. The expected image is read from
the actual pod spec; no new shared-state field or definition count is needed.
All five paired mutations still fail their intended assertions.

The approved compatibility completion fix now reads a completed main in a
Pending pod too. A real-pod probe exposed this when main exited 0 and a sidecar
pull failure left the pod Pending, not the reported fixture's Running phase.
The original SC-09 case now shares the live compatibility helper: actual main
exit, Pending phase, sidecar ImagePullBackOff and a matching kubelet pull event
precede Wait. Its exit-zero assertion is retained; the old status setter is
gone. Both original mutations still fail, and reverting Pending handling now
fails the real case too. This retained compatibility contract is not the
production execProcess path. Pure regression tests cover
reasonless scheduling and Pending completion for main exits 0/42, with a
sidecar first in the status array; they fail against the pre-fix source.

The shared Given says “a task using Kubernetes”: it prepares task state, not
a running container. Its consumers use that one phrase with no alias or typed
contract change; native vocabulary checks cover the complete feature corpus.

The five set_pipeline scenarios now read real files through the production
artifact daemon and DaemonSetVolume, including gzip/tar and subpath selection.
They use owned local daemon storage, not a stub volume or Kubernetes hostPath;
this tests consumption of an existing artifact, not producer publication.
The two task-preflight scenarios now use a real database-backed worker pool
and factory against the local envtest API. Their present input is read through
the same real artifact helper. These assert validation before execution, not
executing-pod behavior. The partial-output put now uses a real database-backed
pool, worker, BusyBox pod and SPDY exec. Its explicitly approved resource-fault
executable emits valid version JSON and actually exits 4; no runtime result is
substituted. The two artifact integrations and F23 now use the approved owned
hostPath, namespace pod-security exception and temporary unused high TCP port.
The failed-init observability case now observes real init failures and recovery
through production SPDY. The last host executor and its status-writing fixture
have been removed; this does not remove the other reported-status families.

Retry classification also uses the real put step and database-backed pool:
a closed, previously working route to envtest causes connection refusal;
a missing required input causes the permanent error. The abort case cancels
at the actual failed pod request, forwarding its transport result unchanged.
Paired mutations verify retry decisions, the build-log notice and abort
precedence. No worker-selection error or step result is injected.

The cache-hit get uses a real worker/cache association and artifact daemon.
Its existing provenance assertion also reads the returned artifact's bytes
and verifies that no resource pod was created. The version-keyed cache map and
stub cache volume are gone; uncached gets now use real resources as described below.

The direct volume round-trips and terminal checks now use real BusyBox pods
and production SPDY execution. Six existing cases moved to the live tier;
no scenarios or step definitions were added. The terminal fixture no longer
sets pod status or uses the host executor, and both TTY mutations fail their
intended assertions.

The approved Volume.StreamIn fix creates the extraction directory before
unpacking. Its path is passed as a quoted positional shell argument, not
interpolated into shell source. The retired host executor used to create
directories implicitly, which had masked the two nested-upload failures. Both nested cases now pass in the full live run; a
separate real-BusyBox probe also preserves literal shell punctuation in paths.

Interception's four existing cases now share live/interception.feature and
use real kubelet execution, SPDY and in-pod supervisor files. The raw-handle
decoy and completed-pod preservation checks remain. The terminal fixture now
runs an actual supervised task before PID 1 exits, so production writes the
completion annotation too. It no longer seeds that record. Annotation erasure
still fails the existing preservation assertion, and corrupting production's
writer now fails it as well (the previous seeded fixture stayed green).
No host executor or fabricated pod status is used by this family. All four
controls and the paired refusal/exit/log mutation checks pass. This does not
complete the broader migration.

The eight supervised-task cases now run through real pause pods, SPDY and
the production supervisor, grouped with failed-task retention in
live/task-command.feature. They share worker setup and remote state checks
with interception. A single Given replaces the old two-step host setup;
TaskCluster no longer carries a host workspace or reports Running status.
Recovery reconstructs runtime objects against the same pod; the missing-record
case explicitly clears the annotation, not a full ATC process crash.

The supervised-task increment passed its eight task controls, five paired
focused mutations and four interception regressions. Cancellation has since
moved four more cases from the host executor to the live tier. Its old fixture
masked a real defect: resource children survived cancellation.

The approved resource fix gives get/put/check commands an invocation-scoped
process group and uses a bounded, independent exec to stop that group. It
retains the resource pod and unrelated processes; task cancellation still
deletes the task pod. A cancellation marker prevents late startup, and a
process-birth check avoids trusting a reusable PID alone. Resource stdin,
stdout, stderr, argument quoting and zero/nonzero exits have real-pod controls.

Resource images now need setsid in addition to sh and the small filesystem
utilities used by the wrapper. BusyBox 1.37.0 is verified. Deliberately detached
sessions are outside a process group: this does not provide per-command cgroup
containment. Normal completion removes the invocation's state directory.

The host-resource fixtures have been replaced by actual resource executables.
Five targeted faults are detected by the cancellation and protocol tests.

Six host echo-resource scenarios have now become four real Git-resource
cases in live/git-resource.feature, plus one row in the existing image
construction outline. They run the upstream image pinned by digest through
production SPDY: real get, put, check and invalid-source rejection. Put reads
a real mounted Git checkout and its pushed commit is independently inspected.
This uses direct volume streaming inside an owned pod, not artifact-daemon
publication or automatic input fetching. The default-image fallback is tested
separately without pulling an unpinned image. Database creation state is
observed before Run and retained through the final contract assertion.

The pinned-version and missing-version get-step cases also use real Git now,
through the production worker pool and engine delegates. A separate read-only
Git HTTP server holds two commits. The get pins the older commit; assertions
read its finish event, cache row and actual artifact bytes. The missing ref
fails with the real Git exit status and exposes no downstream artifact.
Five paired faults verify those contracts. Three new definitions replace
three obsolete assertions, with no scenario or vocabulary growth.

The get-timeout case also uses real Git now: its owned HTTP server is paused,
the resource’s actual HTTP child must be observed before the 15-second
deadline, and that child must stop afterward. The existing timeout-message
and no-finish assertions remain. Three paired faults are detected. The stalled
fake-process helper and three obsolete setup definitions are gone; one shared
definition replaces them.

The put-to-get and successful-retry cases now share live/time-resource.feature
and the upstream time-resource image pinned by digest. The resource generates
the version; assertions compare the published version, dynamic get, database
cache and actual artifact files. Retry first encounters a real invalid-source
failure, then succeeds; a mutation that runs the third attempt is caught by its
actual extra publication. Four paired faults preserve the original contracts.
The fake get process and in-memory version catalogue are removed, and the
real pool/cache helpers are shared. This input-free publication does not cover
artifact-daemon publication or automatic input fetching. The handoff and F23
live cases exercise those storage boundaries separately.

The aborted-retry case now shares that publication/retry feature. It cancels
an actual Git fetch only after observing its live HTTP child, and confirms the
child stops while its resource pod remains. A real time-resource put is armed
as attempt 2. Durable build events require that attempt 1 initialized and
attempt 2 did not: no publication alone is insufficient because the real
worker correctly rejects an already-cancelled context. Removing the retry
cancellation guard is detected by the forbidden attempt-2 initialization.
The obsolete mock attempt list and three definitions are removed; three real
definitions replace them. HTTP observation and server cleanup are shared with
the existing Git timeout fixture.

All four on_abort rows now share the same feature and real Git/time setup.
They distinguish cancellation, actual exec-connection loss, nonzero Git exit
and success. The hook uses a healthy independent route, so a wrongly selected
hook can really publish. Four paired faults detect missing or unwanted actual
timestamp publications. Hook-only mock scripts/fates and eight obsolete
definitions are removed; two new hook definitions and one generalized Given
cover the migrated cases. The relay is shared with existing live diagnostics.

Six successful-startup observability cases now share one real-pod scenario in
live/task-command.feature. A real init-process gate holds the pod Pending until
the runtime watches it; the kubelet supplies initialization, sidecar and Running
status. A second wait observes the same already-running pod. Both real OTLP
spans must retain lifecycle events exactly once and name the actual node.
Nine paired faults verify these contracts, including first-snapshot handling.
This tests observation of a pre-created pod, not production pod assembly or
automatic input staging.

The failed-input diagnostic case now shares that real-pod setup. An actual
init process fails to unpack a missing archive; the runtime must identify it,
include its actual exit/log evidence and retain the same failed pod without
running the task. Two paired faults verify the name and replacement guard;
a new-only fault verifies log preservation. This tests diagnostics for a
pre-created init, not production daemon input fetching.

Image-pull observation also shares the live startup feature. An owned pod's
scheduling gate is removed only after the runtime opens its watch. A separate
unmodified Kubernetes watch must observe ContainerCreating -> Running, and
kubelet Pulling/Pulled events must identify that pod UID and main container.
PullAlways requests a real pull cycle but may reuse cached layers. The real
task must execute before the original image.pulled trace assertion is checked.
Both paired faults are detected. No registry, node or RBAC settings change.
OE-06 separately covers repeated-failure deduplication with real init retries;
one terminal failed-pod snapshot cannot replace that contract.

Twenty non-executing integration cases now use the existing real SPDY
dependency without requesting a host workspace. Pod-construction wording
explicitly says the pause pod is created, not that the command executes.
The two host artifact-chain cases opt in separately and reject missing host
setup. A redundant persisted-volume lookup case is merged without losing
its no-pod assertion; both paired lookup mutations still fail as intended.

The successful-startup consolidation reduced six cases to one and removed
three net definitions. Moving failed-input diagnostics adds one net definition
and no cases; image-pull observation adds one more definition and no cases.
Runtime construction and bounded watch/release/Wait orchestration are shared.
The successful real task case now also covers cached completion reattachment
and recovery through a fresh runtime container, using the original integration
case's pipeline/job/build metadata and opaque handle. This removes one seeded
local case, retains every existing task assertion, and adds no live case or net
definitions. Cached-status and annotation-reader faults fail the respective
exit-status assertions. Recovery does not restart a full ATC process.
The latest full v5 coverage gate (2026-09-15) passed all 564 cases
(478 local + 86 live): 1,966/2,466 production JetBridge statements
(79.724250%; target >= 50%). Combined scenario/setup time was 28m44.848s,
excluding build/report overhead. This measurement predates the subsequent assertion
consolidations below and is not a coverage measurement of the current tree.
The audit verifies every expanded scenario against the feature source, all
resource drains, namespace/storage/port cleanup, and byte-for-byte restoration
of the normal adapter. See the [migration journal](V5-MIGRATION.md) for evidence.
Coverage is satisfied; the no-doubles goal is not. The scoped status-input audit
still finds 22 lifecycle cases and three direct Node-address cases using supplied
status. Other discovery objects and retained legacy-test equivalence also need
audit; those 25 cases are not a complete inventory of every possible double.

Watch features now share one assertion for the requested pod name and phase.
The scoped case retains its identity check; every former phase-only consumer
also gets that check. This removes one definition without adding a scenario,
removes migration-only “really” wording, and names API-only pods as Pending
rather than running. All three paired selector/phase faults remain detected.
A wrong-name production fault passes the old initial-read check and fails the
new one, demonstrating an additional detection rather than only a rename.

The existing real-database volume identity scenario now also preserves the exact
constructor-supplied database object for both volume kinds, plus the daemon row’s
handle, worker, team and artifact type. Four original-Go/Brine fault pairs support
retiring JB-volume-002/003; both cloned-object faults previously passed Brine.
The same scenario now constructs two independently named persisted volumes,
initializes their real artifacts and verifies both artifact-to-volume links.
Five further original-Go/Brine fault pairs support retiring the handle and
uniqueness tests JB-volume-000/019; all four preceding object-identity faults
remain detected. No scenario or definition was added. Other restored volume
tests remain.

The two placeholder cases now check raw and gzip I/O refusal, exact error
fragments and HasExecutor on production’s NewStubVolume. Eleven original-Go
failure pairs and two gzip-only preservation checks support retiring
JB-volume-015/016/017. No scenario is added; three focused definitions replace
one stub Given (two net definitions). Normal streaming helpers are unchanged.
The production placeholder is not a test double; other legacy mocks remain.

Nested uploads now share one raw/gzip outline and verify the actual SPDY
request as well as file contents. Four exact original-Go mutation pairs support
retiring JB-volume-006; three faults previously passed Brine. Both existing gzip
path cases remain, with one additional raw row and one reusable route assertion.
The observer forwards requests and responses unchanged. One-second shutdown
grace for these disposable I/O pods reduced the same five-case live group from
231.625s to 64.644s; production lifecycle settings are unchanged.

The same passive transport observer now checks task hijacks in one three-row
outline: Bash login, TTY, and nil-TTY shell execution. It checks the original
command/wrapper, exactly one real exec request and the unchanged pod UID.
Eighteen original-Go mutation pairs support removing JB-container-044/045/046
and their mock fixture. The existing resource terminal cases keep their distinct
open-stdin, unsupervised behavior; this outline uses looked-up tasks with nil stdin.
The Bash image is digest-pinned and /bin/bash is verified before each scenario.

The existing successful task-completion scenario now shares that supervised-exec
request assertion. It also checks literal pause Command/Args and a nonnull process.
Eleven exact original-Go mutation pairs support retiring JB-container-030 with
no added scenario; output, pod identity and completion/recovery checks remain.
The no-daemon output pair is now replaced by one live task-output scenario.
It reads the actual returned *Volume, verifies exact archive bytes against an
independent real-pod read, observes download routing, and retains the original
pod. Fifteen original-Go failure pairs support removing JB-container-041/042,
their fixture and unused mount/Run helpers. The expected pod is explicit in the
feature: a separate naming fault exposed and closed a circular expectation.
Daemon-backed handoff remains separate; input-streaming and other legacy mocks remain.

The ephemeral working-set group now uses one exact-set mount table assertion
against the real API. Twenty-one original-Go/Brine mutation pairs support
retiring five legacy cases (JB-container-002/004/005/009/010). Three missing
Brine combinations are added, for two fewer cases overall and no net increase
in step definitions. The other shared legacy container mocks remain.
These tables are explicit because the pinned SDK and CLI expand outline values
in step text but not attached table cells; the migration journal records the
failed control and parser evidence. No local interpolation workaround is used.

The same table assertion now optionally checks a volume-name prefix. Exact and
trailing-slash overlaps must retain the input volume; separate paths must keep
separate mounts. Eight original-Go failure pairs support retiring the three
remaining overlap/separation cases, including field-specific real API rejection
of duplicate mount paths. All 21 preceding working-set failure observations
remain unchanged. One Brine case is added and three Go cases removed, with no
new definition. This outline uses a fixed table and expands only step text.

The failed-input scenario now also requires one failed-init event and zero
completed-init events from the real OTLP collector. The shared event-count
assertion accepts a number, covering absence and exactly-once checks without
new scenarios or definitions. A missing named span still fails a zero-count
check. OE-06 separately observes repeated actual failures on one init before
recovery; the single failed-input observation does not replace that contract.

Completion recovery is consolidated into the existing successful/failing live
tasks (exit 0/3), whose commands and completion records come from production
SPDY execution. The successful task also retains its original runtime container,
deletes its exact pod and proves it absent before recovering from memory alone.
This replaces the last seeded memory-completion case while retaining its no-pod
requirement: bypassing memory, requiring the pod and corrupting the cached exit
all fail. The earlier annotation-reader fault checks remain documented.

The three container-GC failure-isolation cases now use real PostgreSQL: owned
NOLOGIN roles selectively lose SELECT/DELETE access, and a competing transaction
locks one named failed container. Independent probes and SQLSTATE assertions
verify the actual refusals; the other containers still reach their expected
states. The three repository error-returning doubles are removed without
adding scenarios or definitions.

Build-log event deletion and cursor advancement now fail through real locks on
the first pipeline's event table or job row. The production pipeline factory
is unwrapped for those two cases; the same four-row outline still checks event
fates, the unchanged cursor, and the healthy pipeline's reaped log. Independent
SQL probes and recorded production errors identify actual lock timeouts.
The two read-failure rows now use PostgreSQL query cancellation: a temporary
table lock identifies the intended production read on an owned backend; the
fixture cancels that exact query and releases the lock before healthy work
continues. Both read-error wrappers and their two decorators are removed.
The original four-row outline and all step definitions remain unchanged.
Three production-only faults retain seven paired scenario/fault detections;
reported lifecycle inputs and the broader legacy-test audit remain work.

The scheduling-timeout case observes an actual scheduler CPU refusal,
not a supplied PodScheduled condition. Production Run now builds its git
resource pod from ContainerLimits; the fixture no longer constructs a pod.
The request exceeds every observed node's entire CPU capacity, even when
empty. This case requires stable node membership and CPU capacity during the
run and checks both afterward; do not run it during node additions or capacity
changes. The owned namespace still admits one pod and retains its memory/storage
bounds. A matching scheduler event and exact refusal text are required alongside
the original 2s/3s deadlines and waiting warnings. No PriorityClass, node or
cluster-wide RBAC is changed.

The retained Go scheduling-timeout leaf (JB-process-023) is retired.
Its five outcome assertions have per-leaf production mutation pairs with this
real-scheduler case. A further construction-failure mutation also fails the
original Go leaf and current Brine at Run, while the old pre-created Brine
fixture passes: the former construction gap is now measured and closed.
The remaining zero-grace cancellation Go leaf (JB-kept-002) is also retired.
Existing live task cancellation cases now inspect the actual DELETE request
and require exactly one successful request with an explicit zero grace period,
as well as command cancellation and pod absence. The passive observer reads an
independent body copy and forwards the original request, response and error
unchanged. Counts are HTTP attempts: retries are visible, not hidden as one
logical client call. Five production mutation pairs preserve the old assertions;
duplicate deletion passed the previous Brine case and fails the new check.
The running-cancellation integration leaf (JB-kept-001) is also retired after
its own five paired faults. The task-removal assertion now additionally lists
the owned namespace and requires it to be empty. Its three other integration
leaves remain. Those two retirements added no Brine case or definition.

The remaining core hijack-cancellation leaf (JB-kept-000) is now replaced by
a distinct real scenario: a separately looked-up task session is cancelled,
while the original task and same pod survive and the task can still complete.
The case requires zero actual DELETE attempts; all API traffic remains real.
Three production mutation pairs cover the lookup flag, deletion exclusion and
cancellation propagation. This missing contract adds one case and two
definitions; it does not reuse resource-retention assertions as hijack proof.

The basic eviction case now writes a fixed 24MiB into a 16MiB emptyDir.
The kubelet supplies the Failed/Evicted status and matching volume-limit
event; the compatibility runtime must preserve its eviction classification
and build-log diagnosis. This is pod-local storage enforcement, not node-wide
pressure. The separate node-pressure diagnostic fixtures remain migration work.

Unfinished Pending recovery now uses the API-created phase and unchanged
resource version without a status update. The distinct legacy unreported
phase remains explicitly supplied and is not claimed migrated. The unused
markPodRunning status setter is removed; both refusal cases remain.

The current inventory is 569 scenarios (482 local + 87 live), with 1,046 step
definitions.

OE-06 moved to live without adding a scenario, replacing three
obsolete host-execution phrases with two real-init actions. Both original trace
checks remain, with an additional zero-completion check using existing language.
A pre-existing OnFailure pod genuinely fails twice and recovers: this tests
logical-init deduplication, not production default restart policy or artifact fetching.
The Failed/no-container-status case now uses actual PodGC state and the real
compatibility Attach/Wait path. A scheduling gate prevents execution; an owned
finalizer retains the real Failed state, with finalizer handling shared by the
cancellation cases. The original exit-1 assertion remains. A 15-second deadline
also prevents the remaining reported-state helper from hanging on a broken
terminal classifier. This moves one case and adds one behavior-level definition.

The two terminal check-pod reuse rows now use real BusyBox exits and kubelet
status. Their existing unfinished-pod/new-UID assertion and all three step
phrases are unchanged; no scenarios or definitions were added. This verifies
replacement during Run, not execution of the subsequent check command.

The orphan-pod lookup case now reuses the live interception fixture: a real
kubelet reports Running and real exec verifies the pod's hostname before the
unchanged clean-miss assertion. Reaped-producer cases only create an API object
and verify its UID-scoped deletion; they no longer write unused pod status.
The shared worker status-writing helper is removed, with no new fixture helper,
scenario or definition. Artifact lifetime assertions remain unchanged.

PW-03 selector scoping now uses two scheduling-gated live pods. The neighbour
actually exits 1 before the watched pod is released and becomes Running. The
original identity/phase assertion remains; intermediate Pending events are
consumed only for the watched pod, never by filtering away neighbour events.
The former prototype feature moved to live/pod-watch.feature without adding
scenarios or definitions, and its direct status-writing closure was removed.

The separate reported-status subsequent-change case is now consolidated into
that live selector case: its initial Pending read and subsequent Running phase
cover the same transition, with a stronger pod-identity assertion. Both stale-
phase and wrong-phase production mutations still fail the retained assertion.
No live case, helper or definition was added.

The burst-of-updates case also runs live now. A scheduling gate preserves the
initial Pending read; an exec-released file gate lets the same main container
reach Running and then exit 0 before the runtime drains its watch. Independent
API/container/log observations establish both transitions, and the unchanged
Succeeded assertion detects both suppressed and stale terminal-event faults.
The scenario moved without adding a case; one explicit live setup definition
was added. Scheduling-gate release is shared with the selector fixture.

PW-06 fallback now shares the gated live-pod setup and startup observer. Its
real namespace-scoped watch permission is revoked while Get stays available;
the established TCP stream closes before the gate is released and the kubelet
reports Running. All three original fallback faults still fail the unchanged
assertion. The watch and exec routes share explicit/default HTTP(S) port handling;
local and live watches share the transparent routing implementation. No scenario
or definition was added.

The two reconnect/replay rows now also use real gated pods. A direct admin
connection drives actual Running or Succeeded state while the runtime route is
closed; reconnection waits for UID-scoped graceful deletion and NotFound.
Intermediate kubelet events are allowed, but replaying a consumed checkpoint
is rejected so stale resume versions cannot hide among them. Both rows retain
their final assertions and all three paired faults, including replacing replay
with a fresh Get. RealWatch.setPhase is removed. No case or definition was added.

The completed-check, selector, burst and replay fixtures share one actual-pod exit observer.
Original identity, advancing API version, terminal phase, main-container exit,
restart count, timestamps and exact kubelet logs now have one implementation.
Pod creation, neighbour ordering and production assertions remain in their
respective scenarios; the observer introduces no new step language or cases.

Direct JetBridge container-construction calls no longer allocate a dummy timing
delegate: the production method does not read that argument. Real engine
delegates are unchanged. The retired host executor's unused direct PTY
requirement was also removed from this nested module; Fly's root dependency
and the selected module versions are unchanged.

Watch-read cancellation now uses the shared persisted-annotation checkpoint to
prove an established stream, instead of supplying a Running phase. The HTTP
watch and cancelled read retain independent lifetimes. Both missing-cancellation
and swallowed-error mutations still fail the unchanged original assertion;
there are no added scenarios, definitions or fixture helpers.

The expiry case now checks the phase of the first recovered object, alongside
its UID and resourceVersion. A production fault that kept the initial phase
previously escaped because a later replay restored the final expected phase.
The new check rejects that fault. A metadata-only expiry prototype cannot
exercise changed-phase recovery, so it was not adopted; the reported lifecycle
fixture remains until that contract can be preserved without supplied status.

The cache-closing fixture no longer creates Nodes or supplies their addresses.
Its standalone daemons do not label nodes, and cache reads use real EndpointSlice
IPs directly. All ten existing cases remain: nine production faults retain
eleven paired detections, and an incorrect Node-object prerequisite now fails
two cases that previously passed with the fabricated Nodes. This removes twelve
redundant Node lifecycles per cache matrix without new cases or vocabulary.
Other Node fixtures remain where production really resolves their addresses or
labels them at daemon startup; those are not claimed migrated.

The broader no-doubles goal is not complete: other reported-status fixtures remain.
The early-sidecar image-repair probe did not preserve ContainerCreating before
main startup, so those six cases remain unchanged. Two explicitly approved fault boundaries are
labelled in code: version-then-exit-4 resource behavior and resolver-panic recovery.
These exceptions do not authorize replacing ordinary workers, pools, executors
or resolvers. Five tests solely for the retired host executor were removed;
the independent workspace-disposal contract is retained in task_workspace_test.go.
No production Ginkgo tests were retired in these increments.
See the migration journal for the evidence and remaining work.

Run on Linux with user/network namespaces enabled, a C compiler, BusyBox, the
module-required Go toolchain, matching Brine CLI and engine, PostgreSQL tools on
PATH, an OpenTelemetry Collector (otelcol), and kube-apiserver/etcd assets
selected by KUBEBUILDER_ASSETS.

Trace assertions read the real collector's OTLP JSON file after flushing the
production exporter. The runner image pins the collector archive by checksum.
For local runs, put otelcol on PATH or set BRINE_OTELCOL_BINARY to its absolute
path. Each tracing scenario owns and stops its collector and removes its files;
there is no in-memory exporter or fallback receiver.

BusyBox supplies the real sh/wget/sleep used by fetch-script tests. Set
BRINE_BUSYBOX_BINARY to an absolute working BusyBox executable if it is not
on PATH. The runner image pins a tested upstream musl build in
deploy/Dockerfile.test-runner. The build checks wget's timeout option inside
an empty private namespace; some Debian builds crash with that option.
There is no GNU-tool or shell-function fallback.

```sh
sh scripts/build-private-network
export BRINE_KUBE_CONTEXT=your-authorized-test-context
brine run --no-engine --mode sync --format jsonl
```

From the suite root, Brine discovers both the local manifest and live/.brine
and runs both tiers once. The live tier runs real pods and SPDY exec, so it
needs a reachable Kubernetes cluster. To run only the live tier:

```sh
(cd live && brine run --no-engine --mode sync --format jsonl)
```

There is no default context and no mock fallback. Each live scenario creates
its own namespace, baseline pod-security policy, resource quota and container
limits; cleanup deletes that namespace with a UID precondition and waits
for its removal. The selected identity needs namespace create/get/delete and
permission for quotas, limit ranges, pods, pod logs, exec and listing Events
inside it. The image-pull case requires pod scheduling-gate support. No
privileged containers, hostPath volumes, node changes or RBAC grants are made
by those ordinary live fixtures. The separately approved hostPath prerequisite
probe below is an explicit exception, not part of the default feature run.
CI selects `BRINE_KUBE_CONTEXT=in-cluster` to use its projected service-account
credentials directly; it must already have the required permissions.

The manifest runs scripts/run-private-network. It creates private addresses
for real daemon peers without changing host interfaces. Builds happen before
entering that namespace; missing prerequisites fail explicitly.

Native vocabulary/protocol checks and the full coverage gate (both tiers):

```sh
go vet ./...
BRINE_PROTOCOL_LAUNCHER="$PWD/scripts/run-private-network" \
  go run github.com/onsi/ginkgo/v2/ginkgo -r --timeout=5m
sh scripts/coverage
```

The coverage command requires BRINE_KUBE_CONTEXT and the live artifact
fixture approvals/configuration described below: both hostPath/hostPort
opt-ins, the approved node and port, and an absolute path to the current
static Linux daemon executable. Missing prerequisites fail before any build
or test starts. It runs both manifests and merges their instrumented counters. A failed or missing live run fails the
gate; local-only results are not reported as the full suite. Vocabulary
guards scan both tiers recursively.

Coverage must be at least 50% of production statements in
atc/worker/jetbridge, measured from Brine alone. This is not repository-wide
coverage. The coverage script builds its prerequisites and restores the
normal adapter after measurement. Do not run another build targeting .build
concurrently with it.

Use JSONL for filtered runs: this Brine revision's brief reporter counts
tag-skipped scenarios as failures. The CLI/engine revision in CI must match
the Go module pin; the task checks the runner image's build receipt.

The CI Brine task builds its static daemon from the same checkout and accepts
explicit pipeline variables for live artifact fixtures:

- `brine-allow-hostpath-tests` and `brine-allow-hostport-tests`: set to `"1"` only after approval.
- `brine-artifact-node`: the approved schedulable Linux node.
- `brine-artifact-daemon-port`: an approved unused TCP port in 49152..60999.

These variables do not grant Kubernetes permissions. The task identity still
needs the documented access, and the fixture verifies ownership and an unused
port before proceeding. The 60-minute task budget includes both tiers and
build/validation; a 30-minute budget is insufficient headroom over the previous
28m45s CLI-only measurement, before the newly migrated live cases. A runner
matching the module revision must be published under a new immutable tag and
referenced by the pipeline before deployment. Source configuration alone is
not evidence that CI ran successfully.

### Approved live artifact handoff

The two cases in features/live/artifact-integration.feature (select
@artifact-integration) first upload real bytes through a persisted artifact
volume and look it up again in PostgreSQL. A production fetch-inputs init must
deliver those bytes before the task runs. The publication case makes a Git
commit from the input, deletes its producer, and lets the unmodified pinned
Git resource fetch and publish the task output. Its source is an actual bare
Git repository inside the owned put pod, accessed through file://; this is
resource-protocol and cross-pod artifact evidence, not an external S3 service
test. The old fixture merely echoed non-JSON input under an incidental s3 name.
Original mount, exit and container-row assertions are retained; real commit
and file-content checks replace the echo-response check.


F23 in features/live/severed-artifact.feature uses the same owned storage and
node-daemon prerequisites below (select @severed-artifact). Its positive
prerequisite first publishes a completed task output and reads it after deleting
the producer. It then cuts only the writing task's transparent TLS route, proves
post-Wait growth through the independent storage observer and liveness through
fresh SPDY exec, and checks that no output location or readable alias was
published. The task's writes are stopped only after that premise is verified.


The five cases in features/live/artifact-handoff.feature now use real
production task pods, SPDY execution, node-local storage and the production
artifact daemon. They retain the exact raw/gzip/S2 contents, returned-output
gzip read, read after producer deletion, destination collision and daemon
outage contracts. The outage assertion uses the actual producer node name.

The consumer's real fetch-inputs init must exit successfully and deliver exact
files before the public StreamIn check. The fixture then clears those owned
files so StreamIn must deliver them independently. BusyBox tar returns exit 1
for the nonempty-directory collision; unlike the old host executor, SPDY does
not attach tar stderr to this API's error. The check requires that real exit,
preserved collision contents and exact partial extraction of the other members.
It does not accept an arbitrary exec failure.

Storage is the exact emptyDir path of an owned anchor pod. A separate observer
verifies ownership markers and actual kubelet reclamation through a read-only
mount of that anchor's pod directory. No broad kubelet root, other pod's path
or existing artifact store is mounted. Only the owned namespace receives the
hostPath admission exception. Resource quotas remain bounded. After deleting a
pod, the fixture waits until the owned quota no longer counts more pods than
the namespace contains. One consumer admission hit stale quota accounting in
validation; the failure is retained in the journal. Successful repeats do not
prove that every admission-cache race is eliminated.

The daemon binds one explicitly selected high TCP host port on the node's
InternalIP. After verifying that listener, setup creates an owned headless
Service and EndpointSlice pointing to that actual node endpoint and daemon pod.
This lets the production discovery client upload and find persisted artifacts.
Setup rejects an existing Kubernetes allocation or listening port;
cleanup deletes the UID-matched daemon and waits for connection refusal.
The production daemon port 7780 is not bound on the node by these tests.
No host networking, node-label changes or cluster-wide policy changes are
made. Mirroring cases explicitly enable --peer-discovery without --node-name:
only their producer daemon receives a dedicated service account with a Role
allowing list on EndpointSlices in the owned namespace. Other daemon/storage
pods receive no service-account credentials. All support pods use read-only
root filesystems, no added capabilities or privilege escalation. Task pods
retain production's generated spec.

Mirroring uses an independent peer pod and emptyDir on the same physical node,
not a second-node failure-domain test. The peer's controller-generated
EndpointSlice supplies its actual address. Checks read its filesystem before
HTTP delivery so read-through cannot disguise a missing proactive copy. A
finished producer pod is removed before peer startup to retain the existing
four-pod quota; its artifact data stays in the owned storage anchor.

This fixture requires an explicitly approved single-node cluster with the
existing concourse.dev/artifact-cache=ready label. Production tasks schedule
normally; the single-node premise prevents DirectoryOrCreate from making an
owned kubelet path on a different node. The kubelet root defaults to
/var/lib/kubelet; BRINE_KUBELET_ROOT can select an explicitly verified alternate.

From this module on Linux, build the current static daemon for the actual node:

```sh
(
  cd ../../../..
  CGO_ENABLED=0 go build -ldflags='-s -w' \
    -o atc/worker/jetbridge/brine/.build/brine-live-artifact-daemon ./cmd/artifact-daemon
)
export BRINE_KUBE_CONTEXT=your-authorized-test-context
export BRINE_ALLOW_HOSTPATH_TESTS=1 BRINE_ALLOW_HOSTPORT_TESTS=1
export BRINE_LIVE_ARTIFACT_NODE=your-approved-node
export BRINE_LIVE_ARTIFACT_DAEMON_PORT=49179 # select an approved unused port
export BRINE_LIVE_ARTIFACT_DAEMON_BINARY="$PWD/.build/brine-live-artifact-daemon"
brine run --no-engine --mode sync --format jsonl live --tags @artifact-handoff

```

The executable is size-checked before creating any live namespace. Build it
with the stripping flags shown above: debug symbols consume fixture storage
without exercising additional behavior. Producer and peer keep their existing
storage budget, with reserved space for artifact bytes and logs.

These explicit environment settings are also required for the full coverage
gate; they are not permission to run the fixture on an arbitrary cluster.
Do not run another handoff suite on the same selected node port concurrently.

The executable is gzip-compressed only for transfer. The original SHA-256,
actual ELF architecture, PID 1 executable and in-pod health are checked.
An independent pod verifies the node-IP health route before task execution.
No localhost tunnel or HTTP stand-in substitutes for the production route.

The existing non-database native probe shares this setup and additionally
checks bounded cleanup failure recovery:

```sh
go test -tags live ./steps -run '^TestLiveArtifactStorePersistsAndCleans$' -count=1 -timeout=8m

```

It has a three-minute setup bound, ten-second explicit storage-cleanup
assertion and independent recovery cleanup. Historical startup/reclamation
timeouts and the inconclusive old-probe comparison remain in the migration
journal; they are not counted as passing evidence.

### Remaining migration work and authority

| Work | What must be preserved | Constraint |
| --- | --- | --- |
| Artifact integrations | Persisted inputs, task-output consumption and real resource protocol | Now use real tasks, production fetch-init, daemon discovery and an unmodified Git resource. No host commands, installed echo scripts or reported Running state remain in these flows. Existing port 7780 and node/cluster/RBAC configuration remain untouched. |
| F23 publication after disconnect | Original failure and absent-location assertions, plus continued real writes and downstream refusal | Now uses real task/SPDY/storage/daemon, with owned cleanup. No EOF or pod status is injected. |
| Reported lifecycle and observability states | Each original state sequence and repeated-observation assertion | Some are fidelity/design problems, not permissions. A real 512Mi repeated-OOM probe retained Failed/current OOM/restart count 2 but lost previous termination on deletion; RF-10 remains unchanged. Node-level disruptions require a separately authorized environment; owned-pod faults do not justify changing nodes. |
| Partial-output put | Valid version output followed by exit 4, with nothing published | Now uses an explicitly approved executable fault in a real owned BusyBox pod: valid JSON, actual exit 4, real production pool/worker/DB/SPDY. An ordinary parse failure is not equivalent. |
| Resolver panic recovery | Recovery from an actual panic, not an HTTP/registry error | Explicit fault injection is now approved for this recovery boundary only. Ordinary resolution/authentication remains real; the injector stays clearly labelled. |

The latest handoff validation had one excluded cleanup-timeout run. Its
unchanged repeat cleaned successfully, and a read-only probe independently
confirmed the old storage directory was gone. The delay is documented, not
claimed fixed. Detailed results and evidence are in V5-MIGRATION.md.

The final direct-volume handoff pass (2026-09-15) retires the last two tests in
volume_restored_test.go and removes that file. One existing handoff outline now
has direct/deferred binding rows, sharing raw/gzip byte and routing assertions.
Seven exact original-Go/live fault pairs support the retirement. Inventory is
576 cases and 1067 definitions; that checkpoint retained 43 Ginkgo specs, which
is not a count of mocks. Native daemon and other lifecycle/integration doubles
remain. Final results, the corrected mock call-order analysis, and the broader
backlog are recorded in V5-MIGRATION.md.

The supervised-task TTY follow-up (2026-09-15) retires two more mock-backed
entries, leaving 41 Ginkgo specs. The existing two terminal rows now exercise
both resource processes and nil-stdin supervised tasks. Three task-only faults
fail the new request checks while the former resource-only scenarios still
pass. No Brine case or definition was added. Direct-mode sidecar routing and
the broader lifecycle/native-daemon backlog remain; see V5-MIGRATION.md.

The subsequent direct-sidecar checkpoint (2026-09-15) retires the two SC-07
mock-backed entries after matched original-Go/real-Brine logging faults. The
existing sidecar-log outline covers exec/direct and dedicated/fallback in four
rows, with real source logs and observed runtime requests. No new definitions
were added: inventory is 578 cases (484 local + 94 live), 1067 definitions,
and 39 remaining Ginkgo specs, all passing. Three native suites and default/live
vet pass; all 14 owned namespaces were independently confirmed absent. See
[V5-MIGRATION.md](V5-MIGRATION.md#2026-09-15--direct-sidecar-log-contracts-migrated)
for paired faults and preserved source. This is not a new full-suite or coverage
measurement; the no-doubles migration and final validation remain incomplete.

The final restored-runtime follow-up (2026-09-15) absorbs PE-02 into the existing
direct-command outline and removes behavioral_runtime_spec_restored_test.go.
Three task/resource-command-specific fault pairs preserve exact command, argument
and creation-counter behavior using the real API server. Inventory is 579 cases
(485 local + 94 live), 1067 definitions and 38 remaining Ginkgo specs.
All remaining specs, native suites, vet checks and the complete affected feature
pass. The broader no-doubles migration and final validation remain incomplete;
see V5-MIGRATION.md for evidence and recoverable original source.

The basic workflow follow-up (2026-09-15) retires JB-integration-000 into the
existing live completion/recovery outline, adding its Ubuntu/command/handle row.
Nine exact original-Go/real-Brine fault pairs preserve its assertions. The two
other integration cases remain unchanged. Inventory is 580 cases (485 local +
95 live), 1067 definitions and 37 remaining Ginkgo specs. Final native/vet
checks and focused live controls pass; all 32 owned namespaces are absent. See
V5-MIGRATION.md for the broader shared-regression scope, evidence and remaining
work. No new full-suite coverage measurement or goal-completion claim is made.

The mounted npm/PostgreSQL follow-up (2026-09-15) retires JB-integration-012
after ten matched original-Go/real-Brine faults. Real application volume uploads,
a database query and npm execution replace its doubles using the shared task runner.
Only the put-input workflow remains in integration_restored_test.go. Inventory is
581 cases (485 local + 96 live), 1068 definitions and 36 remaining Ginkgo
specs. Final targeted checks pass and all 15 owned namespaces are absent. The
no-op input contract and broader no-doubles/final-validation work remain open;
see V5-MIGRATION.md for scope, evidence and recoverable original source.

The mounted-input follow-up (2026-09-15) supersedes the no-op retention above:
JB-container-040 is replaced by a second row in the same application outline,
with three matched mutation failures and real cluster controls. Inventory is
582 cases (485 local + 97 live), 1068 definitions and 35 remaining Ginkgo cases.
The retired file is recoverable; details and evidence are in V5-MIGRATION.md.
The broader no-doubles goal and final full-suite validation remain incomplete.

The pause-recovery migration retires fourteen mock-backed cases into eight
real-cluster rows, one pure policy table and real-byte-stream checks. No new
Brine scenarios or definitions were needed for the final four retirements.
Preemption metadata and impossible defensive inputs are pure policy coverage;
real Brine cases check startup/dial recovery, init failure, interrupted exec,
creation counts and diagnostics. See V5-MIGRATION.md for the exact boundary.
The final mock-backed Ginkgo workflow is now a real S3 put against an owned
MinIO server. Exactly two backed input mounts, exact stdout and zero exit remain
asserted, with independent uploaded-byte verification and five paired faults.
Explicit volume uploads establish the inputs; this is not automatic staging.
The legacy put file and unused fake artifact/executor helpers are removed.
Current inventory: 591 cases (485 local + 106 live), 1072 definitions.
All 20 default Ginkgo cases (shell/quoting), native/pure checks, targeted live
runs, vet and builds pass. All 10 owned namespaces from this checkpoint are
absent. Native doubles, injected statuses and final full-suite validation remain
open. See V5-MIGRATION.md for evidence and recoverable original sources.
