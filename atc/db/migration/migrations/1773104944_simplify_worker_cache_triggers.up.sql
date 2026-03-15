-- Replace the generic notify_trigger() (which builds JSON payloads) with
-- simple trigger functions that just fire a bare NOTIFY.  The worker cache
-- now does a full refresh on every signal, so payloads are unused.

DROP TRIGGER IF EXISTS workers_upsert_or_delete_trigger ON workers;
DROP TRIGGER IF EXISTS containers_insert_or_delete_trigger ON containers;
DROP FUNCTION IF EXISTS notify_trigger();

CREATE OR REPLACE FUNCTION notify_worker_event() RETURNS trigger AS $$
BEGIN
  PERFORM pg_notify('worker_events_channel', '');
  RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION notify_container_event() RETURNS trigger AS $$
BEGIN
  PERFORM pg_notify('container_events_channel', '');
  RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER workers_notify_trigger AFTER INSERT OR UPDATE OR DELETE ON workers
  FOR EACH ROW EXECUTE PROCEDURE notify_worker_event();

CREATE TRIGGER containers_notify_trigger AFTER INSERT OR DELETE ON containers
  FOR EACH ROW EXECUTE PROCEDURE notify_container_event();
