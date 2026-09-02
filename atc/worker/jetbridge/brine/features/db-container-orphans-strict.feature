Feature: Container repository orphan discovery

  Scenario Outline: Orphan profile <profile> has observation <observation>
    When the production orphan repository evaluates profile "<profile>"
    Then the orphan repository observation is "<observation>"

    Examples:
      | profile                         | observation                              |
      | check-expired-creating          | creating=1;created=0;destroying=0;error=false |
      | check-expired-created           | creating=0;created=1;destroying=0;error=false |
      | check-expired-destroying        | creating=0;created=0;destroying=1;error=false |
      | check-config-cleaned            | creating=1;created=0;destroying=0;error=false |
      | check-worker-version-changed    | creating=1;created=0;destroying=0;error=false |
      | build-interceptible             | creating=0;created=0;destroying=0;error=false |
      | build-noninterceptible-creating | creating=1;created=0;destroying=0;error=false |
      | build-noninterceptible-created  | creating=0;created=1;destroying=0;error=false |
      | build-noninterceptible-destroying | creating=0;created=0;destroying=1;error=false |
      | build-deleted                   | creating=1;created=0;destroying=0;error=false |
      | memory-running                  | creating=0;created=0;destroying=0;error=false |
      | memory-finished                 | creating=0;created=0;destroying=1;error=false |
