Feature: Resource-scoped API authorization uses persisted ownership

  Source: 33 specs from check_pipeline_access_handler_test.go (10 of 12),
  check_build_write_access_handler_test.go (all 7),
  check_worker_team_access_handler_test.go (all 16), and 13 of the 17 source
  specs in check_build_read_access_handler_test.go. The omitted specs inject a
  selective late Pipeline or Job lookup result that cannot be produced by a
  healthy real database after BuildForAPI has already returned its object.

  Each scenario uses real teams, pipelines, jobs, builds, workers, roles, and
  access_tokens in PostgreSQL. Database-error cases use a genuinely closed
  connection. A successful response is accepted only if the production
  handler supplied the expected persisted object in its delegate context.

  Scenario Outline: Pipeline access follows visibility and persisted team roles — <case>
    Given the real "pipeline" resource boundary receives case "<case>"
    Then the auth response status is <status>
    And the auth delegate was <delegate>

    Examples:
      | case                 | status | delegate   |
      | team-error           | 500    | not reached |
      | team-missing         | 404    | not reached |
      | public               | 200    | reached     |
      | private-authorized   | 200    | reached     |
      | private-other-team   | 403    | not reached |
      | private-anonymous    | 401    | not reached |
      | pipeline-missing     | 404    | not reached |

  Scenario Outline: Build write access follows persisted build ownership — <case>
    Given the real "build-write" resource boundary receives case "<case>"
    Then the auth response status is <status>
    And the auth delegate was <delegate>

    Examples:
      | case         | status | delegate    |
      | same-team    | 200    | reached     |
      | missing      | 404    | not reached |
      | lookup-error | 500    | not reached |
      | other-team   | 403    | not reached |
      | weak-role    | 403    | not reached |
      | anonymous    | 401    | not reached |

  Scenario Outline: Worker access follows persisted worker ownership — <case>
    Given the real "worker" resource boundary receives case "<case>"
    Then the auth response status is <status>
    And the auth delegate was <delegate>

    Examples:
      | case          | status | delegate    |
      | anonymous     | 401    | not reached |
      | team-admin    | 200    | reached     |
      | team-system   | 200    | reached     |
      | team-match    | 200    | reached     |
      | team-other    | 403    | not reached |
      | global-admin  | 200    | reached     |
      | global-member | 403    | not reached |
      | missing       | 404    | not reached |
      | lookup-error  | 500    | not reached |

  Scenario Outline: Build read access follows pipeline and job visibility — <case>
    Given the real "build-read" resource boundary receives case "<case>"
    Then the auth response status is <status>
    And the auth delegate was <delegate>

    Examples:
      | case                                        | status | delegate    |
      | any/same-team/public-pipeline               | 200    | reached     |
      | any/same-team/missing                       | 404    | not reached |
      | any/same-team/lookup-error                  | 500    | not reached |
      | any/other-team/public-pipeline              | 200    | reached     |
      | any/other-team/private-pipeline             | 403    | not reached |
      | any/other-team/one-off                      | 403    | not reached |
      | any/anonymous/public-pipeline               | 200    | reached     |
      | any/anonymous/private-pipeline              | 401    | not reached |
      | any/anonymous/one-off                       | 401    | not reached |
      | public-job-only/same-team/private-job       | 200    | reached     |
      | public-job-only/same-team/missing           | 404    | not reached |
      | public-job-only/same-team/lookup-error      | 500    | not reached |
      | public-job-only/other-team/one-off          | 403    | not reached |
      | public-job-only/other-team/public-job       | 200    | reached     |
      | public-job-only/other-team/private-job      | 403    | not reached |
      | public-job-only/other-team/private-pipeline | 403    | not reached |
      | public-job-only/anonymous/one-off           | 401    | not reached |
      | public-job-only/anonymous/public-job        | 200    | reached     |
      | public-job-only/anonymous/private-job       | 401    | not reached |
      | public-job-only/anonymous/private-pipeline  | 401    | not reached |
