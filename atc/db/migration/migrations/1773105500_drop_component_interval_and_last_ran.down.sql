DO $$ BEGIN RAISE EXCEPTION 'migration 1773105500 is forward-only: restore the pre-upgrade database backup instead'; END $$;
