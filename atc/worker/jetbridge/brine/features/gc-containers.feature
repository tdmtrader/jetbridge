Feature: Collecting the containers and volumes a finished build left behind

  The container collector and the volume collector are the two components that
  decide when a row stops being a record of something real and becomes garbage.
  They run on a timer inside the web node and nothing else reclaims what they
  miss. A container row never marked destroying is never offered to the reaper,
  so the container sits on the worker until the node is replaced; a volume row
  marked destroying too early hands the reaper a disk a running step is still
  reading. Both mistakes are silent — the build that suffers is the next one.

  Both suites migrated here already ran against real PostgreSQL and already
  asserted outcomes: the state column after the sweep, or whether the row is
  still there. There was no double to remove and no traffic to stop counting,
  so every scenario below has to be justified by its sentence alone.

  What the migration does add is siblings. Nine of the sixteen ginkgo specs
  asserted a row's fate with no row beside it that the same sweep had to treat
  DIFFERENTLY — the four orphan specs, the two missing-container specs, the
  failed-container spec, the hijacked-check spec, and the failed-volume spec,
  each of which built exactly the rows it was about and swept an otherwise
  empty database. A collector that marked everything destroying passed six of
  them; a collector that did nothing at all passed the other three. The
  remaining seven already had a discriminator, which is why the excess-check,
  missing-volume and orphaned-volume specs read as they do. Here every
  scenario has one, in the same database and under the same sweep.

  Source: atc/gc/container_collector_test.go (13 specs) and
  atc/gc/volume_collector_test.go (3 specs). 15 of the 16 are here in 10
  scenarios; the sixteenth is dispositioned at the foot of the file. Neither
  suite carried requirement identifiers, so there are no tags.

  # ==========================================================================
  # Containers a build has finished with
  # ==========================================================================

  # "Orphaned" is a property of the BUILD, not the container: a container is
  # collectable once its build stops being interceptible, which is the flag the
  # ATC clears when nobody can `fly hijack` into it any more. On top of that
  # sits a second rule that has nothing to do with the build — a container
  # someone hijacked recently is a shell an operator is sitting in, and pulling
  # it out from under them is how a debugging session dies mid-sentence. So two
  # reasons to reclaim and two reasons not to, and all four are in one database
  # under one sweep because that is the only arrangement in which "this one and
  # not that one" is an assertion rather than a coincidence.
  #
  # Reddened by: dropping `b.interceptible = false` from the second arm of
  # FindOrphanedContainers' predicate — "live-build" then dies, which is a
  # container belonging to a build a user is currently watching. Or by dropping
  # the `time.Since(LastHijack()) > hijackContainerGracePeriod` guard in
  # cleanupOrphanedContainers, which kills "fresh-hijack". Or by inverting that
  # comparison, which spares both of the two that should go and takes the one
  # that should stay — three of the four rows below move at once.
  Scenario: A container is reclaimed once its build is finished, unless somebody is still inside it
    Given a container collector sweeping a real database
    And the container "never-hijacked" belongs to a build that has finished
    And the container "stale-hijack" belongs to a build that has finished
    And the container "stale-hijack" was last hijacked an hour ago
    And the container "fresh-hijack" belongs to a build that has finished
    And the container "fresh-hijack" was last hijacked a moment ago
    And the container "live-build" belongs to a build that is still running
    When the container collector sweeps
    Then the container collector completed without error
    And the container "never-hijacked" is now destroying
    And the container "stale-hijack" is now destroying
    And the container "fresh-hijack" is still created
    And the container "live-build" is still created

  # A container the worker has stopped reporting is not deleted on sight. The
  # ATC stamps missing_since when a worker's report omits a handle it holds a
  # row for, and the grace period exists because a report can be late, partial,
  # or lost — deleting on the first missed beat throws away the ATC's only
  # record of a container that is still running, and the row is what the reaper
  # later uses to destroy it. So the deletion and the reprieve are the same
  # rule seen from two sides, and both are asserted here in one database: the
  # survivor is what makes the deletion mean something, since an absence is
  # satisfied by an empty table.
  #
  # The third row is not from the ginkgo suite and is the one addition this
  # file makes. RemoveMissingContainers also refuses to touch a container whose
  # WORKER is stalled, and that predicate had no spec at all. A stalled worker
  # is one the ATC has lost contact with, not one that has gone: its containers
  # are still there. Deleting the rows makes them unreclaimable — the ATC
  # forgets the containers exist, so it never asks anyone to destroy them, and
  # they occupy the node until it is rebuilt. It costs no clone to assert here
  # because the sweep is the same sweep.
  #
  # Reddened by: replacing the `NOW() - missing_since > interval` comparison in
  # RemoveMissingContainers with a bare `missing_since IS NOT NULL` — that is,
  # deleting on the first missed report — which takes "just-missing". Or by
  # dropping `w.state != 'stalled'`, which takes "on-stalled-worker". Or by
  # dropping the whole call from Run(), which strands "long-missing".
  Scenario: A container the worker stopped reporting is deleted once the grace period passes, but not while the worker itself is stalled
    Given a container collector sweeping a real database
    And the container "long-missing" belongs to a build that is still running
    And the worker stopped reporting the container "long-missing" an hour ago
    And the container "just-missing" belongs to a build that is still running
    And the worker stopped reporting the container "just-missing" a moment ago
    And the container "on-stalled-worker" sits on a worker that has stalled
    And the worker stopped reporting the container "on-stalled-worker" an hour ago
    When the container collector sweeps
    Then the container collector completed without error
    And the container "long-missing" has been removed from the database
    And the container "just-missing" is still in the database
    And the container "on-stalled-worker" is still in the database

  # A container that never made it out of `creating` — the worker refused it,
  # the image would not pull, the pod never scheduled — is garbage the moment
  # it is recorded as failed, with no grace period and no owner to consult.
  # The neighbour is the whole scenario: DestroyFailedContainers is one UPDATE
  # whose only discriminator is `WHERE state = 'failed'`, and without a healthy
  # container beside it a sweep that marked the entire table destroying would
  # be indistinguishable from the correct one. Every running build in the
  # deployment would then have its containers reclaimed underneath it.
  #
  # Reddened by: dropping the `Where(sq.Eq{"state": failed})` from
  # DestroyFailedContainers, which takes "live-build"; or by removing the call
  # from Run(), which strands "failed-container" in a state nothing else
  # collects.
  Scenario: A container that failed to be created is marked for destruction, and nothing else is
    Given a container collector sweeping a real database
    And the container "failed-container" failed while it was being created
    And the container "live-build" belongs to a build that is still running
    When the container collector sweeps
    Then the container collector completed without error
    And the container "failed-container" is now destroying
    And the container "live-build" is still created

  # Check containers are the one kind nothing else ever reclaims: a resource's
  # check container has no build to finish and no worker report to go missing
  # from, so without a cap the ATC accumulates one per check per resource
  # forever. The cap keeps the newest — a check is about to reuse it — and
  # takes the rest, EXCEPT one an operator has hijacked, because a resource
  # check that will not pass is exactly what somebody debugs by sitting inside
  # its container.
  #
  # Three containers for one resource rather than the ginkgo suite's two, so
  # both halves of that sentence are one sweep: the newest is kept by the cap,
  # the middle is beyond it and goes, and the oldest is beyond it too and stays
  # only because of the hijack. A sweep that ignored the cap and a sweep that
  # ignored the hijack are different mutations and each moves exactly one row.
  #
  # Reddened by: raising maxCheckContainersPerResource above 1, which spares
  # "excess-check"; or dropping the
  # `(c.last_hijack IS NULL OR NOW() - c.last_hijack > $2)` clause from
  # DestroyExcessCheckContainers, which takes "hijacked-check"; or flipping the
  # window's `ORDER BY id DESC` to ASC, which keeps the oldest and destroys the
  # newest — the one the next check was about to reuse.
  Scenario: Only the newest check container for a resource survives the cap, and a hijacked one survives it too
    Given a container collector sweeping a real database
    And the resource "some-resource" has the check containers "hijacked-check", "excess-check" and "newest-check", oldest first
    And the container "hijacked-check" was last hijacked a moment ago
    When the container collector sweeps
    Then the container collector completed without error
    And the container "newest-check" is still created
    And the container "excess-check" is now destroying
    And the container "hijacked-check" is still created

  # ==========================================================================
  # One sweep, four independent jobs
  # ==========================================================================
  #
  # containerCollector.Run does four things and accumulates their failures into
  # one multierror instead of returning at the first. That is the property
  # these three scenarios are about, and it is worth more than it looks: the
  # collector runs on a timer, so a step that fails EVERY pass — a check-
  # container cap deadlocking against a live check, a delete blocked by a lock
  # — would, under a short-circuiting Run, permanently stop every step after
  # it. Orphaned containers would accumulate for as long as the deadlock
  # lasted, and the only symptom is a containers table that grows.
  #
  # Each scenario disables one step and then asserts the OUTCOMES of the other
  # three on real rows: nothing here counts calls or inspects the error text.
  # The failure is injected by wrapping the real repository and failing exactly
  # one of its methods; the other three go to PostgreSQL as usual. That is a
  # narrower version of the pilot's "the database has gone away" and it is the
  # only lever that works, because a closed connection fails all four steps at
  # once and PostgreSQL will not fail just one of them on request: the FK from
  # volumes to containers is ON DELETE SET NULL, so even a volume attached to a
  # missing container cannot make its deletion fail. See the note in
  # steps/gc_containers.go on why this wrapper is admitted and what it is not.
  #
  # The three positions are three scenarios rather than an outline because the
  # mutation each one catches is position-specific: a `return errs` added
  # inside the second `if err != nil` block only ever executes when the SECOND
  # step failed, so only the scenario that fails the second step can redden it.
  # An outline would also have to vary its Then rows, which is the shape rule 4
  # is about.

  # Reddened by: `return errs` after the orphaned-containers block in Run().
  Scenario: A collector that cannot look up orphans still collects the failed, the missing and the excess
    Given a container collector sweeping a real database
    And the container "failed-container" failed while it was being created
    And the container "missing-container" belongs to a build that is still running
    And the worker stopped reporting the container "missing-container" an hour ago
    And the resource "some-resource" has the check containers "excess-check" and "newest-check", oldest first
    And the collector cannot look up orphaned containers
    When the container collector sweeps
    Then the sweep reported the failure rather than a clean pass
    And the container "failed-container" is now destroying
    And the container "missing-container" has been removed from the database
    And the container "excess-check" is now destroying
    And the container "newest-check" is still created

  # Reddened by: `return errs` after the failed-containers block in Run().
  Scenario: A collector that cannot destroy failed containers still collects the orphaned, the missing and the excess
    Given a container collector sweeping a real database
    And the container "orphaned-container" belongs to a build that has finished
    And the container "missing-container" belongs to a build that is still running
    And the worker stopped reporting the container "missing-container" an hour ago
    And the resource "some-resource" has the check containers "excess-check" and "newest-check", oldest first
    And the collector cannot destroy failed containers
    When the container collector sweeps
    Then the sweep reported the failure rather than a clean pass
    And the container "orphaned-container" is now destroying
    And the container "missing-container" has been removed from the database
    And the container "excess-check" is now destroying
    And the container "newest-check" is still created

  # Reddened by: `return errs` after the missing-containers block in Run() —
  # the block whose failure is the likeliest of the four in production, since
  # it is the only DELETE and the only one that can lose a lock race.
  Scenario: A collector that cannot delete missing containers still collects the orphaned, the failed and the excess
    Given a container collector sweeping a real database
    And the container "orphaned-container" belongs to a build that has finished
    And the container "failed-container" failed while it was being created
    And the resource "some-resource" has the check containers "excess-check" and "newest-check", oldest first
    And the collector cannot delete missing containers
    When the container collector sweeps
    Then the sweep reported the failure rather than a clean pass
    And the container "orphaned-container" is now destroying
    And the container "failed-container" is now destroying
    And the container "excess-check" is now destroying
    And the container "newest-check" is still created

  # ==========================================================================
  # Volumes
  # ==========================================================================

  # The same grace period as containers, over a different query — volumes are
  # deleted by a recursive CTE that takes a missing volume's children with it,
  # because a volume's parent cannot be deleted while the child still points at
  # it. The third row here is the one the container side has no equivalent of:
  # a volume the worker is still reporting has a NULL missing_since, and it is
  # here because NULL is where this predicate is fragile.
  #
  # A note on what does NOT redden it, checked rather than assumed. The query
  # opens `missing_since IS NOT NULL and NOW() - missing_since > $1`, and that
  # first clause is dead: `NOW() - NULL` is NULL, `NULL > interval` is NULL,
  # and a NULL row is not selected. Deleting it changes nothing, so it is not
  # the mutation this row is guarding against — writing that down here rather
  # than naming a reddening that could not happen.
  #
  # Reddened by: making the comparison total over NULL, which is the shape a
  # well-meant NULL fix takes — `COALESCE(NOW() - missing_since, INTERVAL '99
  # years') > $1` reads as tidying up an untidy predicate and quietly means "a
  # volume nobody has ever reported missing has been missing forever", which
  # deletes "still-reported" while it is in use. Or by dropping the
  # grace-period comparison, which takes "just-missing". Or by removing the
  # call from Run(), which strands "long-missing" and leaks a disk on the node
  # for good, since nothing else ever revisits a volume the worker has stopped
  # mentioning.
  Scenario: A volume the worker stopped reporting is deleted once the grace period passes
    Given a volume collector sweeping a real database
    And the volume "long-missing" is held by a container that is still around
    And the worker stopped reporting the volume "long-missing" an hour ago
    And the volume "just-missing" is held by a container that is still around
    And the worker stopped reporting the volume "just-missing" a moment ago
    And the volume "still-reported" is held by a container that is still around
    When the volume collector sweeps
    Then the volume collector completed without error
    And the volume "long-missing" has been removed from the database
    And the volume "just-missing" is still in the database
    And the volume "still-reported" is still in the database

  # A volume that failed to be created holds no data anybody wants and is
  # deleted outright rather than marked destroying — there is nothing on the
  # node to reclaim first. DestroyFailedVolumes is a DELETE whose only
  # discriminator is `v.state = 'failed'`, so the healthy volume beside it is
  # what separates "deletes the failed ones" from "deletes the volumes", and
  # the difference between those two is every cache and every step input in the
  # deployment.
  #
  # Reddened by: widening or dropping the state predicate in
  # DestroyFailedVolumes, which takes "healthy-volume"; or removing the call
  # from Run(), which leaves "failed-volume" behind — a row in a state no other
  # sweep looks at.
  Scenario: A volume that failed to be created is deleted, and a healthy one beside it is not
    Given a volume collector sweeping a real database
    And the volume "failed-volume" failed while it was being created
    And the volume "healthy-volume" is held by a container that is still around
    When the volume collector sweeps
    Then the volume collector completed without error
    And the volume "failed-volume" has been removed from the database
    And the volume "healthy-volume" is still in the database

  # This is how a volume becomes garbage in the ordinary case: not by failing
  # and not by going missing, but by outliving the container that held it. When
  # the container row is deleted the volume's container_id becomes NULL — that
  # is the FK's ON DELETE SET NULL doing it, not the collector — and a volume
  # with no container, no cache, no base type and no task cache is attached to
  # nothing. Marking it destroying is the step that eventually frees the disk.
  #
  # Marked destroying, not deleted: the bytes are still on the node, so the row
  # has to survive long enough for the reaper to be told about it. That is the
  # hand-off gc-reclamation.feature picks up.
  #
  # The held volume beside it is the discriminator, and it is the one that
  # would cost real data: GetOrphanedVolumes decides on seven NULL columns at
  # once, and a sweep that lost `v.container_id IS NULL` would mark every
  # volume of every running step for destruction.
  #
  # Reddened by: dropping `v.container_id IS NULL` from GetOrphanedVolumes,
  # which takes "held-volume"; or removing markOrphanedVolumesAsDestroying from
  # Run(), which leaves "orphaned-volume" created forever and the disk it names
  # never reclaimed. The query's `w.state = 'running'` clause is why the worker
  # here is registered running, but nothing below reddens on it — a scenario
  # for it would need a second worker in another state, and it belongs with the
  # stalled-worker row in the missing-containers scenario rather than here.
  Scenario: A volume whose container is gone is marked for destruction, and one still held is left alone
    Given a volume collector sweeping a real database
    And the volume "orphaned-volume" is held by a container that has since been destroyed
    And the volume "held-volume" is held by a container that is still around
    When the volume collector sweeps
    Then the volume collector completed without error
    And the volume "orphaned-volume" is now destroying
    And the volume "held-volume" is still created

  # ==========================================================================
  # Disposition
  # ==========================================================================
  #
  # "succeeds with nothing to collect" — `Expect(collector.Run(...)).To(Succeed())`
  # against an empty database — is not a scenario here. It asserts no outcome:
  # nothing survived and nothing was reclaimed, because there was nothing. Rule
  # 3 asks what change in the collector reddens it, and there is no honest
  # answer; each of the four steps is a single statement that matches zero rows
  # and returns zero, and the two that iterate a result set iterate an empty
  # one. The ten scenarios above all assert the sweep completed without error
  # over databases that do contain rows, which is the same claim under a
  # stricter precondition.
  #
  # 10 scenarios, so 10 template-database clones on top of the pilot's 11.
  # Measured on this file rather than projected: 5,050 ms for the whole file
  # against the pilot's ~3.9 s fixed cost (postmaster plus dispose, paid once),
  # so ~1.15 s of scenario time, or ~115 ms a scenario — within noise of the
  # 113 ms the pilot measured. A second run with three other agents building
  # and running on the same machine took 6,220 ms, which is what contention
  # costs and not what a scenario costs.
  #
  # Every scenario above has been reddened by the mutation named against it,
  # one mutation at a time against real production code, reverted after each:
  #
  #   scenario 1  hijack comparison inverted           -> 3 of its 4 rows moved
  #   scenario 2  grace period -> missing_since IS NOT NULL, and separately
  #               `w.state != 'stalled'` -> 1=1         -> 1 row each
  #   scenario 3  failed-state predicate widened to all rows
  #   scenario 4  cap raised to 2, and separately the hijack clause defeated
  #   scenarios 5-7  `return errs` added to each of the three `if err != nil`
  #               blocks in Run() — each reddened EXACTLY its own scenario and
  #               no other, which is the evidence that these are three
  #               scenarios and not one outline
  #   scenario 8  COALESCE(NOW() - missing_since, INTERVAL '99 years')
  #   scenario 9  DestroyFailedVolumes pointed at created volumes
  #   scenario 10 `v.container_id IS NULL` dropped from GetOrphanedVolumes
