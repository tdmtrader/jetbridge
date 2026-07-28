-- Agent principals were a retired, parallel bearer-token authority. All
-- human-facing agent routes now use ordinary team authorization, so preserving
-- these rows would leave credentials that no current code is allowed to use.
DROP TABLE agent_principals;
