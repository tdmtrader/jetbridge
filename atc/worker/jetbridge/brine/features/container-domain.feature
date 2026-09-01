Feature: Container state transitions preserve the database lifecycle contract

  Source: all 20 specs in atc/db/container_test.go. These scenarios create
  real workers, builds, containers, and volumes in PostgreSQL, then invoke the
  production container objects. The two lost-database profiles use a second
  real connection that is closed after the object is loaded; no repository or
  SQL double participates.

  The lifecycle scenario covers the six read/transition specs: metadata on a
  creating container, creation, its two attached volumes, metadata after
  creation, transition to destroying, and metadata after that transition.
  The failed outline covers the eight Failed specs, including idempotency and
  refusal from later states. The destroy outline covers the three outcomes for
  each of the two terminal object types, for the remaining six specs.

  Scenario: A container carries its metadata and volumes through creation and destruction readiness
    When a real database container evaluates profile "lifecycle"
    Then the container domain result is "creating-metadata=true;created=true;created-metadata=true;volumes=true;destroying=true;destroying-metadata=true"

  Scenario Outline: Failing a <state> container produces <result>
    When a real database container evaluates profile "fail-<state>"
    Then the container domain result is "<result>"

    Examples:
      | state          | result                                      |
      | creating       | returned=true;error=false;state=failed       |
      | already-failed | returned=true;error=false;state=failed       |
      | created        | returned=false;error=true;state=created      |
      | destroying     | returned=false;error=true;state=destroying   |
      | closed         | returned=false;error=true;state=creating     |

  Scenario Outline: Destroying a <state> container when the database is <condition> produces <result>
    When a real database container evaluates profile "destroy-<state>-<condition>"
    Then the container domain result is "<result>"

    Examples:
      | state      | condition | result                                  |
      | destroying | live      | destroyed=true;error=false;present=false |
      | destroying | missing   | destroyed=false;error=true;present=false |
      | destroying | closed    | destroyed=false;error=true;present=true  |
      | failed     | live      | destroyed=true;error=false;present=false |
      | failed     | missing   | destroyed=false;error=true;present=false |
      | failed     | closed    | destroyed=false;error=true;present=true  |
