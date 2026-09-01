Feature: Team database methods preserve durable team boundaries

  Source subset: 18 of 133 leaves in atc/db/team_test.go. Every scenario uses
  fresh PostgreSQL and the production db.Team, db.Pipeline, and factory objects.

  Scenario Outline: Strict DB team profile <profile>
    Given the strict real DB team evaluates profile "<profile>"
    Then the strict DB team observation is "<expected>"

    Examples:
      | profile                                | expected                                                        |
      | delete/team                            | found=false                                                     |
      | delete/event-table                     | event-table=false                                               |
      | rename                                 | renamed-found=true                                              |
      | workers/team-and-global                | team-worker,global-worker                                       |
      | workers/excludes-other-team            |                                                                |
      | pipelines/list                         | fake-pipeline@master,fake-pipeline@feature/foo,fake-pipeline-two |
      | pipelines/grouped-order                | fake-pipeline@master,fake-pipeline@feature/foo,fake-pipeline@other,fake-pipeline-two |
      | pipelines/empty                        | count=0                                                        |
      | public-pipelines/one                   | public                                                         |
      | public-pipelines/empty                 | count=0                                                        |
      | order-pipelines                        | mine=pipeline2,pipeline1;theirs=pipeline1,pipeline2             |
      | order-pipelines/missing                | error:pipeline 'missing' not found                              |
      | pipeline/instance-lookup               | found=true;same-id=true                                         |
      | pipeline/name-does-not-match-instance  | found=false;nil=true                                            |
      | pipeline/named-wins                    | found=true;same-id=true                                         |
      | rename-pipeline/one                    | found=true;name=new                                             |
      | rename-pipeline/instances              | found=true;names=new,new,new                                    |
      | rename-pipeline/missing                | found=false                                                     |
