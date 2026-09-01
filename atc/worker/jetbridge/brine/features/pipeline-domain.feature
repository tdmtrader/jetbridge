Feature: Pipelines persist lifecycle and build state in PostgreSQL

  Source: 24 specs in atc/db/pipeline_test.go covering pause metadata, archive
  cleanup, unpause scheduling, destruction, jobs/builds, started builds,
  resource types, and config. Every scenario uses fresh PostgreSQL and concrete
  production db.Pipeline, db.Build, db.Job, and factory objects.

  Scenario Outline: Pipeline domain profile <profile> produces <result>
    When the real pipeline domain handles profile "<profile>"
    Then the pipeline domain result is "<result>"

    Examples:
      | profile                 | result                                                        |
      | check-unpaused          | paused=false                                                  |
      | check-paused            | paused=true                                                   |
      | pause                   | paused=true                                                   |
      | paused-by-empty         | paused-by=                                                    |
      | paused-by-user          | paused-by=concourse                                           |
      | paused-at               | within-one-second=true                                        |
      | archive-state           | archived=true                                                 |
      | archive-updated         | advanced=true                                                 |
      | archive-version         | version=0                                                     |
      | archive-jobs            | count=9;equal=true                                             |
      | archive-resources       | count=2;equal=true                                             |
      | archive-resource-types  | count=2;equal=true                                             |
      | archive-prototypes      | count=2;equal=true                                             |
      | unpause                 | paused=false                                                  |
      | unpause-schedules       | advanced=9                                                    |
      | destroy                 | pipeline=false;build=false;lookup=false                       |
      | destroy-marker          | marked=true                                                   |
      | jobs                    | job-name,some-other-job,a-job,shared-job,random-job,job-1,job-2,other-serial-group-job,different-serial-group-job |
      | builds                  | count=3;matches=true;one-off-excluded=true                    |
      | started-metadata        | id-positive=true;job=;pipeline=fake-pipeline;name=1;team=some-team;status=started |
      | started-public-plan     | equal=true                                                    |
      | started-event           | type=status;version=1.0;id=0;status=started;time-matches=true |
      | resource-types          | some-other-resource-type,some-resource-type                  |
      | config                  | equal=true                                                    |
