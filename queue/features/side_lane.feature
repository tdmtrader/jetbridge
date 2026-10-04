Feature: Side lane: a red stands only with the changes it was tested with
  A change is ejected only after it fails on exactly the main it would land
  on, composed with exactly the changes that would land before it, no more
  and no fewer. (A run tested on a main that has since moved is recomposed:
  see compose_ahead.feature.) When the changes ahead differ, a red is no
  verdict: the change is recomposed and tested again; nothing is ejected and
  no retry is spent.

  Scenario: A red tested with exactly the changes that would land before it stands
    Given change "c" was tested with "a" and "b" ahead of it
    When its red comes in and "a" and "b" would land before it
    Then the red stands and "c" may be ejected

  Scenario: A red tested without a change that now lands before it is recomposed, not blamed
    Given change "c" was tested with "a" and "b" ahead of it
    When its red comes in and "a", "b" and "x" would land before it
    Then "c" is recomposed, naming the changes it was tested with and those ahead now

  Scenario: A red tested with a change that no longer lands before it is recomposed, not blamed
    Given change "c" was tested with "a" and "b" ahead of it
    When its red comes in and only "a" would land before it
    Then "c" is recomposed, naming the changes it was tested with and those ahead now
