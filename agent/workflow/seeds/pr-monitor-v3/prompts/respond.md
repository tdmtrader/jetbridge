Work only from the immutable inputs provided to this step. `source-repository`
is the exact current pull-request branch.
Preserve every commit and change already present in `source-repository`,
including commits added by humans or bots after the previously accepted
review. Do not reset it to
`accepted-candidate`, replace it with the target branch, or discard external
work. Use `accepted-review`, `accepted-candidate`, and `accepted-validation`
only as evidence about the originating accepted revision.

The `pull-request` observation contains one authorized completed review batch.
Address only thread IDs listed in the completed review batch. Do not invent,
infer, or answer any other thread. Do not contact the forge, fetch live state,
push a branch, publish a comment, or attempt to complete the pull request. No
forge credential is available to this step.

Write one `pull-request-response/v1` candidate beneath the literal directory
printed for `$AGENT_OUTPUT_RESPONSE_DRAFT`. It must contain `record.json`;
copy the exact output type and schema supplied by the platform and declare
`pull-request` as its one `primary` subject with the exact input type and
digest. Set `batch_id` to the completed batch ID exactly. Replies must be a
lexicographically sorted subset of that batch's thread IDs, with no duplicate
or fabricated IDs. Summarize the revision truthfully even when no thread-level
reply is needed. The deterministic response-authority step will reopen the
observation and reject any response outside this exact batch.

Implement the smallest complete revision in a copy of the current PR work.
Write one `repository-change/v1` candidate beneath the literal directory
printed for `$AGENT_OUTPUT_DRAFT_CHANGE`. It must contain `record.json` and its
payload beneath `content/`. Copy the exact output type and schema supplied by
the platform, and copy the exact input type and digest for
`source-repository` as the one `base` subject. The result must descend from
that current source state so all existing PR commits remain adopted. A later
deterministic step performs the required rebase onto the exact target and the
full authoritative validation.
