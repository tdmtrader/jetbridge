Read `request/upgrade-request.json` and upgrade exactly the requested component in
the immutable `repository` input. Account for breaking changes and migrations,
update generated lock state when appropriate, and run focused plus broader tests.
Do not publish the result or contact any live environment.

Write a `repository-change/v1` candidate beneath the literal directory printed
for `$AGENT_OUTPUT_CHANGE`; write `record.json`, place the payload beneath
`content/`, declare `repository` as the one `base` subject, and copy the exact
platform-provided type, digest, and schema values. Write
`$AGENT_OUTPUT_REPORT/upgrade-report.json` conforming exactly
to `upgrade-report/v1`, including the requested from/to version and an honest
summary of compatibility work and test results. Jetbridge will validate and seal
both outputs atomically.
