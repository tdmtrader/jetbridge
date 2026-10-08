Feature: A managed output is read through its authorized node service

  @HOP-35 @HOP-37
  Scenario: The control plane inspects a retained exact generation without bucket credentials
    Given an artifact daemon serving the output plane over authenticated TLS
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then the consumer can inspect only the exact output through an authenticated daemon

  @HOP-35 @HOP-36 @HOP-37
  Scenario Outline: Only a live, unspent read warrant permits a verified output download
    Given an artifact daemon serving the output plane over authenticated TLS
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then the consumer downloads with a "<claim>" reader's claim

    Examples:
      | claim    |
      | live     |
      | spent    |
      | expired  |
      | forged   |
      | limited  |
