# Per-test disposition: the gc/lidar campaign

An auditor could not verify the chain from a deleted test to its evidence,
because this record did not exist. It does now. One row per It measured, with
the mutation, what brine did under it, and the skeptic's reason where a claim
was refuted.

Verdicts: DELETED = both-red, survived refutation, removed.
REFUTED = a skeptic broke the pairing; the test STAYS.
GAP = the Go test reddens and brine does not; the test STAYS and brine owes a scenario.
INERT = no mutation reddened it; the test STAYS, recorded as a defect in the existing suite.
BRINE_STRONGER = brine discriminates where the Go test does not; removed.


## access_tokens_collector_test.go

**[GAP]** AccessTokensCollector Run forwards its configured leeway, sparing a token only just expired
  - mutation: M_V — atc/gc/access_tokens_collector.go:29: `c.lifecycle.RemoveExpiredAccessTokens(c.leeway)` -> `RemoveExpiredAccessTokens(0)`, the constructor's leeway silently dropped

**[GAP]** AccessTokensCollector Run removes tokens that have expired and keeps those that have not
  - mutation: M_U — atc/db/access_token_lifecycle.go:25 (RemoveExpiredAccessTokens): the `expires_at < now() - '%d seconds'::interval` predicate replaced with `1=1`, so every access token in the deployment is deleted on every sweep


## artifacts_collector_test.go

**[DELETED]** ArtifactCollector Run keeps an artifact that has not yet reached the cutoff
  - mutation: M_R — atc/db/worker_artifact_lifecycle.go:24 (RemoveExpiredArtifacts): `created_at < NOW() - interval '12 hours'` -> `interval '6 hours'`
  - brine: gc-caches.feature, Scenario Outline "A build artifact is reclaimed only once it is older than twelve hours — <case>" (row 2, `one minute short of it`) — `FAIL (6/7 steps)` / `Step FAILED: And the artifact "under-test" survived the sweep`
  - skeptic: Not re-run. Same wiring and same insert statement as the row above. The It inserts one row at 11h59m and asserts `ConsistOf("just-inside")`; outline row 2 uses the identical 11h59m age and asserts `the artifact "under-test" survived the sweep`, plus a second artifact at 1h that the It does not have. The claim also reports the useful control — the sibling It stayed `SUCCESS! -- 1 Passed | 0 Failed`

**[DELETED]** ArtifactCollector Run removes artifacts older than twelve hours and keeps the rest
  - mutation: M_Q — atc/db/worker_artifact_lifecycle.go:24 (RemoveExpiredArtifacts): `created_at < NOW() - interval '12 hours'` -> `interval '24 hours'`
  - brine: gc-caches.feature, Scenario Outline "A build artifact is reclaimed only once it is older than twelve hours — <case>" (row 1, `one minute past the cutoff`) — `FAIL (6/7 steps)` / `Step FAILED: And the artifact "under-test" has been reclaimed` (18 passed, 1 failed of 19)
  - skeptic: Not re-run. I did verify the wiring is not a re-implementation: steps/gc_caches.go builds `gc.NewArtifactCollector(db.NewArtifactLifecycle(database.Conn))` and seeds rows with the byte-identical statement the ginkgo suite uses, `INSERT INTO worker_artifacts(name, created_at) VALUES($1, NOW() - $2::interval)`, reading back with `SELECT name FROM worker_artifacts ORDER BY name`. The It's `ConsistOf(


## check_collector_test.go

**[GAP]** keeps the only completed check for a scope
  - mutation: atc/db/check_lifecycle.go:37 (DeleteCompletedChecks) — the clause `AND NOT EXISTS ( SELECT 1 FROM resource_builds WHERE build_id = b.id )` deleted, so the scope's last_check_build_id no longer protects the most recent completed check. (overlay M12)

**[GAP]** removes a completed check once a newer one has completed
  - mutation: atc/db/check_lifecycle.go:36 (DeleteCompletedChecks) — `WHERE completed AND resource_id IS NOT NULL` changed to `WHERE FALSE AND completed AND resource_id IS NOT NULL`, so the resource arm of the delete matches nothing. (overlay M11)


## container_collector_test.go

**[DELETED]** keeps a container missing for less than the grace period
  - mutation: atc/db/container_repository.go:178 (RemoveMissingContainers) — the grace-period comparison replaced by `sq.Expr("missing_since IS NOT NULL")`, i.e. delete on the first missed report. (overlay M3b; isolates this row, where the M3 inversion reddens both missing-container rows)
  - brine: FAIL  A container the worker stopped reporting is deleted once the grace period passes, but not while the worker itself is stalled (11/12 steps, 20ms) — `Step FAILED: And the container "just-missing" is still in the database` / `Error: expected the container rows the sweep left behind to include "ju
  - skeptic: Reproduced under my own overlay M3b (grace-period comparison -> `sq.Expr("missing_since IS NOT NULL")`). Go: `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed`, `[FAILED] Expected <bool>: false to be true` at container_collector_test.go:198. brine: 9 passed 1 failed, `the container "just-missing" is still in the database | expected the container rows the sweep left behind to include "just-missi

**[DELETED]** leaves a container whose build is still interceptible alone
  - mutation: atc/db/container_repository.go:235 (FindOrphanedContainers) — `sq.Eq{"b.interceptible": false},` replaced by `sq.Expr("1=1"),`, so every container with a build_id is treated as orphaned. (overlay M2)
  - brine: FAIL  A container is reclaimed once its build is finished, unless somebody is still inside it (13/13 steps, 39ms) — `Step FAILED: And the container "live-build" is still created` / `Error: expected the containers still in the created state to include "live-build", found [fresh-hijack]`
  - skeptic: Reproduced under my own overlay M2 (container_repository.go FindOrphanedContainers, `sq.Eq{"b.interceptible": false}` -> `sq.Expr("1=1")`). Go: `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed`. brine failing step in scenario 1: `the container "live-build" is still created | expected the containers still in the created state to include "live-build", found [fresh-hijack]` — byte-identical to th

**[DELETED]** leaves a created orphan hijacked within the grace period alone
  - mutation: atc/gc/container_collector.go:132 — the whole `if time.Since(createdContainer.LastHijack()) > c.hijackContainerGracePeriod {` guard removed, so every created orphan is transitioned unconditionally. (overlay M1b)
  - brine: FAIL  A container is reclaimed once its build is finished, unless somebody is still inside it (12/13 steps, 60ms) — `Step FAILED: And the container "fresh-hijack" is still created` / `Error: expected the containers still in the created state to include "fresh-hijack", found [live-build]`. The other 
  - skeptic: Reproduced under my own overlay M1b (guard block removed, transition unconditional). Go: `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed`, `Expected <string>: destroying to equal <string>: created` at container_collector_test.go:168. brine: 9 passed 1 failed, `the container "fresh-hijack" is still created | expected the containers still in the created state to include "fresh-hijack", found [l

**[DELETED]** leaves an excess check container hijacked within the grace period alone
  - mutation: atc/db/container_repository.go:466 (DestroyExcessCheckContainers) — the line `AND (c.last_hijack IS NULL OR NOW() - c.last_hijack > $2)` deleted, and the now-unused parameter dropped from the Exec at :470-471 (`repository.conn.Exec(query, maxPerResource)`). (overlay M7b). NOTE: a first attempt (M7) replaced the clause with `AND ($2 IS NOT NULL)` and was VOID — Postgres returned `ERROR: could not d
  - brine: FAIL  Only the newest check container for a resource survives the cap, and a hijacked one survives it too (8/8 steps, 21ms) — `Step FAILED: And the container "hijacked-check" is still created` / `Error: expected the containers still in the created state to include "hijacked-check", found [newest-che
  - skeptic: Reproduced under my own overlay M7b (the `AND (c.last_hijack IS NULL OR NOW() - c.last_hijack > $2)` clause deleted and the `$2` parameter dropped from the Exec — I verified it compiles and Postgres accepts it, so this is not the VOID $2-type-error variant). Go: `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed`, `Expected <string>: destroying to equal <string>: created` at container_collector_

**[DELETED]** marks a created orphan hijacked beyond the grace period as destroying
  - mutation: atc/gc/container_collector.go:132 — `> c.hijackContainerGracePeriod` changed to `> 24*time.Hour`, i.e. the configured grace period replaced by a constant that swallows the one-hour-old hijack but not the never-hijacked row. (overlay M1c; this isolates the row, whereas the M1 inversion reddens three rows at once)
  - brine: FAIL  A container is reclaimed once its build is finished, unless somebody is still inside it (11/13 steps, 21ms) — `Step FAILED: And the container "stale-hijack" is now destroying` / `Error: expected the containers now marked destroying to include "stale-hijack", found [never-hijacked]`. Nine other
  - skeptic: Reproduced under my own overlay M1c (`> c.hijackContainerGracePeriod` -> `> 24*time.Hour`). Go: `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed`, `[FAILED] Expected <string>: created to equal <string>: destroying` at container_collector_test.go:158. brine: 9 passed 1 failed, failing step `the container "stale-hijack" is now destroying | expected the containers now marked destroying to include

**[DELETED]** marks a created orphan that was never hijacked as destroying
  - mutation: atc/gc/container_collector.go:132 — `if time.Since(createdContainer.LastHijack()) > c.hijackContainerGracePeriod {` changed to `< c.hijackContainerGracePeriod`. (overlay M1)
  - brine: FAIL  A container is reclaimed once its build is finished, unless somebody is still inside it (10/13 steps, 21ms) — `Step FAILED: And the container "never-hijacked" is now destroying`
  - skeptic: Reproduced independently under my own overlay M1 (container_collector.go:132 `>` -> `<`). Go: `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed`, `[FAILED] Expected <string>: created to equal <string>: destroying` at container_collector_test.go:148. brine (10 scenarios, 7 passed 3 failed) failed the exact analogue step: `the container "never-hijacked" is now destroying | expected the containers

**[DELETED]** marks check containers beyond the per-resource cap as destroying
  - mutation: atc/gc/container_collector.go:14 — `const maxCheckContainersPerResource = 1` changed to `= 2`. (overlay M6)
  - brine: FAIL  Only the newest check container for a resource survives the cap, and a hijacked one survives it too (7/8 steps, 17ms) — `Step FAILED: And the container "excess-check" is now destroying` / `Error: expected the containers now marked destroying to include "excess-check", found []`
  - skeptic: This It has TWO assertions, so I tested both. Assertion 1 (oldest -> destroying) reproduces under M6 (`maxCheckContainersPerResource = 1` -> `2`): Go `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed` at :230; brine `the container "excess-check" is now destroying | expected the containers now marked destroying to include "excess-check", found []` (though M6 is broad — 4 of 10 scenarios). Assert

**[DELETED]** marks failed containers as destroying
  - mutation: atc/db/container_repository.go:365 (DestroyFailedContainers) — `Where(sq.Eq{"state": string(atc.ContainerStateFailed)})` changed to `string(atc.ContainerStateCreated)`. (overlay M5)
  - brine: FAIL  A container that failed to be created is marked for destruction, and nothing else is (6/7 steps, 9ms) — `Step FAILED: And the container "failed-container" is now destroying` / `Error: expected the containers now marked destroying to include "failed-container", found [live-build]`
  - skeptic: Nearly refuted, then not. The It asserts MORE than the claim's brine scenario: before the sweep it asserts `Expect(stateOf(failed.Handle())).To(Equal(string(atc.ContainerStateFailed)))` (line 210), pinning `creatingContainer.Failed()`. I built overlay F (atc/db/container.go:97, `Set("state", atc.ContainerStateFailed)` -> `ContainerStateDestroying`): the Go It goes red — `[FAILED] Expected <string>

