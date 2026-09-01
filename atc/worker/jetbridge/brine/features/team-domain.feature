Feature: Teams own durable workers, pipelines, authorization, and one-off builds

  Source: 30 specs in atc/db/team_test.go: Delete, Rename, SaveWorker,
  Workers, UpdateProviderAuth, Pipelines, PublicPipelines, OrderPipelines,
  CreateOneOffBuild, Pipeline lookup, basic SavePipeline state, and
  RenamePipeline. All observations come from fresh PostgreSQL reads and real
  db.Team/db.Worker/db.Pipeline/db.Build objects.

  Scenario Outline: Team domain profile <profile>
    Given the real team domain evaluates profile "<profile>"
    Then the team domain observation <comparison> "<expected>"

    Examples:
      | profile                                | comparison | expected                                                        |
      | delete                                 | is         | found=false;event-table=false                                   |
      | rename                                 | is         | renamed-found=true                                              |
      | worker-overwrite                       | is         | name=worker;state=running                                       |
      | worker-cross-team                      | contains   | error:                                                          |
      | workers/team-and-global                | is         | team-worker,global-worker                                       |
      | workers/excludes-other-team            | is         |                                                                |
      | workers/empty                          | is         | count=0                                                        |
      | auth/save-and-clear-legacy             | is         | owner=local:username;legacy-valid=false                         |
      | auth/override                          | is         | owner=local:new;roles=1                                         |
      | pipelines/list                         | is         | fake-pipeline@master,fake-pipeline@feature/foo,fake-pipeline-two |
      | pipelines/grouped-order                | is         | fake-pipeline@master,fake-pipeline@feature/foo,fake-pipeline@other,fake-pipeline-two |
      | pipelines/empty                        | is         | count=0                                                        |
      | public-pipelines/one                   | is         | public                                                         |
      | public-pipelines/empty                 | is         | count=0                                                        |
      | order-pipelines                        | is         | mine=pipeline2,pipeline1;theirs=pipeline1,pipeline2             |
      | order-pipelines/missing                | contains   | missing                                                        |
      | one-off-build                          | is         | id=true;name=1;team=some-team;job=;pipeline=;status=pending     |
      | pipeline/instance-lookup               | is         | found=true;same-id=true                                         |
      | pipeline/name-does-not-match-instance  | is         | found=false;nil=true                                            |
      | pipeline/named-wins                    | is         | found=true;same-id=true                                         |
      | save-pipeline/default                  | is         | created=true;team-id=true;paused=false;archived=false           |
      | save-pipeline/paused                   | is         | paused=true                                                     |
      | rename-pipeline/one                    | is         | found=true;name=new                                             |
      | rename-pipeline/instances              | is         | found=true;names=new,new,new                                    |
      | rename-pipeline/missing                | is         | found=false                                                     |
