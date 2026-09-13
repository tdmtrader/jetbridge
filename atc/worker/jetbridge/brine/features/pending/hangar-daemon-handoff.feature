Feature: What the output daemon answers

  NOT RUN YET — see ../README.md and the note at the head of
  hangar-capture-pod.feature. These scenarios are checked, not executed.

  Driven against the real binary through the fixture in
  ../../steps/hangar_fixture.go, for the reason ../../steps/realdaemon.go
  records: a double can be made to refuse, but it cannot say the answer is
  RIGHT, and here half of what an operation does is a change to a node's
  filesystem that no response shows.

  NOTHING HERE COUNTS A REQUEST. The scenarios that mean "the daemon was not
  called" say instead that the source is still held.

  # A takeover is the only concurrency this runner can say, and it says it
  # sequentially: the epoch is bumped, and the old owner's fence is then stale.
  @HOP-10
  Scenario: A lease takeover bumps the epoch, and the previous owner's fence stops being served
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the owner's lease is taken over
    Then the Hangar daemon answers 200, holding the source
    When a stale fence is presented
    Then the daemon's refusal says "fence"
  # Assertion ORDER is part of the assertion: brine stops at the first red step,
  # so the classification is asserted before the survival check rather than
  # after it (MIGRATION-EVIDENCE.md:218-230).
  #
  # Reddened by: RequestSourcePreservingStop removing the incarnation directory
  # as part of the stop — the source line reddens while the acknowledgement line
  # above it stays green.
  @HOP-14 @HOP-16
  Scenario: A source-preserving stop leaves the pod's artifact path in place
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the step is stopped without destroying its source
    Then the witness is what the step reports
    And the source is still there after the stop

  # Reddened by: recreatePausePodIfTerminal (atc/worker/jetbridge/process.go:990)
  # deleting the terminal pause Pod without first consulting the ledger
  # classifier — the capture line reddens and the ordinary control above stays
  # green, which is precisely the regression the Go test was written to catch.
  @HOP-12 @HOP-16
  Scenario: Pause pod recreation for a capture-held source is refused, and an ordinary one still recreates
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And its pause pod reaches a terminal state
    And the daemon holds the source
    Then the pause pod is recreated
    When destructive cleanup is requested before any witness
    Then destructive cleanup is refused

  # "The hold is gone" needs its presence half, and the release is the only
  # thing that makes it gone: the scenario asserts the source is held, releases
  # it, and asserts it is not.
  @HOP-9 @HOP-11
  Scenario: A released hold is gone from the node, and a held one is not
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    Then the source is still held on the node
    When the hold is released
    Then the source has been released
