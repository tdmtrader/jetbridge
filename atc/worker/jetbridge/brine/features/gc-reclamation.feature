Feature: Reclaiming a worker's rows, and the workers themselves

  Garbage collection is the one subsystem where the only thing worth asserting
  is what is left. Every scenario below runs a real sweep against real
  PostgreSQL and then says which rows survived and which are gone. There is no
  call count anywhere in this file, and there was none in the two suites it
  came from either — atc/gc/destroyer_test.go and atc/gc/worker_collector_test.go
  already assert outcomes, which is why they were picked to go first.

  Two consumers care. The jetbridge reaper calls the destroyer once per sweep
  with the handles its worker still reports, and a row reclaimed a moment too
  early makes a live container invisible to the scheduler while a row never
  reclaimed leaks forever. The worker collector removes ephemeral workers that
  stopped heartbeating, and removing the wrong one throws away a worker that
  was merely stalled.

  Source: atc/gc/destroyer_test.go (14 cases) and atc/gc/worker_collector_test.go
  (4 cases), all 18 migrated, into 11 scenarios. Neither carried requirement
  identifiers, so there are no tags.

  This file is a PILOT, and its size is the measurement. brine's Postgres plane
  gives every scenario its own database cloned from a template, sequentially,
  on one suite-scoped postmaster, so 11 scenarios is 11 clones — the number at
  the foot of this file is what has to be multiplied out before the remaining
  114 scenarios are written. That is also why containers and volumes share a
  scenario wherever the rule under test is the same rule: destroyer.go
  implements DestroyContainers and DestroyVolumes as two copies of one
  function, so a scenario that sweeps both and asserts both catches a change to
  either, and splitting them would double the clone cost to restate the same
  sentence twice.

  # ==========================================================================
  # Reclaiming a worker's containers and volumes
  # ==========================================================================

  # The central rule, and the one that reads backwards until you know the
  # protocol: the handles passed to the destroyer are the ones to KEEP. They
  # are what the worker still reports (container_repository.go names the
  # parameter handlesToIgnore); anything else in the destroying state on that
  # worker is deleted. So each scenario makes two and keeps one.
  #
  # Reddened by: DestroyContainers returning nil before it reaches the
  # repository — the gone row survives. Or by RemoveDestroyingContainers'
  # NotEq on the handle becoming an Eq — then the kept row dies and the gone
  # one survives, and both halves of this scenario fail at once.
  Scenario: The destroying rows a worker no longer reports are reclaimed, and the ones it reports are kept
    Given a destroyer sweeping a worker's containers and volumes
    And the container "kept-container" on that worker is being destroyed
    And the container "gone-container" on that worker is being destroyed
    And the volume "kept-volume" on that worker is being destroyed
    And the volume "gone-volume" on that worker is being destroyed
    When the worker reports that it still holds the container "kept-container" and the volume "kept-volume"
    Then the sweep completed without error
    And the container "gone-container" has been reclaimed
    And the volume "gone-volume" has been reclaimed
    And the container "kept-container" survived the sweep
    And the volume "kept-volume" survived the sweep

  # An empty report is a report. The worker answered, and it holds nothing, so
  # everything it was still tracking is garbage. The neighbour is here because
  # "delete every destroying row" and "delete every destroying row belonging to
  # this worker" are only distinguishable when someone else has one — and a
  # sweep that reached across workers would take rows a live worker is still
  # using.
  #
  # Reddened by: the nil guard in destroyer.go widening to
  # `if len(currentHandles) == 0 { return nil }`, which is the tidy-looking
  # change this scenario exists to stop — the gone rows would then survive
  # forever, because a worker with nothing left to report never reclaims
  # anything. The neighbour half is reddened one layer down, by dropping the
  # worker_name predicate from RemoveDestroyingContainers.
  Scenario: A worker that reports holding nothing loses its own destroying rows and nobody else's
    Given a destroyer sweeping a worker's containers and volumes
    And the container "gone-container" on that worker is being destroyed
    And the volume "gone-volume" on that worker is being destroyed
    And the container "neighbours-container" on a second worker is being destroyed
    And the volume "neighbours-volume" on a second worker is being destroyed
    When the worker reports that it holds nothing at all
    Then the sweep completed without error
    And the container "gone-container" has been reclaimed
    And the volume "gone-volume" has been reclaimed
    And the container "neighbours-container" survived the sweep
    And the volume "neighbours-volume" survived the sweep

  # ...and the distinction the scenario above is only half of. A nil handle
  # list means the worker did not report at all — the reaper could not reach
  # it, or has not asked yet. That is not the same as a worker that reported
  # nothing, and treating it as the same is destructive rather than merely
  # wrong: squirrel renders NotEq over an EMPTY slice as `(1=1)` — a typed nil
  # boxed in an interface is not == nil, so it never reaches the IS NOT NULL
  # arm; it falls to the empty-list arm, which for NotEq is sqlTrue. Checked
  # against squirrel v1.5.4 in the module cache rather than assumed. The
  # consequence is the same and the point is sharper: nil and []string{} render
  # BYTE-IDENTICAL SQL, so destroyer.go's `currentHandles == nil` guard is the
  # only thing separating a worker that reported nothing from one whose report
  # never arrived. Which is why these are two scenarios and not one. So
  # a sweep that dropped the guard would delete every destroying row on a
  # worker whose report simply failed to arrive.
  #
  # Reddened by: deleting `if currentHandles == nil { return nil }` from
  # DestroyContainers or DestroyVolumes. Both survivors below then disappear.
  Scenario: A report that never arrived is not a report of nothing
    Given a destroyer sweeping a worker's containers and volumes
    And the container "survivor-container" on that worker is being destroyed
    And the volume "survivor-volume" on that worker is being destroyed
    When no report from the worker ever arrives
    Then the sweep completed without error
    And the container "survivor-container" survived the sweep
    And the volume "survivor-volume" survived the sweep

  # A sweep with no worker name is a caller bug, and the destroyer refuses it
  # rather than passing it down. Without the guard the delete still runs, with
  # `worker_name = ''`, matches nothing, and reports success — so the caller
  # would be told a sweep happened when none did, which is the failure mode
  # that hides a broken reaper for as long as you care to leave it running.
  #
  # Reddened by: deleting the `if workerName == ""` guard from either method.
  # The rows still survive — that is the point — but the refusal is gone.
  Scenario: A sweep that cannot name its worker is refused and reclaims nothing
    Given a destroyer sweeping a worker's containers and volumes
    And the container "survivor-container" on that worker is being destroyed
    And the volume "survivor-volume" on that worker is being destroyed
    When the destroyer is asked to reclaim for a worker with no name
    Then reclaiming the containers was refused, saying "worker-name-must-be-provided"
    And reclaiming the volumes was refused, saying "worker-name-must-be-provided"
    And the container "survivor-container" survived the sweep
    And the volume "survivor-volume" survived the sweep

  # The database really is gone here: the destroyer holds repositories over a
  # connection that has been closed, which is how this fails in production when
  # the ATC loses PostgreSQL mid-sweep. A destroyer that swallowed the error
  # would report a clean sweep to a reaper that then tells the worker its
  # containers are reclaimed when the rows are all still there.
  #
  # Reddened by: `return err` becoming `return nil` after the repository call
  # in DestroyContainers or DestroyVolumes.
  Scenario: A sweep the database refuses is reported as a failure, not as a clean pass
    Given a destroyer sweeping a worker's containers and volumes
    And the container "survivor-container" on that worker is being destroyed
    And the volume "survivor-volume" on that worker is being destroyed
    And the database behind the destroyer has gone away
    When the worker reports that it holds nothing at all
    Then reclaiming the containers was refused, saying "closed"
    And reclaiming the volumes was refused, saying "closed"
    And the container "survivor-container" survived the sweep
    And the volume "survivor-volume" survived the sweep

  # The other half of the destroyer's surface: the reaper asks which volumes
  # are waiting, then goes and deletes them off the node before letting the
  # rows go. Two things have to be true of that answer, and only both together
  # are worth anything — it must name every volume actually being destroyed,
  # or the node leaks disk, and it must name nothing else, or the reaper
  # deletes data a running step is still reading.
  #
  # Reddened by: FindDestroyingVolumesForGc returning nil rather than what the
  # repository handed it — the count drops to 0. The negatives are reddened by
  # widening the repository's state or worker_name predicate.
  Scenario: Only this worker's volumes, and only the ones being destroyed, are offered for reclamation
    Given a destroyer sweeping a worker's containers and volumes
    And the volume "first-to-go" on that worker is being destroyed
    And the volume "second-to-go" on that worker is being destroyed
    And the volume "still-in-use" on that worker is still in use
    And the volume "neighbours-volume" on a second worker is being destroyed
    When the destroyer is asked which volumes are waiting to be reclaimed
    Then 2 volumes are waiting to be reclaimed
    And the volume "first-to-go" is waiting to be reclaimed
    And the volume "second-to-go" is waiting to be reclaimed
    And the volume "still-in-use" is not waiting to be reclaimed
    And the volume "neighbours-volume" is not waiting to be reclaimed

  # A failed read and an empty answer are the same value and opposite facts.
  # If the error is dropped the reaper is told there is nothing to reclaim,
  # believes it, and moves on — and the volumes it was supposed to collect are
  # never offered again by that sweep. So the refusal is the assertion, and the
  # empty answer beside it is only there to show that the empty answer alone
  # would not have distinguished anything.
  #
  # Reddened by: FindDestroyingVolumesForGc returning the handles and a nil
  # error when the repository failed.
  Scenario: A read the database refuses is reported, not answered as nothing to reclaim
    Given a destroyer sweeping a worker's containers and volumes
    And the volume "first-to-go" on that worker is being destroyed
    And the database behind the destroyer has gone away
    When the destroyer is asked which volumes are waiting to be reclaimed
    Then asking which volumes are waiting was refused, saying "closed"
    And 0 volumes are waiting to be reclaimed

  # DISPOSITION — "returns nothing when the worker has no destroying volumes"
  # is not a scenario of its own. Its content is that a volume which is not
  # being destroyed is not offered, and that is the "still-in-use" row above,
  # asserted alongside two volumes that ARE offered so a lookup answering
  # nothing at all could not pass. What is left over is the difference between
  # a nil slice and an empty one, which the reaper ranges over identically.

  # ==========================================================================
  # Reclaiming the workers themselves
  # ==========================================================================

  # Two conditions, and both matter, because each one alone would throw away a
  # worker that is fine. An ephemeral worker is one that will not come back —
  # it is disposable by construction — so once it stops heartbeating its row is
  # garbage. A worker that is still heartbeating is doing its job whatever else
  # is true of it. And a non-ephemeral worker that stopped heartbeating is
  # STALLED, not gone: it keeps its row, its containers stay addressable, and
  # an operator gets to look at it. Deleting it would silently discard a
  # worker's whole inventory on a network blip.
  #
  # Reddened by: the collector skipping DeleteUnresponsiveEphemeralWorkers
  # entirely, which fails the first row. The two survival rows are pinned one
  # layer down, in atc/db/worker_lifecycle.go — the collector's whole content
  # is which lifecycle call it makes, and the selectivity lives in that call's
  # `WHERE ephemeral AND expires < NOW()`. Dropping either predicate reddens
  # exactly one of the rows below and neither of the others.
  #
  # Each row is its own database, so the survivors cannot vouch for the
  # reclaimed one: "has been reclaimed" is an absence, and an absence passes on
  # an empty table. A second worker nobody expects to touch is registered
  # alongside, so a fixture that quietly stopped inserting fails on the
  # bystander instead of passing on the vacuum. The ginkgo source has the same
  # hole — this is not a regression it introduced, just one worth not carrying
  # across.
  Scenario Outline: A worker is reclaimed only when it is both ephemeral and unresponsive — <case>
    Given a collector for workers that have stopped heartbeating
    And the worker "<name>" is <state>
    And the worker "bystander" is persistent and still heartbeating
    When the collector sweeps for unresponsive workers
    Then the collector completed without error
    And the worker "<name>" <fate>
    And the worker "bystander" is still registered

    Examples:
      | case                     | name               | state                                  | fate                |
      | disposable and gone      | expired-ephemeral  | ephemeral and has stopped heartbeating | has been reclaimed  |
      | disposable but alive     | live-ephemeral     | ephemeral and still heartbeating       | is still registered |
      | stalled, not disposable  | expired-persistent | persistent and has stopped heartbeating | is still registered |

  # A collector that reports success on a failed delete is worse than one that
  # never ran: the component runner marks the pass done and waits a full
  # interval before trying again, so a database blip becomes a whole cycle in
  # which no dead worker is collected and nothing says so. The worker below is
  # registered over a connection that still works, so its survival shows the
  # sweep really did reclaim nothing.
  #
  # Reddened by: `return err` becoming `return nil` after
  # DeleteUnresponsiveEphemeralWorkers fails in worker_collector.go — note that
  # the very next call in that function, GetWorkerStateByName, already treats
  # its own failure that way on purpose, so the two branches sit three lines
  # apart and only one of them may swallow.
  Scenario: A collector that cannot reach the database reports the failure rather than a clean pass
    Given a collector for workers that have stopped heartbeating
    And the worker "expired-ephemeral" is ephemeral and has stopped heartbeating
    And the database the collector reads has gone away
    When the collector sweeps for unresponsive workers
    Then the collector's sweep was refused, saying "closed"
    And the worker "expired-ephemeral" is still registered

  # ==========================================================================
  # The measurement
  # ==========================================================================
  #
  # 11 scenarios, so 11 sequential template-database clones. What that costs,
  # measured on this file rather than reasoned about (n=5 each, this machine,
  # other suites running alongside):
  #
  #   this file, 1 scenario   (--line 46)   3,997 ms mean
  #   this file, 11 scenarios               5,123 ms mean
  #   -> marginal cost of a scenario        ~113 ms
  #   -> fixed cost (postmaster + dispose)  ~3.9 s, paid once per run
  #
  # For comparison, `go test ./atc/gc/` — the whole ginkgo suite — 109 leaf specs, 91 It plus 18 Entry rows under the one DescribeTable — run
  # serially — is 15.1 s, or ~139 ms a spec. That is the number the schedule
  # risk was really about, and it does NOT say what it was assumed to say: the
  # ginkgo suite ALREADY clones a template database per spec. gc_suite_test.go
  # calls CreateTestDBFromTemplate in a plain BeforeEach, so every one of its 98
  # specs pays the same clone brine pays. Cloning is not a cost brine adds.
  #
  # So the whole of atc/gc — 109 specs, ~125 scenarios — projects to ~14 s of
  # database time in brine against ~15 s serially in ginkgo. The real
  # difference is the one left over: ginkgo runs 9-way and brine's Postgres
  # plane is one postmaster, so what ginkgo finishes in ~3 s of wall clock
  # brine finishes in ~14 s. That is a parallelism gap of about 4.5x on this
  # package, not the sequential-clone catastrophe the risk was written up as,
  # and it is the same gap already measured at 2.1x on the first slice.
