Audit only the mounted immutable inputs. Do not connect to a database, Jira,
Git hosting, or any other live system.

Compare the captured database schema/sample evidence with the anonymization
policy and implementation in the repository snapshot. Write the complete
`audit-findings/v1` contract to `$AGENT_OUTPUT_FINDINGS`.

When a bounded, well-supported repository fix is appropriate, also write a
`repository-change/v1` relative to the declared repository input to
`$AGENT_OUTPUT_CHANGE/record.json`, with its payload beneath `content/`.
Copy the exact output type/schema and repository type/digest values printed by
the platform and declare `repository` as the one `base` subject. After the
complete change has been written, look up
`change` in the JSON object `$JETBRIDGE_OPTIONAL_OUTPUT_MARKERS` and create the
empty marker file at that path. Without that marker Jetbridge intentionally
treats the always-mounted output directory as absent. Otherwise omit the
optional output and do not create its marker. Never publish, push, merge, or
mutate the source database.
