Feature: Pipelines persist lifecycle and build state in PostgreSQL

  Source: 24 specs in atc/db/pipeline_test.go covering pause metadata, archive
  cleanup, unpause scheduling, destruction, jobs/builds, started builds,
  resource types, and config. All scenarios use real db.Pipeline objects.

  Scenario Outline: Pipeline domain profile <profile> produces <result>
    When the real pipeline domain handles profile "<profile>"
    Then the pipeline domain result is "<result>"

    Examples:
      | profile                 | result                                                        |
      | check-unpaused          | paused=false                                                  |
      | check-paused            | paused=true                                                   |
      | pause                   | paused=true                                                   |
      | paused-by-empty         | paused-by=                                                    |
      | paused-by-user          | paused-by=brine-user                                          |
      | paused-at               | recent=true                                                   |
      | archive-state           | archived=true                                                 |
      | archive-updated         | advanced=true                                                 |
      | archive-version         | version=0                                                     |
      | archive-jobs            | count=2;empty=true                                             |
      | archive-resources       | count=1;empty=true                                             |
      | archive-resource-types  | count=1;source-empty=true                                      |
      | archive-prototypes      | count=1;source-empty=true                                      |
      | unpause                 | paused=false                                                  |
      | unpause-schedules       | advanced=2                                                    |
      | destroy                 | pipeline=false;build=false                                    |
      | destroy-marker          | marked=true                                                   |
      | jobs                    | count=2                                                       |
      | builds                  | count=2                                                       |
      | started-metadata        | id-positive=true;pipeline=pipeline;team=pipeline-domain;status=started |
      | started-public-plan     | equal=true                                                    |
      | started-event           | events=1                                                      |
      | resource-types          | count=1                                                       |
      | config                  | equal=true                                                    |
