DO $$ BEGIN RAISE EXCEPTION 'migration 1773105511 is forward-only: restore the pre-upgrade database backup instead'; END $$;
