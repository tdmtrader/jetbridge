CREATE FUNCTION notify_container_event() RETURNS trigger AS $$
BEGIN
  PERFORM pg_notify('container_events_channel', '');
  RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER containers_notify_trigger AFTER INSERT OR DELETE ON containers
  FOR EACH ROW EXECUTE PROCEDURE notify_container_event();
