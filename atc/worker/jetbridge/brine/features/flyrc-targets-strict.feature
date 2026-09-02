Feature: Fly target persistence through the production filesystem boundary

  Scenario Outline: Production flyrc target behavior — <profile>
    Given the production flyrc target profile "<profile>" is exercised
    Then the flyrc target observation exactly matches "<profile>"

    Examples:
      | profile                   |
      | default-team              |
      | fly-home-precedence       |
      | delete-one                |
      | delete-all                |
      | update-properties         |
      | rename-target             |
      | new-file-permissions      |
      | existing-file-permissions |
      | ca-set                    |
      | insecure-false            |
      | insecure-true             |
      | unknown-target            |
      | no-target                 |
