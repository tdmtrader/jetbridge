# Per-test disposition: the jetbridge campaign

The gc/lidar campaign left a row per deleted It. The jetbridge campaign did
not: it deleted twenty whole test files, 401 tests, and the record of why
lived in twenty commit messages and one summary. An auditor asking "what
evidence killed THIS test?" had nowhere to look. This file is that record,
written after the fact from the deletion commits themselves, so it says
plainly where the evidence is thin rather than dressing it up.

## Verdict key

- **DELETED** — the branch removed the test and the recorded evidence for it
  still stands.
- **REFUTED** — re-measurement found an honest mutation that reddens the Go
  test while brine stays green. The pairing is broken; the test is BACK.
- **GAP** — the Go test reddens and brine does not. The test is BACK and
  brine owes a scenario.
- **INERT** — no mutation reddened it at all. The test is BACK, recorded as a
  defect in the test itself rather than as coverage.
- **UNMEASURABLE** — the pairing could not be measured either way.
- **KEPT** — the test was never part of the campaign: core added it after the
  rebase base, so no evidence was ever offered against it. It is carried, not
  deleted. See the last section of this file.

## Evidence granularity, and why the word matters

Each row says what granularity of evidence the branch actually had:

- **per-test** — a mutation was named for THIS test, and the brine scenario it
  reddens alongside was named too. Two files were recorded this way:
  `artifact_locator_test.go` and `storage_daemonset_durable_test.go`.
- **per-file** — the deletion commit named mutations, but for the FILE. A
  mutation reddened the file and it reddened a scenario. **That is not
  per-test evidence.** A file-level both-red says the file as a whole and the
  suite as a whole move together under one mutation; it says nothing about
  which of the file's 73 tests carried the behaviour, and nothing at all
  about the other 72. Seventeen of the twenty files are in this state.
- **none** — the deletion commit recorded no mutation for the file either.
  One file: `resource_test.go`.

## Rebase impact and re-verification (2026-09-05)

The branch was rebased onto `core` for 0.3.2 (74 commits replayed). Every one
of these 401 rows was classified against four criteria: (a) the recorded
mutation target changed on core, (b) the named brine scenario or its step file
changed, (c) a symbol either artefact reads changed, (d) a core commit touched
the same behaviour. 138 of the 401 came out impacted and were re-measured one
at a time, each in its own detached worktree. 84 did not survive that
re-measurement and their tests are restored; the restoration files are named
after the file each test came from and carry the row ids in a header comment.

A completeness critic re-read every classification afterwards and flipped 30
rows from not-impacted to impacted -- 25 jetbridge and 5 gc/lidar -- where the
classifier had argued a change was inert instead of applying criterion (b) as
written. Those rows carry `[CRITIC REVISION -> impacted by rule (b)]` at the
front of their reason, with the original reason kept after `|| original reason:`
so both readings are on the record. All 25 jetbridge flips were then measured
like any other impacted row.

A row marked NOT IMPACTED is a claim about the REBASE, not a re-endorsement of
its original evidence. Where that evidence was per-file, it is still per-file,
and this file says so on every such row.

Across both disposition files the 149 re-measured rows end HOLDS 64, REFUTED
61, GAP 21, INERT 3 — of which 138 are the jetbridge rows below (HOLDS 54,
REFUTED 60, GAP 21, INERT 3) and 11 are in `DISPOSITION-gc-lidar.md`.

**Correction, 2026-09-05.** Two rows were published here as REFUTED and are
GAP: `JB-behavioral_permutations-017` and `JB-volume_daemonset-012`. In both,
the skeptic's mutation did break the verifier's pairing — so the mechanical
"refuted" flag on the record is true — but the skeptic's own final verdict was
GAP, because the behaviour the mutation exposed is covered by no brine scenario
at all. The tags were derived from the flag instead of the verdict. Both
verdicts restore the test, so nothing about the branch's contents changes; what
changes is the debt, because a GAP says brine still owes a scenario and a
REFUTED does not. Both are now in the owed inventory in `MIGRATION-EVIDENCE.md`.

129 of the 149 rows were handed to a skeptic. The 20 that were not are the 17
the verifier already called GAP and the 3 it already called INERT: those keep
their test whatever a skeptic finds, so the adversarial pass could only have
been redundant. Every row whose verifier verdict would have left a test deleted
got one. Rows in that group read `skeptic: not reached`.


## artifact_locator_test.go

Deleted by `ee655171ee` — "Delete artifact_locator_test.go, and make its one real gap assertable". Recorded evidence granularity: **per-test**.

**[DELETED]** TestArtifactLocator_RecordAndLocate  `JB-artifact_locator-000`
  - recorded evidence: PER-TEST — Per-test claim only, no mutation named for this test. Deletion commit: "Five of its six tests are evidenced per test: a mutation reddens each one and the brine scenario it reddens alongside is about the same behaviour." The only Locate-side mutation named anywhere on the branch is MIGRATION-EVIDENCE.md Correction 1, c…
  - rebase impact: NOT IMPACTED — The test pins ArtifactLocator.Record/Locate and ArtifactLocation.NodeName, all defined in atc/worker/jetbridge/artifact_locator.go, which is byte-identical between aef2244a63 and 5133d0ddbc (empty `git diff --stat`) and absent from core_changed_symbols.json — so (a)/(d) fail. Its brine evidence is step-closing.feature…

**[DELETED]** TestArtifactLocator_LocateNode  `JB-artifact_locator-001`
  - recorded evidence: PER-TEST — Named, and it was the file's one MEASURED brine hole. Commit 86bd3cf682 ("Migrate 23 behaviours the Go tests could only state as field reads"): "the first measurement already found a hole: making ArtifactLocator.LocateNode report found for every key reddens artifact_locator_test.go and leaves brine green." Closed by b…
  - rebase impact: NOT IMPACTED — LocateNode is unchanged on core: `git log -SLocateNode aef2244a63..5133d0ddbc -- atc/` returns nothing, and artifact_locator.go is byte-identical merge-base→core. The one commit matching -SArtifactLocator (0d336e062b, task cache identity) only added `locator := NewArtifactLocator()` inside daemonset_integration_test.g…

**[DELETED]** TestArtifactLocator_LocateWithHostDir  `JB-artifact_locator-002`
  - recorded evidence: PER-TEST — file-level only (covered by the blanket "Five of its six tests are evidenced per test"); no mutation named for this test in the commit body or MIGRATION-EVIDENCE.md.
  - rebase impact: NOT IMPACTED — Pins that Record's hostDir survives into ArtifactLocation.HostDir; ArtifactLocation and Record are unchanged on core (artifact_locator.go byte-identical, and -SArtifactLocation over atc/ in aef2244a63..5133d0ddbc returns no commits). The brine counterpart "An artifact remembers the directory it was stored in" and its…

**[DELETED]** TestArtifactLocator_LocateMissing  `JB-artifact_locator-003`
  - recorded evidence: PER-TEST — Per-test claim only. The mutation that targets exactly this behaviour is named in MIGRATION-EVIDENCE.md but only as the worked example of why file-level pairing is invalid: "mutating `ArtifactLocator.Locate` to report found for unknown keys reddened `artifact_locator_test.go`, and the brine failure it paired with was…
  - rebase impact: NOT IMPACTED — Pins Locate returning found=false on an empty index. Locate is unmodified on core (artifact_locator.go byte-identical merge-base→core; file absent from core_changed_symbols.json). Its brine counterpart "An artifact nobody recorded is not held anywhere" is in the untouched step-closing.feature, backed by untouched clos…

