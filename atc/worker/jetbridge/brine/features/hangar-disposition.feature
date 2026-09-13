Feature: Which outcome a finished producer selects, and what the build is told

  Sequential and observable, which is why these are here rather than in Go: an
  arbiter picks one member of a closed vocabulary, and an announcement is a row
  production writes and production reads back — the same shape as the exit
  annotation ../step-closing.feature reads.

  `the capture settles` is the CONTROL PLANE, driven to quiescence: the real
  repository over this scenario's real PostgreSQL, the real coordinator, and
  jetbridge's own client against the real output daemon. Nothing here chooses a
  transition and nothing here counts a call.

  Kept in Go and cited rather than duplicated: "conflicting replay fails" (a
  race), "deadline expiry alone releases nothing" (a clock, and convention 9
  forbids a chain that waits), and every crash half — before/after a commit, an
  acknowledgement or a registration — which is the class brine cannot interpose
  on. Those are atc/hangaroutput/transitions_test.go (every crash half as a
  state) and atc/hangaroutput/ambiguity_test.go (every lost answer).

  # Reddened by: CommitCaptureReservation committing the reservation already
  # `resolved` — Stage 2 creates a DISTINCT unresolved reservation and resolves
  # nothing, so a Stage 2 that resolves has skipped the canonicalization the
  # scope and digest come from. It reddens this scenario at its FIRST line
  # (the settle stops at the publish, which refuses to create an object with no
  # committed logical resolution) and leaves the no_capture and cancellation
  # scenarios below green — which is what tells a Stage 2 that does too much
  # apart from an arbiter that picked the wrong branch.
  @HOP-5
  Scenario: A successful finish selects capture and commits the producer checkpoint with an unresolved reservation
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the disposition is "capture"
    And the producer checkpoint is committed with an unresolved reservation

  # One scenario, both halves, because "selects no_capture" passes on a
  # coordinator that never selects capture at all. The failing producer is
  # asserted first and the succeeding one second, over a new build of the same
  # step — which is the only honest way to run the same producer twice.
  @HOP-2 @HOP-5
  Scenario: A failed producer selects no_capture, and the same producer succeeding selects capture
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the step fails
    And the capture settles
    Then the disposition is "no_capture"
    And the no_capture reason is "authoritative_non_success"
    When a new build of the same step is admitted
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    Then the disposition is "capture"

  # Reddened by: the arbiter asking about the producer's outcome BEFORE the
  # cancellation — which is how a cancellation-selected outcome reaches
  # no_capture. The two pre_reservation_cancel scenarios below redden on their
  # one disposition line and this one stays green, which is what tells a branch
  # confusion apart from a broken arbiter.
  @HOP-5 @HOP-9
  Scenario: no_capture completes in two halves, and the outcome stays pending between them
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step fails
    When the capture settles
    Then the outcome was pending between the two halves
    And the disposition is "no_capture"
    And the produced output is still on the node

  # The producer FAILS and is then cancelled, and the "never" in the title is
  # what that is for: Req 11 says cancellation before Stage 2 selects only
  # pre_reservation_cancel, and the branch it must never reach is no_capture,
  # which is what an authoritative non-success selects. Without a non-success
  # witness beside the cancellation there is nothing for the branch to be
  # confused WITH, and an arbiter that asked the producer's outcome first would
  # answer pre_reservation_cancel here too — pinning "cancellation is honoured"
  # rather than the ordering the title names.
  #
  # Reddened by: the arbiter asking about the producer's outcome BEFORE the
  # cancellation. The no_capture scenarios above stay green, which is what
  # tells a branch confusion apart from a broken arbiter.
  @HOP-11
  Scenario: Cancellation before Stage 2 selects pre_reservation_cancel, never no_capture
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the step fails
    And the step is cancelled before Stage 2
    And the capture settles
    Then the disposition is "pre_reservation_cancel"

  # An absence, and the held form directly above it is its control: a
  # cancellation whose hold was never acknowledged still has bytes on a node,
  # because the incarnation is reserved before the producing Pod exists.
  #
  # Asserted as STORE STATE and never as a call count: the handoff settled with
  # a release the daemon acknowledged, which is the outcome a release has, and
  # "the daemon was called" is not assertable here at all.
  #
  # What it does NOT assert is that the bytes are gone. A release releases the
  # HOLD; the incarnation is the step's own output and the artifact daemon has
  # aliased it read-only at the ordinary path, so deleting it here would be a
  # settlement destroying a step's output while that alias still pointed at it.
  # Deletion is reclamation by policy — Phase 7's — and the no_capture scenario
  # above asserts the surviving bytes directly.
  #
  # Reddened by: the pre_reservation_cancel branch forking on the acknowledged
  # hold instead of the reserved source — this reddens on the release line
  # while the disposition line above it stays green.
  @HOP-11
  Scenario: A cancelled handoff with no acknowledged hold releases its reservation and closes
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When the handoff is cancelled with no acknowledged hold
    And the capture settles
    Then the disposition is "pre_reservation_cancel"
    And the reserved incarnation is released

  # The CONTROL is the first scenario in this file: the same operations, served
  # from a committed Stage 2 reservation. Without it, "a predeclaration is
  # refused" passes on a plane that refuses everything.
  @HOP-5 @HOP-21
  Scenario: Seal and publish are refused from a predeclaration, and served from the Stage 2 reservation
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When seal and publish are attempted from the predeclaration
    Then the capture is refused as "predeclaration"

  @HOP-6
  Scenario: A new build gets a new handoff identity and a new source lease
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    When a new build of the same step is admitted
    Then the new build's handoff identity is not the old one

  # The three announcements in EMISSION ORDER, since order is part of the
  # assertion. Each line asserts that its announcement stands where the
  # sequence says it should, so the three together say the whole order and each
  # one still says something alone.
  #
  # Reddened by: the Req 18 emitter dropping the selection announcement and
  # emitting only seal start and the outcome — this reddens on its first
  # announcement line, and the payload scenario below stays green.
  @HOP-18
  Scenario: A capture-selected step announces selection, seal start and its terminal disposition
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the build announces "capture-selected"
    And the build announces "capture-seal-started"
    And the build announces "capture-disposition"

  # The absence and its control in one scenario, because "an ordinary step
  # emits none of them" passes on a build that emits nothing at all. The
  # capture step's own announcement is asserted FIRST, and the ordinary step
  # beside it runs a whole recovery pass — which is the emitter's chance to
  # announce per execution rather than per handoff.
  @HOP-18
  Scenario: An ordinary step announces none of them, while the capture step beside it announces all three
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    Then the build announces "capture-disposition"
    When an ordinary step runs beside it
    Then the build announces nothing about capture

  # The payload asserted WHOLE, which is the only form in which "never a grant,
  # key, path or consumer ref" can fail. The field set itself is pinned in Go
  # (atc/hangaroutput/announcement_test.go): a type with three closed-vocabulary
  # strings has nowhere to put a fourth thing, and no value assertion can say
  # that.
  @HOP-18
  Scenario: The announcement carries the disposition and reason and nothing else
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the announcement carries the disposition and reason and nothing else
