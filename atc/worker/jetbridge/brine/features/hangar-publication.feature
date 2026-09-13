Feature: What a sealed source becomes, and what the bucket then holds

  The store is the emulated OUTPUT bucket the fixture created — never the
  artifact daemon's, which is a different bucket served by a different binary
  under a different service account — read back through the same client the
  daemon uses. There is no request log on the fixture, so "holds exactly one
  object" is an outcome; dedup is told from overwrite by seeding the key with a
  DIFFERENT variant and naming which bytes are there afterwards, which is the
  device ../daemon-durable.feature:100-109 uses.

  Both halves of every receipt assertion are production. The daemon signs with
  its epoch private key and the check verifies with the production verifier
  under the activation-pinned public key, against the one-use challenge the
  receipt answers, never against a string a test wrote.

  THE WHEN IS THE DAEMON'S. `the capture settles` is the control plane's Stage 2
  transaction and belongs to Phase 5; what these scenarios say is the node's
  half — confirm the seal, publish the sealed tree, answer a stat challenge.

  # The control is asserted FIRST: a publish with no caller-supplied key returns
  # a receipt naming the server-derived scope, and only then does the same
  # publish carrying a bucket field get refused.
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
    When the publish request also carries a caller-chosen "scope"
    Then the daemon's refusal says "server-derived"
    When the publish request also carries a caller-chosen "key"
    Then the daemon's refusal says "server-derived"
    When the publish request also carries a caller-chosen "prefix"
    Then the daemon's refusal says "server-derived"

  # Assert the receipt WHOLE — scope, digest, generation, epoch and key id — not
  # `contains`, which is convention 8 and what the GAP rows warn about.
  #
  # The scope cannot be spelled here, and that is the assertion rather than a
  # gap in it: it is an opaque per-tenant, per-epoch hash, so a feature file
  # that could name one would be a feature file choosing where an object goes.
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
    When the daemon seals and publishes the source
    Then the capture returns a receipt for the server-derived scope at a store-assigned generation
    When the published tree is read back from the output bucket
    Then the output bucket holds exactly one object, marked "hangar-output-v1"

  @HOP-25 @HOP-27
  Scenario: The receipt verifies under the activation-pinned public key and fails under any other
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    When the daemon seals and publishes the source
    Then the receipt does not verify under any other key
    And the receipt verifies under the activation-pinned public key

  # Convention 6 and 7 are why this is three scenarios and not one: repeating a
  # publish cannot tell dedup from overwrite. The discriminators are the
  # wrong-variant and unmarked twins below.
  @HOP-23 @HOP-25
  Scenario: Publishing identical canonical bytes twice deduplicates to one object and two receipts
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the daemon seals and publishes the source
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
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the output bucket's key for this tree already holds a different variant
    When the daemon seals and publishes the source
    Then the capture is refused as "collision"

  @HOP-22 @HOP-23
  Scenario: An object at the key with no marker is a typed collision, and the marked one still deduplicates
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the output bucket's key for this tree already holds an object with no marker
    When the daemon seals and publishes the source
    Then the capture is refused as "marker"
