-- Legacy direct-Git rows deliberately retain NULL in both columns. Provider-
-- native operations use the closed operation kind plus one exact JSON action
-- payload; the paired shape prevents either representation being guessed.
ALTER TABLE agent_publications
    ADD COLUMN operation_kind TEXT,
    ADD COLUMN operation_payload JSONB,
    ADD CONSTRAINT agent_publications_operation_kind_payload_check
        CHECK ((
            (
                operation_kind IS NULL
                AND operation_payload IS NULL
            )
            OR
            (
                operation_kind IN (
                    'publish_pr_branch',
                    'create_pr',
                    'publish_pr_status',
                    'respond_to_review'
                )
                AND operation_payload IS NOT NULL
                AND jsonb_typeof(operation_payload) = 'object'
            )
        ) IS TRUE);
