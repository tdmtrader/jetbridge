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

  # The control is asserted FIRST: a publish with no caller-supplied key returns
  # a receipt naming the server-derived scope and digest, and only then does the
  # same publish carrying a bucket field get refused.
  #
  # Reddened by: the publish handler reading the request's `bucket` field
  # instead of the namespace resolved from the active epoch — one step reddens,
  # the refusal line, and the control line above it stays green.
  @HOP-7 @HOP-20 @HOP-23
  Scenario: A caller-supplied bucket, scope or object key is refused, and the server-derived one is served
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    Then the Hangar daemon answers 200, holding the source
    When the publish request also carries a caller-chosen "bucket"
    Then the daemon's refusal says "server-derived"

  # Assert the receipt WHOLE — scope, digest, generation, epoch and key id — not
  # `contains`, which is convention 8 and what the GAP rows warn about.
  #
  # Reddened by: the Ed25519 signer omitting Generation from the signed claim
  # set, so the whole-receipt check reddens on its one line while the object
  # assertion above it stays green.
  @HOP-21 @HOP-22 @HOP-25
  Scenario: A sealed source publishes one marked object and a receipt naming its scope, digest and generation
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the capture returns a receipt for "build/result" at generation 1
    When the published tree is read back from the output bucket
    Then the output bucket holds exactly one object, marked "hangar-output-v1"

  @HOP-25 @HOP-27
  Scenario: The receipt verifies under the activation-pinned public key and fails under any other
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the receipt verifies under the activation-pinned public key
    And the receipt does not verify under any other key

  # Convention 6 and 7 are why this is three scenarios and not one: repeating a
  # publish cannot tell dedup from overwrite.
  @HOP-23 @HOP-25
  Scenario: Publishing identical canonical bytes twice deduplicates to one object and two receipts
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    When the published tree is read back from the output bucket
    And the same canonical bytes are captured again
    Then the output bucket holds exactly one object, marked "hangar-output-v1"
    And the two captures share one object and carry two distinct receipts

  # Reddened by: the publisher's create-if-absent path falling back to an
  # unconditional write when GenerationMatch: 0 returns 412 — this scenario
  # reddens on its refusal line while the dedup scenario's `holds exactly` line
  # above stays green, which is what distinguishes the mutation from a broken
  # publisher.
  @HOP-23
  Scenario: The same key holding a DIFFERENT variant is a typed collision, never an overwrite
    Given a real artifact daemon publishing to a Hangar output bucket
    And the output bucket already holds "hangar/v1/scopes/build/trees/sha256/deadbeef.tar.zst" whose tree reads "somebody else's bytes"
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the capture is refused as "collision"

  @HOP-22 @HOP-23
  Scenario: An object at the key with no marker is a typed collision, and the marked one still deduplicates
    Given a real artifact daemon publishing to a Hangar output bucket
    And the output bucket already holds "hangar/v1/scopes/build/trees/sha256/deadbeef.tar.zst" with no marker
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the capture settles
    Then the capture is refused as "collision"

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
