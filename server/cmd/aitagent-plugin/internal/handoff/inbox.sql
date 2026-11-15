-- Standalone OFFLINE PostgreSQL fixture schema, not an Octop migration.
-- No goal, proof, token or key material is stored. Do not run against production.
CREATE TABLE aitagent_capacity_guard (id boolean PRIMARY KEY CHECK(id),marker bigint NOT NULL DEFAULT 0);
INSERT INTO aitagent_capacity_guard(id) VALUES(true);
CREATE TABLE aitagent_intake (
 delivery_key text PRIMARY KEY CHECK (delivery_key ~ '^[a-f0-9]{64}$'),
 fingerprint text NOT NULL CHECK (fingerprint ~ '^[a-f0-9]{64}$'),
 organization_id text NOT NULL CHECK(octet_length(organization_id) BETWEEN 1 AND 128),
 employee_id text NOT NULL CHECK(octet_length(employee_id) BETWEEN 1 AND 128),
 actor_id bigint NOT NULL CHECK (actor_id > 0),
 source_expires_at bigint NOT NULL CHECK (source_expires_at > 0),
 state text NOT NULL CHECK (state IN ('pending_authorization','authorization_observed','execution_observed','blocked','cancelled','expired')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 8),
 source_version bigint NOT NULL DEFAULT 1 CHECK (source_version > 0),
 terminal_at timestamptz,
 terminal_by text,
 archived_at timestamptz,
 archived_by text,
 archive_digest text,
 archive_event_count integer,
 archive_terminal_operation_id text,
 CHECK ((state IN ('cancelled','expired')) = (terminal_at IS NOT NULL AND terminal_by IS NOT NULL)),
 CHECK (archived_at IS NULL OR (state IN ('cancelled','expired') AND archived_by IS NOT NULL AND archive_digest ~ '^[a-f0-9]{64}$' AND archive_event_count=revision AND archive_terminal_operation_id IS NOT NULL))
);
CREATE TABLE aitagent_intake_audit (
 delivery_key text NOT NULL REFERENCES aitagent_intake(delivery_key),
 fingerprint text NOT NULL CHECK(fingerprint ~ '^[a-f0-9]{64}$'),
 action text NOT NULL CHECK (action IN ('pending_intake_recorded','authorization_observed','execution_observed','blocked','cancelled','expired')),
 revision bigint NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 8),
 operation_id text NOT NULL DEFAULT 'intake' CHECK(octet_length(operation_id) BETWEEN 1 AND 128),
 PRIMARY KEY(delivery_key,revision),
 UNIQUE(delivery_key,operation_id)
);

-- The runtime role cannot rewrite immutable receipt fields or age retention.
-- The trusted schema owner is intentionally outside this boundary (fixture
-- aging/fault injection and real migrations require owner privileges).
CREATE OR REPLACE FUNCTION public.aitagent_guard_row() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE owner_name text;
BEGIN
 SELECT pg_get_userbyid(relowner) INTO owner_name FROM pg_class WHERE oid=TG_RELID;
 IF current_user=owner_name THEN RETURN NEW; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.state<>'pending_authorization' OR NEW.revision<>1 OR NEW.terminal_at IS NOT NULL OR NEW.archived_at IS NOT NULL THEN
   RAISE EXCEPTION 'invalid initial intake';
  END IF;
 ELSE
  IF OLD.state IN ('cancelled','expired') OR OLD.archived_at IS NOT NULL OR NEW.state='expired' OR NEW.revision<>OLD.revision+1 OR NEW.source_version<OLD.source_version THEN
   RAISE EXCEPTION 'invalid intake transition';
  END IF;
  IF NEW.state='execution_observed' AND OLD.state<>'authorization_observed' THEN RAISE EXCEPTION 'invalid review order'; END IF;
  IF NEW.state='pending_authorization' THEN RAISE EXCEPTION 'invalid reset'; END IF;
  NEW.terminal_at:=CASE WHEN NEW.state='cancelled' THEN clock_timestamp() ELSE OLD.terminal_at END;
  NEW.terminal_by:=CASE WHEN NEW.state='cancelled' THEN session_user ELSE OLD.terminal_by END;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER aitagent_guard_row BEFORE INSERT OR UPDATE ON aitagent_intake FOR EACH ROW EXECUTE FUNCTION public.aitagent_guard_row();

-- State changes must have a corresponding immutable event in the same tx.
-- This does not prove the application's assertions or protect against an owner.
CREATE OR REPLACE FUNCTION public.aitagent_require_audit() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE expected_action text;
BEGIN
 IF NEW.archived_at IS NOT NULL THEN RETURN NULL; END IF;
 expected_action:=CASE WHEN NEW.state='pending_authorization' THEN 'pending_intake_recorded' ELSE NEW.state END;
 IF NOT EXISTS(SELECT 1 FROM public.aitagent_intake_audit a WHERE a.delivery_key=NEW.delivery_key AND a.revision=NEW.revision AND a.fingerprint=NEW.fingerprint AND a.action=expected_action) THEN
  RAISE EXCEPTION 'intake audit required';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER aitagent_require_audit AFTER INSERT OR UPDATE ON aitagent_intake
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.aitagent_require_audit();

CREATE OR REPLACE FUNCTION public.aitagent_validate_event() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM public.aitagent_intake i WHERE i.delivery_key=NEW.delivery_key AND i.fingerprint=NEW.fingerprint AND i.revision>=NEW.revision AND i.archived_at IS NULL) THEN
  RAISE EXCEPTION 'invalid intake event';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER aitagent_validate_event AFTER INSERT ON aitagent_intake_audit
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.aitagent_validate_event();
