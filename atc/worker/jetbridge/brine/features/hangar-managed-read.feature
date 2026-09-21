Feature: A managed output is read through its authorized node service

  @HOP-35 @HOP-37
  Scenario: The control plane inspects a retained exact generation without bucket credentials
    Given a Hangar output daemon accepting authenticated TLS connections
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then the consumer can inspect only the exact output through an authenticated daemon

  @HOP-35 @HOP-36 @HOP-37
  Scenario Outline: Only a live read lease permits a verified output download
    Given a Hangar output daemon accepting authenticated TLS connections
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then the consumer downloads with a "<lease>" read lease

    Examples:
      | lease    |
      | live     |
      | released |
      | missing  |
      | forged   |
      | limited  |
