Feature: Fix build private plan migration

  These scenarios run the actual migration against real PostgreSQL rows.

  Scenario: Up migration ignores NULL plans
    When the actual build private plan migration evaluates profile "up-null"
    Then the build private plan migration observation exactly matches "up-null"

  Scenario: Up migration removes the plan key
    When the actual build private plan migration evaluates profile "up-plan"
    Then the build private plan migration observation exactly matches "up-plan"

  Scenario: Up migration updates multiple plans
    When the actual build private plan migration evaluates profile "up-multiple"
    Then the build private plan migration observation exactly matches "up-multiple"

  Scenario: Down migration ignores NULL plans
    When the actual build private plan migration evaluates profile "down-null"
    Then the build private plan migration observation exactly matches "down-null"

  Scenario: Down migration nests the plan under a plan key
    When the actual build private plan migration evaluates profile "down-plan"
    Then the build private plan migration observation exactly matches "down-plan"

  Scenario: Down migration updates multiple plans
    When the actual build private plan migration evaluates profile "down-multiple"
    Then the build private plan migration observation exactly matches "down-multiple"
