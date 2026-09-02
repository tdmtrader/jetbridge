Feature: Remaining job scheduling and lifecycle state uses durable PostgreSQL behavior

  Every scenario creates production teams, pipelines, jobs, builds, and caches
  through concrete database factories. No builder, recorder, observer channel,
  trace exporter, test logger, stub, fake, or mock participates.

  Scenario Outline: Remaining DB job behavior — <profile>
    Given the remaining production job behavior "<profile>" is exercised
    Then the remaining production job behavior exactly matches "<profile>"

    Examples:
      | profile |
      | schedule-basic |
      | schedule-persists |
      | schedule-max-one-allowed |
      | schedule-serial-finished-allowed |
      | schedule-serial-earlier-subject-allowed |
      | schedule-serial-succeeded-ignored |
      | schedule-pipeline-paused |
      | schedule-job-paused |
      | schedule-job-paused-inputs |
      | schedule-missing-build |
      | lifecycle-cross-pipeline-scope |
      | lifecycle-scheduled-remains-pending |
      | lifecycle-two-pending-order |
      | lifecycle-rerun-old-order |
      | lifecycle-rerun-newest-order |
      | lifecycle-multiple-rerun-order |
      | lifecycle-start-state-plan |
      | lifecycle-start-time |
      | lifecycle-finish-state-time |
      | cache-missing-path-zero |
      | cache-missing-step-zero |
      | cache-missing-step-preserves |
      | cache-step-preserves-other-job |
