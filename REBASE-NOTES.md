# Rebase notes: codex/hangar-core-foundation -> origin/core

26 commits replayed onto `origin/core` (594609e92d). Merge base was 069c8e8634.
Core added 42 commits since the branch forked, the bulk of them the
artifact-extraction containment rework (ab1c66c2c5 .. 559921ef96).

## Conflicts and non-trivial resolutions

### 1. cmd/artifact-daemon/main.go (commit 2a82eb9117 "serve strict Hangar trees")
Textual conflict at the server construction site.

- core: added `os.MkdirAll(*storagePath)` before construction and changed
  `NewServer` to return `(*Server, error)` because it now acquires an `*os.Root`
  handle on the storage directory.
- branch: declared `hangarLabeler` and `closeHangar` beside the old
  single-value `NewServer` call.

Resolved in favour of core: kept the MkdirAll, kept the two-value `NewServer`
with the fail-closed `os.Exit(1)` on a storage-root open failure, and appended
the branch's two declarations after it. The Hangar service is still composed
later (line ~219) and is unaffected by the handle change because it takes
`storagePath` as a string and does its own scratch-dir validation outside the
storage root.

### 2. cmd/artifact-daemon/hangar_handlers.go + containment.go (same commit)
NOT a textual conflict — a semantic one core's own guard caught.

