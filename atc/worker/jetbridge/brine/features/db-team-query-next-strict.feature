Feature: Remaining team queries preserve durable PostgreSQL ownership boundaries

  Each scenario creates concrete rows through production factories, calls a
  production db.Team method, and compares the returned object identities and
  persisted state. No test doubles or recording loggers participate.

  Scenario Outline: Remaining DB team query behavior — <profile>
    Given the remaining production team query "<profile>" is exercised
    Then the remaining production team query exactly matches "<profile>"

    Examples:
      | profile |
      | metadata-full-state-set |
      | metadata-partial-filter |
      | metadata-empty-filter |
      | metadata-team-boundary |
      | find-container-present |
      | artifact-volume-present |
      | container-worker-creating |
      | container-worker-created |
      | private-builds-pagination |
      | private-builds-team-boundary |
      | time-builds-zero-limit |
      | builds-from-past-end |
      | builds-invalid-range |
      | check-containers-present |
      | check-containers-shared-config |
      | is-check-container-check |
      | is-check-container-task |
      | check-container-inside-team |
      | check-container-outside-team |
      | task-container-inside-team |
      | task-container-outside-team |
      | cache-running-worker |
      | cache-stalled-worker |
      | cache-two-running-workers |
      | cache-before-prune-workers |
      | cache-before-prune-volume |
      | cache-before-prune-row |
      | cache-after-prune-workers |
      | cache-after-prune-volume |
      | cache-after-prune-row |
