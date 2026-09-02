Feature: The Go Concourse config client crosses the production API boundary

  Scenario: PipelineConfig returns the exact persisted ordinary config and version
    Given the strict production config client behavior "read-ordinary" is exercised
    Then the strict production config client behavior exactly matches "read-ordinary"

  Scenario: PipelineConfig returns the exact persisted instanced config and version
    Given the strict production config client behavior "read-instanced" is exercised
    Then the strict production config client behavior exactly matches "read-instanced"

  Scenario: PipelineConfig maps the production missing response to not found without error
    Given the strict production config client behavior "read-missing" is exercised
    Then the strict production config client behavior exactly matches "read-missing"

  Scenario: CreateOrUpdatePipelineConfig reports created and decodes production warnings
    Given the strict production config client behavior "create-result" is exercised
    Then the strict production config client behavior exactly matches "create-result"

  Scenario: CreateOrUpdatePipelineConfig sends instance vars while creating
    Given the strict production config client behavior "create-instanced" is exercised
    Then the strict production config client behavior exactly matches "create-instanced"

  Scenario: CreateOrUpdatePipelineConfig reports updated and decodes production warnings
    Given the strict production config client behavior "update-result" is exercised
    Then the strict production config client behavior exactly matches "update-result"

  Scenario: CreateOrUpdatePipelineConfig sends instance vars while updating
    Given the strict production config client behavior "update-instanced" is exercised
    Then the strict production config client behavior exactly matches "update-instanced"
