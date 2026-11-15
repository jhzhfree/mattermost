-- ISOLATED TEST ONLY. This is not an Octop role migration or human authenticator.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='aitagent_owner') THEN CREATE ROLE aitagent_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS; END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='aitagent_app') THEN CREATE ROLE aitagent_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS; END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='aitagent_reader') THEN CREATE ROLE aitagent_reader LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS; END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='aitagent_maintenance') THEN CREATE ROLE aitagent_maintenance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS; END IF;
END $$;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

-- Narrow fixture authorization lock, avoiding UPDATE grants on permission rows
-- merely to use FOR SHARE. Real authenticated caller/Octop policy is NOT supplied.
CREATE OR REPLACE FUNCTION public.aitagent_fixture_authorized(org text,worker text,actor bigint,key text,action text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,pg_temp SET timezone='UTC' AS $$
DECLARE active boolean; current_key text;
BEGIN
 IF session_user='aitagent_reader' AND action<>'inspect' THEN RETURN false; END IF;
 IF session_user NOT IN ('aitagent_app','aitagent_reader') OR action NOT IN ('intake','authorization_observed','execution_observed','inspect','cancelled') THEN RETURN false; END IF;
 SELECT g.active,g.key_id INTO active,current_key FROM public.aitagent_test_grants g
  WHERE g.org=$1 AND g.worker=$2 AND g.actor=$3 FOR SHARE;
 RETURN FOUND AND active AND current_key=key;
END $$;
REVOKE ALL ON FUNCTION public.aitagent_fixture_authorized(text,text,bigint,text,text) FROM PUBLIC;

ALTER TABLE public.aitagent_capacity_guard OWNER TO aitagent_owner;
ALTER TABLE public.aitagent_intake OWNER TO aitagent_owner;
ALTER TABLE public.aitagent_intake_audit OWNER TO aitagent_owner;
ALTER TABLE public.aitagent_test_grants OWNER TO aitagent_owner;
ALTER FUNCTION public.aitagent_guard_row() OWNER TO aitagent_owner;
ALTER FUNCTION public.aitagent_require_audit() OWNER TO aitagent_owner;
ALTER FUNCTION public.aitagent_validate_event() OWNER TO aitagent_owner;
ALTER FUNCTION public.aitagent_expire_batch() OWNER TO aitagent_owner;
ALTER FUNCTION public.aitagent_archive_batch() OWNER TO aitagent_owner;
ALTER FUNCTION public.aitagent_fixture_authorized(text,text,bigint,text,text) OWNER TO aitagent_owner;
REVOKE ALL ON TABLE public.aitagent_capacity_guard,public.aitagent_intake,public.aitagent_intake_audit,public.aitagent_test_grants FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO aitagent_app,aitagent_reader,aitagent_maintenance;
GRANT SELECT ON public.aitagent_capacity_guard TO aitagent_app;
GRANT UPDATE(marker) ON public.aitagent_capacity_guard TO aitagent_app;
GRANT SELECT ON public.aitagent_intake,public.aitagent_intake_audit TO aitagent_app,aitagent_reader;
GRANT INSERT(delivery_key,fingerprint,organization_id,employee_id,actor_id,source_expires_at,state,source_version) ON public.aitagent_intake TO aitagent_app;
GRANT UPDATE(state,revision,source_version,terminal_at,terminal_by) ON public.aitagent_intake TO aitagent_app;
GRANT INSERT(delivery_key,fingerprint,action,revision,operation_id) ON public.aitagent_intake_audit TO aitagent_app;
GRANT EXECUTE ON FUNCTION public.aitagent_fixture_authorized(text,text,bigint,text,text) TO aitagent_app,aitagent_reader;
GRANT EXECUTE ON FUNCTION public.aitagent_expire_batch(),public.aitagent_archive_batch() TO aitagent_maintenance;
