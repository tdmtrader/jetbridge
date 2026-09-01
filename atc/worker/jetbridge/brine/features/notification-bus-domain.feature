Feature: Notification subscribers observe the real PostgreSQL bus

  Source: 18 of 23 specs in atc/db/notifications_bus_test.go. The source uses a
  stub executor and listener for 21 of them and adds two PostgreSQL checks at
  the end. These scenarios replace the stubs with DbConn.Bus throughout, while
  notification pressure replaces the two callback-driven deadlock tests.

  Source disposition by profile: round-trip covers Notify's statement/success,
  ListenSignal's first-listener/success, and the PostgreSQL delivery spec (6);
  wrong-channel covers PostgreSQL selectivity (1); same-channel covers listen-once,
  fan-out, multi-listener unlisten success/selectivity, and delivery after one
  unsubscribe (5); single-unlisten covers the last-listener unsubscribe and
  success (2); different-channels covers routing (1); coalescing covers both queue semantics (2);
  pressure covers both non-deadlock specs (2). Several source assertions share
  one externally observable round trip, so the 24 dispositions intentionally
  include the source's final PostgreSQL delivery spec already represented by
  round-trip. The three injected errors and two synthetic nil-notification
  reconnect specs stay in Go: calling a production listener after Close is not
  a database failure, it is an invalid use that blocks on its closed control
  channel, while a real reconnect requires interrupting the suite postmaster.

  Scenario Outline: Notification bus profile <profile> produces <result>
    When the real PostgreSQL notification bus evaluates profile "<profile>"
    Then the notification bus result is "<result>"

    Examples:
      | profile            | result                                               |
      | round-trip         | first=true;second=false                              |
      | wrong-channel      | received=false                                       |
      | same-channel       | first=true;second=true;after-unlisten=true            |
      | single-unlisten    | error=false;received=false                           |
      | different-channels | first=true;second=false                              |
      | coalescing         | one=true;extra=false;again=true                      |
      | pressure           | listen=true;unlisten=true                            |
