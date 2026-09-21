Feature: A session handoff reaches only its exact live execution

  Scenario Outline: A handoff bounds Pod lifetime before passing stdin
    Given a Run producer and a ready output node
    Then its bound session transport encounters <case>

    Examples:
      | case                       |
      | "a live execution"         |
      | "a tighter deadline"       |
      | "a replacement Pod"        |
      | "a foreign Pod witness"    |
      | "a foreign node witness"   |
      | "a completed execution"    |
      | "a terminating Pod"        |
      | "a restarted container"    |
      | "an expired lifetime"      |
      | "a missing executor"       |
      | "a zero lifetime"          |
      | "a replacement container during stdin" |
      | "a replacement Pod during stdin"       |
