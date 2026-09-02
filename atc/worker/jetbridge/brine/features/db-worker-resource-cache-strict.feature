Feature: Worker resource caches preserve source identity and invalidation time

  Scenario Outline: Worker resource cache profile <profile> has its exact result
    When the production worker resource cache handles profile "<profile>"
    Then the worker resource cache result is "<result>"

    Examples:
      | profile          | result                                                  |
      | create           | valid=true;cache=true                                   |
      | same-source      | valid=true;cache=true;same=true                         |
      | different-source | valid=false;cache=true;same=true                        |
      | other-worker     | valid=true;cache=true;different=true;source=true         |
      | invalid-before   | found=true;cache=true;same=true;source-zero=true         |
      | invalid-after    | found=false;cache=false                                  |
      | replacement      | valid=true;cache=true;different=true;source=true         |
      | invalid-remains  | found=true;cache=true;source-zero=true                   |
