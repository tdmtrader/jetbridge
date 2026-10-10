DO $$ BEGIN RAISE EXCEPTION 'migration 1789273599 is forward-only: restore the pre-upgrade database backup instead'; END $$;
