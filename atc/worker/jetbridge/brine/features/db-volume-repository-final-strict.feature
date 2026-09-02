Feature: Remaining production volume repository behavior
  Scenario: Destroying volume lookup is empty after the persisted volume is destroyed
    When the final production volume repository evaluates profile "destroying-empty"
    Then the final volume repository observation is "empty=true"

  Scenario: Base resource type volume persists without a team
    When the final production volume repository evaluates profile "base-no-team"
    Then the final volume repository observation is "team-id-null=true"

  Scenario: Removing unreported destroying volumes returns no error
    When the final production volume repository evaluates profile "remove-unreported"
    Then the final volume repository observation is "error=nil"

  Scenario: Removing destroying volumes with an empty report returns no error
    When the final production volume repository evaluates profile "remove-empty-report"
    Then the final volume repository observation is "error=nil"

  Scenario: Removing destroying volumes ignores creating rows without error
    When the final production volume repository evaluates profile "remove-creating"
    Then the final volume repository observation is "error=nil"

  Scenario: Removing destroying volumes protects reported rows without error
    When the final production volume repository evaluates profile "remove-protected"
    Then the final volume repository observation is "error=nil"

  Scenario: Updating missing times for a subset returns no error
    When the final production volume repository evaluates profile "update-subset"
    Then the final volume repository observation is "error=nil"

  Scenario: Updating missing times for the full report returns no error
    When the final production volume repository evaluates profile "update-full"
    Then the final volume repository observation is "error=nil"

  Scenario: Updating missing times clears a reported missing volume without error
    When the final production volume repository evaluates profile "update-restored"
    Then the final volume repository observation is "error=nil"
