-- Prevent a new web node from committing a provider-native operation after
-- the lossiness check but before the discriminator columns are removed. The
-- migration runner executes this file in one transaction, so the lock is held
-- through the ALTER TABLE below.
LOCK TABLE agent_publications IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_publications
        WHERE operation_kind IS NOT NULL
           OR operation_payload IS NOT NULL
    ) THEN
        RAISE EXCEPTION
            'cannot remove provider-native publication operations that legacy binaries cannot represent';
    END IF;
END
$$;

ALTER TABLE agent_publications
    DROP CONSTRAINT agent_publications_operation_kind_payload_check,
    DROP COLUMN operation_payload,
    DROP COLUMN operation_kind;
