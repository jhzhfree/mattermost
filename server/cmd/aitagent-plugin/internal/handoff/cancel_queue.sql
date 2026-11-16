-- Explicit isolated fixture only; not a production migration or scheduler.
CREATE TABLE aitagent_cancel_queue (
 delivery_key text PRIMARY KEY REFERENCES aitagent_outbox(delivery_key),
 fingerprint text NOT NULL CHECK(fingerprint ~ '^[a-f0-9]{64}$'),
 observed_source_version bigint NOT NULL CHECK(observed_source_version>0),
 phase text NOT NULL CHECK(phase IN ('pending','confirmed','attention')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 16),
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_token text,
 lease_until timestamptz,
 receiver_terminal text,
 CHECK((lease_token IS NULL)=(lease_until IS NULL)),
 CHECK(lease_token IS NULL OR (phase='pending' AND lease_token ~ '^[a-f0-9]{64}$')),
 CHECK((phase='confirmed')=(receiver_terminal IS NOT NULL)),
 CHECK(receiver_terminal IS NULL OR receiver_terminal IN ('cancelled','expired'))
);
CREATE TABLE aitagent_cancel_queue_audit (
 delivery_key text NOT NULL REFERENCES aitagent_cancel_queue(delivery_key),
 attempt integer NOT NULL CHECK(attempt BETWEEN 0 AND 16),
 event text NOT NULL CHECK(event IN ('queued','claimed','retry','confirmed','attention')),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(delivery_key,attempt,event)
);
CREATE OR REPLACE FUNCTION public.aitagent_cancel_queue_audited() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE expected text;
BEGIN
 expected:=CASE WHEN NEW.lease_token IS NOT NULL THEN 'claimed' WHEN NEW.attempts=0 THEN 'queued' WHEN NEW.phase='pending' THEN 'retry' ELSE NEW.phase END;
 IF NOT EXISTS(SELECT 1 FROM public.aitagent_cancel_queue_audit a WHERE a.delivery_key=NEW.delivery_key AND a.attempt=NEW.attempts AND a.event=expected) THEN RAISE EXCEPTION 'cancellation queue audit required'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER aitagent_cancel_queue_audited AFTER INSERT OR UPDATE ON aitagent_cancel_queue
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.aitagent_cancel_queue_audited();
CREATE OR REPLACE FUNCTION public.aitagent_cancel_queue_transition() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public,pg_temp AS $$
DECLARE owner_name text;
BEGIN
 SELECT pg_get_userbyid(relowner) INTO owner_name FROM pg_class WHERE oid=TG_RELID;
 IF current_user=owner_name THEN RETURN NEW; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.phase<>'pending' OR NEW.attempts<>0 OR NEW.lease_token IS NOT NULL THEN RAISE EXCEPTION 'invalid initial cancellation queue'; END IF;
  NEW.next_attempt_at:=clock_timestamp();
 ELSIF OLD.phase<>'pending' THEN RAISE EXCEPTION 'terminal cancellation queue';
 ELSIF NEW.attempts=OLD.attempts+1 THEN
  IF NEW.phase<>'pending' OR NEW.lease_token IS NULL OR OLD.next_attempt_at>clock_timestamp() OR OLD.lease_until>clock_timestamp() THEN RAISE EXCEPTION 'invalid cancellation claim'; END IF;
  NEW.lease_until:=clock_timestamp()+interval '5 seconds';
  NEW.next_attempt_at:=OLD.next_attempt_at;
 ELSIF NEW.attempts=OLD.attempts AND NEW.attempts>0 THEN
  IF NEW.lease_token IS NOT NULL OR (NEW.phase='attention' AND NEW.attempts<>16) OR (NEW.phase<>'attention' AND (OLD.lease_token IS NULL OR OLD.lease_until<=clock_timestamp())) THEN RAISE EXCEPTION 'invalid cancellation completion'; END IF;
  NEW.next_attempt_at:=clock_timestamp()+least(60,power(2,NEW.attempts-1))::integer*interval '1 second';
 ELSE RAISE EXCEPTION 'invalid cancellation attempt';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER aitagent_cancel_queue_transition BEFORE INSERT OR UPDATE ON aitagent_cancel_queue
 FOR EACH ROW EXECUTE FUNCTION public.aitagent_cancel_queue_transition();
