Feature: ID token generation preserves signed credential claims

  Source: all 15 leaves in atc/creds/idtoken/token_generator_test.go. Every
  scenario stores real RSA and EC keys in fresh PostgreSQL, loads them through
  the production SigningKeyFactory, invokes the production TokenGenerator, and
  verifies the resulting JWT with the stored public key and a real clock.

  Scenario Outline: Strict ID token generator profile <profile>
    Given the strict real ID token generator evaluates profile "<profile>"
    Then the strict ID token generator observation is "<expected>"

    Examples:
      | profile          | expected                                                                                         |
      | valid-token      | signature=valid;subject=main/idtoken                                                              |
      | scope-team       | subject=main                                                                                     |
      | scope-instance   | subject64=bWFpbi9pZHRva2VuL2ZvbzpiYXI=                                                         |
      | scope-job        | subject64=bWFpbi9pZHRva2VuL2ZvbzpiYXIvdGVzdGpvYg==                                            |
      | escaped-subject  | escape=ZmFrZSUyRnRlYW0vZmFrZSUyRnBpcGVsaW5lLyJmYWtlJTJGZm9vIjoiZmFrZSUyRmJhciIvZmFrZSUyRmpvYg== |
      | audience         | contains-testaud=true                                                                            |
      | es256            | algorithm=ES256;signature=valid                                                                  |
      | claim-subject    | subject=main/idtoken                                                                             |
      | claim-issuer     | issuer=https://concourse.test                                                                    |
      | claim-expiry     | expiry=15m0s;returned-match=true                                                                 |
      | claim-team       | team=main                                                                                        |
      | claim-pipeline   | pipeline=idtoken                                                                                 |
      | claim-ivars      | foo=bar                                                                                          |
      | claim-job        | job=testjob                                                                                      |
      | no-audience      | audience-count=0                                                                                 |
