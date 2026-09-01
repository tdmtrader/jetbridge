Feature: Pipeline configuration validation reports operator-facing diagnostics

  Source: all 114 specs in configvalidate/validate_test.go covering valid config,
  invalid identifiers, groups, resources, resource types, prototypes, display
  configuration, and an empty pipeline. These scenarios call Validate directly
  with concrete atc.Config values and assert both diagnostic cardinality and
  the meaningful user-facing fragments.

  Scenario Outline: Validation profile <profile>
    Given the production pipeline validator checks profile "<profile>"
    Then validation returns <warnings> warnings and <errors> errors
    And the validation diagnostics contain "<diagnostics>"

    Examples:
      | profile                                    | warnings | errors | diagnostics                                                                                  |
      | valid                                      | 0        | 0      | none                                                                                         |
      | identifier/group                           | 1        | 0      | '_some-group' is not a valid identifier                                                      |
      | identifier/resource                        | 1        | 1      | '_some-resource' is not a valid identifier ;; resource '_some-resource' is not used          |
      | identifier/resource-type                   | 1        | 0      | '_some-resource-type' is not a valid identifier                                              |
      | identifier/prototype                       | 1        | 0      | '_some-prototype' is not a valid identifier                                                  |
      | identifier/var-source                      | 1        | 1      | '_some-var-source' is not a valid identifier ;; invalid dummy credential manager config     |
      | identifier/job                             | 1        | 1      | '_some-job' is not a valid identifier ;; job '_some-job' belongs to no group                 |
      | identifier/steps                           | 5        | 1      | '_get-step' is not a valid identifier ;; '_run-step' is not a valid identifier                  |
      | group/unknown-resource                     | 0        | 1      | invalid groups: ;; unknown resource 'bogus-resource'                                           |
      | group/unknown-job-glob                     | 0        | 1      | invalid groups: ;; no jobs match 'bogus-*' for group 'bogus'                                   |
      | group/jobs-excluded                        | 0        | 1      | job 'stand-alone-job' belongs to no group ;; job 'other-stand-alone-job' belongs to no group    |
      | group/duplicate-twice                      | 0        | 1      | 'some-group' appears 2 times                                                                          |
      | group/duplicate-four-times                 | 0        | 1      | group 'some-group' appears 4 times                                                                    |
      | group/invalid-glob                         | 0        | 1      | invalid glob expression 'some-bad-glob-[0-9' for group 'a-group'                            |
      | resource/no-name                           | 0        | 1      | resources[1] has no name                                                                    |
      | resource/no-type                           | 0        | 1      | resources.bogus-resource has no type                                                        |
      | resource/no-name-or-type                   | 0        | 1      | resources[1] has no name ;; resources[1] has no type                                         |
      | resource/duplicate                         | 0        | 1      | resources[0] and resources[1] have the same name                                            |
      | resource-type/no-name                      | 0        | 1      | resource_types[1] has no name                                                               |
      | resource-type/no-type                      | 0        | 1      | resource_types.bogus-resource-type has no type                                              |
      | resource-type/no-name-or-type              | 0        | 1      | resource_types[1] has no name ;; resource_types[1] has no type                               |
      | resource-type/image-only                   | 0        | 0      | none                                                                                         |
      | resource-type/image-and-type               | 0        | 1      | resource_types.conflicting-type cannot specify both 'image' and 'type'                      |
      | resource-type/duplicate                    | 0        | 1      | resource_types[0] and resource_types[1] have the same name                                  |
      | prototype/no-name                          | 0        | 1      | prototypes[1] has no name                                                                   |
      | prototype/no-type                          | 0        | 1      | prototypes.bogus-prototype has no type                                                      |
      | prototype/no-name-or-type                  | 0        | 1      | prototypes[1] has no name ;; prototypes[1] has no type                                      |
      | prototype/duplicate                        | 0        | 1      | prototypes[0] and prototypes[1] have the same name                                          |
      | prototype/name-conflicts-with-resource-type | 0       | 1      | resource_types[0] and prototypes[1] have the same name                                      |
      | display/http                               | 0        | 0      | none                                                                                         |
      | display/relative                           | 0        | 0      | none                                                                                         |
      | display/unsupported-scheme                 | 0        | 1      | background_image scheme must be either http, https or relative                              |
      | display/invalid-url                        | 0        | 1      | background_image is not a valid URL: ://example.com                                         |
      | pipeline/no-jobs                           | 0        | 1      | pipeline must contain at least one job                                                      |
      | var-source/unknown-type                    | 0        | 1      | unknown credential manager type: some                                                      |
      | var-source/invalid-config                  | 0        | 1      | failed to create credential manager some: invalid dummy credential manager config          |
      | var-source/duplicate                       | 0        | 1      | var_sources[0] and var_sources[1] have the same name ('some')                               |
      | var-source/unresolved                      | 0        | 1      | could not resolve inter-dependent var sources: s3                                            |
      | var-source/circular                        | 0        | 1      | could not resolve inter-dependent var sources: s1, s2, s3                                   |
      | job/no-name                                | 0        | 1      | jobs[2] has no name                                                                          |
      | job/appended-negative-build-logs           | 1        | 1      | jobs.some-other-job has negative build_logs_to_retain: -1 ;; build_logs_to_retain is deprecated |
      | job/duplicate-inputs                       | 0        | 1      | plan.do[1].get(some-resource): repeated name ;; plan.do[2].get(some-resource): repeated name |
      | job/duplicate-input-names                  | 0        | 1      | plan.do[1].get(some-resource): repeated name ;; plan.do[2].get(some-resource): repeated name |
      | job/same-resource-different-names          | 0        | 0      | none                                                                                         |
      | job/duplicate-name                         | 0        | 1      | jobs[0] and jobs[2] have the same name ('some-job')                                         |
      | job/both-retention-fields                  | 1        | 1      | can't use both build_log_retention and build_logs_to_retain                                 |
      | job/negative-build-logs                    | 1        | 1      | jobs.some-job has negative build_logs_to_retain: -1 ;; build_logs_to_retain is deprecated   |
      | job/deprecated-build-logs                  | 1        | 0      | build_logs_to_retain is deprecated. Use build_log_retention instead                         |
      | job/negative-retention                     | 0        | 1      | negative build_log_retention.builds: -1 ;; negative build_log_retention.days: -1            |
      | plan/task-missing-config-and-name           | 0        | 1      | must specify either `file:` or `config:` ;; identifier cannot be an empty string             |
      | plan/task-file-and-config                   | 0        | 1      | must specify one of `file:` or `config:`, not both                                           |
      | plan/task-invalid-inline                    | 0        | 1      | task(some-resource).config: missing 'platform'                                               |
      | plan/task-hermetic                          | 1        | 1      | specifies `hermetic:` only works with Kubernetes runtime ;; missing 'platform'               |
      | plan/sidecar-missing-name                   | 0        | 1      | missing 'name'                                                                               |
      | plan/sidecar-missing-image                  | 0        | 1      | missing 'image'                                                                              |
      | plan/sidecar-reserved-name                  | 0        | 1      | reserved container name                                                                      |
      | plan/sidecar-valid                          | 0        | 0      | none                                                                                         |
      | plan/skip-download-registry-image           | 0        | 0      | none                                                                                         |
      | plan/skip-download-image-field              | 0        | 0      | none                                                                                         |
      | plan/skip-download-non-image                | 0        | 1      | skip_download ;; registry-image                                                              |
      | plan/normal-download-any-type               | 0        | 0      | none                                                                                         |
      | plan/put-existing                           | 0        | 0      | none                                                                                         |
      | plan/get-missing                            | 0        | 1      | get(some-nonexistent-resource): unknown resource 'some-nonexistent-resource'                 |
      | plan/put-missing                            | 0        | 1      | put(some-nonexistent-resource): unknown resource 'some-nonexistent-resource'                 |
      | plan/run-missing-prototype                  | 0        | 1      | unknown prototype 'some-nonexistent-prototype'                                               |
      | plan/get-custom-existing                    | 0        | 0      | none                                                                                         |
      | plan/get-custom-missing                     | 0        | 1      | get(custom-name): unknown resource 'some-missing-resource'                                   |
      | plan/put-custom-existing                    | 0        | 0      | none                                                                                         |
      | plan/put-custom-missing                     | 0        | 1      | put(custom-name): unknown resource 'some-missing-resource'                                   |
      | job-hook/success/existing                   | 0        | 0      | none                                                                                         |
      | job-hook/success/missing                    | 0        | 1      | on_success.get(some-nonexistent-resource): unknown resource                                  |
      | job-hook/failure/existing                   | 0        | 0      | none                                                                                         |
      | job-hook/failure/missing                    | 0        | 1      | on_failure.get(some-nonexistent-resource): unknown resource                                  |
      | job-hook/error/existing                     | 0        | 0      | none                                                                                         |
      | job-hook/error/missing                      | 0        | 1      | on_error.get(some-nonexistent-resource): unknown resource                                    |
      | job-hook/abort/existing                     | 0        | 0      | none                                                                                         |
      | job-hook/abort/missing                      | 0        | 1      | on_abort.get(some-nonexistent-resource): unknown resource                                    |
      | job-hook/ensure/existing                    | 0        | 0      | none                                                                                         |
      | job-hook/ensure/missing                     | 0        | 1      | ensure.get(some-nonexistent-resource): unknown resource                                      |
      | cross-job/hook-put                          | 0        | 0      | none                                                                                         |
      | cross-job/hook-get                          | 0        | 0      | none                                                                                         |
      | cross-job/try-put                           | 0        | 0      | none                                                                                         |
      | cross-job/try-get                           | 0        | 0      | none                                                                                         |
      | nested/abort                                | 0        | 1      | on_abort.put(custom-name): unknown resource 'some-missing-resource'                          |
      | nested/error                                | 0        | 1      | on_error.put(custom-name): unknown resource 'some-missing-resource'                          |
      | nested/ensure                               | 0        | 1      | ensure.put(custom-name): unknown resource 'some-missing-resource'                            |
      | nested/success                              | 0        | 1      | on_success.put(custom-name): unknown resource 'some-missing-resource'                        |
      | nested/failure                              | 0        | 1      | on_failure.put(custom-name): unknown resource 'some-missing-resource'                        |
      | nested/try                                  | 0        | 1      | try.put(custom-name): unknown resource 'some-missing-resource'                               |
      | plan/invalid-timeout                        | 0        | 1      | timeout: invalid duration 'nope'                                                             |
      | plan/non-positive-retry                     | 0        | 1      | attempts: must be greater than 0                                                             |
      | plan/set-pipeline-empty                     | 0        | 1      | no file specified ;; identifier cannot be an empty string                                    |
      | passed/bogus-job                            | 0        | 1      | passed: no matching job(s) for 'bogus-job'                                                   |
      | passed/unmatched-glob                       | 0        | 1      | passed: no matching job(s) for 'bogus-*'                                                     |
      | passed/valid-output                         | 0        | 0      | none                                                                                         |
      | passed/valid-input                          | 0        | 0      | none                                                                                         |
      | passed/valid-glob                           | 0        | 0      | none                                                                                         |
      | passed/custom-name                          | 0        | 0      | none                                                                                         |
      | passed/job-does-not-use-resource            | 0        | 1      | job 'some-empty-job' does not interact with resource 'some-resource'                         |
      | load-var/empty                              | 0        | 1      | no file specified ;; identifier cannot be an empty string                                    |
      | load-var/duplicate                          | 0        | 1      | load_var(a-var): repeated var name                                                           |
      | plan/unknown-field                          | 0        | 1      | unknown fields                                                                               |
      | across/valid                                | 0        | 0      | none                                                                                         |
      | across/no-vars                              | 0        | 1      | across: no vars specified                                                                    |
      | across/repeated-var                         | 0        | 1      | across[1]: repeated var name                                                                 |
      | across/shadows-parent                       | 1        | 0      | across[0]: shadows local var 'var1'                                                          |
      | across/substep-shadows-parent               | 1        | 0      | across.load_var(a): shadows local var 'a'                                                    |
      | across/non-positive-limit                   | 0        | 1      | max_in_flight: must be greater than 0                                                        |
      | resource/unused-and-aliased                 | 0        | 1      | resource 'unused-resource' is not used ;; resource 'get-alias' is not used ;; resource 'put-alias' is not used |
      | cycle/self                                  | 0        | 1      | pipeline contains a cycle that starts at Job 'some-job-1'                                   |
      | cycle/multiple-jobs                         | 0        | 1      | pipeline contains a cycle that starts at Job 'some-job-2'                                   |
      | cycle/glob                                  | 0        | 1      | pipeline contains a cycle that starts at Job 'some-job-1'                                   |
      | cycle/multiple-passes-acyclic               | 0        | 0      | none                                                                                         |
      | cycle/none                                  | 0        | 0      | none                                                                                         |
