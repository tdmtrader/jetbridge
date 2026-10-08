Feature: What a consuming step's read of a published output is allowed

  A consumer's input is a managed read of the output namespace: the web admits
  the read against the consumer's claim and the daemon serves it at the output
  plane's read route. The shape of the init container that performs the read
  is hangar-managed-input-init.feature's; the materialization it produces is
  hangar-managed-materialization.feature's. What is left here is the one rule
  the claim ledger imposes on the read.

  # Control first: the granted read is the line above the refusal, because a
  # refusal passes on a daemon that refuses everything. A read's authority is
  # its own reader's claim, and a claim protects a registered generation:
  # once every claim is given back and the reclaim pass has stamped the
  # generation reclaimed, the same read is refused as not found.
  @HOP-35 @HOP-36
  Scenario: Once the generation is reclaimed the consumer's read is refused, while the same read before it succeeds
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And the consumer binds the output inside its own transaction
    Then the managed read is warranted
    When every claim is released and the reclaim pass runs
    Then the managed read is refused as "not found"
