Feature: A Run retains the definition that was actually admitted

  @core-review
  Scenario: A template edit does not rewrite an admitted Run
    Given a Run admitted from a parameterized template
    When the template command and default parameter change
    Then the Run still returns its original template and materialized definition

  @core-review
  Scenario: Payload reclamation preserves the Run definition
    Given a Run admitted from a parameterized template
    When its completed payload is reclaimed
    Then the Run still returns its original template and materialized definition

  @core-review
  Scenario: Database encryption rotation preserves the admitted definition
    Given a Run admitted from a parameterized template
    When the database encrypts its data and rotates the encryption key
    Then the Run still returns its original template and materialized definition

  @core-review
  Scenario: A changed definition cannot pass its original attestation
    Given a Run admitted from a parameterized template
    When the stored definition bytes are corrupted
    Then the definition reader refuses the corrupted revision

  @core-review
  Scenario: A retained definition cannot be deleted while its Run exists
    Given a Run admitted from a parameterized template
    When deletion of the retained definition is attempted
    Then deletion is refused and the original definition remains

  @core-review
  Scenario: Refused admission rolls back the definition with the Run
    Given an admission transaction that rejects its new Run before commit
    Then neither the Run nor its retained definition exists

  @core-review
  Scenario: An encrypted database with no retained Runs can undo the definition migration
    Given an admission transaction that rejects its new Run before commit
    When the encrypted database rolls back the empty definition migration
    Then the schema rollback completes without losing encryption support
