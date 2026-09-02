Feature: Remaining jobs API public listing and badge behavior

  Every request crosses a real TCP listener, production routing and access
  wrappers, and PostgreSQL-backed teams, pipelines, jobs, and builds.

  Scenario Outline: Remaining jobs API production behavior — <profile>
    Given the remaining production jobs API behavior "<profile>" is exercised
    Then the remaining production jobs API behavior exactly matches "<profile>"

    Examples:
      | profile |
      | list-public-only |
      | list-member-private |
      | list-empty |
      | get-public-anonymous |
      | get-public-outsider |
      | badge-public-outsider |
      | badge-buildless |
      | badge-errored |
      | badge-failed |
      | badge-succeeded |
      | badge-aborted |
      | badge-default-omitted |
      | badge-default-empty |
      | badge-scale-short |
      | badge-scale-medium |
      | badge-scale-long |
      | badge-production-title |
      | badge-status-width |
      | dashboard-empty |
      | dashboard-public-anonymous |
      | builds-public-outsider |
