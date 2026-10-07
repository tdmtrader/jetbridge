Feature: What a finished producer's capture row becomes

  Sequential and observable, which is why these are here rather than in Go:
  the coordinator moves one capture row through a closed vocabulary of states,
  and the row is what production writes and production reads back.

  `the capture settles` is the CONTROL PLANE, driven as far as it goes: the
  real capture rows over this scenario's real PostgreSQL, the real coordinator,
  and jetbridge's own client against the real output daemon. Nothing here
  chooses a transition and nothing here counts a call.

  Kept in Go and cited rather than duplicated: the CAS conflicts (a race),
  deadline expiry (a clock, and convention 9 forbids a chain that waits), and
  every crash half around a commit or a node answer, which is the class brine
  cannot interpose on. Those are atc/hangaroutput's coordinator suites.

  @HOP-5
  Scenario: A successful finish publishes the capture and releases its marker
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the capture row is "published"
    And the capture is released

  # One scenario, both halves, because "a failed producer is discarded" passes
  # on a coordinator that never publishes at all. The failing producer is
  # asserted first and the succeeding one second, over a new build of the same
  # step — which is the only honest way to run the same producer twice.
  @HOP-2 @HOP-5
  Scenario: A failed producer is discarded, and the same producer succeeding is published
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the step fails
    And the capture settles
    Then the capture row is "discarded"
    And the discard reason is "producer_failed"
    And the produced output is still on the node
    When a new build of the same step is admitted
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    Then the capture row is "published"

  # The cancellation is asked about a producer whose own outcome is a failure,
  # because that is the only state in which "run_cancelled, never
  # producer_failed" can be false: a cancellation the plane honoured after
  # reading the outcome first would be discarded for the wrong reason.
  @HOP-11
  Scenario: Cancellation before the seal discards the capture as cancelled, never as a failed producer
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the step fails
    And the capture is cancelled before it is sealed
    And the capture settles
    Then the capture row is "discarded"
    And the discard reason is "run_cancelled"
    And the capture is released

  # The absence: no hold was ever made, so there is no marker on the node. The
  # release still has to land -- a terminal row with no released_at keeps the
  # step directory protected forever -- and it is answered by a node that held
  # nothing. The scenario above is its control.
  @HOP-11
  Scenario: A capture cancelled with no hold is discarded and released
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When the capture is cancelled with no hold
    And the capture settles
    Then the capture row is "discarded"
    And the discard reason is "run_cancelled"
    And the capture is released
