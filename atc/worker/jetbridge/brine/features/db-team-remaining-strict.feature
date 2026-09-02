Feature: Remaining team persistence invariants over real PostgreSQL

  Each scenario creates its own PostgreSQL database and calls the production
  db.Team methods. The rows are kept one-to-one with source leaves so mutation
  evidence cannot promote an aggregate journey in place of an individual test.

  Scenario Outline: Remaining DB team source behavior — <profile>
    Given the remaining production DB team behavior "<profile>" is exercised
    Then the remaining DB team behavior exactly matches "<profile>"

    Examples:
      | profile |
      | save-worker-overwrites |
      | save-worker-rejects-other-team |
      | workers-empty |
      | auth-persists |
      | auth-clears-legacy |
      | auth-overrides |
      | one-off-build |
      | started-build-fields |
      | started-build-public-plan |
      | started-build-event |
      | save-pipeline-created |
      | save-pipeline-team |
      | save-pipeline-paused |
      | save-pipeline-unpaused |
      | save-pipeline-unarchived |
      | save-pipeline-update-created |
      | save-pipeline-update-paused |
      | save-pipeline-update-unpaused |
      | save-pipeline-update-unarchives |
      | pipeline-lookup |
      | save-pipeline-same-name-across-teams |
