Feature: Production resource config scope persistence

  Scenario: A non-unique config without a resource reuses one global scope
    When the production resource config evaluates profile "nonunique-global-none"
    Then the resource config scope observation exactly matches "nonunique-global-none"

  Scenario: Global resources disabled gives each resource its own scope
    When the production resource config evaluates profile "nonunique-resource-unique"
    Then the resource config scope observation exactly matches "nonunique-resource-unique"

  Scenario: Global resources enabled shares a non-unique config scope
    When the production resource config evaluates profile "nonunique-resource-global"
    Then the resource config scope observation exactly matches "nonunique-resource-global"

  Scenario: A unique base config without a resource reuses one global scope
    When the production resource config evaluates profile "unique-global-none"
    Then the resource config scope observation exactly matches "unique-global-none"

  Scenario: A unique base config keeps a resource-specific scope
    When the production resource config evaluates profile "unique-resource-unique"
    Then the resource config scope observation exactly matches "unique-resource-unique"
