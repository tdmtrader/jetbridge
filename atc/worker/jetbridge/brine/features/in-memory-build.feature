Feature: In-memory checks become persisted build history when they start

  Source: all 24 specs in atc/db/build_in_memory_check_test.go. Scenarios use a
  real resource, event tables, advisory locks, and PostgreSQL-backed build
  factory; the original suite's repeated assertions are grouped by lifecycle.

  Scenario: A new in-memory build exposes its pre-persistence identity
    When a real in-memory check build evaluates profile "creation"
    Then the in-memory build result is "id=0;name=check;pending=true;running=true;manual=false;plan=true;lager=true;trace=true;span=true;events-error=true"

  Scenario: Successful completion before check start never allocates a build ID
    When a real in-memory check build evaluates profile "prestart-success"
    Then the in-memory build result is "events-error=true;id=0"

  Scenario: Errored completion before check start updates summary and event state
    When a real in-memory check build evaluates profile "prestart-error"
    Then the in-memory build result is "summary=errored;event=status;id=3"

  Scenario: Starting a check persists identity, events, ownership, and locking
    When a real in-memory check build evaluates profile "started"
    Then the in-memory build result is "id-positive=true;summary=succeeded;events=status,initialize,start;log=log;cache=true;owner=true;run-state=in-memory-check-build:1;second-lock=false"

  Scenario: The API build factory reconstructs an in-memory check from its scope
    When a real in-memory check build evaluates profile "api"
    Then the in-memory build result is "found=true;id=1999;name=check;running=false;plan=true;events=true"
