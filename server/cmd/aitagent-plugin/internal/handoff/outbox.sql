-- Explicit disposable fixture only. Not a Mattermost KV or Octop migration.
CREATE TABLE aitagent_outbox_guard(id boolean PRIMARY KEY CHECK(id));
INSERT INTO aitagent_outbox_guard VALUES(true);
CREATE TABLE aitagent_outbox (
 delivery_key text PRIMARY KEY CHECK(delivery_key ~ '^[a-f0-9]{64}$'),
 fingerprint text NOT NULL CHECK(fingerprint ~ '^[a-f0-9]{64}$'),
 organization_id text NOT NULL, employee_id text NOT NULL, actor_id bigint NOT NULL,
 source_expires_at bigint NOT NULL,
 phase text NOT NULL CHECK(phase IN ('prepared','delivery_unknown','receiver_recorded')),
 revision integer NOT NULL CHECK(revision BETWEEN 1 AND 3),
 CHECK((phase='prepared' AND revision=1) OR (phase='delivery_unknown' AND revision=2) OR (phase='receiver_recorded' AND revision=3))
);
CREATE TABLE aitagent_outbox_audit (
 delivery_key text NOT NULL REFERENCES aitagent_outbox,
 revision integer NOT NULL CHECK(revision BETWEEN 1 AND 3),
 phase text NOT NULL CHECK(phase IN ('prepared','delivery_unknown','receiver_recorded')),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(delivery_key,revision)
);
CREATE OR REPLACE FUNCTION public.aitagent_outbox_audited() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM public.aitagent_outbox_audit a WHERE a.delivery_key=NEW.delivery_key AND a.revision=NEW.revision AND a.phase=NEW.phase) THEN
 RAISE EXCEPTION 'outbox audit required'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER aitagent_outbox_audited AFTER INSERT OR UPDATE ON aitagent_outbox
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.aitagent_outbox_audited();
CREATE OR REPLACE FUNCTION public.aitagent_outbox_transition() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.revision<>1 THEN RAISE EXCEPTION 'invalid initial outbox'; END IF;
 ELSIF NEW.revision<>OLD.revision+1 THEN
  RAISE EXCEPTION 'invalid outbox transition';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER aitagent_outbox_transition BEFORE INSERT OR UPDATE ON aitagent_outbox
 FOR EACH ROW EXECUTE FUNCTION public.aitagent_outbox_transition();
