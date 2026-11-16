//go:build offlinepg

// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
)

func outboxSetup(t *testing.T) (*lifecycleFixture, *Outbox) {
	t.Helper()
	f := lifecycleSetup(t)
	_, err := f.admin.Exec(`DROP TABLE IF EXISTS aitagent_outbox_audit,aitagent_outbox,aitagent_outbox_guard CASCADE;` + OfflineOutboxSchema + `
 ALTER TABLE aitagent_outbox OWNER TO aitagent_owner;
 ALTER TABLE aitagent_outbox_audit OWNER TO aitagent_owner;
 ALTER TABLE aitagent_outbox_guard OWNER TO aitagent_owner;
 ALTER FUNCTION aitagent_outbox_audited() OWNER TO aitagent_owner;
 ALTER FUNCTION aitagent_outbox_transition() OWNER TO aitagent_owner;
 REVOKE ALL ON aitagent_outbox,aitagent_outbox_audit,aitagent_outbox_guard FROM PUBLIC;
 GRANT SELECT ON aitagent_outbox,aitagent_outbox_audit,aitagent_outbox_guard TO aitagent_app;
 GRANT UPDATE(id) ON aitagent_outbox_guard TO aitagent_app;
 GRANT INSERT(delivery_key,fingerprint,organization_id,employee_id,actor_id,source_expires_at,phase,revision) ON aitagent_outbox TO aitagent_app;
 GRANT UPDATE(phase,revision) ON aitagent_outbox TO aitagent_app;
 GRANT INSERT(delivery_key,revision,phase) ON aitagent_outbox_audit TO aitagent_app;`)
	if err != nil {
		t.Fatal(err)
	}
	s := NewOfflineOutbox(true, f.app, func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
		return roleAuth(ctx, tx, p, c, ReviewAction("intake"))
	})
	return f, s
}
func prepareOutbox(t *testing.T, f *lifecycleFixture, s *Outbox) {
	t.Helper()
	if o := s.Prepare(context.Background(), f.proof, f.p, func() int64 { return 101 }, f.source); o != OutboxPrepared {
		t.Fatal(o)
	}
}
func beginOutbox(f *lifecycleFixture, s *Outbox) OutboxOutcome {
	return s.BeginAttempt(context.Background(), f.proof, f.p, func() int64 { return 101 }, f.source)
}
func TestOfflinePGOutboxUnknownReceiverCommitReconcilesOriginalIdentity(t *testing.T) {
	f, s := outboxSetup(t)
	prepareOutbox(t, f, s)
	if o := beginOutbox(f, s); o != OutboxAttempt {
		t.Fatal(o)
	}
	f.inbox.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost receiver ack")
	}
	if o := f.accept(); o != IntakeUnknown {
		t.Fatal(o)
	}
	// Restart sender object, then reconcile after the source has expired/vanished.
	s = NewOfflineOutbox(true, f.app, s.authorize)
	if o := beginOutbox(f, s); o != OutboxUnresolved {
		t.Fatal("resend permitted", o)
	}
	maintenance(t, f, false, 1)
	if o := s.Reconcile(context.Background(), f.candidate.DeliveryKey, f.p, f.inspect.Inspect); o != OutboxRecorded {
		t.Fatal(o)
	}
	if o := s.Reconcile(context.Background(), f.candidate.DeliveryKey, f.p, f.inspect.Inspect); o != OutboxRecorded {
		t.Fatal(o)
	}
	var events int
	if err := f.admin.QueryRow("SELECT count(*) FROM aitagent_outbox_audit").Scan(&events); err != nil || events != 3 {
		t.Fatal(events, err)
	}
	for _, q := range []string{"DELETE FROM aitagent_outbox", "DELETE FROM aitagent_outbox_audit", "UPDATE aitagent_outbox SET fingerprint=repeat('b',64)", "UPDATE aitagent_outbox SET phase='prepared',revision=1", "TRUNCATE aitagent_outbox CASCADE"} {
		deniedSQL(t, f.app, q)
	}
}
func TestOfflinePGOutboxCrashBeforeDeliveryNeverInfersAbsence(t *testing.T) {
	f, s := outboxSetup(t)
	prepareOutbox(t, f, s)
	s.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost sender ack")
	}
	if o := beginOutbox(f, s); o != OutboxUnknown {
		t.Fatal(o)
	}
	s = NewOfflineOutbox(true, f.app, s.authorize)
	if o := beginOutbox(f, s); o != OutboxUnresolved {
		t.Fatal(o)
	}
	// No receiver invocation occurred: this cannot distinguish loss from lag.
	if o := s.Reconcile(context.Background(), f.candidate.DeliveryKey, f.p, f.inspect.Inspect); o != OutboxUnresolved {
		t.Fatal(o)
	}
	var phase string
	if err := f.admin.QueryRow("SELECT phase FROM aitagent_outbox").Scan(&phase); err != nil || phase != "delivery_unknown" {
		t.Fatal(phase, err)
	}
}
func TestOfflinePGOutboxConcurrentAttemptAndBoundIdentity(t *testing.T) {
	f, s := outboxSetup(t)
	prepareOutbox(t, f, s)
	var wg sync.WaitGroup
	results := make(chan OutboxOutcome, 8)
	for j := 0; j < 8; j++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- beginOutbox(f, s) }()
	}
	wg.Wait()
	close(results)
	winners := 0
	for o := range results {
		if o == OutboxAttempt {
			winners++
		} else if o != OutboxUnresolved {
			t.Fatal(o)
		}
	}
	if winners != 1 {
		t.Fatal(winners)
	}
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	spoof := func(ctx context.Context, k string, p Policy) (StoredState, ReviewOutcome) {
		r, o := f.inspect.Inspect(ctx, k, p)
		r.Fingerprint = op(99)
		return r, o
	}
	if o := s.Reconcile(context.Background(), f.candidate.DeliveryKey, f.p, spoof); o != OutboxDenied {
		t.Fatal(o)
	}
	// Permission revoked after remote read must prevent local confirmation.
	revoke := func(ctx context.Context, k string, p Policy) (StoredState, ReviewOutcome) {
		r, o := f.inspect.Inspect(ctx, k, p)
		if _, err := f.admin.Exec("UPDATE aitagent_test_grants SET active=false"); err != nil {
			t.Fatal(err)
		}
		return r, o
	}
	if o := s.Reconcile(context.Background(), f.candidate.DeliveryKey, f.p, revoke); o != OutboxDenied {
		t.Fatal(o)
	}
}
func TestOfflinePGOutboxSourceCancelAndAuditRollback(t *testing.T) {
	f, s := outboxSetup(t)
	prepareOutbox(t, f, s)
	cancelled := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(f.i, "cancelled"), nil
	}
	if o := s.BeginAttempt(context.Background(), f.proof, f.p, func() int64 { return 101 }, cancelled); o != OutboxDenied {
		t.Fatal(o)
	}
	if _, err := f.admin.Exec("ALTER TABLE aitagent_outbox_audit ADD CONSTRAINT outbox_fault CHECK(revision<>2) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if o := beginOutbox(f, s); o != OutboxUnavailable {
		t.Fatal(o)
	}
	var phase string
	if err := f.admin.QueryRow("SELECT phase FROM aitagent_outbox").Scan(&phase); err != nil || phase != "prepared" {
		t.Fatal(phase, err)
	}
	if _, err := f.admin.Exec("ALTER TABLE aitagent_outbox_audit DROP CONSTRAINT outbox_fault"); err != nil {
		t.Fatal(err)
	}
	if o := beginOutbox(f, s); o != OutboxAttempt {
		t.Fatal(o)
	}
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	if o := f.state.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1)); o != ReviewCancelled {
		t.Fatal(o)
	}
	ageTerminals(t, f)
	maintenance(t, f, true, 1)
	s.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost reconcile ack")
	}
	if o := s.Reconcile(context.Background(), f.candidate.DeliveryKey, f.p, f.inspect.Inspect); o != OutboxUnknown {
		t.Fatal(o)
	}
	s = NewOfflineOutbox(true, f.app, s.authorize)
	if o := s.Reconcile(context.Background(), f.candidate.DeliveryKey, f.p, f.inspect.Inspect); o != OutboxRecorded {
		t.Fatal(o)
	}
}