**[DELETED]** TestArtifactLocator_Remove  `JB-artifact_locator-004`
  - recorded evidence: PER-TEST — Named and verified in the deletion commit: "Verified: `delete(l.locations, key)` becoming a no-op reddens the old suite AND three brine scenarios, including the one now carrying the concurrent removal claim."
  - rebase impact: NOT IMPACTED — The recorded mutation for this row is `delete(l.locations, key)` becoming a no-op in ArtifactLocator.Remove — that function is byte-identical on core (artifact_locator.go unchanged, lines 52-56 at 5133d0ddbc), so the mutation still applies verbatim and (a)/(d) fail. The three brine scenarios it reddens (including "A c…

**[DELETED]** TestArtifactLocator_ConcurrentAccess  `JB-artifact_locator-005`
  - recorded evidence: PER-TEST — Named explicitly, and rejected as evidence. Deletion commit: "The sixth, TestArtifactLocator_ConcurrentAccess, asserts NOTHING. It spawns 300 goroutines over 26 colliding keys and waits. Without -race — which CLAUDE.md forbids in this repository's unit tier — it can only fail by panicking or deadlocking, so as a cover…
  - rebase impact: NOT IMPACTED — The whole disposition here is that the Go test asserted nothing (300 goroutines, 26 colliding keys, only wg.Wait()) — a claim about the deleted test's own body, which the rebase cannot change; I re-read the merge-base body at aef2244a63 and confirmed it contains no t.Error/t.Fatal. The replacement claim lives in "Reco…


## config_test.go

Deleted by `7bd092f004` — "delete config_test.go — brine proven to carry every behavior it guarded". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** Config NewConfig returns a config with the given namespace  `JB-config-000`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — NewConfig is byte-identical between aef2244a63 and 5133d0ddbc — core's only two config.go commits (1e023e7ca4, 06d3c556b1) are pure additions: the resolve-capability consts/fields, ArtifactDaemonNamespace, and the new MinimumArtifactResolveCapabilityTTL func. brine steps/config.go and features/worker-config.feature ar…

**[DELETED]** Config NewConfig defaults namespace to 'default' when empty  `JB-config-001`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The recorded mutation target — the `if namespace == "" { namespace = "default" }` branch inside NewConfig — is byte-identical on core (function extracted at both revisions and compared), and `git log -S NewConfig aef2244a63..5133d0ddbc -- atc/` returns zero commits. The CF-01 outline row `| left empty || default |` is…

**[DELETED]** Config NewConfig stores the kubeconfig path when provided  `JB-config-002`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — NewConfig and the Config.KubeconfigPath field are unchanged on core (`git log -S KubeconfigPath aef2244a63..5133d0ddbc -- atc/` is empty; the only Config struct edits are three ADDED fields). Nothing on core touches the pass-through this test pins; its weak evidence (no recorded mutation) is a pre-existing condition,…

**[DELETED]** Config NewConfig defaults PodStartupTimeout to 5 minutes  `JB-config-003`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The hint's `podStartupTimeout` is 1164f9db3d's NEW unexported helper in process.go, reached only by (*execProcess).waitForRunning — this test never builds an execProcess; it asserts the field NewConfig writes, and both NewConfig and `DefaultPodStartupTimeout = 5 * time.Minute` in config.go are byte-identical on core.…

**[DELETED]** Config CacheBasePath constant equals /concourse/cache  `JB-config-004`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — `CacheBasePath = "/concourse/cache"` is identical at merge-base (config.go:37) and core (config.go:47 — it only moved down by the ten added resolve-capability lines), and `git log -S CacheBasePath aef2244a63..5133d0ddbc -- atc/` returns nothing. The CF-07 scenario 'Caches live under a fixed path' is unchanged by the r…

**[DELETED]** Config MergeResourceTypeImages returns defaults when no overrides are provided  `JB-config-005`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — MergeResourceTypeImages and the eight-entry DefaultResourceTypeImages map are both byte-identical on core (no core commit matches -S on either symbol), so no built-in type was added or removed since the merge-base. The WR-05 completeness scenario that supersedes this test is not in step_changes and passed post-rebase.

**[DELETED]** Config MergeResourceTypeImages overrides a default type image  `JB-config-006`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The mutation targets recorded for this row ('overrides ignored', 'split on ":" instead of "="') both live in MergeResourceTypeImages, whose body — `strings.Cut(entry, "=")` plus the `!ok || name == "" || image == ""` skip — is byte-identical on core. The WR-06 'replace a default' outline row and its untouched-defaults…

**[DELETED]** Config MergeResourceTypeImages adds a new base type not in defaults  `JB-config-007`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — MergeResourceTypeImages is byte-identical on core, so the add-a-new-key path this test pins is unchanged. The WR-06 'add a new type' outline row and the 'no resource type was invented that nobody configured' inverse guard live in worker-config.feature, which the rebase did not touch (it is one of the 26 untouched feat…

**[DELETED]** Config MergeResourceTypeImages merges multiple overrides correctly  `JB-config-008`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Both the merge loop and the defaults map (including the `time -> concourse/time-resource` entry this test leans on) are byte-identical on core. The 'Several overrides merge together and leave the rest alone' scenario is unchanged by the rebase and green in the 16/16 post-rebase feature run.

**[DELETED]** Config MergeResourceTypeImages last-wins for duplicate overrides  `JB-config-009`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The override loop's in-order `merged[name] = image` assignment — the only thing last-wins depends on — is byte-identical on core. The WR-06 'last one wins' outline row is unchanged; its thin evidence (no mutation isolates last-wins) predates the rebase and is not a drift impact.

**[DELETED]** Config MergeResourceTypeImages handles images with colons (tags)  `JB-config-010`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The recorded mutation ('split on ":" instead of "="') targets the `strings.Cut(entry, "=")` call, which is byte-identical on core. The WR-06 'image with a tag' outline row is in worker-config.feature, untouched by the rebase and passing.

**[DELETED]** Config MergeResourceTypeImages handles images with digest references  `JB-config-011`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same unchanged `strings.Cut(entry, "=")` split as the tag case; MergeResourceTypeImages is byte-identical between aef2244a63 and 5133d0ddbc. The WR-06 'image by digest' outline row was not modified by the rebase.

**[DELETED]** Config MergeResourceTypeImages skips malformed entries without equals sign  `JB-config-012`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The mutation target ('malformed override accepted') is the `if !ok || name == "" || image == "" { continue }` guard, byte-identical on core. The 'A malformed override is skipped rather than accepted' scenario, including its `there is no resource type "malformed-no-equals"` step, is unchanged by the rebase.

**[DELETED]** Config MergeResourceTypeImages does not modify the DefaultResourceTypeImages map  `JB-config-013`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The copy-then-write body (`merged := make(...)` then range-copy) that the 'defaults map mutated in place' mutation targets is byte-identical on core, and DefaultResourceTypeImages is still a package-level map with the same eight entries. 'Merging does not mutate the shared defaults' is not in step_changes.

**[DELETED]** Config NewClientset when a valid kubeconfig is provided creates a clientset from the kubeconfig file  `JB-config-014`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — NewClientset and RestConfig are both byte-identical on core and neither appears in any core commit under -S; the kubeconfig fixture is a test-local temp file that core cannot affect. The 'A kubeconfig file on disk produces a clientset' scenario and its steps/config.go testKubeconfig const are untouched by the rebase.

**[DELETED]** Config NewClientset when the kubeconfig path does not exist returns an error  `JB-config-015`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The mutation target for 'bad kubeconfig returns no error' is NewClientset/RestConfig's clientcmd.BuildConfigFromFlags arm, byte-identical on core (zero -S hits for either symbol). 'A kubeconfig path that does not exist is an error, not a panic' is in the untouched worker-config.feature and passed post-rebase.

**[DELETED]** Config NewClientset when no kubeconfig is provided and not in-cluster returns an error  `JB-config-016`
  - recorded evidence: PER-FILE (deletion commit `7bd092f004`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — RestConfig's `return rest.InClusterConfig()` fall-through — the exact line this test pins — is byte-identical on core, and no core commit touches NewClientset or RestConfig. 'With no kubeconfig and no cluster, there is nothing to connect to', including the KUBERNETES_SERVICE_HOST unset precondition in steps/config.go,…


## behavioral_worker_test.go

Deleted by `6a557c9fc6` — "Delete behavioral_worker_test.go; no gaps needed closing first". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** Behavioral Worker Tests RC-02: LookupVolume returns DaemonSetVolume returns a DaemonSetVolume backed by the persisted volume  `JB-behavioral_worker-000`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Every source function is byte-identical base->core: atc/worker/jetbridge/worker.go (LookupVolume, NewWorker, SetVolumeRepo) and atc/db/volume_repository.go (FindVolume, CreateVolumeWithHandle) have an empty diff aef2244a63..5133d0ddbc, and volume_daemonset.go's only change is InitializeTaskCache (NewDaemonSetVolume/Ha…

**[DELETED]** Behavioral Worker Tests RC-02: LookupVolume returns DaemonSetVolume with ArtifactLocator populated returns volume with its persisted worker source  `JB-behavioral_worker-001`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The hint 'NewDaemonSetBackend' is real but inert here: 1e023e7ca4 only added a `resolveSigner` built when Config.ArtifactDaemonResolveCapabilityKey is non-empty, and the test's cfg comes from NewConfig("test-namespace", "") (NewConfig itself diffs IDENTICAL), so the signer is nil and the path is unchanged. DaemonSetBa…

**[DELETED]** Behavioral Worker Tests RC-02: LookupVolume returns DaemonSetVolume without ArtifactLocator returns volume with its persisted worker source  `JB-behavioral_worker-002`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — storageBackend is nil so LookupVolume takes the NewDaemonSetVolume branch; NewDaemonSetVolume and DaemonSetVolume.Source diff IDENTICAL and worker.go is byte-identical on core, so the assertion Source()==workerName pins nothing core touched. Its brine counterpart (step-integration.feature @RC-02 'A locator pointing at…

**[DELETED]** Behavioral Worker Tests RC-03: Cache hit short-circuit returns a cached volume without creating a pod  `JB-behavioral_worker-003`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins that Worker.LookupVolume schedules no pods. worker.go and atc/db/volume_repository.go are unchanged on core, and no core commit between aef2244a63 and 5133d0ddbc contains the string LookupVolume in atc/worker/jetbridge, atc/db or atc/runtime. step-integration.feature @RC-03 'A cache hit is served from the databas…

**[DELETED]** Behavioral Worker Tests RC-03: Cache hit short-circuit persists the resource-cache association  `JB-behavioral_worker-004`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — createdVolume.InitializeResourceCache and DaemonSetVolume.InitializeResourceCache both diff IDENTICAL (atc/db/volume.go changed only InitializeTaskCache + TaskIdentifier); atc/db/resource_cache_factory.go and worker_factory.go are unchanged; team.CreateOneOffBuild diffs IDENTICAL despite atc/db/team.go's 263-insertion…

**[DELETED]** Behavioral Worker Tests RC-05: Cache invalidation returns not found when no persisted volume has the handle  `JB-behavioral_worker-005`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the not-found path of Worker.LookupVolume over volumeRepository.FindVolume; both files are byte-identical between the merge-base and core, and its counterpart worker.feature Scenario Outline 'A handle the database does not hold is not found' is not among the 8 brine files the rebase changed.

**[DELETED]** Behavioral Worker Tests RC-05: Cache invalidation returns not found when volumeRepo is nil  `JB-behavioral_worker-006`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the `if w.volumeRepo == nil { return nil, false, nil }` guard at the top of Worker.LookupVolume plus NewWorker; atc/worker/jetbridge/worker.go has zero core commits since the merge-base, so the guard is character-for-character the code the branch's evidence was taken against.

**[DELETED]** Behavioral Worker Tests CO-09: CreateVolumeForArtifact returns DaemonSetVolume with ArtifactKey persists an artifact volume and returns its database artifact  `JB-behavioral_worker-007`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Worker.CreateVolumeForArtifact and ArtifactKey live in the unchanged worker.go/artifact_locator.go; createdVolume.InitializeArtifact and Type diff IDENTICAL, volumeRepository.CreateVolume/FindVolume are unchanged. The -SArtifactKey hit on 0d336e062b resolves to a test file (daemonset_integration_test.go), not producti…

**[DELETED]** Behavioral Worker Tests LR-03: ATC restart resilience - LookupVolume without ArtifactLocator reconstructs the volume from persisted state  `JB-behavioral_worker-008`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — NewWorker, SetVolumeRepo and LookupVolume are in the byte-identical worker.go; db.NewVolumeRepository and volumeRepository.FindVolume are in the byte-identical atc/db/volume_repository.go. step-integration.feature @LR-03 'A restarted ATC finds a step's output again from persisted state' survives the rebase unmodified…

**[DELETED]** Behavioral Worker Tests LR-04: Container reuse when createdContainer already exists returns the same persisted container without inserting another row  `JB-behavioral_worker-009`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The 'buildVolumeMounts' hint is a substring false positive: 0d336e062b changed Container.buildVolumeMounts in container.go (cache-mode gate moved from c.metadata.JobID to c.containerSpec.TaskCacheIdentity), but this test runs Worker.FindOrCreateContainer -> Worker.buildVolumeMountsForSpec in worker.go, which is byte-i…

**[DELETED]** Behavioral Worker Tests FindOrCreateContainer returns volume mounts matching spec returns mounts for Dir, inputs, outputs, and caches  `JB-behavioral_worker-010`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same substring false positive as -009. Worker.buildVolumeMountsForSpec is unchanged and its cache branch keys off spec.Caches alone (no TaskCacheIdentity), so Dir+1 input+1 output+1 cache still yields exactly 4 mounts. The ContainerSpec.JobID -> TaskCacheIdentity swap in atc/runtime/types.go costs nothing here because…

**[DELETED]** Behavioral Worker Tests LookupVolume propagates DB errors returns the real repository error  `JB-behavioral_worker-011`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the `if err != nil { return nil, false, err }` propagation in Worker.LookupVolume over a closed connection; worker.go and atc/db/volume_repository.go are byte-identical base->core, and worker.feature 'Looking a volume up reports a lost database' is unchanged by the rebase.

**[DELETED]** Behavioral Worker Tests SkipResourceCache returns false to enable caching in DaemonSet mode  `JB-behavioral_worker-012`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Worker.SkipResourceCache is a one-line method in the byte-identical worker.go; no core commit between aef2244a63 and 5133d0ddbc contains the string SkipResourceCache in atc/worker/jetbridge, atc/db or atc/runtime. Its counterpart step in worker.feature @RC-01 is unchanged.

**[DELETED]** Behavioral Worker Tests CreateVolumeForArtifact without volumeRepo returns an error indicating volume repository not configured  `JB-behavioral_worker-013`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the nil-volumeRepo guard message in Worker.CreateVolumeForArtifact; worker.go has zero core commits since the merge-base, so the error string and the guard are unchanged. worker.feature 'Creating an artifact volume without a volume repository is refused' survives the rebase untouched.

**[DELETED]** Behavioral Worker Tests LookupVolume passes handle to FindVolume finds only the exact persisted handle  `JB-behavioral_worker-014`
  - recorded evidence: PER-FILE (deletion commit `6a557c9fc6`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Exact-handle matching lives entirely in volumeRepository.FindVolume/CreateVolumeWithHandle (atc/db/volume_repository.go, empty diff) reached through the byte-identical Worker.LookupVolume. Its counterparts worker.feature 'A volume in the database is found by its handle' and the 'a prefix of a real one' outline row are…


## artifact_integration_test.go

Deleted by `65a62160d8` — "Delete resource, secret_env and artifact_integration; keep supervisor_script". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** Artifact Integration multi-step pipeline with artifact passing creates an artifact in step 1 and passes it as input to step 2  `JB-artifact_integration-000`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The only exercised symbols core changed are Container.buildVolumeMounts (0d336e062b) and Container.buildArtifactInitContainers (1e023e7ca4), and both changes sit in branches this test cannot reach: it builds the worker from NewConfig("ci-namespace","") and never calls SetArtifactLocator, so storageBackend==nil, CacheH…

**[DELETED]** Artifact Integration multi-step pipeline with artifact passing passes artifacts through get → task → put pipeline steps  `JB-artifact_integration-001`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same configuration as row 000 — storageBackend nil, no Caches, no CacheHostPath/CacheStore — so 0d336e062b's three changed cache arms and 1e023e7ca4's BuildFetchInitContainers error propagation are unreachable, and the input/output/workdir mounts this test pins come from worker.buildVolumeMountsForSpec in a byte-ident…

**[DELETED]** Artifact Integration artifact persistence across pod restarts returns the same DaemonSetVolume key across multiple lookups  `JB-artifact_integration-002`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Every symbol it exercises is byte-identical on core: worker.go (CreateVolumeForArtifact, LookupVolume) has an empty diff, config.go's ArtifactKey is untouched (that file is +54/-0, all resolve-capability consts/fields), and volume_daemonset.go's only change is InitializeTaskCache's identity signature, which this test…

**[DELETED]** Artifact Integration artifact persistence across pod restarts preserves DB volume association through lookup  `JB-artifact_integration-003`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — LookupVolume and CreateVolumeForArtifact live in a byte-identical worker.go; DaemonSetVolume.DBVolume is untouched (volume_daemonset.go changed only InitializeTaskCache); atc/db/worker_artifact.go and atc/db/volume_repository.go have empty diffs, so WorkerArtifact.Volume, CreatedVolume.Handle/WorkerName/WorkerArtifact…

**[DELETED]** Artifact Integration artifact cleanup artifact volumes are created as VolumeTypeArtifact for Reaper identification  `JB-artifact_integration-004`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The single production site of db.VolumeTypeArtifact in this package is worker.go:221 inside CreateVolumeForArtifact, and worker.go is byte-identical between aef2244a63 and 5133d0ddbc; VolumeRepository.CreateVolume/FindVolume and CreatedVolume.Type are in atc/db/volume_repository.go (empty diff) and the untouched part…

**[DELETED]** Artifact Integration artifact cleanup orphaned artifacts return not-found when DB record is removed  `JB-artifact_integration-005`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The !found branch of Worker.LookupVolume is in a byte-identical worker.go, and CreatedVolume.Destroying / DestroyingVolume.Destroy sit in atc/db/volume.go whose only two hunks on core are InitializeTaskCache (identity signature + Validate) and TaskIdentifier's COALESCE'd SELECT — neither on the destroy-then-lookup pat…

**[DELETED]** Artifact Integration artifact cleanup artifact volumes from different teams are isolated  `JB-artifact_integration-006`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Team-scoped artifact lookup is WorkerArtifact.Volume/ID in atc/db/worker_artifact.go (empty diff) and the teamID plumbing in CreateVolumeForArtifact in a byte-identical worker.go; atc/db/team_factory.go (CreateTeam) is also unchanged. atc/db/team.go's +263 lines are entirely in savePipeline/savePipelineWithOptions/sav…

**[DELETED]** Artifact Integration CreateVolumeForArtifact always returns DaemonSetVolume returns a DaemonSetVolume  `JB-artifact_integration-007`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — This pins the storageBackend==nil branch of CreateVolumeForArtifact (worker.go:243) plus the persisted worker_artifact_id/handle; worker.go has an empty diff, NewDaemonSetVolume's constructor is untouched (volume_daemonset.go changed only InitializeTaskCache), and VolumeRepository.FindVolume / CreatedVolume.WorkerArti…


## behavioral_permutations_test.go

Deleted by `6d2589a43a` — "Delete behavioral_permutations_test.go — 18 evidenced, 1 inert". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

Restored tests from this file live in `atc/worker/jetbridge/behavioral_permutations_restored_test.go`.

**[REFUTED]** TestBuildVolumeMounts_MultipleInputsNoOutputs  `JB-behavioral_permutations-000`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): this is a rebase conflict file and core commit 0d336e062b really did edit it. Rule (b): its counterpart container-pod.feature is backed by steps/container_spec.go and steps/domain.go, both in step_changes. (a) does not apply on its own — 0d336e062b changed only buildVolumeMounts' cache-mode arms, and this te…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go :: (*Container).buildVolumeMounts — drop the last input's volume+mount:; brine RED: Then the pod has 3 volumes — status failed, error: expected 3 volumes, found 2: [dir-0 input-1]; go RED: behavioral_permutations_test.go:173: expected 4 volumes, got 3 / --- FAIL: TestBuildVolumeMounts_MultipleInputsNoOutputs (0.00s); skeptic: different-behaviour pairing — the Go test's hostPath third is unreachable from @CO-04 by construction, so I built two narrower mutations that break only what the Go test asserts and measured brine on… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[DELETED]** TestBuildVolumeMounts_NoInputsMultipleOutputs  `JB-behavioral_permutations-001`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c) conflict file, plus rule (b) — the named counterpart "An output's directory on the node carries the name the pipeline gave it" lives in container-pod.feature, whose step files changed in the rebase. The recorded mutation targets buildVolumeMounts' OUTPUT loop, which 0d336e062b left byte-identical, so (a) does…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go :: (*Container).buildVolumeMounts — output loop, line 980; brine RED: Then the volume mounted at "/tmp/build/workdir/compiled" is the node directory the daemon serves as "compile-output-handle/compiled" — error: the step writes "/tmp/build/workdir/compiled" into "/var/…; go RED: behavioral_permutations_test.go:225: volume "output-1": hostPath "/artifact-store/steps/handle-2/output-1" does not contain expected suffix "steps/handle-2/logs" behavioral_permutations_test.go:229:…; skeptic: Three attacks run: (1) red-by-adaptation — baseline the restored test with the mutation reverted; (2) mutation-not-the-recorded-one / order-masked assertion — apply the recorded diff verbatim at line… → HOLDS

**[REFUTED]** TestBuildVolumeMounts_MixedOverlap  `JB-behavioral_permutations-002`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c) conflict file; rule (b) because the named counterpart @CO-05 "An input sharing an output's path is filed under the output's name" is in container-pod.feature, whose backing steps/container_spec.go and steps/domain.go changed in the rebase. The recorded mutation targets the `subdir = outName` overlap branch, w…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go :: (*Container).buildVolumeMounts (lines 949-951); brine RED: Then the volume mounted at "/tmp/build/workdir/repo" is the node directory recorded for the output "repo-modified" — error: the step writes "/tmp/build/workdir/repo" into "/var/concourse/artifacts/st…; go RED: behavioral_permutations_test.go:267: volume "input-1": hostPath "/artifact-store/steps/handle-3/input-1" does not contain expected suffix "steps/handle-3/modified-code" behavioral_permutations_test.g…; skeptic: different-behaviour pairing (narrower mutation isolating the clause the Go test pins), plus red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[REFUTED]** TestBuildVolumeMounts_AllOverlapping  `JB-behavioral_permutations-003`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Same conflict file (c) and same container-pod.feature step-file churn (b) as its MixedOverlap twin; both are evidenced by the one recorded `subdir = outName` mutation, which core left untouched, so (a) adds nothing.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts, line 950 (unchanged from the recorded target). Neutralized the overlap branch's assignment (kept `_ = outName` so the file still com…; brine RED: Then the volume mounted at "/tmp/build/workdir/repo" is the node directory recorded for the output "repo-modified" (line 463) — status failed, error: the step writes "/tmp/build/workdir/repo" into "/…; go RED: behavioral_permutations_test.go:324: volume "input-1": hostPath "/artifact-store/steps/handle-4/input-1" does not contain expected suffix "steps/handle-4/code-out" behavioral_permutations_test.go:328…; skeptic: different-behaviour pairing, driven by a NARROWER honest mutation (which of several outputs the shared directory is filed under) — plus red-by-adaptation and unrelated-brine-red controls, both of whi… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[REFUTED]** TestBuildVolumeMounts_NoOverlap  `JB-behavioral_permutations-004`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c) conflict file and rule (b) container-pod.feature step churn. Counts-and-presence only, no caches, so the cache-mode narrowing in 0d336e062b cannot reach it — (a) and (d) do not apply.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go :: (*Container).buildVolumeMounts — output loop guard made unconditional so a non-overlapping output never gets its own volume/mount:; brine RED: Then the pod has 3 volumes (line 52) — status failed, error: expected 3 volumes, found 2: [dir-0 input-1]; go RED: behavioral_permutations_test.go:354: expected 5 volumes, got 3 (--- FAIL: TestBuildVolumeMounts_NoOverlap (0.00s)) — this is assertVolumeCount(t, volumes, 5), which Fatal-stops before assertMountCoun…; skeptic: different-behaviour pairing — built a NARROWER, honest mutation that breaks only the multiplicity clause the Go test pins ("each of several disjoint outputs gets its own volume/mount"), which brine c… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[DELETED]** TestBuildVolumeMounts_AllVolumeTypes_DaemonSet  `JB-behavioral_permutations-005`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — The one test core itself edited (0d336e062b added TaskCacheIdentity to its ContainerSpec), and the very arm its evidence names — buildVolumeMounts' `storageBackend != nil && len(resolvedCaches) > 0` cache auto-detect — is the arm 0d336e062b narrowed, with CacheVolume and stableCacheKey re-signatured alongside; its bri…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go :: (*Container).buildVolumeMounts — delete the artifact-store cache auto-detect arm (2 lines, at line 1002 on 27d81692fa):; brine RED: Then the cache at "/tmp/build/workdir/.cache" is kept on the node under "/var/concourse/artifacts/caches/job-7-compile-" — error: expected the cache at "/tmp/build/workdir/.cache" to live on the node…; go RED: behavioral_permutations_test.go:428: expected cache to be hostPath in DaemonSet mode behavioral_permutations_test.go:430: volume "cache-4": expected hostPath, got emptyDir or nil --- FAIL: TestBuildV…; skeptic: Four attacks run, none landed: (1) unrelated/flaky brine red — ran features/container-pod.feature clean BEFORE touching anything; (2) red-by-adaptation — ran the restored+adapted Go test unmutated; (… → HOLDS

**[REFUTED]** TestBuildVolumeMounts_PutContainer  `JB-behavioral_permutations-006`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Impacted only by rule (c) — it lives in a conflict file core edited. It names no brine scenario (4b37fe4ee0 declared the put-specific layout a deliberate non-gap), so (b) has nothing to attach to beyond the file-wide list, and its counts-and-presence assertions touch no region 0d336e062b or 1e023e7ca4 changed.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go :: (*Container).buildVolumeMounts (drop the last input's volume+mount); brine RED: Then the pod has 3 volumes -> "expected 3 volumes, found 2: [dir-0 input-1]"; go RED: behavioral_permutations_test.go:464: expected 4 volumes, got 3; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go assertion pins), plus red-by-adaptation control and an adapter-sensitivity control with the verifier's own recorded mutation → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[REFUTED]** TestBuildVolumeMounts_GetContainer  `JB-behavioral_permutations-007`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c) conflict file, and rule (b) because its named counterpart "A get step's resource lands in the node directory the daemon will serve" is in container-pod.feature, whose step files changed in the rebase. The dir-volume branch it pins was not touched by core, so (a)/(d) do not apply.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts (line 919 on the rebased head 27d81692fa — identical to the recorded target):; brine RED: Then the step sees a volume mounted at "/tmp/resource" (line 628) — error: expected the step's mounts to include "/tmp/resource", found []; go RED: behavioral_permutations_test.go:494: expected 1 volumes, got 0 --- FAIL: TestBuildVolumeMounts_GetContainer (0.00s); skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts), plus red-by-adaptation and unrelated-brine-red checks → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[GAP]** TestBuildVolumeMounts_CheckContainer  `JB-behavioral_permutations-008`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c) conflict file; rule (b) because both named counterparts (@CO-08 "A check's workspace is ephemeral even on a node-local worker" and "A repeated check is not handed a workspace to clear") are container-pod.feature scenarios whose step files changed. (*Container).stepVolume and BuildCleanupInitContainer are byte…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation Two mutations, applied and measured SEPARATELY (the test bundles two behaviours), exactly as the recipe specified. Both recorded targets exist byte-identical on the rebased head 27d81692fa. MUTATION…; brine RED: MUTATION 1 — Then the volume mounted at "/tmp/build/check" is lost with the pod (line 380) -> failed: "expected the volume at \"/tmp/build/check\" to be ephemeral, it is node-local storage". Sibling…; go RED: MUTATION 1: `behavioral_permutations_test.go:527: volume "dir-0": expected emptyDir, got something else` (--- FAIL: TestBuildVolumeMounts_CheckContainer (0.00s)) — that is the assertEmptyDir(t, volum…; skeptic: Four attacks run, all measured: (1) red-by-adaptation — unmutated Go baseline with the restored adapted test; (2) unrelated/flaky brine red — one clean unmutated run of the whole feature; (3) full re… → GAP
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[REFUTED]** TestBuildVolumeMounts_SidecarWithCaches  `JB-behavioral_permutations-009`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — Declares Caches, so 0d336e062b's cache-mode narrowing changes which production path it exercises — on core with a nil TaskCacheIdentity it silently stops reaching DaemonSetBackend.CacheVolume (one of its declared source functions) while still passing, a false green; plus rule (c) conflict file and rule (b) container-p…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation PRIMARY (used for the HOLDS verdict) — atc/worker/jetbridge/container.go, func buildSidecarContainers (line 535 on the rebased head 27d81692fa):; brine RED: And the sidecar "postgres" sees the same volumes as the step — scenario_end error: `the step sees "/tmp/build/workdir/input-a" but sidecar "postgres" does not`; go RED: behavioral_permutations_test.go:586: expected sidecar to have 3 mounts (same as main), got 1; skeptic: different-behaviour pairing (narrower honest mutation that breaks only what the Go test asserts), plus red-by-adaptation and unrelated-brine-red as controls, plus a stale-binary/flake control on the… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[REFUTED]** TestBuildVolumeMounts_SidecarWithScratch  `JB-behavioral_permutations-010`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c) conflict file and rule (b) container-pod.feature step churn. It runs in emptyDir mode with no caches, and buildSidecarContainers plus the ScratchPaths loop are byte-identical on core, so (a)/(d) do not apply.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation PRIMARY (the one the verdict rests on) — atc/worker/jetbridge/container.go, func buildSidecarContainers:; brine RED: And the sidecar "postgres" sees the same volumes as the step (line 159) — error: the step sees "/tmp/build/workdir/input-a" but sidecar "postgres" does not; go RED: behavioral_permutations_test.go:631: sidecar is missing scratch mount at /scratch/tmp --- FAIL: TestBuildVolumeMounts_SidecarWithScratch (0.00s); skeptic: different-behaviour pairing (narrower honest mutation that breaks only what the Go test asserts) + red-by-adaptation + unrelated/flaky brine red → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[REFUTED]** TestBuildArtifactInitContainers_MultipleInputs  `JB-behavioral_permutations-011`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — Its production target DaemonSetBackend.BuildFetchInitContainers was rewritten on core by 1e023e7ca4 (error return, per-item capability signing, new batchItem.Capability wire field) and (*Container).buildArtifactInitContainers now propagates that error; plus rule (c) conflict file and rule (b) — artifact-recording.feat…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/storage_daemonset.go :: (*DaemonSetBackend).BuildFetchInitContainers — truncate the batch to its first item after the input loop, before the empty-batch check:; brine RED: And that fetch asks the daemon for "vol-b" — error verbatim: expected the batch of keys the pod asks for in one request to mention "vol-b", got "{\"items\":[{\"key\":\"vol-a\",\"dest\":\"/var/folders…; go RED: behavioral_permutations_test.go:673: expected 4 volume mounts (1 hostpath + 3 inputs), got 2; skeptic: different-behaviour pairing (split the verifier's compound mutation into its two independent halves and measured each separately); also ran red-by-adaptation and decorative-assertion checks → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[DELETED]** TestBuildArtifactInitContainers_InputWithoutArtifact  `JB-behavioral_permutations-012`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — The recorded mutation targets the `continue` that skips an artifact-less input, which now sits inside the rewritten BuildFetchInitContainers (1e023e7ca4 added an error return and a signing block immediately below it), so the mutation itself has to change shape; plus rule (c) conflict file and rule (b) container-pod.fe…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/storage_daemonset.go:BuildFetchInitContainers (line 141); brine RED: Then the pod fetches exactly 1 of the step's inputs (line 599) -- error: `the pod for "mixed-inputs-handle" has no "fetch-inputs" init container, so nothing is fetched into its workspace at all: ever…; go RED: behavioral_permutations_test.go:706: expected 1 init container (nil artifact skipped), got 0 -- --- FAIL: TestBuildArtifactInitContainers_InputWithoutArtifact (0.00s); skeptic: Ran five attacks: (1) red-by-adaptation — restored the RAW merge-base test, not the pre-adapted copy, and ran it clean; (2) unrelated/flaky brine red — full clean run of container-pod.feature; (3) or… → HOLDS
  - superseded 2026-09-07 (Hangar branch): the brine half of this pairing is gone. Its scenario, `An input with nothing to fetch is skipped, not fatal to the rest`, described a step with an artifact-less input, which `(*Container).validateInputs` now rejects at pod build time and which neither producer of `runtime.Input` can emit; the scenario and the three `the pod fetches …` sentences it alone used were deleted. The Go half changed shape with it: `BuildFetchInitContainers` is exported, so instead of skipping an artifact-less input it now refuses one, pinned by `TestDaemonSetBackend_BuildFetchInitContainers_RefusesNilArtifact`.

**[DELETED]** TestBuildArtifactInitContainers_LocatorHitVsMiss  `JB-behavioral_permutations-013`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — It pins the batch command TEXT produced by BuildFetchInitContainers, and 1e023e7ca4 changed what goes into that text (batchItem gained a Capability field, and the function gained an error return); plus rule (c) conflict file and rule (b) — artifact-recording.feature's step file changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/storage_daemonset.go :: (*DaemonSetBackend).BuildFetchInitContainers — deleted the locator lookup so the raw artifact key is sent to the daemon:; brine RED: Then that fetch asks the daemon for "build-42/result" — error verbatim: `expected the batch of keys the pod asks for in one request to mention "build-42/result", got "{\"items\":[{\"key\":\"vol-resul…; go RED: behavioral_permutations_test.go:753: expected batch command to contain locator HostDir 'producer-handle/result', got: sh -c ... PAYLOAD='{"items":[{"key":"vol-a","dest":"/artifact-store/steps/locator…; skeptic: Ran four attacks: (1) red-by-adaptation, (2) unrelated/flaky brine red, (3) order-masked / decorative assertion, (4) the sharp one — a NARROWER mutation that breaks only the SECOND of the Go test's t… → HOLDS

**[REFUTED]** TestBuildSidecarContainers_GetsAllMountsInDaemonSetMode  `JB-behavioral_permutations-014`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — Declares Caches (/cache/data), so 0d336e062b's cache-mode narrowing silently changes which production path it covers — with a nil TaskCacheIdentity the cache becomes an emptyDir and DaemonSetBackend.CacheVolume is never reached, while the mount-presence assertions still pass; plus rule (c) conflict file and rule (b) c…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go :: buildSidecarContainers (line 546-548 on 27d81692fa); brine RED: And the sidecar "postgres" sees the same volumes as the step -- error: the step sees "/tmp/build/workdir/input-a" but sidecar "postgres" does not; go RED: behavioral_permutations_test.go:804: expected sidecar to have 5 mounts (same as main), got 1 -- `--- FAIL: TestBuildSidecarContainers_GetsAllMountsInDaemonSetMode (0.00s)`; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go assertion pins), plus red-by-adaptation control and a reproduction of the recorded mutation to identify which mount actuall… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[DELETED]** TestBuildPod_InitContainerOrdering  `JB-behavioral_permutations-015`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — buildPod's init-container assembly now threads an error out of (*Container).buildArtifactInitContainers, and the fetch container it orders is built by the rewritten BuildFetchInitContainers (1e023e7ca4); plus rule (c) conflict file and rule (b) — both counterpart features (container-pod, container-run) had step files…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation TWO mutations, applied and measured separately (recipe asked for both). M1 — init-container ORDER, atc/worker/jetbridge/container.go :: (*Container).buildPod (lines 436-448 on 27d81692fa):; brine RED: M1 — Then the pod clears the workspace before it fetches its inputs (line 524) => failed: "the pod for \"order-handle\" runs [fetch-inputs cleanup-stale] in that order, so it deletes the inputs it ha…; go RED: M1 (order swap): "behavioral_permutations_test.go:864: expected first init container to be 'cleanup-stale', got \"fetch-inputs\"" and "behavioral_permutations_test.go:869: expected second init contai…; skeptic: Four attacks run (not just reasoned): (1) red-by-adaptation — clean control with the restored test in place; (2) unrelated/flaky brine red — clean runs of BOTH counterpart features before any mutatio… → HOLDS

**[GAP]** TestBuildArtifactInitContainers_NoDaemonSet_ReturnsNil  `JB-behavioral_permutations-016`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (a, c) — The function holding the guard it pins, (*Container).buildArtifactInitContainers, was modified on core by 1e023e7ca4 (its other return now propagates BuildFetchInitContainers' error), so (a) fires literally even though the `storageBackend == nil` guard is byte-identical; plus rule (c) conflict file. Not (b) or (d): it…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/container.go, (*Container).buildArtifactInitContainers (line 515-519 on core 27d81692fa):; brine GREEN: none — 44 scenario_end events, all status "passed", under the recorded mutation. Re-run under the sharper variant (a spurious `probe-init` init container injected into every no-storage-backend pod):…; go RED: behavioral_permutations_test.go:904: expected nil init containers in non-DaemonSet mode, got 0 --- FAIL: TestBuildArtifactInitContainers_NoDaemonSet_ReturnsNil (0.00s) FAIL github.com/concourse/conco…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[GAP]** TestBuildVolumeMounts_EmptyDirMode_AllEmptyDir  `JB-behavioral_permutations-017`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c) conflict file and rule (b) container-pod.feature step churn. It runs with no storage backend and no caches, so neither the cache-mode narrowing (0d336e062b) nor the capability signing (1e023e7ca4) can reach it — (a) and (d) do not apply.
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/container.go :: (*Container).buildVolumeMounts — input loop (line 943 on 27d81692fa), skip the last input:; brine RED: Then the pod has 3 volumes (line 22) — status "failed", error: expected 3 volumes, found 2: [dir-0 input-1]; go RED: behavioral_permutations_test.go:928: expected 3 volumes, got 2 --- FAIL: TestBuildVolumeMounts_EmptyDirMode_AllEmptyDir (0.00s); skeptic: Ran four attacks: (1) red-by-adaptation control, (2) unrelated/flaky brine red control, (3) reproduce the recorded mutation, (4) narrower mutations isolating each of the deleted test's three claims. Attacks 1-3 failed. Attack 4 split the test: the ephemerality claim IS independently both-red (closing the verifier's declared honest limit), but the MOUNT-COUNT claim is not covered anywhere — duplicating each input's VolumeMount is Go-red (`expected 3 mounts, got 4`) and leaves all 44 container-pod scenarios green, and `step-integration.feature`'s one exact mount count is over a parallel implementation in worker.go → GAP (the pairing broke on a claim no brine scenario owns)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`

**[REFUTED]** TestBuildVolumeMounts_RelativeScratchPath  `JB-behavioral_permutations-018`
  - recorded evidence: PER-FILE (deletion commit `6d2589a43a`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c) conflict file and rule (b) — its named counterpart @CO-08 "A relative scratch path lands inside the working directory" is a container-pod.feature scenario whose backing step files changed in the rebase. The ScratchPaths resolution branch is byte-identical on core, so (a)/(d) do not apply.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go :: (*Container).buildVolumeMounts (ScratchPaths loop); brine RED: Then the step sees a volume mounted at "/tmp/build/workdir/tmp-work" -- expected the step's mounts to include "/tmp/build/workdir/tmp-work", found [/tmp/build/workdir /tmp-work]; go RED: behavioral_permutations_test.go:955: no mount found at path "/tmp/build/relative-scratch"; skeptic: decorative/uncovered-clause: the Go test pins the mount COUNT (exactly 2), which no brine step asserts — built a narrower honest mutation that breaks only that clause; also ran red-by-adaptation and… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_permutations_restored_test.go`


## behavioral_runtime_spec_test.go

Deleted by `49969fd6c4` — "Delete behavioral_runtime_spec_test.go; give the tty flag a real terminal". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

Restored tests from this file live in `atc/worker/jetbridge/behavioral_runtime_spec_restored_test.go`.

**[DELETED]** [PE-03] ImagePullPolicy for main container [PE-03] sets ImagePullPolicy to PullIfNotPresent on the main container  `JB-behavioral_runtime_spec-000`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) scenario-level: the named counterpart, container-spec.feature '@PE-03 Scenario: The main container is pulled only when absent', drives the pod through the 'When the container runs' DefineMap in brine/steps/container_spec.go, which the rebase edited (TaskCacheIdentity: in.taskCacheIdentity()) — that builder is the…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).buildPod — main container literal:; brine RED: And the main container image pull policy is "IfNotPresent" (container-spec.feature line 16) — step_end status "failed", error verbatim: expected the main container image pull policy to be "IfNotPrese…; go RED: [FAILED] Expected <v1.PullPolicy>: Always to equal <v1.PullPolicy>: IfNotPresent In [It] at: .../atc/worker/jetbridge/behavioral_runtime_spec_test.go:101; skeptic: Four attacks run, all failed to break the pairing: (1) red-by-adaptation — restored Go test run clean; (2) unrelated/flaky brine red — feature run clean with no mutation; (3) order-masked / decorativ… → HOLDS

**[DELETED]** [PE-05] Image URL prefix stripping for main container [PE-05] strips Concourse image URL prefixes from main container image / strips docker:/// prefix  `JB-behavioral_runtime_spec-001`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) scenario-level: the counterpart container-spec.feature '@PE-05 Scenario Outline' Examples row docker3 runs through the 'When the container runs' builder in brine/steps/container_spec.go, which the rebase edited for 0d336e062b. Not (a)/(d): stripImagePrefix and resolveImage (container.go:695-727) are byte-identical…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func stripImagePrefix (line 719):; brine RED: Then the main container image is "busybox:latest" (container-spec.feature:23) — error: expected the main container image to be "busybox:latest", got "/busybox:latest"; go RED: [FAILED] Expected <string>: /busybox:latest to equal <string>: busybox:latest In [It] at: .../atc/worker/jetbridge/behavioral_runtime_spec_test.go:154; skeptic: Four attacks run, all failed to refute: (1) red-by-adaptation, (2) unrelated/flaky brine red (clean baseline), (3) mutation-not-the-recorded-one, (4) different-behaviour pairing probed by reading bot… → HOLDS

**[DELETED]** [PE-05] Image URL prefix stripping for main container [PE-05] strips Concourse image URL prefixes from main container image / strips docker:// prefix  `JB-behavioral_runtime_spec-002`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) scenario-level: same 'When the container runs' DefineMap in brine/steps/container_spec.go changed in the rebase; the Examples row docker2 is this row's evidence. stripImagePrefix itself is unchanged on core, so (a) and (d) are clean.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func stripImagePrefix (line 718-725) — dropped the "docker://" element from the prefix slice:; brine RED: Then the main container image is "busybox:latest" (line 23) — status failed, error: expected the main container image to be "busybox:latest", got "docker://busybox:latest"; go RED: [FAILED] Expected <string>: docker://busybox:latest to equal <string>: busybox:latest In [It] at: atc/worker/jetbridge/behavioral_runtime_spec_test.go:154 — assertion is Expect(mainImage).To(Equal(ex…; skeptic: Ran four attacks, all failed to refute: (1) red-by-adaptation — clean baseline of the restored Go test; (2) unrelated/flaky brine red — clean baseline of the whole feature; (3) order-masked/decorativ… → HOLDS

**[DELETED]** [PE-05] Image URL prefix stripping for main container [PE-05] strips Concourse image URL prefixes from main container image / strips raw:/// prefix  `JB-behavioral_runtime_spec-003`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) scenario-level: the rawpfx Examples row of container-spec.feature '@PE-05' is built by the 'When the container runs' DefineMap in brine/steps/container_spec.go, which the rebase edited. stripImagePrefix is unchanged on core (no diff, no -S hit), so (a)/(d) are clean.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go : func stripImagePrefix (line 718 on head 27d81692fa); brine RED: Then the main container image is "alpine:3" (line 23) — step_end/scenario_end error: expected the main container image to be "alpine:3", got "raw:///alpine:3"; go RED: behavioral_runtime_spec_test.go:154 — [FAILED] Expected <string>: raw:///alpine:3 to equal <string>: alpine:3 (Expect(mainImage).To(Equal(expectedImage))); skeptic: Four attacks run, all failed to refute: (1) red-by-adaptation — restored Go test with the mutation REVERTED; (2) unrelated/flaky brine red — clean baseline run of the feature; (3) order-masked/decora… → HOLDS

**[DELETED]** [PE-05] Image URL prefix stripping for main container [PE-05] strips Concourse image URL prefixes from main container image / leaves plain image reference unchanged  `JB-behavioral_runtime_spec-004`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) scenario-level: the negative 'plain' Examples row of container-spec.feature '@PE-05' runs through the rebase-edited 'When the container runs' DefineMap in brine/steps/container_spec.go. Nothing on core touches stripImagePrefix, so this is a rebase-side impact only.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func stripImagePrefix (line 718 on head 27d81692fa) — the recorded over-eager-stripper mutation, applied verbatim:; brine RED: Then the main container image is "alpine:3.18" (line 23) — status failed, error: expected the main container image to be "alpine:3.18", got "3.18"; go RED: [FAILED] Expected <string>: 3.18 to equal <string>: alpine:3.18 In [It] at: .../atc/worker/jetbridge/behavioral_runtime_spec_test.go:154; skeptic: Ran six attacks: (1) red-by-adaptation (clean run of the restored Go test), (2) unrelated/flaky brine red (clean brine run before any mutation), (3) order-masked/decorative assertion (which brine ste… → HOLDS

**[DELETED]** [PE-06] Environment variable merging [PE-06] merges env vars from both ContainerSpec and ProcessSpec into the pod  `JB-behavioral_runtime_spec-005`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) scenario-level: container-spec.feature '@PE-06 Scenario: Environment merges both specs' uses the 'When the container runs' DefineMap in brine/steps/container_spec.go, which the rebase changed to set TaskCacheIdentity. The production merge site (container.go:423-424 in buildPod) and envVars/splitEnvVar are byte-ide…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).buildPod, line 424 — delete the ProcessSpec env append so only ContainerSpec.Env reaches the main container:; brine RED: And the main container environment resolves "PROCESS_VAR" to "from_process" (line 41) — error: expected "PROCESS_VAR" in the main container environment, found none of 2 vars; go RED: [FAILED] Expected <string>: to equal <string>: from_process In [It] at: atc/worker/jetbridge/behavioral_runtime_spec_test.go:221 (the `By("including env vars from ProcessSpec"); Expect(envMap["PROCES…; skeptic: Ran four attacks: (1) red-by-adaptation (sha-compare the restored file to the merge-base blob, then run it clean); (2) unrelated/flaky brine red (run container-spec.feature once with no mutation); (3… → HOLDS

**[DELETED]** [PE-06] Environment variable merging [PE-06] ProcessSpec env vars take precedence over ContainerSpec on key collision  `JB-behavioral_runtime_spec-006`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) scenario-level: container-spec.feature '@PE-06 Scenario: The process spec wins a collision' is built by the rebase-edited 'When the container runs' DefineMap in brine/steps/container_spec.go. The append order this row pins (container.go:423-424) is untouched by core.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).buildPod (lines 423-424):; brine RED: Then the main container environment resolves "SHARED_VAR" to "from_process" (line 50) — error: expected the effective value for "SHARED_VAR" to be "from_process", got "from_container" (all values: SH…; go RED: behavioral_runtime_spec_test.go:260 — Expect(sharedVarValues[len(sharedVarValues)-1]).To(Equal("from_process")) → "[FAILED] Expected <string>: from_container to equal <string>: from_process"; skeptic: Four attacks run, none refuted: (1) unrelated/flaky brine red — clean control run of the whole feature; (2) red-by-adaptation — byte-compare of the restored file against the merge-base plus an unmuta… → HOLDS

**[REFUTED]** [PE-08] TTY flag in exec mode [PE-08] passes TTY=true to ExecInPod when ProcessSpec.TTY is set  `JB-behavioral_runtime_spec-007`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) at FEATURE granularity only: the named counterpart lives in container-run.feature, which the rebase's step_changes lists under brine/steps/container_extra.go (runExtraSpecFromDraft gained TaskCacheIdentity, reported INERT for that feature's drafts) — but the two @PE-08 scenarios themselves are backed by brine/step…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/process.go, (*execProcess).Wait — the tty argument to PodExecutor.ExecInPod:; brine RED: Then the step reports "terminal" (line 293) — status failed, error verbatim: expected the command's output to mention "terminal", got "pipe " (a shell that believes it is talking to a pipe gives `fly…; go RED: behavioral_runtime_spec_test.go:334 — Expect(calls[0].tty).To(BeTrue(), "expected TTY=true to be passed to ExecInPod"). Ginkgo output verbatim: [FAILED] expected TTY=true to be passed to ExecInPod Ex…; skeptic: different-behaviour pairing (narrower mutation isolating the path the Go It exercises and brine deliberately avoids), plus red-by-adaptation and harness-liveness controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_runtime_spec_restored_test.go`

**[GAP]** [PE-08] TTY flag in exec mode [PE-08] passes TTY=false to ExecInPod when ProcessSpec.TTY is nil  `JB-behavioral_runtime_spec-008`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) at FEATURE granularity only, same as row 007: container-run.feature appears in step_changes via brine/steps/container_extra.go, while the @PE-08 negative scenario itself is backed by the unchanged brine/steps/tty.go. Core did not touch process.go:847 or anything else this pins.
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/process.go, (*execProcess).Wait — the tty argument to executor.ExecInPod:; brine RED: Then the step reports "pipe" (line 301) — status failed, error verbatim: expected the command's output to mention "pipe", got "terminal\r " (a shell that believes it is talking to a pipe gives `fly h…; go RED: behavioral_runtime_spec_test.go:375 — [FAILED] expected TTY=false when ProcessSpec.TTY is nil / Expected / <bool>: true / to be false; skeptic: Four attacks run: (1) unrelated/flaky brine red — clean baseline of container-run.feature; (2) red-by-adaptation — sha-checked restoration, unmutated Go run; (3) order-masked/decorative assertion — s… → GAP
  - **the test is restored** — `atc/worker/jetbridge/behavioral_runtime_spec_restored_test.go`

**[REFUTED]** [SC-07] Sidecar log streaming routing (direct mode) [SC-07] when SidecarWriters contains an entry, GetLogs is requested for the sidecar container by name  `JB-behavioral_runtime_spec-009`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) at FEATURE granularity only: the inferred counterpart sits in container-pod.feature, which step_changes lists under brine/steps/container_spec.go and domain.go — but the @SC-07 scenarios are backed by brine/steps/container_lifecycle.go (runWithSidecarIO, which builds its own ContainerSpec with no TaskCacheIdentity…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation TWO mutations, both in atc/worker/jetbridge/process.go, (*Process).streamLogs. (1) RECORDED mutation (recipe verbatim), at process.go ~262-275: for _, sc := range p.container.containerSpec.Sidecars {…; brine RED: Then the sidecar's output arrives on its own stream (line 260) - status failed, error: "expected the sidecar's log on its dedicated stream; nothing arrived, so a user watching that sidecar would see…; go RED: Under variant B: '[FAILED] expected GetLogs to be called for the sidecar container / Expected / <bool>: false / to be true' at behavioral_runtime_spec_test.go:464; summary 'Ran 1 of 56 Specs ... FAIL…; skeptic: different-behaviour pairing (primary, via a narrower mutation) + mutation-not-the-recorded-one; also ran red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_runtime_spec_restored_test.go`

**[INERT]** [SC-07] Sidecar log streaming routing (direct mode) [SC-07] when SidecarWriters is empty, GetLogs is still requested for the sidecar (prefix fallback path)  `JB-behavioral_runtime_spec-010`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) at FEATURE granularity only, same as row 009: container-pod.feature is in step_changes' features_affected via container_spec.go/domain.go, while the @SC-07 fallback scenario runs through the untouched brine/steps/container_lifecycle.go. Nothing on core changed Process.streamLogs, streamContainerLogsPrefixed or cop…
  - re-verified 2026-09-05 (rebase onto core): INERT — mutation atc/worker/jetbridge/process.go, (*Process).streamLogs — recorded mutation applied verbatim in intent (delete the prefix-fallback else branch so a sidecar with no dedicated writer is dropped); Add(1)…; brine RED: Then the sidecar's output is folded into the build log, labelled "[postgres]" (line 270) — status failed, error: expected the build log to mention "[postgres]", got "fake logs" (the label is what mak…; go GREEN: NONE under the recorded mutation — 'Ran 1 of 56 Specs ... SUCCESS! -- 1 Passed | 0 Failed | 0 Pending | 55 Skipped'. The spec's only assertion is `Expect(logRequested).To(BeTrue(), "expected GetLogs…; skeptic: not reached — the verifier stopped at INERT
  - **the test is restored** — `atc/worker/jetbridge/behavioral_runtime_spec_restored_test.go`

**[DELETED]** [RF-04] Additional terminal waiting states [RF-04] fails the pod immediately when a container enters a terminal waiting state / InvalidImageName  `JB-behavioral_runtime_spec-011`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Nothing on core touches the direct-mode failure ladder this pins: isPodFailedFast/terminalWaitingReasons/Process.pollUntilDone are byte-identical between aef2244a63 and 5133d0ddbc (process.go's only core change is 1164f9db3d, a new podStartupTimeout helper used by the exec-path waitForRunning). Its evidence, failure-p…

**[DELETED]** [RF-04] Additional terminal waiting states [RF-04] fails the pod immediately when a container enters a terminal waiting state / CreateContainerConfigError  `JB-behavioral_runtime_spec-012`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same as the InvalidImageName row: core changed nothing in isPodFailedFast or the terminalWaitingReasons table (no diff in process.go beyond 1164f9db3d's timeout helper, and no -S hit for terminalWaitingReasons in aef2244a63..5133d0ddbc). failure-priority.feature and its step files are absent from the rebase's step_cha…

**[DELETED]** [RF-04] Additional terminal waiting states [RF-04] fails the pod immediately when a container enters a terminal waiting state / ImagePullBackOff (existing)  `JB-behavioral_runtime_spec-013`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — isPodFailedFast, metric.Metrics.K8sImagePullFailures and RecordK8sPodFailure are unchanged on core (atc/metric/metrics.go has an empty diff for the range), and failure-priority.feature's '@RF-04' outline plus its backing steps (pod_failure.go, process.go, registry.go) were untouched by the rebase.

**[DELETED]** [RF-04] Additional terminal waiting states [RF-04] fails the pod immediately when a container enters a terminal waiting state / ErrImagePull (existing)  `JB-behavioral_runtime_spec-014`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — No core commit in aef2244a63..5133d0ddbc alters isPodFailedFast or the reason table, and the rebase changed no step file or feature backing failure-priority.feature '@RF-04' Examples row errpull. The evidence (both-red-by-hang, per the deletion commit) stands unchanged.

**[DELETED]** [RF-04] Additional terminal waiting states [RF-04] fails the pod immediately when a container enters a terminal waiting state / CrashLoopBackOff (existing)  `JB-behavioral_runtime_spec-015`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — CrashLoopBackOff handling lives in the unchanged isPodFailedFast/pollUntilDone path; core's only process.go edit (1164f9db3d) is the exec-path startup-timeout helper. failure-priority.feature and brine/steps/pod_failure.go are not in the rebase's step_changes.

**[DELETED]** [RF-09] Failure detection priority order [RF-09] reports OOMKilled rather than CrashLoopBackOff when both conditions are present  `JB-behavioral_runtime_spec-016`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The priority ladder in Process.pollUntilDone, isPodOOMKilled and writePodDiagnostics are byte-identical on core (no diff hunks in that region of process.go; no -S hits for those symbols in the range). Its evidence, failure-priority.feature 'Scenario: An OOM kill is reported ahead of the crash loop it caused', is backe…

**[DELETED]** [RF-09] Failure detection priority order [RF-09] reports ImagePullBackOff before checking exit code when both are present  `JB-behavioral_runtime_spec-017`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — isPodFailedFast and podExitCode are unchanged on core, and failure-priority.feature 'Scenario: An image pull failure is reported ahead of the exit code' is backed by brine/steps/pod_failure.go / process.go, neither of which appears in the rebase's step_changes.

**[DELETED]** [OE] Observability span events exec mode (waitForRunning span) [OE-02] emits pod.initialized span event when Initialized condition becomes True  `JB-behavioral_runtime_spec-018`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — waitForRunning did change on core (1164f9db3d) but only in how it resolves its timeout — the podEventTracker.emitPodLifecycleEvents block that emits pod.initialized is untouched, and the test's cfg comes from NewConfig, which sets a POSITIVE DefaultPodStartupTimeout, so the negative-value change cannot reach it. obser…

**[DELETED]** [OE] Observability span events exec mode (waitForRunning span) [OE-04] emits image.pulled span event when container transitions out of ContainerCreating  `JB-behavioral_runtime_spec-019`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same waitForRunning refactor caveat: 1164f9db3d touched only the timeout line, not the image.pulling/image.pulled emission, and NewConfig supplies a positive PodStartupTimeout. NewPodWatcher (pod_watcher.go) has an empty core diff, and observability.feature '@OE-04' plus brine/steps/observability.go were untouched by…

**[DELETED]** [OE] Observability span events init container failure events [OE-06] emits init.container.failed span event when init container exits non-zero  `JB-behavioral_runtime_spec-020`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The init.container.failed branch of podEventTracker.emitPodLifecycleEvents is byte-identical on core; the only process.go change in the range is the startup-timeout helper. observability.feature '@OE-06 Scenario: A failed init container is recorded as a failure, not a completion' and its backing brine/steps/observabil…

**[DELETED]** [OE-06] init.container.failed span event (dedicated test) [OE-06] emits init.container.failed event and then transitions to Running succeeds  `JB-behavioral_runtime_spec-021`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same production code and same evidence as row 020 — nothing in the emitPodLifecycleEvents init-container branch or NewPodWatcher changed on core, and no observability step file or feature changed in the rebase.

**[DELETED]** [OE-09] Observability event deduplication [OE-09] emits pod.scheduled event only once even when pod is observed in Scheduled state multiple times  `JB-behavioral_runtime_spec-022`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The podEventTracker seen-set is untouched on core (process.go's only change is podStartupTimeout/waitForRunning's timeout line), and observability.feature '@OE-09 Scenario: A condition seen twice is recorded once' plus brine/steps/observability.go are not in the rebase's step_changes.

**[DELETED]** [OE-09] Observability event deduplication [OE-09] emits sidecar.started event only once even when sidecar is observed Running multiple times  `JB-behavioral_runtime_spec-023`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Not rebase-impacted: the sidecar.started dedup branch of emitPodLifecycleEvents is unchanged on core and no observability step file or feature moved in the rebase. Separately (and unaffected by the rebase), the original assessment recorded that brine's @OE-09 scenario dedups pod.scheduled ONLY, so the sidecar half has…

**[DELETED]** [OE] Remaining observability coverage (OE-01, OE-05, OE-07, OE-08, OE-10) [OE-01] emits pod.scheduled with node.name when PodScheduled becomes True  `JB-behavioral_runtime_spec-024`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The pod.scheduled emission and its node.name attribute sit in the unchanged part of emitPodLifecycleEvents; 1164f9db3d only replaced waitForRunning's inline timeout default. observability.feature '@OE-01 Scenario: The trace records which node the step landed on' and brine/steps/observability.go were untouched by the r…

**[DELETED]** [OE] Remaining observability coverage (OE-01, OE-05, OE-07, OE-08, OE-10) [OE-05] emits init.container.completed when an init container exits 0  `JB-behavioral_runtime_spec-025`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — init.container.completed is emitted from a block core did not modify, and its evidence observability.feature '@OE-05 Scenario: A successful init container is recorded' is backed by the unchanged brine/steps/observability.go.

**[DELETED]** [OE] Remaining observability coverage (OE-01, OE-05, OE-07, OE-08, OE-10) [OE-07] emits sidecar.started with container.name when a non-main container reaches Running  `JB-behavioral_runtime_spec-026`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The sidecar.started branch (and its non-"main" name test) is unchanged on core; buildSidecarContainers has no core diff. observability.feature '@OE-07 Scenario: A sidecar coming up is recorded' and its step file are not in the rebase's step_changes.

**[DELETED]** [OE] Remaining observability coverage (OE-01, OE-05, OE-07, OE-08, OE-10) [OE-08] emits pod.phase.<phase> events on phase transitions  `JB-behavioral_runtime_spec-027`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The span.AddEvent("pod.phase."+...) line inside waitForRunning is untouched by 1164f9db3d, which changed only the function's first two lines (timeout resolution) — exactly the case the rule calls out as not impacting span-event tests. observability.feature '@OE-08 @OE-10 Scenario: An ordinary startup is timed and its…

**[DELETED]** [OE] Remaining observability coverage (OE-01, OE-05, OE-07, OE-08, OE-10) [OE-10] records K8sPodStartupDuration when the pod reaches Running  `JB-behavioral_runtime_spec-028`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Borderline but clean: 1164f9db3d changed waitForRunning's timeout resolution, while this row pins the K8sPodStartupDuration.Set at the end of the same function, and the test's config comes from NewConfig (PodStartupTimeout = DefaultPodStartupTimeout, positive), so the only behavioural delta (negative timeout now becom…

**[DELETED]** [OE] Remaining observability coverage (OE-01, OE-05, OE-07, OE-08, OE-10) [OE-10] increments K8sImagePullFailures when a container hits ImagePullBackOff  `JB-behavioral_runtime_spec-029`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — isPodFailedFast, K8sImagePullFailures.Inc and RecordK8sPodFailure are all unchanged on core (atc/metric/metrics.go diff is empty for the range), and the counterpart pod-lifecycle.feature 'Scenario: An unpullable image is counted for the operator' is backed by brine/steps/process.go, which the rebase did not touch.

**[DELETED]** [OE] Remaining observability coverage (OE-01, OE-05, OE-07, OE-08, OE-10) [OE-10] records the K8sPodFailure OTel counter with a reason attribute  `JB-behavioral_runtime_spec-030`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Not rebase-impacted: metric.RecordK8sPodFailure, InitOTelMetrics and isPodOOMKilled are byte-identical between the merge-base and core, and no brine step file or feature that could carry this moved in the rebase. Independently of the rebase, the original assessment found NO counterpart at all (no brine feature or step…

**[REFUTED]** [P3] Runtime edge cases (PE-02, PE-09, RF-14, RF-15) PE-02: direct mode command embedding [PE-02] bakes the real command into the main container (no pause pod) and counts the container  `JB-behavioral_runtime_spec-031`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — (b) scenario-level and direct: the named counterpart container-run.feature '@PE-02 Scenario: Without an exec transport the pod runs the command itself' is defined at brine/steps/container_extra.go:142 and builds its spec through runExtraSpecFromDraft (line ~950) — the exact function the rebase edited for 0d336e062b. N…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation Two mutations, one per clause of the row's test, applied separately (each reverted before the next). M1 (command-embedding clause) — atc/worker/jetbridge/container.go, (*Container).Run, line 115:; brine RED: M1: step 'Then the pod itself carries the step's command' (line 34) — status failed, error: "expected the pod to run [/bin/sh -c echo hello] itself, it runs [sh -c trap 'exit 0' TERM; sleep 86400 & w…; go RED: M1 (behavioral_runtime_spec_test.go:1796): "[FAILED] Expected <[]string | len:3, cap:3>: [ \"sh\", \"-c\", \"trap 'exit 0' TERM; sleep 86400 & wait\", ] to equal <[]string | len:1, cap:1>: [\"/opt/re…; skeptic: different-behaviour pairing, via a narrower mutation that breaks only what the Go It asserts (plus a red-by-adaptation control and a reproduction of the verifier's M1) → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/behavioral_runtime_spec_restored_test.go`

**[DELETED]** [P3] Runtime edge cases (PE-02, PE-09, RF-14, RF-15) PE-09: direct mode process completion [PE-09] streams pod logs to Stdout, returns the main container exit code, and deletes the pod  `JB-behavioral_runtime_spec-032`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — podExitCode, Process.Wait/pollUntilDone, streamContainerLogsMain and the pod Delete are all byte-identical on core, and the counterpart pod-lifecycle.feature '@PE-09 Scenario: A step whose pod succeeds reports success and leaves nothing behind' is backed by brine/steps/process.go, which is not among the rebase's seven…

**[DELETED]** [P3] Runtime edge cases (PE-02, PE-09, RF-14, RF-15) PE-09: direct mode process completion [PE-09] defaults to exit 0 for PodSucceeded with no main container status  `JB-behavioral_runtime_spec-033`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The phase-only fallback in podExitCode is unchanged on core, and its counterpart failure-priority.feature 'Scenario: A succeeded pod with no container status still passes the step' is backed by step files the rebase left alone.

**[DELETED]** [P3] Runtime edge cases (PE-02, PE-09, RF-14, RF-15) PE-09: direct mode process completion [PE-09] defaults to exit 1 for PodFailed with no main container status  `JB-behavioral_runtime_spec-034`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same unchanged podExitCode fallback; core made no edit to that region of process.go and the counterpart failure-priority.feature 'Scenario: A failed pod with no container status still fails the step' sits in an untouched feature/step pair.

**[DELETED]** [P3] Runtime edge cases (PE-02, PE-09, RF-14, RF-15) RF-14: init container failure reporting [RF-14] returns an error with the failed init container's name, state, and retrieved logs  `JB-behavioral_runtime_spec-035`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — This pins waitForRunning's terminal-phase branch ("pod terminated before exec could run" plus init-container name/state/logs), which 1164f9db3d did not touch — that commit changed only the function's timeout resolution, and NewConfig gives a positive PodStartupTimeout. Its counterpart observability.feature '@RF-14 Sce…

**[DELETED]** [P3] Runtime edge cases (PE-02, PE-09, RF-14, RF-15) RF-15: exec mode failure context [RF-15] writes both pod and node diagnostics to stderr when an exec operation fails  `JB-behavioral_runtime_spec-036`
  - recorded evidence: PER-FILE (deletion commit `49969fd6c4`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — fetchPodFailureContext, writePodDiagnostics, writeNodeDiagnostics and fetchPodNodeName are unchanged on core (no diff hunks, no -S hits in aef2244a63..5133d0ddbc), and the counterpart pod-lifecycle.feature '@RF-15' scenarios are backed by brine/steps/process.go, absent from the rebase's step_changes.


## integration_test.go

Deleted by `dbc96d3e6d` — "Delete integration_test.go; record the storage-backend blind spot again". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

Restored tests from this file live in `atc/worker/jetbridge/integration_restored_test.go`.

**[REFUTED]** Integration simple task pipeline runs a task step end-to-end: create container → run → wait → exit  `JB-integration-000`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — The evidence for this test's pause-pod clauses is container-run.feature:84 "With an exec transport the pod is a placeholder the step runs inside", and container-run.feature's step definitions had to change in the rebase (steps/container_extra.go runExtraSpecFromDraft now sets TaskCacheIdentity, backed by the new Conta…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func (*Container).createPausePod (line 394):; brine RED: And the pod is a placeholder, not the step's command (line 88) — error: expected the pod NOT to carry the step's command, its entrypoint is "/bin/sh -c echo hello" — the step was baked into the pod i…; go RED: atc/worker/jetbridge/integration_test.go:102 — [FAILED] Expected <[]string | len:3, cap:3>: [ "/bin/sh", "-c", "echo hello world && exit 0", ] to equal <[]string | len:3, cap:3>: [ "sh", "-c", "trap…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts), plus red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/integration_restored_test.go`

**[DELETED]** Integration simple task pipeline handles task failure with non-zero exit code  `JB-integration-001`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Its evidence is task-command.feature:49 "A failing command's exit code reaches the consumer" and steps/task_command.go; `git diff e04145c3f9 f4bd28114c -- atc/worker/jetbridge/brine/` shows neither changed in the rebase. Its source functions (ExecExitError unwrapping in execProcess.Wait, createPausePod, annotateExitSt…

**[DELETED]** Integration task reattach after web restart re-execs the identical supervisor command against the surviving pod  `JB-integration-002`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Its evidence is step-closing.feature "A web that restarted mid-step reuses the pod instead of scheduling a second one", backed by steps/closing.go — neither is in the rebase's brine diff. Core touched none of Container.Run's pod-reuse branch, Container.Attach's "no completion status" branch, supervisorCommand, shellQu…

**[DELETED]** Integration pipeline with get/put resources runs a get step followed by a put step with the resource protocol  `JB-integration-003`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Its evidence is step-integration.feature's "A get step's request reaches its resource and the answer comes back" / "A put step's inputs are mounted where its resource expects them", backed by steps/integration.go — neither changed in the rebase. Config.ResourceTypeImages/MergeResourceTypeImages are untouched (config.g…

**[DELETED]** Integration build cancellation returns an error when the context is cancelled during exec-mode task  `JB-integration-004`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Its evidence is step-closing.feature's @PE-10 "A cancelled step leaves its pod behind so an operator can hijack it", backed by steps/closing.go — neither changed in the rebase. wrapIfTransient, fetchPodFailureContext and preferContextCancellation are byte-identical merge-base→core (git log -S returns nothing), and the…

**[DELETED]** Integration build cancellation returns an error when the context is cancelled during an exec-mode resource step  `JB-integration-005`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same evidence and same production surface as its task twin: step-closing.feature @PE-10 plus steps/closing.go, neither changed in the rebase; execProcess.Wait's non-ExecExitError branch, wrapIfTransient and fetchPodFailureContext are unchanged on core.

**[DELETED]** Integration pipeline failure modes detects ImagePullBackOff in a task step and returns a diagnostic error  `JB-integration-006`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Its evidence is failure-priority.feature:21/:31 and pod-lifecycle.feature's diagnostics scenarios, backed by steps/pod_failure.go — none of those files appear in the rebase's brine diff. isPodFailedFast, writePodDiagnostics, NewPodWatcher and metric.RecordK8sPodFailure are untouched on core; waitForRunning's only edit…

**[DELETED]** Integration pipeline failure modes detects pod eviction in a resource get step and returns a diagnostic error  `JB-integration-007`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Its evidence is step-closing.feature "An evicted step is a retryable interruption, not a failed build" plus pod-lifecycle.feature:99; neither those features nor steps/closing.go / steps/pod_failure.go changed in the rebase. interruptionErrorForPod, interruptionReasonForPod, isPodEvicted, preferContextCancellation, wri…

**[DELETED]** Integration pipeline failure modes detects CrashLoopBackOff in a task step during waitForRunning  `JB-integration-008`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Its evidence is failure-priority.feature:31 "A terminal waiting state fails the pod immediately" plus pod-lifecycle.feature, backed by steps/pod_failure.go — unchanged in the rebase. It pins waitForRunning's ORDERING (terminal-state check before the Running check); core's only waitForRunning change (1164f9db3d) replac…

**[DELETED]** Integration cache volume lifecycle LookupVolume returns DaemonSetVolume  `JB-integration-009`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Its evidence is step-integration.feature "A looked-up artifact still carries its database row" (the disposition explicitly declines the Go-type assertion), and that feature's steps did not change in the rebase. Every production file it exercises is byte-identical merge-base→core: worker.go (LookupVolume, SetVolumeRepo…

**[DELETED]** Integration input/output passing between steps mounts input volumes from a get step and output volumes for a task  `JB-integration-010`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — Its evidence is container-pod.feature:15 "A step sees its working directory and every input" plus the output/scratch siblings at :32/:43/:57, and container-pod.feature's shared "the container runs" builder in steps/container_spec.go changed in the rebase (its runtime.ContainerSpec literal now carries TaskCacheIdentity…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts — deleted the working-directory arm (the first block of the function), so a step with a Dir gets no volume/mount at its working direc…; brine RED: Then the pod has 3 volumes (line 22) — status "failed", error: expected 3 volumes, found 2: [input-0 input-1]; go RED: integration_test.go:533 — [FAILED] Expected <[]v1.VolumeMount | len:4, cap:4>: [ {Name: "input-0", MountPath: "/tmp/build/workdir/source-code"}, {Name: "input-1", MountPath: "/tmp/build/workdir/ci"},…; skeptic: Four attacks run, none refuted: (1) different-behaviour pairing via a NARROWER honest mutation aimed at the half of the Go assertion the named scenario does not exercise (multi-output); (2) unrelated… → HOLDS

**[REFUTED]** Integration input/output passing between steps passes inputs from a get step to a put step via volume mounts  `JB-integration-011`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — Same evidence as its sibling — container-pod.feature:15 "A step sees its working directory and every input" — and that feature's step definitions (steps/container_spec.go's "the container runs" DefineMap, plus the new ContainerDraft.taskCacheIdentity in steps/domain.go) changed in the rebase. Its production surface (b…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts — the exact recorded mutation, applied unchanged:; brine RED: Then the pod has 3 volumes (line 22) — status failed, error: `expected 3 volumes, found 1: [dir-0]`. The five preceding steps (Given a jetbridge worker on a fake Kubernetes cluster / And a task conta…; go RED: integration_test.go:598 — `[FAILED] Expected <[]v1.VolumeMount | len:0, cap:0>: nil to have length 2` (during STEP "verifying input volumes are mounted in the pause Pod"; the preceding STEP "creating…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts), plus red-by-adaptation and unrelated-brine-red checks → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/integration_restored_test.go`

**[REFUTED]** Integration task with sidecar containers creates a pod with sidecars that share volume mounts and runs the task via exec  `JB-integration-012`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — Its evidence is container-pod.feature's sidecar scenarios at :139/:146/:171/:180, and container-pod.feature's shared "the container runs" builder in steps/container_spec.go (with steps/domain.go's new taskCacheIdentity helper) had to change after the rebase. buildSidecarContainers, buildSidecarResourceRequirements, st…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go :: buildSidecarContainers (line 536); brine RED: And the sidecar "postgres" sees the same volumes as the step (line 159) — status failed, error: the step sees "/tmp/build/workdir" but sidecar "postgres" does not. Run 01M1SE7PR7KP5MHB55ARJCJFSX: run…; go RED: integration_test.go:687 — Expect(sidecar.VolumeMounts).To(Equal(mainMounts)). Verbatim: "[FAILED] Expected <[]v1.VolumeMount | len:0, cap:0>: nil to equal <[]v1.VolumeMount | len:2, cap:2>: [{Name: \…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts), plus red-by-adaptation and unrelated-brine-red as controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/integration_restored_test.go`

**[DELETED]** Integration task with sidecar containers runs a task with multiple sidecars  `JB-integration-013`
  - recorded evidence: PER-FILE (deletion commit `dbc96d3e6d`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — Its evidence is container-pod.feature:159 "Several sidecars all run" (plus :139/:146/:171/:180), and container-pod.feature's step definitions changed in the rebase (steps/container_spec.go's ContainerSpec literal gained TaskCacheIdentity, backed by the new steps/domain.go helper). Nothing on core touched buildSidecarC…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func buildSidecarContainers (declared line 536; loop header line 544):; brine RED: Then the pod runs 3 containers (line 169) — error: "expected 3 containers, found 2: [main postgres]"; go RED: integration_test.go:733 — [FAILED] Expected <[]string | len:2, cap:2>: ["main", "postgres"] to contain elements <[]string | len:3, cap:3>: ["main", "postgres", "redis"] the missing elements were <[]s…; skeptic: Three attacks, all run: (1) narrower mutation on a different dimension — rename only the SIDECAR containers, leaving the count at 3, to test whether brine's failing step is a count-only proxy for the… → HOLDS


## podname_integration_test.go

Deleted by `e472c0f32e` — "Delete podname_integration_test.go; audit the pre-fix brine evidence". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** Pod Name Integration Run creates pod with readable name uses GeneratePodName when metadata has pipeline/job/build  `JB-podname_integration-000`
  - recorded evidence: PER-FILE (deletion commit `e472c0f32e`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Nothing on core touches the pod-naming path: podname.go (GeneratePodName/sanitizeSegment/hexSuffix) and worker.go are byte-identical merge-base->core, and container.go's only three changed symbols (buildArtifactInitContainers, stableCacheKey, buildVolumeMounts) are all task-cache-scoped while this spec declares no Cac…

**[DELETED]** Pod Name Integration Run creates pod with readable name falls back to handle when metadata has no pipeline/job  `JB-podname_integration-001`
  - recorded evidence: PER-FILE (deletion commit `e472c0f32e`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins GeneratePodName's fallback branch (empty PipelineName/JobName -> return handle) in podname.go, which is byte-identical between aef2244a63 and 5133d0ddbc; `git log -SGeneratePodName aef2244a63..5133d0ddbc -- atc/worker/jetbridge/` is empty. The spec carries no caches so container.go's cache-only edits cannot reach…

**[DELETED]** Pod Name Integration Run creates pod with readable name uses readable name in exec mode too  `JB-podname_integration-002`
  - recorded evidence: PER-FILE (deletion commit `e472c0f32e`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Exec-mode naming via createPausePod/buildPod, neither of which appears in the core changed-symbol map nor in any -S grep over aef2244a63..5133d0ddbc. process.go did change (1164f9db3d) but only by extracting podStartupTimeout and altering NEGATIVE PodStartupTimeout handling; NewConfig sets PodStartupTimeout = DefaultP…

**[DELETED]** Pod Name Integration Attach looks up pod by podName finds the pod using the readable name, not the handle  `JB-podname_integration-003`
  - recorded evidence: PER-FILE (deletion commit `e472c0f32e`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins Container.Attach's cached-exit-status short-circuit and exitedProcess.Wait. The full `git diff aef2244a63 5133d0ddbc -- atc/worker/jetbridge/container.go` is three hunks (buildArtifactInitContainers error return, stableCacheKey, buildVolumeMounts cache switch); Attach is not among them and `-S'func (c *Container)…

**[DELETED]** Pod Name Integration Attach looks up pod by podName looks up pod by podName when no exit status is cached  `JB-podname_integration-004`
  - recorded evidence: PER-FILE (deletion commit `e472c0f32e`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins Attach's `attach: pod %q not found` wrap naming c.podName rather than the handle. Attach and the podName field are unchanged on core (container.go diff is cache-only; `-S podName -- atc/worker/jetbridge/ atc/db/` over the range returns nothing). Counterpart step-integration.feature:161 and steps/integration.go ar…

**[DELETED]** Pod Name Integration Pod labels include rich metadata adds pipeline, job, build, step, and handle labels  `JB-podname_integration-005`
  - recorded evidence: PER-FILE (deletion commit `e472c0f32e`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins buildPodLabels/buildPodAnnotations output. Neither symbol is in core_changed_symbols.json for container.go, and `-S buildPodLabels`, `-S addLabel` and `-S 'concourse.ci/pipeline'` over aef2244a63..5133d0ddbc (jetbridge + db) all return no commits. Counterpart step-integration.feature:171 (@PN-07) verified present…

**[DELETED]** Pod Name Integration Pod labels include rich metadata omits empty metadata labels  `JB-podname_integration-006`
  - recorded evidence: PER-FILE (deletion commit `e472c0f32e`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins addLabel's `if value != ""` skip inside buildPodLabels — untouched on core by the same evidence as row 005 (no changed symbol, no -S hit, container.go's three hunks are cache-only). Counterpart step-integration.feature:185 (@PN-07) untouched by the rebase.

**[DELETED]** Pod Name Integration Pod labels include rich metadata truncates label values to 63 chars (K8s label limit)  `JB-podname_integration-007`
  - recorded evidence: PER-FILE (deletion commit `e472c0f32e`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the 63-char clamp in addLabel/buildPodLabels; that code is byte-identical on core (container.go's only changes are buildArtifactInitContainers, stableCacheKey and buildVolumeMounts). Counterpart step-integration.feature:198 (@PN-07) and steps/integration.go were not changed by the rebase.

**[DELETED]** Pod Name Integration Volume binding uses podName binds volumes to the readable pod name after Run  `JB-podname_integration-008`
  - recorded evidence: PER-FILE (deletion commit `e472c0f32e`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — Conservative (b) hit: this row's brine_counterparts name atc/worker/jetbridge/brine/features/container-run.feature (the @CO-04 pair "A volume handed back before the step runs knows no pod" / "Running the step is what binds its volumes to a pod"), and container-run.feature is in features_affected of the rebase's contai…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).Run — exec-mode branch, line 180:; brine RED: Then the mount at "/tmp/build/workdir/my-input" reads from the pod the step created -> expected the mount at "/tmp/build/workdir/my-input" to read from the pod "my-pipeline-unit-test-b42-task-550e840…; go RED: podname_integration_test.go:396 — STEP: pod name is the readable name after Run, not the UUID handle; [FAILED] Expected <string>: 550e8400-e29b-41d4-a716-446655440000 to match regular expression <str…; skeptic: Four attacks run, all measured: (1) red-by-adaptation — restored Go test run clean before any mutation; (2) unrelated/flaky brine red — clean baseline run of the whole feature; (3) order-masked asser… → HOLDS


## podname_test.go

Deleted by `ed2e073a6b` — "Delete podname_test.go; close the three naming gaps it was hiding". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** GeneratePodName build step containers produces <pipeline>-<job>-b<build>-<type>-<suffix> format  `JB-podname-000`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The whole naming family is untouched by core: `git diff --stat aef2244a63 5133d0ddbc -- atc/worker/jetbridge/podname.go` is empty (byte-identical), as is atc/db/container_metadata.go which defines db.ContainerMetadata and db.ContainerTypeTask, and `git log -S` over the full tree for GeneratePodName/sanitizeSegment/hex…

**[DELETED]** GeneratePodName build step containers includes step name for get and put types  `JB-podname-001`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the get/put type segment of GeneratePodName's build-step branch; podname.go is byte-identical merge-base to core and db.ContainerTypeGet/Put live in the also-unchanged atc/db/container_metadata.go. The brine rows that carry this ("A pod is named after its step — get / put") sit in pod-naming.feature, which the re…

**[DELETED]** GeneratePodName build step containers uses the first 8 hex chars of the handle as the suffix  `JB-podname-002`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — hexSuffix is unchanged on core — podname.go has an empty diff aef2244a63..5133d0ddbc and `git log -ShexSuffix aef2244a63..5133d0ddbc` is empty. Its brine counterpart "The handle supplies a disambiguating suffix — standard uuid" is in the untouched pod-naming.feature.

**[DELETED]** GeneratePodName sanitization lowercases all characters  `JB-podname-003`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — strings.ToLower inside sanitizeSegment is unchanged on core (podname.go byte-identical; no -SsanitizeSegment commits in aef2244a63..5133d0ddbc). Its evidence is BRINE-ONLY-RED — the Go assertion was weaker than its name — and the brine row that supplies the redness ("A hostile name is sanitized — uppercase", with the…

**[DELETED]** GeneratePodName sanitization replaces underscores, dots, and spaces with hyphens  `JB-podname-004`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The three strings.ReplaceAll calls in sanitizeSegment are untouched by core (empty podname.go diff). The positive-form brine assertion this row depends on (`the pod name reads "<reads>"`, added on-branch by 3cda504d20) is in pod-naming.feature and brine/steps/podname.go, both byte-identical across the rebase.

**[DELETED]** GeneratePodName sanitization strips non-alphanumeric non-hyphen characters  `JB-podname-005`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — nonAlphanumHyphen (`[^a-z0-9-]`) lives in podname.go, which core did not modify; `git log -SnonAlphanumHyphen aef2244a63..5133d0ddbc` is empty. Its brine row "A hostile name is sanitized — punctuation" is in the unchanged pod-naming.feature.

**[DELETED]** GeneratePodName sanitization collapses consecutive hyphens  `JB-podname-006`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — multiHyphen (`-{2,}`) is in the byte-identical podname.go and no core commit between aef2244a63 and 5133d0ddbc contains the symbol. Its brine row "A hostile name is sanitized — doubled hyphens" is untouched by the rebase.

**[DELETED]** GeneratePodName truncation truncates pipeline and job segments to 20 chars each  `JB-podname-007`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The recorded gap here (the maxEach = available/2 budget split being deletable without reddening this spec) is a pre-existing weakness of the Go test, not rebase drift: GeneratePodName's budget arithmetic and maxPodNameLen are in the byte-identical podname.go, and the scenarios that close the gap ("One long segment can…

**[DELETED]** GeneratePodName truncation does not exceed 63 chars total (DNS label safe)  `JB-podname-008`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched budget arithmetic and maxPodNameLen in the byte-identical podname.go; the gap named against this row (build "999999" is too short for the split to matter) predates the rebase. Both brine counterparts — "A very long pipeline and job still fit in a DNS label" and the added "A long build number cannot push…

**[DELETED]** GeneratePodName truncation does not end with a hyphen after truncation  `JB-podname-009`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — BRINE-ONLY-RED / vacuous Go assertion, and nothing on core disturbs that finding: the strings.TrimRight in sanitizeSegment is in the byte-identical podname.go, and the brine scenario that actually forbids a trailing hyphen ("Truncation does not leave a trailing hyphen", via the dnsLabel regexp `^[a-z0-9]([-a-z0-9]*[a-…

**[DELETED]** GeneratePodName fallback for missing metadata falls back to UUID-only when pipeline and job are empty (fly execute)  `JB-podname-010`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The build-step guard at podname.go:63 is unchanged on core (empty file diff, no -SGeneratePodName commits). The brine row "Without metadata the handle is the name — task, no metadata" is in the untouched pod-naming.feature, so the recorded equivalence note about flipping || to && still applies as written.

**[DELETED]** GeneratePodName fallback for missing metadata falls back to UUID-only when metadata is completely empty  `JB-podname-011`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same unchanged guard in the byte-identical podname.go; db.ContainerMetadata's zero value is defined in atc/db/container_metadata.go, which also has an empty diff aef2244a63..5133d0ddbc. Its brine counterpart is in the unmodified pod-naming.feature.

**[DELETED]** GeneratePodName check containers uses chk-<step-name>-<suffix> format  `JB-podname-012`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The check branch (podname.go:35-46) is untouched by core — the file is byte-identical and db.ContainerTypeCheck's home, atc/db/container_metadata.go, is too. The brine row "A pod is named after its step — check" is in pod-naming.feature, unchanged by the rebase.

**[DELETED]** GeneratePodName check containers truncates long resource names in check format  `JB-podname-013`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — maxResource = maxPodNameLen - 13 in the check branch is in the byte-identical podname.go and no core commit contains maxPodNameLen. The provenance note (this behaviour was migrated late, as "A check on a very long resource name still fits") concerns branch history, not the rebase; that scenario is present and unchange…

**[DELETED]** GeneratePodName check containers falls back to UUID for check with no step name  `JB-podname-014`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The `if metadata.StepName == "" { return handle }` arm at podname.go:36-38 is unchanged on core (empty podname.go diff). Its brine row "Without metadata the handle is the name — check, no step" is in the untouched pod-naming.feature.

**[DELETED]** GeneratePodName resource type operation containers uses rt-<step-name>-<type>-<suffix> for get steps with no job context  `JB-podname-015`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The resource-type branch (podname.go:51-60) is in the byte-identical podname.go; the `-Srt-` hits in the core log (fcdf440662, 1164f9db3d, 1e023e7ca4 and others) are substring matches on unrelated tokens such as "cert-"/"start-", none touching podname.go, which has an empty diff. The brine row "A pod is named after it…

**[DELETED]** GeneratePodName resource type operation containers uses rt- format when only StepName is available (no pipeline)  `JB-podname-016`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched resource-type branch in the byte-identical podname.go, and the brine row "A pod is named after its step — get, step only" sits in pod-naming.feature, which is not in the rebase's step_changes or its one feature expectation change.

**[DELETED]** GeneratePodName resource type operation containers falls back to UUID when StepName is empty and no job context  `JB-podname-017`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Both guards this row falls through (the resource-type `metadata.StepName != ""` at podname.go:51 and the build-step guard at :63) are in the byte-identical podname.go. Its brine counterpart "Without metadata the handle is the name — get, no step" is in the unmodified pod-naming.feature.

**[DELETED]** GeneratePodName handle suffix extraction strips hyphens from UUID to extract hex suffix  `JB-podname-018`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — hexSuffix's strings.ReplaceAll(handle, "-", "") is unchanged on core (empty podname.go diff; no -ShexSuffix commits in aef2244a63..5133d0ddbc). Its brine row "The handle supplies a disambiguating suffix — hyphens stripped" is untouched by the rebase.

**[DELETED]** GeneratePodName handle suffix extraction handles short handles gracefully  `JB-podname-019`
  - recorded evidence: PER-FILE (deletion commit `ed2e073a6b`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The `if len(hex) > 8` guard at podname.go:138-141 is in the byte-identical podname.go and no core commit between the merge-base and 5133d0ddbc mentions hexSuffix. Its brine row "The handle supplies a disambiguating suffix — handle under 8" is in the unchanged pod-naming.feature.


## registrar_test.go

Deleted by `970b33791c` — "Delete registrar_test.go; close the volumes/start-time gap it was holding". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** Registrar Register saves a worker to the database with the correct attributes  `JB-registrar-000`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The whole of atc/worker/jetbridge/registrar.go (Register, WorkerName, countActivePods, resourceTypes, heartbeatTTL) is byte-identical between aef2244a63 and 5133d0ddbc (md5 e6857c11c27aefadf64d9293e7464c95 at both revs, and `git log aef2244a63..5133d0ddbc -- registrar.go` is empty), as are atc/db/worker_factory.go, at…

**[DELETED]** Registrar Register reports active containers by counting Pods in the namespace  `JB-registrar-001`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — countActivePods and workerLabelKey live in registrar.go, which is byte-identical on core, and `git log -ScountActivePods`/`-SworkerLabelKey` over aef2244a63..5133d0ddbc returns nothing; db.Worker.ActiveContainers is unchanged (atc/db/worker.go empty-diff). The counterpart WR-04 Scenario Outline "The container count re…

**[DELETED]** Registrar Register only counts Pods with the worker label  `JB-registrar-002`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The label-selector discrimination is entirely inside the unchanged registrar.go (countActivePods + workerLabelKey), and no core commit in the range touches either symbol. Its counterpart, the WR-04 example "unlabelled bystander" (1/1/1), sits in the untouched worker-registration.feature backed by the untouched steps/r…

**[DELETED]** Registrar Register counts multiple labelled Pods  `JB-registrar-003`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same unchanged countActivePods in the byte-identical registrar.go; the len(pods.Items) cardinality it pins is not touched by any core commit in aef2244a63..5133d0ddbc. Counterpart WR-04 example "several pods" (3/0/3) is in the untouched worker-registration.feature.

**[DELETED]** Registrar Register reports zero active containers when no Pods exist  `JB-registrar-004`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The empty-list-is-not-an-error path is in the byte-identical registrar.go, and db.Worker.ActiveContainers is unchanged. Counterpart WR-04 example "nothing running" (0/0/0) is in the untouched worker-registration.feature; neither that feature nor steps/registrar.go appears in the rebase's 8 changed brine files.

**[DELETED]** Registrar Register propagates SaveWorker errors  `JB-registrar-005`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The "saving worker" wrap is in the byte-identical registrar.go, and the whole error path underneath it is unchanged on core: atc/db/worker_factory.go (NewWorkerFactory, SaveWorker) and atc/db/worker_cache.go (NewStaticWorkerCache) both produce an empty `git diff aef2244a63 5133d0ddbc`, and `git log -SSaveWorker -- atc…

**[DELETED]** Registrar Heartbeat calls SaveWorker to refresh the TTL  `JB-registrar-006`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Registrar.Heartbeat and the heartbeatTTL const are in the byte-identical registrar.go (`git log -SheartbeatTTL -- atc/` over the range is empty), and db.Worker.ExpiresAt/Reload are unchanged. The TTL-ceiling closure this row depends on — WR-03 "A heartbeat renews a lease that has run out" plus the step "its lease expi…

**[DELETED]** Registrar ResourceTypes registration registers default resource types when no overrides are set  `JB-registrar-007`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — config.go IS in the core changed-symbols map, but its change is +54/-0 and confined to the artifact-resolve-capability region (new consts DefaultArtifactResolveCapabilityTTL/ArtifactResolveInitRetryBudget/artifactResolveExpirySafetyMargin, new Config fields, new MinimumArtifactResolveCapabilityTTL); the DefaultResourc…

**[DELETED]** Registrar ResourceTypes registration registers custom resource types when overrides are set  `JB-registrar-008`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — MergeResourceTypeImages is md5-identical (7793bfb1badf240bb550acfda3cd13d1) between aef2244a63 and 5133d0ddbc — core's only edit to config.go was additive and elsewhere in the file — and `git log -SMergeResourceTypeImages -- atc/` over the range is empty; Config.ResourceTypeImages is untouched. Counterparts WR-06 "An…

**[DELETED]** Registrar WorkerName returns a deterministic name based on the namespace  `JB-registrar-009`
  - recorded evidence: PER-FILE (deletion commit `970b33791c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Both symbols are byte-identical on core: Registrar.WorkerName in the unchanged registrar.go and NewConfig in config.go (md5 90c44f35d2dcd70e2798e8bd8330e25d at both revs, core's config.go change being additive only), with `git log -SWorkerName -- atc/worker/jetbridge/` empty over the range. The transitive assertion th…


## reaper_test.go

Deleted by `d31fea6922` — "Delete reaper_test.go; close the three places it was stronger than brine". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** Reaper container reporting reports active pod handles to UpdateContainersMissingSince  `JB-reaper-000`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Both source files this pins — atc/worker/jetbridge/reaper.go (Reaper.Run label-selector + handle-collection loop) and atc/db/container_repository.go (UpdateContainersMissingSince) — are byte-identical merge-base→core (`git diff --stat aef2244a63 5133d0ddbc` empty), NewConfig is character-for-character unchanged, and n…

**[DELETED]** Reaper container reporting deletes the DB rows of destroying containers no pod reports, on this worker only  `JB-reaper-001`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The whole chain it pins is unchanged on core: reaper.go, atc/gc/destroyer.go (Destroyer.DestroyContainers), container_repository.go (RemoveDestroyingContainers) and atc/metric/metrics.go (ContainersDeleted) all diff empty against the merge-base, and `git log -S` finds no commit for DestroyContainers, RemoveDestroyingC…

**[DELETED]** Reaper container reporting reports empty handles when no pods exist  `JB-reaper-002`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The `make([]string, len(remainingPods))` empty-not-nil construction lives in reaper.go, which core never touched, and UpdateContainersMissingSince in container_repository.go is likewise unchanged. No core commit and no rebase step change names either.

**[DELETED]** Reaper container reporting calls DestroyUnknownContainers with active pod handles to catch orphans  `JB-reaper-003`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — DestroyUnknownContainers is in atc/db/container_repository.go, which is byte-identical merge-base→core, as is reaper.go's call site; `git log -S DestroyUnknownContainers aef2244a63..5133d0ddbc` returns nothing. The rebase changed no reaper step file or pod-reaping scenario.

**[DELETED]** Reaper pod reaping deletes pods that are in 'destroying' state in the DB  `JB-reaper-004`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — FindDestroyingContainers (container_repository.go) and the destroying-delete loop (reaper.go) are both in files core left byte-identical, and the same-sweep-survival claim this test pins is a property of that untouched loop. pod-reaping.feature — where the correction it forced is recorded — is identical between e04145…

**[DELETED]** Reaper pod reaping removes the DB row of a destroying container whose pod is already gone  `JB-reaper-005`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — reaper.go, gc/destroyer.go, container_repository.go (RemoveDestroyingContainers) and metric/metrics.go (ContainersDeleted) are all unchanged on core, so both the row-removal and the delta-of-1 it pins are untouched behaviour.

**[DELETED]** Reaper pod reaping does not fail when a pod vanishes between the list and the delete  `JB-reaper-006`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The `!apierrors.IsNotFound(err)` guard is in reaper.go, byte-identical merge-base→core. The evidence that closed this blocker — steps/reaper_gaps.go and the scenario 'A pod deleted by someone else mid-sweep does not fail the reaper' in pod-reaping.feature — survives the rebase unmodified (both files diff empty e04145c…

**[DELETED]** Reaper pod reaping does nothing when no containers are in destroying state  `JB-reaper-007`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The empty-FindDestroyingContainers path lives entirely in unchanged reaper.go and unchanged container_repository.go; no core commit in the range touches either symbol, and no rebase step change names pod-reaping.

**[DELETED]** Reaper completed pod reaping keeps a completed step's pod while its build is still running  `JB-reaper-008`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — splitCompletedPods and the `remainingPods = append(remainingPods, retained...)` re-report are in reaper.go (unchanged on core), and RunningBuildLookup.GetAllStartedBuilds is in atc/db/build_factory.go, which is also byte-identical merge-base→core. The blocker-1 evidence (steps/reaper_gaps.go plus 'A pod kept for a run…

**[DELETED]** Reaper completed pod reaping reaps a completed step's pod once its build is no longer running  `JB-reaper-009`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — This is the only reaper row whose source_functions include a symbol core changed — (*build).Finish in atc/db/build.go (bb0b6ea126, f7cf071454) — but both additions (the `SELECT pipeline_run_id` re-read plus lockPipelineRun, and the attemptRunCompletion/ComponentReclaimerPipelineRuns notify) are gated on `runID.Valid`,…

**[DELETED]** Reaper completed pod reaping fast-reaps a completed check pod, which has no build to resume  `JB-reaper-010`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The ownedByBuild scan over buildIDLabelKey and the completed-pod delete loop are both in reaper.go, byte-identical merge-base→core; `git log -S buildIDLabelKey` over atc/worker/jetbridge finds no commit in the range. No rebase change touches pod-reaping.

**[DELETED]** Reaper completed pod reaping keeps completed pods when the running-build set cannot be read  `JB-reaper-011`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The GetAllStartedBuilds error branch is in unchanged reaper.go over unchanged atc/db/build_factory.go, and the test's helper closedJetbridgeCloneConn lives in atc/worker/jetbridge/jetbridge_suite_test.go, which is itself byte-identical merge-base→core. The closing evidence (ReaperLookupFailureDefinitions in steps/reap…

**[DELETED]** Reaper completed pod reaping keeps completed pods when no build lookup is configured  `JB-reaper-012`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The `if r.buildLookup == nil` branch and NewReaper are in reaper.go, unchanged on core; NewConfig in config.go is identical too (config.go's +54 lines are all new artifact-daemon resolve-capability consts and fields, none reachable from the reaper).

**[DELETED]** Reaper completed pod reaping still deletes a retained pod through the DB destroying path  `JB-reaper-013`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The handleToPodName map, the retain path in splitCompletedPods and the destroying-delete loop are all in unchanged reaper.go; CreatedContainer.Destroying (atc/db/container.go) and FindDestroyingContainers (container_repository.go) are in files core left byte-identical. The rebase left steps/reaper_gaps.go and pod-reap…

**[DELETED]** Reaper reaper idempotency is safe to run twice when first run already deleted the pod  `JB-reaper-014`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Two consecutive Reaper.Run sweeps over unchanged reaper.go, gc/destroyer.go, container_repository.go and metric/metrics.go; the ContainersDeleted delta this pins depends on no symbol core touched in aef2244a63..5133d0ddbc.

**[DELETED]** Reaper reaper idempotency does not destroy a newly created pod that is not marked destroying  `JB-reaper-015`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Both the report-both-rows-first behaviour (UpdateContainersMissingSince) and the destroying-only delete loop live in files core left byte-identical (container_repository.go, reaper.go). No rebase step change names pod-reaping.

**[DELETED]** Reaper readable pod names with handle labels reports DB handles (from labels) not pod names to UpdateContainersMissingSince  `JB-reaper-016`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The handle-resolution loop and handleLabelKey are in reaper.go, unchanged on core, and `git log -S handleLabelKey aef2244a63..5133d0ddbc -- atc/worker/jetbridge` is empty; UpdateContainersMissingSince is likewise untouched.

**[DELETED]** Reaper readable pod names with handle labels deletes pods by pod name when DB returns handles for destruction  `JB-reaper-017`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The `podName := handle; if name, ok := handleToPodName[handle]` resolution is in unchanged reaper.go; CreatedContainer.Destroying and FindDestroyingContainers are in unchanged atc/db files. The blocker-2 replacement scenario 'A destroyed container's readable pod is deleted by its pod name' in pod-reaping.feature is by…

**[DELETED]** Reaper readable pod names with handle labels falls back to pod name when handle label is missing (backward compat)  `JB-reaper-018`
  - recorded evidence: PER-FILE (deletion commit `d31fea6922`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The `handle := podMeta.Name` fallback is in reaper.go and the assertion lands on UpdateContainersMissingSince in container_repository.go — both files byte-identical merge-base→core, with no matching commit in the range and no rebase change to any reaper brine artifact.


## container_test.go

Deleted by `c67193f78f` — "Delete container_test.go; give brine a worker that actually has a backend". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

Restored tests from this file live in `atc/worker/jetbridge/container_restored_test.go`.

**[REFUTED]** Container Run creates a Pod with the correct image, command, args, and env  `JB-container-000`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): container_test.go is a rebase conflict file, modified on core by 0d336e062b and resolved by taking the branch's deletion. Rule (b): its evidence names container-run.feature, container-spec.feature and container-pod.feature, all three of which had their ContainerSpec literal changed by the rebase fix in steps…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go — (*Container).buildPod, the mainContainerName container literal:; brine RED: And that pod works in "/tmp/build/workdir" (line 35) — error: expected the step's working directory to be "/tmp/build/workdir", got ""; go RED: [FAILED] Expected <string>: to equal <string>: /workdir In [It] at: <wt>/atc/worker/jetbridge/container_test.go:93 — i.e. Expect(pod.Spec.Containers[0].WorkingDir).To(Equal("/workdir")); skeptic: different-behaviour pairing (narrower mutation on a clause the Go It pins and no brine scenario asserts: the Command/Args split), plus red-by-adaptation control and unrelated-brine-red control → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run returns a Process with an ID  `JB-container-001`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): its only named counterpart is container-run.feature, whose ContainerSpec builder (steps/container_extra.go runExtraSpecFromDraft, plus steps/domain.go) changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func (*Container).Run, line 128 (recipe said 129; the line is 128 on 27d81692fa):; brine RED: And the step has an identity a restarted web could attach to (container-run.feature:36) — step_end status "failed", error verbatim: "expected the process to have an id, it has an empty one" (check de…; go RED: [FAILED] Expected <string>: not to be empty In [It] at: .../atc/worker/jetbridge/container_test.go:117 — i.e. `Expect(process.ID()).ToNot(BeEmpty())` at container_test.go:117, spec `Container Run [It…; skeptic: Four attacks run, all failed: (1) red-by-adaptation — reverted mutation with the restored Go test in place; (2) unrelated/flaky brine red — clean brine run of container-run.feature; (3) order-masked… → HOLDS

**[REFUTED]** Container Run with Dir volume creates a Pod with an emptyDir volume for spec.Dir when Dir is set  `JB-container-002`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): the DISPOSITION that retired it lives in container-run.feature and hands coverage to container-pod.feature 'A step sees its working directory and every input'; both features are in step_changes via steps/container_spec.go and steps/domain.go. Not (a): buildVolumeMounts changed only i…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func (*Container).buildVolumeMounts (line 916 on 27d81692fa) — deleted the whole Dir block:; brine RED: Then the pod has 3 volumes -> status "failed", error: `expected 3 volumes, found 2: [input-0 input-1]` (scenario_end error identical). The first 6 steps (worker, task container "input-vol-handle", it…; go RED: [FAILED] Expected <[]v1.Volume | len:0, cap:0>: nil to have length 1 In [It] at: .../atc/worker/jetbridge/container_test.go:149 Summarizing 1 Failure: [FAIL] Container Run with Dir volume [It] create…; skeptic: Narrower-mutation attack (different-behaviour pairing): I isolated the clause of the Go It that the brine scenario does not state — the main container is handed EXACTLY ONE mount — and mutated only t… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run with Dir volume does not create a Dir volume when spec.Dir is empty  `JB-container-003`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'A step that declares no working directory is given no workspace', and container-run.feature's draft-to-ContainerSpec builder changed in the rebase (steps/container_extra.go, steps/domain.go).
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func (*Container).buildVolumeMounts — make the Dir volume unconditional so an empty Dir still mints `dir-0`:; brine RED: Then the pod has 0 volumes (line 58) — error: "expected 0 volumes, found 1: [dir-0]"; go RED: [FAIL] Container Run with Dir volume [It] does not create a Dir volume when spec.Dir is empty — container_test.go:182 — `Expect(pod.Spec.Volumes).To(BeEmpty())`: "Expected <[]v1.Volume | len:1, cap:1…; skeptic: Four attacks run, all failed: (1) narrower differentiating mutation (mount-half-only, to test whether brine's second Then is decorative/order-masked — the verifier's own admitted "asserted-but-unexer… → HOLDS

**[REFUTED]** Container Run with input volumes creates a Pod with emptyDir volumes mounted at input paths  `JB-container-004`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'A step sees its working directory and every input', a feature whose ContainerSpec literal changed in the rebase (steps/container_spec.go). Not (a): the input loop of buildVolumeMounts is byte-identical between merge-base and core.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts — the input loop (line 952 on the rebased head, recorded as ~945):; brine RED: step_end failed | text: the step sees a volume mounted at "/tmp/build/workdir/input-a" | error: expected the step's mounts to include "/tmp/build/workdir/input-a", found [/tmp/build/workdir /tmp/buil…; go RED: Container Run with input volumes [It] creates a Pod with emptyDir volumes mounted at input paths — container_test.go:216, failed at container_test.go:243 during STEP "mounting volumes at the correct…; skeptic: different-behaviour pairing (narrower mutation on the clause brine never asserts), plus red-by-adaptation and unrelated-brine-red baselines, plus a reproduction of the recorded mutation to prove the… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run with output volumes creates a Pod with emptyDir volumes mounted at output paths  `JB-container-005`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'An output on its own path gets its own volume', and that feature's spec builder changed in the rebase (steps/container_spec.go + steps/domain.go).
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func (*Container).buildVolumeMounts — unconditional `continue` at the top of the output loop (line ~967 on 27d81692fa), so no output ever gets its own volume/mount:; brine RED: Then the pod has 3 volumes (line 52) — error: expected 3 volumes, found 2: [dir-0 input-1]; go RED: [FAIL] Container Run with output volumes [It] creates a Pod with emptyDir volumes mounted at output paths — STEP: adding emptyDir volumes for Dir and each output; [FAILED] Expected <[]v1.Volume | len…; skeptic: different-behaviour pairing (narrower mutation, two independent variants); plus red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run with same-name input and output shares a single volume when input and output paths overlap  `JB-container-006`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'An output that shares an input's path gets one volume, not two', whose ContainerSpec literal changed in the rebase (steps/container_spec.go).
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts — deleted the overlap-dedup guard in the output loop (recorded mutation, applied verbatim):; brine RED: Then the pod has 2 volumes (line 40) — step_end status "failed", error: `expected 2 volumes, found 3: [dir-0 input-1 output-2]`; scenario_end status "failed" with the same error. Run totals with the…; go RED: container_test.go:343, inside By("creating only 2 volumes (dir + shared input/output), not 3"): `Expect(pod.Spec.Volumes).To(HaveLen(2))` — `[FAILED] Expected <[]v1.Volume | len:3, cap:3>: [ {Name: "…; skeptic: different-behaviour pairing (two narrower mutations that break exactly what the Go It asserts), plus red-by-adaptation control → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run with same-name input and output uses the input volume for the shared mount (not a new output volume)  `JB-container-007`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): its only counterpart, container-pod.feature 'An input sharing an output's path is filed under the output's name', is in a feature whose spec builder changed in the rebase — and that scenario was written three days AFTER the deletion (b831d9cd36 / 4b37fe4ee0), so the evidence was neve…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation TWO mutations were measured, both in atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts. (A) THE RECORDED MUTATION — drop the input-loop subdir override (recipe's edit, target still ex…; brine RED: Then the pod has 2 volumes (line 40) — status failed — error verbatim: `expected 2 volumes, found 3: [dir-0 input-1 output-2]`. Run totals: scenarios 44, passed 43, failed 1, verdict failed. For the…; go RED: WITH mutation (B): `FAIL! -- 0 Passed | 1 Failed | 0 Pending | 91 Skipped`. Verbatim: STEP: the shared mount being named input-*, not output-* [FAILED] Expected <string>: output-2 to have prefix <str…; skeptic: Primary: different-behaviour pairing via a NARROWER mutation that breaks only what the Go assertion pins (mutation C) — brine stayed fully green. Supporting: mutation-not-the-recorded-one (the verifi… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run with non-overlapping inputs and outputs creates separate volumes for non-overlapping input and output  `JB-container-008`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'An output on its own path gets its own volume', a feature whose ContainerSpec builder changed in the rebase (steps/container_spec.go).
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func (c *Container) buildVolumeMounts (the output loop's overlap-dedup guard):; brine RED: Then the pod has 3 volumes -> "expected 3 volumes, found 2: [dir-0 input-1]" (scenario_end status=failed, duration_ms=4); go RED: container_test.go:419 — [FAILED] Expected <[]v1.Volume | len:2, cap:2>: [ {Name: "dir-0", ...}, {Name: "input-1", ...} ] to have length 3 (STEP: creating 3 volumes (dir + input + output)); skeptic: Decorative/uncovered-clause: the Go It asserts TWO things (3 volumes AND 3 mounts on the main container). brine's paired scenario counts volumes but no brine step anywhere counts the pod's container… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run with cache volumes creates a Pod with emptyDir volumes mounted at cache paths  `JB-container-009`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c) — Rule (c): conflict file. Rule (a): 0d336e062b rewrote both auto-detect arms of the cacheMode switch in buildVolumeMounts, so the default (emptyDir) arm this row pins is now reached under different conditions even though this input's answer is unchanged. Rule (b): its counterparts are in container-pod.feature, whose sp…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts — the `default:` arm of the cacheMode switch (line 1050 on 27d81692fa):; brine RED: Then the pod has 3 volumes (line 77) — status "failed", error: expected 3 volumes, found 2: [dir-0 scratch-0]; go RED: container_test.go:459 — STEP: adding emptyDir volumes for Dir and the cache; [FAILED] Expected <[]v1.Volume | len:1, cap:1>: [ { Name: "dir-0", VolumeSource: { HostPath: nil, EmptyDir: {Medium: "", S…; skeptic: narrower mutation that breaks only what the Go test asserts (mount-count clause), plus red-by-adaptation check, unrelated-brine-red check, and reproduction of the recorded coarse mutation → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run with scratch path volumes creates a Pod with emptyDir volumes for scratch paths  `JB-container-010`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'Scratch space never outlives the pod', whose ContainerSpec literal changed in the rebase (steps/container_spec.go). Not (a): the ScratchPaths loop of buildVolumeMounts is untouched by 0d336e062b.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func (c *Container) buildVolumeMounts() — ScratchPaths loop:; brine RED: And the volume mounted at "/tmp/scratch" is lost with the pod -> error: mount "/tmp/scratch" names volume "scratch-0", which the pod does not define; go RED: container_test.go:510, STEP "adding emptyDir volumes for Dir and the scratch path": [FAILED] Expected <[]v1.Volume | len:1, cap:1>: [{Name: "dir-0", VolumeSource: {HostPath: nil, EmptyDir: {Medium: "…; skeptic: different-behaviour pairing (narrower mutation on the SAME recorded target), plus red-by-adaptation and unrelated-brine-red as controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[INERT]** Container Run with scratch path volumes does not create cache entries for scratch paths  `JB-container-011`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c) — Rule (c): conflict file. Rule (a): both source functions this row names changed on core — buildVolumeMounts' cache arms (0d336e062b) and buildArtifactInitContainers, which 1e023e7ca4 turned into an error-returning call. Rule (b): its counterpart sits in container-pod.feature, whose spec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): INERT — mutation Two mutations were measured, both in atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts. M1 (the recorded mutation, verbatim from the recipe) — append the resolved ScratchPaths onto `r…; brine RED: And the volume mounted at "/tmp/scratch" is lost with the pod (line 67) -> error: expected the volume at "/tmp/scratch" to be ephemeral, it is node-local storage; go GREEN: No failing assertion in any of the three runs -- the test passed with M1 applied, with M2 applied, and with no mutation. The assertion under test is Expect(pod.Spec.InitContainers).To(BeEmpty()) (By(…; skeptic: not reached — the verifier stopped at INERT
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run with scratch paths and caches together creates separate volumes for caches and scratch paths  `JB-container-012`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c) — Rule (c): conflict file. Rule (a): the cacheMode arms of buildVolumeMounts this row traverses were rewritten by 0d336e062b (the answer for this input is unchanged, but the decision is not). Rule (b): it migrated to container-pod.feature 'A cache and a scratch path are different volumes', a feature whose spec builder c…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts, ScratchPaths loop (line ~1070 on 27d81692fa). THE RECORDED MUTATION WAS INERT AND WAS REPLACED BY ONE SHARPER VARIANT (see mapping_n…; brine RED: Then the pod has 3 volumes -- scenario_end error verbatim: "expected 3 volumes, found 2: [dir-0 cache-1]". The six preceding steps all passed: "a jetbridge worker on a fake Kubernetes cluster", "a ta…; go RED: container_test.go:577, under By("adding emptyDir volumes for Dir, cache, and scratch"): Expect(pod.Spec.Volumes).To(HaveLen(3)) -- "[FAILED] Expected <[]v1.Volume | len:2, cap:2>: [ {Name: \"dir-0\",…; skeptic: Four attacks run, all measured: (1) red-by-adaptation — clean-tree focused run of the restored Go test; (2) mutation-not-the-recorded-one — the recorded Variant A measured on the Go side (verifier ha… → HOLDS

**[DELETED]** Container Run with relative scratch paths resolves relative scratch paths against the working directory  `JB-container-013`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): its counterpart container-pod.feature 'A relative scratch path lands inside the working directory' lives in a feature whose ContainerSpec builder changed in the rebase — and it was added by b831d9cd36 on 2026-08-29, three days after the deletion, so it was never contemporaneous evide…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts — ScratchPaths loop (line 1069 on 27d81692fa):; brine RED: Then the step sees a volume mounted at "/tmp/build/workdir/tmp-work" (line 483) — status failed, error verbatim: `expected the step's mounts to include "/tmp/build/workdir/tmp-work", found [/tmp/buil…; go RED: container_test.go:631 — [FAILED] Expected <[]string | len:2, cap:2>: ["/tmp/build/workdir", "scratch"] to contain element matching <string>: /tmp/build/workdir/scratch Summary WITH mutation: `Will ru…; skeptic: Four attacks run, none refuted: (1) red-by-adaptation — clean Go test with the pre-adapted file in place; (2) unrelated/flaky brine red — one clean run of the whole feature; (3) mutation-not-the-reco… → HOLDS

**[DELETED]** Container Run with cache hostPath configured when CacheHostPath is set uses hostPath volumes with stable keys for caches  `JB-container-014`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — Every criterion fires: (c) conflict file; (a)+(d) 0d336e062b rewrote both stableCacheKey's signature/branching and the auto-detect arm this test exercises, so the restored test now takes the emptyDir path and FAILS for a reason unrelated to any mutation; (b) container-pod.feature 'A cache is kept on the node...' had t…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go :: stableCacheKey (line 894); brine RED: Then the cache at "/tmp/build/workdir/.cache" is kept on the node under "/var/concourse/cache/job-7-compile-" (line 284) — status failed, error: expected the cache filed under "/var/concourse/cache/j…; go RED: [FAILED] Expected <string>: /var/concourse/cache/job-7-e68dc0f30e98 to have prefix <string>: /var/concourse/cache/job-7-compile- In [It] at: .../atc/worker/jetbridge/container_test.go:691; skeptic: Three attacks run, all failed to refute: (1) red-by-adaptation — clean baseline of the restored Go spec; (2) different-behaviour pairing via a NARROWER second mutation aimed at the Go It's OTHER asse… → HOLDS

**[REFUTED]** Container Run with cache hostPath configured when CacheHostPath is set but JobID is 0 (one-off build) falls back to emptyDir for one-off builds  `JB-container-015`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — Rule (c) conflict file; rules (a)+(d) 0d336e062b DELETED the exact gate this test exists to pin — `c.config.CacheHostPath != "" && c.metadata.JobID != 0` became `... && c.containerSpec.TaskCacheIdentity != nil` — so the restored test passes for a new reason and is vacuous; rule (b) container-pod.feature's one-off scen…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts — remove the "no key => ephemeral cache" gate in both places it now lives, and give the deref sites a zero identity (as the merge-bas…; brine RED: Then the volume mounted at "/tmp/build/workdir/.cache" is lost with the pod (line 320) — status "failed", error: expected the volume at "/tmp/build/workdir/.cache" to be ephemeral, it is node-local s…; go RED: [FAILED] one-off builds should not use hostPath Expected <*v1.HostPathVolumeSource | 0x140004a48d0>: { Path: "/var/concourse/cache/run-0-0--5a116b607dc0", Type: "DirectoryOrCreate", } to be nil In [I…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts) + mutation-not-the-recorded-one + unrelated-brine-red baseline + red-by-adaptation control → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run with explicit CacheStore selector when CacheStore=hostpath overrides artifact store uses hostPath even though artifact store is configured  `JB-container-016`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c, d) — Rule (c) conflict file; rules (a)+(d) 0d336e062b added a downgrade that forces an EXPLICIT CacheStore=hostpath back to emptyDir when TaskCacheIdentity is nil, so the restored test now fails outright; rule (b) container-pod.feature's outline needed rebase commit 81d13b70203 to keep the hostpath row green.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation RECORDED MUTATION (tried first, INERT for this row):; brine RED: Then the volume mounted at "/tmp/build/workdir/.cache" survives the pod (line 332) — status failed, error: expected the volume at "/tmp/build/workdir/.cache" to be node-local storage, it is ephemeral; go RED: [FAILED] expected a hostPath volume for cache — In [It] at: atc/worker/jetbridge/container_test.go:814, i.e. Expect(hostPathVol).ToNot(BeNil(), "expected a hostPath volume for cache"). Run summary: '…; skeptic: different-behaviour pairing (two narrow one-sided mutations, each isolating one arm of the `case CacheStoreHostPath` switch), plus a red-by-adaptation control and an unrelated-brine-red control → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run with explicit CacheStore selector when CacheStore=emptydir overrides artifact store uses emptyDir without init containers or cache uploads  `JB-container-017`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c) — Rule (c): conflict file. Rule (a): both functions it pins changed on core — the cacheMode switch in buildVolumeMounts (0d336e062b) and buildArtifactInitContainers, which 1e023e7ca4 made error-returning. Rule (b): its counterpart outline lives in container-pod.feature, whose spec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts, the `default:` (CacheStoreEmptyDir) arm of the cacheMode switch — emit a hostPath volume instead of an emptyDir:; brine RED: Then the volume mounted at "/tmp/build/workdir/.cache" is lost with the pod — status "failed", error: expected the volume at "/tmp/build/workdir/.cache" to be ephemeral, it is node-local storage; go RED: [FAILED] Expected <[]v1.Volume | len:0, cap:0>: nil to have length 1 In [It] at: .../atc/worker/jetbridge/container_test.go:864 — i.e. Expect(cacheVols).To(HaveLen(1)) under By("using emptyDir volume…; skeptic: narrower-mutation separator (plus red-by-adaptation, reproduction, decorative-assertion audit) → HOLDS

**[GAP]** Container Run with explicit CacheStore selector when CacheStore=emptydir is explicitly set uses emptyDir for caches  `JB-container-018`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c) — Rule (c): conflict file. Rule (a): it pins the cacheMode switch that 0d336e062b rewrote (its own arm is unchanged, but the switch is not). Rule (b): same container-pod.feature outline, whose builder changed in the rebase. It is also a near-duplicate of JB-container-017 (same Config, different StepName), so one re-run…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/container.go, (*Container).buildVolumeMounts — `default:` (CacheStoreEmptyDir) arm of the cacheMode switch, ~line 1046:; brine GREEN: none — the named scenario passed under the recorded mutation AND under the sharper variant. Both runs: 44 scenario_end events, 0 failures, exit 0. No other scenario in the feature reddened either (al…; go RED: [FAILED] emptyDir caches should not use subPath Expected <string>: cache to be empty In [It] at: .../atc/worker/jetbridge/container_test.go:915 (the STEP that failed: `cache volumes should be emptyDi…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run with resource limits when CPU and Memory limits are specified sets K8s resource requests and limits on the main container  `JB-container-019`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'Limits alone reserve exactly what they cap', a feature whose ContainerSpec literal changed in the rebase. Not (a)/(d): `git log -S buildResourceRequirements aef2244a63..5133d0ddbc` returns no commits.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func buildResourceRequirements (Guaranteed-QoS branch, line 802 on 27d81692fa):; brine RED: And the step is reserved "1024m" CPU and "1Gi" memory — error: `expected a cpu request of 1024m, none is set` (scenario_end: {"name":"Limits alone reserve exactly what they cap","status":"failed","er…; go RED: container_test.go:970 — `[FAILED] expected CPU request of 1024m, got 0 / Expected <int>: -1 to equal <int>: 0` (preceded by `STEP: setting requests equal to limits for Guaranteed QoS`). With mutation…; skeptic: Three attacks run, all failed to refute: (1) NARROWER MUTATION / different-behaviour pairing — a mutation that breaks only ONE of the four assertions the Go It makes (memory-request mirroring only, C… → HOLDS

**[DELETED]** Container Run with resource limits when no limits are specified creates a pod with no resource constraints (BestEffort QoS)  `JB-container-020`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'A step that asks for nothing is evicted first', whose spec builder changed in the rebase. buildResourceRequirements itself is untouched on core.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func buildResourceRequirements (line ~786 on 27d81692fa):; brine RED: Then the pod is scheduled as "BestEffort" — error verbatim: expected the pod's QoS class to be "BestEffort", got "Burstable" (limits=map[cpu:{{1 0} {<nil>} 1 DecimalSI}] requests=map[]); go RED: container_test.go:1006 — [FAILED] Expected <v1.ResourceList | len:1>: { "cpu": {i: {value: 1, scale: 0}, d: {Dec: nil}, s: "1", Format: "DecimalSI"}, } to be nil (assertion: Expect(mainContainer.Reso…; skeptic: Three attacks run: (1) red-by-adaptation — unmutated tree with the restored/adapted Go test; (2) different-behaviour pairing via a NARROWER mutation that breaks only the Go assertion's representation… → HOLDS

**[DELETED]** Container Run with resource limits when both limits and independent requests are specified (Burstable QoS) sets limits and requests independently  `JB-container-021`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'Independent requests below the limits make the pod burstable', a feature whose ContainerSpec literal changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func buildResourceRequirements (~line 780):; brine RED: And the step is reserved "512m" CPU and "1Gi" memory — error: expected a cpu request of 512m, got 2048m; go RED: [FAILED] Expected <int>: 1 to equal <int>: 0 In [It] at: atc/worker/jetbridge/container_test.go:1057 — i.e. Expect(mainContainer.Resources.Requests.Cpu().Cmp(*resource.NewMilliQuantity(512, resource.…; skeptic: Four attacks run, all failed: (1) NARROWER MUTATION that breaks only the single quantity the Go It pins (CPU request), leaving memory and ephemeral requests independent — the sharpest available separ… → HOLDS

**[REFUTED]** Container Run with resource limits when only requests are specified with no limits (Burstable no-cap QoS) sets requests with no limits  `JB-container-022`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'Requests without limits reserve a floor but set no ceiling', whose spec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func buildResourceRequirements (Burstable-no-cap branch, after the independent-requests ResourceList is assembled):; brine RED: Then/And "the pod is scheduled as \"Burstable\"" — scenario_end error: `expected the pod's QoS class to be "Burstable", got "Guaranteed" (limits=map[cpu:{{512 -3} {<nil>} DecimalSI} memory:{{10737418…; go RED: container_test.go:1100, under `By("not setting any limits")`: `Expect(mainContainer.Resources.Limits).To(BeNil())` → `[FAILED] Expected <v1.ResourceList | len:2>: {"cpu": {i: {value: 256, scale: -3},…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go It asserts), plus red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run with resource limits when ephemeral-storage limits and requests are specified sets ephemeral-storage in K8s resource requirements  `JB-container-023`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'Local disk is reserved and capped like any other resource', a feature whose ContainerSpec literal changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go : buildResourceRequirements — deleted both ephemeral-storage mapping blocks (the recorded mutation, verbatim):; brine RED: Then the step may use at most "2Gi" of local disk, reserving "1Gi" (line 249) — status failed, error: "expected an ephemeral-storage limit of 2Gi, none is set"; go RED: [FAILED] expected ephemeral-storage limit of 5Gi, got 0 Expected <int>: -1 to equal <int>: 0 In [It] at: .../atc/worker/jetbridge/container_test.go:1151 (i.e. Expect(ephLimitQty.Cmp(*resource.NewQuan…; skeptic: Three attacks run, all failed to refute: (1) mutation-not-the-recorded-one / different-behaviour pairing, attacked with a NARROWER mutation than the recorded one — I deleted ONLY the `if limits.Ephem… → HOLDS

**[REFUTED]** Container Run with security context when the container is not privileged sets AllowPrivilegeEscalation=false on non-privileged container  `JB-container-024`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): its two clauses land in container-pod.feature 'An unprivileged step cannot gain privileges' and container-run.feature 'A step is confined even when nobody asked for confinement', both features whose ContainerSpec builders changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation Two mutations, applied and measured separately (the row's Go test asserts both clauses). MUTATION 1 (primary — privilege escalation), atc/worker/jetbridge/container.go, func buildContainerSecurityCon…; brine RED: MUTATION 1 — step_end status=failed, keyword "Then", text "the step cannot escalate its privileges" (container-pod.feature line 121), error: `expected privilege escalation to be denied, got &Security…; go RED: MUTATION 1 (the row's own clause) — `• [FAILED] Container Run with security context when the container is not privileged [It] sets AllowPrivilegeEscalation=false on non-privileged container`; last ST…; skeptic: Decorative/uncovered-clause attack (the sharpest available): the restored Go It asserts THREE clauses and one — the deliberate RunAsNonRoot carve-out — has no brine counterpart anywhere. I built an h… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run with security context when the container is privileged sets Privileged=true on privileged container  `JB-container-025`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'A privileged step is granted privilege', whose ContainerSpec literal changed in the rebase. buildContainerSecurityContext itself is untouched on core.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func buildContainerSecurityContext (line 842 on 27d81692fa) — deleted the privileged early-return branch, so a privileged step now receives the hardened (AllowPrivi…; brine RED: Then the step can escalate its privileges — error: "expected a privileged container, got &SecurityContext{Capabilities:nil,Privileged:nil,SELinuxOptions:nil,RunAsUser:nil,RunAsNonRoot:nil,ReadOnlyRoo…; go RED: container_test.go:1254 — By("setting Privileged=true on container security context"); Expect(mainContainer.SecurityContext.Privileged).ToNot(BeNil()) => "[FAILED] Expected <*bool | 0x0>: nil not to b…; skeptic: decorative/insufficient-coverage via a NARROWER mutation (the "the It asserts more than any scenario" attack the Scanner and Destroyer skeptics used in DISPOSITION-gc-lidar.md) — mutate only the clau… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run with imagePullSecrets and serviceAccount when image pull secrets and service account are configured includes imagePullSecrets and serviceAccountName in the pod spec  `JB-container-026`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'Operator-configured pull secrets and service account reach the pod', a feature whose spec builder changed in the rebase. Not (a)/(d): `git log -S buildImagePullSecrets` over the range returns nothing.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation Two mutations, applied and measured SEPARATELY (both from the recipe), in atc/worker/jetbridge/container.go on worktree base 27d81692fa. (1) (*Container).buildPod PodSpec literal, line 484:; brine RED: Mutation (1): step `And the pod runs as the service account "ci-runner"` (line 202) -> status failed, error: expected the pod's service account to be "ci-runner", got "" | Mutation (2): step `Then th…; go RED: Mutation (1): `[FAILED] Expected <string>: to equal <string>: ci-runner In [It] at: .../atc/worker/jetbridge/container_test.go:1311` (under `By("setting serviceAccountName from config")`). Mutation (…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts), plus red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run with imagePullSecrets and serviceAccount when no image pull secrets or service account are configured creates a pod with no imagePullSecrets or serviceAccountName  `JB-container-027`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'With nothing configured the pod names no credentials', a feature whose ContainerSpec literal changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func buildImagePullSecrets (line 857):; brine RED: Then the pod names no image pull secret and no service account (line 209) — step_end status "failed", error: "expected no image pull secrets, got 1"; go RED: [FAILED] Expected <[]v1.LocalObjectReference | len:1, cap:1>: [{Name: "default"}] to be empty In [It] at: .../atc/worker/jetbridge/container_test.go:1344 — i.e. `Expect(pod.Spec.ImagePullSecrets).To(…; skeptic: Ran four attacks: (1) unrelated/flaky brine red — clean baseline of container-pod.feature; (2) red-by-adaptation — clean Go baseline with the pre-adapted restored file; (3) mutation-not-the-recorded-… → HOLDS

**[REFUTED]** Container Run with imagePullSecrets and serviceAccount when ImageRegistry is configured with a SecretName auto-includes the registry secret in imagePullSecrets  `JB-container-028`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'A private registry's secret is added to the pod', whose spec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func buildImagePullSecrets (line ~866) — deleted the registry-append block:; brine RED: Then the pod pulls images using the secret "gcr-auth" (line 216) — status "failed", error: expected the pod's image pull secrets to include "gcr-auth", found [existing-secret]; go RED: container_test.go:1387 — [FAILED] Expected <[]v1.LocalObjectReference | len:1, cap:1>: [ { Name: "existing-secret", }, ] to have length 2 (i.e. `Expect(pod.Spec.ImagePullSecrets).To(HaveLen(2))`); skeptic: different-behaviour pairing via a narrower mutation (the Go It's `HaveLen(2)` clause is uncovered by brine's membership-only step); plus red-by-adaptation and harness-liveness controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run with imagePullSecrets and serviceAccount when ImageRegistry SecretName duplicates an existing imagePullSecret deduplicates the secret name  `JB-container-029`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'A registry secret the operator already listed is not duplicated', a feature whose ContainerSpec literal changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func buildImagePullSecrets (line 866) — dropped the `&& !seen[registry.SecretName]` dedup guard:; brine RED: Then the pod names the secret "gcr-auth" exactly once (line 226) — error: expected the secret "gcr-auth" exactly once, found it 2 times; go RED: [FAILED] Expected <[]v1.LocalObjectReference | len:2, cap:2>: [ { Name: "shared-secret", }, { Name: "shared-secret", }, ] to have length 1 In [It] at: .../atc/worker/jetbridge/container_test.go:1432; skeptic: Ran four attacks: (1) unrelated/flaky brine red — clean run of container-pod.feature; (2) red-by-adaptation — unmutated Go run with the restored file; (3) order-masked assertion — step-level event in… → HOLDS

**[REFUTED]** Container Run uses exec-mode for all tasks (universal pause pod) creates a pause pod even when stdin is nil  `JB-container-030`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'With an exec transport the pod is a placeholder the step runs inside', whose draft-to-ContainerSpec builder (steps/container_extra.go) changed in the rebase. Not (a): the only process.go change on core (1164f9db3d) is waitForRunning delegating to…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation Two mutations, both in atc/worker/jetbridge/container.go on the rebased head 27d81692fa. (A) The RECORDED mutation, applied verbatim at the recorded line (still line 115 on the rebased tree):; brine RED: And the pod is a placeholder, not the step's command (line 88) — error: "expected the pod NOT to carry the step's command, its entrypoint is \"/bin/sh -c echo hello\" — the step was baked into the po…; go RED: container_test.go:1480 — [FAILED] Expected <[]string | len:3, cap:3>: ["/bin/sh", "-c", "echo hello"] to equal <[]string | len:3, cap:3>: [ "sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait", ] (un…; skeptic: Primary: different-behaviour pairing, executed as a NARROWER mutation that breaks only what the Go assertion pins (the exact pause string) and nothing the brine step can observe. Secondary: red-by-ad… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run uses exec-mode for all tasks (universal pause pod) keeps pause pod alive after command completes with exit 0  `JB-container-031`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): the success direction is covered by container-run.feature 'With an exec transport the pod is a placeholder the step runs inside' -> 'And the pod is still on the cluster afterwards', and that feature's spec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/process.go, (*execProcess).Wait — success path, immediately before the final `return runtime.ProcessResult{ExitStatus: 0}, nil`:; brine RED: And the pod is a placeholder, not the step's command (line 88) — error: get pod "placeholder-task": pods "placeholder-task" not found. (scenario_end: status "failed", same error; feature_end passed=1…; go RED: [FAILED] pause pod should NOT be deleted after exec completes — GC handles cleanup Expected <[]v1.Pod | len:0, cap:0>: nil to have length 1 In [It] at: .../atc/worker/jetbridge/container_test.go:1521…; skeptic: Ran three: (1) red-by-adaptation — clean Go run of the restored file BEFORE any mutation; (2) unrelated/flaky brine red — clean run of container-run.feature before any mutation; (3) order-masked/deco… → HOLDS

**[DELETED]** Container Run uses exec-mode for all tasks (universal pause pod) keeps pause pod alive after command completes with non-zero exit  `JB-container-032`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'A failed step's pod is kept for the operator, not cleaned up on the spot', a feature whose ContainerSpec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/process.go, func (p *execProcess) Wait — recipe mutation (1): delete the pause pod when the exec returns a non-zero *ExecExitError.; brine RED: And the pod is still on the cluster afterwards (line 99) — status failed, error: expected the pod "kept-after-failure" to still be on the cluster after the step finished, it is gone — its outputs can…; go RED: STEP: verifying the pod still exists after failed exec — for debugging [FAILED] pause pod should NOT be deleted after exec failure — GC handles cleanup Expected <[]v1.Pod | len:0, cap:0>: nil to have…; skeptic: Five attacks run, all measured: (1) different-behaviour pairing via a NARROWER mutation that breaks only the Go-only clause; (2) red-by-adaptation; (3) unrelated/flaky brine red (clean baseline); (4)… → HOLDS

**[DELETED]** Container FindOrCreateContainer returns VolumeMounts when container spec has inputs returns a VolumeMount for each input with correct MountPath  `JB-container-033`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'Every path the step declared comes back as its own volume', whose ContainerSpec builder changed in the rebase. Not (a): atc/worker/jetbridge/worker.go is byte-identical merge-base to core.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/worker.go, func (w *Worker) buildVolumeMountsForSpec — drop the addMount inside the inputs loop, keep the inputMountPaths bookkeeping (loop var i became unused, so `i` -> `_`; 1…; brine RED: Then the caller is handed 5 volumes in all -> expected 5 volumes, found 3: [/tmp/build/workdir /tmp/build/workdir/result /tmp/build/workdir/.cache]; go RED: [FAILED] Expected <[]runtime.VolumeMount | len:0, cap:0>: nil to have length 2 In [It] at: .../atc/worker/jetbridge/container_test.go:1587; skeptic: Four attacks run, all failed: (1) unrelated/flaky brine red — clean baseline run of container-run.feature; (2) red-by-adaptation — unmutated focused Go run with the pre-adapted restoration in place;… → HOLDS

**[GAP]** Container FindOrCreateContainer returns VolumeMounts when container spec has inputs returns Volumes with an executor wired up for StreamIn/StreamOut  `JB-container-034`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): the DISPOSITION that deliberately dropped it hands coverage to volume-streaming.feature, which is in step_changes (steps/domain.go, steps/container_extra.go). Note it was NAMED as not-migrated, so this row's disposition is a coverage argument, not both-red evidence — worth re-checkin…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/worker.go, (*Worker).newVolumeForMount — exactly the recorded mutation:; brine GREEN: none — 20 of 20 scenarios passed under the recorded mutation; run_end {"features":1,"scenarios":20,"passed":20,"failed":0,"duration_ms":22999}, verdict "passed", brine exit 0. NO scenario in the feat…; go RED: [FAILED] volume should have an executor for StreamIn/StreamOut Expected <bool>: false to be true In [It] at: /Users/tdmtrader/concourse/concourse/.worktrees/rv-jb-container-034/atc/worker/jetbridge/c…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container FindOrCreateContainer returns VolumeMounts when container spec has outputs returns a VolumeMount for each output with correct MountPath  `JB-container-035`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'Every path the step declared comes back as its own volume', a feature whose ContainerSpec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/worker.go, (*Worker).buildVolumeMountsForSpec — unconditional `continue` at the top of the outputs loop, exactly as the recipe specified:; brine RED: Then the caller is handed 5 volumes in all — error: `expected 5 volumes, found 4: [/tmp/build/workdir /tmp/build/workdir/my-input /tmp/build/workdir/other-input /tmp/build/workdir/.cache]` (scenario_…; go RED: container_test.go:1637 — `[FAILED] Expected <[]runtime.VolumeMount | len:0, cap:0>: nil to have length 2`. Spec text: `Container FindOrCreateContainer returns VolumeMounts when container spec has out…; skeptic: different-behaviour pairing (narrower mutation aimed at the one clause the Go It asserts and no brine scenario can see: ONE mount PER declared output path, plural); plus red-by-adaptation control and… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container FindOrCreateContainer returns VolumeMounts when container spec has outputs returns output Volumes with an executor wired up for StreamIn/StreamOut  `JB-container-036`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): the same not-migrated DISPOSITION points at volume-streaming.feature, which is in step_changes. Same disposition as JB-container-034 — one re-run settles all four HasExecutor rows.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/worker.go, func (w *Worker) newVolumeForMount: func (w *Worker) newVolumeForMount(handle, mountPath string) *Volume { - if w.executor != nil { - return NewDeferredVolume(handle,…; brine RED: Then the streamed output holds "output.txt" containing "hello-from-the-step" -- error: reading the step's output failed: cannot stream out: volume outputsurvives-output-result has no executor (stub v…; go RED: [FAILED] volume should have an executor for StreamIn/StreamOut Expected <bool>: false to be true In [It] at: .../atc/worker/jetbridge/container_test.go:1654; skeptic: Three run: (1) red-by-adaptation — unmutated restored test; (2) unrelated/flaky brine red — clean feature run; (3) different-behaviour pairing via a NARROWER mutation plus its complement — output-cal… → HOLDS

**[DELETED]** Container FindOrCreateContainer returns VolumeMounts when container spec has caches returns a VolumeMount for each cache with correct MountPath  `JB-container-037`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'Every path the step declared comes back as its own volume', whose Then explicitly lists a volume at '/tmp/build/workdir/.cache'; that feature's spec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/worker.go, (*Worker).buildVolumeMountsForSpec — deleted the whole cache loop:; brine RED: Then the caller is handed 5 volumes in all — step_end status=failed, error: `expected 5 volumes, found 4: [/tmp/build/workdir /tmp/build/workdir/my-input /tmp/build/workdir/other-input /tmp/build/wor…; go RED: [FAILED] Expected <[]runtime.VolumeMount | len:0, cap:0>: nil to have length 1 In [It] at: .../atc/worker/jetbridge/container_test.go:1682 Summary: `Ran 1 of 92 Specs in 0.855 seconds` / `FAIL! -- 0…; skeptic: Ran four attacks: (1) unrelated/flaky brine red — clean baseline run of container-run.feature; (2) red-by-adaptation — restored pre-adapted container_test.go on the un-mutated tree and ran the focuse… → HOLDS

**[GAP]** Container FindOrCreateContainer returns VolumeMounts when container spec has caches returns cache Volumes with an executor wired up for StreamIn/StreamOut  `JB-container-038`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): third of the four not-migrated HasExecutor cases, whose DISPOSITION hands coverage to volume-streaming.feature — a feature in step_changes.
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/worker.go, (*Worker).newVolumeForMount (line 204 on 27d81692fa) — remove the executor branch so every mount gets a stub volume:; brine GREEN: none — 20 of 20 scenarios passed under the mutation, and 20 of 20 again under the inverse-branch variant; no scenario_end event had status != passed in either run; go RED: [FAILED] volume should have an executor for StreamIn/StreamOut Expected <bool>: false to be true In [It] at: /Users/tdmtrader/concourse/concourse/.worktrees/rv-jb-container-038/atc/worker/jetbridge/c…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container FindOrCreateContainer returns VolumeMounts deferred pod name is set when Run creates the pod sets the pod name on deferred volumes after Run  `JB-container-039`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to the container-run.feature pair 'A volume handed back before the step runs knows no pod' / 'Running the step is what binds its volumes to a pod', a feature whose ContainerSpec builder changed in the rebase. bindVolumesToPod itself is untouched by core's three jetbridge…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).Run, exec-mode branch:; brine RED: Then every volume the caller was handed now names the pod "deferred-pod-handle" | error: expected the volume at "/tmp/build/workdir" to name the pod "deferred-pod-handle", it names ""; go RED: [FAILED] Expected <string>: to equal <string>: deferred-pod-handle In [It] at: atc/worker/jetbridge/container_test.go:1740 -- under STEP: "pod name is set after Run"; skeptic: Three run attacks: (1) red-by-adaptation — restored test with mutation reverted; (2) unrelated/flaky brine red — clean unmutated feature run; (3) different-behaviour pairing — a NARROWER mutation tha… → HOLDS

**[GAP]** Container Input streaming is a no-op (handled by init containers) does not exec any streaming commands for inputs  `JB-container-040`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (a, b, c) — Rule (c): conflict file. Rule (a): buildArtifactInitContainers, the function this row's title is about, changed on core (1e023e7ca4 — it now propagates a capability-signing error instead of discarding it). Rule (b): the DISPOSITION and the nearest positive scenario both live in container-run.feature/container-pod.feat…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/process.go, (*execProcess).streamInputs — make it issue one ExecInPod tar-in per declared input instead of returning nil:; brine GREEN: none — the scenario passed under the mutation. All 44 scenarios in container-pod.feature passed (44 scenario_end events, all status=passed; brine exit 0). No other scenario in the feature reddened ei…; go RED: [FAIL] Container Input streaming is a no-op (handled by init containers) [It] does not exec any streaming commands for inputs — container_test.go:1797: [FAILED] Expected <[]jetbridge_test.execCall |…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Output volume extraction after exec output volumes can StreamOut after exec completes  `JB-container-041`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): its counterpart container-run.feature 'A step's output can be read back out of the volume afterwards' (@VT-03) is in a feature whose ContainerSpec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go, func (*Volume).StreamOut — the recorded "equivalently" form of the recipe (tar the wrong path instead of the mount):; brine RED: Then the streamed output holds "output.txt" containing "hello-from-the-step" (line 170) — status "failed", error: expected "output.txt" in the step's output, it holds []; go RED: container_test.go:1872 — [FAILED] Expected <[]string | len:6, cap:6>: ["tar", "cf", "-", "-C", "/", "dev/null"] to equal <[]string | len:6, cap:6>: ["tar", "cf", "-", "-C", "/tmp/build/workdir/result…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go It uniquely asserts) — plus red-by-adaptation and unrelated-brine-red checks, both of which failed to refute; a liveness re… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Output volume extraction after exec pod remains running after exec for output extraction  `JB-container-042`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): the DISPOSITION that dropped it as a third duplicate points at container-run.feature 'A failed step's pod is kept for the operator', a feature in step_changes.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/process.go — (*execProcess).Wait, success path:; brine RED: And the pod is a placeholder, not the step's command (line 88) — error: `get pod "placeholder-task": pods "placeholder-task" not found`; go RED: STEP: pod still exists after exec completes — not deleted [FAILED] Expected <[]v1.Pod | len:0, cap:0>: nil to have length 1 In [It] at: .../atc/worker/jetbridge/container_test.go:1895; skeptic: different-behaviour pairing, attacked with a NARROWER mutation (plus a red-by-adaptation control and a blanket-mutation positive control) → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Properties and SetProperty stores and retrieves properties  `JB-container-043`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: container_test.go is a rebase conflict file (core modified it in 0d336e062b; the rebase took the deletion). Its counterpart is container-lifecycle.feature, which is NOT in step_changes, and neither Container.SetProperty nor Container.Properties nor annotatePod was touched by core's three jetbridge commi…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func (*Container) SetProperty (line 322 on 27d81692fa) — dropped the in-memory map write, kept the annotation-persistence branch:; brine RED: Then reading it back yields "my-key" as "my-value" (line 33) — status failed, error: expected property "my-key", the container has 0 properties. The preceding Given step (a container that has recorde…; go RED: [FAILED] Expected <map[string]string | len:0>: {} to have {key: value} <map[interface {}]interface {} | len:1>: { <string>"my-key": <string>"my-value", } In [It] at: <wt>/atc/worker/jetbridge/contain…; skeptic: Ran four: (1) different-behaviour pairing, probed with a NARROWER mutation N1 that breaks only the behaviour the Go It pins; (2) red-by-adaptation (clean restoration, no mutation); (3) unrelated/flak… → HOLDS

**[REFUTED]** Container Run into existing pod (fly hijack) execs into the existing pod without creating a new one  `JB-container-044`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only. Its actual covering scenario is worker.feature 'Intercepting a step attaches to the pod the step created' — worker.feature is NOT in step_changes (the DISPOSITION merely lives in container-run.feature), and Container.Run's lookedUp branch is untouched by core's three jetbridge commits.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation Two mutations were measured, both in atc/worker/jetbridge/container.go, func (*Container).Run, exec-mode branch. M1 = the recorded mutation (delete the lookedUp guard), line ~150:; brine RED: Then the interception succeeds (worker.feature:108) — error: expected the interception to succeed, it failed: container "550e8400-e29b-41d4-a716-446655440000" has no pod to intercept: pod "my-pipelin…; go RED: container_test.go:1970 — [FAILED] Unexpected error: <*errors.errorString | 0x14000795b30>: container "hijack-pod" has no pod to intercept: pod "hijack-pod" does not exist { s: "container \"hijack-pod…; skeptic: Primarily "different-behaviour pairing / narrower mutation": two independent narrow routing mutations that break exactly the assertion the ginkgo It uniquely makes (execCalls[0].podName) leave the pa… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run into existing pod (fly hijack) passes TTY flag through to executor for interactive sessions  `JB-container-045`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. It was NAMED as deliberately not migrated (PE-08 was ruled an integration-boundary contract), and the scenarios that now cover it — container-run.feature 'A step that asks for a terminal gets a real one' — were added after the deletion; container-run.feature's step changes in the rebase t…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/process.go, func (p *execProcess) Wait (line 802), the ExecInPod call at line 882:; brine RED: Then the step reports "terminal" (line 293) — status failed, error: expected the command's output to mention "terminal", got "pipe " (a shell that believes it is talking to a pipe gives `fly hijack`…; go RED: [FAILED] Expected <bool>: false to be true In [It] at: .../atc/worker/jetbridge/container_test.go:2003 — i.e. Expect(hijackExecutor.execCalls[0].tty).To(BeTrue()); spec 'Container Run into existing p…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go assertion pins), plus red-by-adaptation and unrelated-brine-red checks → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container Run into existing pod (fly hijack) does not set TTY when ProcessSpec.TTY is nil  `JB-container-046`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. Second half of the same not-migrated PE-08 DISPOSITION as JB-container-045; its covering scenario is in container-run.feature but the rebase's container-run change was to the ContainerSpec builder, not the TTY path.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/process.go, (*execProcess).Wait — the tty argument of the ExecInPod call is forced true:; brine RED: Then the step reports "pipe" — scenario_end error: expected the command's output to mention "pipe", got "terminal\r " (a shell that believes it is talking to a pipe gives `fly hijack` no line editing…; go RED: [FAILED] Expected <bool>: true to be false In [It] at: atc/worker/jetbridge/container_test.go:2018 (the line is `Expect(hijackExecutor.execCalls[0].tty).To(BeFalse())`); skeptic: different-behaviour pairing (narrower honest mutation that reddens only what the Go It asserts), plus red-by-adaptation control and an unrelated-brine-red control → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Run into existing pod (fly hijack) propagates exit codes from hijacked commands  `JB-container-047`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'An intercepted command's exit code reaches the operator', and container-run.feature's draft-to-ContainerSpec builder changed in the rebase (steps/container_extra.go, steps/domain.go).
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/process.go, func (*execProcess).Wait — the ExecExitError branch (now line 959 on 27d81692fa; recorded as line 758 at the merge-base):; brine RED: Then the intercepted command exits 130 (line 201) — status "failed", error: `the interception failed: process exited with code 130`. Baseline (mutation reverted, adapter rebuilt): same scenario statu…; go RED: container_test.go:2030 — `Expect(err).ToNot(HaveOccurred())` after `result, err := process.Wait(ctx)`: [FAILED] Unexpected error: <*jetbridge.ExecExitError | 0x1400059a400>: process exited with code…; skeptic: Three attacks run, all failed to refute: (1) NARROWER MUTATION, value clause only — `return runtime.ProcessResult{}, nil` (exit code swallowed, error still nil), to test whether brine pins the NUMBER… → HOLDS
  - correction to the recorded brine command note: it read `(exit 1 mutated; exit 1 baseline)`, which contradicts the baseline line above — the baseline scenario PASSED. What was measured is `exit 1 mutated; baseline green`. Mutated, `container-run.feature` is 17 of 19: the target scenario plus its exit-code neighbour "A failed step's pod is kept for the operator". Unmutated, the target passes and the suite is 568/568; the verifier's baseline exit 1 was one unrelated scenario ("A step's output can be read back out of the volume afterwards"), which the skeptic saw pass in two of three later runs and judged environmental. The verdict does not move: the target's red is mutation-caused either way.

**[DELETED]** Container Run replaces terminal pod replaces a Succeeded pod with a new pause pod  `JB-container-048`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. Its counterpart is container-lifecycle.feature Scenario Outline 'A finished pod is replaced, not reused', and container-lifecycle.feature is NOT in step_changes; Container.Run's terminal-pod branch, GeneratePodName and recreatePausePodIfTerminal are all untouched on core (`git log -S` ret…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func (*Container).Run (exec-mode terminal-pod branch):; brine RED: Then the step gets a live pod, not the dead one (line 20) — error: expected a live pod, "chk-my-time-aaaa1111" is still Succeeded — the dead pod was reused; go RED: [FAILED] terminal pod should have been replaced Expected <v1.PodPhase>: Succeeded not to equal <v1.PodPhase>: Succeeded In [It] at: .../atc/worker/jetbridge/container_test.go:2094; skeptic: Ran three: (1) red-by-adaptation (clean baseline with the restored test), (2) unrelated/flaky brine red (clean feature run), (3) narrower-mutation / different-behaviour pairing (a second, narrower ho… → HOLDS

**[DELETED]** Container Run replaces terminal pod replaces a Failed pod with a new pause pod  `JB-container-049`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. Same Scenario Outline in container-lifecycle.feature, which is not in step_changes, and the same untouched Run branch as JB-container-048.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).Run line 159 — dropped the PodFailed disjunct from the terminal-pod delete condition:; brine RED: Then the step gets a live pod, not the dead one (line 20) — status failed, error verbatim: `expected a live pod, "chk-my-time-aaaa1111" is still Failed — the dead pod was reused`. The two preceding s…; go RED: [FAILED] terminal pod should have been replaced Expected <v1.PodPhase>: Failed not to equal <v1.PodPhase>: Failed In [It] at: .../atc/worker/jetbridge/container_test.go:2094 With the mutation: `Will…; skeptic: Ran five: (1) red-by-adaptation, (2) unrelated/flaky brine red on a clean tree, (3) mutation-not-the-recorded-one, (4) order-masked/decorative assertion, (5) different-behaviour pairing via a NARROWE… → HOLDS

**[DELETED]** Container Run metrics when pod creation succeeds (direct mode) increments ContainersCreated  `JB-container-050`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'A container the runtime created is counted', a feature whose ContainerSpec builder changed in the rebase. atc/metric/metrics.go is byte-identical merge-base to core.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).Run — direct-mode (fallback, non-executor) success path. Deleted line 197.; brine RED: Then the operator sees 1 container created and 0 failed (container-run.feature:229) — status failed, error: "expected the operator to see 1 created and 0 failed, they see 0 created and 0 failed (run…; go RED: [FAILED] Expected <float64>: 0 to equal <float64>: 1 In [It] at: atc/worker/jetbridge/container_test.go:2146 — i.e. Expect(metric.Metrics.ContainersCreated.Delta()).To(Equal(float64(1))); skeptic: Five attacks run, all failed to break the pairing: (1) unrelated/flaky brine red — clean baseline; (2) red-by-adaptation — clean Go baseline with the restored file; (3) mutation-not-the-recorded-one… → HOLDS

**[DELETED]** Container Run metrics when pod creation succeeds (exec mode) increments ContainersCreated  `JB-container-051`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'A placeholder pod counts the same as a step's own pod', whose spec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).Run — execMode branch, line 179 (the increment immediately before `c.bindVolumesToPod(podName)`; the direct-mode one at line 197 was left intact).; brine RED: Then the operator sees 1 container created and 0 failed (line 234) — error: "expected the operator to see 1 created and 0 failed, they see 0 created and 0 failed (run error: <nil>)"; go RED: [FAIL] Container Run metrics when pod creation succeeds (exec mode) [It] increments ContainersCreated [FAILED] Expected <float64>: 0 to equal <float64>: 1 In [It] at: .../atc/worker/jetbridge/contain…; skeptic: Ran four attacks: (A) mutation-not-the-recorded-one / different-behaviour pairing, by mutating the SIBLING direct-mode increment to test whether the named exec scenario actually discriminates the exe… → HOLDS

**[REFUTED]** Container Run metrics when pod creation fails increments FailedContainers  `JB-container-052`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'A pod the cluster refused is counted as a failure, not a creation', a feature whose ContainerSpec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, (*Container).Run — direct fallback createPod error path, line 194:; brine RED: Then the operator sees 0 container created and 1 failed (line 239) — status "failed", error: "expected the operator to see 0 created and 1 failed, they see 1 created and 0 failed (run error: create p…; go RED: [FAILED] Expected <float64>: 0 to equal <float64>: 1 In [It] at: .../atc/worker/jetbridge/container_test.go:2223 — i.e. `Expect(metric.Metrics.FailedContainers.Delta()).To(Equal(float64(1)))`. Run su…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts), plus red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container Attach when the process has already exited returns an already-exited process  `JB-container-053`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. Its counterpart is container-lifecycle.feature 'A step the runtime still remembers is not run again', a feature absent from step_changes, and Container.Attach is untouched by core's three jetbridge commits.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, (*Container).Attach — deleted the in-process exit-status early return (lines 263-273 on 27d81692fa):; brine RED: Then the step is recovered as having exited 0 — error: `the step was not recovered at all: attach: pod "attach-handle" not found: pods "attach-handle" not found` (scenario_end status=failed; the Give…; go RED: container_test.go:2255 — `[FAILED] Unexpected error: <*fmt.wrapError | 0x14000a92200>: attach: pod "attach-handle" not found: pods "attach-handle" not found ... occurred` from `Expect(err).ToNot(Have…; skeptic: Ran four attacks: (1) unrelated/flaky brine red — clean run of the whole feature; (2) red-by-adaptation — clean Go run with the restored file; (3) mutation-not-the-recorded-one / flake — reproduced t… → HOLDS

**[DELETED]** Container Attach exec-mode: when executor is set and pod has exit annotation returns exitedProcess from the pod annotation  `JB-container-054`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. Its counterpart is container-lifecycle.feature Scenario Outline 'After a restart the pod's own record is enough', a feature not in step_changes; Attach's annotation path and annotateExitStatus are both untouched on core (`git log -S annotateExitStatus` over the range returns nothing).
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func (c *Container) Attach:; brine RED: Then the step is recovered as having exited 0 -> "the step was not recovered at all: attach: exec-mode pod \"attach-annotated\" has no completion status" (row 1, line 49); row 2 identical with "...ex…; go RED: container_test.go:2304 -- [FAILED] Unexpected error: <*errors.errorString | 0x140002d47c0>: attach: exec-mode pod "exec-attach-handle" has no completion status { s: "attach: exec-mode pod \"exec-atta…; skeptic: Four attacks run: (1) red-by-adaptation control, (2) unrelated/flaky brine red control, (3) different-behaviour pairing via two narrower phase-gate mutations (P1, P1b), (4) decorative/order-masked as… → HOLDS

**[REFUTED]** Container Attach exec-mode: when executor is set and pod has no exit annotation returns error so engine falls through to Run  `JB-container-055`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. Its counterpart is container-lifecycle.feature 'With no record of the result the step is run again rather than assumed', a feature not in step_changes; Attach is untouched on core.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, (*Container).Attach (exec-mode branch, line ~294-298):; brine RED: Then the step cannot be recovered and must be run again (line 62) — status "failed", error: "expected re-attaching to fail so the engine re-runs the step; it reported success with exit 0, which would…; go RED: container_test.go:2349 — `[FAILED] Expected an error to have occurred. Got: <nil>: nil`. The spec body is: _, err := execContainer.Attach(ctx, "some-process", runtime.ProcessIO{}) Expect(err).To(Have…; skeptic: different-behaviour pairing / narrower mutation (primary); plus red-by-adaptation control and a full reproduction of the recorded pairing on both sides → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Container FindOrCreateContainer failure handling marks the container as failed when Created() fails  `JB-container-056`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. Its covering scenario is worker.feature 'A container that cannot be recorded is left for the collector' — worker.feature is not in step_changes — and worker.go, atc/db/container.go and atc/db/creating_container.go are all byte-identical merge-base to core.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/worker.go, (*Worker).FindOrCreateContainer (line 135-139) — deleted the markContainerAsFailed call on the Created() error path:; brine RED: And the container "test-handle" is left in state "failed" (worker.feature:56) — error verbatim: expected the state the container is left in for "test-handle" to be "failed", got "creating". The three…; go RED: container_test.go:2385, inside STEP "marking the container as failed in the DB": Expect(stateOf("fail-create-handle")).To(Equal(string(atc.ContainerStateFailed))) — [FAILED] Expected <string>: creati…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts), plus red-by-adaptation and unrelated-brine-red controls, plus reproduction of the verifier's own mutation → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Container FindOrCreateContainer failure handling returns error when FindContainer fails  `JB-container-057`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'A lost database connection is reported as a lookup failure', a feature whose ContainerSpec builder changed in the rebase. worker.go and atc/db/worker.go are unchanged on core.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/worker.go, (*Worker).FindOrCreateContainer (line ~103) — swallow the FindContainer lookup error:; brine RED: Then requesting the container fails saying "find container in db" (line 251) — status "failed", error: expected the failure to mention "find container in db", got "create container in db: sql: databa…; go RED: container_test.go:2392 — [FAILED] Expected <*fmt.wrapError>: create container in db: sql: database is closed {msg: "create container in db: sql: database is closed", err: <*errors.errorString>{s: "sq…; skeptic: Three attacks run, all failed to refute: (1) red-by-adaptation — restored Go test with the mutation REVERTED; (2) unrelated/flaky brine red — clean run of container-run.feature with no mutation; (3)… → HOLDS

**[DELETED]** Container FindOrCreateContainer failure handling returns error when CreateContainer fails  `JB-container-058`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'A handle another worker already holds cannot be claimed', a feature in step_changes via steps/container_extra.go and steps/domain.go.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/worker.go, (*Worker).FindOrCreateContainer (line ~115) — swallow the CreateContainer failure:; brine RED: Then requesting the container fails saying "create container in db" (container-run.feature:259) — step_end status "failed", error: "expected the request to fail, it succeeded"; scenario_end status "f…; go RED: [FAILED] Expected an error, got nil In [It] at: .../atc/worker/jetbridge/container_test.go:2407 @ 09/05/26 11:14:19.725 (assertion source: `Expect(err).To(MatchError(ContainSubstring("create containe…; skeptic: Three attacks run, none refuted: (1) red-by-adaptation — restored Go test with the mutation REVERTED; (2) narrower mutation targeting exactly the Go assertion's discriminating clause (the substring "… → HOLDS

**[DELETED]** Container FindOrCreateContainer failure handling recovers a stale creating container by transitioning to created  `JB-container-059`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'A container a crash left half-created is adopted, not duplicated', a feature whose ContainerSpec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/worker.go, func (w *Worker) FindOrCreateContainer:; brine RED: Then requesting the container succeeds -> "expected the request to succeed, it failed: create container in db: insert container: ERROR: duplicate key value violates unique constraint \"containers_han…; go RED: container_test.go:2418 — `[FAILED] Expected success, but got an error: <*fmt.wrapError>: create container in db: insert container: ERROR: duplicate key value violates unique constraint "containers_ha…; skeptic: Four attacks run, all failed: (1) narrower mutation that breaks ONLY the Go test's state-transition assertion and leaves the success/count assertions passing; (2) red-by-adaptation (restored test gre… → HOLDS

**[DELETED]** Container FindOrCreateContainer failure handling marks stale creating container as failed when Created() fails on recovery  `JB-container-060`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-run.feature 'A half-created container that still cannot be completed is left for the collector', a feature in step_changes.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/worker.go, (*Worker).FindOrCreateContainer — deleted the markContainerAsFailed call on the Created() error path:; brine RED: And the container row "stale-fail-handle" is left in state "failed" (line 276) -> status "failed", error: expected the container's state for "stale-fail-handle" to be "failed", got "creating". The tw…; go RED: [FAILED] Expected <string>: creating to equal <string>: failed In [It] at: atc/worker/jetbridge/container_test.go:2444 (STEP: marking the stale container as failed). Summary with mutation: 'Ran 1 of…; skeptic: Ran four attacks in my own worktree (rv-jb-container-060-skeptic @ 27d81692fa): (1) red-by-adaptation — reverted mutation with the restored Go test in place; (2) unrelated/flaky brine red — clean run… → HOLDS

**[INERT]** Concurrent container operations handles concurrent SetProperty and Properties without races  `JB-container-061`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. No brine counterpart exists (grepping the branch's features for concurrency/race hits only the daemon features), and neither SetProperty nor Properties changed on core — so nothing but the conflict makes this row unsafe to close.
  - re-verified 2026-09-05 (rebase onto core): INERT — mutation atc/worker/jetbridge/container.go, (*Container).Properties (line 304 on 27d81692fa) — recorded mutation, applied verbatim:; brine GREEN: none — under the recorded mutation the run reported {"type":"run_end","features":1,"scenarios":7,"passed":7,"failed":0,"duration_ms":4578}; 'What a container records can be read back' ended status "p…; go GREEN: none under the recorded mutation. 5 independent runs with the recorded mutation: all `Ran 1 of 92 Specs` / `SUCCESS! -- 1 Passed | 0 Failed | 0 Pending | 91 Skipped`, exit 0, zero 'fatal error'/'conc…; skeptic: not reached — the verifier stopped at INERT
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[GAP]** Concurrent container operations creates independent containers concurrently without interference  `JB-container-062`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. No brine counterpart; the nearest sentence, worker.feature 'A container is recorded before any pod exists', is single-threaded and worker.feature is not in step_changes. NewWorker and FindOrCreateContainer are unchanged on core.
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/worker.go, func (w *Worker).FindOrCreateContainer — a concurrency-ONLY gate (sequentially inert: one caller in flight => no error, just 50ms slower; two or more simultaneous call…; brine GREEN: none — scenario_end status=passed; worker.feature 31/31 scenarios passed and container-pod.feature 44/44 scenarios passed WITH the mutation applied. No scenario anywhere in the feature set reddened:…; go RED: [FAILED] goroutine 0 should succeed Unexpected error: <*errors.errorString | 0x140006b67c0>: concurrent find-or-create container { s: \"concurrent find-or-create container\", } occurred In [It] at: .…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[GAP]** Concurrent container operations handles concurrent Run and pod creation on the fake clientset  `JB-container-063`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (c) — Rule (c) only: conflict file. No brine counterpart for concurrent pod creation; the nearest is container-run.feature 'A container the runtime created is counted', which counts one. Container.createPod is unchanged on core.
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/container.go, (*Container).createPod (line 379 on 27d81692fa) — force a fixed pod name so concurrent Runs collide:; brine GREEN: none — scenario_end status "passed"; all three steps passed ("Given a jetbridge worker on a fake Kubernetes cluster", "When a step is run on it", "Then the operator sees 1 container created and 0 fai…; go RED: [FAILED] goroutine 0 Run should succeed Unexpected error: <*fmt.wrapError | 0x140006a89c0>: create pod: pods "jetbridge-pod" already exists { msg: "create pod: pods \"jetbridge-pod\" already exists",…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Run with sidecar containers when no sidecars are configured creates a pod with only the main container  `JB-container-064`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'A step with no sidecars runs alone', a feature whose ContainerSpec literal changed in the rebase. Not (a)/(d): `git log -S buildSidecarContainers aef2244a63..5133d0ddbc` returns no commits.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func buildSidecarContainers (line 536-539) — the recorded target exists unchanged on the rebased head.; brine RED: Then the pod runs 1 containers (line 147) — status "failed", error: `expected 1 containers, found 2: [main mutant]`. scenario_end: {"type":"scenario_end","name":"A step with no sidecars runs alone","…; go RED: container_test.go:2680 — `Expect(pod.Spec.Containers).To(HaveLen(1))`. Ginkgo: `[FAILED] Expected <[]v1.Container | len:2, cap:2>: [ { Name: "main", Image: "busybox", Command: ["/bin/sh"], Args: ["-c…; skeptic: Four attacks run and measured, none refuted: (1) red-by-adaptation — clean restored Go test unmutated; (2) unrelated/pre-existing brine red — clean run of the whole feature, manifest-filtered; (3) na… → HOLDS

**[REFUTED]** Run with sidecar containers when one sidecar is configured creates a pod with the main container and the sidecar  `JB-container-065`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'A sidecar runs alongside the step and shares its working set', a feature whose ContainerSpec builder changed in the rebase. Not (a): buildSidecarContainers and buildVolumeMounts' non-cache paths are unchanged on core.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func buildSidecarContainers (line ~552) — dropped `VolumeMounts: mounts` from the sidecar corev1.Container literal. `mounts` is still computed, so a `_ = mounts` li…; brine RED: And the sidecar "postgres" sees the same volumes as the step (line 159) — error: the step sees "/tmp/build/workdir" but sidecar "postgres" does not; go RED: container_test.go:2755, under STEP "giving the sidecar the same volume mounts as the main container": `Expect(sidecar.VolumeMounts).To(Equal(mainMounts))` -> [FAILED] Expected <[]v1.VolumeMount | len…; skeptic: different-behaviour pairing (narrower mutations that break only what the Go It asserts), plus red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Run with sidecar containers when multiple sidecars are configured creates a pod with the main container and all sidecars  `JB-container-066`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'Several sidecars all run', a feature whose ContainerSpec literal changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/container.go, func buildSidecarContainers (line ~587 on 27d81692fa) — drop every sidecar after the first:; brine RED: Then the pod runs 3 containers (line 169) — status "failed", error: `expected 3 containers, found 2: [main postgres]`. scenario_end: {"type":"scenario_end","name":"Several sidecars all run","status":…; go RED: container_test.go:2804 — `Expect(pod.Spec.Containers).To(HaveLen(3))`: `[FAILED] Expected <[]v1.Container | len:2, cap:2>: [ {Name: "main", Image: "busybox", ...}, {Name: "redis", Image: "redis:7", P…; skeptic: Two narrower mutations that break only the clauses the Go test asserts beyond the count (declaration order), plus red-by-adaptation and unrelated-brine-red controls. The Go It asserts FOUR clauses —… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[GAP]** Run with sidecar containers when a sidecar has resources, command, args, and workingDir maps all sidecar fields to the K8s container spec  `JB-container-067`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): its nearest counterparts are container-pod.feature 'A sidecar runs alongside the step and shares its working set' and 'A sidecar that names a working directory keeps its own', both in a feature whose spec builder changed in the rebase — but the row's evidence itself records that no s…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/container.go, func buildSidecarResourceRequirements — deleted the entire Limits block (recipe mutation (1)):; brine GREEN: none — no step failed. All 44 scenarios in container-pod.feature reported scenario_end status=passed under mutation (1), and again under the stacked mutation (1)+(2). Every sidecar scenario passed: "…; go RED: container_test.go:2867, inside STEP "mapping resource limits": [FAILED] Expected <string>: 0 to equal <string>: 500m (the assertion is `Expect(sidecar.Resources.Limits.Cpu().String()).To(Equal("500m"…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[REFUTED]** Run with sidecar containers when sidecars are configured alongside the artifact store includes main and user sidecar containers (no artifact-helper sidecar)  `JB-container-068`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): its nearest counterpart is container-pod.feature 'A sidecar runs alongside the step and shares its working set', in a feature whose ContainerSpec builder changed in the rebase. The row's own premise is weak — NewConfig("test-namespace","") leaves ArtifactDaemonHostPath unset so no ba…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation Target still exists and is unmoved: `(*Container).buildPod` in `atc/worker/jetbridge/container.go`, immediately after the `buildSidecarContainers` append (line 465 on 27d81692fa). MUTATION A — the re…; brine RED: Then the pod runs 2 containers (line 157) -> status "failed", error: `expected 2 containers, found 3: [main postgres artifact-helper]`; go RED: container_test.go:2921 -- [FAILED] Expected <[]string | len:3, cap:4>: ["main", "redis", "artifact-helper"] to equal <[]string | len:2, cap:2>: ["main", "redis"]; skeptic: different-behaviour pairing (narrower mutation targeting only what the Go It asserts), plus red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[DELETED]** Run with sidecar containers when a sidecar has no workingDir and the main container has a dir inherits the main container's working directory  `JB-container-069`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'A sidecar inherits the step's working directory' (@SC-03), a feature whose ContainerSpec literal changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func buildSidecarContainers (line ~550):; brine RED: Then the sidecar "helper" works in "/tmp/build/workdir" -> error: expected the sidecar's working directory for "helper" to be "/tmp/build/workdir", got ""; go RED: [FAILED] Expected <string>: to equal <string>: /tmp/build/workdir In [It] at: .../atc/worker/jetbridge/container_test.go:2975 (preceded by STEP labels "main container has the expected working dir" th…; skeptic: Four attacks run, all failed: (1) narrower mutation / different-behaviour pairing — kept the fallback but gave it a wrong NON-EMPTY value, to see whether either side merely asserts non-emptiness or w… → HOLDS

**[DELETED]** Run with sidecar containers when a sidecar specifies its own workingDir uses the sidecar's own workingDir instead of inheriting  `JB-container-070`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): it migrated to container-pod.feature 'A sidecar that names a working directory keeps its own' (@SC-03), a feature whose spec builder changed in the rebase.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/container.go, func buildSidecarContainers (~line 550):; brine RED: Then the sidecar "helper" works in "/opt/helper" -> expected the sidecar's working directory for "helper" to be "/opt/helper", got "/tmp/build/workdir"; go RED: [FAILED] Expected <string>: /tmp/build/workdir to equal <string>: /app In [It] at: .../atc/worker/jetbridge/container_test.go:3017 (assertion source: `Expect(sidecar.WorkingDir).To(Equal("/app"))`); skeptic: Ran four: (1) red-by-adaptation — restored Go test with the mutation reverted; (2) mutation-not-the-recorded-one — re-derived the recipe edit myself and diffed; (3) decorative/order-masked assertion… → HOLDS

**[GAP]** Run with sidecar containers when sidecars are configured in exec-mode (pause pod) creates a pause pod with sidecar containers  `JB-container-071`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): its two nearest counterparts, container-pod.feature 'A sidecar runs alongside the step and shares its working set' and container-run.feature 'With an exec transport the pod is a placeholder the step runs inside', are both in features whose ContainerSpec builders changed in the rebase…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/container.go, func (c *Container) buildPod — drop sidecars from pause pods only:; brine GREEN: none — no step failed. container-pod.feature: 44 scenarios, 44 passed, 0 failed (run_end exit 0). container-run.feature: 19 scenarios, 19 passed, 0 failed (run_end exit 0). The named scenario emitted…; go RED: container_test.go:3071, under By("the sidecar is present in the pod") — Expect(pod.Spec.Containers).To(HaveLen(2)): [FAILED] Expected <[]v1.Container | len:1, cap:1>: [ { Name: "main", Image: "busybo…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`

**[GAP]** Run with sidecar containers when a sidecar image has a docker:/// prefix (image_artifact handoff) strips Concourse URL prefixes from sidecar images in the pod spec  `JB-container-072`
  - recorded evidence: PER-FILE (deletion commit `c67193f78f`; nothing names this test alone)
  - rebase impact: IMPACTED (b, c) — Rule (c): conflict file. Rule (b): its inferred counterpart is container-spec.feature Scenario Outline 'A Concourse image URL prefix is stripped', and container-spec.feature is in step_changes (steps/container_spec.go, steps/domain.go). The inference is also wrong on the merits — see the recipe.
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/container.go : buildSidecarContainers; brine GREEN: none - all 4 Examples rows passed under the mutation. container-spec.feature run_end: {"features":1,"scenarios":7,"passed":7,"failed":0}. container-pod.feature run_end: {"features":1,"scenarios":44,"…; go RED: [FAILED] Expected <string>: "docker..." to equal | <string>: "us-doc..." In [It] at: .../atc/worker/jetbridge/container_test.go:3131 -- i.e. Expect(pod.Spec.Containers[1].Image).To(Equal("us-docker.p…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/container_restored_test.go`


## secret_env_test.go

Deleted by `65a62160d8` — "Delete resource, secret_env and artifact_integration; keep supervisor_script". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** SecretEnv in Pod Spec emits ValueFrom.SecretKeyRef for env vars in SecretEnv  `JB-secret_env-000`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Everything this test pins is byte-identical base->core: applySecretRefs, envVars and splitEnvVar in atc/worker/jetbridge/container.go diff clean between aef2244a63 and 5133d0ddbc, worker.go is unchanged, and NewConfig in config.go is outside every core hunk (core only appended capability-TTL consts/fields). The three…

**[DELETED]** SecretEnv in Pod Spec keeps all env vars as literal values when SecretEnv is nil  `JB-secret_env-001`
  - recorded evidence: PER-FILE (deletion commit `65a62160d8`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same core surface as JB-secret_env-000 and the same negative result on all four rules: the len(secretEnv)==0 early return in applySecretRefs is inside the byte-identical function body, envVars/splitEnvVar are identical, worker.go and the db helpers it exercises (db/container_owner.go, db/worker.go, db/worker_factory.g…


## resource_test.go

Deleted by `65a62160d8` — "Delete resource, secret_env and artifact_integration; keep supervisor_script". Recorded evidence granularity: **none**. The deletion commit named no mutation at all.

**[DELETED]** Resource Step Execution get step persists the created get container  `JB-resource-000`
  - recorded evidence: NONE (deletion commit `65a62160d8` names no mutation)
  - rebase impact: NOT IMPACTED — The test only calls Worker.FindOrCreateContainer and reads the containers row; atc/worker/jetbridge/worker.go (FindOrCreateContainer, buildVolumeMountsForSpec, newContainer) and the db container files are byte-identical merge-base->core (empty `git diff --stat`), no core migration (1773105505-09) touches the container…

**[DELETED]** Resource Step Execution get step creates a pause Pod and execs /opt/resource/in with stdin/stdout  `JB-resource-001`
  - recorded evidence: NONE (deletion commit `65a62160d8` names no mutation)
  - rebase impact: NOT IMPACTED — Pins pod image concourse/git-resource, a pause (non-script) pod command, and the ExecInPod call shape (namespace/pod/container "main", command, stdin, stdout, exit 0). The only core changes on that path are inert here: buildArtifactInitContainers (1e023e7ca4) keeps its `storageBackend == nil -> (nil,nil)` early return…

**[DELETED]** Resource Step Execution get step returns non-zero exit code on resource failure  `JB-resource-002`
  - recorded evidence: NONE (deletion commit `65a62160d8` names no mutation)
  - rebase impact: NOT IMPACTED — Pins that ExecExitError{ExitCode:1} comes back as ProcessResult{ExitStatus:1} with a nil error. process.go's only core diff is the new podStartupTimeout helper plus waitForRunning delegating to it; execProcess.Wait's errors.As branch, uploadOutputsToArtifactStore, annotateExitStatus and SetProperty are untouched, and…

**[DELETED]** Resource Step Execution put step creates a Pod with input volumes and execs /opt/resource/out  `JB-resource-003`
  - recorded evidence: NONE (deletion commit `65a62160d8` names no mutation)
  - rebase impact: NOT IMPACTED — buildVolumeMounts does carry behaviour_changed=true (0d336e062b), but the change does not touch what this test pins: I read core's buildVolumeMounts at 5133d0ddbc and the Inputs loop that emits VolumeMount{MountPath: input.DestinationPath} is byte-identical to the merge-base — all three edits (both auto-detect arms, t…

**[DELETED]** Resource Step Execution check step creates a Pod and execs /opt/resource/check with stdin/stdout  `JB-resource-004`
  - recorded evidence: NONE (deletion commit `65a62160d8` names no mutation)
  - rebase impact: NOT IMPACTED — Pins the check exec command with no args plus stdin/stdout passthrough and exit 0. Container.stepVolume (including its ContainerTypeCheck emptyDir guard), Container.Run, createPausePod, GeneratePodName, resolveImage, execProcess.Wait and ExecInPod are all unchanged merge-base->core (container.go's only diffs are build…

**[DELETED]** Resource Step Execution check step does not set a process ID for check steps  `JB-resource-005`
  - recorded evidence: NONE (deletion commit `65a62160d8` names no mutation)
  - rebase impact: NOT IMPACTED — Pins that with an empty ProcessSpec.ID the process identifies itself by the container handle. Container.Run's `processID := c.handle` default, newExecProcess and execProcess.ID are untouched on core (container.go changed only in the three cache/init-container symbols; -S greps for newExecProcess/GeneratePodName return…


## storage_daemonset_durable_test.go

Deleted by `5110801cd7` — "Delete storage_daemonset_durable_test.go — 10 of 10 tests evidenced". Recorded evidence granularity: **per-test**.

**[DELETED]** TestFindResourceCacheDoesNotWarmOnALocalHit  `JB-storage_daemonset_durable-000`
  - recorded evidence: PER-TEST — NAMED IN THE COMMIT BODY: "Nine of these ten reddened under the seven designed mutations. The tenth, TestFindResourceCacheDoesNotWarmOnALocalHit, never did — not because it is uncovered but because no mutation aimed at it. One more mutation, removing the early return that makes a local hit skip the warm, reddens it AN…
  - rebase impact: NOT IMPACTED — The recorded mutation target, (*DaemonSetBackend).FindResourceCache's local-hit early return, is hash-identical between aef2244a63 and 5133d0ddbc (sha256 2d284265af1443e7, 43 lines on both), as is bindProbed (0017e3803578dfa2); daemon_client.go, daemon_warm.go and atc/metric are byte-identical (empty git diff). The me…

**[DELETED]** TestFindResourceCacheDoesNotWarmWithoutAContentKey  `JB-storage_daemonset_durable-001`
  - recorded evidence: PER-TEST — Not individually named. Covered by the commit body's per-test aggregate "Nine of these ten reddened under the seven designed mutations" — the seven mutations are not enumerated anywhere in the commit or MIGRATION-EVIDENCE.md, so this test's specific reddening mutation is unrecorded. Its brine twin ("A cache with no co…
  - rebase impact: NOT IMPACTED — The pinned guard `if durableKey == "" || !probe.DurableCapable` lives in FindResourceCache, which is hash-identical merge-base to core; DaemonClient.ProbeResourceCache and WarmResourceCache are in daemon_client.go / daemon_warm.go, both byte-identical (empty `git diff --stat aef2244a63 5133d0ddbc`). No core commit bet…

**[DELETED]** TestFindResourceCacheDoesNotWarmAnUnadvertisedDaemon  `JB-storage_daemonset_durable-002`
  - recorded evidence: PER-TEST — Not individually named; covered by "Nine of these ten reddened under the seven designed mutations". Brine twin present pre-deletion: "A daemon that predates the durable tier is never asked to warm" (step-closing.feature:183), whose comment reproduces this test's rationale word for word.
  - rebase impact: NOT IMPACTED — The `!probe.DurableCapable` half of the guard is in the hash-identical FindResourceCache, and DurableTierHeader plus ProbeResult.DurableCapable live in daemon_warm.go / daemon_client.go, both byte-identical on core. `git log -SDurableTierHeader` and `-SProbeResourceCache` over aef2244a63..5133d0ddbc return nothing. Br…

**[DELETED]** TestFindResourceCacheWarmsOnAMiss  `JB-storage_daemonset_durable-003`
  - recorded evidence: PER-TEST — Not individually named; covered by "Nine of these ten reddened under the seven designed mutations". Brine twin pre-deletion: "A cache that is only in durable storage is restored and its bytes reach the build" (step-closing.feature:194), whose comment says "The ginkgo case asserted `restores == 1`; what a get step expe…
  - rebase impact: NOT IMPACTED — The whole warm path is untouched: FindResourceCache is hash-identical, and warmOwners, WarmResourceCache and defaultWarmTimeout are in daemon_warm.go, byte-identical merge-base to core. NewDaemonSetVolumeFromIP is in volume_daemonset.go, whose only core change (0d336e062b/7fdfd93cf5) is InitializeTaskCache's jobID ->…

**[DELETED]** TestFindResourceCacheSuppressesAfterAFailedWarm  `JB-storage_daemonset_durable-004`
  - recorded evidence: PER-TEST — Not individually named; covered by "Nine of these ten reddened under the seven designed mutations". Brine twin pre-deletion: "A failed warm is not retried on every scheduler tick" (step-closing.feature:211), whose comment is a near-verbatim copy of this test's doc comment and adds the discriminator "Without suppressio…
  - rebase impact: NOT IMPACTED — warm_negative_cache.go (suppressed, suppress, warmSuppressionWindow) is byte-identical between aef2244a63 and 5133d0ddbc, as is the suppression branch inside the hash-identical FindResourceCache and the atc/metric counters. `git log -SwarmNegativeCache -SDurableWarmSuppressed` over the range returns no commits. Brine…

**[DELETED]** TestRegisterAliasSendsTheDurableKeyAsGiven  `JB-storage_daemonset_durable-005`
  - recorded evidence: PER-TEST — Not individually named; covered by "Nine of these ten reddened under the seven designed mutations". Two brine twins pre-deletion, and the feature comments explicitly cite "the ginkgo case": "An artifact filed under a content key can be restored on a node that never had it" (step-closing.feature:221 — "The ginkgo case…
  - rebase impact: NOT IMPACTED — DaemonClient.RegisterAlias, daemonEndpoints, daemonIPs and NewDaemonClient all live in daemon_client.go, which is byte-identical merge-base to core (empty diff), and `git log -SRegisterAlias` over aef2244a63..5133d0ddbc under atc/worker/jetbridge returns nothing — the POST /register payload shape is unchanged. The two…

**[DELETED]** TestFindResourceCacheBindsAVolumeThatCanPeerFallBack  `JB-storage_daemonset_durable-006`
  - recorded evidence: PER-TEST — Not individually named; covered by "Nine of these ten reddened under the seven designed mutations". Brine twins pre-deletion, and the feature comment names this Go assertion directly: "The ginkgo case asserted `dsv.daemonClient != nil`, an unexported field on a struct — unreachable from outside the package and, more t…
  - rebase impact: NOT IMPACTED — bindProbed is hash-identical (0017e3803578dfa2, 6 lines on both revisions), and NewDaemonSetVolumeFromIP (volume_daemonset.go:53), SetDaemonClient (:190) and fetchArtifactWithPeerFallback are all outside the one hunk core changed in that file — the sole change is InitializeTaskCache's identity signature and the atc im…

**[DELETED]** TestProbeSkipsEndpointsTheAPIMarkedNotReady  `JB-storage_daemonset_durable-007`
  - recorded evidence: PER-TEST — Not individually named; covered by "Nine of these ten reddened under the seven designed mutations". Brine twin pre-deletion: "A daemon pod the API marked not ready is not a cache hit" (step-closing.feature:250), whose comment reproduces this test's rationale and adds the discriminator "The pod HOLDS the cache and woul…
  - rebase impact: NOT IMPACTED — The readiness filter (`if ep.Conditions.Ready != nil && !*ep.Conditions.Ready { continue }`) is in daemonEndpoints in daemon_client.go, which is byte-identical between aef2244a63 and 5133d0ddbc; `git log -SdaemonEndpoints` over the range returns nothing. This test constructs only a DaemonClient, so nothing in the stor…

**[DELETED]** TestProbeStillUsesReadyEndpoints  `JB-storage_daemonset_durable-008`
  - recorded evidence: PER-TEST — Not individually named as a Go test, but its behaviour is explicitly absorbed by a named brine scenario. step-closing.feature:150-157 (comment above "A cache already on the node is served without touching the store"): "This is also the 'a ready endpoint is still used' case — the endpoint the cluster publishes carries…
  - rebase impact: NOT IMPACTED — Same untouched code as row 007 — the positive arm of the daemonEndpoints readiness filter in the byte-identical daemon_client.go, exercised through a bare DaemonClient with no backend involved. The scenario that absorbs it, 'A cache already on the node is served without touching the store' (step-closing.feature:158),…

**[DELETED]** TestFindResourceCacheCountsEveryOutcomeExactlyOnce  `JB-storage_daemonset_durable-009`
  - recorded evidence: PER-TEST — Not individually named in the commit body; covered by "Nine of these ten reddened under the seven designed mutations". It is however the one test with an explicit written DISPOSITION in the brine feature file rather than a scenario of its own — step-closing.feature:282-288: "DISPOSITION — 'the four counters partition…
  - rebase impact: NOT IMPACTED — All four metric.Inc sites are inside the hash-identical FindResourceCache, and atc/metric is byte-identical between the two revisions (empty `git diff --stat aef2244a63 5133d0ddbc -- atc/metric/`), so Counter.Delta and the four counters are unchanged; warm_negative_cache.go is likewise byte-identical. Its evidence is…


## watch_test.go

Deleted by `d432501a17` — "Delete watch_test.go; convert its two spy specs into working doubles". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** watchPod returns a watch.Interface filtered to a specific pod by field selector  `JB-watch-000`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — (a) the target jetbridge.WatchPod lives in atc/worker/jetbridge/watch.go, which is byte-identical between aef2244a63 and 5133d0ddbc (empty `git diff --stat`, and `git log aef2244a63..5133d0ddbc -- watch.go` is empty), so the FieldSelector construction this test pins is untouched; (d) `git log -SWatchPod -- atc/worker/…

**[DELETED]** watchPod passes resourceVersion to the watch options  `JB-watch-001`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — (a) the ResourceVersion pass-through inside jetbridge.WatchPod is in watch.go, byte-identical merge-base to core; (d) no core commit in aef2244a63..5133d0ddbc touches WatchPod (`-SWatchPod` empty). (b) the named brine artefacts (features/pod-watch.feature, steps/podwatch.go, steps/podwatch_fidelity.go) are not in step…

**[DELETED]** PodWatcher Next returns initial pod state from Get() on first call  `JB-watch-002`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — (a) NewPodWatcher and the needsInitialSync/Get branch of PodWatcher.Next are entirely in watch.go, which core did not touch (empty diff and empty path log); `-SPodWatcher`, `-SinitialPod` and `-SneedsInitialSync` over atc/worker/jetbridge all return nothing, so (d) fails too. (b) pod-watch.feature and both podwatch st…

**[DELETED]** PodWatcher Next returns pod events from watch channel on subsequent calls  `JB-watch-003`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — (a) the watch-channel read branch and the event.Object.(*corev1.Pod) assertion are in watch.go, byte-identical between merge-base and core; go.mod is also unchanged over the range, so the client-go watch types this test rides on did not move. (d) no core commit matches -SWatchPod/-SPodWatcher in atc/worker/jetbridge.…

**[DELETED]** PodWatcher Next re-establishes the watch when the channel closes  `JB-watch-004`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — (a) the closed-channel re-establish branch of PodWatcher.Next is in watch.go, untouched on core; (d) `git log -SPodWatcher -- atc/worker/jetbridge/` and `-SWatchPod` over aef2244a63..5133d0ddbc are both empty. (b) the brine counterparts (pod-watch.feature 'A dropped connection does not lose the change that happened du…

**[DELETED]** PodWatcher Next falls back to Get() if watch re-establishment fails consecutively  `JB-watch-005`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — This is the only row whose source_functions name a file core DID change (maxConsecutiveAPIErrors at atc/worker/jetbridge/process.go:153), so I checked the hunks: core's only edits to process.go (1164f9db3d) are the new podStartupTimeout helper near line 536 and waitForRunning at ~1047 delegating to it; the const block…

**[DELETED]** PodWatcher Next passes lastResourceVersion when reconnecting to avoid missed events  `JB-watch-006`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — (a) lastResourceVersion tracking and its re-use on the reconnecting WatchPod call are both in watch.go, byte-identical merge-base to core; (d) `-SlastResourceVersion -- atc/worker/jetbridge/` over aef2244a63..5133d0ddbc is empty. (b) the counterpart scenario 'A reconnect resumes from the last version, so the finish is…

**[DELETED]** PodWatcher Next delivers rapid pod updates without losing the final state  `JB-watch-007`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — (a) the one-event-per-Next ordering lives in PodWatcher.Next in watch.go, which core did not modify (empty diff, empty path log); (d) no core commit in the range matches -SPodWatcher/-SWatchPod under atc/worker/jetbridge. (b) the counterpart 'A burst of updates does not leave the step on a stale state' (pod-watch.feat…

**[DELETED]** PodWatcher Next returns ErrPodDeleted when a Deleted event is received  `JB-watch-008`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — (a) both ErrPodDeleted (watch.go:18) and the watch.Deleted branch of Next are in watch.go, byte-identical between merge-base and core; (d) `-SErrPodDeleted -- atc/worker/jetbridge/` over the range is empty. (b) the counterpart 'A pod deleted out from under the step is reported, not waited on' (pod-watch.feature line 5…

**[DELETED]** PodWatcher Next returns error when context is cancelled  `JB-watch-009`
  - recorded evidence: PER-FILE (deletion commit `d432501a17`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — (a) the non-blocking ctx.Done() check at the top of PodWatcher.Next is in watch.go, untouched on core; the one core change in this package's process.go (1164f9db3d, waitForRunning delegating to the new podStartupTimeout) is a startup-timeout refactor that does not reach PodWatcher's cancellation check, and (d) `-SPodW…


## process_test.go

Deleted by `d3a0d151ab` — "Delete process_test.go; fix RF-07 by moving it to the chain that can fail". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

Restored tests from this file live in `atc/worker/jetbridge/process_restored_test.go`.

**[DELETED]** Process Wait when the Pod succeeds returns exit status 0  `JB-process-000`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins Process.Wait/pollUntilDone/podExitCode on the direct chain; process.go's only change since the merge-base is 1164f9db3d's podStartupTimeout extraction, which lives on the exec chain and is never reached here. container.go's changed symbols (buildArtifactInitContainers, buildVolumeMounts, stableCacheKey) are unrea…

**[DELETED]** Process Wait when the Pod fails with a non-zero exit code returns the exit code without an error  `JB-process-001`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — podExitCode and pollUntilDone are byte-identical between aef2244a63 and 5133d0ddbc (`git log -SpodExitCode aef2244a63..5133d0ddbc -- atc/worker/jetbridge/` is empty). Nothing on core changes that a PodFailed pod with a terminated main container is a result rather than an error.

**[DELETED]** Process Wait when the context is cancelled returns the context error and deletes the Pod  `JB-process-002`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The ctx.Done branch of Process.Wait and its Pods().Delete call are untouched by the single process.go commit in range (1164f9db3d, which only extracts podStartupTimeout on the exec path). The PE-10 select race the evidence records is a pre-existing product defect on both revisions, not a rebase effect.

**[DELETED]** Process Wait pod failure state detection (direct mode) detects ImagePullBackOff as a terminal failure  `JB-process-003`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — isPodFailedFast and terminalWaitingReasons have no commits in aef2244a63..5133d0ddbc, and atc/metric/ is byte-identical, so the K8sImagePullFailures increment is unchanged too. failure-priority.feature and its steps are absent from rebase_result.step_changes.

**[DELETED]** Process Wait pod failure state detection (direct mode) detects ErrImagePull as a terminal failure  `JB-process-004`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched pair as the ImagePullBackOff case: isPodFailedFast/terminalWaitingReasons unchanged on core, metric package byte-identical, and the @RF-04 outline row in failure-priority.feature was not among the features the rebase had to change.

**[DELETED]** Process Wait pod failure state detection (direct mode) detects OOMKilled as a terminal failure  `JB-process-005`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — isPodOOMKilled and writePodDiagnostics have no commits in the range; process.go's whole diff is the 13-line podStartupTimeout extraction. failure-priority.feature @RF-01 is not in step_changes.

**[DELETED]** Process Wait pod failure state detection (direct mode) detects OOMKilled from last termination state (restarted container)  `JB-process-006`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The LastTerminationState branch of isPodOOMKilled is unchanged on core (`git log -SisPodOOMKilled` over the range is empty), and failure-priority.feature @RF-09 was untouched by the rebase.

**[DELETED]** Process Wait pod failure state detection (direct mode) does not detect OOMKilled when termination reason is different  `JB-process-007`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — A negative on isPodOOMKilled plus podExitCode; neither symbol has a commit in aef2244a63..5133d0ddbc. Nothing on core touches the reason=='Error' path this pins.

**[DELETED]** Process Wait pod failure state detection (direct mode) detects pod eviction as a terminal failure  `JB-process-008`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — interruptionErrorForPod/interruptionReasonForPod/preferContextCancellation have no commits in range and `git log -SInterruptionEvicted aef2244a63..5133d0ddbc -- atc/` is empty, so the typed runtime.InterruptionError this pins is unchanged. The recorded RF-05 wording DRIFT ('pod interrupted: evicted' at process.go:1290…

**[DELETED]** Process Wait pod failure state detection (direct mode) detects external pod deletion as a terminal failure  `JB-process-009`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — ErrPodDeleted and PodWatcher.Next live in atc/worker/jetbridge/watch.go, which `git diff --stat aef2244a63 5133d0ddbc` reports as byte-identical; writePodDiagnostics/writeNodeDiagnostics have no commits either. pod-watch.feature and failure-priority.feature @RF-06 were untouched by the rebase.

**[DELETED]** Process Wait pod failure state detection (direct mode) detects CrashLoopBackOff as a terminal failure  `JB-process-010`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the terminal-waiting-reason check running while the phase is still Running; isPodFailedFast and terminalWaitingReasons are unchanged on core and the @RF-04 crashloop row was not a rebase-affected feature.

**[DELETED]** Process failure diagnostics in build logs writes pod conditions and waiting reasons to stderr on ImagePullBackOff  `JB-process-011`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — writePodDiagnostics has no commits in aef2244a63..5133d0ddbc, so the stderr text this pins is byte-identical on core. pod-lifecycle.feature @RF-10, which strengthened it with the PodScheduled assertion, is absent from rebase_result.step_changes.

**[DELETED]** Process failure diagnostics in build logs includes sidecar container status in diagnostics (first of two identically-named Its; sidecar "my-sidecar", image "bad-sidecar:latest")  `JB-process-012`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — writePodDiagnostics and isPodFailedFast are unchanged on core, and container.go's diff touches no sidecar wiring (`git diff ... -- container.go | grep idecar` is empty; `git log -SSidecarConfig -- atc/` over the range is empty). The @RF-10 @SC-08 outline that absorbed this row was not rebase-affected.

**[DELETED]** Process failure diagnostics in build logs includes sidecar container status in diagnostics (second of two identically-named Its; sidecar "redis-sidecar", image "redis:bad-tag")  `JB-process-013`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Identical requirement to the line-446 twin with different fixture strings; same untouched symbols (writePodDiagnostics, isPodFailedFast) and the same unaffected pod-lifecycle.feature outline. Nothing on core touches it.

**[DELETED]** Process failure diagnostics in build logs writes eviction reason to stderr  `JB-process-014`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — writePodDiagnostics and interruptionErrorForPod have no commits in range; the kubelet message text it greps for comes straight from the pod fixture. pod-lifecycle.feature and failure-priority.feature @RF-05 were untouched by the rebase.

**[DELETED]** Process failure diagnostics in build logs includes node name in diagnostics when available  `JB-process-015`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The 'Node:' line in writePodDiagnostics and writeNodeDiagnostics are both unchanged on core (no commits for either symbol in aef2244a63..5133d0ddbc), and pod-lifecycle.feature @RF-10 @RF-11 is not in step_changes.

**[DELETED]** Process failure diagnostics in build logs includes container termination message and restart history in diagnostics  `JB-process-016`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Every string it asserts is produced by writePodDiagnostics/isPodOOMKilled, neither of which core touched. The counterpart @RF-10 scenario in pod-lifecycle.feature survived the rebase unedited.

**[DELETED]** Process failure diagnostics in build logs writes node diagnostics on eviction showing pressure conditions  `JB-process-017`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — writeNodeDiagnostics (including the spot-label and pressure-condition branches) has no commits in range, and the Nodes().Get call path is untouched. pod-lifecycle.feature @RF-11 was not rebase-affected.

**[DELETED]** Process failure diagnostics in build logs writes node diagnostics showing cordoned status  `JB-process-018`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The Spec.Unschedulable branch of writeNodeDiagnostics is unchanged on core; nothing in the 13-line process.go diff or anywhere else in the package touches the 'cordoned (unschedulable)' / 'node may be draining' text.

**[DELETED]** Process failure diagnostics in build logs handles node not found gracefully in diagnostics  `JB-process-019`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The Nodes().Get error path of writeNodeDiagnostics has no commits in aef2244a63..5133d0ddbc, so the graceful-degradation text is byte-identical, and pod-lifecycle.feature @RF-11 was untouched by the rebase.

**[DELETED]** Process pod startup timeout times out waitForRunning after the configured duration  `JB-process-020`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: IMPACTED (a, d) — (a)+(d): 1164f9db3d rewrote the exact line this test pins — (*execProcess).waitForRunning's `timeout := ...` now delegates to the new podStartupTimeout(cfg) helper instead of the inline zero-check, and core_changed_symbols marks both behaviour_changed=true. The file-level both-red evidence rested on mutating source te…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/process.go, func (*execProcess).waitForRunning (recipe MUTATION 1, applied to current core source at lines 1151/1153):; brine RED: Then the step fails saying "timed out" (line 170) -> status failed; error: expected the failure to mention "timed out", got "waiting for pod running: pod did not start waiting for pod to start (timeo…; go RED: [FAILED] Expected <string>: waiting for pod running: pod did not start waiting for pod to start (timeout: 200ms, phase: ) to contain substring <string>: timed out In [It] at: .../atc/worker/jetbridge…; skeptic: Four attacks run, all failed to refute: (1) red-by-adaptation; (2) unrelated/flaky brine red; (3) different-behaviour pairing probed with a NARROWER single-statement mutation; (4) mutation-not-the-re… → HOLDS

**[DELETED]** Process pod startup timeout writes diagnostics to stderr on timeout  `JB-process-021`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: IMPACTED (a, d) — (a)+(d): same 1164f9db3d change to (*execProcess).waitForRunning. This It is the second half of the same RF-08 requirement — it asserts the diagnostics written at the moment the startup deadline (now computed by podStartupTimeout) expires, so the mutation the file-level evidence stood on has moved.
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/process.go, func (*execProcess).waitForRunning — generic (non-scheduling) DeadlineExceeded arm:; brine RED: And the build log shows "Pod Failure Diagnostics" (line 171) — step_end status=failed, error: expected the build log to mention "Pod Failure Diagnostics", got "". The three preceding steps all passed…; go RED: [FAILED] Expected <string>: to contain substring <string>: Pod Failure Diagnostics In [It] at: .../atc/worker/jetbridge/process_test.go:846 — i.e. Expect(stderrBuf.String()).To(ContainSubstring("Pod…; skeptic: Ran four: (1) red-by-adaptation — restored Go test green with mutation reverted; (2) unrelated/flaky brine red — clean run of the whole feature first; (3) reproduction of the recorded mutation on bot… → HOLDS

**[DELETED]** Process execProcess failure state detection detects ImagePullBackOff in waitForRunning  `JB-process-022`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Runs through waitForRunning but pins isPodFailedFast's terminal-waiting-reason detection, not the startup deadline — exactly the carve-out in the rule. isPodFailedFast has no commits in range, atc/metric/ is byte-identical, and this container uses NewConfig's positive DefaultPodStartupTimeout, for which podStartupTime…

**[REFUTED]** Process execProcess failure state detection waits for Unschedulable pod and times out  `JB-process-023`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: IMPACTED (a, d) — (a)+(d): this is the only row that pins the interaction of BOTH deadlines (it sets PodStartupTimeout=2s and PodSchedulingTimeout=3s at process_test.go:915-917), and waitForRunning's `effectiveTimeout = max(timeout, schedTimeout)` is fed by the line 1164f9db3d rewrote. It is also the test the deletion commit is entirel…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/process.go : func (*execProcess).waitForRunning — MUTATION 1 (the both-red one), in the `timeoutCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil` arm:; brine RED: Then the step fails naming "Unschedulable" (failure-priority.feature:133) — error: expected the failure to mention "Unschedulable", got "waiting for pod running: timed out waiting for pod to start (t…; go RED: MUTATION 1 — process_test.go:963: `[FAILED] Expected <string>: waiting for pod running: timed out waiting for pod to start (timeout: 2s, phase: Pending) to contain substring <string>: pod scheduling…; skeptic: Narrower-mutation / different-behaviour pairing (primary, SUCCEEDED), plus red-by-adaptation, unrelated-brine-red, and mutation-not-the-recorded-one (all three FAILED to refute). → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/process_restored_test.go`

**[DELETED]** Process execProcess failure state detection waits for Unschedulable pod and succeeds when scheduled  `JB-process-024`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the unschedulable-then-recovered RESET inside isPodUnschedulable/waitForRunning, not the deadline: it sets only PodSchedulingTimeout=30s and leaves PodStartupTimeout at NewConfig's positive default, for which podStartupTimeout(cfg) returns the same value 1164f9db3d's inline code did. isPodUnschedulable has no com…

**[DELETED]** Process execProcess failure state detection detects pod eviction before reaching Running phase  `JB-process-025`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins interruptionErrorForPod/writePodDiagnostics/writeNodeDiagnostics on the exec chain; none has a commit in aef2244a63..5133d0ddbc and `-SInterruptionEvicted -- atc/` is empty. The waitForRunning refactor touches only timeout resolution, which this test never reaches (the eviction is terminal long before any deadlin…

**[DELETED]** Process execProcess failure state detection detects pod terminated before exec could run  `JB-process-026`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the PodSucceeded terminal branch and recreatePausePodIfTerminal; neither symbol has a commit in the range, and the timeout-resolution refactor is unrelated to it. (Separately: the evidence records NO brine scenario for this branch — a coverage gap, not a rebase impact.)

**[DELETED]** Process execProcess failure state detection preserves the pause pod when context is cancelled (for fly hijack)  `JB-process-027`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the exec-mode cancellation path of execProcess.Wait and Container.Run's pause-pod branch; process.go's only change is the podStartupTimeout extraction and container.go's changes are unreachable here (no caches, no storage backend). step-closing.feature @PE-10, which now carries this behaviour by explicit line cit…

**[DELETED]** Process supervised gates the in-pod supervisor on container type and stdin (F18) [Entry: task, no stdin → supervised]  `JB-process-028`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — `git log -Ssupervised aef2244a63..5133d0ddbc -- atc/worker/jetbridge/` is empty and process.go's whole diff is the 13-line podStartupTimeout extraction, so execProcess.supervised and buildSupervisorScript are unchanged. task-command.feature was untouched by the rebase. (The gating rule has no brine counterpart at all…

**[DELETED]** Process supervised gates the in-pod supervisor on container type and stdin (F18) [Entry: get, no stdin → raw command]  `JB-process-029`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched execProcess.supervised gate, taken on the ContainerTypeGet arm; no commit on core touches it and no rebase-changed step file or feature is involved.

**[DELETED]** Process supervised gates the in-pod supervisor on container type and stdin (F18) [Entry: task with stdin → raw command]  `JB-process-030`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched gate on the non-nil-stdin arm; execProcess.supervised and streamInputs have no commits in aef2244a63..5133d0ddbc, and steps/tty.go (whose comment is the only brine trace of the rule) is not in step_changes.

**[DELETED]** Process exec-mode pod failure diagnostics writes pod failure diagnostics when exec fails due to pod death  `JB-process-031`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins fetchPodFailureContext's post-mortem re-Get plus writePodDiagnostics/writeNodeDiagnostics; none has a commit in the range. The waitForRunning refactor is upstream of the exec and does not touch the severed-exec branch this asserts. pod-lifecycle.feature @RF-15 was untouched by the rebase.

**[DELETED]** Process exec-mode pod failure diagnostics writes diagnostics when pod is already gone (not found)  `JB-process-032`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The NotFound branch of fetchPodFailureContext has no commits in aef2244a63..5133d0ddbc, so the 'pod no longer exists' text is byte-identical on core; pod-lifecycle.feature @RF-15 was not rebase-affected.

**[DELETED]** Process severed-exec output-location recording (F23) does NOT record output locations for a task step on a severed exec, preserving fail-fast (review 2026-07-12)  `JB-process-033`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — artifact_locator.go is byte-identical between the two revisions and uploadOutputsToArtifactStore has no commits. The DaemonSetBackend named in source_functions is never actually installed — grepping the merge-base file for storageBackend/SetStorageBackend/DaemonSetBackend hits only the comment at line 1328 — so storag…

**[DELETED]** Process terminal-end agent kill (timed-out/aborted agent step) leaves timed-out/aborted task steps alone (existing semantics)  `JB-process-034`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins an exec-call-count negative on execProcess.Wait plus execProcess.supervised; neither has a commit in the range and the podStartupTimeout extraction cannot change how many execs are issued. Nothing on core touches it.

**[DELETED]** Process transient API error handling tolerates a single API error during pollUntilDone  `JB-process-035`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — pollUntilDone and maxConsecutiveAPIErrors have no commits in range, and atc/worker/jetbridge/watch.go (PodWatcher.Next's initial-sync retry loop) is byte-identical between aef2244a63 and 5133d0ddbc. pod-lifecycle.feature @RF-12 was untouched by the rebase.

**[DELETED]** Process transient API error handling fails after 3 consecutive API errors in pollUntilDone  `JB-process-036`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched pair: pollUntilDone unchanged, watch.go byte-identical, so the 'consecutive API errors' failure after three reads is unchanged on core. pod-lifecycle.feature @RF-13 is absent from step_changes.

**[DELETED]** Process transient API error handling resets error count after a successful API call  `JB-process-037`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — watch.go's initial-sync loop — the code the evidence shows this test misdescribes — is byte-identical on core, so both the behaviour and the recorded refutation still stand unchanged. No rebase-changed feature or step file is involved.

**[DELETED]** Process K8s-specific metrics ImagePullFailures counter increments K8sImagePullFailures when ImagePullBackOff is detected  `JB-process-038`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — atc/metric/ is byte-identical between the two revisions and isPodFailedFast has no commits, so the counter increment is unchanged. The test runs inside waitForRunning but pins the pull-failure metric, not the startup deadline — the carve-out case.

**[DELETED]** Process K8s-specific metrics PodStartupDuration gauge records startup duration when pod reaches Running  `JB-process-039`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Pins the gauge Set on waitForRunning's PodRunning branch; 1164f9db3d changed only the timeout-resolution line above it, and atc/metric/ is byte-identical. (The recorded finding that `Max() >= 0` CANNOT FAIL is a property of the assertion itself on both revisions, not a rebase impact.)

**[DELETED]** Process sidecar lifecycle when main container exits while sidecars are still running (direct mode) returns the main container's exit code and cleans up the pod  `JB-process-040`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — podExitCode's PodRunning+main-terminated branch and Process.Wait's post-exit delete have no commits in range; `git log -SSidecarConfig aef2244a63..5133d0ddbc -- atc/` is empty and container.go's diff touches no sidecar wiring. pod-lifecycle.feature @SC-10 was untouched by the rebase.

**[DELETED]** Process sidecar lifecycle when main container exits while sidecars are still running (direct mode) returns non-zero exit code from main and cleans up  `JB-process-041`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched podExitCode branch and unchanged atc.SidecarConfig. The neighbouring exit-code-from-any-terminated-container gap the evidence records was found by mutation on this same code, which core has not modified since the merge-base.

**[DELETED]** Process sidecar lifecycle sidecar failure detection fails fast when sidecar has ImagePullBackOff and main hasn't terminated  `JB-process-042`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — isPodFailedFast's scan over all container statuses has no commits in aef2244a63..5133d0ddbc and no sidecar code changed on core. pod-lifecycle.feature @SC-08 is absent from rebase_result.step_changes.

**[DELETED]** Process sidecar lifecycle sidecar failure detection does not fail the task when sidecar fails but main has already terminated  `JB-process-043`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The early-return-when-main-is-terminated arm of isPodFailedFast is unchanged on core, as is podExitCode. pod-lifecycle.feature @SC-09 was untouched by the rebase.

**[DELETED]** Pod phase transition spans direct mode (pollUntilDone) emits pod.phase span events when pod transitions to Succeeded  `JB-process-044`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The k8s.process.wait span and pollUntilDone's phase-change AddEvent are on the DIRECT chain, which 1164f9db3d does not touch at all, and tracing/ is byte-identical between the two revisions. (Separately: no brine scenario covers the direct-mode span family — a gap, not an impact.)

**[DELETED]** Pod phase transition spans direct mode (pollUntilDone) emits pod.phase.failed span event when pod fails  `JB-process-045`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched direct-mode span path plus podExitCode; tracing/ byte-identical, pollUntilDone with no commits in range. Nothing on core touches what this pins.

**[DELETED]** Pod phase transition spans exec mode (waitForRunning) emits pod.phase.running span event when pod reaches Running  `JB-process-046`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — This is the carve-out named verbatim in the rule: the span event is emitted from waitForRunning but 1164f9db3d changed only that function's timeout-resolution line, not its AddEvent block, and tracing/ is byte-identical. observability.feature @OE-08 @OE-10 was untouched by the rebase.

**[DELETED]** Pod phase transition spans init container and sidecar lifecycle events emits init.container.completed span event when init container terminates  `JB-process-047`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — podEventTracker.emitPodLifecycleEvents and newPodEventTracker have no commits in aef2244a63..5133d0ddbc and tracing/ is byte-identical; the waitForRunning change does not touch the tracker call. observability.feature @OE-05/@OE-06 is absent from step_changes.

**[DELETED]** Pod phase transition spans init container and sidecar lifecycle events emits sidecar.started span event when sidecar container reaches Running  `JB-process-048`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched emitPodLifecycleEvents (startedSidecars arm); no sidecar code changed on core (`-SSidecarConfig -- atc/` empty) and observability.feature @OE-07 was not rebase-affected.

**[DELETED]** Pod phase transition spans PVC bind and image pull events emits pod.scheduled span event when PodScheduled condition becomes True  `JB-process-049`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The scheduled arm of emitPodLifecycleEvents has no commits in range and tracing/ is byte-identical, so the event and its dedup are unchanged. observability.feature @OE-01/@OE-09 was untouched by the rebase.

**[DELETED]** Pod phase transition spans PVC bind and image pull events emits image.pulling span event when container is in ContainerCreating  `JB-process-050`
  - recorded evidence: PER-FILE (deletion commit `d3a0d151ab`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The pullingImages arm of emitPodLifecycleEvents is unchanged on core, watch.go (PodWatcher.Next's initial Get, which the 50ms goroutine races) is byte-identical, and tracing/ is byte-identical. observability.feature @OE-04 was not rebase-affected.


## volume_test.go

Deleted by `5a32670606` — "Delete volume_test.go; prove decompression with an encoding tar cannot absorb". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

Restored tests from this file live in `atc/worker/jetbridge/volume_restored_test.go`.

**[REFUTED]** Volume Handle returns the db volume handle  `JB-volume-000`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] (b) collision the panel resolved substantively instead of by the rule. Its OWN named counterpart is volume-streaming.feature:77, and volume-streaming.feature is in features_affected of two rebase step_changes entries (steps/domain.go new ContainerDraft.taskCacheIdentity, steps/…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go :: func (v *Volume) Handle(); brine RED: Then the volume identifies itself by its database handle -> expected the volume to identify as "e62ae326-16a1-493f-bd59-85f7f7479d61" — the handle the artifact repository keys on — got ""; go RED: [FAILED] Expected <string>: to equal <string>: vol-handle-123 In [It] at: .../atc/worker/jetbridge/volume_test.go:83; skeptic: different-behaviour pairing (narrower mutation on a recorded source function: the Go It pins an ABSOLUTE handle the brine step cannot see, because brine compares the volume's handle to the handle of… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[DELETED]** Volume Source returns the worker name from the db volume  `JB-volume-001`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same file, same (b) collision: primary counterpart volume-streaming.feature, which is in step_changes' features_affected via steps/domain.go and steps/container_extra.go. || original reason: (*Volume).Source lives in volume.go, whose only core change is InitializeTaskCache (0d3…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/volume.go :: func (v *Volume) Source() — deleted the dbVolume branch, exactly as the recipe specified:; brine RED: And the volume names the worker it lives on (line 80) — error: expected the volume to name worker "k8s-worker-1", got ""; go RED: [FAILED] Expected <string>: to equal <string>: k8s-worker-1 In [It] at: .../atc/worker/jetbridge/volume_test.go:89; skeptic: Three attacks run, none refuted: (1) different-behaviour pairing via an honest ALTERNATE mutation in the DB read-back path the Go fixture uses but brine's does not (scanVolume drops the persisted wor… → HOLDS

**[REFUTED]** Volume DBVolume returns the underlying db volume  `JB-volume-002`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature (in features_affected of the domain.go/container_extra.go step changes). || original reason: (*Volume).DBVolume is a one-line field return in volume.go and core touched only InitializeTaskCache there; nothing on core changes what a…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go :: (*Volume).DBVolume; brine RED: And both volume kinds still carry their database row (feature line 83) — status "failed", error verbatim: "the deferred volume lost its database row". The two preceding Then/And steps ("the volume id…; go RED: [FAIL] Volume DBVolume [It] returns the underlying db volume — /Users/tdmtrader/concourse/concourse/.worktrees/rv-jb-volume-002/atc/worker/jetbridge/volume_test.go:95 [FAILED] Expected <nil>: nil to…; skeptic: different-behaviour pairing (narrower mutation that breaks ONLY what the Go It asserts), plus red-by-adaptation control and a recorded-mutation control run to prove the adapter rebuild is live → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[REFUTED]** Volume DBVolume returns the persisted DB volume from a DaemonSetVolume  `JB-volume-003`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature; also touches volume_daemonset.go, whose InitializeTaskCache core did change (the row correctly notes the test never calls it). || original reason: atc/worker/jetbridge/volume_daemonset.go changed on core only in (*DaemonSetVolume)…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume_daemonset.go :: func NewDaemonSetVolume — dropped the dbVolume field assignment from the returned struct literal.; brine RED: And both volume kinds still carry their database row (line 83) — error: "the daemonset volume lost its database row"; go RED: [FAILED] Expected <nil>: nil to be identical to <*db.createdVolume | ...>{...} In [It] at: <wt>/atc/worker/jetbridge/volume_test.go:109 — i.e. Expect(daemonSetVolume.DBVolume()).To(BeIdenticalTo(dbVo…; skeptic: different-behaviour pairing via a narrower mutation (plus controls: unrelated-brine-red baseline, red-by-adaptation, and a reproduction of the recorded mutation) → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[GAP]** Volume StreamIn execs tar extract in the correct Pod container at the specified path  `JB-volume-004`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature. || original reason: (*Volume).StreamIn and (*Volume).resolvedPath are byte-identical on core (volume.go's whole diff is 3+/2- inside InitializeTaskCache) and atc/worker/jetbridge/executor.go, which defines ExecInPod, has an empty…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/volume.go, func (v *Volume) StreamIn (line 200 on 27d81692fa) — blank the pod the tar-extract exec is aimed at:; brine GREEN: none — 20 of 20 scenarios passed with the mutation applied. run_end: {"features":1,"scenarios":20,"passed":20,"failed":0,"duration_ms":24346}. Sharper variant (namespace+pod+container all blanked) al…; go RED: [FAILED] Expected <string>: to equal <string>: test-pod In [It] at: <wt>/atc/worker/jetbridge/volume_test.go:126 @ 09/05/26 12:28:46.873 Summarizing 1 Failure: [FAIL] Volume StreamIn [It] execs tar e…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[REFUTED]** Volume StreamIn pipes the reader data to stdin of the exec  `JB-volume-005`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature. || original reason: The stdin plumbing lives entirely in (*Volume).StreamIn and PodExecutor.ExecInPod, both byte-identical on core, and atc/compression has an empty diff over the range. The counterpart "An artifact comes back out…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go, func (v *Volume) StreamIn — stop handing the caller's bytes to the exec:; brine RED: Then the artifact "hello.txt" containing "hello world" is there (line 21) — status failed, error: expected "hello.txt", found []; go RED: [FAILED] Expected <nil>: nil not to be nil In [It] at: .../atc/worker/jetbridge/volume_test.go:140 — i.e. Expect(call.stdin).ToNot(BeNil()); skeptic: different-behaviour pairing — a narrower mutation that breaks only what the Go It asserts (byte-for-byte identity of the caller's reader with ExecInPod's stdin) while leaving the artifact's arrival i… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[REFUTED]** Volume StreamIn uses a subdirectory path when path is not root  `JB-volume-006`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature. || original reason: The path join it pins is (*Volume).resolvedPath, unchanged on core (`git log -SresolvedPath -- atc/worker/jetbridge` is empty over aef2244a63..5133d0ddbc). Its counterparts "A member keeps its path when read ba…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go :: func (v *Volume) StreamIn diff --git a/atc/worker/jetbridge/volume.go b/atc/worker/jetbridge/volume.go index ae37b91046..d209976d69 100644; brine RED: Then the artifact "sub/dir/nested.txt" containing "deep content" is there (line 34) -- error: expected "sub/dir/nested.txt", found [] || line 40 same step text -- error: expected "sub/dir/nested.txt"…; go RED: volume_test.go:153 -- [FAILED] Expected <[]string | len:5, cap:5>: ["tar", "xf", "-", "-C", "/tmp/build/inputs"] to equal <[]string | len:5, cap:5>: [ "tar", "xf", "-", "-C", "/tmp/build/inputs/sub/d…; skeptic: different-behaviour pairing — narrower mutation of the SAME line and the same asserted behaviour (the resolved target handed to tar) that the Go It catches and every brine scenario misses; plus red-b… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[GAP]** Volume StreamIn passes stream-in purpose and volume mount path in ExecAttrs  `JB-volume-007`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision; additionally the reason itself records that no brine scenario asserts ExecAttrs.Purpose, so 'evidence stands' means 'no counterpart exists', which the disposition stage must not read as coverage. || original reason: ExecAttrs is declared in volume.go and Str…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/volume.go :: func (v *Volume) StreamIn (line 201); brine GREEN: n/a — no step failed. volume-streaming.feature: 20 of 20 scenario_end events status=passed WITH the mutation applied, identical to the 20/20 unmutated baseline I ran first. container-run.feature: 19…; go RED: [FAILED] Expected <string>: to equal <string>: stream-in In [It] at: .../atc/worker/jetbridge/volume_test.go:162 @ 09/05/26 12:30:42.877 Summarizing 1 Failure: [FAIL] Volume StreamIn [It] passes stre…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[DELETED]** Volume StreamIn when the exec returns an error returns the error  `JB-volume-008`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature. || original reason: The swallowed-write behaviour lives in (*Volume).StreamIn's error return, byte-identical on core. The scenario that closes it, "A cluster failure reaches the writer rather than being swallowed" (added by the de…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/volume.go :: func (v *Volume) StreamIn — swallow the ExecInPod failure (lines 202-207):; brine RED: Then it fails rather than panicking, saying "exec failed" (line 105) — error: expected a failure mentioning "exec failed", but it succeeded; go RED: [FAILED] Expected an error, got nil In [It] at: <wt>/atc/worker/jetbridge/volume_test.go:174 (assertion: Expect(err).To(MatchError(ContainSubstring("exec failed")))) Summary: Ran 1 of 41 Specs ... FA…; skeptic: Three attacks run, all failed to refute: (1) red-by-adaptation — baseline the restored/adapted Go test unmutated; (2) unrelated/flaky brine red — the whole feature run clean; (3) different-behaviour… → HOLDS

**[GAP]** Volume StreamOut execs tar create in the correct Pod container at the specified path  `JB-volume-009`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision; its assertion-class note is carried by container-run.feature, also in features_affected. || original reason: (*Volume).StreamOut is unchanged on core (`git log -SStreamOut -- atc/worker/jetbridge atc/db` is empty over the range) and executor.go has an empty…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation PRIMARY (the half this row uniquely asserts) — atc/worker/jetbridge/volume.go, func (v *Volume) StreamOut, inside the streaming goroutine:; brine GREEN: none — no step failed under the PRIMARY mutation. volume-streaming.feature ran fully GREEN: run_end {"features":1,"scenarios":20,"passed":20,"failed":0,"duration_ms":23048}; container-run.feature ran…; go RED: PRIMARY mutation, verbatim: [FAILED] Expected <string>: to equal <string>: test-pod In [It] at: .../atc/worker/jetbridge/volume_test.go:194 @ 09/05/26 12:43:03.328 Summarizing 1 Failure: [FAIL] Volum…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[GAP]** Volume StreamOut passes stream-out purpose and volume mount path in ExecAttrs  `JB-volume-010`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision; same ExecAttrs.Purpose no-counterpart caveat as JB-volume-007. || original reason: Same untouched surface as JB-volume-007: ExecAttrs and StreamOut's literal are in the unmodified region of volume.go. The absence of a brine counterpart for ExecAttrs.Purpose…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/volume.go :: (*Volume).StreamOut (line 272, inside the streaming goroutine); brine GREEN: n/a — 20 of 20 scenario_end events reported status=passed under the mutation; no scenario in the feature reddened; go RED: [FAILED] Expected <string>: to equal <string>: stream-out In [It] at: .../atc/worker/jetbridge/volume_test.go:208 (assertion source: Expect(call.attrs.Purpose).To(Equal("stream-out"))); skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[REFUTED]** Volume StreamOut returns the stdout as a ReadCloser via streaming pipe  `JB-volume-011`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] (b) collision named explicitly in its own reason: counterpart container-run.feature:166 @VT-03, and container-run.feature is in features_affected of steps/container_extra.go. The reason argues inertness rather than applying the rule. || original reason: The io.Pipe goroutine is…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go, func (v *Volume) StreamOut — inside the streaming goroutine, discard the tar bytes instead of piping them to the caller:; brine RED: Then the artifact "hello.txt" containing "hello world" is there (line 21) — error: expected "hello.txt", found []; go RED: [FAILED] Expected <[]uint8 | len:0, cap:512>: "" to equal <[]uint8 | len:16, cap:16>: "tar-output-bytes" In [It] at: .../atc/worker/jetbridge/volume_test.go:219; skeptic: different-behaviour pairing (narrower mutation), plus red-by-adaptation control → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[DELETED]** Volume StreamOut uses a subdirectory path when path is not root  `JB-volume-012`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature. || original reason: The StreamIn/StreamOut path asymmetry it encodes is entirely inside volume.go's untouched region; core's only edit there is InitializeTaskCache. The counterpart "A member keeps its path when read back from that…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/volume.go, func (v *Volume) StreamOut (line 255 on rebased head 27d81692fa) — make StreamOut symmetric with StreamIn (descend into the path instead of selecting it as a tar membe…; brine RED: Then the artifact "sub/dir/nested.txt" containing "deep content" is there -> error: expected "sub/dir/nested.txt", found [nested.txt]; go RED: [FAILED] Expected <[]string | len:6, cap:6>: [ "tar", "cf", "-", "-C", "/tmp/build/inputs/sub/dir", ".", ] to equal <[]string | len:6, cap:6>: ["tar", "cf", "-", "-C", "/tmp/build/inputs", "sub/dir"]…; skeptic: Four attacks, all run, none refuted the pairing: (1) red-by-adaptation, (2) unrelated/flaky brine red, (3) mutation-not-the-recorded-one + order-masked assertion (full reproduction), (4) the sharpest… → HOLDS

**[GAP]** Volume StreamOut handles a file path by tarring from the mount root  `JB-volume-013`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature. || original reason: Same untouched (*Volume).StreamOut member-selector logic; no core commit in aef2244a63..5133d0ddbc touches it. Its inferred counterpart "The same member is reachable from the volume root" is unchanged in volume…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation Recorded/recipe mutation, applied verbatim to atc/worker/jetbridge/volume.go, func (v *Volume) StreamOut (line 251-256 on 27d81692fa):; brine GREEN: none — no step failed. Under the recorded mutation: volume-streaming.feature 20/20 scenario_end status=passed, exit 0; container-run.feature 19/19 passed, exit 0. Under the sharper variant: volume-st…; go RED: Under the recorded mutation (volume_test.go:242): [FAILED] Expected <[]string | len:6, cap:6>: ["tar", "cf", "-", "-C", "/tmp/build/inputs", "./pipeline.yml"] to equal <[]string | len:6, cap:6>: ["ta…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[REFUTED]** Volume StreamOut when the exec returns an error propagates the error through the pipe reader  `JB-volume-014`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature. || original reason: The CloseWithError path in (*Volume).StreamOut is unchanged on core. Its pre-existing counterpart "A cluster failure reaches the reader rather than being swallowed" sits in volume-streaming.feature, which the r…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go :: func (v *Volume) StreamOut — last statement of the streaming goroutine (line 288 on 27d81692fa):; brine RED: Then it fails rather than panicking, saying "exec failed" (line 72) — step_end status "failed", error verbatim: expected a failure mentioning "exec failed", but it succeeded; go RED: volume_test.go:256 — Expect(err).To(MatchError(ContainSubstring("exec failed"))) ; Ginkgo verbatim: [FAILED] Expected an error, got nil / In [It] at: .../atc/worker/jetbridge/volume_test.go:256; skeptic: different-behaviour pairing — narrower mutation that breaks only the code path the Go assertion pins (raw/uncompressed StreamOut), plus red-by-adaptation and unrelated-brine-red controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[REFUTED]** Volume StubVolume (nil executor) StreamOut returns an error instead of panicking  `JB-volume-015`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature (@VT-05 stub scenarios). || original reason: NewStubVolume and the nil-executor guard in (*Volume).StreamOut are both in volume.go's untouched region (`git log -SNewStubVolume -- atc/worker/jetbridge` is empty over the range). The…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation Two mutations, both in atc/worker/jetbridge/volume.go, func (v *Volume) StreamOut (the nil-executor guard, lines 233-235 on 27d81692fa). (A) RECORDED mutation — delete the guard entirely:; brine RED: Then it fails rather than panicking, saying "no executor" (line 59) — step_end status "failed", error: `expected the failure to mention "no executor", got "volume stub-handle is not ready"`; scenario…; go RED: Under mutation (B), verbatim: [FAILED] Expected <string>: volume rc-42 is not ready to contain substring <string>: cannot stream out In [It] at: <W>/atc/worker/jetbridge/volume_test.go:271 [FAIL] Vol…; skeptic: different-behaviour pairing — narrower mutation that breaks only what the Go It asserts beyond the brine step (plus red-by-adaptation control and an unrelated-red control) → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[REFUTED]** Volume StubVolume (nil executor) StreamIn returns an error instead of panicking  `JB-volume-016`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature (@VT-05 stub scenarios). || original reason: Mirror of JB-volume-015: the nil-executor guard in (*Volume).StreamIn is unchanged on core, and its @VT-05 counterpart "A stub volume refuses to be written rather than panicking" is byte…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go :: func (v *Volume) StreamIn — delete the stub/nil-executor guard:; brine RED: When a file is put into volume "stub" (line 65) — the run stops dead at this step: the last event emitted is {"type":"step_start",...,"keyword":"When","text":"a file is put into volume \"stub\"","lin…; go RED: [PANICKED] Test Panicked / In [It] at: .../src/runtime/panic.go:262 @ 09/05/26 12:53:04.776 / "runtime error: invalid memory address or nil pointer dereference" / Full Stack Trace: github.com/concour…; skeptic: Primary: narrower mutation that breaks only what the Go It asserts (message-only, guard intact) — brine stayed green. Secondary: characterising the recorded brine "red" (it is a crash-hang, never a r… → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[GAP]** Volume StubVolume (nil executor) HasExecutor returns false  `JB-volume-017`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision; its disposition lives in container-run.feature, which is in features_affected. || original reason: (*Volume).HasExecutor is unchanged on core (`git log -SHasExecutor -- atc/worker/jetbridge` empty over the range) and container-run.feature, which carries the…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/volume.go :: (*Volume).HasExecutor — exactly the recorded mutation, applied verbatim at the recorded location (line 132-134 on the rebased head 27d81692fa):; brine GREEN: none — zero failing steps. Every scenario_end event carried status "passed" in all four runs (vs baseline 20 passed, cr baseline 19 passed, vs mutated 20 passed, cr mutated 19 passed, vs sharper-vari…; go RED: Verbatim from the Ginkgo output with the mutation applied: • [FAILED] [0.117 seconds] Volume StubVolume (nil executor) [It] HasExecutor returns false /…/atc/worker/jetbridge/volume_test.go:283 [FAILE…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[DELETED]** Volume StubVolume (nil executor) Handle returns the stub handle  `JB-volume-018`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision; the reason also records 'no counterpart named' — an evidence gap that predates the rebase but must be surfaced, not closed. || original reason: NewStubVolume and (*Volume).Handle are unchanged on core, so nothing about the stub-handle path moved. This row's…
  - re-verified 2026-09-05 (rebase onto core): HOLDS — mutation atc/worker/jetbridge/volume.go :: NewStubVolume — drop the caller's handle (recorded mutation, applied verbatim in effect; `_ = handle` added only to keep the signature compiling):; brine RED: step: `that fetch asks the daemon for "vol-a"` — error: `expected the batch of keys the pod asks for in one request to mention "vol-a", got "{\"items\":[{\"key\":\"\",\"dest\":\".../steps/wide-fan-in…; go RED: [FAILED] Expected <string>: to equal <string>: rc-42 In [It] at: <wt>/atc/worker/jetbridge/volume_test.go:288 Summarizing 1 Failure: [FAIL] Volume StubVolume (nil executor) [It] Handle returns the st…; skeptic: Ran three attacks: (1) NARROWER MUTATION / different-behaviour pairing — replaced the recorded whole-handle drop with `handle: handle + "-stub"`, which breaks exactly what the Go It asserts (Equal("r… → HOLDS

**[REFUTED]** Volume volume uniqueness two volumes with different handles are distinguishable  `JB-volume-019`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Strongest single suspect in the family: the reason CONCEDES that its counterpart container-run.feature:109 declares a cache and that 0d336e062b 'can now select an emptyDir where it previously selected hostPath', then closes the row anyway on the ground that the surviving assert…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go :: func (v *Volume) Handle() — collapse every db-backed volume onto one identity:; brine RED: Then the volume identifies itself by its database handle (volume-streaming.feature:79) — error: expected the volume to identify as "264a373c-09a9-4009-b498-8b224e5e15d9" — the handle the artifact rep…; go RED: [FAILED] Expected <string>: vol-handle-123 not to equal <string>: vol-handle-123 In [It] at: .../atc/worker/jetbridge/volume_test.go:319 — i.e. Expect(volume.Handle()).ToNot(Equal(volume2.Handle())); skeptic: different-behaviour pairing / narrower mutation that breaks only what the Go test asserts (plus red-by-adaptation control and an unrelated-brine-red control) → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[GAP]** Volume-to-Volume Streaming (same worker) streams data from source volume (pod A) to destination volume (pod B)  `JB-volume-020`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision on volume-streaming.feature. || original reason: Both halves — (*Volume).StreamOut and (*Volume).StreamIn — plus PodExecutor.ExecInPod are byte-identical on core. The counterpart named by the feature comment, "One step's output becomes the next step's input",…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/volume.go, func NewVolume — recorded mutation, applied verbatim:; brine GREEN: (none — scenario passed under the recorded mutation; 20/20 scenarios passed, verdict "passed", exit 0). Every step passed, including "Then the artifact \"result.json\" containing \"built ok\" is ther…; go RED: volume_test.go:367 — [FAILED] Expected <string>: to equal <string>: source-pod (i.e. `Expect(streamOutCall.podName).To(Equal("source-pod"))`, reached after the By("verifying the exec calls target dif…; skeptic: not reached — the verifier stopped at GAP
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`

**[REFUTED]** Volume-to-Volume Streaming (same worker) works with deferred volumes after pod name is set  `JB-volume-021`
  - recorded evidence: PER-FILE (deletion commit `5a32670606`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] (b) collision named in its own reason: counterparts are the container-run.feature @CO-04 pair, and the reason explicitly discusses the container_extra.go step change before dismissing it as inert. JB-podname_integration-008 was marked IMPACTED on this identical argument — the t…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume.go :: func (v *Volume) SetPodName — late pod binding made a no-op:; brine RED: Then every volume the caller was handed now names the pod "deferred-pod-handle" -> expected the volume at "/tmp/build/workdir" to name the pod "deferred-pod-handle", it names ""; go RED: volume_test.go:404 — [FAILED] Expected <string>: to equal <string>: step-1-pod (i.e. Expect(fakeExecutor.execCalls[0].podName).To(Equal("step-1-pod"))); skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts) — plus red-by-adaptation and unrelated-brine-red as controls → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_restored_test.go`


## volume_daemonset_test.go

Deleted by `5be07a572c` — "Delete volume_daemonset_test.go — 16 of 16 tests evidenced, no gaps". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

Restored tests from this file live in `atc/worker/jetbridge/volume_daemonset_restored_test.go`.

**[DELETED]** TestDaemonSetVolume_StreamOut_Success  `JB-volume_daemonset-000`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Core's only change to volume_daemonset.go is (*DaemonSetVolume).InitializeTaskCache (0d336e062b/7fdfd93cf5, jobID int -> atc.TaskCacheIdentity) plus the atc import; StreamOut/fetchArtifactWithPeerFallback/fetchOnce/daemonURL are byte-identical, daemonURLScheme (daemon_tls.go:16) was untouched by 06d3c556b1 which only…

**[DELETED]** TestDaemonSetVolume_StreamOut_NotFound  `JB-volume_daemonset-001`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The 404-to-"not found" mapping lives in fetchOnce/fetchArtifactWithPeerFallback, both byte-identical on core (volume_daemonset.go's whole diff is the InitializeTaskCache signature and one import), and git log -S for StreamOut/fetchOnce/fetchArtifactWithPeerFallback/NodeIPResolver returns zero commits between aef2244a6…

**[DELETED]** TestDaemonSetVolume_StreamOut_NoSourceNode  `JB-volume_daemonset-002`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The test pins the no-sourceNode/no-sourceIP/no-daemonClient guard at the top of StreamOut, and core changed nothing in that function — the only hunk in volume_daemonset.go is InitializeTaskCache (0d336e062b), and the DaemonSetVolume struct fields (sourceNode/sourceIP/daemonClient) are untouched. artifact-daemon.featur…

**[DELETED]** TestDaemonSetVolumeFromIP_StreamOut_Success  `JB-volume_daemonset-003`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — NewDaemonSetVolumeFromIP, newDaemonStreamingHTTPClient, daemonURL and daemonURLScheme all return zero hits from a repo-wide git log -S across aef2244a63..5133d0ddbc; daemon_tls.go's only core change (06d3c556b1) is daemonTLSServerName's ArtifactDaemonNamespace fallback, which this test never reaches since it uses plai…

**[DELETED]** TestDaemonSetVolumeFromIP_Handle  `JB-volume_daemonset-004`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Handle()/Source() and NewDaemonSetVolumeFromIP are untouched on core — volume_daemonset.go's entire diff vs the merge-base is the InitializeTaskCache signature change and one import — and no HTTP or config path is involved at all. Its evidence clause sits in artifact-daemon.feature, which the rebase left unmodified.

**[DELETED]** TestDaemonSetVolumeFromIP_StreamOut_NoIP  `JB-volume_daemonset-005`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched guard as row -002 plus NewDaemonSetVolumeFromIP/newDaemonStreamingHTTPClient, none of which appear in any core commit between the merge-base and 5133d0ddbc (repo-wide git log -S is empty for all three). Core's new Config fields (ArtifactDaemonNamespace, ArtifactDaemonResolveCapability*) default to zero…

**[DELETED]** TestDaemonSetVolume_StreamOut_WithGzipCompression  `JB-volume_daemonset-006`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — newCompressWriter still sits at volume.go:295 unchanged (volume.go's only core hunk is (*Volume).InitializeTaskCache from 0d336e062b) and git diff --stat aef2244a63 5133d0ddbc -- atc/compression/ is empty, so the gzip pipe the test pins is bit-for-bit the code the branch mutated. artifact-daemon.feature "A consumer th…

**[DELETED]** TestDaemonSetVolume_StreamOut_NilCompression_ReturnsRawTar  `JB-volume_daemonset-007`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The nil-compression passthrough is entirely inside StreamOut/fetchArtifactWithPeerFallback/fetchOnce, all byte-identical on core, and atc/compression/ has an empty diff. Nothing in the 7 rebased step files or the one rebased feature file (daemon-containment.feature) backs its artifact-daemon.feature counterpart.

**[DELETED]** TestDaemonSetVolume_StreamOut_SubPath_WithGzip  `JB-volume_daemonset-008`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — filterTarEntry is still at volume_daemonset.go:156 on core with zero git log -S hits repo-wide, and neither newCompressWriter nor atc/compression changed; the sub-path filtering behaviour the test pins is untouched. Its evidence scenario "Asking for one file inside an artifact gets that file and nothing else" is in ar…

**[DELETED]** TestDaemonSetVolume_StreamOut_RootPath_ReturnsAllFiles  `JB-volume_daemonset-009`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The needsFilter=false decision for "."/"" and filterTarEntry are both unchanged on core (volume_daemonset.go's whole diff is InitializeTaskCache plus an import; git log -S filterTarEntry over the repo is empty). artifact-daemon.feature "Asking for the artifact root gets everything in it" was not among the rebase's cha…

**[DELETED]** TestDaemonSetVolume_StreamOut_SubPath_FileNotFound  `JB-volume_daemonset-010`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The empty-archive-on-miss quirk falls out of filterTarEntry plus newCompressWriter, both unchanged on core and both with zero git log -S hits across aef2244a63..5133d0ddbc. Nothing on core touches what this pins, and its artifact-daemon.feature scenario was not rewritten by the rebase.

**[REFUTED]** TestDaemonSetVolume_StreamOut_FallsBackToPeer_OnConnectionRefused  `JB-volume_daemonset-011`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] (b) collision: its gap-closing evidence is named as volume-streaming.feature scenarios, and volume-streaming.feature is in features_affected of the domain.go/container_extra.go step changes. The reason argues the backing steps/volume_streaming.go did not change (true — the reba…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation atc/worker/jetbridge/volume_daemonset.go :: (*DaemonSetVolume).fetchArtifactWithPeerFallback — recorded mutation applied verbatim (3 inserted lines after the `if fetchErr == nil && resp.StatusCode ==…; brine RED: Then the artifact "release.tgz" containing "mirrored to a peer" is there -> status failed, error: reading the volume failed: Get "http://127.0.0.1:56813/artifacts/step-output": EOF (scenario_end: sta…; go RED: volume_daemonset_test.go:511: expected fallback to peer to succeed, got: Get "http://10.0.0.1:7780/artifacts/h/o": connection refused: 10.0.0.1:7780 --> --- FAIL: TestDaemonSetVolume_StreamOut_FallsB…; skeptic: different-behaviour pairing, attacked with a narrower honest mutation (plus red-by-adaptation and unrelated-brine-red controls) → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_daemonset_restored_test.go`

**[GAP]** TestDaemonSetVolume_StreamOut_FallsBack_PreservesNotFoundOnProbeMiss  `JB-volume_daemonset-012`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision: the 'or any peer' evidence is a volume-streaming.feature gap-closing scenario, and that feature is in features_affected. || original reason: The "or any peer" error text and the probe-actually-ran assertion sit in fetchArtifactWithPeerFallback and DaemonClie…
  - re-verified 2026-09-05 (rebase onto core): GAP — mutation atc/worker/jetbridge/volume_daemonset.go : func (*DaemonSetVolume) fetchArtifactWithPeerFallback — the probe-miss error message (line 328, inside `if !found {` after `ProbeStepArtifact`) replaced by…; brine RED: And the failure names the node and its peers rather than the refused connection (line 205) — error: expected the failure to say the artifact was on neither the node nor any peer — which is the only t…; go RED: volume_daemonset_test.go:556: error should not be raw connection-refused after fallback; got: fetch artifact from http://10.0.0.1:7780/artifacts/h/o: Get "http://10.0.0.1:7780/artifacts/h/o": connect…; skeptic: Ran four attacks: (1) red-by-adaptation, (2) unrelated/flaky brine red, (3) different-behaviour pairing via a NARROWER mutation that breaks only the Go assertion, (4) assertion-not-covered — two mutations aimed at the test's SECOND assertion (that the peer probe actually ran), which the verifier skipped. Attacks 1-3 failed — the error-text pairing is genuine. Attack 4 succeeded: deleting the probe, and turning its HEAD into a GET, each redden the Go test while volume-streaming.feature (20/20) and artifact-daemon.feature (25/25) stay green, because the refused-producer scenario gives the peer the same closed port as the producer and the mirror handler answers HEAD and GET alike → GAP (the pairing broke on a claim no brine scenario owns)
  - **the test is restored** — `atc/worker/jetbridge/volume_daemonset_restored_test.go`

**[REFUTED]** TestDaemonSetVolume_StreamOut_HappyPath_PerformsZeroPeerProbes  `JB-volume_daemonset-013`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: IMPACTED (b) — [CRITIC REVISION → impacted by rule (b)] Same (b) collision: named test-for-test replacement is in artifact-daemon.feature (untouched), but its secondary evidence is volume-streaming.feature, which is in features_affected. || original reason: The producer-wins/no-speculative-fan-out ordering lives in fetchArtifactWith…
  - re-verified 2026-09-05 (rebase onto core): REFUTED — mutation Primary mutation, exactly as recorded in the recipe, applied to `atc/worker/jetbridge/volume_daemonset.go` in `func (v *DaemonSetVolume) fetchArtifactWithPeerFallback` (line 295 on the rebased head 2…; brine RED: Then the archive holds "f.txt" containing "producer-content" — error: expected the archive entry for "f.txt" to be "producer-content", got "stale-peer-content"; go RED: volume_daemonset_test.go:597: expected producer-content, got: "" / volume_daemonset_test.go:603: expected 0 peer probes on happy path, got 2 → --- FAIL: TestDaemonSetVolume_StreamOut_HappyPath_Perfor…; skeptic: different-behaviour pairing (narrower mutation that breaks only what the Go test asserts), plus red-by-adaptation, unrelated/flaky brine red, and order-masked assertion — all four run → REFUTED (the pairing broke)
  - **the test is restored** — `atc/worker/jetbridge/volume_daemonset_restored_test.go`

**[DELETED]** TestDaemonSetVolume_StreamOut_NoSourceNode_ProbesDaemons  `JB-volume_daemonset-014`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The restart-resume fall-through to daemon discovery is in StreamOut/fetchArtifactWithPeerFallback plus SetDaemonClient, ProbeStepArtifact and daemonIPs — none of which core touched (volume_daemonset.go's only hunk is InitializeTaskCache; daemon_client.go's diff is empty; git log -S for SetDaemonClient/ProbeStepArtifac…

**[DELETED]** TestDaemonSetVolume_StreamOut_NoSourceNode_ProbeMiss  `JB-volume_daemonset-015`
  - recorded evidence: PER-FILE (deletion commit `5be07a572c`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — "not found on any daemon" is produced by the same unchanged fetchArtifactWithPeerFallback/ProbeStepArtifact/daemonIPs trio, with zero core commits touching any of them between aef2244a63 and 5133d0ddbc. Its artifact-daemon.feature counterpart is untouched by the rebase, and the file itself is neither of the two confli…


## worker_test.go

Deleted by `e826623c6a` — "Delete worker_test.go; close the artifact-key collision it was guarding". Recorded evidence granularity: **per-file**. No mutation was ever named for an individual test in this file; a file-level both-red is not per-test evidence.

**[DELETED]** Worker Name returns the db worker name  `JB-worker-000`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The mutation surface is atc/worker/jetbridge/worker.go, which is byte-identical between aef2244a63 and 5133d0ddbc, so Worker.Name is untouched on core; brine's features/worker.feature and steps/worker.go are absent from rebase_result step_changes and from the rebased worktree's brine diff.

**[DELETED]** Worker SkipResourceCache returns false to enable resource caching  `JB-worker-001`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Worker.SkipResourceCache lives in the byte-identical worker.go and no commit in aef2244a63..5133d0ddbc matches -SSkipResourceCache under atc/; the @RC-01 scenario carrying this behaviour sits in worker.feature, which the rebase did not touch.

**[DELETED]** Worker FindOrCreateContainer when no container exists in the DB creates a container in the DB and defers Pod creation to Run  `JB-worker-002`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — This is the only row that reaches Container.Run's pod-building path, where core did change (*Container).buildVolumeMounts and buildArtifactInitContainers in 0d336e062b/1e023e7ca4 — but both changes are confined to cache volumes and a non-nil storageBackend, and this test declares no Caches, uses NewConfig with empty C…

**[DELETED]** Worker FindOrCreateContainer when transitioning to created state fails marks the container as failed so the GC can clean it up  `JB-worker-003`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Worker.FindOrCreateContainer and markContainerAsFailed are both in the byte-identical worker.go, and atc/db/worker.go and atc/db/container.go are unchanged on core, so the created-transition fault and the resulting 'failed' row behave identically. The decorator's relocation to jetbridge_suite_test.go was a branch-side…

**[DELETED]** Worker FindOrCreateContainer when a created container already exists in the DB returns the existing container without creating a new one in the DB  `JB-worker-004`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The hint symbol buildVolumeMounts matched worker.go's buildVolumeMountsForSpec by substring, and worker.go is byte-identical on core; this test never calls Container.Run so container.go's cache-arm changes are unreachable. The reuse-flag gap the deletion commit records is a pre-existing brine/ginkgo blind spot, untouc…

**[DELETED]** Worker LookupContainer when the Pod exists returns the container  `JB-worker-005`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Worker.LookupContainer sits in the byte-identical worker.go and consults only db.Worker.FindContainer, whose file atc/db/worker.go is unchanged on core; no commit in the range matches -SLookupContainer under atc/.

**[DELETED]** Worker LookupContainer when the Pod exists returns a container with a valid DBContainer for hijack support  `JB-worker-006`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Container.DBContainer and db.CreatedContainer.Handle are reached through the byte-identical worker.go and the unchanged atc/db/container.go; container.go's only core changes (stableCacheKey, buildVolumeMounts, buildArtifactInitContainers) are on the pod-building path this test does not enter.

**[DELETED]** Worker LookupContainer when the Pod exists but the DB container does not returns not found since the container is not tracked in the DB  `JB-worker-007`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The database-only lookup rule this pins lives entirely in the byte-identical worker.go plus the unchanged atc/db/worker.go; nothing on core touches it and worker.feature's replacing scenario was not rebased.

**[DELETED]** Worker LookupContainer when the Pod does not exist returns not found  `JB-worker-008`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same untouched pair as the sibling rows: Worker.LookupContainer in the byte-identical worker.go over db.Worker.FindContainer in the unchanged atc/db/worker.go, with no matching commit in aef2244a63..5133d0ddbc.

**[DELETED]** Worker LookupContainer when the pod was named from build-step metadata execs into the pod the step actually created, not the raw handle  `JB-worker-009`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — GeneratePodName (podname.go), PodExecutor.Exec (executor.go) and Worker.LookupContainer (worker.go) are all in byte-identical files, and the looked-up Run path execs rather than building a pod, so container.go's cache changes never run. process.go's 1164f9db3d refactor only alters a NEGATIVE PodStartupTimeout, which t…

**[DELETED]** Worker LookupContainer when the pod was named from build-step metadata refuses to fabricate a pod when the step's pod is gone  `JB-worker-010`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The 'has no pod to intercept' refusal is raised on the lookedUp branch of Container.Run, which core did not change — container.go's three changed symbols are stableCacheKey, buildVolumeMounts and buildArtifactInitContainers, none reachable once the container is a looked-up one.

**[DELETED]** Worker LookupContainer when the pod was named from build-step metadata refuses to replace a pod that has already exited  `JB-worker-011`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The 'already exited' guard and the exit-status annotation reading are on the same untouched lookedUp Run branch; worker.go, podname.go and executor.go are byte-identical on core and no commit in the range matches these symbols.

**[DELETED]** Worker CreateVolumeForArtifact when the volume repo is configured creates an artifact volume and returns it with the artifact  `JB-worker-012`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Worker.CreateVolumeForArtifact is in the byte-identical worker.go and atc/db/volume_repository.go is unchanged; atc/db/volume.go's only core edits are InitializeTaskCache's identity signature and TaskIdentifier's SQL, neither of which this row's CreateVolume/Created/InitializeArtifact chain touches.

**[DELETED]** Worker CreateVolumeForArtifact when the volume repo is configured returns the handle generated by the persisted volume  `JB-worker-013`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — db.CreatedVolume.Handle and VolumeRepository.FindVolume are unchanged on core (atc/db/volume.go's diff is confined to InitializeTaskCache and TaskIdentifier; volume_repository.go is byte-identical), and the calling method is in the byte-identical worker.go.

**[DELETED]** Worker CreateVolumeForArtifact when the volume repo is configured when the artifact store is configured returns a DaemonSetVolume  `JB-worker-014`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — ArtifactKey, NewDaemonSetVolume and DaemonSetVolume.Key/Handle are untouched: volume_daemonset.go's only core change is InitializeTaskCache, and the single -SArtifactKey hit in the range (0d336e062b) lands only on daemonset_integration_test.go, not on production. The ginkgo-only constant-key mutation this row's proper…

**[DELETED]** Worker CreateVolumeForArtifact when the volume repo is configured always returns a DaemonSetVolume  `JB-worker-015`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The Go-type assertion runs over NewDaemonSetVolume in volume_daemonset.go, whose only core change is InitializeTaskCache, called from the byte-identical worker.go; nothing on core alters which wrapper CreateVolumeForArtifact returns.

**[DELETED]** Worker CreateVolumeForArtifact when the volume repo is NOT configured returns an error  `JB-worker-016`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Both NewWorker and the 'volume repository not configured' fast-fail live in the byte-identical worker.go; config.go's core changes (06d3c556b1, 1e023e7ca4) add only new capability/namespace fields that NewConfig leaves zero and this path never reads.

**[DELETED]** Worker CreateVolumeForArtifact when CreateVolume fails returns the error  `JB-worker-017`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — db.NewVolumeRepository and VolumeRepository.CreateVolume are in atc/db/volume_repository.go, verified byte-identical on core, and the error is surfaced by the byte-identical worker.go — the closed-connection propagation is unchanged.

**[DELETED]** Worker CreateVolumeForArtifact database transition faults when transitioning to created state fails returns the error and leaves the volume creating  `JB-worker-018`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The CreatingVolume.Created transition and the leave-it-creating rule are in unchanged files (atc/db/volume.go's diff does not touch Created; worker.go is byte-identical), and the brine reimplementation failVolumeCreatedTransitionRepo lives in steps/worker.go, which the rebase did not modify.

**[DELETED]** Worker CreateVolumeForArtifact database transition faults when InitializeArtifact fails returns the error and creates no artifact  `JB-worker-019`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — db.CreatedVolume.InitializeArtifact keeps its (name string, buildID int) signature on core — the atc/db/volume.go diff changes only InitializeTaskCache and TaskIdentifier — and its brine counterpart failInitializeArtifactRepo sits in the unrebased steps/worker.go.

**[DELETED]** Worker LookupVolume when the volume exists in the DB returns a cache-backed volume  `JB-worker-020`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Worker.LookupVolume (worker.go), WrapVolumeForLookup and ArtifactKey (storage_daemonset.go, whose diff has no hunk between lines 211 and 679) and NewDaemonSetVolume (volume_daemonset.go, only InitializeTaskCache changed) are all untouched by core.

**[DELETED]** Worker LookupVolume when the volume exists in the DB does not match a different handle  `JB-worker-021`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The exact-handle rule is enforced by db.VolumeRepository.FindVolume in the byte-identical atc/db/volume_repository.go, reached from the byte-identical worker.go; no commit in the range matches -SLookupVolume under atc/.

**[DELETED]** Worker LookupVolume when the volume does not exist in the DB returns not found  `JB-worker-022`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same unchanged pair as its sibling: worker.go and atc/db/volume_repository.go are both byte-identical merge-base to core, and worker.feature's replacing outline was not rebased.

**[DELETED]** Worker LookupVolume when the DB returns an error returns the error  `JB-worker-023`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — db.NewVolumeRepository and FindVolume are in the byte-identical atc/db/volume_repository.go and the error-vs-not-found distinction is made in the byte-identical worker.go; nothing on core touches it.

**[DELETED]** Worker LookupVolume when no volume repo is configured returns not found  `JB-worker-024`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The deliberate misconfiguration-looks-like-absence behaviour is entirely inside the byte-identical worker.go's LookupVolume plus NewWorker; core changed neither, and the scenario recording it sits in the unrebased worker.feature.

**[DELETED]** Worker FindDaemonResourceCache when the daemon has the cache returns a DaemonSetVolume that can StreamOut via HTTP  `JB-worker-025`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — daemon_client.go, node_ip_resolver.go and resource_cache_key.go are byte-identical on core, and storage_daemonset.go's diff has no hunk between lines 211 and 679 where FindResourceCache and the probe path live — its changes are the resolveSigner field, CacheVolume, batchItem, BuildFetchInitContainers and an appended v…

**[DELETED]** Worker FindDaemonResourceCache when a downstream step wraps the returned volume via ArtifactFromVolume StreamOut on the wrapped artifact succeeds without resolving the daemon IP as a node name  `JB-worker-026`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The whole regression chain — FindDaemonResourceCache and ArtifactFromVolume in worker.go, WrapVolumeForLookup in the untouched region of storage_daemonset.go, DaemonSetVolume.StreamOut in volume_daemonset.go (only InitializeTaskCache changed), NodeIPResolver.Resolve and ArtifactLocator in byte-identical files — is unc…

**[DELETED]** Worker FindDaemonResourceCache when a probe hit occurs writes nothing to the ArtifactLocator for the cache key  `JB-worker-027`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — artifact_locator.go is byte-identical on core and the probe-hit path in storage_daemonset.go sits in the region with no diff hunks (211-679), so the must-not-write rule is intact; the permanent-residue status of this internal-state assertion is a migration property, not a rebase effect.

**[DELETED]** Worker FindDaemonResourceCache when the locator has a stale entry for a dead node does not return a cache hit from the stale locator entry  `JB-worker-028`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — ArtifactLocator.Record, DaemonClient and DaemonSetBackend.FindResourceCache are all in files or regions core did not touch (artifact_locator.go and daemon_client.go byte-identical; storage_daemonset.go unchanged between lines 211 and 679), so the stale-entry rejection reproduces exactly.

**[DELETED]** Worker ArtifactFromVolume when a DaemonSet backend is configured wraps a container-mount DeferredVolume as a DaemonSetVolume  `JB-worker-029`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — volume_deferred.go is byte-identical on core, NewDaemonSetVolume in volume_daemonset.go is unchanged (only InitializeTaskCache moved to an identity), and WrapVolumeForLookup sits in the untouched region of storage_daemonset.go; -SArtifactFromVolume returns no commit in the range.

**[DELETED]** Worker ArtifactFromVolume when a DaemonSet backend is configured wraps a StubVolume as a DaemonSetVolume  `JB-worker-030`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — stub_volume.go is byte-identical on core and the wrap runs through the same unchanged ArtifactFromVolume/WrapVolumeForLookup pair as its DeferredVolume sibling.

**[DELETED]** Worker ArtifactFromVolume when a DaemonSet backend is configured preserves the handle as the artifact key  `JB-worker-031`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — This row carries the property the ginkgo-only constant-key mutation attacks, and its whole surface is unchanged: the only -SArtifactKey commit in aef2244a63..5133d0ddbc (0d336e062b) touches daemonset_integration_test.go alone, and DaemonSetVolume.Key/Handle in volume_daemonset.go were not modified.

**[DELETED]** Worker ArtifactFromVolume when a DaemonSet backend is configured resolves the source node from the ArtifactLocator when the locator has an entry  `JB-worker-032`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Nothing on core touches this: artifact_locator.go is byte-identical and DaemonSetVolume.Source is unchanged in volume_daemonset.go. Its cannot-fail status (it only asserts the worker name, true with or without a locator entry) is a property of the test itself and is unaffected by the rebase.

**[DELETED]** Worker ArtifactFromVolume when a DaemonSet backend is configured returns nil when given a nil volume  `JB-worker-033`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The nil guard is a single branch of Worker.ArtifactFromVolume in the byte-identical worker.go; core has no commit matching -SArtifactFromVolume under atc/.

**[DELETED]** Worker ArtifactFromVolume when NO DaemonSet backend is configured (legacy exec-only mode) returns the volume unchanged  `JB-worker-034`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The storageBackend==nil identity return lives in the byte-identical worker.go and the DeferredVolume it returns comes from the byte-identical volume_deferred.go, so the legacy fallback is exactly as recorded.

**[DELETED]** Worker ArtifactFromVolume when NO DaemonSet backend is configured (legacy exec-only mode) returns nil when given a nil volume  `JB-worker-035`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — Same single unchanged nil branch in the byte-identical worker.go, on the no-backend arm; core changed nothing on this path.

**[DELETED]** Worker ArtifactFromVolume regression guard for the exec-backed artifact-read path never returns a *Volume (exec-backed) when a DaemonSet backend is configured  `JB-worker-036`
  - recorded evidence: PER-FILE (deletion commit `e826623c6a`; nothing names this test alone)
  - rebase impact: NOT IMPACTED — The guard ranges over NewDeferredVolume and NewStubVolume (both byte-identical files) through ArtifactFromVolume and WrapVolumeForLookup (worker.go byte-identical; storage_daemonset.go unchanged in that region), and jetbridge.Volume's only core edit in volume.go is InitializeTaskCache, which this type assertion does n…


## Coverage ported from core 0d336e062b (2026-09-04)

The rebase resolved two modify/delete conflicts by taking the deletion
(`container_test.go` and `behavioral_permutations_test.go`, both modified on
core by 0d336e062b). Core's side of those files was not only a mechanical
fixture update: it added a helper, `assertAllPodMountsResolve(pod)`, and a
call site. Taking the deletion dropped that. Three commits put the coverage
into brine instead, each measured both-red before it was committed.

- `ff25608c18` — test(brine): assert that every pod mount names exactly one volume
- `ae548d0cb6` — test(brine): key a run job's cache on its template, not its per-run pipeline
- `27d81692fa` — test(brine): refuse an explicit node-local cache store a step with no key

### New and strengthened scenarios

- **container-pod.feature** — A run job's cache is keyed on the template it came from, not on its per-run pipeline: NEW scenario. stableCacheKey's run arm, new on core in 0d336e062b: with ContainerSpec.TaskCacheIdentity{TeamID, TemplatePipelineID, RunJobName} and CacheHostPath set, the cache volume is a hostPath under run-<team>-<template>-<step>-<hash>. The key is asserted WHOLE (/var/concourse/cache/run-17-23-build-assets-34a6ec221a61, the same digest core's own storage_daemonset_test.go pins) so that a change to the hash CONTE…
- **container-pod.feature** — An explicit node-local cache store is refused a step with nothing to key on: NEW scenario. The downgrade 0d336e062b added: Config.CacheStore=hostpath named explicitly is still downgraded to an emptyDir when ContainerSpec.TaskCacheIdentity is nil. Reuses existing steps only. It is the negative counterpart of the existing outline row `| hostpath | survives the pod |`, which holds only because that step also names a job.
- **container-pod.feature** — A cache is kept on the node, under a key stable across builds: Gained 'And every mount in the pod names exactly one of its volumes' — the port of assertAllPodMountsResolve, which 0d336e062b added to container_test.go and called from exactly this case.
- **container-pod.feature** — With no explicit choice, caches follow the artifact store onto the node: Gained the mount-resolution invariant (storage-backend cache arm).
- **container-pod.feature** — A step sees its working directory and every input: Gained the mount-resolution invariant (dir + two input volumes).
- **container-pod.feature** — An output that shares an input's path gets one volume, not two: Gained the mount-resolution invariant (input/output path overlap, the case where one volume is deliberately shared by two roles).
- **container-pod.feature** — An output on its own path gets its own volume: Gained the mount-resolution invariant (input + distinct output).
- **container-pod.feature** — A cache and a scratch path are different volumes: Gained the mount-resolution invariant (cache + scratch, the two independent name counters).
- **container-pod.feature** — An input sharing an output's path is filed under the output's name: Gained the mount-resolution invariant (storage-backend pod with init containers, so the invariant also walks InitContainers' mounts).

### Both-red evidence for each

These six rows carry ids `JB-worker-037` .. `JB-worker-042`. They continue the
numbering of `worker_test.go` above only because they are the last rows in this
file; they are NOT worker_test.go tests, and they are NOT part of the 401-row
deletion inventory, which ends at `JB-worker-036`. Each is a MEASUREMENT taken
to justify a brine addition — a named production mutation run against core's
own It (or an existing branch test) and against the brine scenario written to
replace it — so its verdict is `BOTH_RED` or `INERT` rather than DELETED /
REFUTED / GAP, and no test was deleted on any of them. A reconciler counting
deleted tests should exclude all six; one auditing the ported coverage should
read exactly these six.

**[BOTH_RED]** ported coverage: every pod mount names exactly one volume (the duplicate half)  `JB-worker-037`
  - go test: Container / Run with cache hostPath configured / when CacheHostPath is set / It uses hostPath volumes with stable keys for caches — the It that 0d336e062b added `assertAllPodMountsResolve(pod)` to
  - source: core revision 5133d0ddbc, atc/worker/jetbridge/container_test.go (the file the rebase resolved as a delete). Copied verbatim, together with assertAllPodMountsResolve, into a TEMPORARY atc/worker/jetbridge/zz_port_probe_test.go under Describe("PortProbe Container"); deleted after measurement, never committed (`ls atc/w…
  - mutation: atc/worker/jetbridge/container.go:(*Container).buildVolumeMounts — in the standalone CacheHostPath cache arm (the `else` branch of `case CacheStoreHostPath:`), one line inserted after the cache VolumeMount append: `volumes = append(volumes, volumes[len(volumes)-1]) // MUTATION M1`. The cache volume NAME is now declared twice; every mount still resolves by lookup, and the pod is rejected by the API server.
  - go: RED. `[FAILED] container "main" mount "cache-1" must resolve to exactly one pod volume / Expected <int>: 2 to equal <int>: 1` at zz_port_probe_test.go:164. `Ran 1 of 21 Specs in 3.588 seconds` / `FAIL! -- 0 Passed | 1 Failed | 0 Pending | 20 Skipped`. CONTROL: on unmutated source the same focus is `ok github.com/conco…
  - brine scenario: container-pod.feature — "A cache is kept on the node, under a key stable across builds" (and, same mutation, "A run job's cache is keyed on the template it came from, not on its per-run pipeline")
  - brine: RED. run_end 44 scenarios, 42 passed, 2 failed. `Step FAILED: And every mount in the pod names exactly one of its volumes` / `error: container "main" mounts volume "cache-1" at "/tmp/build/workdir/.cache", and the pod declares 2 volumes by that name` (second scenario: `... at "/work/cache", and the pod declares 2 volumes by that name`). DISCRIMINATION: the preceding step `Then the cache at "/tmp/build/workdir/.cache…

**[BOTH_RED]** ported coverage: a run job's cache key SHAPE  `JB-worker-038`
  - go test: TestDaemonSetMode_CachesAreDirectHostPath; TestDaemonSetBackend_CacheVolume_UsesRunIdentityInsteadOfEphemeralIDs
  - source: Already present on the branch — atc/worker/jetbridge/daemonset_integration_test.go:556 and atc/worker/jetbridge/storage_daemonset_test.go:194. No restore needed: the run arm of the key was never covered by the deleted container_test.go, which only ever exercised the JobID arm. Measured in place with `go test -count=1…
  - mutation: atc/worker/jetbridge/container.go:stableCacheKey — the run arm's return, `return fmt.Sprintf("run-%d-%d-%s-%s", identity.TeamID, identity.TemplatePipelineID, safe, hash)` -> `return fmt.Sprintf("job-%d-%s-%s", identity.JobID, safe, hash) // MUTATION M2a`. A run job's cache is filed under the ordinary-job key shape with a JobID of 0, so every run job in the cluster shares one directory.
  - go: RED. `--- FAIL: TestDaemonSetMode_CachesAreDirectHostPath (0.00s) / daemonset_integration_test.go:600: expected exact run cache key, got /var/concourse/artifacts/caches/job-0-build-f07d9dac9385`; `--- FAIL: TestDaemonSetBackend_CacheVolume_UsesRunIdentityInsteadOfEphemeralIDs (0.00s) / storage_daemonset_test.go:203: e…
  - brine scenario: container-pod.feature — "A run job's cache is keyed on the template it came from, not on its per-run pipeline"
  - brine: RED. run_end 44 scenarios, 43 passed, 1 failed. `Step FAILED: Then the cache at "/work/cache" is kept on the node under "/var/concourse/cache/run-17-23-build-assets-34a6ec221a61"` / `error: expected the cache filed under "/var/concourse/cache/run-17-23-build-assets-34a6ec221a61" so the next build finds it; it is at "/var/concourse/cache/job-0-build-assets-34a6ec221a61"`.

**[BOTH_RED]** ported coverage: a run job's cache key HASH INPUT  `JB-worker-039`
  - go test: TestDaemonSetMode_CachesAreDirectHostPath; TestDaemonSetBackend_CacheVolume_UsesRunIdentityInsteadOfEphemeralIDs
  - source: Already present on the branch — atc/worker/jetbridge/daemonset_integration_test.go:556 and atc/worker/jetbridge/storage_daemonset_test.go:194. Measured in place.
  - mutation: atc/worker/jetbridge/container.go:stableCacheKey — the run arm's HASH INPUT, `fmt.Fprintf(h, "run-task-cache/v1\x00%d\x00%d\x00%s\x00%s\x00%s", identity.TeamID, identity.TemplatePipelineID, identity.RunJobName, stepName, cachePath)` -> `fmt.Fprintf(h, "%d\x00%s\x00%s", identity.JobID, stepName, cachePath) // MUTATION M2b`. The key KEEPS its run- shape and its team/template segments; only the digest changes, so two d…
  - go: RED. `--- FAIL: TestDaemonSetMode_CachesAreDirectHostPath (0.00s) / daemonset_integration_test.go:600: expected exact run cache key, got /var/concourse/artifacts/caches/run-17-23-build-fb8c9332316b`; `--- FAIL: TestDaemonSetBackend_CacheVolume_UsesRunIdentityInsteadOfEphemeralIDs (0.00s) / storage_daemonset_test.go:20…
  - brine scenario: container-pod.feature — "A run job's cache is keyed on the template it came from, not on its per-run pipeline"
  - brine: RED. run_end 44 scenarios, 43 passed, 1 failed. `Step FAILED: Then the cache at "/work/cache" is kept on the node under "/var/concourse/cache/run-17-23-build-assets-34a6ec221a61"` / `error: expected the cache filed under "/var/concourse/cache/run-17-23-build-assets-34a6ec221a61" so the next build finds it; it is at "/var/concourse/cache/run-17-23-build-assets-44750e2ea35a"`.

**[BOTH_RED]** ported coverage: an explicit hostpath cache store downgrades without an identity  `JB-worker-040`
  - go test: Container / Run with cache hostPath configured / when CacheHostPath is set but JobID is 0 (one-off build) / It falls back to emptyDir for one-off builds — the It 0d336e062b turned into the downgrade test by adding `cfgWithHostPath.CacheStore = jetbridge.Cache…
  - source: core revision 5133d0ddbc, atc/worker/jetbridge/container_test.go. Copied verbatim into the TEMPORARY atc/worker/jetbridge/zz_port_probe_test.go; deleted after measurement, never committed.
  - mutation: atc/worker/jetbridge/container.go:(*Container).buildVolumeMounts — the downgrade block `if cacheMode == CacheStoreHostPath && c.containerSpec.TaskCacheIdentity == nil { cacheMode = CacheStoreEmptyDir }` with its body replaced by `c.containerSpec.TaskCacheIdentity = &atc.TaskCacheIdentity{} // MUTATION M3`. The downgrade never happens: a keyless step keeps node-local storage and is filed under the degenerate key run-…
  - go: RED. `[FAILED] one-off builds should not use hostPath / Expected <*v1.HostPathVolumeSource | 0x140005d4f30>: { Path: "/var/concourse/cache/run-0-0--5a116b607dc0", Type: "DirectoryOrCreate", } to be nil` at zz_port_probe_test.go:148. `Ran 1 of 21 Specs in 2.305 seconds` / `FAIL! -- 0 Passed | 1 Failed | 0 Pending | 20…
  - brine scenario: container-pod.feature — "An explicit node-local cache store is refused a step with nothing to key on"
  - brine: RED, and isolated. run_end 44 scenarios, 43 passed, 1 failed. `Step FAILED: Then the volume mounted at "/tmp/build/workdir/.cache" is lost with the pod` / `error: expected the volume at "/tmp/build/workdir/.cache" to be ephemeral, it is node-local storage`. No other scenario moved: the existing one-off scenario reaches EmptyDir by auto-detect rather than by the downgrade, so this mutation touches only the new scenar…

**[INERT]** ported coverage: the identity gate on the two auto-detect cache arms  `JB-worker-041`
  - go test: Container / Run with cache hostPath configured — BOTH Its (uses hostPath volumes with stable keys for caches; falls back to emptyDir for one-off builds)
  - source: core revision 5133d0ddbc, atc/worker/jetbridge/container_test.go, in the TEMPORARY zz_port_probe_test.go (deleted after measurement).
  - mutation: atc/worker/jetbridge/container.go:(*Container).buildVolumeMounts — THE IDENTITY GATE REMOVED, i.e. the `&& c.containerSpec.TaskCacheIdentity != nil` conjunct deleted from both auto-detect cases: `case c.storageBackend != nil && len(resolvedCaches) > 0 && c.containerSpec.TaskCacheIdentity != nil:` -> `case c.storageBackend != nil && len(resolvedCaches) > 0:` and `case c.config.CacheHostPath != "" && c.containerSpec.T…
  - go: GREEN. `ok github.com/concourse/concourse/atc/worker/jetbridge 23.757s` — both probe Its pass under the mutation.
  - brine scenario: container-pod.feature (all 44, including the four described-cache scenarios 81d13b70 repaired) and artifact-recording.feature — "A task cache is not filed among the step data the daemon sweeps"
  - brine: GREEN. container-pod.feature run_end `44 scenarios, 44 passed, 0 failed`; artifact-recording.feature run_end `22 scenarios, 22 passed, 0 failed`. No step moved.

**[BOTH_RED]** ported coverage: an identity can still select node-local cache storage  `JB-worker-042`
  - go test: Container / Run with cache hostPath configured / when CacheHostPath is set / It uses hostPath volumes with stable keys for caches
  - source: core revision 5133d0ddbc, atc/worker/jetbridge/container_test.go, in the TEMPORARY zz_port_probe_test.go (deleted after measurement).
  - mutation: atc/worker/jetbridge/container.go:(*Container).buildVolumeMounts — the downgrade's guard widened to fire for every step: `if cacheMode == CacheStoreHostPath && c.containerSpec.TaskCacheIdentity == nil {` -> `if cacheMode == CacheStoreHostPath { // MUTATION M5`. The ContainerSpec's identity can no longer select node-local cache storage at all. This is the regression the rebase's identity fix 81d13b70 exists for, expr…
  - go: RED. `[FAILED] expected a hostPath volume for cache / Expected <*v1.Volume | 0x0>: nil not to be nil` at zz_port_probe_test.go:90.
  - brine scenario: container-pod.feature — "A cache is kept on the node, under a key stable across builds"; "An explicit cache store overrides the artifact store default (row 1)"; "With no explicit choice, caches follow the artifact store onto the node"; "A run job's cache is k…
  - brine: RED. container-pod.feature run_end `44 scenarios, 40 passed, 4 failed`; artifact-recording.feature run_end `22 scenarios, 21 passed, 1 failed`. Failing steps: `Then the cache at "/tmp/build/workdir/.cache" is kept on the node under "/var/concourse/cache/job-7-compile-"` / `expected the cache at "/tmp/build/workdir/.cache" to live on the node so it survives the pod; it is ephemeral`; `Then the cache at "/work/cache"…

Suite after the three commits: 35 features, 568 scenarios, 568 passed, 0
failed (566 baseline + the 2 new scenarios); `go test ./atc/worker/jetbridge/`
green with no probe file present.

One honest limit, recorded rather than buried: the mount invariant has two
halves and only the DUPLICATE half is new coverage. A DANGLING mount was
already caught by `volumeAt`, which answers `mount %q names volume %q, which
the pod does not define`. `JB-worker-037` therefore uses a duplicate-volume
mutation, and the discrimination was checked explicitly: under it the preceding
cache-location assertion PASSED and only the new step failed.


## Tests core added after the rebase base (kept, no brine evidence)

The branch was rebased a second time on 2026-09-05, onto `c1c3e70e7c`. In
between, core landed `2a9355e1e6` ("fix(jetbridge): tear down a supervised
step's pod when its context ends"), which added one It to each of three files
this branch deletes: `container_test.go`, `integration_test.go` and
`process_test.go`. The rebase hit them as modify/delete conflicts and took the
deletion, which is right for the file's HISTORY and wrong for these three
tests: the campaign never measured them, so there is no both-red pairing for
them and no brine scenario that inherits them. The protocol says a Go test may
be deleted only with both-red evidence recorded here. They have none, so they
are KEPT — appended to the matching `*_restored_test.go`, adapted only where
the surrounding fixture had been pruned.

Each was run in isolation after the move and passes (`Ran 1 of 89 Specs`,
`SUCCESS! -- 1 Passed | 0 Failed`).

**[KEPT]** Container Run into existing pod (fly hijack) leaves the pod alone when the hijack session's context ends  `JB-kept-000`
  - source: core `2a9355e1e6`, `atc/worker/jetbridge/container_test.go`
  - recorded evidence: NONE — the test postdates the rebase base `09faf11a50`, so it was never classified, never measured, and no brine scenario was offered in its place.
  - what it holds: the teardown `2a9355e1e6` added to `(*execProcess).Wait` must NOT fire for a looked-up Container. A hijack session's context ends when the operator closes the window, and the pod belongs to the step being debugged, not to the session.
  - carried to: `atc/worker/jetbridge/container_restored_test.go`, inside the existing `Describe("Run into existing pod (fly hijack)")`, whose `hijackContainer`/`hijackExecutor` fixture the restored file already keeps. Verbatim but for the `// KEPT FROM CORE` header comment.

**[KEPT]** Integration build cancellation deletes the pause pod when the step's own context is cancelled  `JB-kept-001`
  - source: core `2a9355e1e6`, `atc/worker/jetbridge/integration_test.go`
  - recorded evidence: NONE — postdates the rebase base `09faf11a50`.
  - what it holds: an aborted supervised task's pause pod is deleted from `Wait`, with `GracePeriodSeconds: 0`, rather than left to the reaper — whose fast path only deletes pods carrying an exit-status annotation, which an abandoned step never records.
  - carried to: `atc/worker/jetbridge/integration_restored_test.go`. Its `Describe("build cancellation")` had been pruned entirely (its one pre-existing It, `JB-integration-004`, stays deleted on its own evidence), so the Describe is reintroduced as a wrapper holding only this It. The It itself is verbatim; it uses the `createContainer`, `simulatePodRunning`, `fakeExecutor` and `fakeClientset` fixtures the restored file already keeps.

**[KEPT]** Process execProcess failure state detection deletes a supervised task's pause pod when context is cancelled  `JB-kept-002`
  - source: core `2a9355e1e6`, `atc/worker/jetbridge/process_test.go`
  - recorded evidence: NONE — postdates the rebase base `09faf11a50`.
  - what it holds: the same teardown at the unit seam, and its exclusion — core placed it beside `preserves the pause pod when context is cancelled (for fly hijack)` (a get step, whose command dies with the exec stream) so the two read as a pair. That sibling is `JB-process-027` and stays deleted on its own evidence; only the supervised half is carried.
  - carried to: `atc/worker/jetbridge/process_restored_test.go`, under a new `Describe("supervised step teardown on context end")`. The It is verbatim; the exec-mode fixture it needed (`fakeExecExecutor` + `execWorker` with the executor set) is reproduced from core's `Describe("execProcess failure state detection")` BeforeEach, minus the `execContainer` that only the deleted siblings used.
