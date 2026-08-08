-- agent_publications was a union: legacy direct-Git rows carried NULL in both
-- discriminator columns, and provider-native pull-request rows carried a closed
-- operation kind plus one exact JSON action payload. The pull-request arm is
-- gone -- AcquirePR was the only writer of these two columns and
-- scanAgentPublication's union arm the only reader, and both were removed with
-- the publisher's PRAction type. No Go type can decode a stored payload any
-- more, so the columns cannot describe anything the store can rehydrate.
--
-- Only the two columns 1773106153 added go, along with the paired CHECK that
-- kept them consistent. Nothing else on this table is touched: every remaining
-- column belongs to the surviving direct-Git publisher, which wrote NULL here
-- from the day the columns were introduced. Dropping them is therefore lossless
-- for every live publication.
--
-- agent_publication_inputs and agent_publication_approval_evidence are
-- deliberately NOT dropped here. Their PR writers are gone too, but
-- 1773106152 backfilled human_wait evidence for direct-Git merge approvals
-- into the second table, so removing them is its own decision with its own
-- lossiness argument -- not a consequence of retiring this discriminator.

-- Serialize the emptiness check with the drop, matching the rule 1773106153's
-- own down migration set: the runner executes this file in one transaction, so
-- the lock is held through the ALTER TABLE below and no web node can commit a
-- provider-native operation into the window between the check and the drop.
LOCK TABLE agent_publications IN ACCESS EXCLUSIVE MODE;

-- Refuse rather than destroy, exactly as 1773106166 did for bindings. A row
-- still carrying the discriminator is already unreachable through the store --
-- it cannot be acquired, read, or completed -- but it is also the only record
-- that the operation ever happened. Dropping operation_kind out from under it
-- would silently reclassify it as a direct-Git publication whose publisher is
-- 'provider-native-pr/v1', and the next read of one would fail validation
-- instead of reporting absence. A named refusal leaves the operator holding
-- the evidence and the decision; the remedy is to delete those publications,
-- and their occurrences, deliberately before upgrading.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_publications
        WHERE operation_kind IS NOT NULL
           OR operation_payload IS NOT NULL
    ) THEN
        RAISE EXCEPTION
            'cannot remove provider-native publication operations while they exist';
    END IF;
END
$$;

ALTER TABLE agent_publications
    DROP CONSTRAINT agent_publications_operation_kind_payload_check,
    DROP COLUMN operation_payload,
    DROP COLUMN operation_kind;
