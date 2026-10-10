DO $$ BEGIN RAISE EXCEPTION 'migration 1789793156 is forward-only: restore the pre-upgrade database backup instead'; END $$;
