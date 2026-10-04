Feature: Trying the likeliest change first
  When a batch fails, the failed tests and the files each change touched can
  point at one change as the likely cause. Each change is ranked by how well
  the failed test names point at the directories it touched.

  Scenario: Suspects are ranked from the failed test names and the files each change touched
    Given changes "a", "b" and "c" touched different directories
    When the failed tests name the directories "c" and "b" touched, "c" more often
    Then "c" is the top suspect, then "b"
    And "a" is not a suspect
