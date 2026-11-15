-- Disposable fixture only: owner-defined, narrow maintenance capabilities.
-- No caller-supplied cutoff, key, actor, path, SQL or unbounded batch size.
CREATE OR REPLACE FUNCTION public.aitagent_expire_batch() RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,pg_temp SET timezone='UTC' AS $$
DECLARE r record; n integer:=0; at_time timestamptz:=clock_timestamp();
BEGIN
 PERFORM set_config('lock_timeout','250ms',true);
 PERFORM id FROM public.aitagent_capacity_guard WHERE id=true FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'missing capacity guard'; END IF;
 FOR r IN SELECT * FROM public.aitagent_intake
  WHERE archived_at IS NULL AND state NOT IN ('cancelled','expired')
   AND source_expires_at<=floor(extract(epoch FROM at_time)*1000)::bigint
  ORDER BY delivery_key LIMIT 32 FOR UPDATE SKIP LOCKED
 LOOP
  UPDATE public.aitagent_intake SET state='expired',revision=revision+1,terminal_at=at_time,terminal_by=session_user
   WHERE delivery_key=r.delivery_key;
  INSERT INTO public.aitagent_intake_audit(delivery_key,fingerprint,action,revision,operation_id)
   VALUES(r.delivery_key,r.fingerprint,'expired',r.revision+1,'expire:'||r.delivery_key);
  n:=n+1;
 END LOOP;
 RETURN n;
END $$;
REVOKE ALL ON FUNCTION public.aitagent_expire_batch() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.aitagent_archive_batch() RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public,pg_temp SET timezone='UTC' AS $$
DECLARE r record; n integer:=0; at_time timestamptz:=clock_timestamp();
 history jsonb; entries integer; first_revision bigint; last_revision bigint;
 matching boolean; terminal_action text; terminal_op text; digest text;
BEGIN
 PERFORM set_config('lock_timeout','250ms',true);
 PERFORM id FROM public.aitagent_capacity_guard WHERE id=true FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'missing capacity guard'; END IF;
 FOR r IN SELECT * FROM public.aitagent_intake
  WHERE archived_at IS NULL AND state IN ('cancelled','expired')
   AND terminal_at<=at_time-interval '30 days'
   AND source_expires_at<=floor(extract(epoch FROM at_time-interval '30 days')*1000)::bigint
  ORDER BY delivery_key LIMIT 32 FOR UPDATE SKIP LOCKED
 LOOP
  SELECT jsonb_agg(jsonb_build_array(revision,action,operation_id,fingerprint) ORDER BY revision),
   count(*)::integer,min(revision),max(revision),bool_and(fingerprint=r.fingerprint)
   INTO history,entries,first_revision,last_revision,matching
   FROM public.aitagent_intake_audit WHERE delivery_key=r.delivery_key;
  IF entries<>r.revision OR first_revision<>1 OR last_revision<>r.revision OR matching IS DISTINCT FROM true THEN
   RAISE EXCEPTION 'incomplete archive history';
  END IF;
  SELECT action,operation_id INTO terminal_action,terminal_op FROM public.aitagent_intake_audit
   WHERE delivery_key=r.delivery_key AND revision=r.revision;
  IF terminal_action<>r.state THEN RAISE EXCEPTION 'terminal event mismatch'; END IF;
  digest:=encode(sha256(convert_to(jsonb_build_array('aitagent-archive/v1',r.delivery_key,r.fingerprint,r.state,history)::text,'UTF8')),'hex');
  UPDATE public.aitagent_intake SET archived_at=at_time,archived_by=session_user,
   archive_digest=digest,archive_event_count=entries,archive_terminal_operation_id=terminal_op
   WHERE delivery_key=r.delivery_key;
  DELETE FROM public.aitagent_intake_audit WHERE delivery_key=r.delivery_key;
  n:=n+1;
 END LOOP;
 RETURN n;
END $$;
REVOKE ALL ON FUNCTION public.aitagent_archive_batch() FROM PUBLIC;
