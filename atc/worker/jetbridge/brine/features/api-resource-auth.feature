Feature: Resource-scoped API authorization uses persisted ownership

  Source: 29 exact leaves from check_pipeline_access_handler_test.go,
  check_build_write_access_handler_test.go, and
  check_worker_team_access_handler_test.go. Four closed-connection leaves remain
  in Ginkgo because deliberately invalidating the SUT database handle is
  prohibited fault injection. Two selective late pipeline-lookup leaves were
  already outside this cohort because a healthy database cannot fail only that
  lookup after returning the team.

  Every row uses real PostgreSQL, the production access verifier, a real TCP
  listener/http.Server, production route parsing, and a production downstream
  API handler. Downstream observations are response data or persisted state,
  never a call recorder.

  Scenario Outline: Strict resource authorization profile <profile> preserves <source>
    Given strict resource authorization profile "<profile>" is exercised over real HTTP
    Then the strict resource authorization result is "<result>"

    Examples:
      | profile                             | source                       | result                                           |
      | pipeline-team-missing-status        | pipeline team missing status | status=404                                       |
      | pipeline-public-context             | public pipeline context       | status=200;pipeline=some-pipeline;team=some-team |
      | pipeline-public-status              | public pipeline status        | status=200                                       |
      | pipeline-private-authorized-context | private authorized context    | status=200;pipeline=some-pipeline;team=some-team |
      | pipeline-private-authorized-status  | private authorized status     | status=200                                       |
      | pipeline-private-other-status       | private other-team status     | status=403                                       |
      | pipeline-private-anonymous-status   | private anonymous status      | status=401                                       |
      | pipeline-missing-status             | missing pipeline status       | status=404                                       |
      | pipeline-missing-downstream         | missing pipeline downstream   | status=404;guard-worker-present=true             |
      | build-same-team-status              | same-team build status        | status=204                                       |
      | build-same-team-context             | same-team build context       | status=204;build-aborted=true                    |
      | build-missing-status                | missing build status          | status=404                                       |
      | build-other-team-status             | other-team build status       | status=403                                       |
      | build-weak-role-status              | weak-role build status        | status=403                                       |
      | build-anonymous-status              | anonymous build status        | status=401                                       |
      | worker-anonymous-status             | anonymous worker status       | status=401                                       |
      | worker-anonymous-downstream         | anonymous worker downstream   | status=401;worker-present=true                   |
      | worker-team-admin-downstream        | team admin downstream         | status=200;worker-present=false                  |
      | worker-team-admin-status            | team admin status             | status=200                                       |
      | worker-system-downstream            | system claim downstream       | status=200;worker-present=false                  |
      | worker-team-match-downstream        | matching team downstream      | status=200;worker-present=false                  |
      | worker-team-other-downstream        | other team downstream         | status=403;worker-present=true                   |
      | worker-team-other-status            | other team status             | status=403                                       |
      | worker-global-admin-downstream      | global admin downstream       | status=200;worker-present=false                  |
      | worker-global-admin-status          | global admin status           | status=200                                       |
      | worker-global-member-downstream     | global member downstream      | status=403;worker-present=true                   |
      | worker-global-member-status         | global member status          | status=403                                       |
      | worker-missing-downstream           | missing worker downstream     | status=404;worker-present=false                  |
      | worker-missing-status               | missing worker status         | status=404                                       |
