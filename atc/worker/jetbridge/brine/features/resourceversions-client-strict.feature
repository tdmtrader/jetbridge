Feature: Resource-version client behavior over production HTTP and PostgreSQL

  Scenario Outline: Strict resource-version client behavior — <profile>
    Given the strict production resource-version client behavior "<profile>" is exercised
    Then the strict resource-version client behavior exactly matches "<profile>"

    Examples:
      | profile                 |
      | list-all                |
      | list-from               |
      | list-from-limit         |
      | list-to                 |
      | list-to-limit           |
      | list-from-to            |
      | list-filter             |
      | list-not-found          |
      | pagination-links        |
      | pagination-empty        |
      | disable-success         |
      | disable-not-found       |
      | enable-success          |
      | enable-not-found        |
      | pin-success             |
      | pin-not-found           |
      | pin-error               |
      | unpin-success           |
      | unpin-not-found         |
      | comment-success         |
      | comment-not-found       |
      | clear-resource-success  |
      | clear-resource-error    |
      | clear-type-success      |
      | clear-type-error        |
