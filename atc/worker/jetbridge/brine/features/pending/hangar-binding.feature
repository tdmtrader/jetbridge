Feature: What the ATC binds in PostgreSQL when a consumer takes a published output

  NOT RUN YET — see ../README.md. These scenarios are checked, not executed.

  Every check here is a PRODUCTION read rather than a raw SQL select, so a
  repository that writes the right row through the wrong API cannot pass. The
  database is the real scenario-scoped PostgreSQL the estate already runs
  (../../steps/resources.go), reached by the fixture rather than by a phrase.

  Lease fencing under concurrent takeover, both crash halves of every commit,
  and the 15-minute lease stay in Go: brine has no way to say two of these at
  once, and no injectable clock.

  @HOP-30
  Scenario: A consumer binds the output and acquires its claim in one transaction
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then exactly 1 claim is recorded
    And the claim protects generation 1

  # The committed half is asserted FIRST, so the absence cannot pass on a
  # repository that never writes a claim; the count is `exactly one`.
  @HOP-30 @HOP-32
  Scenario: A rolled-back consumer transaction leaves no claim behind, while the committed one leaves exactly one
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then exactly 1 claim is recorded
    When the consumer's transaction is rolled back
    Then no claim is left behind

  # Reddened by: ReleaseClaim deleting the claim row instead of writing the
  # tombstone — the permanence line reddens while the idempotence line above it
  # stays green.
  @HOP-32
  Scenario: Releasing the claim twice is idempotent and the tombstone is permanent
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And the consumer binds the output inside its own transaction
    When the binding is released
    And the binding is released again
    Then the tombstone is permanent

  # Convention 6: "release is idempotent" passes for a repository that ignores
  # the claim ID entirely, so the twin shows reuse of a released ID is a typed
  # conflict while a fresh ID still succeeds.
  @HOP-32
  Scenario: Re-acquiring a released claim ID is a typed conflict, and a fresh ID still succeeds
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And the consumer binds the output inside its own transaction
    And the binding is released
    When the released claim ID is acquired again
    Then the binding is refused as "lifecycle conflict"
    When a fresh claim ID is acquired
    Then exactly 1 claim is recorded

  @HOP-29
  Scenario: A hidden-to-published transition keeps the same candidate claim ID
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And the consumer binds the output inside its own transaction
    When the ref moves from hidden to published
    Then the candidate claim ID is unchanged

  @HOP-28 @HOP-34
  Scenario: A claim for an unregistered exact ref is refused, and the registered one is granted
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then exactly 1 claim is recorded
    When the consumer names an unregistered exact ref
    Then the binding is refused as "not found"

  @HOP-28 @HOP-34
  Scenario: The binding is not visible before verification, and is visible after
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then the binding is not visible
    When the binding is verified
    Then the binding is visible
