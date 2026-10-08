Feature: Managed Run inputs materialize through live read authority

  @HOP-35 @HOP-36 @HOP-37
  Scenario Outline: The authenticated node installs only the exact claimed input
    Given an artifact daemon serving the output plane over authenticated TLS
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon writes the held marker
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then the consumer materializes with a "<claim>" reader's claim

    Examples:
      | claim                   |
      | live                    |
      | expired                 |
      | forged                  |
      | wrong destination       |
      | wrong generation        |
      | unauthenticated         |
      | node-local warrant        |
      | fresh retry             |
      | conflicting destination |
      | symlink destination     |
