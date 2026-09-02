Feature: Resource client behavior over production HTTP and PostgreSQL

  Scenario: The resource client selects an instanced pipeline when listing resources
    Given the strict production resource client behavior "list-resources" is exercised
    Then the strict resource client behavior exactly matches "list-resources"

  Scenario: The resource client returns a persisted resource
    Given the strict production resource client behavior "get-resource" is exercised
    Then the strict resource client behavior exactly matches "get-resource"

  Scenario: The resource client maps a missing resource to not found
    Given the strict production resource client behavior "get-not-found" is exercised
    Then the strict resource client behavior exactly matches "get-not-found"

  Scenario: The resource client reports and persists one cleared cache association
    Given the strict production resource client behavior "clear-cache" is exercised
    Then the strict resource client behavior exactly matches "clear-cache"

  Scenario: The resource client returns resources sharing the persisted scope
    Given the strict production resource client behavior "list-shared" is exercised
    Then the strict resource client behavior exactly matches "list-shared"

  Scenario: The resource client maps missing shared-resource state to not found
    Given the strict production resource client behavior "shared-not-found" is exercised
    Then the strict resource client behavior exactly matches "shared-not-found"
