Feature: Production job build creation guards

  Scenario: Manual-trigger-disabled jobs reject creation without persisting a build
    When the production job build guard executes profile "manual-disabled"
    Then the job build guard observation is status 409 with 0 persisted builds

  Scenario: Missing persisted jobs return HTTP 404 from build creation
    When the production job build guard executes profile "missing-job"
    Then the job build guard observation is status 404
