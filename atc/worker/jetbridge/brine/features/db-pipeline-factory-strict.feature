Feature: Pipeline factory visibility and ordering over real PostgreSQL

  Scenario Outline: Production pipeline factory behavior — <profile>
    Given the production pipeline factory profile "<profile>" is exercised
    Then the pipeline factory observation exactly matches "<profile>"

    Examples:
      | profile            |
      | visible-team       |
      | visible-empty-name |
      | visible-empty      |
      | visible-nil        |
      | visible-reordered  |
      | all-default        |
      | all-reordered      |
