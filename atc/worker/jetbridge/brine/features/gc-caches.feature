Feature: Reclaiming what a finished build leaves behind

  Four collectors, one question: after a build ends, which of its rows may the
  database forget, and when? Every scenario below runs a real sweep against
  real PostgreSQL and then says which rows survived and which are gone. There
  is no call count anywhere in this file, and there was none in the four suites
  it came from either.

  What breaks in production if these regress is not symmetrical, and it is
  worth naming before the scenarios.

  Reclaiming too EARLY loses data that is still wanted: a resource cache
  deleted while a job still needs it as an input means the next build re-fetches
  a version the cluster already had, and a resource config deleted out from
  under a running check leaves the check with nowhere to write its versions.
  Reclaiming too LATE leaks: `resource_cache_uses` is written on every get and
  every image fetch, so a use that is never released grows without bound and
  drags the invalid-cache query with it; the same is true of `worker_artifacts`,
  which nothing else ever deletes.

  Source: atc/gc/artifacts_collector_test.go (2 cases),
  resource_cache_use_collector_test.go (6), resource_config_collector_test.go
  (7) and resource_cache_collector_test.go (10) — 25 cases, all migrated, into
  19 scenarios. None carried requirement identifiers, so there are no tags.

  Every scenario below has been shown to fail. Twenty mutations were applied to
  atc/db and atc/gc in an isolated worktree and each was rebuilt and run against
  this file; the table at the foot records which scenario each one reddened, and
  all nineteen appear in it. Two of the "reddened by" notes in this file were
  WRONG before that ran, and one ginkgo case turned out to be unreachable by any
  single mutation. Both are called out where they belong.

  The whole file shares one fixture, which is gc_suite_test.go's: a team, a
  pipeline holding one resource, one resource type and two jobs. The two jobs
  are load-bearing — "a later build of the SAME job" and "a later build of
  ANOTHER job" are the two halves of the image-cache rule, and a one-job
  pipeline cannot state it.

  It also shares one sweep. Three of these collectors are only meaningful in a
  chain, exactly as gc_suite_test.go's JustBeforeEach runs them: a cache is not
  collectable until its use row is gone, and a use is not collectable until its
  build is non-interceptible. So the Given names the chain and "garbage
  collection runs" is one sentence throughout.

  # ==========================================================================
  # Expired build artifacts
  # ==========================================================================

  # worker_artifacts rows are written when a build uploads an artifact and are
  # deleted by nothing else in the system — this collector is the only reader
  # of that table's created_at. The cutoff is twelve hours
  # (worker_artifact_lifecycle.go), and both sides of it matter for different
  # reasons: too eager and a build that is still running loses the artifact a
  # later step will ask for; never at all and the table grows for the life of
  # the deployment.
  #
  # The ginkgo pair used 13 hours and 11 hours 59 minutes, which leaves a
  # 61-minute-wide window in which the interval could be changed with both
  # cases still green. One minute either side of the cutoff closes that at the
  # cost of nothing — the row that survives in one example is the row that goes
  # in the other, and only the interval separates them.
  #
  # "fresh" is here because "has been reclaimed" is an absence, and an absence
  # passes on an empty table. A fixture that quietly stopped inserting would
  # fail on the bystander rather than pass on the vacuum.
  #
  # Reddened by: changing '12 hours' in RemoveExpiredArtifacts in either
  # direction — up and the first example's artifact survives, down and the
  # second's disappears. Or by the collector returning nil without calling the
  # lifecycle at all, which fails the first example.
  Scenario Outline: A build artifact is reclaimed only once it is older than twelve hours — <case>
    Given a garbage collector for expired build artifacts
    And the artifact "under-test" was created "<age>" ago
    And the artifact "fresh" was created "1 hour" ago
    When garbage collection runs
    Then garbage collection completed without error
    And the artifact "under-test" <fate>
    And the artifact "fresh" survived the sweep

    Examples:
      | case                       | age                 | fate               |
      | one minute past the cutoff | 12 hours 1 minute   | has been reclaimed |
      | one minute short of it     | 11 hours 59 minutes | survived the sweep |

  # ==========================================================================
  # The uses a build holds on its caches
  # ==========================================================================

  # A resource_cache_uses row is what stops a cache being collected while
  # something might still need it, and it is released when its build can no
  # longer be intercepted — because "interceptible" is precisely "an operator
  # might still hijack into this build's containers", and once that is false
  # nothing will ask for the cache again through this build.
  #
  # The three end states below reach that answer by three DIFFERENT routes, and
  # the ginkgo suite could not tell them apart because it ran the build
  # collector before every assertion:
  #
  #   still running   interceptible is still true; nothing is released
  #   succeeded       Finish leaves interceptible true, and the BUILD COLLECTOR
  #                   clears it, subject to the one-off grace period
  #   failed/aborted  Finish clears interceptible ITSELF, immediately, because
  #                   a non-succeeded build has no container state worth
  #                   hijacking into
  #
  # The last row is the one that proves the third route exists. The build
  # collector deliberately SPARES the latest completed build of a job
  # (constructBuildFilter's `NOT EXISTS ... latest_completed_build_id`), so if
  # Finish did not clear the flag, that build's uses would be held until the
  # failed-build grace period elapsed — an hour of a cache nobody can collect,
  # per failing job, forever, because the next failure replaces it as the
  # latest.
  #
  # "bystander" is a running build alongside, and it is what makes "have been
  # released" mean something: the ginkgo suite counted the whole
  # resource_cache_uses table, which cannot distinguish "this build's uses went"
  # from "somebody's went". Counting per build can, and it is also the content
  # of the ginkgo case that needed two sweeps and a non-zero-then-zero count to
  # say the same thing.
  #
  # Reddened by, in both directions. CleanUsesForFinishedBuilds returning nil
  # before its delete fails the four rows that expect a release and leaves the
  # first alone. DROPPING its `b.interceptible = false` predicate fails the
  # opposite half: the delete then takes every use that has a build at all, so
  # the bystander line goes red in every row. Narrowing the predicate to
  # `b.status = 'failed'` fails the succeeded and aborted rows only. Deleting
  # the `if status != BuildStatusSucceeded` branch from Build.Finish fails the
  # last row and nothing else, because for a one-off the build collector picks
  # up the slack and for a job's latest completed build it deliberately does
  # not.
  Scenario Outline: A build's cache uses are released only when the build can no longer be intercepted — <case>
    Given a garbage collector for resource cache uses
    And the build "under-test" <build state>
    And the cache "under-test-cache" was created for the build "under-test"
    And the build "bystander" is a one-off build that is still running
    And the cache "bystander-cache" was created for the build "bystander"
    When garbage collection runs
    Then garbage collection completed without error
    And the cache uses of the build "under-test" <fate>
    And the cache uses of the build "bystander" are still held

    Examples:
      | case                             | build state                                  | fate               |
      | still running                    | is a one-off build that is still running     | are still held     |
      | succeeded                        | is a one-off build that succeeded            | have been released |
      | failed                           | is a one-off build that failed               | have been released |
      | aborted                          | is a one-off build that was aborted          | have been released |
      | the latest failed build of a job | is a build of the job "some-job" that failed | have been released |

  # ==========================================================================
  # Resource configs, and the four things that keep one alive
  # ==========================================================================

  # A resource_config is the identity of "this type, with this source", and
  # everything that checks or fetches anything hangs off one. Deleting a live
  # one is not a leak, it is an outage: the resource loses its version history
  # and its scope, and the next check starts from nothing.
  #
  # CleanUnreferencedConfigs spares a config referenced from any of four tables.
  # Three of them are here as separate lines rather than separate scenarios,
  # because they are separate REASONS asserted against the same reclaimed
  # orphan in the same database — which is strictly more than the ginkgo suite
  # had, where each "preserve the config" case ran alone and could not have
  # told a working sweep from one that deleted nothing at all.
  #
  # The fourth arm, prototypes, has no coverage here and had none in the ginkgo
  # suite either: dropping `usedByPrototypesIds` from the UNION reddens nothing
  # in this repository. Left as a known hole rather than papered over.
  #
  # Reddened by: dropping any one of the three UNION arms below. Dropping the
  # resources or resource_types arm takes that survivor and nothing else — the
  # scope row cascades. Dropping the resource_caches arm is louder, because
  # resource_caches.resource_config_id is ON DELETE RESTRICT: the whole DELETE
  # aborts, the collector swallows the foreign-key violation, and the ORPHAN
  # line goes red instead. Either way this scenario is the one that says so.
  # CleanUnreferencedConfigs returning nil before the delete fails the orphan
  # line alone.
  Scenario: A config is collected only when nothing references it any more
    Given a garbage collector for unreferenced resource configs
    And the config "held-by-a-cache" is referenced by a resource cache
    And the config "held-by-a-resource" is referenced by a pipeline resource
    And the config "held-by-a-resource-type" is referenced by a pipeline resource type
    And the config "orphan" is referenced by nothing
    And every config was last referenced longer ago than the grace period
    When garbage collection runs
    Then garbage collection completed without error
    And the config "orphan" has been reclaimed
    And the config "held-by-a-cache" survived the sweep
    And the config "held-by-a-resource" survived the sweep
    And the config "held-by-a-resource-type" survived the sweep

  # Being unreferenced is not enough on its own, and the grace period is why.
  # A config is written by a check and referenced again a moment later by the
  # get that follows it; between those two moments it is referenced by nothing.
  # Collecting it there would delete a config that is about to be used, so the
  # collector waits out its window first.
  #
  # The ginkgo suite spent two cases on this — one for a config a resource had
  # released and one for a config a resource type had — and swept twice within
  # each, asserting non-zero and then zero. Both are the same sentence, and both
  # halves fit in one database once the two configs differ only in how long ago
  # they were last referenced.
  #
  # Reddened by: deleting the `now() - last_referenced > gracePeriod` predicate
  # from CleanUnreferencedConfigs — the recently referenced config disappears.
  # Or by inverting it, which fails the other line.
  Scenario: A config nothing references is spared until the grace period elapses
    Given a garbage collector for unreferenced resource configs
    And the config "recently-referenced" is referenced by nothing
    And the config "long-forgotten" is referenced by nothing
    And the config "long-forgotten" was last referenced longer ago than the grace period
    When garbage collection runs
    Then garbage collection completed without error
    And the config "long-forgotten" has been reclaimed
    And the config "recently-referenced" survived the sweep

  # A CHARACTERISATION, not an endorsement. A container running a check holds
  # its config through resource_config_check_sessions, and that is the one
  # reference CleanUnreferencedConfigs does not know about — it is not in the
  # UNION. The foreign key is ON DELETE RESTRICT, so the config is protected,
  # but by the schema rather than by the query, and the way that protection
  # arrives is a statement-wide failure: PostgreSQL aborts the whole DELETE,
  # and the collector catches the foreign-key violation and returns nil. So
  # nothing at all is collected, the sweep reports success, and the next pass
  # will do the same for as long as any check container exists.
  #
  # That is why "orphan" is asserted to SURVIVE here. It is the wrong outcome
  # and the scenario says so; a deployment with a permanent check container
  # collects no configs at all, and neither a log line nor an error rate says
  # so. If someone adds resource_config_check_sessions to the UNION, this line
  # goes red — which is the correct signal, and the prose is here so whoever
  # sees it knows to delete the line rather than restore the behaviour.
  #
  # The ginkgo case this replaces could not observe any of it. Its config was
  # created through the factory that stamps last_referenced = now(), and it
  # never aged it, so the grace period alone spared it: the DELETE never
  # reached the row, the foreign key was never tested, and the case would have
  # passed with the check session removed entirely.
  #
  # Reddened by: the ForeignKeyViolation branch in CleanUnreferencedConfigs
  # returning err instead of nil — the sweep is then refused rather than
  # silently empty, and the first assertion fails.
  Scenario: A config a check session pins blocks the whole sweep, which still reports success
    Given a garbage collector for unreferenced resource configs
    And the config "pinned-by-a-check-session" is held by a container's check session
    And the config "orphan" is referenced by nothing
    And every config was last referenced longer ago than the grace period
    When garbage collection runs
    Then garbage collection completed without error
    And the config "pinned-by-a-check-session" survived the sweep
    And the config "orphan" survived the sweep

  # ==========================================================================
  # Resource caches
  # ==========================================================================

  # The plainest of the five references. While a build is interceptible its
  # uses stand, and while a use stands the cache stands — so a cache is never
  # collected out from under a step that is still running.
  #
  # The finished build beside it is what makes the scenario able to fail in the
  # other direction: without it, a collector that deleted nothing would pass.
  #
  # Reddened by: dropping the resource_cache_uses arm from CleanUpInvalidCaches'
  # UNION, which takes the in-use cache. Or by the collector returning before
  # the delete, which leaves the orphan behind.
  Scenario: A cache a running build still holds is kept, and one no build holds is collected
    Given a garbage collector for resource caches
    And the build "running-build" is a one-off build that is still running
    And the cache "in-use" was created for the build "running-build"
    And the build "finished-build" is a one-off build that succeeded
    And the cache "orphaned" was created for the build "finished-build"
    When garbage collection runs
    Then garbage collection completed without error
    And the cache "orphaned" has been reclaimed
    And the cache "in-use" survived the sweep

  # The scheduler's claim on a cache. Once a version has been chosen as a job's
  # next input, the cache holding it must survive even though the build that
  # fetched it has long finished — otherwise the build about to start re-fetches
  # a version the cluster already has on disk.
  #
  # Pausing the pipeline withdraws that claim, and the predicate that does it is
  # a single `p.paused = false` inside the join. That is a deliberate policy: a
  # paused pipeline is not going to run anything, so its inputs stop being worth
  # keeping. It is also easy to lose, because the join reads correctly without
  # it.
  #
  # "in-use" is the surviving sibling for the paused row, and it earns its place
  # twice: it also shows the paused sweep is selective rather than a sweep that
  # collected everything.
  #
  # Reddened by: deleting `Where(sq.Expr("p.paused = false"))` from the sha256
  # next-build-inputs subquery in CleanUpInvalidCaches — the paused row's cache
  # then survives. Deleting that subquery entirely fails the unpaused row.
  #
  # HOLE, named where it is relevant: there are TWO such subqueries, one
  # joining on rcv.version_md5 and one on rcv.version_sha256, and only the
  # sha256 one is reachable here. version_md5 was kept by the sha256 migration
  # for rows written before it, and nothing writes it now — so the md5 arm can
  # only ever match a pre-migration version, which a test database does not
  # have. Deleting the md5 subquery reddens nothing, here or in ginkgo.
  Scenario Outline: A cache the scheduler still needs as an input is kept unless the pipeline is paused — <case>
    Given a garbage collector for resource caches
    And the build "job-build" is a build of the job "some-job" that succeeded
    And the cache "input-cache" holds the pipeline resource's version for the build "job-build"
    And the cache "input-cache" is the next input for the job "some-job"
    And the build "running-build" is a one-off build that is still running
    And the cache "in-use" was created for the build "running-build"
    And the pipeline is <pipeline state>
    When garbage collection runs
    Then garbage collection completed without error
    And the cache "input-cache" <fate>
    And the cache "in-use" survived the sweep

    Examples:
      | case                              | pipeline state | fate               |
      | the job will run it next          | not paused     | survived the sweep |
      | the pipeline has been paused      | paused         | has been reclaimed |

  # The image a build ran on is remembered after the build ends, because
  # re-running the same job should not re-pull the same image. The build's own
  # use row is gone by the time this sweep finishes — the build succeeded, so
  # the build collector and the use collector have already released it — which
  # means build_image_resource_caches is the only thing left holding this cache,
  # and that is exactly the reference under test.
  #
  # Reddened by: dropping the build_image_resource_caches arm from
  # CleanUpInvalidCaches' UNION.
  Scenario: A job build's image cache outlives the build that recorded it
    Given a garbage collector for resource caches
    And the build "first" is a build of the job "some-job" that succeeded
    And the cache "first-image" was recorded as the image for the build "first"
    And the build "unrelated" is a one-off build that succeeded
    And the cache "orphaned" was created for the build "unrelated"
    When garbage collection runs
    Then garbage collection completed without error
    And the cache "orphaned" has been reclaimed
    And the cache "first-image" survived the sweep

  # ...and it is released when a later build of the same job SUCCEEDS on a
  # different image, because at that point the old image is the one nothing
  # will run again. The release is not the collector's doing at all: Build.Finish
  # deletes build_image_resource_caches for its job with a lower build id, and
  # the collector then finds the cache unreferenced. Two predicates guard that
  # delete and each one alone would be destructive.
  #
  #   the job must MATCH — otherwise every job's successful build would drop
  #   every other job's image cache, and a busy pipeline would re-pull images
  #   on every run
  #
  #   the build must have SUCCEEDED — otherwise a failing job would discard the
  #   image of its own last good build, which is the image the retry needs most
  #
  # So the three rows are the two discriminators, each isolated once and the
  # positive case stated once. The ginkgo suite had a fourth — another job's
  # build that FAILED — and it is dropped here on measurement rather than on
  # taste: it is reddened by no mutation of that delete. Dropping the job_id
  # predicate leaves it green because a failed build never reaches the delete
  # at all; dropping the success guard leaves it green because the job still
  # does not match. Only mutating both at once reaches it, and rows two and
  # three each catch one of those on their own. See the table at the foot of
  # this file.
  #
  # "second-image" is asserted in every row, and it is not filler: it is what
  # catches `build_id <` widening to `build_id <=`, which would have a
  # successful build delete the record of its OWN image. That only works
  # because the second build records its image BEFORE it finishes — the step
  # says so, and the ginkgo suite did the same. Recorded afterwards, the row
  # would not exist when Finish ran and no comparison on it could be wrong.
  #
  # Reddened by: dropping `job_id` from the delete in Build.Finish, which fails
  # the "another job's build won" row. Dropping `status == BuildStatusSucceeded`
  # fails the "same job lost" row. Widening `sq.Lt` to `sq.LtOrEq` fails
  # "second-image" in both rows whose second build succeeded.
  Scenario Outline: A job build's image cache is released only when a later build of the same job succeeds — <case>
    Given a garbage collector for resource caches
    And the build "first" is a build of the job "some-job" that succeeded
    And the cache "first-image" was recorded as the image for the build "first"
    And the build "second" is a build of the job "<later job>" that recorded the image cache "second-image" and then <outcome>
    When garbage collection runs
    Then garbage collection completed without error
    And the cache "first-image" <fate>
    And the cache "second-image" survived the sweep

    Examples:
      | case                               | later job      | outcome   | fate               |
      | a later build of the same job won  | some-job       | succeeded | has been reclaimed |
      | a later build of the same job lost | some-job       | failed    | survived the sweep |
      | another job's build won            | some-other-job | succeeded | survived the sweep |

  # A one-off build has no later build to supersede it, so its image record is
  # released on a timer instead: a day after the build ended. `fly execute` is
  # the thing that makes these, and a developer who runs one twice in an
  # afternoon should hit the same image; a week later nobody cares.
  #
  # "job-image" is the sibling, and it isolates the predicate that matters most
  # here. CleanBuildImageResourceCaches deletes only rows whose job_id IS NULL,
  # so a job build's image cache must be untouched by the day-old rule no
  # matter how long ago that build ended — otherwise the timer would quietly
  # override the supersede rule above and every job's image record would expire
  # daily.
  #
  # Which is why that build is ALSO more than a day old. A job build that ended
  # a moment ago would be spared by the 24-hour predicate on its own, so the
  # sibling would survive whether or not the job_id predicate existed and would
  # prove nothing. Aged past the cutoff, it survives on the job_id predicate
  # alone, and that is the assertion.
  #
  # Reddened by: dropping `Where(sq.Eq{"birc.job_id": nil})` from
  # CleanBuildImageResourceCaches, which takes "job-image" in both examples.
  # Changing '24 HOURS' in either direction fails one example each.
  Scenario Outline: A one-off build's image cache is released a day after the build ended — <case>
    Given a garbage collector for resource caches
    And the build "job-build" is a build of the job "some-job" that succeeded more than a day ago
    And the cache "job-image" was recorded as the image for the build "job-build"
    And the build "one-off" is a one-off build that <ending>
    And the cache "one-off-image" was recorded as the image for the build "one-off"
    When garbage collection runs
    Then garbage collection completed without error
    And the cache "one-off-image" <fate>
    And the cache "job-image" survived the sweep

    Examples:
      | case                     | ending                          | fate               |
      | the build just ended     | succeeded                       | survived the sweep |
      | the build ended days ago | succeeded more than a day ago   | has been reclaimed |

  # ==========================================================================
  # Dispositions and holes
  # ==========================================================================
  #
  # DISPOSITION — "when a later build of the same job has succeeded" in
  # resource_cache_use_collector_test.go is not a scenario of its own. Its
  # content is that uses belong to builds one at a time: releasing one build's
  # does not touch another's. That is the "bystander" line in every row of the
  # use outline, asserted in the same database as the release rather than
  # inferred from a global count that went non-zero and then zero across two
  # sweeps.
  #
  # DISPOSITION — "when the resource cache is still in use" asserted that two
  # caches of two unfinished builds both survive. One unfinished build states
  # the rule; the second was the same sentence again, and the scenario it lives
  # in now contrasts it against a cache that IS collected, which the ginkgo
  # case did not.
  #
  # HOLE — CleanInvalidWorkerResourceCaches is called by the resource cache
  # collector on every pass and is asserted by nothing, here or in ginkgo.
  # Deleting its body reddens no test in this repository.
  #
  # HOLE — CleanDirtyInMemoryBuildUses, the third arm of the cache-use
  # collector, is likewise uncovered: in-memory check builds write uses keyed on
  # in_memory_build_create_time and those are released 24 hours later by a query
  # nothing exercises.
  #
  # HOLE — the prototypes arm of CleanUnreferencedConfigs, noted above.
  #
  # All three are recorded rather than filled because they are outside the four
  # suites this file migrates; filling them is new coverage, not migration, and
  # it should be someone's deliberate decision rather than a side effect.
  #
  # DROPPED ON MEASUREMENT — "when another build of a different job exists with
  # a different image cache ... when the second build fails" is not a row of
  # the image-cache outline. No single mutation of the delete in Build.Finish
  # reddens it, because it is the conjunction of two negatives that the two
  # rows beside it isolate one at a time. It is the only one of the 25 source
  # cases that is not represented, and it is recorded here rather than quietly
  # left out.
  #
  # ==========================================================================
  # What was measured
  # ==========================================================================
  #
  # Twenty mutations, applied one at a time to a checkout of this commit in a
  # separate worktree, each rebuilt and run against this file. The right column
  # names the scenarios that went from green to red. "use row N" and "image row
  # N" are the Examples rows of the two outlines, top to bottom.
  #
  #   worker_artifact_lifecycle.go
  #     '12 hours' -> '24 hours'            artifact row 1
  #     '12 hours' -> '6 hours'             artifact row 2
  #   artifacts_collector.go
  #     never calls the lifecycle           artifact row 1
  #
  #   resource_cache_lifecycle.go
  #     CleanUsesForFinishedBuilds: no-op   use rows 2,3,4,5; "running build
  #                                         holds"; paused row 2; "outlives";
  #                                         image row 1; one-off image row 2
  #     ...: drop `b.interceptible = false` use rows 1,2,3,4,5; "running build
  #                                         holds"; paused rows 1 AND 2
  #     CleanBuildImageResourceCaches:
  #       drop `birc.job_id IS NULL`        one-off image rows 1 and 2
  #     CleanUpInvalidCaches: no-op         "running build holds"; paused row 2;
  #                                         "outlives"; image row 1; one-off
  #                                         image row 2
  #       ...: uses arm protects nothing    "running build holds"; paused row 2
  #       ...: image arm protects nothing   "outlives"; image row 1; one-off
  #                                         image row 2
  #       ...: sha256 inputs ignore paused  paused row 2
  #
  #   build.go
  #     Finish no longer clears
  #       interceptible on failure          use row 5, ALONE
  #     image delete: `<` -> `<=`           image rows 1 and 3
  #     image delete: drop job_id           image row 3
  #     image delete: drop the success
  #       guard                             image row 2
  #
  #   resource_config_factory.go
  #     CleanUnreferencedConfigs: no-op     "nothing references it"; "spared
  #                                         until the grace period"
  #     ...: drop the grace period          "spared until the grace period"
  #     ...: resources arm protects nothing "nothing references it"
  #     ...: resource_types arm likewise    "nothing references it"
  #     ...: resource_caches arm likewise   "nothing references it"
  #     ...: report the FK violation        "a check session pins"
  #
  # What that run corrected. Two notes in this file were wrong when written:
  # the day-old image scenario's sibling could not have caught the job_id
  # predicate until the sibling build was itself aged past the cutoff, and the
  # `<` -> `<=` mutation reddens two rows rather than one. Both were found by
  # running the mutation, not by reading the code — which is the same lesson
  # MIGRATION-EVIDENCE records for the earlier suites.
  #
  # The three arms of the cache-collector chain mean a mutation low in the
  # chain reddens scenarios high in it: CleanUsesForFinishedBuilds doing
  # nothing takes five cache scenarios down with the four use rows. That is
  # honest — those scenarios really do depend on it — but it means a red run
  # should be read from the shortest chain upward.
  #
  # ==========================================================================
  # The cost
  # ==========================================================================
  #
  # 19 scenarios, so 19 sequential template-database clones. Measured on this
  # file: 6.1 s wall clock for the whole run, against the pilot's model of
  # ~3.9 s fixed plus ~113 ms a scenario, which predicts 6.0 s. The 25 ginkgo
  # specs this replaces cost ~3.5 s serially at the pilot's measured ~139 ms a
  # spec. The clone is not a cost brine adds: gc_suite_test.go calls
  # CreateTestDBFromTemplate in a plain BeforeEach, so every one of those 25
  # specs already paid for one.
