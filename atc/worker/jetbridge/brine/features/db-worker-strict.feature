Feature: A persisted worker owns and finds its containers

  Scenario Outline: Worker persistence profile <profile> has its exact result
    When the production worker handles profile "<profile>"
    Then the worker persistence result is "<result>"

    Examples:
      | profile          | result                                                   |
      | delete           | found=false                                              |
      | creating         | creating=true;created=false                              |
      | creating-other   | creating=false;created=false                             |
      | created          | creating=false;created=true                              |
      | created-other    | creating=false;created=false                             |
      | check-dedup      | destroyed=1;before=1;second=true;after=1                 |
      | check-team       | team-valid=false                                         |
      | build-team       | team-valid=true                                          |
      | fixed-handle     | handle=true;creating=true;created=true                    |
