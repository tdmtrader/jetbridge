DO $$ BEGIN RAISE EXCEPTION 'migration 1773105507 is forward-only: restore the pre-upgrade database backup instead'; END $$;
