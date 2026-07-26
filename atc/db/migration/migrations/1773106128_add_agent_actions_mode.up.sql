-- The cluster-wide action-suppression switch: an emergency brake that stops
-- every EXTERNAL side effect (publisher writes) without a redeploy. It does
-- NOT gate dispatch, agent execution, or sealing — suppression bounds effects,
-- not compute, which is what makes a shadow-mode rollout phase possible.
--
-- ABSENCE of an explicit setting means 'active': a brake nobody has touched is
-- not engaged. A FAILED read is treated as 'suppressed' by the readers; that
-- fail-safe policy lives in Go (publisher.EffectiveActionsMode), not here.
--
-- actions_updated_at/by are the switch's OWN provenance. The pre-existing
-- updated_at/updated_by belong to dispatcher_mode; sharing them would make
-- "who paused the dispatcher" read as whoever last touched the switch.
ALTER TABLE agent_settings
    ADD COLUMN actions_mode TEXT NOT NULL DEFAULT 'active'
        CHECK (actions_mode IN ('active','suppressed')),
    ADD COLUMN actions_updated_at TIMESTAMPTZ,
    ADD COLUMN actions_updated_by TEXT;

-- agent_settings is now a MULTI-SETTING singleton. Engaging the switch must be
-- able to CREATE the row without inventing a dispatcher mode, so dispatcher_mode
-- becomes nullable and NULL means "never set" — exactly the same effective mode
-- as no row at all (db.GetDispatcherSetting maps NULL to found=false, and
-- dispatch.ResolveEffectiveMode then honors the --agent-dispatcher-enabled boot
-- flag). The CHECK still constrains every non-NULL value.
ALTER TABLE agent_settings ALTER COLUMN dispatcher_mode DROP NOT NULL;