`TestArchitecture_HandlersRefuseThroughOnePath` (core commit a8a781c1e2, "Make
every refusal visible: one path, counted and logged") failed: the Hangar
handlers replied through two standalone writers, `writeHangarMalformed` and
`writeHangarError`, both calling `http.Error` directly. Every Hangar refusal
would have been invisible in `artifact_daemon_refusals_total` and in the log.

Ported rather than exempted. The two writers became `(*Server).refuseHangarMalformed`
and `(*Server).refuseHangar`, which call `s.refuse`. Status codes, case order
and the client-visible message are byte-for-byte what the writers produced.
Five reason labels were added to the bounded set in containment.go
(`malformed`, `limit_exceeded`, `conflict`, `tree_verification`, `unavailable`);
`unauthorized` reuses the existing `capability` and `not found` the existing
`not_found`. The route label is `refusalRoute`'s `r.Pattern`, which stays
bounded for the three registered Hangar patterns.

`refuse` writes `err.Error()` to the client, so the new methods pass
`errors.New(<fixed classification>)` rather than the underlying Hangar error —
a Hangar error can carry a scope, a digest or a GCS message, and none of that
should reach an unauthenticated caller.

`TestHangarStatusPrecedenceNeverDowngradesCompoundFailuresToNotFound` called
`writeHangarError` directly; it now drives `server.refuseHangar` through a real
test server, so it exercises the same path production does.

### 3. cmd/artifact-daemon/hangar_test.go (commits 2a82eb9117, c0c08969db)
`NewServer` gained an error return in core (c76e4ac4a1, "every filesystem
operation goes through a handle"). Both Hangar call sites now check it and
`t.Fatalf`, matching core's own `newDaemonServer` helper.

### 4. cmd/artifact-daemon/main.go (commit c0c08969db "close Hangar service boundaries")
That commit moved `var hangarLabeler` up into the labeling block and added
`cleanupDaemonServices` calls to every early `os.Exit(1)` after labeling, so
the node's cache/Hangar readiness labels are removed on a failed startup. Core
then added two NEW early exits ahead of them — the `os.MkdirAll(storagePath)`
failure and the `NewServer` storage-root failure. I added the same
`cleanupDaemonServices` call to both. Without it the branch's own invariant
would have had a hole core opened: a daemon that labels the node ready and then
dies opening its storage root would leave `concourse.dev/artifact-cache=ready`
and `concourse.dev/hangar-v1` set on a node that serves nothing.

### 5. cmd/artifact-daemon/hangar_handlers.go (commit 86e6612a4e "preserve fail-closed daemon state")
Conflict only because resolution 2 rewrote the same lines. Took the incoming
`errors.Join(normalizeHangarIOError(copyErr), normalizeHangarIOError(closeErr))`
form and routed its single reply through `s.refuseHangar`.

### 6. atc/atccmd/command.go (commit 746e941cc5 "mount exact Hangar tree inputs")
Both sides added fields to the same `Kubernetes` flag struct. Kept core's whole
list (it carries `ArtifactDaemonResolveCapabilityKey` and
`ArtifactDaemonResolveCapabilityTTL` from the signed-resolve work, 1e023e7ca4)
and inserted the branch's `HangarEnabled` / `HangarCapabilityKey` before
`ImageRegistryPrefix`. gofmt realigned the tags.

### 7. atc/worker/jetbridge/storage_daemonset.go (commit 746e941cc5)
Three hunks, all resolved to the branch's structure, because core's
`if len(items) == 0 { return nil, nil }` early return would have silently
dropped a pod's Hangar materialization init container whenever the pod had
Hangar inputs and no ordinary artifact inputs. The branch replaces it with an
accumulating `[]corev1.Container` guarded per container, which returns a nil
slice in the both-empty case — same behaviour for ordinary pods.

The resolve-capability signing core added inside the input loop
(`b.resolveSigner.SignResolve`) merged cleanly and is untouched; Hangar inputs
`continue` before reaching it and carry their own `HangarGrantSigner` grant.

### 8. atc/worker/jetbridge/daemon_tls_test.go, storage_daemonset_test.go (commit 746e941cc5)
Six identical conflicts, all on a `t.Fatalf` message string
(`"BuildFetchInitContainers: %v"` vs `"build fetch init containers: %v"`).
Kept core's wording.

### 9. deploy/chart/templates/artifact-daemon-daemonset.yaml (commit 6b38e1d333 "gate the Hangar GCS foundation")
Both sides added a `volumeMounts` entry at the same offset — core's
`resolve-capability` secret mount and the branch's `hangar-scratch` emptyDir
mount. Kept both, each under its own `{{- if }}`.

### 10. atc/atccmd/command.go, second time (commit 6b38e1d333)
Same struct, one more field: `HangarCapabilityTTL`. Inserted after
`HangarCapabilityKey` in core's list.

## Post-rebase cleanup

`REBASE-NOTES.md` was swept into commit "feat(jetbridge): mount exact Hangar
tree inputs" by a `git add -A`. Removed with a second `git rebase -i` (edit
that commit, `git rm --cached`, amend) and added to the worktree's
`info/exclude`. Final head has 26 commits and no notes file.

## Verification of the design's hard rule (no agent coupling)

    $ go list -deps ./hangar/ | grep concourse
    github.com/concourse/concourse/hangar

`hangar/` has zero first-party dependencies. `grep -rniE 'ticket|workflow|
playbook|methodology|\bagent\b|anvil'` over `hangar/` and the three daemon
Hangar files matches only `hangar/architecture_test.go`, which is the guard
itself (`TestArchitectureHasNoAgentImports`, rejects any import containing
`/agent/` or ending `/agent`). The Hangar contract persists only Scope, Digest,
Generation, handle and volume — no consumer identifier.

## Containment audit of the merged result

`hangar/` does NOT use core's `*os.Root` helpers. It uses a stricter mechanism
of its own: `openat(dirfd, name, O_RDONLY|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC)`
per path component, each name first checked by `validFilesystemComponent`
(rejects "", ".", "..", anything containing `/`, `\` or NUL, >255 bytes), and
each opened directory re-identified against its parent with
`sameOpenEntryAt` before use. Extraction into the private capture area does go
through `*os.Root` (`hangar/tree.go`). No production path in `hangar/` joins an
untrusted string onto a storage path.

Symlink creation in `hangar/` happens at exactly one site
(`hangar/tree.go:878`, `root.Symlink`), guarded by `cleanSymlinkTarget`, which
is strictly stronger than core's `validateSymlinkTarget`: same
resolve-against-parent-and-reject-`..` rule, plus rejection of empty, absolute,
backslash-bearing, drive-like and over-long targets.

I did NOT rewrite this onto core's helpers. Core's `containment.go`,
`extractTarToRoot` and `validateSymlinkTarget` live in `package main` under
`cmd/artifact-daemon` and are not importable; porting would mean weakening
per-component `O_NOFOLLOW` and fd-identity revalidation down to `os.Root`.
See "Reviewer items" below for what this costs.

## Reviewer items — things that are now semantically doubtful

1. **Two containment implementations, one guard.** Core's structural guards
   (`architecture_root_test.go`, `architecture_containment_test.go`,
   `refusal_visibility_test.go`) scan `os.ReadDir(".")` — i.e. only
   `cmd/artifact-daemon`. `hangar/` is invisible to all of them. Its own
   `architecture_test.go` checks agent imports and nothing else. So the
   symlink-creator rule, the one-place-derives-a-key rule and the
   no-ambient-write rule are enforced in one package and merely *observed* in
   the other. Nothing keeps `cleanSymlinkTarget` and `validateSymlinkTarget` in
   agreement. Suggested: a `hangar/`-local guard mirroring
   `TestArchitecture_SymlinkCreationIsValidated`.

2. **Ambient `os.*` calls in daemon production code that AC5 structurally
   cannot see.** `cmd/artifact-daemon/hangar_handlers.go` calls
   `os.CreateTemp`/`os.Remove`/`os.Open`; `cmd/artifact-daemon/hangar.go` calls
   `os.MkdirAll`/`os.Chmod`/`os.Lstat`/`filepath.EvalSymlinks`.
   `TestArchitecture_NoAmbientWriteThroughAJoinedKey` only flags a function
   that ALSO joins onto `storagePath`, and none of these do, so the guard stays
   green without having proved anything about them. I believe they are safe —
   the paths are the operator's `--hangar-scratch-dir` (validated at startup to
   be an absolute, real, 0700 directory outside the storage root) and the
   canonicalizer's own private temp root — but that is my reading, not a test.

3. **`POST /hangar/v1/materializations` is mTLS-exempt.** It is registered
   outside `protect(...)` (`server.go:350`), so with TLS on it is the only
   Hangar route a caller without a client certificate can reach. Its
   authentication is the per-item signed grant, verified for the whole batch
   before any materialization runs. `validateHangarOptions` refuses to start
   without a 32-byte capability key, so `GrantVerifier` cannot be nil while
   Hangar is enabled — but this is the same exemption shape core just spent
   commits c8c54faf99/1e023e7ca4 hardening for `/resolve`, and it deserves the
   same explicit review.

4. **The NetworkPolicy ingress selector changed, and it is not a Hangar
   change.** `artifact-daemon-networkpolicy.yaml` moved the task-pod ingress
   rule from `matchLabels: app.kubernetes.io/name: <chart name>` to
   `matchExpressions: concourse.ci/worker Exists`. Task pods are labelled
   `concourse.ci/worker=<workerName>` and `concourse.ci/type=...` by
   `buildPodLabels` (container.go:727) and never carry
   `app.kubernetes.io/name` — so the OLD rule matched no task pod at all and
   the NEW rule is the first one that actually admits them. That is very
   probably a genuine fix, but it widens what can reach an mTLS-exempt port and
   it rides in on a Hangar commit. Confirm nothing else in the namespace
   carries `concourse.ci/worker`.

5. **Hangar refusals now count as refusals, including 503s.** My port routes
   `ErrInfrastructure` / `context.Canceled` / `DeadlineExceeded` through
   `s.refuse` with reason `unavailable`, so `artifact_daemon_refusals_total`
   now includes server-side faults, not only client-caused refusals. Core's
   only precedent (`handleDurableRestore`) went the other way and exempted a
   normal-outcome 404. I chose visibility over purity; if the metric is meant
   to mean "the client did something we rejected", the 503 branch should call
   `http.Error` directly and be added to the `known` map with that reason.

6. **`GET /hangar/v1/.../generations/{n}` spools the whole tree to disk before
   replying.** `handleHangarOpen` copies up to `MaxArchiveBytes` into
   `Canonicalizer.TempDir` (the `hangar-scratch` emptyDir) so it can verify the
   digest before the first byte reaches the client — correct for a fail-closed
   store, but the default `--hangar-max-content-bytes` is 10 GiB and the chart
   gives `hangar-scratch` an unbounded `emptyDir: {}`. Concurrent opens can
   fill the node's ephemeral storage. Not a rebase regression; worth a
   `sizeLimit`.

7. **`-tags live` does not compile, and it does not compile on `origin/core`
   either.** `atc/worker/jetbridge/live_test.go` references
   `postgresrunner.StandardTestRunner`, which is referenced exactly once and
   defined zero times across all of `origin/core`. The branch's new
   `live_hangar_flow_test.go` therefore cannot be built or run in this tree.
   PRE-EXISTING, not caused by this rebase — but it means the "isolate Hangar
   live contract" commit is unverifiable here.

8. **Core's early-exit cleanup, extended by me.** I added
   `cleanupDaemonServices` to the two new startup failure paths core
   introduced (`os.MkdirAll(storagePath)` and `NewServer`). That is my code,
   not the branch author's, and it runs before `closeHangar` is meaningful, so
   it passes a no-op closer. Check that reads the way you want it to.

## Test results (all run in this worktree)

    $ go build ./...                       # clean
    $ go vet ./cmd/artifact-daemon/...     # clean
    $ gofmt -l .                           # (touched files) clean

    ok  github.com/concourse/concourse/cmd/artifact-daemon          95.873s
    ok  github.com/concourse/concourse/cmd/artifact-daemon/durable   4.356s
    ok  github.com/concourse/concourse/hangar                        2.464s
    ok  github.com/concourse/concourse/deploy/chart/tests            21.801s
    ok  github.com/concourse/concourse/atc/atccmd                     1.797s
    ok  github.com/concourse/concourse/atc/worker/jetbridge         129.059s
    ok  github.com/concourse/concourse/atc/runtime                    2.266s
    ok  github.com/concourse/concourse/atc/runtime/runtimetest         0.769s

    $ go vet -tags live ./atc/worker/jetbridge/...
    vet: atc/worker/jetbridge/live_test.go:25:39: undefined: postgresrunner.StandardTestRunner
    # PRE-EXISTING on origin/core: referenced once, defined zero times.

Re-run at the final head 3229714e2c after the import tidy and the live_test.go
restoration:

    ok  github.com/concourse/concourse/cmd/artifact-daemon          131.391s
    ok  github.com/concourse/concourse/cmd/artifact-daemon/durable    3.638s

9. **`Config.ArtifactDaemonNamespace` is set by nothing in production.** The
   branch adds it to `atc/worker/jetbridge/config.go` and reads it in
   `daemonTLSServerName`, but no flag in `atccmd` and no chart value ever
   assigns it. Its only writers are `daemon_tls_test.go:143` and
   `live_test.go:195`. Either wire a
   `--kubernetes-artifact-daemon-namespace` flag or drop the field; as it
   stands it is a config knob that exists only for a test.

10. **The Hangar init path is documented as NOT server-authenticated.**
    `docs/hangar.md` states plainly: "Node-IP TLS encrypts the init-to-daemon
    request, but the current init client does not verify the daemon's server
    identity. Do not describe this path as server-authenticated mTLS." The
    compensating control is the local, independently verified read-only
    receipt. This is honest documentation of a real gap, and it is the same
    gap `/resolve` has — but core has since spent five commits hardening
    `/resolve` with signed capabilities, and the Hangar path did not get that
    treatment because it was written before them. Reviewer should decide
    whether the grant alone is the intended equivalent.

## One thing I changed beyond conflict resolution — commit 3229714e2c

**The branch silently removed a live-suite safety net that is not Hangar code.**

`feat(jetbridge): mount exact Hangar tree inputs` (originally 746e941cc5)
deleted `adoptResolveCapability` and its call from
`atc/worker/jetbridge/live_test.go` — 64 lines, no replacement, in a commit
about Hangar inputs. This is NOT a rebase artifact: the function exists at the
merge base 069c8e8634, core has not touched `live_test.go` since
(`git diff 069c8e8634 origin/core -- atc/worker/jetbridge/live_test.go` is
empty), and the pre-rebase branch tip has zero occurrences of it.

That helper copies the deployed daemon's resolve-capability HMAC key into the
live config. Without it the suite signs nothing the daemon will accept, and
the comment the branch deleted says exactly how that fails: "the init
container still exits 0 and the step pod still starts, but the requested
artifact never lands, so the step fails later with a missing file rather than
a fetch error." A live suite that looks like it exercises the daemon while
exercising nothing.

I restored it verbatim as a separate tip commit so it can be dropped with one
`git reset --hard HEAD~1` if the deletion turns out to be deliberate. It
type-checks under `-tags live` (verified by temporarily stubbing the
separately-broken `postgresrunner.StandardTestRunner` reference, then removing
the stub and confirming `git status --porcelain` clean).

Head is now 27 commits: the 26 rebased plus this one.

## `make test-unit`: 85 suites, one failure, and it is not this branch

    Ginkgo ran 85 suites in 16m57.684346416s

    There were failures detected in the following suites:
      concourse ./cmd/concourse

    Test Suite Failed
    make: *** [test-unit] Error 1

The failure:

    [FAILED] in [AfterEach] - cmd/concourse/concourse_test.go:61
    Timed out after 1.000s.
    interrupted ginkgomon process failed to exit in time
    Expected
        <<-chan error | len:0, cap:1>: 0x14000470540
    to receive something.
    Summarizing 1 Failure:
      [FAIL] Web Command [AfterEach] starts atc
    Ran 2 of 2 Specs in 58.371 seconds
    FAIL! -- 1 Passed | 1 Failed | 0 Pending | 0 Skipped

The spec itself passed; the AfterEach did not. `ginkgomon.Interrupt` is called
with no interval, so it inherits Gomega's default 1-second `Eventually`
timeout for a whole ATC to interrupt, drain its trackers and be reaped. The
log shows `atc.tracker.drain.done` at 15:22:24.392 and the failure at
15:22:25.391 — exactly 1.000s later. The machine was at load average 25-34
throughout (several other agents in this session were running `atc/db` and
their own tiers concurrently).

This branch does not touch `cmd/concourse`:

    $ git diff --name-only origin/core..HEAD | grep -c '^cmd/concourse'
    0

Attributed by running the package four times, alternating trees, one worktree
per tree, each `git status --porcelain` clean:

    this branch (3229714e2c)  attempt 1: FAIL   attempt 2: ok  (73.607s)
    origin/core (594609e92d)  attempt 1: FAIL   attempt 2: ok  (48.294s)

Same failure on `origin/core`, same pass on both. The attribution is
test-level, not file-level: `origin/core` fails on the SAME assertion, at the
SAME line, with the SAME message —

    • [FAILED] [11.861 seconds]
      [AfterEach] .../core-baseline/cmd/concourse/concourse_test.go:60
      [It]       .../core-baseline/cmd/concourse/concourse_test.go:68
      [FAILED] in [AfterEach] - .../cmd/concourse/concourse_test.go:61
      [FAILED] Timed out after 1.001s.
      interrupted ginkgomon process failed to exit in time
      Summarizing 1 Failure:

It is a pre-existing, load-induced flake in a test whose shutdown budget is
one second. Worth
raising separately as `ginkgomon.Interrupt(concourseProcess, "30s", "1s")`,
but it is not a rebase regression and not a Hangar regression.

Every other suite passed, including every package this branch touches.

---

# Second rebase: 2026-09-05 — onto origin/core c1c3e70e7c

30 commits replayed onto `origin/core` c1c3e70e7c ("docs(releases): start the
0.3.2 notes…"). Previous base was 594609e92d, which is still the merge base;
core added 102 commits on top of it (the pipeline-templates merge, release
0.3.1, and the CI/live-suite work of 2026-09-04). New head 1913e75035, still
30 commits.

## Textual conflicts (2, both the same file)

### A. `atc/atccmd/command_test.go` — import block
(commit `feat(chart): gate the Hangar GCS foundation`)

Core's RBAC-validation tests added `atc/db` and `github.com/concourse/flag/v2`;
the branch added `github.com/concourse/concourse/hangar`. Independent additions
at the same offset — kept all three, gofmt order.

### B. `atc/atccmd/command_test.go` — end of the suite body
(commit `feat(jetbridge): mount exact Hangar tree inputs`)

Pure append-vs-append. Core appended `writeRBACConfig` plus seven
`TestCustomRoles*` cases; the branch appended
`TestHangarRuntimeRequiresCompleteDaemonTLSAndExactCapabilityKey` and
`TestHangarRuntimeAcceptsCompleteConfigurationAndDisabledCompatibility`. Kept
both blocks in that order; the two sets assert on different validators
(`ValidateCustomRolesForTest` vs `ValidateK8sRuntimeForTest`) and do not
interact.

## Semantic conflicts git merged cleanly and silently broke (2)

Both are duplicate top-level declarations: core and the branch added the same
symbol at different offsets in the same file, so the three-way merge produced
no marker and a file that does not compile. Neither was caught by the conflict
list — only by `go build ./...` and `go vet ./...`.

### C. `Config.ArtifactDaemonNamespace` redeclared — `atc/worker/jetbridge/config.go`

    atc/worker/jetbridge/config.go:204:2: ArtifactDaemonNamespace redeclared
        atc/worker/jetbridge/config.go:193:2: other declaration

Core added the field in `06d3c556b1` ("the live suites compile again"); the
branch added its own in `test(jetbridge): isolate Hangar live contract`. The
two declarations are semantically identical — same type, same "empty means
`Namespace`" rule, same single consumer `daemonTLSServerName`. Resolved toward
core: `git rebase -i` onto that branch commit, deleted the branch's copy and
its doc comment, amended. Core's declaration and comment are what survive.

The rest of that branch commit's `daemon_tls.go` hunk is now a comment-only
change, because the functional line it introduced
(`namespace := cfg.ArtifactDaemonNamespace`) is byte-identical to core's and
merged away. That is correct, not a lost change.

### D. `assertPodMountsResolve` redeclared — `atc/worker/jetbridge/daemonset_integration_test.go`

    daemonset_integration_test.go:840:6: assertPodMountsResolve redeclared
        daemonset_integration_test.go:314:6: other declaration

Core has this helper on `origin/core` at line 614; the branch's
`feat(jetbridge): mount exact Hangar tree inputs` added a second one. They are
NOT equivalent:

  - core's counts volumes by name and fails on `!= 1`, so it catches a
    *duplicate* volume name as well as a missing one, and it uses `t.Fatalf`;
  - the branch's only checks set membership and uses `t.Errorf`.

Resolved toward core, which is strictly stronger: amended the branch commit to
drop its own definition. The branch's two call sites (the Hangar pod-shape
assertions) now run core's version, which is a tightening, not a loosening —
and the full `atc/worker/jetbridge` suite is green under it.

## Reviewer items from the first rebase that core has since closed

**Item 7 — `-tags live` did not compile — CLOSED by core.** `06d3c556b1`
replaced the `postgresrunner.StandardTestRunner` reference with a
`postgresrunner.Runner` booted directly in `TestMain`, and added
`go vet -tags live ./atc/worker/jetbridge/` to the `build-and-vet` job.
`go vet -tags live` and `go vet -tags hangar_live` are both clean at this head.
This is the first time `live_hangar_flow_test.go` has been type-checked in
this tree; its `//go:build live || hangar_live` line means core's new
`-tags live` vet step now covers it in CI without further wiring.

**Item 9 — `Config.ArtifactDaemonNamespace` is set by nothing — MOVED to core.**
Core adopted the field itself with the same semantics, so it is no longer this
branch's to justify. The underlying observation still holds against core: no
`atccmd` flag and no chart value assigns it; its only writers are
`daemon_tls_test.go` and `live_test.go`. Not a rebase item any more.

Items 1-6, 8 and 10 stand unchanged; nothing in core's 102 commits touched
`hangar/`, the daemon's Hangar files, or the NetworkPolicy template.

## The two live-suite restore commits, re-checked against core's rewrite

Core rewrote `live_test.go` twice since the first rebase (`06d3c556b1`
TestMain, `5133d0ddbc` per-test database memoisation). The branch's deletion of
`adoptResolveCapability` and the tip commit that restores it both replayed
without conflict. Verified the net result rather than trusting that:

    $ git diff origin/core..HEAD -- atc/worker/jetbridge/live_test.go
    $ git diff origin/core..HEAD -- atc/worker/jetbridge/live_worker_test.go
    (both empty)

Both files are byte-identical to `origin/core`. The delete-then-restore pair
cancels exactly, adds nothing of its own, and preserves core's newer live-suite
work untouched. If either restore commit is dropped later, that diff stops
being empty — which is the cheapest way to check.

## `deploy/concourse-pipeline.yml`

Both sides changed this file; no conflict. Core's `22d086c913` deleted the
whole `k8s-container-tests` job and `8ecbf14430` reworked `build-image`. The
branch's only hunk is a focused `go test -tags hangar_live -run
'^TestLiveHangarGeneratedPodMaterializesStrictTree$'` line inside
`k8s-live-tests`, a job core kept, and it survives intact. Confirmed the
branch adds nothing to the deleted job.

## Invariants re-verified at head 1913e75035

  - `hangar/architecture_symlink_test.go` present and green (the drift guard
    added in the first rebase).
  - `hangar/tree.go` still has exactly one symlink creator (`root.Symlink`,
    line 878) behind `cleanSymlinkTarget`; extraction still goes through
    `os.OpenRoot`.
  - `cmd/artifact-daemon/hangar_handlers.go` contains no `http.Error` call —
    the only textual match is the comment explaining why. Every Hangar refusal
    still goes through `s.refuse`.
  - `hangarSem` still bounds the materialization route at
    `maxConcurrentHangarMaterializations = 4`, acquired after authorization and
    before any item runs.
  - `gofmt -l` clean over every `.go` file in `origin/core..HEAD`.

## Housekeeping note for the next rebase

The first rebase's claim that `REBASE-NOTES.md` was "added to the worktree's
`info/exclude`" does not take effect. Git reads `$GIT_COMMON_DIR/info/exclude`
(`/Users/tdmtrader/concourse/concourse/.git/info/exclude`), never the
per-worktree `.git/worktrees/<name>/info/exclude`:

    $ git check-ignore -v REBASE-NOTES.md
    $ echo $?
    1

So both untracked files in this worktree — `REBASE-NOTES.md` and the leftover
`cmd/artifact-daemon/hangar_grant_review_test.go` from the earlier review pass
— are still visible to `git add -A`, which is how the notes file was swept
into a commit the first time. Commit by pathspec here, or put the pattern in
the common exclude. Every commit in this rebase was made with an explicit
pathspec or `--amend` over an already-staged set; `git status --porcelain`
shows only those two `??` entries at the final head.

Note also that `hangar_grant_review_test.go` is `package main` under
`cmd/artifact-daemon`, so it is compiled and run by every local
`go test ./cmd/artifact-daemon/...` in this worktree — including the run
reported below. It is untracked and will not be pushed, so CI runs that
package without it.

## Test results at head 1913e75035 (all in this worktree unless said otherwise)

    $ go build ./...                                        # clean
    $ go vet ./cmd/artifact-daemon/... ./hangar/...          # clean
    $ go vet ./...                                          # clean
    $ go vet -tags hangar_live ./atc/worker/jetbridge/       # clean
    $ go vet -tags live      ./atc/worker/jetbridge/         # clean  (NEW: was
                                                            # broken before)
    $ gofmt -l <every .go in origin/core..HEAD>              # clean

    ok  github.com/concourse/concourse/hangar                        1.707s
    ok  github.com/concourse/concourse/cmd/artifact-daemon          99.451s
    ok  github.com/concourse/concourse/cmd/artifact-daemon/durable    1.751s
    ok  github.com/concourse/concourse/deploy/chart/tests            23.163s
    ok  github.com/concourse/concourse/atc/atccmd                     1.478s
    ok  github.com/concourse/concourse/atc/worker/jetbridge          94.887s
    ok  github.com/concourse/concourse/atc/runtime                    0.312s
    ok  github.com/concourse/concourse/atc/runtime/runtimetest        0.601s

### `make test-unit`: one failure, in `cmd/concourse`, and it is a load flake

    Ginkgo ran 88 suites in 15m2.248802833s
    There were failures detected in the following suites:
      concourse ./cmd/concourse
    Test Suite Failed
    make: *** [test-unit] Error 1

    Summarizing 1 Failure:
      [FAIL] Web Command when CONCOURSE_CONCURRENT_REQUEST_LIMIT is invalid
             [It] prints an error and exits
      cmd/concourse/concourse_test.go:84
      [FAILED] Timed out after 1.000s.
      Got stuck at:

      Waiting for:
          'InvalidAction' is not a valid action
    Ran 2 of 2 Specs in 17.264 seconds

Note this is a DIFFERENT spec from the first rebase's failure. Core fixed that
one — `AfterEach` now calls `ginkgomon.Interrupt(concourseProcess, 30*time.Second)`
with a comment citing unit-tests #970/#971. Line 84 is the assertion core did
not give an explicit budget, so it inherits Gomega's 1-second default:

    Eventually(concourseRunner.Err()).Should(gbytes.Say("'InvalidAction' is not a valid action"))

Attribution, in order of strength:

**1. Core baseline `make test-unit` on the same machine — PASSED.**
Worktree `.worktrees/hangar-core-baseline-0905` detached at `c1c3e70e7c`,
`git status --porcelain` clean:

    Ginkgo ran 87 suites in 13m3.752086333s
    Test Suite Passed

(87 vs 88 is the failing suite being counted twice in the branch run; the
suite-name sets are identical — `comm` over both logs is empty both ways.)

That alone is NOT both-red, so:

**2. Direct measurement of the thing the assertion measures.** Built both
binaries and timed spawn → the message, 10 interleaved runs each:

    branch time-to-exit-with-message ms: median 16  min 15  max 18
    core   time-to-exit-with-message ms: median 14  min 13  max 15

Both are ~60x inside the 1-second budget. The branch does cost 2 ms and 20 MB
(146,975,682 vs 126,802,546 bytes) because `atccmd` now pulls `hangar`, which
pulls `cloud.google.com/go/storage` — 168 extra packages in the web binary's
dependency graph (1515 vs 1347; `cloud.google.com/go/storage` 5 vs 0). Real,
worth knowing, and nowhere near enough to explain a 1-second timeout.

**3. Interleaved repeats, alternating trees in one window.** 14 rounds:

    branch  14 runs: 13 PASS, 1 FAIL
    core    14 runs: 14 PASS, 0 FAIL

plus a 3-run branch burst that failed 3/3 while another agent's
`ginkgo ./atc/db/` was saturating the box, and a 20-run interleaved block on a
quiet machine that passed 20/20. Failures track machine load, not tree.

**4. The branch does not touch the package.**

    $ git diff --stat origin/core..HEAD -- cmd/concourse atc/postgresrunner
    (empty)

Conclusion: pre-existing, load-induced, and core's own to fix — the sibling
assertion three lines up already carries `"30s", "2s"`, and line 84 should
carry a budget too. Not a rebase or Hangar regression. Flagged rather than
silently re-run until green.

## New reviewer item found by this rebase

11. **The web binary now links the GCS client, whether or not Hangar is on.**
    `atc/runtime/types.go`, `atc/atccmd/command.go`, `atc/worker/jetbridge/config.go`
    and `.../storage_daemonset.go` import `github.com/concourse/concourse/hangar`
    in production code, and `hangar/gcs.go` imports `cloud.google.com/go/storage`.
    Measured on `./cmd/concourse`:

        deps  1515 (branch)  vs  1347 (core)     +168 packages
        cloud.google.com/go/storage: 5 packages  vs  0
        binary 146,975,682 bytes  vs  126,802,546   +20 MB (+16%)

    This is not a rebase artifact — it is what the branch's design does — and
    it does not break the "hangar has no first-party deps" rule, which is about
    the other direction (`go list -deps ./hangar/ | grep concourse` still
    prints only `hangar` itself). But `--hangar-enabled` defaults off, so every
    deployment that never touches Hangar now carries the GCS/gRPC/OTel stack
    and its package-init cost in the web image. If that matters, the split is
    to keep the store behind an interface in `hangar` and put the GCS
    implementation in a leaf package only the daemon imports; `atccmd` needs
    only `GrantSigner` and the option types. Startup cost measured at +2 ms, so
    this is an image-size and supply-chain-surface question, not a latency one.

---

# Third rebase: 2026-09-07 — onto origin/core 74aaa83d7e (brine migration merged)

30 commits replayed onto `origin/core` 74aaa83d7e ("ci(brine): trigger the brine
job and gate tag-rc on it, now that CI has run it"). Previous base was
c1c3e70e7c, which is still the merge base; core added 95 commits on top of it,
almost all of them one thing: the brine test-migration branch. New head
52f85901f0, still 30 commits.

What core's 95 commits actually did, in this branch's terms: the jetbridge
campaign deleted or renamed twenty whole Go/Ginkgo test files (401 tests) under
`atc/worker/jetbridge`, a companion gc/lidar campaign did the same to `atc/gc`
and `atc/lidar`, and both moved that coverage into a nested, separate Go module at
`atc/worker/jetbridge/brine/` (35 feature files, ~568 scenarios, driven by the
Rust brine CLI), restored 84 tests the migration's own re-measurement could not
justify deleting, and added a `brine` CI job that `tag-rc` now waits on. Outside
the test tiers core touched exactly two production files: `cmd/artifact-daemon/main.go`
(a `--kubeconfig` and a `--listen-address` flag) and `atc/worker/jetbridge/process.go`.

Nothing in the 95 commits touched `hangar/`, `cmd/artifact-daemon/containment.go`,
`deploy/chart/`, `atc/runtime/types.go` or `atc/atccmd/command.go`.

## Textual conflicts (3)

### A. `cmd/artifact-daemon/main.go` — the node-labeling K8s client
(commit `feat(artifact-daemon): serve strict Hangar trees`)

Core's `f94de79f50` ("Let the daemon be pointed at a cluster it is not running
inside") gave `buildK8sClient` a `kubeconfig` parameter and passed `*kubeconfig`
at both call sites. The branch had hoisted `k8sClient` out of the `if *nodeName != ""`
block into a `var k8sClient kubernetes.Interface` above it, so it could hand the
same client to the Hangar labeler, which turns the assignment from `:=` into `=`
and needs its own `var err error`.

Both changes are needed and neither subsumes the other. Resolved to
`k8sClient, err = buildK8sClient(*kubeconfig)` under `var err error` — core's
parameter, the branch's non-shadowing assignment. Taking either side alone is
wrong: `:=` would shadow the hoisted variable and leave the Hangar labeler
holding a nil client; dropping the argument would undo core's commit.

### B. `cmd/artifact-daemon/main.go` — the import block
(commit `fix(artifact-daemon): close Hangar service boundaries`)

That branch commit moved `github.com/concourse/concourse/artifactcap` out of the
stdlib group into its own group; core left it inline among the stdlib imports
and added `net`, `strconv` and `k8s.io/client-go/tools/clientcmd` around it.
Kept the branch's grouping and core's three new imports. `gofmt -l` clean.

### C. `atc/worker/jetbridge/container_restored_test.go` — two deleted blocks
(commit `feat(jetbridge): mount exact Hangar tree inputs`)

Core renamed `container_test.go` to `container_restored_test.go` (71% similarity)
and deleted a number of its tests. Two of the deleted blocks are ones the branch
had edited a single line of:

  - `Context("deferred pod name is set when Run creates the pod")`, and
  - `Context("when a sidecar has no workingDir…")` plus
    `Context("when a sidecar specifies its own workingDir")`.

The branch's edit in each was purely the mechanical repair described below —
adding an `Artifact:` to an input literal — not new coverage. Resolved to core's
deletion: 135 lines dropped, nothing of the branch's intent lost. The branch's
seven OTHER `Artifact:` repairs in that file replayed onto the renamed file
without conflict and are present at the new head.

## Modify/delete conflicts (4), all resolved to core's deletion

`artifact_integration_test.go`, `integration_test.go`, `podname_integration_test.go`
and `resource_test.go` were deleted by the migration and modified by the branch.
In all four the branch's entire diff was the same mechanical repair. I read each
diff before accepting the deletion; none of the four contained a Hangar
assertion or anything else the branch needs. `git rm` on all four.

## The silent semantic conflict this rebase existed to catch

`git` produced no marker for it, `go build ./...` and `go vet ./...` were both
clean, and it only appeared when the suite ran.

The branch's `feat(jetbridge): mount exact Hangar tree inputs` adds
`(*Container).validateInputs`, called at the top of `buildPod`:

    if hasArtifact == hasHangarTree {
        return fmt.Errorf("input %q must set exactly one of Artifact or HangarTree", ...)
    }

An input with neither is now a hard, fail-closed error. That is a change to the
runtime contract, and the branch paid for it by repairing roughly fifteen
`runtime.Input{DestinationPath: …}` literals across six Go test files so each
carries a `&fakeArtifact{}`.

Core's migration restored three of those tests from the pre-branch merge base
into a NEW file, `atc/worker/jetbridge/integration_restored_test.go` — carrying
the pre-repair, artifact-less literals. Git had no reason to conflict: the
branch never touched that filename. The result compiled and vetted clean and
failed at run time:

    [FAIL] Integration input/output passing between steps
           [It] passes inputs from a get step to a put step via volume mounts
      create pause pod: input "/tmp/build/put/compiled-binary" must set
      exactly one of Artifact or HangarTree
    [FAIL] Integration task with sidecar containers
           [It] creates a pod with sidecars that share volume mounts …
      create pause pod: input "/tmp/build/workdir/my-app" must set
      exactly one of Artifact or HangarTree

Ported rather than deleted: the three literals at
`integration_restored_test.go:215,216,272` got the same
`Artifact: &fakeArtifact{handle: …}` the branch gave their originals, with the
same handles. The repair was squashed into
`feat(jetbridge): mount exact Hangar tree inputs` (now `5689db11e6`) rather than
added as a tip commit, so the branch stays bisectable — the commit that
introduces the invariant is still the commit that satisfies it.

`go test ./atc/worker/jetbridge/` then passed, 89/89.

## The second silent semantic conflict: the nested brine module stopped building

Also invisible to `go build ./...` and `go vet ./...`, because both stop at the
root module boundary. It only appeared in `make test-unit`, whose
`ginkgo -r` walks into the nested module and tries to compile it:

    Failed to compile steps:
    # github.com/concourse/concourse/atc/worker/jetbridge/brine/steps
    ../../../../../hangar/gcs.go:18:2: missing go.sum entry for module providing
      package cloud.google.com/go/storage (imported by
      github.com/concourse/concourse/hangar)
    FAIL github.com/concourse/concourse/atc/worker/jetbridge/brine/steps [setup failed]

The mechanism, and it is reviewer item 11 arriving somewhere new. `brine/go.mod`
carries `replace github.com/concourse/concourse => ../../../..`, so the brine
steps compile against THIS tree's root module. `steps/` imports `atc/runtime`;
on this branch `atc/runtime/types.go` imports `hangar`; `hangar/gcs.go` imports
`cloud.google.com/go/storage`. Core minted `brine/go.mod` and `go.sum` against a
root module where that chain did not exist, so the nested module has no
requirement or checksum for any of it.

Not a pre-existing condition — measured on both sides:

    $ cd <origin/core worktree>/atc/worker/jetbridge/brine && go build ./steps/
    (clean, exit 0)

So core's `make test-unit` compiles this package and this branch's did not. A
genuine regression the rebase created, and the correct fix is to bring the
nested module's requirements up to what the root module now needs — not to
exclude brine from the tier. Regenerated with `GOFLAGS=-mod=mod go build ./...`
inside `brine/`, which added 17 indirect requirements to `go.mod` and 15 lines to
`go.sum`, all of them the GCS/gRPC/OTel stack: `cloud.google.com/go/storage`
(v1.64.0, the same version the root module pins), `cloud.google.com/go`,
`cloud.google.com/go/iam`, `cloud.google.com/go/monitoring`, `cel.dev/expr`,
`github.com/cncf/xds/go`, `github.com/envoyproxy/*`,
`github.com/spiffe/go-spiffe/v2`, two GoogleCloudPlatform OTel exporters,
`go.opentelemetry.io/contrib/detectors/gcp` and `google.golang.org/genproto`.

Verified afterwards under the default `-mod=readonly`:

    $ cd atc/worker/jetbridge/brine
    $ go build ./...   # clean
    $ go vet ./...     # clean
    $ go test ./...    # ok  .../brine/steps  11.244s

Squashed into `feat(jetbridge): mount exact Hangar tree inputs` — the same commit
that adds the `hangar` import to `atc/runtime/types.go` — so no commit on the
branch is left unbuildable.

**This belongs on item 11's ledger.** The GCS client's cost is not only +168
packages and +20 MB in the web binary; it is now also 17 indirect requirements
in a test module that has nothing to do with Hangar and never asked for a cloud
SDK. If the store is ever put behind an interface with the GCS implementation in
a leaf package only the daemon imports, this reverts too.

## Reviewer items: status at this head

**Item 1 — two containment implementations, one guard — STANDS, guard re-verified.**
Core did not touch `cmd/artifact-daemon/containment.go` (`git log c1c3e70e7c..origin/core --
cmd/artifact-daemon/containment.go` is empty), and did not move the daemon's
tests. `hangar/architecture_symlink_test.go` still resolves its target through
the relative path `"../cmd/artifact-daemon/containment.go"`, still parses it, and
`TestCoreSymlinkValidatorHasNotDrifted` passes, printing the same rule
fingerprint it was minted against:

    core rule fingerprint: if op:== id:linkname lit:"" return call id:entryName
    if call id:filepath id:IsAbs id:linkname return … return id:nil

The parity table and the whole `hangar` package are green. The underlying
observation is unchanged: core's structural guards still scan only
`os.ReadDir(".")` under `cmd/artifact-daemon`, so `hangar/` remains invisible to
them.

**Item 4 — NetworkPolicy ingress selector — STANDS, unexamined by core.**
`git log c1c3e70e7c..origin/core -- deploy/chart/` is empty; core's 95 commits do
not touch the chart at all. `artifact-daemon-networkpolicy.yaml` still admits
task pods by `matchExpressions: concourse.ci/worker Exists`. Still an
owner-facing decision, still unreviewed, and now one release older.

**Item 5 / commit `Restore the live suite's resolve-capability env fallback` — HOLDS.**
`adoptResolveCapability` is defined at `live_test.go:593` and called at
`live_test.go:255`. The cheap check from the second rebase still returns empty:

    $ git diff origin/core..HEAD -- atc/worker/jetbridge/live_test.go
    $ git diff origin/core..HEAD -- atc/worker/jetbridge/live_worker_test.go
    (both empty)

The delete-then-restore pair still cancels exactly, and brine's migration did not
touch either live file.

**Item 7 — `-tags live` / `-tags hangar_live` — STILL CLOSED.**

    $ go vet -tags live         ./atc/worker/jetbridge/   # clean
    $ go vet -tags hangar_live  ./atc/worker/jetbridge/   # clean

**Item 8 — early-exit cleanup — STANDS, with one thing I had not noticed.**
Core added no new early exits after labeling, so my two additions from the first
rebase are untouched. But auditing the file again turned up two exits that were
never covered and are not mine: `main.go:145` and `main.go:149`, the
resolve-capability key load and `SetResolveCapabilityKey` failures, both after
labeling, both bare `os.Exit(1)` with no `cleanupDaemonServices`. They are
core's code, they were already there at the previous head `1913e75035`, and they
are not a rebase regression — but they are two more holes in exactly the
invariant the branch's `cleanupDaemonServices` exists to close: a daemon that
labels the node ready and then dies configuring its capability key leaves
`concourse.dev/artifact-cache=ready` and `concourse.dev/hangar-v1` set on a node
that serves nothing. Fixing them is a one-line change each; I have not made it,
because it is core's code and outside this rebase.

**Item 9 — `Config.ArtifactDaemonNamespace` — UNCHANGED, still unwired.**
The field is still core's (`atc/worker/jetbridge/config.go:189-193`), still read
only by `daemonTLSServerName` (`daemon_tls.go:41`), and its only writers are
still `daemon_tls_test.go:143` and `live_test.go:289`. No `atccmd` flag and no
chart value assigns it:

    $ git grep -n 'artifact-daemon-namespace\|artifactDaemonNamespace'
    (no matches)

**Item 11 — the GCS client in the web binary — RE-MEASURED, essentially identical.**
Core did not touch `atc/runtime/types.go` or `atc/atccmd/command.go`. Measured
again against a fresh detached `origin/core` worktree, both trees
`git status --porcelain` clean:

                                    branch          core (74aaa83d7e)
    go list -deps ./cmd/concourse    1515            1347      (+168)
      of which cloud.google.com/go/storage  5           0
    binary                      146,976,274     126,836,146   (+20,140,128, +15.9%)

Unchanged from the second rebase's numbers to within a few hundred bytes. Still
an image-size and supply-chain-surface question for the owner, not a latency one.

Items 2, 3, 6 and 10 stand unchanged; nothing in core's 95 commits went near them.

## New reviewer item

12. **The branch's `validateInputs` refutes 15 brine scenarios, and brine now
    gates `tag-rc`.**

    This is the other half of the silent conflict above, and it is NOT
    mechanical — I did not fix it, deliberately.

    Brine's step vocabulary draws a distinction the Go tests never had. Two
    different phrases build an input:

      - `it takes an input at "X" produced by an earlier step` →
        `ContainerDraft.ArtifactInputs`, which `steps/container_spec.go:69`
        turns into `runtime.Input{Artifact: vol, …}`;
      - `it takes an input at "X"` → `ContainerDraft.Inputs`, which
        `steps/container_spec.go:72` and `steps/container_extra.go:953` turn
        into `runtime.Input{DestinationPath: path}` — **no artifact, on
        purpose**.

    Two more sites do the same: `steps/integration.go:495`
    (`the step takes an input at "X"`) and `steps/container_gaps.go:218`
    (`the container runs with an input and the output "N" both at "X"`). Those
    four sites are the only nil-artifact input constructors in the whole brine
    module; `volume_streaming.go` and `artifact_recording.go` always set one.

    Under `validateInputs`, every scenario reaching one of those four fails at
    `buildPod`. Measured, not predicted — the adapter builds and the brine CLI
    runs in this tree (`~/brine-private/target/debug/brine`, contract 3), and
    `brine run features/container-pod.feature` fails exactly as expected:

        A step sees its working directory and every input
          run container "input-vol-handle": create pod: input
          "/tmp/build/workdir/input-a" must set exactly one of Artifact or HangarTree
        An output that shares an input's path gets one volume, not two   (same)
        An output on its own path gets its own volume                    (same)

    The full affected set, by static enumeration of the four step phrases
    against all 35 feature files — 15 scenarios in 4 files:

        container-pod.feature:19    A step sees its working directory and every input
        container-pod.feature:37    An output that shares an input's path gets one volume, not two
        container-pod.feature:49    An output on its own path gets its own volume
        container-pod.feature:154   A sidecar runs alongside the step and shares its working set
        container-pod.feature:462   An input sharing an output's path is filed under the output's name
        container-pod.feature:597   An input with nothing to fetch is skipped, not fatal to the rest
        container-run.feature:114   Every path the step declared comes back as its own volume
        container-run.feature:135   A volume handed back before the step runs knows no pod
        container-run.feature:144   Running the step is what binds its volumes to a pod
        step-integration.feature:101 A step is handed a mount for its directory, its input, its output and its cache
        step-integration.feature:214 A step's input volume starts unbound and ends up reading from its pod
        step-integration.feature:255 A task's output reaches the put step that publishes it
        step-integration.feature:349 A put step's inputs are mounted where its resource expects them
        volume-streaming.feature:244 An output that lands on an input's path reuses that input's volume
        volume-streaming.feature:260 A trailing slash on the output path does not defeat the dedup

    This matters more than a normal test break because core's tip commit is
    `ci(brine): trigger the brine job and gate tag-rc on it`: in
    `deploy/concourse-pipeline.yml`, `tag-rc` now carries
    `passed: [k8s-runtime-tests, brine]`. A red brine job blocks a release cut
    from this branch.

    **Fourteen of the fifteen are the same mechanical repair the branch already
    made on the Go side** — those scenarios want *an input*, not specifically an
    artifact-less one. The one-line fix is to make the plain phrase create a
    volume the way the "produced by an earlier step" phrase does. I did not make
    it, for one reason: it silently redefines what an existing brine phrase
    MEANS, collapsing two deliberately-separate vocabulary terms, and it would
    make `container-pod.feature:596-597` — which uses BOTH phrases in one
    scenario precisely to contrast them — assert nothing.

    **The fifteenth is a genuine contract conflict and no edit resolves it.**
    `An input with nothing to fetch is skipped, not fatal to the rest` asserts
    that an artifact-less input among artifact-bearing siblings is skipped and
    the pod is still built:

        Then the pod fetches exactly 1 of the step's inputs
        And the pod does not fetch the input at "/tmp/build/workdir/no-artifact"

    The branch says the opposite: that input is fatal at `buildPod`, before any
    fetch batch is assembled. Both positions are defensible and they cannot both
    hold. Two facts for whoever decides:

      - The guards the scenario proves — `storage_daemonset.go:201` and `:594`,
        both `if input.Artifact == nil { continue }` — are still in the tree, but
        `validateInputs` makes them unreachable from `FindOrCreateContainer` →
        `Run` → `buildPod`. They are now dead code on that path.
      - Production cannot construct the state either way. Both real producers of
        `runtime.Input` — `atc/exec/put_inputs.go:126` and
        `atc/exec/task_step.go:497` — `continue` when the repository has no
        artifact for a name, so neither can emit an input with a nil `Artifact`.
        The scenario, and the guards it proves, describe a defensive path no
        pipeline reaches.

    So the honest reading is that the branch is probably right and the scenario
    is probably obsolete — but deleting another campaign's deliberately-written
    scenario, in a rebase, on my own judgement, is not a call I should make.

### Item 12 — closed 2026-09-07

The owner ruled on both halves, and `4b99af4c71` carries the result.

1. **The fifteenth scenario is removed.** `An input with nothing to fetch is
   skipped, not fatal to the rest` describes a state production cannot reach —
   re-verified here: `atc/exec/put_inputs.go:115` and `atc/exec/task_step.go:490`
   both `continue` when the artifact repository has no artifact for a name, so
   no pipeline emits an input with a nil `Artifact`. A DISPOSITION comment in
   `container-pod.feature` replaces it, and the three sentences only it used go
   with it.

2. **The rest are reconciled by collapsing the vocabulary**, so brine can only
   express an input production can produce. `ContainerDraft.Inputs` and
   `ContainerDraft.ArtifactInputs` become one field; every input path gets a
   real artifact volume. The qualified phrase `... produced by an earlier step`
   is deleted — in JetBridge every input IS produced by an earlier step, so once
   the artifact-less form ceased to exist the qualifier carried no information.

The measured set was TEN failing scenarios, not the fifteen this item predicted
by static enumeration: the five in `container-run.feature`, `step-integration
.feature:101` and `volume-streaming.feature` that only create the container and
never run it never reach `buildPod`, so `validateInputs` never sees them.

**No assertion changed.** The pod-shape sentences that could have moved — `the
pod has 3 volumes`, `every volume is ephemeral`, `the pod runs 2 containers`,
`handed 5 volumes in all`, and both dedup scenarios — all hold, because a
store-less worker emits no fetch init container whatever the input carries and
the dedup is keyed on the destination path. Full suite at `4b99af4c71`:
**567/567, verdict passed** (35 features, 200s, `brine run --mode sync`).

Both `input.Artifact == nil` guards in `storage_daemonset.go` are KEPT, each now
carrying a comment saying which caller needs it. The one in
`BuildFetchInitContainers` is unreachable from `buildPod` but is still called
directly by `TestDaemonSetBackend_BuildFetchInitContainers_SkipsNilArtifact`.
The one in `preferredInputNode` is not dead code at all: a Hangar tree input
passes `validateInputs` with a nil `Artifact`, so deleting that guard would nil
-dereference `input.Artifact.Handle()` for every Hangar input — the item's claim
that both were unreachable was wrong about this one.


## Test results at head 52f85901f0 (31 with the notes commit) (this worktree unless said otherwise)

    $ go build ./...                                        # clean
    $ go vet ./...                                          # clean
    $ go vet -tags live      ./atc/worker/jetbridge/        # clean
    $ go vet -tags hangar_live ./atc/worker/jetbridge/      # clean
    $ gofmt -l <every .go in origin/core..HEAD>             # clean

    ok  github.com/concourse/concourse/cmd/artifact-daemon          2m58.0s
    ok  github.com/concourse/concourse/cmd/artifact-daemon/durable     8.0s
    ok  github.com/concourse/concourse/hangar                          6.2s
    ok  github.com/concourse/concourse/deploy/chart/tests            1m20.7s
    ok  github.com/concourse/concourse/atc/atccmd                     13.1s
    ok  github.com/concourse/concourse/atc/worker/jetbridge          188.4s  (89/89)

Invariants spot-checked at this head, as in the second rebase:

  - `hangar/architecture_symlink_test.go` present and green; fingerprint
    unchanged.
  - `hangar/tree.go:878` is still the single `root.Symlink` site, behind
    `cleanSymlinkTarget`.
  - `cmd/artifact-daemon/hangar_handlers.go` has exactly one textual `http.Error`
    and it is inside the comment explaining why there are none.
  - `hangarSem` still bounds the materialization route at
    `maxConcurrentHangarMaterializations = 4` (`server.go:185`, acquired at
    `hangar_handlers.go:228`).

### `make test-unit`: two real failures found and fixed, one flake left standing

The tier was run three times in this session — twice on the branch and once on a
detached `origin/core` worktree in a scratch directory — because the first branch
run surfaced two failures and both had to be told apart.

**First branch run: 89 suites, two failures, both genuine, both now fixed.**

    There were failures detected in the following suites:
         gc ./atc/gc
      steps ./atc/worker/jetbridge/brine/steps [Compilation failure]

  - `brine/steps` is the missing-go.sum regression described above. Fixed by
    updating the nested module; measured against core, which compiles it.
  - `atc/gc` was NOT the branch. Two `[BeforeSuite]` failures at
    `postgresrunner.go:241` with `84 Passed | 0 Failed` — the documented
    host-not-branch signature — and the log names the cause outright:

        could not bind IPv4 address "127.0.0.1": Address already in use
        HINT:  Is another postmaster already running on port 6538?
        ERROR:  database "testdb_template" already exists
        [FAILED] drop testdb_template

    Another agent's worktree (`.claude/worktrees/gate-run-creation`) was running
    its own Postgres tier on the same port at the same time; `ps` confirmed its
    test binaries alive during the run. Not a spec failure, not this branch. The
    second run, started only after `ps aux | grep '[.]test'` was 0 and
    `lsof -iTCP:6538` was empty, has `atc/gc` green.

**Second branch run, on a quiet machine: 89 suites, one failure.**

    Ginkgo ran 89 suites in 12m36.899s
    There were failures detected in the following suites:
      concourse ./cmd/concourse

    Summarizing 1 Failure:
      [FAIL] Web Command when CONCOURSE_CONCURRENT_REQUEST_LIMIT is invalid
             [It] prints an error and exits
      cmd/concourse/concourse_test.go:84

Same spec, same line, same message as the second rebase. I did not assume that;
I re-measured, because "it is the known flake" is exactly the claim that gets
lazy on a third rebase.

**1. Core's own `make test-unit`, same machine, fresh detached worktree at
74aaa83d7e, `git status --porcelain` clean — PASSED.**

    Ginkgo ran 88 suites in 11m59.031s
    Test Suite Passed

(88 vs 89 is the failing suite being counted twice in the branch run.)

Not both-red, so:

**2. Interleaved isolated runs, alternating trees, both trees clean.**

    quiet machine (load ~1):   branch 6 runs, 0 failed | core 6 runs, 0 failed
    under CPU load (load 24):  branch 4 runs, 0 failed | core 4 runs, 0 failed

Ten runs each and the failure would not reproduce outside a full 89-suite
`ginkgo -r -p` run. That is itself evidence about the mechanism: what breaks it
is the contention profile of nine procs and dozens of postmasters, not the
package.

**3. Direct measurement of the quantity the assertion measures.** The spec is

    Eventually(concourseRunner.Err()).Should(gbytes.Say("'InvalidAction' is not a valid action"))

with no interval, so it inherits Gomega's 1-second default. Both binaries were
built and spawned with the same bad env var, timing spawn → message, ten runs
each (first run discarded as cold start):

    branch ms:  62 65 60 63 65 70 62 60 64   (median 63)
    core   ms:  62 58 59 64 63 59 60 58 63   (median 60)

Both ~16x inside the one-second budget. The branch costs about 3 ms, which is
the `hangar` → `cloud.google.com/go/storage` package-init cost of item 11 and is
consistent with the +2 ms the second rebase measured. Nowhere near enough to
explain a one-second timeout.

**4. The branch does not touch the package.**

    $ git diff --stat origin/core..HEAD -- cmd/concourse atc/postgresrunner
    (empty)

Conclusion, unchanged from the second rebase and now re-derived rather than
inherited: pre-existing, load-induced, and core's to fix. The sibling assertion
ten lines up already carries `"30s", "2s"`; line 84 should carry a budget too.
Not a rebase regression and not a Hangar regression. Every other suite passed,
including every package this branch touches.

### Housekeeping

**`REBASE-NOTES.md` is now tracked.** For two rebases it was deliberately kept
out of the history — the first rebase went to some trouble to remove it after a
`git add -A` swept it in. This time it is committed on purpose, as the tip
commit, so the next rebase inherits it with the branch instead of depending on
one worktree surviving. The `info/exclude` observation from the second rebase
therefore only matters now for the one remaining untracked file,
`cmd/artifact-daemon/hangar_grant_review_test.go`, which is still visible to
`git add -A` and still must not be committed. Every commit in this rebase was
made by explicit pathspec or `--amend`.

A near-miss worth recording. Vetting the nested brine module needed
`GOFLAGS=-mod=mod`, which rewrote `atc/worker/jetbridge/brine/go.mod` and
`go.sum` (+28 lines). I reverted that with `git checkout --` as an unwanted
mutation of my own making — and it was, at that moment, the correct instinct and
the wrong outcome: those 28 lines were the real fix for a real regression, which
`make test-unit` then found the hard way an hour later. The lesson is not "don't
revert"; it is that a tool writing to go.mod inside a nested module with a
`replace` back to the tree you are rebasing is telling you something about the
tree, and it is worth reading the diff before discarding it.

Also removed: the `.build/brine-adapter-jetbridge` binary built to run the brine
CLI (gitignored anyway, `.gitignore:61`). At the final head
`git status --porcelain` shows exactly one `??` line, the review-test file.
