Feature: A stopped execution cannot begin afterward

  @core-review
  Scenario: A delayed first start loses to an accepted pre-start stop
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the daemon accepts stop before the first start
    And a delayed supervisor attempts the stopped execution's first start
    Then the stopped execution refuses its first start and has no finish witness

  @core-review
  Scenario: The first-start fence survives a daemon restart
    Given a real artifact daemon publishing to a Hangar output bucket
    And a capture-selected task "build" built from image "busybox" declares the output "result"
    And the daemon holds the source
    When the daemon accepts stop before the first start
    And the output daemon restarts with the same control ledger
    And a delayed supervisor attempts the stopped execution's first start
    Then the stopped execution refuses its first start and has no finish witness
