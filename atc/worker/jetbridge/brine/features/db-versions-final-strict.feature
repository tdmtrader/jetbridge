Feature: Production versions database pagination

  Scenario: SuccessfulBuilds returns its sole build and a zero-valued end marker
    When the production versions database profile "successful-one" is exercised
    Then the versions database behavior exactly matches "successful-one"

  Scenario: SuccessfulBuilds returns a full page newest first and then ends
    When the production versions database profile "successful-page-limit" is exercised
    Then the versions database behavior exactly matches "successful-page-limit"

  Scenario: SuccessfulBuilds keeps reruns with their failed original across a filler page
    When the production versions database profile "successful-reruns-filler" is exercised
    Then the versions database behavior exactly matches "successful-reruns-filler"

  Scenario: SuccessfulBuilds orders successful reruns relative to their failed original
    When the production versions database profile "successful-reruns-failed-origin" is exercised
    Then the versions database behavior exactly matches "successful-reruns-failed-origin"

  Scenario: SuccessfulBuilds ends after a successful rerun of a failed boundary build
    When the production versions database profile "successful-failed-boundary" is exercised
    Then the versions database behavior exactly matches "successful-failed-boundary"

  Scenario: SuccessfulBuilds returns a succeeded boundary original after its rerun
    When the production versions database profile "successful-succeeded-boundary" is exercised
    Then the versions database behavior exactly matches "successful-succeeded-boundary"

  Scenario: SuccessfulBuilds returns every rerun that crosses the page boundary
    When the production versions database profile "successful-multiple-reruns-boundary" is exercised
    Then the versions database behavior exactly matches "successful-multiple-reruns-boundary"

  Scenario: UnusedBuilds returns its sole cursor build and a zero-valued end marker
    When the production versions database profile "unused-one" is exercised
    Then the versions database behavior exactly matches "unused-one"

  Scenario: UnusedBuilds returns newer builds ascending then cursor and older builds descending
    When the production versions database profile "unused-older-newer" is exercised
    Then the versions database behavior exactly matches "unused-older-newer"

  Scenario: UnusedBuilds excludes reruns of a non-rerun cursor
    When the production versions database profile "unused-reruns-excluded" is exercised
    Then the versions database behavior exactly matches "unused-reruns-excluded"

  Scenario: UnusedBuilds orders builds around a rerun cursor
    When the production versions database profile "unused-rerun-cursor" is exercised
    Then the versions database behavior exactly matches "unused-rerun-cursor"

  Scenario: FindVersionOfResource matches a partial version and returns its SHA256 digest
    When the production versions database profile "find-partial-version" is exercised
    Then the versions database behavior exactly matches "find-partial-version"
