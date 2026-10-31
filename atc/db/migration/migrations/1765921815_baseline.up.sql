-- Baseline: the schema of upstream Concourse migration 1765921815
-- (rerun_of_bigint, the last migration of Concourse 8.0.x), replacing the
-- 300 migration files from 1510262030_initial_schema through it.
--
-- Produced by migrating an empty database to 1765921815 with those files,
-- then: pg_dump --schema-only --no-owner --no-privileges
-- --exclude-table=migrations_history, dropping the session SET lines, the
-- psql \restrict meta-commands and the pgcrypto extension comment.
--
-- A database already at or past 1765921815 never runs this file. One below
-- it is refused: upgrade it with JetBridge 0.3.1 first. There is no down.

--
-- Name: pgcrypto; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

--
-- Name: build_status; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.build_status AS ENUM (
    'pending',
    'started',
    'aborted',
    'succeeded',
    'failed',
    'errored'
);

--
-- Name: container_stage; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.container_stage AS ENUM (
    'check',
    'get',
    'run'
);

--
-- Name: container_state; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.container_state AS ENUM (
    'creating',
    'created',
    'destroying',
    'failed'
);

--
-- Name: volume_state; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.volume_state AS ENUM (
    'creating',
    'created',
    'destroying',
    'failed'
);

--
-- Name: worker_state; Type: TYPE; Schema: public; Owner: -
--

CREATE TYPE public.worker_state AS ENUM (
    'running',
    'stalled',
    'landing',
    'landed',
    'retiring'
);

--
-- Name: jsonb_coalesce_empty(jsonb); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.jsonb_coalesce_empty(value jsonb) RETURNS jsonb
    LANGUAGE sql
    AS $_$
  SELECT
      CASE WHEN jsonb_typeof($1) != 'object'
          THEN '{}'::jsonb
          ELSE $1
  END
$_$;

--
-- Name: notify_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $_$
DECLARE
  rec RECORD;
  payload TEXT;
  column_name TEXT;
  column_value TEXT;
  payload_items JSONB;
BEGIN
  CASE TG_OP
  WHEN 'INSERT', 'UPDATE' THEN
     rec := NEW;
  WHEN 'DELETE' THEN
     rec := OLD;
  ELSE
     RAISE EXCEPTION 'Unknown TG_OP: "%". Should not occur!', TG_OP;
  END CASE;

  -- Add each requested fields to a data payload.
  FOR i IN 1 .. (TG_NARGS - 1) LOOP
    column_name := TG_ARGV[i];
    EXECUTE format('SELECT $1.%I::TEXT', column_name)
    INTO column_value
    USING rec;
    payload_items := coalesce(payload_items,'{}')::jsonb || json_build_object(column_name,column_value)::jsonb;
  END LOOP;
  -- Build the payload
  payload := json_build_object(
    'operation',TG_OP,
    'data',payload_items
  );
  -- Notify the channel
  PERFORM pg_notify(TG_ARGV[0], payload);

  RETURN rec;
END;
$_$;

