Feature: Pipeline dashboard behavior over real PostgreSQL

  Scenario: Production pipeline dashboard preserves configured jobs and current build summaries
    Given the production pipeline dashboard behavior is exercised
    Then the pipeline dashboard behavior exactly matches the persisted state
