Feature: Recovering a capture through the deployed control plane

  @core-review @HOP-13 @HOP-14 @HOP-17
  Scenario: A controller can recover a writer's exact identity and closed statement
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When a controller reads an admitted writer before and after closing it
    Then the writer lookup preserves the issued identity and the exact closed statement
    And a writer lookup for another execution is refused

  @core-review @HOP-13 @HOP-14 @HOP-17
  Scenario: A completed producer accounts for every writer in its exact pod
    Given a Hangar output daemon accepting authenticated TLS connections
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When the controller seals a capture with "terminated" pod evidence
    Then the controller proves every captured writer has terminated

  @core-review @HOP-13 @HOP-14 @HOP-17
  Scenario Outline: Incomplete or foreign pod evidence never confirms a seal
    Given a Hangar output daemon accepting authenticated TLS connections
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When the controller seals a capture with "<evidence>" pod evidence
    Then the seal remains unconfirmed

    Examples:
      | evidence                 |
      | main running             |
      | init running             |
      | sidecar running          |
      | ephemeral running        |
      | last termination only    |
      | restartable snapshot     |
      | missing container status |
      | missing                  |
      | replacement              |

  @core-review @HOP-13 @HOP-14 @HOP-17
  Scenario: Controller restart preserves termination evidence until publication finishes
    Given a Hangar output daemon accepting authenticated TLS connections
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    When the controller seals a capture with "restart during drain" pod evidence
    Then a fresh controller publishes the receipt and releases only its own pod pin