--
-- Name: on_pipeline_delete(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.on_pipeline_delete() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
        EXECUTE format('INSERT INTO deleted_pipelines VALUES (%s)', OLD.id);
        RETURN NULL;
END;
$$;

--
-- Name: on_pipeline_insert(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.on_pipeline_insert() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  EXECUTE format('CREATE TABLE IF NOT EXISTS pipeline_build_events_%s () INHERITS (build_events)', NEW.id);
  EXECUTE format('CREATE UNIQUE INDEX pipeline_build_events_%s_build_id_event_id ON pipeline_build_events_%s (build_id, event_id)', NEW.id, NEW.id);
  EXECUTE format('CREATE UNIQUE INDEX pipeline_build_events_%s_build_id_old_event_id ON pipeline_build_events_%s (build_id_old, event_id)', NEW.id, NEW.id);
  RETURN NULL;
END;
$$;

--
-- Name: on_team_delete(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.on_team_delete() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
        EXECUTE format('DROP TABLE IF EXISTS team_build_events_%s', OLD.id);
        RETURN NULL;
END;
$$;

--
-- Name: on_team_insert(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.on_team_insert() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  EXECUTE format('CREATE TABLE IF NOT EXISTS team_build_events_%s () INHERITS (build_events)', NEW.id);
  EXECUTE format('CREATE UNIQUE INDEX team_build_events_%s_build_id_event_id ON team_build_events_%s (build_id, event_id)', NEW.id, NEW.id);
  RETURN NULL;
END;
$$;

--
-- Name: worker_resource_caches_fkey_trigger_function(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.worker_resource_caches_fkey_trigger_function() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
begin
    UPDATE worker_resource_caches
    SET invalid_since = now()
    WHERE id = NEW.id;
    RETURN NULL;
end;
$$;

--
-- Name: access_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.access_tokens (
    token text NOT NULL,
    sub text NOT NULL,
    claims jsonb,
    expires_at timestamp with time zone
);

--
-- Name: base_resource_types; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.base_resource_types (
    id integer NOT NULL,
    name text NOT NULL,
    unique_version_history boolean DEFAULT false NOT NULL
);

--
-- Name: base_resource_types_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.base_resource_types_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: base_resource_types_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.base_resource_types_id_seq OWNED BY public.base_resource_types.id;

--
-- Name: build_comments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.build_comments (
    build_id bigint NOT NULL,
    comment text DEFAULT ''::text NOT NULL
);

--
-- Name: build_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.build_events (
    build_id_old integer,
    type character varying(32) NOT NULL,
    payload text NOT NULL,
    event_id integer NOT NULL,
    version text NOT NULL,
    build_id bigint
);

--
-- Name: build_image_resource_caches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.build_image_resource_caches (
    resource_cache_id integer,
    build_id bigint NOT NULL,
    job_id integer
);

--
-- Name: build_pipes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.build_pipes (
    from_build_id bigint NOT NULL,
    to_build_id bigint NOT NULL
);

--
-- Name: build_resource_config_version_inputs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.build_resource_config_version_inputs (
    build_id bigint NOT NULL,
    resource_id integer NOT NULL,
    version_digest text NOT NULL,
    name text NOT NULL,
    first_occurrence boolean
);

--
-- Name: build_resource_config_version_outputs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.build_resource_config_version_outputs (
    build_id bigint NOT NULL,
    resource_id integer NOT NULL,
    version_digest text NOT NULL,
    name text NOT NULL
);

--
-- Name: builds; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.builds (
    id bigint NOT NULL,
    name text NOT NULL,
    status public.build_status NOT NULL,
    scheduled boolean DEFAULT false NOT NULL,
    start_time timestamp with time zone,
    end_time timestamp with time zone,
    schema character varying(16),
    private_plan text,
    completed boolean DEFAULT false NOT NULL,
    job_id integer,
    reap_time timestamp with time zone,
    team_id integer NOT NULL,
    manually_triggered boolean DEFAULT false,
    interceptible boolean DEFAULT true,
    nonce text,
    public_plan json DEFAULT '{}'::json,
    pipeline_id integer,
    drained boolean DEFAULT false NOT NULL,
    create_time timestamp with time zone DEFAULT now() NOT NULL,
    aborted boolean DEFAULT false NOT NULL,
    rerun_of_old integer,
    rerun_number integer DEFAULT 0 NOT NULL,
    inputs_ready boolean DEFAULT false NOT NULL,
    needs_v6_migration boolean DEFAULT true NOT NULL,
    span_context jsonb,
    resource_id integer,
    resource_type_id integer,
    created_by text,
    rerun_of bigint
);

--
-- Name: builds_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.builds_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: builds_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.builds_id_seq OWNED BY public.builds.id;

--
-- Name: cert_cache; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cert_cache (
    domain text NOT NULL,
    cert text NOT NULL,
    nonce text
);

--
-- Name: check_build_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.check_build_events (
)
INHERITS (public.build_events);

--
-- Name: components; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.components (
    id integer NOT NULL,
    name text NOT NULL,
    "interval" text NOT NULL,
    last_ran timestamp with time zone,
    paused boolean DEFAULT false
);

--
-- Name: components_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.components_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: components_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.components_id_seq OWNED BY public.components.id;

--
-- Name: config_version_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.config_version_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: containers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.containers (
    handle text NOT NULL,
    build_id bigint,
    plan_id text,
    pipeline_id integer,
    resource_id integer,
    worker_name text,
    id integer NOT NULL,
    team_id integer,
    state public.container_state DEFAULT 'creating'::public.container_state NOT NULL,
    meta_type text DEFAULT ''::text NOT NULL,
    meta_step_name text DEFAULT ''::text NOT NULL,
    meta_attempt text DEFAULT ''::text NOT NULL,
    meta_working_directory text DEFAULT ''::text NOT NULL,
    meta_process_user text DEFAULT ''::text NOT NULL,
    meta_pipeline_id integer DEFAULT 0 NOT NULL,
    meta_job_id integer DEFAULT 0 NOT NULL,
    meta_build_id bigint DEFAULT 0 NOT NULL,
    meta_pipeline_name text DEFAULT ''::text NOT NULL,
    meta_job_name text DEFAULT ''::text NOT NULL,
    meta_build_name text DEFAULT ''::text NOT NULL,
    image_check_container_id integer,
    image_get_container_id integer,
    missing_since timestamp with time zone,
    resource_config_check_session_id integer,
    last_hijack timestamp with time zone,
    meta_pipeline_instance_vars text DEFAULT ''::text NOT NULL,
    in_memory_build_id bigint,
    in_memory_build_create_time timestamp with time zone
);

--
-- Name: containers_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.containers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: containers_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.containers_id_seq OWNED BY public.containers.id;

--
-- Name: deleted_pipelines; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.deleted_pipelines (
    id integer NOT NULL,
    deleted_at timestamp without time zone DEFAULT now() NOT NULL
);

--
-- Name: job_inputs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.job_inputs (
    name text NOT NULL,
    job_id integer NOT NULL,
    resource_id integer NOT NULL,
    passed_job_id integer,
    trigger boolean DEFAULT false NOT NULL,
    version text
);

--
-- Name: job_outputs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.job_outputs (
    name text NOT NULL,
    job_id integer NOT NULL,
    resource_id integer NOT NULL
);

--
-- Name: jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.jobs (
    name text NOT NULL,
    build_number_seq integer DEFAULT 0 NOT NULL,
    paused boolean DEFAULT false,
    id integer NOT NULL,
    pipeline_id integer NOT NULL,
    first_logged_build_id bigint DEFAULT 0 NOT NULL,
    inputs_determined boolean DEFAULT false NOT NULL,
    max_in_flight_reached boolean DEFAULT false NOT NULL,
    config text,
    active boolean DEFAULT false NOT NULL,
    interruptible boolean DEFAULT false NOT NULL,
    nonce text,
    tags text[],
    latest_completed_build_id bigint,
    next_build_id bigint,
    transition_build_id bigint,
    has_new_inputs boolean DEFAULT false,
    schedule_requested timestamp with time zone DEFAULT '1970-01-01 00:00:00'::timestamp with time zone NOT NULL,
    last_scheduled timestamp with time zone DEFAULT '1970-01-01 00:00:00'::timestamp with time zone NOT NULL,
    public boolean DEFAULT false NOT NULL,
    max_in_flight integer DEFAULT 0 NOT NULL,
    disable_manual_trigger boolean DEFAULT false NOT NULL,
    paused_by text,
    paused_at timestamp with time zone
);

--
-- Name: jobs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.jobs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: jobs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.jobs_id_seq OWNED BY public.jobs.id;

--
-- Name: jobs_serial_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.jobs_serial_groups (
    id integer NOT NULL,
    serial_group text NOT NULL,
    job_id integer
);

--
-- Name: jobs_serial_groups_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.jobs_serial_groups_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: jobs_serial_groups_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.jobs_serial_groups_id_seq OWNED BY public.jobs_serial_groups.id;

--
-- Name: next_build_inputs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.next_build_inputs (
    job_id integer NOT NULL,
    input_name text NOT NULL,
    first_occurrence boolean,
    resource_id integer,
    resolve_error text,
    version_digest text
);

--
-- Name: next_build_pipes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.next_build_pipes (
    from_build_id bigint NOT NULL,
    to_job_id integer NOT NULL
);

--
-- Name: one_off_name; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.one_off_name
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: pipelines; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pipelines (
    version integer DEFAULT 0 NOT NULL,
    id integer NOT NULL,
    name text NOT NULL,
    paused boolean DEFAULT false,
    ordering integer DEFAULT 0 NOT NULL,
    team_id integer NOT NULL,
    public boolean DEFAULT false NOT NULL,
    groups json,
    cache_index integer DEFAULT 1 NOT NULL,
    var_sources text,
    nonce text,
    last_updated timestamp with time zone DEFAULT '1970-01-01 00:00:00'::timestamp with time zone NOT NULL,
    archived boolean DEFAULT false NOT NULL,
    parent_job_id integer,
    parent_build_id bigint,
    display jsonb,
    instance_vars jsonb,
    secondary_ordering integer NOT NULL,
    paused_by text,
    paused_at timestamp with time zone
);

--
-- Name: pipelines_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pipelines_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: pipelines_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pipelines_id_seq OWNED BY public.pipelines.id;

--
-- Name: prototypes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.prototypes (
    id integer NOT NULL,
    pipeline_id integer NOT NULL,
    name text NOT NULL,
    type text NOT NULL,
    config text,
    active boolean DEFAULT false NOT NULL,
    nonce text,
    resource_config_id integer
);

--
-- Name: prototypes_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.prototypes_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: prototypes_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.prototypes_id_seq OWNED BY public.prototypes.id;

--
-- Name: resource_cache_uses; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resource_cache_uses (
    resource_cache_id integer,
    build_id bigint,
    in_memory_build_id bigint,
    in_memory_build_create_time timestamp with time zone
);

--
-- Name: resource_caches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resource_caches (
    id integer NOT NULL,
    resource_config_id integer,
    version jsonb NOT NULL,
    params_hash text NOT NULL,
    metadata text,
    version_digest text NOT NULL
);

--
-- Name: resource_caches_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.resource_caches_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: resource_caches_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.resource_caches_id_seq OWNED BY public.resource_caches.id;

--
-- Name: resource_config_check_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resource_config_check_sessions (
    id integer NOT NULL,
    resource_config_id integer,
    expires_at timestamp with time zone,
    worker_base_resource_type_id integer
);

--
-- Name: resource_config_check_sessions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.resource_config_check_sessions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: resource_config_check_sessions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.resource_config_check_sessions_id_seq OWNED BY public.resource_config_check_sessions.id;

--
-- Name: resource_config_scopes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resource_config_scopes (
    id bigint NOT NULL,
    resource_config_id integer NOT NULL,
    resource_id integer,
    last_check_start_time timestamp with time zone DEFAULT '1970-01-01 00:00:00'::timestamp with time zone NOT NULL,
    last_check_end_time timestamp with time zone DEFAULT '1970-01-01 00:00:00'::timestamp with time zone NOT NULL,
    last_check_succeeded boolean DEFAULT false NOT NULL,
    last_check_build_id bigint,
    last_check_build_plan json DEFAULT '{}'::json
);

--
-- Name: resource_config_scopes_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.resource_config_scopes_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: resource_config_scopes_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.resource_config_scopes_id_seq OWNED BY public.resource_config_scopes.id;

--
-- Name: resource_config_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resource_config_versions (
    id bigint NOT NULL,
    version jsonb NOT NULL,
    version_md5 text,
    metadata jsonb DEFAULT 'null'::jsonb NOT NULL,
    check_order integer DEFAULT 0 NOT NULL,
    resource_config_scope_id bigint NOT NULL,
    span_context jsonb,
    version_sha256 text NOT NULL
);

--
-- Name: resource_config_versions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.resource_config_versions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: resource_config_versions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.resource_config_versions_id_seq OWNED BY public.resource_config_versions.id;

--
-- Name: resource_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resource_configs (
    id integer NOT NULL,
    base_resource_type_id integer,
    source_hash text NOT NULL,
    resource_cache_id integer,
    last_referenced timestamp with time zone DEFAULT '1970-01-01 00:00:00'::timestamp with time zone NOT NULL
);

--
-- Name: resource_configs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.resource_configs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: resource_configs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.resource_configs_id_seq OWNED BY public.resource_configs.id;

--
-- Name: resource_disabled_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resource_disabled_versions (
    resource_id integer NOT NULL,
    version_digest text NOT NULL
);

--
-- Name: resource_pins; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resource_pins (
    resource_id integer NOT NULL,
    version jsonb NOT NULL,
    comment_text text NOT NULL,
    config boolean DEFAULT false NOT NULL
);

--
-- Name: resource_types; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resource_types (
    id integer NOT NULL,
    pipeline_id integer,
    name text NOT NULL,
    type text NOT NULL,
    config text,
    active boolean DEFAULT false NOT NULL,
    nonce text,
    resource_config_id integer
);

--
-- Name: resource_types_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.resource_types_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: resource_types_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.resource_types_id_seq OWNED BY public.resource_types.id;

--
-- Name: resources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resources (
    name text NOT NULL,
    id integer NOT NULL,
    pipeline_id integer NOT NULL,
    config text,
    active boolean DEFAULT false NOT NULL,
    nonce text,
    resource_config_id integer,
    resource_config_scope_id bigint,
    type text NOT NULL,
    in_memory_build_id bigint,
    in_memory_build_start_time timestamp with time zone,
    in_memory_build_plan json,
    in_memory_build_status public.build_status
);

--
-- Name: resources_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.resources_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: resources_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.resources_id_seq OWNED BY public.resources.id;

--
-- Name: signing_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.signing_keys (
    kid text NOT NULL,
    kty text NOT NULL,
    jwk json NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: successful_build_outputs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.successful_build_outputs (
    build_id bigint NOT NULL,
    job_id integer NOT NULL,
    outputs jsonb NOT NULL,
    rerun_of bigint
);

--
-- Name: task_caches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.task_caches (
    id integer NOT NULL,
    job_id integer,
    step_name text NOT NULL,
    path text NOT NULL
);

--
-- Name: task_caches_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.task_caches_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: task_caches_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.task_caches_id_seq OWNED BY public.task_caches.id;

--
-- Name: teams; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.teams (
    id integer NOT NULL,
    name text NOT NULL,
    admin boolean DEFAULT false,
    legacy_auth text,
    nonce text,
    auth text,
    CONSTRAINT constraint_teams_name_not_empty CHECK ((length(name) > 0))
);

--
-- Name: teams_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.teams_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: teams_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.teams_id_seq OWNED BY public.teams.id;

--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id integer NOT NULL,
    sub text NOT NULL,
    username text NOT NULL,
    connector text NOT NULL,
    last_login timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: users_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.users_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;

--
-- Name: volumes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.volumes (
    id integer NOT NULL,
    handle text NOT NULL,
    worker_name text NOT NULL,
    path text,
    container_id integer,
    team_id integer,
    state public.volume_state DEFAULT 'creating'::public.volume_state NOT NULL,
    parent_id integer,
    parent_state public.volume_state,
    worker_base_resource_type_id integer,
    worker_resource_cache_id integer,
    worker_task_cache_id integer,
    worker_resource_certs_id integer,
    missing_since timestamp with time zone,
    worker_artifact_id integer
);

--
-- Name: volumes_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.volumes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: volumes_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.volumes_id_seq OWNED BY public.volumes.id;

--
-- Name: wall; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.wall (
    message text NOT NULL,
    expires_at timestamp with time zone
);

--
-- Name: worker_artifacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.worker_artifacts (
    id integer NOT NULL,
    name text NOT NULL,
    build_id bigint,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: worker_artifacts_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.worker_artifacts_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: worker_artifacts_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.worker_artifacts_id_seq OWNED BY public.worker_artifacts.id;

--
-- Name: worker_base_resource_types; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.worker_base_resource_types (
    worker_name text,
    base_resource_type_id integer,
    image text NOT NULL,
    version text NOT NULL,
    id integer NOT NULL
);

--
-- Name: worker_base_resource_types_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.worker_base_resource_types_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: worker_base_resource_types_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.worker_base_resource_types_id_seq OWNED BY public.worker_base_resource_types.id;

--
-- Name: worker_resource_caches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.worker_resource_caches (
    id integer NOT NULL,
    worker_base_resource_type_id integer,
    resource_cache_id integer,
    worker_name text,
    invalid_since timestamp with time zone
);

--
-- Name: worker_resource_caches_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.worker_resource_caches_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: worker_resource_caches_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.worker_resource_caches_id_seq OWNED BY public.worker_resource_caches.id;

--
-- Name: worker_resource_certs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.worker_resource_certs (
    id integer NOT NULL,
    worker_name text,
    certs_path text
);

--
-- Name: worker_resource_certs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.worker_resource_certs_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: worker_resource_certs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.worker_resource_certs_id_seq OWNED BY public.worker_resource_certs.id;

--
-- Name: worker_task_caches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.worker_task_caches (
    id integer NOT NULL,
    worker_name text,
    task_cache_id integer
);

--
-- Name: worker_task_caches_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.worker_task_caches_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: worker_task_caches_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.worker_task_caches_id_seq OWNED BY public.worker_task_caches.id;

--
-- Name: workers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.workers (
    addr text,
    expires timestamp with time zone,
    active_containers integer DEFAULT 0,
    resource_types text,
    platform text,
    tags text,
    baggageclaim_url text DEFAULT ''::text,
    name text NOT NULL,
    http_proxy_url text,
    https_proxy_url text,
    no_proxy text,
    start_time timestamp with time zone,
    team_id integer,
    state public.worker_state DEFAULT 'running'::public.worker_state NOT NULL,
    version text,
    certs_path text,
    ephemeral boolean,
    active_volumes integer DEFAULT 0,
    active_tasks integer DEFAULT 0,
    CONSTRAINT addr_when_running CHECK ((((state <> 'stalled'::public.worker_state) AND (state <> 'landed'::public.worker_state) AND ((addr IS NOT NULL) OR (baggageclaim_url IS NOT NULL))) OR (state = 'stalled'::public.worker_state) OR (state = 'landed'::public.worker_state))),
    CONSTRAINT workers_active_tasks_check CHECK ((active_tasks >= 0))
);

--
-- Name: base_resource_types id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.base_resource_types ALTER COLUMN id SET DEFAULT nextval('public.base_resource_types_id_seq'::regclass);

--
-- Name: builds id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.builds ALTER COLUMN id SET DEFAULT nextval('public.builds_id_seq'::regclass);

--
-- Name: components id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.components ALTER COLUMN id SET DEFAULT nextval('public.components_id_seq'::regclass);

--
-- Name: containers id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers ALTER COLUMN id SET DEFAULT nextval('public.containers_id_seq'::regclass);

--
-- Name: jobs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs ALTER COLUMN id SET DEFAULT nextval('public.jobs_id_seq'::regclass);

--
-- Name: jobs_serial_groups id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs_serial_groups ALTER COLUMN id SET DEFAULT nextval('public.jobs_serial_groups_id_seq'::regclass);

--
-- Name: pipelines id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipelines ALTER COLUMN id SET DEFAULT nextval('public.pipelines_id_seq'::regclass);

--
-- Name: prototypes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prototypes ALTER COLUMN id SET DEFAULT nextval('public.prototypes_id_seq'::regclass);

--
-- Name: resource_caches id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_caches ALTER COLUMN id SET DEFAULT nextval('public.resource_caches_id_seq'::regclass);

--
-- Name: resource_config_check_sessions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_check_sessions ALTER COLUMN id SET DEFAULT nextval('public.resource_config_check_sessions_id_seq'::regclass);

--
-- Name: resource_config_scopes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_scopes ALTER COLUMN id SET DEFAULT nextval('public.resource_config_scopes_id_seq'::regclass);

--
-- Name: resource_config_versions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_versions ALTER COLUMN id SET DEFAULT nextval('public.resource_config_versions_id_seq'::regclass);

--
-- Name: resource_configs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_configs ALTER COLUMN id SET DEFAULT nextval('public.resource_configs_id_seq'::regclass);

--
-- Name: resource_types id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_types ALTER COLUMN id SET DEFAULT nextval('public.resource_types_id_seq'::regclass);

--
-- Name: resources id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resources ALTER COLUMN id SET DEFAULT nextval('public.resources_id_seq'::regclass);

--
-- Name: task_caches id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_caches ALTER COLUMN id SET DEFAULT nextval('public.task_caches_id_seq'::regclass);

--
-- Name: teams id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.teams ALTER COLUMN id SET DEFAULT nextval('public.teams_id_seq'::regclass);

--
-- Name: users id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);

--
-- Name: volumes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes ALTER COLUMN id SET DEFAULT nextval('public.volumes_id_seq'::regclass);

--
-- Name: worker_artifacts id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_artifacts ALTER COLUMN id SET DEFAULT nextval('public.worker_artifacts_id_seq'::regclass);

--
-- Name: worker_base_resource_types id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_base_resource_types ALTER COLUMN id SET DEFAULT nextval('public.worker_base_resource_types_id_seq'::regclass);

--
-- Name: worker_resource_caches id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_resource_caches ALTER COLUMN id SET DEFAULT nextval('public.worker_resource_caches_id_seq'::regclass);

--
-- Name: worker_resource_certs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_resource_certs ALTER COLUMN id SET DEFAULT nextval('public.worker_resource_certs_id_seq'::regclass);

--
-- Name: worker_task_caches id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_task_caches ALTER COLUMN id SET DEFAULT nextval('public.worker_task_caches_id_seq'::regclass);

--
-- Name: access_tokens access_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.access_tokens
    ADD CONSTRAINT access_tokens_pkey PRIMARY KEY (token);

--
-- Name: base_resource_types base_resource_types_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.base_resource_types
    ADD CONSTRAINT base_resource_types_name_key UNIQUE (name);

--
-- Name: base_resource_types base_resource_types_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.base_resource_types
    ADD CONSTRAINT base_resource_types_pkey PRIMARY KEY (id);

--
-- Name: build_comments build_comments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_comments
    ADD CONSTRAINT build_comments_pkey PRIMARY KEY (build_id);

--
-- Name: builds builds_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_pkey PRIMARY KEY (id);

--
-- Name: cert_cache cert_cache_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cert_cache
    ADD CONSTRAINT cert_cache_pkey PRIMARY KEY (domain);

--
-- Name: components components_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.components
    ADD CONSTRAINT components_pkey PRIMARY KEY (id);

--
-- Name: workers constraint_workers_name_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workers
    ADD CONSTRAINT constraint_workers_name_unique UNIQUE (name);

--
-- Name: containers containers_handle_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_handle_key UNIQUE (handle);

--
-- Name: containers containers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_pkey PRIMARY KEY (id);

--
-- Name: jobs jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_pkey PRIMARY KEY (id);

--
-- Name: jobs_serial_groups jobs_serial_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs_serial_groups
    ADD CONSTRAINT jobs_serial_groups_pkey PRIMARY KEY (id);

--
-- Name: jobs jobs_unique_pipeline_id_name; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_unique_pipeline_id_name UNIQUE (pipeline_id, name);

--
-- Name: next_build_inputs next_build_inputs_unique_job_id_input_name; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.next_build_inputs
    ADD CONSTRAINT next_build_inputs_unique_job_id_input_name UNIQUE (job_id, input_name);

--
-- Name: pipelines pipelines_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipelines
    ADD CONSTRAINT pipelines_pkey PRIMARY KEY (id);

--
-- Name: prototypes prototypes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prototypes
    ADD CONSTRAINT prototypes_pkey PRIMARY KEY (id);

--
-- Name: resource_caches resource_caches_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_caches
    ADD CONSTRAINT resource_caches_pkey PRIMARY KEY (id);

--
-- Name: resource_config_check_sessions resource_config_check_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_check_sessions
    ADD CONSTRAINT resource_config_check_sessions_pkey PRIMARY KEY (id);

--
-- Name: resource_config_versions resource_config_scope_id_and_version_sha256_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_versions
    ADD CONSTRAINT resource_config_scope_id_and_version_sha256_unique UNIQUE (resource_config_scope_id, version_sha256);

--
-- Name: resource_config_scopes resource_config_scopes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_scopes
    ADD CONSTRAINT resource_config_scopes_pkey PRIMARY KEY (id);

--
-- Name: resource_config_versions resource_config_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_versions
    ADD CONSTRAINT resource_config_versions_pkey PRIMARY KEY (id);

--
-- Name: resource_configs resource_configs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_configs
    ADD CONSTRAINT resource_configs_pkey PRIMARY KEY (id);

--
-- Name: resource_pins resource_pins_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_pins
    ADD CONSTRAINT resource_pins_pkey PRIMARY KEY (resource_id);

--
-- Name: resource_types resource_types_pipeline_id_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_types
    ADD CONSTRAINT resource_types_pipeline_id_name_key UNIQUE (pipeline_id, name);

--
-- Name: resource_types resource_types_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_types
    ADD CONSTRAINT resource_types_pkey PRIMARY KEY (id);

--
-- Name: resources resources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resources
    ADD CONSTRAINT resources_pkey PRIMARY KEY (id);

--
-- Name: signing_keys signing_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.signing_keys
    ADD CONSTRAINT signing_keys_pkey PRIMARY KEY (kid);

--
-- Name: task_caches task_caches_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_caches
    ADD CONSTRAINT task_caches_pkey PRIMARY KEY (id);

--
-- Name: teams teams_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.teams
    ADD CONSTRAINT teams_pkey PRIMARY KEY (id);

--
-- Name: resources unique_pipeline_id_name; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resources
    ADD CONSTRAINT unique_pipeline_id_name UNIQUE (pipeline_id, name);

--
-- Name: users user_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT user_unique UNIQUE (sub);

--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

--
-- Name: volumes volumes_id_state_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_id_state_key UNIQUE (id, state);

--
-- Name: volumes volumes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_pkey PRIMARY KEY (id);

--
-- Name: volumes volumes_worker_name_handle_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_worker_name_handle_key UNIQUE (worker_name, handle);

--
-- Name: worker_artifacts worker_artifacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_artifacts
    ADD CONSTRAINT worker_artifacts_pkey PRIMARY KEY (id);

--
-- Name: worker_base_resource_types worker_base_resource_types_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_base_resource_types
    ADD CONSTRAINT worker_base_resource_types_pkey PRIMARY KEY (id);

--
-- Name: worker_base_resource_types worker_base_resource_types_worker_name_base_resource_type_i_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_base_resource_types
    ADD CONSTRAINT worker_base_resource_types_worker_name_base_resource_type_i_key UNIQUE (worker_name, base_resource_type_id);

--
-- Name: worker_resource_caches worker_resource_caches_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_resource_caches
    ADD CONSTRAINT worker_resource_caches_pkey PRIMARY KEY (id);

--
-- Name: worker_resource_certs worker_resource_certs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_resource_certs
    ADD CONSTRAINT worker_resource_certs_pkey PRIMARY KEY (id);

--
-- Name: worker_task_caches worker_task_caches_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_task_caches
    ADD CONSTRAINT worker_task_caches_pkey PRIMARY KEY (id);

--
-- Name: workers workers_addr_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workers
    ADD CONSTRAINT workers_addr_key UNIQUE (addr);

--
-- Name: workers workers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workers
    ADD CONSTRAINT workers_pkey PRIMARY KEY (name);

--
-- Name: build_events_build_id_event_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX build_events_build_id_event_id ON public.build_events USING btree (build_id, event_id);

--
-- Name: build_events_build_id_old_event_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX build_events_build_id_old_event_id ON public.build_events USING btree (build_id_old, event_id);

--
-- Name: build_image_resource_caches_build_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX build_image_resource_caches_build_id ON public.build_image_resource_caches USING btree (build_id);

--
-- Name: build_image_resource_caches_job_build_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX build_image_resource_caches_job_build_idx ON public.build_image_resource_caches USING btree (job_id, build_id);

--
-- Name: build_image_resource_caches_resource_cache_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX build_image_resource_caches_resource_cache_id ON public.build_image_resource_caches USING btree (resource_cache_id);

--
-- Name: build_inputs_resource_versions_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX build_inputs_resource_versions_idx ON public.build_resource_config_version_inputs USING btree (resource_id, version_digest);

--
-- Name: build_names_uniq_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX build_names_uniq_idx ON public.builds USING btree (job_id, name);

--
-- Name: build_pipes_from_build_id_to_build_id_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX build_pipes_from_build_id_to_build_id_uniq ON public.build_pipes USING btree (from_build_id, to_build_id);

--
-- Name: build_pipes_to_build_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX build_pipes_to_build_id_idx ON public.build_pipes USING btree (to_build_id);

--
-- Name: build_resource_config_version_inputs_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX build_resource_config_version_inputs_uniq ON public.build_resource_config_version_inputs USING btree (build_id, resource_id, version_digest, name);

--
-- Name: build_resource_config_version_outputs_build_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX build_resource_config_version_outputs_build_id_idx ON public.build_resource_config_version_outputs USING btree (build_id);

--
-- Name: build_resource_config_version_outputs_resource_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX build_resource_config_version_outputs_resource_id_idx ON public.build_resource_config_version_outputs USING btree (resource_id);

--
-- Name: build_resource_config_version_outputs_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX build_resource_config_version_outputs_uniq ON public.build_resource_config_version_outputs USING btree (build_id, resource_id, version_digest, name);

--
-- Name: builds_interceptible_completed; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX builds_interceptible_completed ON public.builds USING btree (interceptible, completed);

--
-- Name: builds_ordered_by_job_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX builds_ordered_by_job_id_idx ON public.builds USING btree (job_id, id DESC);

--
-- Name: builds_pipeline_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX builds_pipeline_id ON public.builds USING btree (pipeline_id);

--
-- Name: builds_resource_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX builds_resource_id_idx ON public.builds USING btree (resource_id);

--
-- Name: builds_resource_type_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX builds_resource_type_id_idx ON public.builds USING btree (resource_type_id);

--
-- Name: builds_running_builds_by_serial_groups_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX builds_running_builds_by_serial_groups_idx ON public.builds USING btree (job_id) WHERE ((completed = false) AND (scheduled = true));

--
-- Name: builds_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX builds_status ON public.builds USING btree (status);

--
-- Name: builds_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX builds_team_id ON public.builds USING btree (team_id);

--
-- Name: check_build_events_build_id_event_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX check_build_events_build_id_event_id ON public.check_build_events USING btree (build_id, event_id);

--
-- Name: components_name_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX components_name_key ON public.components USING btree (name);

--
-- Name: containers_build_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX containers_build_id ON public.containers USING btree (build_id);

--
-- Name: containers_build_id_not_null; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX containers_build_id_not_null ON public.containers USING btree (worker_name) WHERE (build_id IS NOT NULL);

--
-- Name: containers_in_memory_build_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX containers_in_memory_build_id_idx ON public.containers USING btree (in_memory_build_id, in_memory_build_create_time);

--
-- Name: containers_plan_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX containers_plan_id ON public.containers USING btree (plan_id);

--
-- Name: containers_resource_config_check_session_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX containers_resource_config_check_session_id ON public.containers USING btree (resource_config_check_session_id);

--
-- Name: containers_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX containers_team_id ON public.containers USING btree (team_id);

--
-- Name: containers_worker_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX containers_worker_name ON public.containers USING btree (worker_name);

--
-- Name: index_teams_name_unique_case_insensitive; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX index_teams_name_unique_case_insensitive ON public.teams USING btree (lower(name));

--
-- Name: job_inputs_job_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX job_inputs_job_id_idx ON public.job_inputs USING btree (job_id);

--
-- Name: job_inputs_passed_job_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX job_inputs_passed_job_id_idx ON public.job_inputs USING btree (passed_job_id);

--
-- Name: job_inputs_resource_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX job_inputs_resource_id_idx ON public.job_inputs USING btree (resource_id);

--
-- Name: job_outputs_job_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX job_outputs_job_id_idx ON public.job_outputs USING btree (job_id);

--
-- Name: job_outputs_resource_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX job_outputs_resource_id_idx ON public.job_outputs USING btree (resource_id);

--
-- Name: jobs_latest_completed_build_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX jobs_latest_completed_build_id ON public.jobs USING btree (latest_completed_build_id);

--
-- Name: jobs_next_build_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX jobs_next_build_id ON public.jobs USING btree (next_build_id);

--
-- Name: jobs_pipeline_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX jobs_pipeline_id ON public.jobs USING btree (pipeline_id);

--
-- Name: jobs_serial_groups_job_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX jobs_serial_groups_job_id_idx ON public.jobs_serial_groups USING btree (job_id);

--
-- Name: jobs_transition_build_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX jobs_transition_build_id ON public.jobs USING btree (transition_build_id);

--
-- Name: missing_volumes_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX missing_volumes_idx ON public.volumes USING btree (state) WHERE (missing_since IS NOT NULL);

--
-- Name: needs_v6_migration_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX needs_v6_migration_idx ON public.builds USING btree (job_id, COALESCE((rerun_of_old)::bigint, id) DESC, id DESC) WHERE ((status = 'succeeded'::public.build_status) AND needs_v6_migration);

--
-- Name: next_build_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX next_build_id_idx ON public.builds USING btree (job_id) WHERE ((status = 'pending'::public.build_status) OR (status = 'started'::public.build_status));

--
-- Name: next_build_inputs_job_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX next_build_inputs_job_id ON public.next_build_inputs USING btree (job_id);

--
-- Name: next_build_pipes_from_build_id_to_job_id_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX next_build_pipes_from_build_id_to_job_id_uniq ON public.next_build_pipes USING btree (from_build_id, to_job_id);

--
-- Name: next_build_pipes_job_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX next_build_pipes_job_id_idx ON public.next_build_pipes USING btree (to_job_id);

--
-- Name: order_builds_by_rerun_of_or_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX order_builds_by_rerun_of_or_id_idx ON public.builds USING btree (COALESCE(rerun_of, (rerun_of_old)::bigint, id) DESC, id DESC);

--
-- Name: order_job_builds_by_rerun_of_or_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX order_job_builds_by_rerun_of_or_id_idx ON public.builds USING btree (job_id, COALESCE(rerun_of, (rerun_of_old)::bigint, id) DESC, id DESC) WHERE (job_id IS NOT NULL);

--
-- Name: parent_job_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX parent_job_id ON public.pipelines USING btree (parent_job_id);

--
-- Name: pending_builds_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX pending_builds_idx ON public.builds USING btree (job_id, id) WHERE (status = 'pending'::public.build_status);

--
-- Name: pipelines_name_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX pipelines_name_team_id ON public.pipelines USING btree (name, team_id) WHERE (instance_vars IS NULL);

--
-- Name: pipelines_name_team_id_instance_vars; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX pipelines_name_team_id_instance_vars ON public.pipelines USING btree (name, team_id, instance_vars) WHERE (instance_vars IS NOT NULL);

--
-- Name: pipelines_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX pipelines_team_id ON public.pipelines USING btree (team_id);

--
-- Name: prototypes_pipeline_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX prototypes_pipeline_id ON public.prototypes USING btree (pipeline_id);

--
-- Name: prototypes_pipeline_id_name_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX prototypes_pipeline_id_name_uniq ON public.prototypes USING btree (pipeline_id, name);

--
-- Name: prototypes_resource_config_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX prototypes_resource_config_id ON public.prototypes USING btree (resource_config_id);

--
-- Name: rerun_of_builds_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX rerun_of_builds_idx ON public.builds USING btree (rerun_of);

--
-- Name: rerun_of_old_builds_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX rerun_of_old_builds_idx ON public.builds USING btree (rerun_of_old);

--
-- Name: resource_cache_uses_build_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_cache_uses_build_id ON public.resource_cache_uses USING btree (build_id);

--
-- Name: resource_cache_uses_in_memory_build_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_cache_uses_in_memory_build_id_idx ON public.resource_cache_uses USING btree (in_memory_build_id, in_memory_build_create_time);

--
-- Name: resource_cache_uses_resource_cache_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_cache_uses_resource_cache_id ON public.resource_cache_uses USING btree (resource_cache_id);

--
-- Name: resource_caches_resource_config_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_caches_resource_config_id ON public.resource_caches USING btree (resource_config_id);

--
-- Name: resource_caches_rsc_config_id_version_digest_params_hash_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX resource_caches_rsc_config_id_version_digest_params_hash_uniq ON public.resource_caches USING btree (resource_config_id, version_digest, params_hash);

--
-- Name: resource_config_check_sessions_resource_config_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_config_check_sessions_resource_config_id ON public.resource_config_check_sessions USING btree (resource_config_id);

--
-- Name: resource_config_check_sessions_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX resource_config_check_sessions_uniq ON public.resource_config_check_sessions USING btree (resource_config_id, worker_base_resource_type_id);

--
-- Name: resource_config_scope_id_and_version_md5_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_config_scope_id_and_version_md5_idx ON public.resource_config_versions USING btree (resource_config_scope_id, version_md5);

--
-- Name: resource_config_scopes_last_check_build_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_config_scopes_last_check_build_id_idx ON public.resource_config_scopes USING btree (last_check_build_id);

--
-- Name: resource_config_scopes_resource_config_id_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX resource_config_scopes_resource_config_id_uniq ON public.resource_config_scopes USING btree (resource_config_id) WHERE (resource_id IS NULL);

--
-- Name: resource_config_scopes_resource_id_resource_config_id_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX resource_config_scopes_resource_id_resource_config_id_uniq ON public.resource_config_scopes USING btree (resource_id, resource_config_id) WHERE (resource_id IS NOT NULL);

--
-- Name: resource_config_versions_check_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_config_versions_check_order_idx ON public.resource_config_versions USING btree (resource_config_scope_id, check_order DESC);

--
-- Name: resource_config_versions_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_config_versions_version ON public.resource_config_versions USING gin (version jsonb_path_ops) WITH (fastupdate='false');

--
-- Name: resource_config_versions_version_md5; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_config_versions_version_md5 ON public.resource_config_versions USING btree (version_md5);

--
-- Name: resource_configs_base_resource_type_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_configs_base_resource_type_id ON public.resource_configs USING btree (base_resource_type_id);

--
-- Name: resource_configs_base_resource_type_id_so_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX resource_configs_base_resource_type_id_so_key ON public.resource_configs USING btree (base_resource_type_id, source_hash);

--
-- Name: resource_configs_resource_cache_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_configs_resource_cache_id ON public.resource_configs USING btree (resource_cache_id);

--
-- Name: resource_configs_resource_cache_id_so_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX resource_configs_resource_cache_id_so_key ON public.resource_configs USING btree (resource_cache_id, source_hash);

--
-- Name: resource_disabled_versions_resource_id_version_digest_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX resource_disabled_versions_resource_id_version_digest_uniq ON public.resource_disabled_versions USING btree (resource_id, version_digest);

--
-- Name: resource_types_pipeline_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_types_pipeline_id ON public.resource_types USING btree (pipeline_id);

--
-- Name: resource_types_resource_config_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resource_types_resource_config_id ON public.resource_types USING btree (resource_config_id);

--
-- Name: resources_in_memory_build_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resources_in_memory_build_id_idx ON public.resources USING btree (in_memory_build_id);

--
-- Name: resources_pipeline_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resources_pipeline_id ON public.resources USING btree (pipeline_id);

--
-- Name: resources_resource_config_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resources_resource_config_id ON public.resources USING btree (resource_config_id);

--
-- Name: succeeded_builds_ordering_with_rerun_builds_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX succeeded_builds_ordering_with_rerun_builds_idx ON public.builds USING btree (job_id) INCLUDE (id, rerun_of, rerun_of_old) WHERE (status = 'succeeded'::public.build_status);

--
-- Name: successful_build_outputs_build_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX successful_build_outputs_build_id_idx ON public.successful_build_outputs USING btree (build_id);

--
-- Name: successful_build_outputs_job_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX successful_build_outputs_job_id_idx ON public.successful_build_outputs USING btree (job_id);

--
-- Name: successful_build_outputs_ordering_with_rerun_builds_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX successful_build_outputs_ordering_with_rerun_builds_idx ON public.successful_build_outputs USING btree (job_id, rerun_of, COALESCE(rerun_of, build_id) DESC, build_id DESC);

--
-- Name: successful_build_outputs_outputs_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX successful_build_outputs_outputs_idx ON public.successful_build_outputs USING gin (outputs jsonb_path_ops) WITH (fastupdate='false');

--
-- Name: successful_build_outputs_rerun_of_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX successful_build_outputs_rerun_of_idx ON public.successful_build_outputs USING btree (rerun_of);

--
-- Name: task_caches_job_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX task_caches_job_id ON public.task_caches USING btree (job_id);

--
-- Name: task_caches_job_id_step_name_path_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX task_caches_job_id_step_name_path_uniq ON public.task_caches USING btree (job_id, step_name, path);

--
-- Name: unencrypted_private_plans_build_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX unencrypted_private_plans_build_idx ON public.builds USING btree (id) WHERE ((nonce IS NULL) AND (private_plan IS NOT NULL));

--
-- Name: volumes_container_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX volumes_container_id ON public.volumes USING btree (container_id);

--
-- Name: volumes_handle; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX volumes_handle ON public.volumes USING btree (handle);

--
-- Name: volumes_parent_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX volumes_parent_id ON public.volumes USING btree (parent_id);

--
-- Name: volumes_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX volumes_team_id ON public.volumes USING btree (team_id);

--
-- Name: volumes_worker_base_resource_type_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX volumes_worker_base_resource_type_id ON public.volumes USING btree (worker_base_resource_type_id);

--
-- Name: volumes_worker_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX volumes_worker_name ON public.volumes USING btree (worker_name);

--
-- Name: volumes_worker_resource_cache_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX volumes_worker_resource_cache_id ON public.volumes USING btree (worker_resource_cache_id);

--
-- Name: volumes_worker_resource_cache_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX volumes_worker_resource_cache_unique ON public.volumes USING btree (worker_resource_cache_id);

--
-- Name: volumes_worker_task_cache_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX volumes_worker_task_cache_id ON public.volumes USING btree (worker_task_cache_id);

--
-- Name: worker_base_resource_types_base_resource_type_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX worker_base_resource_types_base_resource_type_id ON public.worker_base_resource_types USING btree (base_resource_type_id);

--
-- Name: worker_base_resource_types_worker_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX worker_base_resource_types_worker_name ON public.worker_base_resource_types USING btree (worker_name);

--
-- Name: worker_resource_caches_resource_cache_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX worker_resource_caches_resource_cache_id ON public.worker_resource_caches USING btree (resource_cache_id);

--
-- Name: worker_resource_caches_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX worker_resource_caches_uniq ON public.worker_resource_caches USING btree (worker_name, resource_cache_id) WHERE (worker_base_resource_type_id IS NOT NULL);

--
-- Name: worker_resource_caches_worker_base_resource_type_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX worker_resource_caches_worker_base_resource_type_id ON public.worker_resource_caches USING btree (worker_base_resource_type_id);

--
-- Name: worker_resource_cert_volumes_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX worker_resource_cert_volumes_idx ON public.volumes USING btree (worker_name, worker_resource_certs_id);

--
-- Name: worker_task_caches_task_cache_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX worker_task_caches_task_cache_id ON public.worker_task_caches USING btree (task_cache_id);

--
-- Name: worker_task_caches_worker_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX worker_task_caches_worker_name ON public.worker_task_caches USING btree (worker_name);

--
-- Name: worker_task_caches_worker_name_task_cache_id_uniq; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX worker_task_caches_worker_name_task_cache_id_uniq ON public.worker_task_caches USING btree (worker_name, task_cache_id);

--
-- Name: workers_team_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX workers_team_id ON public.workers USING btree (team_id);

--
-- Name: containers containers_insert_or_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER containers_insert_or_delete_trigger AFTER INSERT OR DELETE ON public.containers FOR EACH ROW EXECUTE FUNCTION public.notify_trigger('container_events_channel', 'id', 'worker_name', 'build_id');

--
-- Name: pipelines deleted_pipelines_insert_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER deleted_pipelines_insert_trigger AFTER DELETE ON public.pipelines FOR EACH ROW EXECUTE FUNCTION public.on_pipeline_delete();

--
-- Name: pipelines pipeline_build_events_insert_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pipeline_build_events_insert_trigger AFTER INSERT ON public.pipelines FOR EACH ROW EXECUTE FUNCTION public.on_pipeline_insert();

--
-- Name: teams team_build_events_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER team_build_events_delete_trigger AFTER DELETE ON public.teams FOR EACH ROW EXECUTE FUNCTION public.on_team_delete();

--
-- Name: teams team_build_events_insert_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER team_build_events_insert_trigger AFTER INSERT ON public.teams FOR EACH ROW EXECUTE FUNCTION public.on_team_insert();

--
-- Name: worker_resource_caches worker_resource_caches_fkey_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER worker_resource_caches_fkey_trigger AFTER UPDATE ON public.worker_resource_caches FOR EACH ROW WHEN (((old.worker_base_resource_type_id IS NOT NULL) AND (new.worker_base_resource_type_id IS NULL))) EXECUTE FUNCTION public.worker_resource_caches_fkey_trigger_function();

--
-- Name: workers workers_upsert_or_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER workers_upsert_or_delete_trigger AFTER INSERT OR DELETE OR UPDATE ON public.workers FOR EACH ROW EXECUTE FUNCTION public.notify_trigger('worker_events_channel', 'name');

--
-- Name: build_comments build_comments_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_comments
    ADD CONSTRAINT build_comments_build_id_fkey FOREIGN KEY (build_id) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: build_image_resource_caches build_image_resource_caches_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_image_resource_caches
    ADD CONSTRAINT build_image_resource_caches_build_id_fkey FOREIGN KEY (build_id) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: build_image_resource_caches build_image_resource_caches_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_image_resource_caches
    ADD CONSTRAINT build_image_resource_caches_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: build_image_resource_caches build_image_resource_caches_resource_cache_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_image_resource_caches
    ADD CONSTRAINT build_image_resource_caches_resource_cache_id_fkey FOREIGN KEY (resource_cache_id) REFERENCES public.resource_caches(id) ON DELETE RESTRICT;

--
-- Name: build_pipes build_pipes_from_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_pipes
    ADD CONSTRAINT build_pipes_from_build_id_fkey FOREIGN KEY (from_build_id) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: build_pipes build_pipes_to_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_pipes
    ADD CONSTRAINT build_pipes_to_build_id_fkey FOREIGN KEY (to_build_id) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: build_resource_config_version_inputs build_resource_config_version_inputs_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_resource_config_version_inputs
    ADD CONSTRAINT build_resource_config_version_inputs_build_id_fkey FOREIGN KEY (build_id) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: build_resource_config_version_inputs build_resource_config_version_inputs_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_resource_config_version_inputs
    ADD CONSTRAINT build_resource_config_version_inputs_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resources(id) ON DELETE CASCADE;

--
-- Name: build_resource_config_version_outputs build_resource_config_version_outputs_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_resource_config_version_outputs
    ADD CONSTRAINT build_resource_config_version_outputs_build_id_fkey FOREIGN KEY (build_id) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: build_resource_config_version_outputs build_resource_config_version_outputs_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.build_resource_config_version_outputs
    ADD CONSTRAINT build_resource_config_version_outputs_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resources(id) ON DELETE CASCADE;

--
-- Name: builds builds_pipeline_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES public.pipelines(id) ON DELETE CASCADE;

--
-- Name: builds builds_rerun_of_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_rerun_of_fkey FOREIGN KEY (rerun_of_old) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: builds builds_rerun_of_fkey1; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_rerun_of_fkey1 FOREIGN KEY (rerun_of) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: builds builds_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resources(id) ON DELETE CASCADE;

--
-- Name: builds builds_resource_type_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_resource_type_id_fkey FOREIGN KEY (resource_type_id) REFERENCES public.resource_types(id) ON DELETE CASCADE;

--
-- Name: builds builds_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT builds_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;

--
-- Name: containers containers_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_build_id_fkey FOREIGN KEY (build_id) REFERENCES public.builds(id) ON DELETE SET NULL;

--
-- Name: containers containers_image_check_container_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_image_check_container_id_fkey FOREIGN KEY (image_check_container_id) REFERENCES public.containers(id) ON DELETE SET NULL;

--
-- Name: containers containers_image_get_container_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_image_get_container_id_fkey FOREIGN KEY (image_get_container_id) REFERENCES public.containers(id) ON DELETE SET NULL;

--
-- Name: containers containers_resource_config_check_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_resource_config_check_session_id_fkey FOREIGN KEY (resource_config_check_session_id) REFERENCES public.resource_config_check_sessions(id) ON DELETE SET NULL;

--
-- Name: containers containers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE SET NULL;

--
-- Name: containers containers_worker_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.containers
    ADD CONSTRAINT containers_worker_name_fkey FOREIGN KEY (worker_name) REFERENCES public.workers(name) ON DELETE CASCADE;

--
-- Name: volumes fkey_container_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT fkey_container_id FOREIGN KEY (container_id) REFERENCES public.containers(id) ON DELETE SET NULL;

--
-- Name: jobs_serial_groups fkey_job_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs_serial_groups
    ADD CONSTRAINT fkey_job_id FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: builds fkey_job_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.builds
    ADD CONSTRAINT fkey_job_id FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: job_inputs job_inputs_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_inputs
    ADD CONSTRAINT job_inputs_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: job_inputs job_inputs_passed_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_inputs
    ADD CONSTRAINT job_inputs_passed_job_id_fkey FOREIGN KEY (passed_job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: job_inputs job_inputs_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_inputs
    ADD CONSTRAINT job_inputs_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resources(id) ON DELETE CASCADE;

--
-- Name: job_outputs job_outputs_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_outputs
    ADD CONSTRAINT job_outputs_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: job_outputs job_outputs_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_outputs
    ADD CONSTRAINT job_outputs_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resources(id) ON DELETE CASCADE;

--
-- Name: jobs jobs_latest_completed_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_latest_completed_build_id_fkey FOREIGN KEY (latest_completed_build_id) REFERENCES public.builds(id) ON DELETE SET NULL;

--
-- Name: jobs jobs_next_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_next_build_id_fkey FOREIGN KEY (next_build_id) REFERENCES public.builds(id) ON DELETE SET NULL;

--
-- Name: jobs jobs_pipeline_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES public.pipelines(id) ON DELETE CASCADE;

--
-- Name: jobs jobs_transition_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_transition_build_id_fkey FOREIGN KEY (transition_build_id) REFERENCES public.builds(id) ON DELETE SET NULL;

--
-- Name: next_build_inputs next_build_inputs_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.next_build_inputs
    ADD CONSTRAINT next_build_inputs_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: next_build_inputs next_build_inputs_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.next_build_inputs
    ADD CONSTRAINT next_build_inputs_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resources(id) ON DELETE CASCADE;

--
-- Name: next_build_pipes next_build_pipes_from_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.next_build_pipes
    ADD CONSTRAINT next_build_pipes_from_build_id_fkey FOREIGN KEY (from_build_id) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: next_build_pipes next_build_pipes_to_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.next_build_pipes
    ADD CONSTRAINT next_build_pipes_to_job_id_fkey FOREIGN KEY (to_job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: pipelines pipelines_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pipelines
    ADD CONSTRAINT pipelines_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;

--
-- Name: prototypes prototypes_pipeline_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prototypes
    ADD CONSTRAINT prototypes_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES public.pipelines(id) ON DELETE CASCADE;

--
-- Name: prototypes prototypes_resource_config_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.prototypes
    ADD CONSTRAINT prototypes_resource_config_id_fkey FOREIGN KEY (resource_config_id) REFERENCES public.resource_configs(id);

--
-- Name: resource_cache_uses resource_cache_uses_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_cache_uses
    ADD CONSTRAINT resource_cache_uses_build_id_fkey FOREIGN KEY (build_id) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: resource_cache_uses resource_cache_uses_resource_cache_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_cache_uses
    ADD CONSTRAINT resource_cache_uses_resource_cache_id_fkey FOREIGN KEY (resource_cache_id) REFERENCES public.resource_caches(id) ON DELETE RESTRICT;

--
-- Name: resource_caches resource_caches_resource_config_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_caches
    ADD CONSTRAINT resource_caches_resource_config_id_fkey FOREIGN KEY (resource_config_id) REFERENCES public.resource_configs(id) ON DELETE RESTRICT;

--
-- Name: resource_config_check_sessions resource_config_check_sessions_resource_config_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_check_sessions
    ADD CONSTRAINT resource_config_check_sessions_resource_config_id_fkey FOREIGN KEY (resource_config_id) REFERENCES public.resource_configs(id) ON DELETE RESTRICT;

--
-- Name: resource_config_check_sessions resource_config_check_sessions_worker_base_resource_type_id_fke; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_check_sessions
    ADD CONSTRAINT resource_config_check_sessions_worker_base_resource_type_id_fke FOREIGN KEY (worker_base_resource_type_id) REFERENCES public.worker_base_resource_types(id) ON DELETE CASCADE;

--
-- Name: resource_config_scopes resource_config_scopes_resource_config_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_scopes
    ADD CONSTRAINT resource_config_scopes_resource_config_id_fkey FOREIGN KEY (resource_config_id) REFERENCES public.resource_configs(id) ON DELETE CASCADE;

--
-- Name: resource_config_scopes resource_config_scopes_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_scopes
    ADD CONSTRAINT resource_config_scopes_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resources(id) ON DELETE CASCADE;

--
-- Name: resource_config_versions resource_config_versions_resource_config_scope_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_config_versions
    ADD CONSTRAINT resource_config_versions_resource_config_scope_id_fkey FOREIGN KEY (resource_config_scope_id) REFERENCES public.resource_config_scopes(id) ON DELETE CASCADE;

--
-- Name: resource_configs resource_configs_base_resource_type_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_configs
    ADD CONSTRAINT resource_configs_base_resource_type_id_fkey FOREIGN KEY (base_resource_type_id) REFERENCES public.base_resource_types(id) ON DELETE CASCADE;

--
-- Name: resource_configs resource_configs_resource_cache_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_configs
    ADD CONSTRAINT resource_configs_resource_cache_id_fkey FOREIGN KEY (resource_cache_id) REFERENCES public.resource_caches(id) ON DELETE RESTRICT;

--
-- Name: resource_disabled_versions resource_disabled_versions_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_disabled_versions
    ADD CONSTRAINT resource_disabled_versions_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resources(id) ON DELETE CASCADE;

--
-- Name: resource_pins resource_pins_resource_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_pins
    ADD CONSTRAINT resource_pins_resource_id_fkey FOREIGN KEY (resource_id) REFERENCES public.resources(id) ON DELETE CASCADE;

--
-- Name: resource_types resource_types_pipeline_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_types
    ADD CONSTRAINT resource_types_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES public.pipelines(id) ON DELETE CASCADE;

--
-- Name: resource_types resource_types_resource_config_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resource_types
    ADD CONSTRAINT resource_types_resource_config_id_fkey FOREIGN KEY (resource_config_id) REFERENCES public.resource_configs(id) ON DELETE RESTRICT;

--
-- Name: resources resources_pipeline_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resources
    ADD CONSTRAINT resources_pipeline_id_fkey FOREIGN KEY (pipeline_id) REFERENCES public.pipelines(id) ON DELETE CASCADE;

--
-- Name: resources resources_resource_config_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resources
    ADD CONSTRAINT resources_resource_config_id_fkey FOREIGN KEY (resource_config_id) REFERENCES public.resource_configs(id) ON DELETE RESTRICT;

--
-- Name: resources resources_resource_config_scope_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resources
    ADD CONSTRAINT resources_resource_config_scope_id_fkey FOREIGN KEY (resource_config_scope_id) REFERENCES public.resource_config_scopes(id) ON DELETE SET NULL;

--
-- Name: successful_build_outputs successful_build_outputs_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.successful_build_outputs
    ADD CONSTRAINT successful_build_outputs_build_id_fkey FOREIGN KEY (build_id) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: successful_build_outputs successful_build_outputs_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.successful_build_outputs
    ADD CONSTRAINT successful_build_outputs_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: successful_build_outputs successful_build_outputs_rerun_of_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.successful_build_outputs
    ADD CONSTRAINT successful_build_outputs_rerun_of_fkey FOREIGN KEY (rerun_of) REFERENCES public.builds(id) ON DELETE CASCADE;

--
-- Name: task_caches task_caches_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_caches
    ADD CONSTRAINT task_caches_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

--
-- Name: volumes volumes_parent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_parent_id_fkey FOREIGN KEY (parent_id, parent_state) REFERENCES public.volumes(id, state) DEFERRABLE;

--
-- Name: volumes volumes_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE SET NULL;

--
-- Name: volumes volumes_worker_artifact_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_worker_artifact_id_fkey FOREIGN KEY (worker_artifact_id) REFERENCES public.worker_artifacts(id) ON DELETE SET NULL;

--
-- Name: volumes volumes_worker_base_resource_type_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_worker_base_resource_type_id_fkey FOREIGN KEY (worker_base_resource_type_id) REFERENCES public.worker_base_resource_types(id) ON DELETE SET NULL;

--
-- Name: volumes volumes_worker_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_worker_name_fkey FOREIGN KEY (worker_name) REFERENCES public.workers(name) ON DELETE CASCADE;

--
-- Name: volumes volumes_worker_resource_cache_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_worker_resource_cache_id_fkey FOREIGN KEY (worker_resource_cache_id) REFERENCES public.worker_resource_caches(id) ON DELETE SET NULL;

--
-- Name: volumes volumes_worker_task_cache_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT volumes_worker_task_cache_id_fkey FOREIGN KEY (worker_task_cache_id) REFERENCES public.worker_task_caches(id) ON DELETE SET NULL;

--
-- Name: worker_artifacts worker_artifacts_build_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_artifacts
    ADD CONSTRAINT worker_artifacts_build_id_fkey FOREIGN KEY (build_id) REFERENCES public.builds(id) ON DELETE SET NULL;

--
-- Name: worker_base_resource_types worker_base_resource_types_base_resource_type_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_base_resource_types
    ADD CONSTRAINT worker_base_resource_types_base_resource_type_id_fkey FOREIGN KEY (base_resource_type_id) REFERENCES public.base_resource_types(id) ON DELETE RESTRICT;

--
-- Name: worker_base_resource_types worker_base_resource_types_worker_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_base_resource_types
    ADD CONSTRAINT worker_base_resource_types_worker_name_fkey FOREIGN KEY (worker_name) REFERENCES public.workers(name) ON DELETE CASCADE;

--
-- Name: worker_resource_caches worker_resource_caches_resource_cache_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_resource_caches
    ADD CONSTRAINT worker_resource_caches_resource_cache_id_fkey FOREIGN KEY (resource_cache_id) REFERENCES public.resource_caches(id) ON DELETE CASCADE;

--
-- Name: worker_resource_caches worker_resource_caches_worker_base_resource_type_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_resource_caches
    ADD CONSTRAINT worker_resource_caches_worker_base_resource_type_id_fkey FOREIGN KEY (worker_base_resource_type_id) REFERENCES public.worker_base_resource_types(id) ON DELETE SET NULL;

--
-- Name: worker_resource_caches worker_resource_caches_worker_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_resource_caches
    ADD CONSTRAINT worker_resource_caches_worker_name_fkey FOREIGN KEY (worker_name) REFERENCES public.workers(name) ON DELETE CASCADE;

--
-- Name: volumes worker_resource_certs_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.volumes
    ADD CONSTRAINT worker_resource_certs_id_fkey FOREIGN KEY (worker_resource_certs_id) REFERENCES public.worker_resource_certs(id) ON DELETE SET NULL;

--
-- Name: worker_resource_certs worker_resource_certs_worker_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_resource_certs
    ADD CONSTRAINT worker_resource_certs_worker_name_fkey FOREIGN KEY (worker_name) REFERENCES public.workers(name) ON UPDATE SET NULL ON DELETE CASCADE;

--
-- Name: worker_task_caches worker_task_caches_task_cache_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_task_caches
    ADD CONSTRAINT worker_task_caches_task_cache_fkey FOREIGN KEY (task_cache_id) REFERENCES public.task_caches(id) ON DELETE CASCADE;

--
-- Name: worker_task_caches worker_task_caches_worker_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.worker_task_caches
    ADD CONSTRAINT worker_task_caches_worker_name_fkey FOREIGN KEY (worker_name) REFERENCES public.workers(name) ON DELETE CASCADE;

--
-- Name: workers workers_team_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workers
    ADD CONSTRAINT workers_team_id_fkey FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE CASCADE;
