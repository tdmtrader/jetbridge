Feature: Pipeline variables interpolate into YAML without losing type or path semantics

  Source: 34 of 35 specs in vars/template_test.go. The omitted spec only asks
  a FakeVariables implementation to return a synthetic error. All scenarios
  here use production Template evaluation with concrete StaticVariables or
  NamedVariables and assert rendered YAML or production diagnostics.

  Scenario Outline: Variable template profile <profile>
    Given the production variable template evaluates profile "<profile>"
    Then the template observation <comparison> "<expected>"

    Examples:
      | profile                      | comparison | expected                                                              |
      | simple                       | is         | foo\n                                                                 |
      | leading-slash                | is         | foo\n                                                                 |
      | multiple                     | is         | foo: bar\n                                                            |
      | boolean                      | is         | otherstuff: true\n                                                   |
      | typed-values                 | is         | typed-yaml-preserved                                                  |
      | missing-required             | is         | error:undefined vars: key, key2, key4, key_in_array                   |
      | missing-named                | is         | error:undefined vars: var1:key1, var2:key1                            |
      | missing-tolerated            | is         | ((key)): ((key2))\nfoo: 2\n                                        |
      | unused-required              | is         | error:unused vars: key1, key3                                         |
      | unused-named                 | is         | error:unused vars: var1:key1, var2:key1                               |
      | unused-tolerated             | is         | ((key)): ((key2))\n                                                 |
      | missing-and-unused           | contains   | undefined vars: key2 ;; unused vars: key1, key3                       |
      | number-template              | is         | 1234\n                                                                |
      | nil-key                      | is         | null: value\n                                                         |
      | unicode                      | is         | ☃\n                                                                   |
      | dash-underscore              | is         | dash: underscore\n                                                    |
      | quoted-dot-colon             | is         | bar: fuzz\n                                                            |
      | quoted-colon                 | is         | bar: foo\n                                                             |
      | quoted-dot-subkey            | is         | bar: topsekrit\n                                                       |
      | middle-one                   | is         | url: https://10.0.0.0\n                                               |
      | middle-many                  | is         | uri: nats://nats:secret@10.0.0.0:4222\n                              |
      | at-in-name                   | is         | secret\n                                                               |
      | middle-string-int            | is         | address: 10.0.0.0:4222\n                                            |
      | middle-unsupported-float     | contains   | float64 ;; eulers_number                                              |
      | middle-repeated              | is         | acct_and_password: nats:nats\n                                       |
      | middle-of-key                | is         | aws_cpi: props\n                                                      |
      | same-value-twice             | is         | foo: foo\n                                                             |
      | multiline-value              | is         | multiline-yaml-preserved                                             |
      | operation-unspecified        | is         | val\n                                                                 |
      | invalid-expression-tolerated | is         | (()\n                                                                 |
      | named-source                 | is         | abc: val\n                                                             |
      | subkey                       | is         | e\n                                                                   |
      | subkey-variable-missing      | contains   | undefined vars: key                                                   |
      | subkey-field-missing         | contains   | missing field 'subkey_not_found' in var: key.subkey_not_found         |

  Scenario Outline: Template resolver profile <profile>
    Given the production template resolver evaluates profile "<profile>"
    Then the template resolver observation is "<expected>"

    Examples:
      | profile            | expected                                                                                                                                                                                                 |
      | all-defined        | all-defined                                                                                                                                                                                              |
      | partial-tolerated  | resources:\n- name: my-repo\n  source:\n    private_key: some-private-key\n- name: env-state\n  source:\n    bucket: ((bucket))\n    key: ((state))\n                                                                       |
      | partial-required   | error:undefined vars: bucket, state                                                                                                                                                                       |
      | source-order       | source-order-preserved                                                                                                                                                                                    |
      | byte-slice         | foo\n                                                                                                                                                                                                    |
      | multiple-values    | foo=bar\n                                                                                                                                                                                                |
      | unicode-value      | ☃\n                                                                                                                                                                                                      |
      | punctuated-keys    | dash = underscore\n                                                                                                                                                                                    |
      | repeated-value     | foo=foo\n                                                                                                                                                                                                |
      | local-source       | foo=((.:key))\n                                                                                                                                                                                      |
      | named-source       | foo=((source:key))\n                                                                                                                                                                                  |
      | multiline          | resolver-multiline-yaml-preserved                                                                                                                                                                       |
      | undefined-list     | error:undefined vars: not-specified-one, not-specified-two                                                                                                                                               |
      | invalid-expression | (()\n                                                                                                                                                                                                    |
