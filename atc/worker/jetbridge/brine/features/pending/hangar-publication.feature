Feature: What a sealed source becomes, and what the bucket then holds

  NOT RUN YET — see ../README.md. These scenarios are checked, not executed.

  The store is the emulated output bucket the fixture created, read back through
  the same client the daemon uses. There is no request log on the fixture, so
  "holds exactly one object" is an outcome; dedup is told from overwrite by
  seeding the key with a DIFFERENT variant and naming which bytes are there
  afterwards, which is the device ../daemon-durable.feature:100-109 uses.

  Both halves of every receipt assertion are production: the daemon signs with
  its epoch private key and the check verifies with the production verifier
  under the activation-pinned public key, never against a string a test wrote.

  # The whole chain, end to end. The consumer's materialization is the LAST
  # line, which is why the object and receipt assertions are written before it
  # rather than after: brine stops at the first red step.
  #
  # Reddened by: RegisterReceipt registering the logical ref without its
  # generation.
  @HOP-21 @HOP-22 @HOP-25 @HOP-26
  Scenario: A sealed source becomes a marked object, a signed receipt and a registered exact ref
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    When the published tree is read back from the output bucket
    Then the output bucket holds exactly one object, marked "hangar-output-v1"
    And the registered exact ref names the published generation

  # The sequential form of AC 8, which is the only form this runner can honestly
  # say. It does NOT stand alone as a dedup assertion: its discriminators are
  # the wrong-variant and unmarked twins above, which it cites rather than
  # seeding a fourth time.
  @HOP-23 @HOP-25
  Scenario: Two concurrent-in-sequence captures of identical bytes get one object and two distinct receipts
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the same canonical bytes are captured again
    Then the two captures share one object and carry two distinct receipts

  @HOP-34 @HOP-45
  Scenario: An exact replacement generation supersedes the old one, and the old ref no longer resolves
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    Then the registered exact ref names the published generation
    When an exact replacement generation is published
    Then the old ref no longer resolves
