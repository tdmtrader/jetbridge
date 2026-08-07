-- Restore the exact shape 1773106153 left, and no row. The up migration
-- refuses to run while any provider-native operation exists, so every database
-- that could have reached 1773106167 held NULL in both columns for every row --
-- adding them back nullable IS the faithful inverse, with nothing to invent.
--
-- The shape has to be exact rather than approximate because the rollback chain
-- continues past this file: 1773106153's own down drops the constraint by name
-- and then the two columns. A restore that omitted the constraint, or named it
-- differently, would make the next step down fail.

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