func TestOfflinePGOutboxCapacityAndCorruptAuditFailClosed(t *testing.T) {
	f, s := outboxSetup(t)
	if _, err := f.admin.Exec(`BEGIN;
 INSERT INTO aitagent_outbox(delivery_key,fingerprint,organization_id,employee_id,actor_id,source_expires_at,phase,revision)
 SELECT lpad(to_hex(n),64,'0'),lpad(to_hex(n),64,'0'),'target-org','target-worker',9,1,'prepared',1 FROM generate_series(1,128) n;
 INSERT INTO aitagent_outbox_audit(delivery_key,revision,phase) SELECT delivery_key,1,'prepared' FROM aitagent_outbox;
 COMMIT`); err != nil {
		t.Fatal(err)
	}
	if o := s.Prepare(context.Background(), f.proof, f.p, func() int64 { return 101 }, f.source); o != OutboxFull {
		t.Fatal(o)
	}
	// New disposable fixture, not removal of production identities.
	f, s = outboxSetup(t)
	prepareOutbox(t, f, s)
	if _, err := f.admin.Exec("DELETE FROM aitagent_outbox_audit"); err != nil {
		t.Fatal(err)
	}
	if o := beginOutbox(f, s); o != OutboxUnavailable {
		t.Fatal(o)
	}
}

func TestOfflinePGOutboxClockLockAndLastSourceReadBoundary(t *testing.T) {
	f, s := outboxSetup(t)
	prepareOutbox(t, f, s)
	readings := []int64{101, 100, 101, 101, 101}
	index := 0
	clock := func() int64 {
		n := readings[index]
		if index < len(readings)-1 {
			index++
		}
		return n
	}
	if o := s.BeginAttempt(context.Background(), f.proof, f.p, clock, f.source); o != OutboxDenied {
		t.Fatal(o)
	}
	tx, err := f.admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("SELECT * FROM aitagent_outbox FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	if o := beginOutbox(f, s); o != OutboxUnavailable {
		t.Fatal(o)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	cancelled := false
	lastRead := func(context.Context, Binding, string) (SourceRecord, error) {
		r := observeSource(f.i, "awaiting_octop_authorization")
		cancelled = true
		return r, nil
	}
	// Cancellation after observation cannot be made atomic with the sender DB.
	// Recording an attempt is not an execution grant, nor proof of current source.
	if o := s.BeginAttempt(context.Background(), f.proof, f.p, func() int64 { return 101 }, lastRead); o != OutboxAttempt || !cancelled {
		t.Fatal(o, cancelled)
	}
}
