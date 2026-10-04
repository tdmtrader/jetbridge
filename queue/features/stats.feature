Feature: A fixed set of queue stats
  The queue folds its settle records and current state into one fixed summary,
  so an operator can read how it is doing without choosing anything.

  Scenario: The stats count changes landed in the last hour from settle records
    Given three changes landed within the last hour and one landed two hours ago
    When the stats are read for the last hour
    Then three changes are counted as landed
    And the landed per hour rate is three

  Scenario: Admitted per hour counts admissions, not rows
    Given one change was admitted, paused once, and then landed within the hour
    When the stats are read for the last hour
    Then one change is counted as admitted

  Scenario: The stats count landings and time their walk
    Given two changes landed together and a third landed later in the last hour
    When the stats are read for the last hour
    Then two landings are counted per hour
    And the walk time runs from the earliest admit in each landing to its land
