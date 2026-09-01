Feature: Persisted builds expose stable identity, state, and logging context

  Source: 24 specs in atc/db/build_test.go covering creation metadata,
  comments, one-off plan state, LagerData, SyslogTag, TracingAttrs, Reload,
  Drain, Start, MarkAsAborted, and Pipeline. Every build is created through a
  real team/job and every state transition is reloaded from PostgreSQL.

  Scenario Outline: Build domain profile <profile>
    Given the real build domain evaluates profile "<profile>"
    Then the build domain observation <comparison> "<expected>"

    Examples:
      | profile              | comparison | expected                                                                  |
      | creation-metadata    | contains   | created-by=brine-user ;; recent=true ;; teams=build-team ;; owner-plan=some-plan |
      | one-off-no-plan      | is         | has-plan=false                                                            |
      | comment-round-trip   | is         | first=hello-world;second=updated-comment                                  |
      | lager/one-off        | contains   | build=1 ;; team=build-team                                                |
      | lager/job            | contains   | build=1 ;; job=some-job ;; pipeline=some-pipeline ;; team=build-team      |
      | syslog/one-off       | contains   | build-team/                                                               |
      | syslog/job           | is         | build-team/some-pipeline/some-job/1/origin                                |
      | tracing/one-off      | contains   | build=1 ;; team_name=build-team                                           |
      | tracing/job          | contains   | build=1 ;; job=some-job ;; pipeline=some-pipeline ;; team_name=build-team |
      | reload-after-start   | is         | started=true;before=pending;found=true;after=started                       |
      | drain                | is         | before=false;immediate=true;reloaded=true                                 |
      | start-aborted        | is         | started=false;status=pending                                              |
      | start-success        | is         | started=true;status=started;has-plan=true;public-plan=true                |
      | mark-aborted         | is         | aborted=true                                                              |
      | pipeline             | is         | found=true;name=some-pipeline                                             |
