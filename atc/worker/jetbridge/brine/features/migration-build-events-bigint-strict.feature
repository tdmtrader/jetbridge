Feature: Build event bigint migration indexes

  Scenario: Up migration indexes both existing pipeline partition columns
    When the actual build event bigint migration evaluates profile "up-existing-pipeline"
    Then the build event bigint migration observation exactly matches "up-existing-pipeline"

  Scenario: Up migration indexes only the new column on existing team partitions
    When the actual build event bigint migration evaluates profile "up-existing-team"
    Then the build event bigint migration observation exactly matches "up-existing-team"

  Scenario: Up migration installs the new index trigger for new teams
    When the actual build event bigint migration evaluates profile "up-new-team"
    Then the build event bigint migration observation exactly matches "up-new-team"

  Scenario: Up migration installs both index triggers for new pipelines
    When the actual build event bigint migration evaluates profile "up-new-pipeline"
    Then the build event bigint migration observation exactly matches "up-new-pipeline"

  Scenario: Down migration restores the old ID index on existing pipeline partitions
    When the actual build event bigint migration evaluates profile "down-existing-pipeline-old"
    Then the build event bigint migration observation exactly matches "down-existing-pipeline-old"

  Scenario: Down migration removes the new ID index from existing pipeline partitions
    When the actual build event bigint migration evaluates profile "down-existing-pipeline-new"
    Then the build event bigint migration observation exactly matches "down-existing-pipeline-new"

  Scenario: Down migration removes the new ID index from existing team partitions
    When the actual build event bigint migration evaluates profile "down-existing-team"
    Then the build event bigint migration observation exactly matches "down-existing-team"

  Scenario: Down migration leaves new team partitions without a build ID index
    When the actual build event bigint migration evaluates profile "down-new-team"
    Then the build event bigint migration observation exactly matches "down-new-team"

  Scenario: Down migration keeps the build ID index trigger for new pipelines
    When the actual build event bigint migration evaluates profile "down-new-pipeline"
    Then the build event bigint migration observation exactly matches "down-new-pipeline"
