Feature: Resource config check-session lifecycle over real PostgreSQL

  Scenario Outline: Production inactive check-session cleanup — <profile>
    Given the production inactive check-session cleanup profile "<profile>" is exercised
    Then the inactive check-session cleanup observation exactly matches "<profile>"

    Examples:
      | profile                 |
      | active-resource         |
      | inactive-resource       |
      | paused-resource         |
      | active-resource-type    |
      | inactive-resource-type  |
      | paused-resource-type    |
      | active-prototype        |
      | inactive-prototype      |
      | paused-prototype        |
