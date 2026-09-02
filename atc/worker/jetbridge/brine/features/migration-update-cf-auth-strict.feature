Feature: Cloud Foundry team authorization migration

  Scenario: Up migration adds developer to an organization and space group
    When the actual Cloud Foundry authorization migration evaluates profile "up-org-space"
    Then the Cloud Foundry authorization migration exactly matches "up-org-space"

  Scenario: Up migration handles each supported Cloud Foundry group shape
    When the actual Cloud Foundry authorization migration evaluates profile "up-cf-combinations"
    Then the Cloud Foundry authorization migration exactly matches "up-cf-combinations"

  Scenario: Up migration leaves GitHub pivotal-cf groups unchanged
    When the actual Cloud Foundry authorization migration evaluates profile "up-github-groups"
    Then the Cloud Foundry authorization migration exactly matches "up-github-groups"

  Scenario: Down migration removes the developer role
    When the actual Cloud Foundry authorization migration evaluates profile "down-developer"
    Then the Cloud Foundry authorization migration exactly matches "down-developer"

  Scenario: Down migration leaves auditor and manager roles unchanged
    When the actual Cloud Foundry authorization migration evaluates profile "down-other-roles"
    Then the Cloud Foundry authorization migration exactly matches "down-other-roles"
