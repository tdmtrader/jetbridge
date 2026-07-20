-- agent_settings is a SINGLETON row (id is pinned to 1) holding cluster-wide
-- agent-platform runtime toggles. Today it carries dispatcher_mode, the
-- runtime control for the autonomous dispatcher loop. ABSENCE of the row is
-- meaningful: the dispatcher falls back to the --agent-dispatcher-enabled boot
-- flag, which preserves current live behavior until an admin sets a mode.
CREATE TABLE agent_settings (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    dispatcher_mode TEXT NOT NULL CHECK (dispatcher_mode IN ('off','paused','active')),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by      TEXT
);
