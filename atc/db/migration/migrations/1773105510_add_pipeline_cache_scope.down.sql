DO $$ BEGIN RAISE EXCEPTION 'migration 1773105510 is forward-only: restore the pre-upgrade database backup instead'; END $$;
