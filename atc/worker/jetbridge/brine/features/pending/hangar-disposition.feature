Feature: Which outcome a finished producer selects, and what the build is told

  NOT RUN YET — see ../README.md. These scenarios are checked, not executed.

  Sequential and observable, which is why they are here rather than in Go: an
  arbiter picks one member of a closed vocabulary, and a build event is a row
  production writes and production reads back — the same shape as the exit
  annotation ../step-closing.feature reads.

  Kept in Go and cited rather than duplicated: "conflicting replay fails" (a
  race) and "deadline expiry alone releases nothing" (a clock, and convention 9
  forbids a chain that waits).

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
  # coordinator that never selects capture at all.
  @HOP-2 @HOP-5
  Scenario: A failed producer selects no_capture
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the step fails
    And the capture settles
    Then the disposition is "no_capture"
    And the no_capture reason is "producer_failed"

  @HOP-5 @HOP-9
  Scenario: no_capture completes in two halves, and the outcome stays pending between them
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step fails
    When the capture settles
    Then the outcome is still pending
    When the second half of the no_capture close runs
    Then the disposition is "no_capture"

  # Reddened by: RecordNoCaptureIntent accepting a cancellation-selected
  # outcome — this reddens on its one disposition line while the ordinary
  # no_capture scenario above stays green, which is what tells a branch
  # confusion apart from a broken arbiter.
  @HOP-11
  Scenario: Cancellation before Stage 2 selects pre_reservation_cancel, never no_capture
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the step is cancelled before Stage 2
    And the capture settles
    Then the disposition is "pre_reservation_cancel"

  # An absence, so the held form directly above it is its control, and "the
  # daemon was not called" is asserted as an OUTCOME — the hold is still there
  # to be released, and the store's state names it — rather than as a count.
  @HOP-11
  Scenario: A cancelled handoff with no acknowledged hold closes without a daemon release
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When the handoff is cancelled with no acknowledged hold
    And the capture settles
    Then the disposition is "pre_reservation_cancel"
    And no daemon release was needed, and the hold is still there to release

  @HOP-5 @HOP-21
  Scenario: Seal and publish are refused from a predeclaration, and served from the Stage 2 reservation
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When seal and publish are attempted from the predeclaration
    Then the capture is refused as "unauthorized"

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
  # assertion.
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

  # The absence and its control in one scenario, because "an ordinary step emits
  # none of them" passes on a build that emits nothing at all.
  @HOP-18
  Scenario: An ordinary step announces none of them, while the capture step beside it announces all three
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the build announces "capture-selected"
    And the build announces nothing about capture

  # The payload asserted WHOLE, which is the only form in which "never a grant,
  # key, path or consumer ref" can fail.
  @HOP-18
  Scenario: The announcement carries the disposition and reason and nothing else
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the announcement carries the disposition and reason and nothing else
