Feature: Remaining container repository behavior over real PostgreSQL

  Scenario Outline: Live container repository profile <profile> preserves <observation>
    When the live production container repository evaluates "<profile>"
    Then its exact remaining observation is "<observation>"

    Examples:
      | profile                   | observation                                        |
      | live-check-session        | creating=0;created=0;destroying=0;error=false       |
      | refreshed-worker-type     | creating=0;created=0;destroying=0;error=false       |
      | no-failed-count           | affected=0                                         |
      | unknown-preserves-created | created=some-handle2;error=false                    |
