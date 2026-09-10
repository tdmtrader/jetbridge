Feature: What a consuming step's Pod says about a published output

  This is where the learning's split row 2 lands: the consumer's Hangar init
  container's args and mounts are spec, so the two Go tests at
  atc/worker/jetbridge/storage_daemonset_test.go:492 and :559 move here.
  TestDaemonSetBackend_HangarInitSignalsCleanUpAndFail (:613) does NOT move —
  it runs the generated shell with a fake wget on PATH, and script text is not
  spec.

  The consumer's pod is the existing PodCreated state, so every existing mount
  and volume check composes with the three new ones below.

  # Reddened by: BuildFetchInitContainers base64-encoding a receipt whose
  # Generation has been zeroed before it reaches the init command — the exact
  # receipt line reddens and the mount scenarios stay green.
  @HOP-26 @HOP-35
  Scenario: The consumer's Hangar init verifies exactly the receipt for its TreeRef
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And a later step "consume" takes the published output "result" at "/tmp/build/from-earlier"
    When the consumer's pod is built
    Then the consumer's Hangar init verifies exactly the receipt for its tree

  # A mount COUNT plus ReadOnly, not membership (convention 8).
  @HOP-26 @HOP-37
  Scenario: Each verified tree gets one fixed read-only verification mount
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And a later step "consume" takes the published output "result" at "/tmp/build/from-earlier"
    When the consumer's pod is built
    Then the consumer's pod declares exactly 1 read-only verification mount per tree

  # An absence whose control is the receipt assertion in the same scenario,
  # asserted first.
  @HOP-7 @HOP-26
  Scenario: A user-controlled destination never enters the verification command
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And a later step "consume" takes the published output "result" at "/tmp/build/from-earlier"
    When the consumer's pod is built
    Then the consumer's Hangar init verifies exactly the receipt for its tree
    And no user-controlled destination enters the verification command

  @HOP-35 @HOP-37
  Scenario: A consumer pod materializes exactly the receipt's tree
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And a later step "consume" takes the published output "result" at "/tmp/build/from-earlier"
    When the consumer's pod is built
    Then the consumer's pod materializes exactly the receipt's tree
    And the step sees a volume mounted at "/tmp/build/from-earlier"

  # Control first: the granted read is the line above the refusal, because a
  # refusal passes on a daemon that refuses everything.
  @HOP-35 @HOP-36
  Scenario: Without an active claim the consumer's read is refused, while the same read with a claim succeeds
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    And the consumer binds the output inside its own transaction
    Then the managed read is granted
    When the consumer holds no active claim
    Then the managed read is refused as "no active claim"
