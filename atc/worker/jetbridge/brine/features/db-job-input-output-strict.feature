Feature: Job inputs and outputs over real PostgreSQL
  Production job queries must return the persisted graph for only the selected
  job. Each row replaces one exact source leaf.

  Scenario Outline: Job input or output behavior — <profile>
    Given the production job input or output behavior "<profile>" is exercised
    Then the job input or output behavior exactly matches "<profile>"

    Examples:
      | profile                  |
      | algorithm-input          |
      | algorithm-get-pin        |
      | algorithm-get-wins-api   |
      | algorithm-config-pin     |
      | algorithm-api-pin        |
      | algorithm-multiple       |
      | algorithm-gets-only      |
      | inputs                   |
      | outputs                  |
