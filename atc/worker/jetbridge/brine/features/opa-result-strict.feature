Feature: OPA JSON results become the production policy decision

  Scenario Outline: OPA result profile <profile>
    Given the production OPA result profile "<profile>" is parsed
    Then the OPA result observation is "<expected>"

    Examples:
      | profile             | expected                                                                              |
      | allowed-missing     | error:allowed: key 'a.b' not found                                                    |
      | allowed-not-bool    | error:allowed: key 'a.b' must have a boolean value                                    |
      | allowed-too-shallow | error:allowed: key 'a' must have a boolean value                                      |
      | allowed-too-deep    | error:allowed: cannot access field 'c' of non-map value ('bool') from var: a.b.c     |
      | allowed             | allowed=true;block=false;messages=                                                    |
      | denied              | allowed=false;block=true;messages=                                                    |
      | explicit-block      | allowed=true;block=true;messages=                                                     |
      | messages            | allowed=true;block=true;messages=e,f                                                  |
