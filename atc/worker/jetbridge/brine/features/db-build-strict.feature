Feature: Persisted build behavior survives production defects

  Source subset: 27 of 95 leaves in atc/db/build_test.go. Every scenario
  creates concrete production database objects in fresh PostgreSQL and calls
  the real db.Build implementation.

  Scenario Outline: Strict DB build profile <profile>
    Given the strict real DB build evaluates profile "<profile>"
    Then the strict DB build observation is "<expected>"

    Examples:
      | profile                    | expected                                                                 |
      | created-by                 | brine-user                                                              |
      | one-off-no-plan            | has-plan=false                                                           |
      | create-time                | recent=true                                                              |
      | comment-round-trip         | empty=true;first=hello-world;second=updated-comment                      |
      | run-state                  | matches=true                                                             |
      | associated-team            | count=1;same=true                                                        |
      | resource-cache-user        | build-id=true                                                            |
      | container-owner            | build-id=true;plan-id=true;team-id=true                                  |
      | lager/one-off              | matches=true                                                             |
      | lager/job                  | matches=true                                                             |
      | lager/resource             | matches=true                                                             |
      | syslog/one-off             | matches=true                                                             |
      | syslog/job                 | matches=true                                                             |
      | syslog/resource            | matches=true                                                             |
      | tracing/one-off            | matches=true                                                             |
      | tracing/job                | matches=true                                                             |
      | tracing/resource           | matches=true                                                             |
      | reload                     | before=pending;found=true;after=started                                  |
      | drain/default              | drained=false                                                            |
      | drain/persisted            | immediate=true;found=true;reloaded=true                                  |
      | start/aborted-result       | started=false                                                            |
      | start/aborted-status       | found=true;status=pending                                                |
      | start/result               | started=true                                                             |
      | start/event                | type=status;version=1.0;id=0;status=started;time-matches=true             |
      | start/status               | found=true;status=started                                                |
      | start/public-plan          | found=true;has-plan=true;matches=true                                    |
      | pipeline                   | found=true;same-id=true;name=some-pipeline                               |
