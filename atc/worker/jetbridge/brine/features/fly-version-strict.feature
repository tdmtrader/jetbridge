Feature: Fly version parsing and development detection

  Scenario: GetSemver parses a stable release
    When the production Fly version profile "stable" is exercised
    Then the Fly version observation exactly matches "stable"

  Scenario: GetSemver rejects malformed release components
    When the production Fly version profile "invalid" is exercised
    Then the Fly version observation exactly matches "invalid"

  Scenario: GetSemver parses a pre-release
    When the production Fly version profile "pre-release" is exercised
    Then the Fly version observation exactly matches "pre-release"

  Scenario: GetSemver parses a post-release
    When the production Fly version profile "post-release" is exercised
    Then the Fly version observation exactly matches "post-release"

  Scenario: IsDev detects only development version components
    When the production Fly version profile "development-detection" is exercised
    Then the Fly version observation exactly matches "development-detection"