**[DELETED]** removes a container missing for longer than the grace period
  - mutation: atc/db/container_repository.go:178 (RemoveMissingContainers) — `sq.Expr(fmt.Sprintf("NOW() - missing_since > '%s'", ...))` changed to `NOW() - missing_since < '%s'`, inverting the grace-period comparison. (overlay M3)
  - brine: FAIL  A container the worker stopped reporting is deleted once the grace period passes, but not while the worker itself is stalled (10/12 steps, 25ms) — `Step FAILED: And the container "long-missing" has been removed from the database` / `Error: expected a container "long-missing" to have been delet
  - skeptic: Reproduced under my own overlay M3 (RemoveMissingContainers `NOW() - missing_since >` -> `<`). Go: `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed`, `[FAILED] Expected <bool>: true to be false` at container_collector_test.go:189. brine: 7 passed 3 failed; the relevant failing step is `the container "long-missing" has been removed from the database | expected a container "long-missing" to have

**[REFUTED]** collects everything downstream when destroying failed containers fails
  - mutation: atc/gc/container_collector.go:55-59 — `return errs` added inside the `if err != nil` block that follows `c.markFailedContainersAsDestroying(...)`. (overlay M9)
  - skeptic: REFUTED for the same reason and by the same measurement as the sibling. Assertion `Expect(err).To(MatchError(ContainSubstring("nope")))` at container_collector_test.go:322 has no brine counterpart. Under overlay E the Go spec goes red — `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed`, failure at :322 — while gc-containers.feature reports `"scenarios":10,"passed":10,"failed":0`. The claim's o

**[REFUTED]** collects everything downstream when finding orphans fails
  - mutation: atc/gc/container_collector.go:49-53 — `return errs` added inside the `if err != nil` block that follows `c.cleanupOrphanedContainers(...)`, so Run short-circuits at the first step. (overlay M8)
  - skeptic: REFUTED: the It asserts something no brine scenario anywhere asserts. Its first assertion is `Expect(err).To(MatchError(ContainSubstring("nope")))` (line 308) — that Run's returned error carries the CAUSE, not merely that it is non-nil. brine's counterpart step is `the sweep reported the failure rather than a clean pass`, which by its own comment in steps/gc_containers.go checks only `in.Err == ni

**[REFUTED]** collects everything downstream when removing missing containers fails
  - mutation: atc/gc/container_collector.go:61-65 — `return errs` added inside the `if err != nil` block that follows `c.containerRepository.RemoveMissingContainers(...)`. (overlay M10)
  - skeptic: REFUTED, same gap. `Expect(err).To(MatchError(ContainSubstring("nope")))` at container_collector_test.go:336 is uncovered. Under overlay E: Go `Ran 1 of 109 Specs` / `FAIL! -- 0 Passed | 1 Failed` with the failure at :336; brine gc-containers.feature `"scenarios":10,"passed":10,"failed":0`. The claim's M10 pairing itself reproduces (Go `[FAILED] Expected <string>: created to equal <string>: destro

**[INERT]** succeeds with nothing to collect
  - mutation: Three honest mutations tried, none reddened it. (a) atc/gc/container_collector.go:132 `time.Since(createdContainer.LastHijack()) > c.hijackContainerGracePeriod` -> `<`. (b) atc/db/container_repository.go:235 `sq.Eq{"b.interceptible": false}` -> `sq.Expr("1=1")`. (c) atc/db/container_repository.go:365 `Where(sq.Eq{"state": string(atc.ContainerStateFailed)})` -> `...ContainerStateCreated)`. It pins 


## deprecated_scope_collector_test.go

**[GAP]** DeprecatedScopeCollector [It] collects scopes past the grace period
  - mutation: m10: atc/gc/deprecated_scope_collector.go:37-42 — the `dsc.conn.Exec(DELETE FROM resource_config_scopes ...)` removed, the Run body returning `nil` without executing anything.

**[GAP]** DeprecatedScopeCollector [It] does NOT collect scopes within the grace period
  - mutation: m9: atc/gc/deprecated_scope_collector.go:41 — `AND deprecated_at < now() - $1::interval` removed from the DELETE, so every deprecated scope is collected immediately regardless of the grace period.


## destroyer_test.go

