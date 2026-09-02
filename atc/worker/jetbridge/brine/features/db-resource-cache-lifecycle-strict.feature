Feature: Resource cache lifecycle over real PostgreSQL

  Scenario Outline: Production resource cache lifecycle behavior — <profile>
    Given the production resource cache lifecycle profile "<profile>" is exercised
    Then the resource cache lifecycle observation exactly matches "<profile>"

    Examples:
      | profile                         |
      | job-use-survives-dirty-clean    |
      | recent-memory-use-survives      |
      | expired-memory-use-is-reclaimed |
      | deleted-build-cache-is-reclaimed |
      | previous-job-image-is-reclaimed |
      | unconfigured-type-cache-is-reclaimed |
      | next-input-cache-survives       |
      | unused-resource-cache-is-reclaimed |
