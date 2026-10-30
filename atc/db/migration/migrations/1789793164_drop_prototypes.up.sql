-- Prototypes and the `run:` step that was to run messages against them are
-- gone: no pipeline section declares them, no route checks them, and nothing
-- reads or writes this table any more.
DROP TABLE prototypes;