**[DELETED]** Destroyer DestroyContainers does nothing when the handle list is nil
  - mutation: M_C — atc/gc/destroyer.go:46-48: the `if currentHandles == nil { return nil }` guard deleted from DestroyContainers (and the matching one in DestroyVolumes)
  - brine: gc-reclamation.feature, Scenario "A report that never arrived is not a report of nothing" — `FAIL (6/7 steps)` / `Step FAILED: And the container "survivor-container" survived the sweep`
  - skeptic: Re-run, because the claimed mutation M_C deleted the nil guard from BOTH DestroyContainers and DestroyVolumes and so could not attribute the reddening to either. I built the container-only version (deleted `if currentHandles == nil { return nil }` from DestroyContainers only, volume guard untouched). Go, focused on both nil-guard Its: `Ran 2 of 109 Specs in 1.072 seconds` / `FAIL! -- 1 Passed | 1 

**[DELETED]** Destroyer DestroyContainers leaves containers on a different worker alone
  - mutation: M_I — atc/db/container_repository.go:199-201 (RemoveDestroyingContainers): the `sq.Eq{"worker_name": workerName}` predicate replaced with `sq.Expr("1=1")`, so the sweep reaches across workers
  - brine: gc-reclamation.feature, Scenario "A worker that reports holding nothing loses its own destroying rows and nobody else's" — `FAIL (10/11 steps)` / `Step FAILED: And the container "neighbours-container" survived the sweep`
  - skeptic: Not re-run. The one thing worth flagging is a direction asymmetry: the Go It sweeps a worker that has NO destroying rows of its own (`DestroyContainers(other.Name(), []string{})`) and checks a row on a different worker survives, while brine sweeps the worker that DOES have rows and checks the neighbour's survives. Both pin the same predicate — `sq.Eq{"worker_name": workerName}` in RemoveDestroying

**[DELETED]** Destroyer DestroyContainers removes every destroying container when the worker reports an empty list
  - mutation: M_A — atc/db/container_repository.go:202-204 (RemoveDestroyingContainers): `sq.NotEq{"handle": handlesToIgnore}` -> `sq.Eq{"handle": handlesToIgnore}`
  - brine: gc-reclamation.feature, Scenario "A worker that reports holding nothing loses its own destroying rows and nobody else's" — `FAIL (8/11 steps)` / `Step FAILED: And the container "gone-container" has been reclaimed`
  - skeptic: Not independently re-run, but everything checkable holds. The It text resolves to exactly 1 of the 109 specs (verified by ginkgo dry-run enumeration: `Ran 109 of 109 Specs`, 1 exact / 1 substring match), so the claimed `Ran 1 of 109` is not a zero-match artifact. The It body has one behavioural assertion — `Expect(containerHandles()).NotTo(ContainElement(gone.Handle()))` — and brine's paired step 

**[DELETED]** Destroyer DestroyContainers removes the destroying containers the worker no longer reports
  - mutation: M_A — atc/db/container_repository.go:202-204 (RemoveDestroyingContainers): `sq.NotEq{"handle": handlesToIgnore}` -> `sq.Eq{"handle": handlesToIgnore}` (the keep-list becomes a kill-list)
  - brine: gc-reclamation.feature, Scenario "The destroying rows a worker no longer reports are reclaimed, and the ones it reports are kept" — `FAIL (8/11 steps)` / `Step FAILED: And the container "gone-container" has been reclaimed` (run: 9 passed, 2 failed, 11 scenario lines)
  - skeptic: I re-ran this one specifically because the claim pairs mismatched halves: the Go test failed at destroyer_test.go:120 (the KEPT container), while the brine scenario failed at step 8 on the GONE container. That is the exact 'reddens for a different reason' shape, so I built a narrower mutation to isolate the kept half — atc/db/container_repository.go RemoveDestroyingContainers, `sq.NotEq{"handle": 

**[DELETED]** Destroyer DestroyVolumes does nothing when the handle list is nil
  - mutation: M_C2 — atc/gc/destroyer.go:68-70: the `if currentHandles == nil { return nil }` guard deleted from DestroyVolumes ONLY (container guard left intact, so attribution is unambiguous)
  - brine: gc-reclamation.feature, Scenario "A report that never arrived is not a report of nothing" — `FAIL (7/7 steps)` / `Step FAILED: And the volume "survivor-volume" survived the sweep`
  - skeptic: Not re-run, but this is the one claim in the batch that already carried its own control — the claimant mutated the volume nil guard ONLY and reported that the container nil-guard It stayed `SUCCESS! -- 1 Passed | 0 Failed` under it. I independently ran the mirror-image experiment (container guard only) and got the complementary result: `FAIL! -- 1 Passed | 1 Failed` with the volume It green, and e

**[DELETED]** Destroyer DestroyVolumes removes every destroying volume when the worker reports an empty list
  - mutation: M_B — atc/db/volume_repository.go:197-199 (RemoveDestroyingVolumes): `sq.NotEq{"handle": handles}` -> `sq.Eq{"handle": handles}`
  - brine: gc-reclamation.feature, Scenario "A worker that reports holding nothing loses its own destroying rows and nobody else's" — `FAIL (9/11 steps)` / `Step FAILED: And the volume "gone-volume" has been reclaimed`
  - skeptic: Not independently re-run. It text resolves to exactly 1 of 109 specs in the dry-run enumeration, so the claimed `Ran 1 of 109` is real. The It's single behavioural assertion is `Expect(remaining).NotTo(ContainElement(gone.Handle()))` against `GetDestroyingVolumes`; brine's step 9 `And the volume "gone-volume" has been reclaimed` is a CheckNotMember over `SELECT handle FROM volumes`, non-vacuous be

**[DELETED]** Destroyer DestroyVolumes removes the destroying volumes the worker no longer reports
  - mutation: M_B — atc/db/volume_repository.go:197-199 (RemoveDestroyingVolumes): `sq.NotEq{"handle": handles}` -> `sq.Eq{"handle": handles}`
  - brine: gc-reclamation.feature, Scenario "The destroying rows a worker no longer reports are reclaimed, and the ones it reports are kept" — `FAIL (9/11 steps)` / `Step FAILED: And the volume "gone-volume" has been reclaimed`
  - skeptic: Re-run for the same mismatched-halves reason as the container twin: Go failed at destroyer_test.go:188 (the KEPT volume), brine at step 9 on the GONE volume. Isolating mutation: atc/db/volume_repository.go RemoveDestroyingVolumes, `sq.NotEq{"handle": handles}` -> `sq.Expr("1=1")`. Go: `Ran 1 of 109 Specs in 1.076 seconds` / `FAIL! -- 0 Passed | 1 Failed | 0 Pending | 108 Skipped`. brine: `FAIL The

**[DELETED]** Destroyer FindDestroyingVolumesForGc returns the handles of the destroying volumes
  - mutation: M_G — atc/gc/destroyer.go:100: `return destroyingVolumesHandles, nil` -> `return nil, nil`
  - brine: gc-reclamation.feature, Scenario "Only this worker's volumes, and only the ones being destroyed, are offered for reclamation" — `FAIL (7/11 steps)` / `Step FAILED: Then 2 volumes are waiting to be reclaimed`
  - skeptic: Not re-run. The It asserts `ConsistOf(first.Handle(), second.Handle())` — an exact two-element set. brine's scenario asserts strictly more over the same answer: `Then 2 volumes are waiting to be reclaimed` (CheckCount), `first-to-go is waiting`, `second-to-go is waiting` (CheckMember), plus two negatives (`still-in-use` and `neighbours-volume` not waiting) that the ginkgo It does not have at all. 

**[REFUTED]** Destroyer DestroyContainers when the container repository fails returns the error and destroys nothing
  - mutation: M_F_c — atc/gc/destroyer.go:53: `return err` -> `return nil` after RemoveDestroyingContainers fails
  - skeptic: REFUTED — old red, brine green under an honest mutation, and here the gap is substantive rather than cosmetic. The It deliberately injects a sentinel through `failRemoveDestroyingContainers` returning `errors.New("I am le tired")` and asserts `MatchError("I am le tired")` — exact equality, which is precisely an assertion that the destroyer passes the repository's error through UNWRAPPED and unsubs

**[REFUTED]** Destroyer DestroyContainers when the worker name is not provided returns an error and destroys nothing
  - mutation: M_E_c — atc/gc/destroyer.go:39-44: the `if workerName == "" { ... return err }` guard deleted from DestroyContainers only
  - skeptic: REFUTED — old red, brine green under an honest mutation. The Go It asserts `Expect(err).To(MatchError("worker-name-must-be-provided"))`, and Gomega's MatchError against a string is EXACT equality of `err.Error()`. brine's paired step is `CheckContains` — a substring test (steps/assert.go: `containsCheck`, 'sentences that mean the value MENTIONS something rather than equals it'). So the It pins the

**[REFUTED]** Destroyer DestroyVolumes when the volume repository fails returns the error and destroys nothing
  - mutation: M_F_v — atc/gc/destroyer.go:75: `return err` -> `return nil` after RemoveDestroyingVolumes fails
  - skeptic: REFUTED — same substantive gap as the container twin. The It injects `failRemoveDestroyingVolumes` returning `errors.New("I am le tired")` and asserts `MatchError("I am le tired")` exactly, which is an assertion that the destroyer does not wrap the repository's error; brine asserts only that the refusal contains "closed". Mutation: `return err` -> `return fmt.Errorf("destroy volumes on worker %s: 

**[REFUTED]** Destroyer DestroyVolumes when the worker name is not provided returns an error and destroys nothing
  - mutation: M_E_v — atc/gc/destroyer.go:61-66: the `if workerName == "" { ... return err }` guard deleted from DestroyVolumes only
  - skeptic: REFUTED — same gap as the DestroyContainers twin. `MatchError("worker-name-must-be-provided")` is exact equality; brine's `And reclaiming the volumes was refused, saying "worker-name-must-be-provided"` is a substring check. Mutation: `err := fmt.Errorf("cannot sweep volumes: %w", errors.New("worker-name-must-be-provided"))` at destroyer.go:61-66. Go: `Ran 2 of 109 Specs in 0.975 seconds` / `FAIL! 

**[REFUTED]** Destroyer FindDestroyingVolumesForGc when the volume repository fails returns the error
  - mutation: M_H — atc/gc/destroyer.go:87: `return nil, err` -> `return nil, nil` after GetDestroyingVolumes fails (a failed read becomes "nothing to reclaim")
  - skeptic: REFUTED — old red, brine green. The It uses `failGetDestroyingVolumes` returning `errors.New("some-bad-err")` and asserts `MatchError("some-bad-err")` exactly; brine asserts the refusal contains "closed". Mutation: `return nil, err` -> `return nil, fmt.Errorf("get destroying volumes on worker %s: %w", workerName, err)` at destroyer.go:87. Go mutated: `Ran 3 of 109 Specs in 1.458 seconds` / `FAIL! 

**[INERT]** Destroyer FindDestroyingVolumesForGc returns nothing when the worker has no destroying volumes
  - mutation: Two honest mutations, both leaving it green. M_G — atc/gc/destroyer.go:100: `return destroyingVolumesHandles, nil` -> `return nil, nil`. M_G2 — atc/db/volume_repository.go:632-635 (GetDestroyingVolumes): the `"state": string(VolumeStateDestroying)` predicate dropped, so every volume on the worker is offered to the reaper.


## pipeline_collector_test.go

**[DELETED]** PipelineCollector Run archives a child pipeline once its parent is archived
  - mutation: M_T — atc/gc/pipeline_collector.go:26: `err := pc.pipelineLifecycle.ArchiveAbandonedPipelines()` replaced with `var err error` (the lifecycle call is never made)
  - brine: gc-pipelines.feature, Scenario "A child pipeline is archived when the parent that set it is archived, and only then" — `FAIL (10/13 steps)` / `Step FAILED: And the pipeline "orphaned-child" has been archived` (24 passed, 1 failed of 25)
  - skeptic: Not re-run. Same wiring as above. The It asserts exactly one thing beyond `Run()` succeeding — that the child of an archived parent is archived — and brine's `And the pipeline "orphaned-child" has been archived` is a CheckMember over `WHERE archived = true`, the same fact. Under the claimed mutation (the ArchiveAbandonedPipelines call never made) the scenario's other three pipelines stay active an

**[DELETED]** PipelineCollector Run leaves a child pipeline whose parent is still healthy
  - mutation: M_S — atc/db/pipeline_lifecycle.go:44 (ArchiveAbandonedPipelines): `sq.Expr("1=1")` added as the first arm of the `sq.Or{...}`, so every child pipeline is archived regardless of its parent's health
  - brine: gc-pipelines.feature, Scenario "A child pipeline is archived when the parent that set it is archived, and only then" — `FAIL (11/13 steps)` / `Step FAILED: And the pipeline "healthy-child" is still active` (24 passed, 1 failed of 25)
  - skeptic: Not re-run. steps/gc_pipelines.go builds `gc.NewPipelineCollector(db.NewPipelineLifecycle(database.Conn, database.LockFactory))` — the same constructor and the same arguments as the ginkgo BeforeEach — and the checks are CheckMember over `SELECT name FROM pipelines WHERE archived = $1`, i.e. positive membership in a state-filtered set, which cannot pass vacuously. The It asserts one pipeline is no


## resource_cache_collector_test.go

**[DELETED]** ResourceCacheCollector Run resource caches when the cache is no longer in use when the cache is an image resource version for a job build [It] leaves it alone
  - mutation: M18 — atc/db/build.go:954-958, SaveImageResourceVersion no longer performs the `INSERT INTO build_image_resource_caches`, so the image record that is the only thing protecting this cache is never written. NOTE: no mutation of the UNION arm this It nominally pins (resource_cache_lifecycle.go:95-101) can redden it — build_image_resource_caches.resource_cache_id is ON DELETE RESTRICT, so removing the
  - brine: RED, 13 passed / 6 failed. Scenario "A job build's image cache outlives the build that recorded it" — failing step: `And the cache "first-image" survived the sweep` -> "expected the resource cache rows still in the database to include \"first-image\", found []". NOTE, separate finding: mutation M8 (
  - skeptic: Not refuted, but with the caveat named. I built M18 (SaveImageResourceVersion performs no INSERT) and ran it: Go `FAIL! -- 10 Passed | 6 Failed`, this It red at resource_cache_collector_test.go:173; brine 13/6 with "A job build's image cache outlives the build that recorded it" red on `And the cache "first-image" survived the sweep` -> "expected the resource cache rows still in the database to inc

**[DELETED]** ResourceCacheCollector Run resource caches when the cache is no longer in use when the cache is an image resource version for a job build when another build of a different job exists with a different image cache when the second build succeeds [It] keeps the new cache and the old one
  - mutation: M5 — atc/db/build.go:654-656, dropped `sq.Eq{"job_id": b.jobID}` from Finish's build_image_resource_caches delete, so any job's successful build discards every other job's image record.
  - brine: RED, 18 passed / 1 failed. Scenario "A job build's image cache is released only when a later build of the same job succeeds — <case> (row 3)" — failing step: `And the cache "first-image" survived the sweep` -> "expected the resource cache rows still in the database to include \"first-image\", found 
  - skeptic: Rebuilt M5 (dropped `sq.Eq{"job_id": b.jobID}`). Go: `Ran 16 of 109 Specs in 2.930 seconds` / `FAIL! -- 15 Passed | 1 Failed`, this It alone at resource_cache_collector_test.go:251. brine: 18/1, image row 3 on `And the cache "first-image" survived the sweep` -> "expected the resource cache rows still in the database to include \"first-image\", found [second-image]". The It's second assertion (`sec

**[DELETED]** ResourceCacheCollector Run resource caches when the cache is no longer in use when the cache is an image resource version for a job build when another build of the same job exists with a different image cache when the second build fails [It] keeps the new cache and the old one
  - mutation: M6 — atc/db/build.go:651, dropped the success guard on the build_image_resource_caches delete: it now runs under `if b.jobID != 0 {` instead of `if b.jobID != 0 && status == BuildStatusSucceeded {`, so a FAILING build discards the image of its own last good build. (The rest of the success-only block is left guarded, so only that one predicate moves.)
  - brine: RED, 18 passed / 1 failed. Scenario "A job build's image cache is released only when a later build of the same job succeeds — <case> (row 2)" — failing step: `And the cache "first-image" survived the sweep` -> "expected the resource cache rows still in the database to include \"first-image\", found 
  - skeptic: Rebuilt M6 (delete moved out of the `status == BuildStatusSucceeded` guard, the rest of the block left guarded). Go: `Ran 16 of 109 Specs in 3.097 seconds` / `FAIL! -- 15 Passed | 1 Failed`, this It alone at resource_cache_collector_test.go:216. brine: 18/1, image row 2 on `And the cache "first-image" survived the sweep` -> "expected the resource cache rows still in the database to include \"first

**[DELETED]** ResourceCacheCollector Run resource caches when the cache is no longer in use when the cache is an image resource version for a job build when another build of the same job exists with a different image cache when the second build succeeds [It] keeps the new cache and removes the old one
  - mutation: M7 — atc/db/build.go:657-659, widened `sq.Lt{"build_id": b.id}` to `sq.LtOrEq{...}` in Finish's build_image_resource_caches delete, so a successful build deletes the record of its OWN image as well as its predecessor's.
  - brine: RED, 17 passed / 2 failed. Scenario "A job build's image cache is released only when a later build of the same job succeeds — <case> (row 1)" — failing step: `And the cache "second-image" survived the sweep` -> "expected the resource cache rows still in the database to include \"second-image\", foun
  - skeptic: Rebuilt M7 (`sq.Lt` -> `sq.LtOrEq`). Go: `Ran 16 of 109 Specs in 2.979 seconds` / `FAIL! -- 14 Passed | 2 Failed`, this It red at resource_cache_collector_test.go:206 (the `secondJobCache` survives assertion). brine: 17/2, image row 1 on `And the cache "second-image" survived the sweep` -> "expected the resource cache rows still in the database to include \"second-image\", found []". Same assertio

**[DELETED]** ResourceCacheCollector Run resource caches when the cache is no longer in use when the cache is an image resource version for a one-off build when the build finished a day ago [It] removes the cache
  - mutation: M9 — atc/db/resource_cache_lifecycle.go:34, widened CleanBuildImageResourceCaches' cutoff from `'24 HOURS'::INTERVAL` to `'48 HOURS'::INTERVAL`, so the 25-hour-old one-off's image record is still held.
  - brine: RED, 18 passed / 1 failed. Scenario "A one-off build's image cache is released a day after the build ended — <case> (row 2)" — failing step: `And the cache "one-off-image" has been reclaimed` -> "expected the resource cache rows still in the database not to include \"one-off-image\", but it does: [j
  - skeptic: Rebuilt M9 ('24 HOURS' -> '48 HOURS'). Go: `Ran 16 of 109 Specs in 2.823 seconds` / `FAIL! -- 15 Passed | 1 Failed`, this It alone at resource_cache_collector_test.go:292. brine: 18/1, "A one-off build's image cache is released a day after the build ended — <case> (row 2)" on `And the cache "one-off-image" has been reclaimed` -> "expected the resource cache rows still in the database not to includ

**[DELETED]** ResourceCacheCollector Run resource caches when the cache is no longer in use when the cache is an image resource version for a one-off build when the build finished recently [It] leaves it alone
  - mutation: M10 — atc/db/resource_cache_lifecycle.go:34, narrowed CleanBuildImageResourceCaches' cutoff from `'24 HOURS'::INTERVAL` to `'0 SECONDS'::INTERVAL`, so a one-off's image record is released the moment the build ends.
  - brine: RED, 18 passed / 1 failed. Scenario "A one-off build's image cache is released a day after the build ended — <case> (row 1)" — failing step: `And the cache "one-off-image" survived the sweep` -> "expected the resource cache rows still in the database to include \"one-off-image\", found [job-image]"
  - skeptic: Rebuilt M10 ('24 HOURS' -> '0 SECONDS'). Go: `Ran 16 of 109 Specs in 3.080 seconds` / `FAIL! -- 15 Passed | 1 Failed`, this It alone at resource_cache_collector_test.go:277. brine: 18/1, "A one-off build's image cache is released a day after the build ended — <case> (row 1)" on `And the cache "one-off-image" survived the sweep` -> "expected the resource cache rows still in the database to include 

**[DELETED]** ResourceCacheCollector Run resource caches when the cache is no longer in use when the cache is an input to a job when pipeline is not paused [It] leaves it alone
  - mutation: M3 — atc/db/resource_cache_lifecycle.go:125, replaced `Where(sq.Expr("p.paused = false"))` with `Where("1 = 0")` in the version_sha256 next-build-inputs subquery, so that arm of the UNION protects nothing.
  - brine: RED, 18 passed / 1 failed. Scenario "A cache the scheduler still needs as an input is kept unless the pipeline is paused — <case> (row 1)" — failing step: `And the cache "input-cache" survived the sweep` -> "expected the resource cache rows still in the database to include \"input-cache\", found [in
  - skeptic: Rebuilt M3. Go: `Ran 16 of 109 Specs in 3.350 seconds` / `FAIL! -- 15 Passed | 1 Failed`, this It alone at resource_cache_collector_test.go:161. brine: 18/1, "A cache the scheduler still needs as an input is kept unless the pipeline is paused — <case> (row 1)" on `And the cache "input-cache" survived the sweep` -> "expected the resource cache rows still in the database to include \"input-cache\", 

**[DELETED]** ResourceCacheCollector Run resource caches when the cache is no longer in use when the cache is an input to a job when pipeline is paused [It] removes the cache
  - mutation: M2 — atc/db/resource_cache_lifecycle.go:125, deleted `Where(sq.Expr("p.paused = false"))` from the version_sha256 next-build-inputs subquery, so a paused pipeline's scheduler claim still protects the cache.
  - brine: RED, 18 passed / 1 failed. Scenario "A cache the scheduler still needs as an input is kept unless the pipeline is paused — <case> (row 2)" — failing step: `And the cache "input-cache" has been reclaimed` -> "expected the resource cache rows still in the database not to include \"input-cache\", but i
  - skeptic: Rebuilt M2 and ran both sides. Go: `Ran 16 of 109 Specs in 2.831 seconds` / `FAIL! -- 15 Passed | 1 Failed`, the single failure being this It at resource_cache_collector_test.go:155. brine: 18 passed / 1 failed, "A cache the scheduler still needs as an input is kept unless the pipeline is paused — <case> (row 2)" on `And the cache "input-cache" has been reclaimed` -> "expected the resource cache r

**[DELETED]** ResourceCacheCollector Run resource caches when the resource cache is still in use [It] does not delete the cache
  - mutation: M12 — atc/db/resource_cache_lifecycle.go:45, CleanUsesForFinishedBuilds: deleted the `sq.Expr("b.interceptible = false")` predicate, so the delete takes the uses of every build that has a build row at all, including running ones. The cache then has no use protecting it and CleanUpInvalidCaches collects it.
  - brine: RED. Scenario "A cache a running build still holds is kept, and one no build holds is collected" — failing step: `And the cache "in-use" survived the sweep` -> "expected the resource cache rows still in the database to include \"in-use\", found []". (M12 reddened 8 of the 19 scenarios; this is the o
  - skeptic: Reproduced M12 myself (dropped `sq.Expr("b.interceptible = false")`): Go `Ran 16 of 109 Specs` / `FAIL! -- 13 Passed | 3 Failed`, this It red at resource_cache_collector_test.go:114; brine 11/8 with "A cache a running build still holds is kept, and one no build holds is collected" red on `And the cache "in-use" survived the sweep` -> "expected the resource cache rows still in the database to inclu

**[REFUTED]** ResourceCacheCollector Run resource caches when the cache is no longer in use when the cache is an image resource version for a job build when another build of a different job exists with a different image cache when the second build fails [It] keeps the new cache and the old one
  - mutation: M18 — atc/db/build.go:954-958, SaveImageResourceVersion writes no build_image_resource_caches row. ALSO measured M19 — build.go:651 and :654-656 TOGETHER (success guard AND job_id both dropped), which is the only mutation that reaches this It's own discriminating conjunction. Neither half alone reddens it: M5 (job_id dropped) leaves it green because a FAILED build never reaches the delete; M6 (suc
  - skeptic: REFUTED two ways, both measured. (a) M18 pairs the wrong sentences. I ran it: this It fails at resource_cache_collector_test.go:262 on `Expect(resourceCacheExists(jobCache)).To(BeTrue())` — i.e. because the image record was never WRITTEN, which is claim 4's sentence, not this It's conjunction. brine's six M18 reds are "outlives" and image rows 1/2/3 plus the one-off rows; none of them states "a DI


## resource_cache_use_collector_test.go

**[DELETED]** ResourceCacheUseCollector Run cache uses for one-off builds before the build has completed [It] does not clean up the uses
  - mutation: M12 — atc/db/resource_cache_lifecycle.go:45, CleanUsesForFinishedBuilds: deleted the `sq.Expr("b.interceptible = false")` predicate, so a still-running build's uses are released.
  - brine: RED, 11 passed / 8 failed. Scenario "A build's cache uses are released only when the build can no longer be intercepted — <case> (row 1)" [the `still running` row] — failing step: `And the cache uses of the build "under-test" are still held` -> "expected the builds whose cache uses survive to includ
  - skeptic: Not refuted, and the evidence is better than the claim's. M12 reproduced: Go `FAIL! -- 13 Passed | 3 Failed`, this It red at resource_cache_use_collector_test.go:79; brine use row 1 (`still running`) red on `And the cache uses of the build "under-test" are still held` -> "expected the builds whose cache uses survive to include \"under-test\", found []". M12 is broad (8 of 19 brine scenarios), so I

**[DELETED]** ResourceCacheUseCollector Run cache uses for one-off builds once the build has been aborted [It] cleans up the uses
  - mutation: M11 — atc/db/resource_cache_lifecycle.go:41-50, CleanUsesForFinishedBuilds replaced with `return nil`; the delete never runs.
  - brine: RED, 10 passed / 9 failed. Scenario "A build's cache uses are released only when the build can no longer be intercepted — <case> (row 4)" [the `aborted` row] — failing step: `And the cache uses of the build "under-test" have been released` -> "expected the builds whose cache uses survive not to incl
  - skeptic: Same objection to M11 as above, and same remedy. NM1 (`b.interceptible = false` -> `b.status = 'succeeded'`, so only succeeded builds are released): Go `Ran 16 of 109 Specs in 2.765 seconds` / `FAIL! -- 12 Passed | 4 Failed`, this It red. brine: 16 passed / 3 failed, use row 4 (the `aborted` row, a one-off) red on `And the cache uses of the build "under-test" have been released` -> "expected the b

**[DELETED]** ResourceCacheUseCollector Run cache uses for one-off builds once the build has completed successfully [It] cleans up the uses
  - mutation: M11 — atc/db/resource_cache_lifecycle.go:41-50, CleanUsesForFinishedBuilds replaced with `return nil`; the delete never runs.
  - brine: RED, 10 passed / 9 failed. Scenario "A build's cache uses are released only when the build can no longer be intercepted — <case> (row 2)" [the `succeeded` row] — failing step: `And the cache uses of the build "under-test" have been released` -> "expected the builds whose cache uses survive not to in
  - skeptic: The claim's M11 (whole function replaced by `return nil`) is exactly the too-broad mutation that pairs nothing with anything — it reddens 9 of 19 brine scenarios and every release-expecting It at once. So I replaced it with a narrow one, NM2: `b.interceptible = false` -> `b.status IN ('failed','aborted')`, i.e. succeeded builds' uses are never released. Go: `Ran 16 of 109 Specs in 2.636 seconds` /

**[DELETED]** ResourceCacheUseCollector Run cache uses for one-off builds once the build has failed when the build is a one-off [It] cleans up the uses
  - mutation: M11 — atc/db/resource_cache_lifecycle.go:41-50, CleanUsesForFinishedBuilds replaced with `return nil`; the delete never runs.
  - brine: RED, 10 passed / 9 failed. Scenario "A build's cache uses are released only when the build can no longer be intercepted — <case> (row 3)" [the `failed` row] — failing step: `And the cache uses of the build "under-test" have been released` -> "expected the builds whose cache uses survive not to inclu
  - skeptic: Same NM1 run (`b.interceptible = false` -> `b.status = 'succeeded'`). Go: `FAIL! -- 12 Passed | 4 Failed`, this It red. brine: use row 3 (the `failed` row, a one-off) red on `And the cache uses of the build "under-test" have been released` -> "expected the builds whose cache uses survive not to include \"under-test\", but it does: [under-test bystander]". Narrow, subject-matched, and it distinguis

**[DELETED]** ResourceCacheUseCollector Run cache uses when the build is for a job when it is the latest failed build [It] cleans up the uses since Finish marks failed builds non-interceptible
  - mutation: M13 — atc/db/build.go:632-634, deleted the `if status != BuildStatusSucceeded { updateBuilder = updateBuilder.Set("interceptible", false) }` branch from Build.Finish, so a failed build's flag is left for the build collector, which deliberately spares a job's latest completed build.
  - brine: RED, 18 passed / 1 failed — a clean 1:1 isolation. Scenario "A build's cache uses are released only when the build can no longer be intercepted — <case> (row 5)" [the `latest failed build of a job` row] — failing step: `And the cache uses of the build "under-test" have been released` -> "expected th
  - skeptic: The claim's M13 is already a clean 1:1 (one It, one scenario), and I found a second independent narrow pairing. Under my NM1 run: Go `FAIL! -- 12 Passed | 4 Failed` with this It red; brine 16/3 with use row 5 (`the latest failed build of a job`) red on `And the cache uses of the build "under-test" have been released` -> "expected the builds whose cache uses survive not to include \"under-test\", b

**[REFUTED]** ResourceCacheUseCollector Run cache uses when the build is for a job when a later build of the same job has succeeded [It] cleans up the uses
  - mutation: M11 — atc/db/resource_cache_lifecycle.go:41-50, CleanUsesForFinishedBuilds replaced with `return nil`. Also reddened by M12 (line 45, `b.interceptible = false` dropped), which breaks its intermediate `Expect(countResourceCacheUses()).NotTo(BeZero())` because the second build's uses go too.
  - skeptic: REFUTED by direct measurement — old RED, brine GREEN. Mutation CP1, a single honest predicate change in CleanUsesForFinishedBuilds (atc/db/resource_cache_lifecycle.go:45): `sq.Expr("b.interceptible = false")` -> `sq.Expr("(b.interceptible = false OR b.job_id IS NOT NULL)")`, i.e. a job build's uses are released regardless of interceptibility. Go: `Ran 16 of 109 Specs in 3.098 seconds` / `FAIL! -- 


## resource_config_check_session_collector_test.go

**[GAP]** ResourceConfigCheckSessionCollector Run when the resource config changes [It] removes the resource config check session
  - mutation: m8: atc/db/resource_config_check_session_lifecycle.go:59-68 — the DELETE in CleanInactiveResourceConfigCheckSessions removed entirely, replaced with `return nil`, so sessions orphaned by a config change are never reclaimed.

**[GAP]** ResourceConfigCheckSessionCollector Run when the resource config check session is expired [It] removes the resource config check session
  - mutation: m7: atc/db/resource_config_check_session_lifecycle.go:71-78 — the body of CleanExpiredResourceConfigCheckSessions replaced with `return nil`, so expired sessions are never deleted.

**[GAP]** ResourceConfigCheckSessionCollector Run when the resource is active [It] keeps the resource config check session
  - mutation: m8b: atc/db/resource_config_check_session_lifecycle.go:60-63 — the `id NOT IN (usedByActiveUnpausedResources UNION ...)` predicate dropped from CleanInactiveResourceConfigCheckSessions, so the sweep deletes every check session including the live one.

**[GAP]** ResourceConfigCheckSessionCollector Run when the resource is removed [It] removes the resource config check session
  - mutation: m8: atc/db/resource_config_check_session_lifecycle.go:59-68 — the DELETE in CleanInactiveResourceConfigCheckSessions removed entirely, replaced with `return nil`.


## resource_config_collector_test.go

**[DELETED]** ResourceConfigCollector Run configs when config is not referenced in resource types [It] spares the config until the grace period elapses
  - mutation: m4: atc/db/resource_config_factory.go:271 — the `now() - last_referenced > '%d seconds'::interval` grace-period predicate deleted from CleanUnreferencedConfigs. (Also reddens under m5.)
  - brine: RED under the SAME mutation. Scenario: "A config nothing references is spared until the grace period elapses". Failing step: `And the config "recently-referenced" survived the sweep`. Error: "expected the resource config rows still in the database to include \"recently-referenced\", found []". NOTE:
  - skeptic: Same measurement, same conclusion. Focus sanity unmutated: `Ran 1 of 109 Specs` / `SUCCESS! -- 1 Passed | 0 Failed | 0 Pending | 108 Skipped`. Under m4 it fails at `[FAILED] Expected ... not to be zero-valued  In [It] at: .../resource_config_collector_test.go:236` -- the first-sweep spare assertion -- alongside the :174 duplicate, and the SAME m4 adapter reddens brine's "A config nothing reference

**[DELETED]** ResourceConfigCollector Run configs when config is not referenced in resources [It] spares the config until the grace period elapses
  - mutation: m4: atc/db/resource_config_factory.go:271 — the `Where(sq.Expr(fmt.Sprintf("now() - last_referenced > '%d seconds'::interval", ...)))` grace-period predicate deleted from CleanUnreferencedConfigs. (Also reddens under m5.)
  - brine: RED under the SAME mutation. Scenario: "A config nothing references is spared until the grace period elapses". Failing step: `And the config "recently-referenced" survived the sweep`. Error: "expected the resource config rows still in the database to include \"recently-referenced\", found []"
  - skeptic: I attacked this and could not break it. Focus sanity: the exact focus string matched `Ran 1 of 109 Specs` / `SUCCESS! -- 1 Passed | 0 Failed | 0 Pending | 108 Skipped` unmutated, so neither the green nor the red is vacuous. Under m4 (grace predicate deleted from CleanUnreferencedConfigs) the group run gave `Ran 7 of 109 Specs in 2.478 seconds` / `FAIL! -- 5 Passed | 2 Failed | 0 Pending | 102 Skip

**[DELETED (brine stronger)]** ResourceConfigCollector Run configs when config is referenced in resource caches [It] preserve the config
  - mutation: m3: atc/db/resource_config_factory.go:267 — `usedByResourceCachesIds + " UNION " +` removed from the `id NOT IN (...)` clause, i.e. the exact UNION arm this It is named after. Also green under m1, m2, m4, m5, m6.
  - brine: RED. Scenario: "A config is collected only when nothing references it any more". Failing step: `And the config "orphan" has been reclaimed`. Error: "expected the resource config rows still in the database not to include \"orphan\", but it does: [held-by-a-cache held-by-a-resource held-by-a-resource-

**[DELETED (brine stronger)]** ResourceConfigCollector Run configs when config is referenced in resource types [It] preserve the config
  - mutation: m2: atc/db/resource_config_factory.go:269 — `usedByResourceTypesIds + " UNION " +` removed from the `id NOT IN (...)` clause, i.e. the exact UNION arm this It is named after. Also green under m1, m3, m4, m5, m6.
  - brine: RED. Scenario: "A config is collected only when nothing references it any more". Failing step: `And the config "orphan" has been reclaimed`. Error: "expected the resource config rows still in the database not to include \"orphan\", but it does: [held-by-a-cache held-by-a-resource held-by-a-resource-

**[DELETED (brine stronger)]** ResourceConfigCollector Run configs when config is referenced in resources [It] preserve the config
  - mutation: m1: atc/db/resource_config_factory.go:268 — `usedByResourcesIds + " UNION " +` removed from the `id NOT IN (...)` clause, i.e. the exact UNION arm this It is named after. Also green under m2, m3, m4, m5, m6. (Note: the gc-caches.feature prose predicts this mutation "takes that survivor and nothing else — the scope row cascades"; the measured failure is instead the ORPHAN line, with held-by-a-resou
  - brine: RED. Scenario: "A config is collected only when nothing references it any more". Failing step: `And the config "orphan" has been reclaimed`. Error: "expected the resource config rows still in the database not to include \"orphan\", but it does: [held-by-a-cache held-by-a-resource held-by-a-resource-

**[DELETED (brine stronger)]** ResourceConfigCollector Run configs when the config is referenced in resource config check sessions [It] preserves the config
  - mutation: m6: atc/db/resource_config_factory.go:278 — the ForeignKeyViolation branch of CleanUnreferencedConfigs returns `err` instead of `nil`. Also measured green under m1/m2/m3 (each UNION arm dropped, resource_config_factory.go:267-269), m4 (grace predicate deleted, :271) and m5 (`return nil` before the DELETE, :265) — six honest mutations, none of which this It can see.
  - brine: RED. Scenario: "A config a check session pins blocks the whole sweep, which still reports success". Failing step: `Then garbage collection completed without error`. Error: "a collector in the chain failed: ERROR: update or delete on table \"resource_configs\" violates foreign key constraint \"resour

**[REFUTED]** ResourceConfigCollector Run configs when config is not referenced in resource caches [It] cleans up the config
  - mutation: m5: atc/db/resource_config_factory.go:265 — `return nil` inserted immediately before the `psql.Delete("resource_configs")` statement, so CleanUnreferencedConfigs collects nothing and reports success.
  - skeptic: The It asserts MORE than the brine scenario, and I measured the gap. It pins two production facts: the DELETE in CleanUnreferencedConfigs (which m5 hits) AND the fact that the resource-cache path calls findOrCreateResourceConfig with updateLastReferenced=FALSE at atc/db/resource_cache_factory.go:63, so a cache-derived config keeps the column default '1970-01-01' and is collectable with NO aging. T


## scanner_test.go

**[DELETED]** Scanner > checks all persisted resources beyond the worker concurrency limit
  - mutation: m9_fanout_one — atc/lidar/scanner.go:308, add `return` after `s.check(ctx, rs, resourceTypes)` in the scanResources worker loop, so each worker handles exactly one resource and the fan-out silently drops everything past maxConcurrency.
  - brine: RED. Scenario "Every resource is checked even when there are four times as many as there are workers" failed at step `And 20 checks were enqueued` -> "expected 20 checks, found 5: [resource-00 resource-01 resource-02 resource-03 resource-04]". A second scenario also reddened: "A crash scanning one r
  - skeptic: Stands, with one noted caveat. I read the brine step rather than trusting its name: `everyResourceCheckedOnce` (steps/resource_checking.go:1177) builds the declared resource names, sorts them, and compares the joined string against the checked set — a genuine set comparison, so it catches twenty checks of one resource, which a bare count would not. That is at least as strong as the Go It's `seenID

**[DELETED]** Scanner > creates a real check plan with its persisted custom parent type
  - mutation: m5_nil_types — atc/lidar/scanner.go:289, `resourceTypes := resourceTypesMap[rs.PipelineID()]` becomes `var resourceTypes db.ResourceTypes`, so the resource is planned with no knowledge of its own pipeline's custom types and the nested parent-type check and fetch disappear.
  - brine: RED. Scenario "A resource on a custom type gets a plan that checks and fetches the type first" failed at step `And the check plan for "custom-resource" pulls its image from the base type "global-base-type"` -> "expected the base type in the check plan for \"custom-resource\" to be \"global-base-type
  - skeptic: Stands. Every clause has a counterpart: `TypeImage.CheckPlan.Check.Name/Source/Tags` map to the three `parent type check in the plan for "custom-resource"` steps, `TypeImage.GetPlan.Get.Name/Type/Source` to the three `parent type fetch` steps, and `TypeImage.BaseType` to `pulls its image from the base type "global-base-type"`. The one clause with no literal counterpart, `Expect(call.resourceTypes[

**[DELETED]** Scanner > excludes a steady-state put-only resource after a successful scoped check
  - mutation: m8_resources_predicate — atc/db/check_factory.go:174-187, delete the whole `Where(sq.Or{ ... ji.resource_id != nil ... })` predicate from checkFactory.Resources(), so put-only outputs whose last check succeeded are enumerated and checked again every tick.
  - brine: RED. Scenario "An output nobody reads is not checked again once its check succeeded" failed at step `And no check was enqueued for "put-only-resource"` -> "expected the resources a check was enqueued for not to include \"put-only-resource\", but it does: [input-resource put-only-resource]"
  - skeptic: Stands — and I re-measured it end to end rather than taking the claim on trust. Deleting the whole `Where(sq.Or{...ji.resource_id...})` predicate from checkFactory.Resources (atc/db/check_factory.go:173-186): Go "Ran 35 of 35 Specs" / "FAIL! -- 34 Passed | 1 Failed", the single failure being "[FAIL] Scanner [It] excludes a steady-state put-only resource after a successful scoped check". brine, sam

**[DELETED]** Scanner > forwards a nil pin from an unpinned persisted resource
  - mutation: m7c_unpinned_gets_version — atc/lidar/scanner.go:449, the pin lookup is kept but a version is invented when there is none: `version := checkable.CurrentPinnedVersion(); if version == nil { version = atc.Version{"ref": "invented"} }`. This breaks ONLY the unpinned half, leaving the pinned half correct, which is what isolates this It from its sibling.
  - brine: RED, on the unpinned clause specifically. Scenario "A pinned resource is checked from its pin and an unpinned one from nothing" failed at step `And the check plan for "unpinned-resource" starts from the version "nothing"` -> "expected the version the check plan starts from for \"unpinned-resource\" 
  - skeptic: Stands. The It is short — `factory.Calls()` len 1, the resource id, `Expect(factory.Calls()[0].from).To(BeNil())`, and a build finish — and brine's `And the check plan for "unpinned-resource" starts from the version "nothing"` covers the load-bearing clause, in a scenario that carries the pinned sibling alongside it. Notably this It does NOT assert the argument flags, so E1 left it green, confirmi

**[DELETED]** Scanner > naturally excludes a persisted check_every never resource
  - mutation: m3_check_never — atc/lidar/scanner.go:451-453, delete the `if checkable.CheckEvery() != nil && checkable.CheckEvery().Never { return }` guard from check(), so a resource an operator told never to check is checked anyway.
  - brine: RED. Scenario "A resource told never to be checked is left alone, and its neighbours are not" failed at step `And no check was enqueued for "never-resource"` -> "expected the resources a check was enqueued for not to include \"never-resource\", but it does: [never-resource ordinary-resource]"
  - skeptic: Stands. The It asserts `factory.Calls()` empty plus `Consistently(fixture.CheckBuilds).ShouldNot(Receive())`; brine's `And no check was enqueued for "never-resource"` covers both, and brine adds a sibling (`And a check was enqueued for "ordinary-resource"`) the Go It lacks, so brine cannot pass on a scan that checked nothing at all. brine is strictly stronger.

**[DELETED]** Scanner > returns the real-backed enumeration failure
  - mutation: m1_res_enum_nil — atc/lidar/scanner.go:50, in Run(): `return err` after `s.checkFactory.Resources()` fails becomes `return nil`, so a failed enumeration is swallowed and reported as success.
  - brine: RED. Scenario "A scan whose resource enumeration the database refuses says so" failed at step `Then the scan was refused, saying "closed"` -> "expected the scan to be refused, it reported success — a caller told a failed sweep succeeded moves on believing the rows are gone"
  - skeptic: Stands. The It's only other clause, `Expect(factory.Calls()).To(BeEmpty())`, is vacuous in its own fixture — the test calls `useLidarDB()` with no `persistLidarPipeline`, so there are zero resources and nothing could have been checked whatever the scanner did. The feature file reaches the same conclusion in prose ("There is no 'and nothing was checked' clause here on purpose... the absence disting

**[DELETED]** Scanner > returns the resource-type enumeration failure after loading real resources
  - mutation: m2_type_enum_nil — atc/lidar/scanner.go:56, in Run(): `return err` after `s.checkFactory.ResourceTypesByPipeline()` fails becomes `return nil`, so every resource is then checked against an empty set of resource types.
  - brine: RED. Scenario "A scan that loaded its resources but cannot read their types checks nothing" failed at step `Then the scan was refused, saying "resource_types"` -> "expected the scan to be refused, it reported success — a caller told a failed sweep succeeded moves on believing the rows are gone"
  - skeptic: Stands. Both Go clauses have brine counterparts: `MatchError("nope")` maps to `Then the scan was refused, saying "resource_types"`, and `Expect(factory.Calls()).To(BeEmpty())` maps to `And no check was enqueued for "waiting-resource"` — and unlike the sibling above, brine's fixture actually persists a resource, so the absence is non-vacuous there. brine is at least as strong here.

**[DELETED]** Scanner Native Resource Resolution > creates a real in-memory check for a persisted non-native resource
  - mutation: m19_native_branch — atc/lidar/scanner.go:303, `if s.resolver != nil && rs.Type() == "registry-image"` becomes `if s.resolver != nil`, so an ordinary (non-registry-image) resource is pushed down the native-resolution path and never gets a check pod.
  - brine: RED. Scenario "An image resource is resolved from the registry while an ordinary one goes to a pod" failed at step `And a check was enqueued for "ordinary-resource"` -> "expected the resources a check was enqueued for to include \"ordinary-resource\", found []". Three further scenarios reddened on t
  - skeptic: Stands. Every clause maps: `ResolveCallCount()` zero to `And the resource "ordinary-resource" was left unresolved`, `factory.Calls()` len 1 plus the resource id and `build.ResourceID()` to `And a check was enqueued for "ordinary-resource"`, and `plan.Check.Resource` equalling the resource name to the plan steps in the sibling scenarios. m19_native_branch reddens four of 35 Its, which is well short

**[DELETED]** Scanner Native Resource Resolution > does not increment ChecksEnqueued for a production in-flight duplicate
  - mutation: m20_metric_hoist — atc/lidar/scanner.go:461-465, hoist `metric.Metrics.ChecksEnqueued.Inc()` out of the `else` in check() so it fires for a skipped duplicate too, turning a cluster whose checks are all stuck into one whose dashboard looks busy.
  - brine: RED. Scenario "The checks-enqueued counter counts checks created, not checks skipped" failed at step `And the checks enqueued counter went up by 1` -> "expected the checks enqueued counter to be 1, got 2" — the in-flight duplicate was counted.
  - skeptic: Stands — re-measured rather than taken on trust, because a process-global counter is exactly where cross-scenario leakage would hide. Hoisting `metric.Metrics.ChecksEnqueued.Inc()` out of the `else` in scanner.go's check(): Go "Ran 35 of 35 Specs" / "FAIL! -- 34 Passed | 1 Failed", the single failure being "[FAIL] Scanner Native Resource Resolution [It] does not increment ChecksEnqueued for a prod

**[DELETED]** Scanner Native Resource Resolution > does not persist native scope or version when the resolver fails
  - mutation: m14b_rs_resolve_err — atc/lidar/scanner.go:379-382, drop the `return` from the `if err != nil` branch after `s.resolver.Resolve(...)` in resolveResource, so a registry that cannot answer results in an empty digest being written.
  - brine: RED, on two scenarios. Scenario Outline "A registry-image resource is not resolved when <case>" row 1 ("the registry cannot answer for it") failed at step `And the resource "quiet-image" was left unresolved` -> "expected the resources the scan attached to a config not to include \"quiet-image\", but
  - skeptic: Stands. The It asserts `ResolveCallCount()` equals 1 plus config and scope both zero; brine's outline row 1 uses a real refusal ("quiet-image reads missing/app:latest instead") with a resolved bystander, and the m14b claim additionally reports the wrong-password clause of the private-image scenario reddening, which is a second independent witness for the same return. E7 left this It green, confirm

**[DELETED]** Scanner Native Resource Resolution > increments ChecksEnqueued when the production factory creates a check
  - mutation: m24_metric_drop — atc/lidar/scanner.go:464, delete `metric.Metrics.ChecksEnqueued.Inc()` from the `else` in check(), so the counter an operator watches to know the cluster is checking anything never moves.
  - brine: RED, on three scenarios, all at the same clause. "The checks-enqueued counter counts checks created, not checks skipped" failed at step `And the checks enqueued counter went up by 1` -> "expected the checks enqueued counter to be 1, got 0"; identically for "A scope collected before the version is sa
  - skeptic: Stands. `Expect(metric.Metrics.ChecksEnqueued.Delta()).To(BeNumerically("==", 1))` maps to `And the checks enqueued counter went up by 1`, and `factory.Calls()` len 1 with the resource id maps to `And a check was enqueued for "fresh-resource"`. The scenario "The checks-enqueued counter counts checks created, not checks skipped" carries both this It and its sibling in one database with an in-flight

**[DELETED]** Scanner Native Resource Resolution > passes persisted native basic-auth credentials to the resolver
  - mutation: m10b_rs_auth — atc/lidar/scanner.go:368-375, delete the `if username, ok := source["username"].(string); ok && username != ""` block from resolveResource so `auth` stays nil.
  - brine: RED. Scenario "A private image resolves with the credentials it carries and not without them" failed at step `And the resource "private-image" resolved to the digest "sha256:private-app"` -> "the resource \"private-image\" was never attached to a config scope, so the scan resolved nothing for it"
  - skeptic: Stands, and brine is stronger. The scenario "A private image resolves with the credentials it carries and not without them" gives the resource and the resource type DIFFERENT repositories under DIFFERENT logins with distinct digests, so each clause names which image its own copy of the credential block resolved, and the wrong-password row proves the registry actually checks. That is more than `Res

**[DELETED]** Scanner Resource Type Resolution > does not persist a version when the resolver fails
  - mutation: m14_rt_resolve_err — atc/lidar/scanner.go:189-192, drop the `return` from the `if err != nil` branch after `s.resolver.Resolve(...)` in resolveResourceType, so a registry outage writes an empty digest into the version instead of leaving the row alone for the next tick.
  - brine: RED. Scenario Outline "A resource type is not resolved when <case>" row 1 ("the registry cannot answer for it") failed at step `And the resource type "quiet-type" was left unresolved` -> "expected the resource types the scan attached to a config not to include \"quiet-type\", but it does: [bystander
  - skeptic: Stands. The It asserts `ResolveCallCount()` equals 1 (the registry was asked) and `ResourceConfigScopeID()` is zero; brine's outline row 1 arranges a genuine refusal ("quiet-type reads missing/type:latest instead") and asserts `And the resource type "quiet-type" was left unresolved` with a resolved bystander beside it, so the absence is not vacuous. E7 left this It green because it asserts a call 

**[DELETED]** Scanner Resource Type Resolution > passes persisted basic-auth credentials to the resolver
  - mutation: m10_rt_auth — atc/lidar/scanner.go:178-185, delete the `if username, ok := source["username"].(string); ok && username != ""` block from resolveResourceType so `auth` stays nil and every private resource-type image in the cluster stops resolving.
  - brine: RED. Scenario "A private image resolves with the credentials it carries and not without them" failed at step `And the resource type "private-type" resolved to the digest "sha256:private-type"` -> "the resource type \"private-type\" was never attached to a config scope, so the scan resolved nothing f
  - skeptic: Stands, and brine is genuinely stronger here as the claim says. The Go It asserts `auth.Username`/`auth.Password` against values the test itself supplied plus the digest landing; brine's registry refuses a wrong password, so `And the resource type "private-type" resolved to the digest "sha256:private-type"` is an assertion that the credentials arrived intact, and the scenario carries a third row (

**[REFUTED]** Scanner > creates an in-memory check from a persisted base-type resource
  - mutation: m4_plan_source — atc/db/resource.go:328, in (*resource).CheckPlan: `Source:  sourceDefaults.Merge(r.config.Source)` becomes `Source:  sourceDefaults`, dropping the resource's own source from the plan the check pod executes.
  - skeptic: REFUTED — the It asserts more than any scenario. Beyond the plan fields brine checks, it pins three TryCreateCheck arguments no scenario mentions: `Expect(call.manuallyTriggered).To(BeFalse())`, `Expect(call.skipIntervalRecursively).To(BeFalse())`, `Expect(call.toDB).To(BeFalse())`. MEASURED (experiment E1) — scanner.go:455, `TryCreateCheck(..., version, false, false, false)` becomes `(..., versio

**[REFUTED]** Scanner > forwards a persisted API pin to the production CheckFactory
  - mutation: m7_pin_nil — atc/lidar/scanner.go:449, `version := checkable.CurrentPinnedVersion()` becomes `var version atc.Version`, so a pinned resource is checked from nothing and the pipeline drifts off the pin a human set.
  - skeptic: REFUTED — same uncovered clauses as the base-type It. This It asserts `Expect(call.manuallyTriggered).To(BeFalse())`, `Expect(call.skipIntervalRecursively).To(BeFalse())` and `Expect(call.toDB).To(BeFalse())`; no step in resource-checking.feature mentions any of them. MEASURED (E1, scanner.go:455 skipIntervalRecursively false->true): Go "Ran 35 of 35 Specs" / "FAIL! -- 33 Passed | 2 Failed" with "

**[REFUTED]** Scanner > recovers when real resource scheduling crosses the explicit panic seam
  - mutation: m6_no_recover — atc/lidar/scanner.go:293-299, delete the `defer func() { err := util.DumpPanic(recover(), "scanning resource %d", rs.ID()); ... }()` from the per-item closure inside the scanResources worker, so a panic scanning one resource kills the worker goroutine and the whole process.
  - skeptic: REFUTED — no named brine Scenario goes red; the run aborts. MEASURED, reproducing the claim's own mutation (deleting the `defer util.DumpPanic(recover(), ...)` from the per-item closure in scanResources): `brine run` exits 2 and ends `{"run_id":"01M1BSYQT2VAZ2BJE4GAE2MADB","status":"in_flight"}` — no summary object, no verdict. Counting the event stream: 7 `scenario_start`, 6 `scenario_end`, and *

**[REFUTED]** Scanner Native Resource Resolution > persists a native digest and creates one ordinary check from a mixed pipeline
  - mutation: m19_native_branch — atc/lidar/scanner.go:303, `if s.resolver != nil && rs.Type() == "registry-image"` becomes `if s.resolver != nil`, collapsing the two branches so the ordinary half of a mixed pipeline is resolved instead of checked.
  - skeptic: REFUTED — the It pins `Expect(freshNative.ResourceConfigID()).To(Equal(expectedNativeConfig.ID()))`, config identity that no scenario asserts. E5 happened to leave this one green only because its native source carries no tag, so I built the complementary mutation. MEASURED (E5b) — in both FindOrCreateResourceConfig sites an absent tag is normalised to "latest" in the config key, an honest bug that

**[REFUTED]** Scanner Native Resource Resolution > persists a native digest, exact resource config, scope, and check end time
  - mutation: m21b_rs_no_endtime — atc/lidar/scanner.go:436-440, delete the `scope.UpdateLastCheckEndTime(true)` call from resolveResource, so a natively resolved resource never records that it was checked.
  - skeptic: REFUTED — the native twin of the resource-type case, and it carries one more uncovered clause. The It pins `Expect(freshResource.ResourceConfigID()).To(Equal(expectedConfig.ID()))`, `Expect(scope.ResourceID()).To(BeNil())` (the digest lands on the GLOBAL scope, not a per-resource one — the very property the feature file's private-image commentary depends on and never asserts), and `Expect(lastChec

**[REFUTED]** Scanner Native Resource Resolution > skips a persisted native resource with check_every never
  - mutation: m12b_rs_never — atc/lidar/scanner.go:343-346, delete the `if rs.CheckEvery() != nil && rs.CheckEvery().Never { ... return }` skip from resolveResource.
  - skeptic: REFUTED — the It asserts `Expect(resolver.ResolveCallCount()).To(BeZero())` and brine's outline row asserts only `And the resource "quiet-image" was left unresolved`, a `CheckNotMember` over `attachedResources()` that cannot see a registry request whose answer is thrown away. MEASURED (E7) — the two skip guards in resolveResource (`CheckEvery().Never` and the interval check) moved from before the 

**[REFUTED]** Scanner Native Resource Resolution > treats a scope deletion during native version save as a debug-level race
  - mutation: m17f_rs_fk_only — atc/lidar/scanner.go:423-429, drop ONLY the FK arm's `return` after `scope.SaveVersions` in resolveResource (the non-FK arm keeps its return), so a resource whose scope was collected mid-save falls through to the stamp, the success log and the metric. The split half of m17b.
  - skeptic: REFUTED for the same reason as its resource-type twin, and disclaimed by the feature file itself ("It stays in Go"). MEASURED (E6) — FK-arm `logger.Debug("scope-deleted-during-version-save", ...)` becomes `logger.Error("failed-to-save-versions", err)` with the `return` preserved, so control flow is unchanged. Go: "Ran 35 of 35 Specs" / "FAIL! -- 33 Passed | 2 Failed", naming "[FAIL] Scanner Native

**[REFUTED]** Scanner Resource Type Resolution > persists the resolved digest, scope, and check end time
  - mutation: m22_resolved_image_colon — atc/db/resource_type.go:269, `return repo + "@" + digest` becomes `return repo + ":" + digest`, so a resolved type is pulled by a tag-shaped reference instead of by digest and a pod gets whatever moved under the tag since the scan. Also independently reddened by m21_rt_no_endtime (scanner.go:247-251, delete the `scope.UpdateLastCheckEndTime(true)` call).
  - skeptic: REFUTED — the It asserts the identity of the resource_config row, which brine deliberately dropped, and that omission is reddenable. The It pins `Expect(freshType.ResourceConfigID()).To(Equal(expectedConfig.ID()))` where expectedConfig is `FindOrCreateResourceConfig("registry-image", config.Source, nil)` — i.e. the config must be keyed on the type's own full source. The feature file's DISPOSITION 

**[REFUTED]** Scanner Resource Type Resolution > resolves persisted resource types across independent pipelines
  - mutation: m15_first_pipeline_only — atc/lidar/scanner.go:74-76, add `break` to the `for _, types := range resourceTypesMap` loop in scanResourceTypes so only one pipeline's types are collected, freezing every other team's custom types at whatever image they were first set to.
  - skeptic: REFUTED twice over, on the resource_config identity clauses. The It asserts `firstType.ResourceConfigID()` equals a config looked up from the first type's own source, the same for the second, and `firstType.ResourceConfigID()).NotTo(Equal(secondType.ResourceConfigID()))`. MEASURED (E5, tag stripped from the config key): Go "FAIL! -- 29 Passed | 6 Failed" including "[FAIL] Scanner Resource Type Res

**[REFUTED]** Scanner Resource Type Resolution > skips a persisted check_every never resource type
  - mutation: m12_rt_never — atc/lidar/scanner.go:153-156, delete the `if rt.CheckEvery() != nil && rt.CheckEvery().Never { ... return }` skip from resolveResourceType.
  - skeptic: REFUTED — same uncovered clause, same experiment. The It asserts `Expect(resolver.ResolveCallCount()).To(BeZero())`; brine's Scenario Outline row 2 asserts only `And the resource type "quiet-type" was left unresolved`, which reads config attachment. MEASURED (E7, guards moved below the Resolve call in resolveResourceType): Go "Ran 35 of 35 Specs" / "FAIL! -- 30 Passed | 5 Failed" naming "[FAIL] Sc

**[REFUTED]** Scanner Resource Type Resolution > skips a persisted direct image reference
  - mutation: m11_rt_image_skip — atc/lidar/scanner.go:147-150, delete the `if rt.Image() != "" { ... return }` skip, so a resource type whose image an operator pinned outright is resolved anyway and the pin is overwritten with whatever the tag points at today.
  - skeptic: REFUTED — the It asserts `Expect(resolver.ResolveCallCount()).To(BeZero())`, that the registry was NOT ASKED, and brine has no vocabulary for a request whose result is discarded. Its outcome step "was left unresolved" is `CheckNotMember` over `attachedResourceTypes()` — it observes config attachment, not registry traffic. MEASURED (E7) — the three skip guards in resolveResourceType (`rt.Image() !=

**[REFUTED]** Scanner Resource Type Resolution > skips a persisted resource type whose nonzero interval has not elapsed
  - mutation: m13_rt_interval — atc/lidar/scanner.go:158-166, delete the whole interval block from resolveResourceType (the `interval := atc.DefaultResourceTypeInterval` default, the CheckEvery override, and the `time.Now().Before(rt.LastCheckEndTime().Add(interval))` guard), so a declared check_every is ignored and the registry is asked on every tick.
  - skeptic: REFUTED — same uncovered clause. The It asserts `Expect(resolver.ResolveCallCount()).To(BeZero())`; brine's interval scenario instead moves the registry between two scans and asserts the digest did not change, which detects a wrong WRITE but not a wasted READ. MEASURED (E7): Go "Ran 35 of 35 Specs" / "FAIL! -- 30 Passed | 5 Failed" naming "[FAIL] Scanner Resource Type Resolution [It] skips a persi

**[REFUTED]** Scanner Resource Type Resolution > treats a scope deletion during version save as a debug-level race
  - mutation: m17e_rt_fk_only — atc/lidar/scanner.go:234-240, drop ONLY the FK arm's `return` after `scope.SaveVersions` in resolveResourceType (the non-FK arm keeps its return), so a pass whose scope was collected mid-save falls through to the check-end-time stamp, the success log and the metric. This is the split half of m17; see the bucket note on why the unsplit m17 was misleading.
  - skeptic: REFUTED, and the feature file refutes it itself. resource-checking.feature's DISPOSITION section states: the DEBUG-versus-ERROR log level "is a pure log-level assertion... there is no outcome to assert and no vocabulary in this corpus for asserting a log line. It stays in Go." The claim marks for deletion precisely one of the Its that disposition says stays. MEASURED (E6) — the FK arm's `logger.De

**[GAP]** Scanner Native Resource Resolution > does not resolve or attach a persisted native source without a repository
  - mutation: m16b_rs_repo_guard — atc/lidar/scanner.go:361-364, delete the `if repository == "" { logger.Error("missing-repository-in-source", nil); return }` guard from resolveResource.

**[GAP]** Scanner Native Resource Resolution > skips a persisted native resource whose nonzero interval has not elapsed
  - mutation: m13b_rs_interval — atc/lidar/scanner.go:348-356, delete the whole interval block from resolveResource (the `interval := atc.DefaultCheckInterval` default, the CheckEvery override, and the `time.Now().Before(rs.LastCheckEndTime().Add(interval))` guard).

**[GAP]** Scanner Native Resource Resolution > treats a scope deletion before native resource attachment as a debug-level race
  - mutation: m18b_rs_fk_attach_noreturn — atc/lidar/scanner.go:405-417, drop BOTH `return`s from the `rs.SetResourceConfigScope(scope)` error guard in resolveResource, so a resource whose scope was collected before it could be pointed at it falls through to SaveVersions.

**[GAP]** Scanner Resource Type Resolution > does not call the resolver when persisted source has no repository
  - mutation: m16_rt_repo_guard — atc/lidar/scanner.go:171-174, delete the `if repository == "" { logger.Error("missing-repository-in-source", nil); return }` guard from resolveResourceType, so a source naming no repository reaches the resolver.

**[GAP]** Scanner Resource Type Resolution > logs a non-FK version-save failure as an error
  - mutation: m17c_rt_nonfk_only — atc/lidar/scanner.go:242-243, drop ONLY the non-FK arm's `return` after `scope.SaveVersions` in resolveResourceType (the FK arm keeps its return), so an ordinary save failure (a dropped connection, say) falls through to the check-end-time stamp and the metric.

**[GAP]** Scanner Resource Type Resolution > treats a scope deletion before attachment as a debug-level race
  - mutation: TWO mutations, because the first is log-level-only and I did not want to grade on that alone. m18_rt_fk_attach_noreturn: scanner.go:218, `if db.IsForeignKeyViolation(err)` becomes `if false` in resolveResourceType, so the collected-scope race is logged at ERROR instead of DEBUG with control flow unchanged. m18c_rt_fk_attach_fallthrough: scanner.go:216-228, drop BOTH `return`s from the attach guard

**[INERT]** Lidar PostgreSQL fixture > reads persisted pipeline state through a separately constructed factory
  - mutation: NONE APPLICABLE. This It names no atc/lidar production line: it persists a team+pipeline and reads them back through db.NewTeamFactory(fixture.Conn, fixture.LockFactory).FindTeam / loadedTeam.Pipeline, asserting only that the ids round-trip. It never constructs a scanner. It was carried through all 40 mutations (including m4 atc/db/resource.go, m8 atc/db/check_factory.go and m22 atc/db/resource_ty

**[INERT]** Scanner > does not schedule a check for an already-cancelled empty enumeration
  - mutation: THREE honest mutations of the exact lines this It pins, run separately: mcA_worker_ignores_cancel (scanner.go:311-314, delete `case <-ctx.Done(): ... return` from the scanResources worker select); mcB_producer_ignores_cancel (scanner.go:269-273, replace the producer's select with a bare `resourcesChan <- rs`); mcC_run_ignores_cancel (scanner.go:325-331, replace the final `select { case <-done / ca


## task_cache_collector_test.go

**[GAP]** TaskCacheCollector Run [It] collects caches of an archived pipeline and leaves the rest
  - mutation: M16 — atc/db/task_cache_lifecycle.go:28-30, replaced the whole `WHERE p.archived OR (p.paused AND j.next_build_id IS NULL) OR (j.paused AND j.next_build_id IS NULL)` with `WHERE false`, so CleanUpInvalidTaskCaches collects nothing. ALSO measured M14 — task_cache_lifecycle.go:28, dropped just the `p.archived OR` arm: that leaves this It GREEN, because pipeline.archive() (atc/db/pipeline.go:731) als

**[GAP]** TaskCacheCollector Run [It] returns the error when the cleanup fails
  - mutation: M15 — atc/db/task_cache_lifecycle.go:35-37, changed CleanUpInvalidTaskCaches' `if err != nil { return nil, err }` on the Query to `return nil, nil`, so a database failure is swallowed and the collector reports success.


## volume_collector_test.go

**[DELETED]** VolumeCollector Run when there are failed volumes deletes all the failed volumes from the database
  - mutation: M_O — atc/db/volume_repository.go:599 (DestroyFailedVolumes): `"v.state": string(VolumeStateFailed)` -> `"v.state": string(VolumeStateDestroying)`, so failed volumes are never deleted
  - brine: gc-containers.feature, Scenario "A volume that failed to be created is deleted, and a healthy one beside it is not" — `FAIL (6/7 steps)` / `Step FAILED: And the volume "failed-volume" has been removed from the database` (only this one scenario of the 10 reddened)
  - skeptic: Not re-run under M_O. The It's only behavioural assertion is that the failed volume's row count is zero after Run (the preceding state check is a fixture assertion, not a claim about the collector); brine's `And the volume "failed-volume" has been removed from the database` asserts that, and adds `And the volume "healthy-volume" is still in the database`, which the ginkgo It does not have — so a c

**[DELETED]** VolumeCollector Run when there are orphaned volumes marks orphaned volumes as 'destroying'
  - mutation: M_P — atc/gc/volume_collector.go:50: the `vc.markOrphanedVolumesAsDestroying(...)` call in Run() removed (method left defined but never invoked)
  - brine: gc-containers.feature, Scenario "A volume whose container is gone is marked for destruction, and one still held is left alone" — `FAIL (6/7 steps)` / `Step FAILED: And the volume "orphaned-volume" is now destroying` (only this one scenario of the 10 reddened)
  - skeptic: Re-run, chosen as the spot-check for the whole gc-containers file because this It has TWO assertions (`HaveLen(1)` and `Equal(expectedOrphanedVolumeHandles)`) and the claim quoted only one. Mutation: deleted the `vc.markOrphanedVolumesAsDestroying(...)` call and its error arm from VolumeCollector.Run. Go, focused on all three VolumeCollector Its: `Ran 3 of 109 Specs in 2.040 seconds` / `FAIL! -- 2

**[DELETED]** VolumeCollector Run when there are volumes the worker has stopped reporting deletes the ones past the grace period and keeps the rest
  - mutation: M_N — atc/db/volume_repository.go:168 (RemoveMissingVolumes): `WHERE missing_since IS NOT NULL and NOW() - missing_since > $1` -> `WHERE COALESCE(NOW() - missing_since, INTERVAL '99 years') > $1`, the feature file's own named mutation (a NULL missing_since now reads as "missing forever")
  - brine: gc-containers.feature, Scenario "A volume the worker stopped reporting is deleted once the grace period passes" — `FAIL (11/11 steps)` / `Step FAILED: And the volume "still-reported" is still in the database`. Same mutation also reddened the other two volume scenarios in that file, which is expected
  - skeptic: Not re-run under M_N, but I exercised the gc-containers volume plumbing end to end in a separate experiment (see the orphaned-volume row) and it behaved correctly, including this scenario passing green at `A volume the worker stopped reporting is deleted once the grace period passes (11 steps)` when unaffected. brine builds `gc.NewVolumeCollector(database.VolumeRepository, gcGracePeriod)` with `gc


## worker_collector_test.go

**[DELETED]** WorkerCollector Run leaves a non-ephemeral worker even once it has expired
  - mutation: M_L — atc/db/worker_lifecycle.go:26 (DeleteUnresponsiveEphemeralWorkers): `.Where(sq.Eq{"ephemeral": true})` deleted, leaving only `expires < NOW()`
  - brine: gc-reclamation.feature, Scenario Outline "A worker is reclaimed only when it is both ephemeral and unresponsive — <case>" (row 3, `stalled, not disposable`) — `FAIL (6/7 steps)` / `Step FAILED: And the worker "expired-persistent" is still registered` / `Error: expected the workers still registered t
  - skeptic: Not re-run. Same structure as the row above: dropping `sq.Eq{"ephemeral": true}` can only redden the expired-persistent row, and the claim's own quoted brine error (`expected the workers still registered to include "expired-persistent", found [bystander]`) shows the bystander present, so the scenario did not die on setup. brine wires `gc.NewWorkerCollector(db.NewWorkerLifecycle(...))` and register

**[DELETED]** WorkerCollector Run leaves an ephemeral worker that is still heartbeating
  - mutation: M_K — atc/db/worker_lifecycle.go:27 (DeleteUnresponsiveEphemeralWorkers): `.Where(sq.Expr("expires < NOW()"))` deleted, leaving only the `ephemeral` predicate
  - brine: gc-reclamation.feature, Scenario Outline "A worker is reclaimed only when it is both ephemeral and unresponsive — <case>" (row 2, `disposable but alive`) — `FAIL (6/7 steps)` / `Step FAILED: And the worker "live-ephemeral" is still registered`
  - skeptic: Not re-run. The mutation is narrow and one layer down (drop `expires < NOW()` from DeleteUnresponsiveEphemeralWorkers), and it can only redden the row whose worker is ephemeral-but-live — which is exactly outline row 2. The It's assertion (`found` is true for live-ephemeral) and brine's `And the worker "live-ephemeral" is still registered` read the same fact off the same table; brine additionally 

**[DELETED]** WorkerCollector Run removes an ephemeral worker that has stopped heartbeating
  - mutation: M_J — atc/gc/worker_collector.go:36: `affected, err := wc.workerLifecycle.DeleteUnresponsiveEphemeralWorkers()` replaced with `var affected []string; var err error`, i.e. the collector never makes the call
  - brine: gc-reclamation.feature, Scenario Outline "A worker is reclaimed only when it is both ephemeral and unresponsive — <case>" (row 1, `disposable and gone`) — `FAIL (6/7 steps)` / `Step FAILED: And the worker "expired-ephemeral" has been reclaimed`
  - skeptic: Re-run, because M_J deletes the whole `DeleteUnresponsiveEphemeralWorkers` call and is therefore broad enough to be worth checking for spurious pairing. Go, focused on all four WorkerCollector Its: `Ran 4 of 109 Specs in 1.261 seconds` / `FAIL! -- 2 Passed | 2 Failed | 0 Pending | 105 Skipped`, failing at worker_collector_test.go:48 and :81. brine: `9 passed, 2 failed`, `FAIL A worker is reclaimed

**[DELETED]** WorkerCollector Run returns the error when the delete fails
  - mutation: M_M — atc/gc/worker_collector.go:39: `return err` -> `return nil` after DeleteUnresponsiveEphemeralWorkers fails
  - brine: gc-reclamation.feature, Scenario "A collector that cannot reach the database reports the failure rather than a clean pass" — `FAIL (5/6 steps)` / `Step FAILED: Then the collector's sweep was refused, saying "closed"`
  - skeptic: Not re-run under its own mutation, but I got a second independent both-red witness for free during the M_J run: Go failed at worker_collector_test.go:81 and brine failed `A collector that cannot reach the database reports the failure rather than a clean pass (5/6 steps)` / `Step FAILED: Then the collector's sweep was refused, saying "closed"` / `Error: expected the collector's sweep to be refused,
