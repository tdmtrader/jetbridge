Feature: Jobs persist state, builds, cursors, and pending work in PostgreSQL

  Source: 35 specs in atc/db/job_test.go covering configuration flags, pause
  metadata, logged/completed IDs, build lookup and pagination, time cursors,
  pending-build idempotence, and new-input state through production db.Job methods.

  Scenario Outline: Job domain profile <profile> produces <result>
    When the real job domain handles profile "<profile>"
    Then the job domain result is "<result>"

    Examples:
      | profile               | result                                               |
      | public-true           | public=true                                          |
      | public-false          | public=false                                         |
      | public-default        | public=false                                         |
      | disable-true          | disabled=true                                        |
      | disable-false         | disabled=false                                       |
      | unpaused              | paused=false                                         |
      | pause-state           | paused=true                                          |
      | pause-no-schedule     | unchanged=true                                       |
      | pause-by-empty        | paused-by=                                           |
      | pause-at              | recent=true                                          |
      | pause-by-user         | paused-by=concourse                                  |
      | unpause-state         | paused=false                                         |
      | unpause-schedule      | within-second=true                                   |
      | first-logged          | id=57;same-ok=true;decrease-exact=true               |
      | latest-completed      | matches=true                                         |
      | build-latest          | found=true;matches=true;status=pending:true          |
      | build-exact           | found=true;matches=true;status=pending:true          |
      | build-missing         | found=false;nil=true                                 |
      | build-latest-missing  | found=false;nil=true                                 |
      | build-create-schedule | advanced=true                                        |
      | builds-empty          | builds-exact=true;pagination-exact=true              |
      | builds-first          | builds-exact=true;pagination-exact=true              |
      | builds-to-middle      | builds-exact=true;pagination-exact=true              |
      | builds-to-end         | builds-exact=true;pagination-exact=true              |
      | builds-from-middle    | builds-exact=true;pagination-exact=true              |
      | builds-from-start     | builds-exact=true;pagination-exact=true              |
      | time-empty            | builds-exact=true                                    |
      | time-limit            | builds-exact=true                                    |
      | time-to               | builds-exact=true                                    |
      | time-from             | builds-exact=true                                    |
      | time-range            | builds-exact=true                                    |
      | ensure-pending        | pending=1;next-matches=true                          |
      | ensure-idempotent     | pending=1;next-matches=true;started=true;after-start=0 |
      | new-inputs-initial    | new=false                                            |
      | new-inputs-toggle     | true=true;false=true                                 |
