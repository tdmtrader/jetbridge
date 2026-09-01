Feature: CCTray XML reflects persisted pipeline and build state

  The strict rows use a production CC server, production authentication and
  concrete PostgreSQL state over a real TCP listener. Each row corresponds to
  one source leaf in atc/api/cc_test.go. Selective database-failure leaves and
  the separate anonymous-visibility contexts remain in the source suite.

  Scenario Outline: Strict CC profile <profile> preserves <source>
    When strict CC API profile "<profile>" is exercised over real HTTP
    Then the strict CC observation "<kind>" is "<expected>"

    Examples:
      | profile                | source                              | kind         | expected                                                                                                                                                                                                                                  |
      | succeeded-status       | successful build status             | status       | 200                                                                                                                                                                                                                                       |
      | succeeded-content-type | successful build content type       | content-type | application/xml                                                                                                                                                                                                                           |
      | succeeded-project      | successful build XML                | project      | activity=Sleeping;label=1;build-status=Success;time=2018-11-04T21:26:38Z;name=something-else/some-job;url=https://example.com/teams/a-team/pipelines/something-else/jobs/some-job                              |
      | aborted-project        | aborted build XML                   | project      | activity=Sleeping;label=1;build-status=Exception;time=2018-11-04T21:26:38Z;name=something-else/some-job;url=https://example.com/teams/a-team/pipelines/something-else/jobs/some-job                            |
      | errored-project        | errored build XML                   | project      | activity=Sleeping;label=1;build-status=Exception;time=2018-11-04T21:26:38Z;name=something-else/some-job;url=https://example.com/teams/a-team/pipelines/something-else/jobs/some-job                            |
      | failed-project         | failed build XML                    | project      | activity=Sleeping;label=1;build-status=Failure;time=2018-11-04T21:26:38Z;name=something-else/some-job;url=https://example.com/teams/a-team/pipelines/something-else/jobs/some-job                              |
      | building-project       | next build activity XML             | project      | activity=Building;label=1;build-status=Success;time=2018-11-04T21:26:38Z;name=something-else/some-job;url=https://example.com/teams/a-team/pipelines/something-else/jobs/some-job                              |
      | no-last-build-empty    | job without a finished build        | empty        | root=Projects;projects=0                                                                                                                                                                                                                  |
      | no-job-status          | pipeline without jobs status        | status       | 200                                                                                                                                                                                                                                       |
      | no-job-empty           | pipeline without jobs XML           | empty        | root=Projects;projects=0                                                                                                                                                                                                                  |
      | instanced-project      | instanced pipeline name and URL     | project      | activity=Sleeping;label=1;build-status=Success;time=2018-11-04T21:26:38Z;name=something-else/branch:'feature/foo'/some-job;url=https://example.com/teams/a-team/pipelines/something-else/jobs/some-job?vars.branch=%22feature%2Ffoo%22 |
      | no-pipeline-status     | team without pipelines status       | status       | 200                                                                                                                                                                                                                                       |
      | no-pipeline-empty      | team without pipelines XML          | empty        | root=Projects;projects=0                                                                                                                                                                                                                  |
      | missing-team-status    | missing authorized team             | status       | 404                                                                                                                                                                                                                                       |
