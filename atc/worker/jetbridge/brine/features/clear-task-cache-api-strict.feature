Feature: Clear task cache API strict behavior
  The production API route clears persisted task caches through a real authenticated
  HTTP server and the real PostgreSQL job implementation.

  Scenario: Clearing a step without a path deletes both matching caches and preserves another job
    Given the production ClearTaskCache API executes profile "all-step-caches"
    Then the ClearTaskCache API observation exactly matches profile "all-step-caches"

  Scenario: Clearing one path deletes only that cache and preserves both decoys
    Given the production ClearTaskCache API executes profile "selected-cache-path"
    Then the ClearTaskCache API observation exactly matches profile "selected-cache-path"

  Scenario: Clearing an absent path reports zero removed caches
    Given the production ClearTaskCache API executes profile "missing-cache-path"
    Then the ClearTaskCache API observation exactly matches profile "missing-cache-path"

  Scenario: Clearing an absent step reports zero and preserves all caches
    Given the production ClearTaskCache API executes profile "missing-step"
    Then the ClearTaskCache API observation exactly matches profile "missing-step"

  Scenario: Clearing a cache for a missing configured job returns not found
    Given the production ClearTaskCache API executes profile "missing-job"
    Then the ClearTaskCache API observation exactly matches profile "missing-job"
