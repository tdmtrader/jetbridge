Feature: Variable references parse and render production syntax

  The scenarios call Reference.String and ParseReference directly with real
  values. They use no substitute parser, variable provider, or observer.

  Scenario Outline: Variable reference profile <profile>
    Given the production variable reference profile "<profile>" is evaluated
    Then the variable reference observation is "<expected>"

    Examples:
      | profile                     | expected                                                               |
      | string-path                 | hello                                                                  |
      | string-fields               | hello.a.b                                                              |
      | string-special-all          | string-special-all-preserved                                            |
      | string-special-mixed        | string-special-mixed-preserved                                          |
      | string-source               | source:hello                                                           |
      | parse-path                  | source=;path=hello;fields=                                              |
      | parse-fields                | source=;path=hello;fields=a,b                                           |
      | parse-special-all           | source=;path=hello.world;fields=a.b,foo:bar                             |
      | parse-special-mixed         | source=;path=hello.world;fields=a,foo:bar                               |
      | parse-source                | source=source;path=hello;fields=                                        |
      | parse-colon-no-source       | source=;path=my:path;fields=field.1,field.2                             |
      | parse-quoted-source-error   | parse-quoted-source-error-preserved                                     |
      | parse-empty-field           | error:invalid var 'vault:.field': empty field                          |
      | parse-empty-quoted-field    | parse-empty-quoted-field-preserved                                      |
      | parse-empty-path            | error:invalid var 'vault:': empty field                                |
      | parse-trim-unquoted         | source=;path=hello;fields=world                                         |
      | parse-preserve-quoted-space | parse-preserve-quoted-space-preserved                                  |
