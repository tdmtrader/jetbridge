Feature: Composing ahead and the tested base
  Every run records the main it was tested on: main's head when it started,
  or the candidate of the run in flight it was composed ahead on. A verdict
  is used only when that base is still the main the run would land on; when
  it is not, the run is recomposed: nothing lands, nothing is ejected and no
  retry is spent.

  Scenario: A run composed ahead is tested on its base run's candidate and used only after that run lands
    Given changes "a" and "b" are queued and two runs may be in flight
    When "b" is composed ahead on the run testing "a" and its verdict comes in first
    Then "b" was tested on the candidate of the run testing "a"
    And "a" lands, then "b" lands, with no recompose

  Scenario: A run composed ahead on a run that goes red is recomposed, never landed or ejected
    Given "b" is composed ahead on the run testing "a"
    When "a" fails
    Then "a" is ejected and "b" is recorded as a recompose naming the base it was tested on
    And "b" is composed again on main and lands

  Scenario: A green run tested on a main that has since moved is recomposed and lands on the new main
    Given change "a" is being tested on main
    When main moves outside the queue and "a" passes
    Then "a" is recorded as a recompose naming the old and the new main
    And "a" is tested again on the new main and lands

  Scenario: A red run tested on a main that has since moved ejects nothing and is tested again
    Given change "a" is being tested on main
    When main moves outside the queue and "a" fails
    Then nothing is ejected and "a" is recorded as a recompose
    And "a" is ejected only once it fails on the new main

  Scenario: A run whose main cannot be read now is recomposed, never landed
    Given change "a" is being tested on main
    When main cannot be read as "a" passes
    Then "a" is recorded as a recompose and nothing lands
    And "a" lands once main can be read again
