Feature: A managed input is verified before the task starts

  @HOP-35 @HOP-36 @HOP-37
  Scenario Outline: The actual input initialization obeys its read lease and sealed receipt
    Given a Hangar output daemon accepting authenticated TLS connections
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    And the step finishes and the daemon witnesses it
    And the capture settles
    And the published tree is read back from the output bucket
    When the consumer binds the output inside its own transaction
    Then the consumer input initializes with "<condition>"

    Examples:
      | condition                    |
      | live                         |
      | released                     |
      | forged                       |
      | lost success response        |
      | conflicting sealed receipt   |
      | a transient failure          |
