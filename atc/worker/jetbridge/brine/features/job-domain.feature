Feature: Jobs persist state, builds, cursors, and pending work in PostgreSQL

  Source: 35 specs in atc/db/job_test.go covering configuration flags, pause
  metadata, logged/completed IDs, build lookup and pagination, time cursors,
  pending-build idempotence, and new-input state. No db.Job fake is used.

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
      | unpause-schedule      | advanced=true                                        |
      | first-logged          | id=57;same-ok=true;decrease-error=true               |
      | latest-completed      | matches=true                                         |
      | build-latest          | found=true;matches=true                              |
      | build-exact           | found=true;matches=true                              |
      | build-missing         | found=false;nil=true                                 |
      | build-latest-missing  | found=false;nil=true                                 |
      | build-create-schedule | advanced=true                                        |
      | builds-empty          | count=0;empty-pages=true                             |
      | builds-first          | count=2;newer=false;older=true;pages-correct=true    |
      | builds-to-middle      | count=2;newer=true;older=true;pages-correct=true     |
      | builds-to-end         | count=2;newer=true;older=false;pages-correct=true    |
      | builds-from-middle    | count=2;newer=true;older=true;pages-correct=true     |
      | builds-from-start     | count=2;newer=false;older=true;pages-correct=true    |
      | time-empty            | count=0;correct=true                                 |
      | time-limit            | count=2;correct=true                                 |
      | time-to               | count=3;correct=true                                 |
      | time-from             | count=3;correct=true                                 |
      | time-range            | count=2;correct=true                                 |
      | ensure-pending        | pending=1                                            |
      | ensure-idempotent     | pending=1                                            |
      | new-inputs-initial    | new=false                                            |
      | new-inputs-toggle     | true=true;false=true                                 |
