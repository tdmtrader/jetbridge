Feature: Fly rejects a team before attempting the requested operation

  Source: the two 25-row tables in fly/integration/error_handling_test.go.
  These are 50 initial specs. Fly is the real compiled CLI; GET team crosses a
  real TCP listener and rata route, production token verifier, accessor and
  authorization handler, team-scoped handler, and PostgreSQL. GET info is
  served by that same real HTTP server and reports the compiled production
  version used by fly.

  Scenario Outline: Every team-aware command explains a missing team — <command>
    Given fly targets a team the real API reports as "missing"
    When fly runs the "<command>" command with that team
    Then fly exits once with the matching team error

    Examples:
      | command              |
      | checklist            |
      | containers           |
      | trigger-job          |
      | expose-pipeline      |
      | hide-pipeline        |
      | hijack               |
      | jobs                 |
      | pause-job            |
      | pause-pipeline       |
      | unpause-job          |
      | unpause-pipeline     |
      | set-pipeline         |
      | destroy-pipeline     |
      | get-pipeline         |
      | order-pipelines      |
      | abort-build          |
      | archive-pipeline     |
      | resources            |
      | check-resource-type  |
      | check-resource       |
      | resource-versions    |
      | watch                |
      | clear-resource-cache |
      | clear-task-cache     |
      | rename-pipeline      |

  Scenario Outline: Every team-aware command explains a forbidden team — <command>
    Given fly targets a team the real API reports as "forbidden"
    When fly runs the "<command>" command with that team
    Then fly exits once with the matching team error

    Examples:
      | command              |
      | checklist            |
      | containers           |
      | trigger-job          |
      | expose-pipeline      |
      | hide-pipeline        |
      | hijack               |
      | jobs                 |
      | pause-job            |
      | pause-pipeline       |
      | unpause-job          |
      | unpause-pipeline     |
      | set-pipeline         |
      | destroy-pipeline     |
      | get-pipeline         |
      | order-pipelines      |
      | abort-build          |
      | archive-pipeline     |
      | resources            |
      | check-resource-type  |
      | check-resource       |
      | resource-versions    |
      | watch                |
      | clear-resource-cache |
      | clear-task-cache     |
      | rename-pipeline      |
