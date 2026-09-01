Feature: CCTray XML reflects persisted pipeline and build state

  Source: 14 specs in atc/api/cc_test.go covering status/header/XML, build
  status translation, activity, empty projects, instance vars, and not-found.

  Scenario: A successful build renders a sleeping successful project
    When the real CC API handles profile "succeeded"
    Then the CC API returned status 200
    And the CC API content type is "application/xml"
    And the CC XML activity is "Sleeping"
    And the CC XML build status is "Success"

  Scenario Outline: Finished build status <profile> renders <status>
    When the real CC API handles profile "<profile>"
    Then the CC API returned status 200
    And the CC XML build status is "<status>"

    Examples:
      | profile | status    |
      | aborted | Exception |
      | errored | Exception |
      | failed  | Failure   |

  Scenario: A running next build changes project activity to building
    When the real CC API handles profile "building"
    Then the CC XML activity is "Building"
    And the CC XML build status is "Success"

  Scenario: A buildless job is omitted
    When the real CC API handles profile "no-last-build"
    Then the CC API returned status 200
    And the CC XML is empty

  Scenario: A pipeline with no jobs returns an empty project list
    When the real CC API handles profile "no-job"
    Then the CC API returned status 200
    And the CC XML is empty

  Scenario: A team with no pipelines returns an empty project list
    When the real CC API handles profile "no-pipeline"
    Then the CC API returned status 200
    And the CC XML is empty

  Scenario: Instance variables are preserved in project name and URL
    When the real CC API handles profile "instanced"
    Then the CC API returned status 200
    And the CC XML contains "feature/foo"
    And the CC XML contains "vars.branch"

  Scenario: A missing team returns not found
    When the real CC API handles profile "missing-team"
    Then the CC API returned status 404
