Feature: Container state transitions preserve the database lifecycle contract

  Source: 17 externally observable specs in atc/db/container_test.go. Every row
  uses real PostgreSQL, production Lager, production WorkerCache/WorkerFactory,
  and production container and volume objects. The three closed-connection
  error specs remain in Ginkgo because invalidating the SUT's database handle
  is prohibited fault injection for this migration.

  Scenario Outline: Container profile <profile> produces <result>
    When a real database container evaluates profile "<profile>"
    Then the container domain result is "<result>"

    Examples:
      | profile                        | result                                                       |
      | creating-metadata              | equal=true                                                   |
      | created-return                 | returned=true;error=false;state=created                       |
      | created-volumes                | count=2;handles=true;paths=true                               |
      | created-metadata               | equal=true                                                   |
      | destroying-return              | returned=true;error=false;state=destroying                    |
      | destroying-metadata            | equal=true                                                   |
      | fail-creating-state            | failed-count=1;handle-present=true;returned=true;state=failed |
      | fail-creating-error            | error=false                                                  |
      | fail-already-state             | failed-count=1;handle-present=true;returned=true;state=failed |
      | fail-already-error             | error=false                                                  |
      | fail-created-not-marked        | failed-count=0;returned=false;state=created                   |
      | fail-destroying-not-marked     | failed-count=0;returned=false;state=destroying                |
      | fail-destroying-error          | error=true                                                   |
      | destroy-destroying-live        | destroyed=true;error=false;present=false                      |
      | destroy-destroying-missing     | destroyed=false;error=true;present=false                      |
      | destroy-failed-live            | destroyed=true;error=false;present=false                      |
      | destroy-failed-missing         | destroyed=false;error=true;present=false                      |
