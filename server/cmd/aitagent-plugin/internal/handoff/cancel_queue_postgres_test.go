//go:build offlinepg

// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
)

func cancelQueueSetup(t *testing.T) (*lifecycleFixture, *Outbox, CancellationTarget) {
	t.Helper()
	f, s := outboxSetup(t)
	_, err := f.admin.Exec(`DROP TABLE IF EXISTS aitagent_cancel_queue_audit,aitagent_cancel_queue CASCADE;` + OfflineCancelQueueSchema + `
 ALTER TABLE aitagent_cancel_queue OWNER TO aitagent_owner;
 ALTER TABLE aitagent_cancel_queue_audit OWNER TO aitagent_owner;
 ALTER FUNCTION aitagent_cancel_queue_audited() OWNER TO aitagent_owner;
 ALTER FUNCTION aitagent_cancel_queue_transition() OWNER TO aitagent_owner;
 REVOKE ALL ON aitagent_cancel_queue,aitagent_cancel_queue_audit FROM PUBLIC;
 GRANT SELECT ON aitagent_cancel_queue,aitagent_cancel_queue_audit TO aitagent_app;
 GRANT INSERT(delivery_key,fingerprint,observed_source_version,phase) ON aitagent_cancel_queue TO aitagent_app;
 GRANT UPDATE(phase,attempts,next_attempt_at,lease_token,lease_until,receiver_terminal) ON aitagent_cancel_queue TO aitagent_app;
 GRANT INSERT(delivery_key,attempt,event) ON aitagent_cancel_queue_audit TO aitagent_app;`)
	if err != nil {
		t.Fatal(err)
	}
	s.cancelAuthorize = func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
		return roleAuth(ctx, tx, p, c, ReviewCancel)
	}
	prepareOutbox(t, f, s)
	if o := beginOutbox(f, s); o != OutboxAttempt {
		t.Fatal(o)
	}
	target := CancellationTarget{Inspect: f.inspect.Inspect, Cancel: func(ctx context.Context, k string, p Policy, r int64, op string) ReviewOutcome {
		return f.state.PropagateCancellation(ctx, k, p, r, op, cancelEvidence(f))
	}}
	if o := s.QueueCancellation(context.Background(), f.candidate.DeliveryKey, f.p, cancelEvidence(f)); o != CancelQueued {
		t.Fatal(o)
	}
	return f, s, target
}
func dueCancel(t *testing.T, f *lifecycleFixture) {
	t.Helper()
	if _, err := f.admin.Exec("BEGIN; SET LOCAL ROLE aitagent_owner; UPDATE aitagent_cancel_queue SET next_attempt_at=clock_timestamp()-interval '1 minute',lease_until=CASE WHEN lease_token IS NULL THEN NULL ELSE clock_timestamp()-interval '1 minute' END WHERE phase='pending'; COMMIT"); err != nil {
		t.Fatal(err)
	}
}
func queueSnapshot(t *testing.T, f *lifecycleFixture) (string, int) {
	t.Helper()
	var p string
	var a int
	if err := f.admin.QueryRow("SELECT phase,attempts FROM aitagent_cancel_queue").Scan(&p, &a); err != nil {
		t.Fatal(err)
	}
	return p, a
}
func TestOfflinePGCancelQueueEarlyCancelLateIntakeAndRestart(t *testing.T) {
	f, s, target := cancelQueueSetup(t)
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueued {
		t.Fatal(o)
	}
	if phase, a := queueSnapshot(t, f); phase != "pending" || a != 1 {
		t.Fatal(phase, a)
	}
	// The receiver's earlier source observation may still commit after cancellation.
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	s = NewOfflineOutboxWithCancellation(true, f.app, s.authorize, s.cancelAuthorize) // no in-memory retry history
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelDeferred {
		t.Fatal(o)
	}
	dueCancel(t, f)
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelConfirmed {
		t.Fatal(o)
	}
	if r := f.view(t); r.State != "cancelled" {
		t.Fatal(r)
	}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelConfirmed {
		t.Fatal(o)
	}
	if phase, a := queueSnapshot(t, f); phase != "confirmed" || a != 2 {
		t.Fatal(phase, a)
	}
	for _, q := range []string{"DELETE FROM aitagent_cancel_queue", "DELETE FROM aitagent_cancel_queue_audit", "UPDATE aitagent_cancel_queue SET fingerprint=repeat('a',64)", "UPDATE aitagent_cancel_queue SET phase='pending',attempts=0,receiver_terminal=NULL"} {
		deniedSQL(t, f.app, q)
	}
}
func TestOfflinePGCancelQueueUnknownClaimsAndAcknowledgements(t *testing.T) {
	f, s, target := cancelQueueSetup(t)
	calls := 0
	inspect := target.Inspect
	target.Inspect = func(ctx context.Context, k string, p Policy) (StoredState, ReviewOutcome) {
		calls++
		return inspect(ctx, k, p)
	}
	s.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost claim ack")
	}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueueUnknown || calls != 0 {
		t.Fatal(o, calls)
	}
	s = NewOfflineOutboxWithCancellation(true, f.app, s.authorize, s.cancelAuthorize)
	dueCancel(t, f)
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	// Receiver commit error is NOT trusted: the independent Inspect confirms it.
	f.state.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost receiver cancel ack")
	}
	commits := 0
	s.commit = func(tx *sql.Tx) error {
		commits++
		if err := tx.Commit(); err != nil {
			return err
		}
		if commits == 2 {
			return errors.New("lost confirmation ack")
		}
		return nil
	}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueueUnknown {
		t.Fatal(o)
	}
	s = NewOfflineOutboxWithCancellation(true, f.app, s.authorize, s.cancelAuthorize)
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelConfirmed {
		t.Fatal(o)
	}
	if len(f.view(t).Events) != 2 {
		t.Fatal("duplicate cancellation")
	}
}
func TestOfflinePGCancelQueueConcurrentClaimAndStaleWorkerFence(t *testing.T) {
	f, s, target := cancelQueueSetup(t)
	entered := make(chan struct{})
	resume := make(chan struct{})
	var calls atomic.Int32
	target.Inspect = func(ctx context.Context, k string, p Policy) (StoredState, ReviewOutcome) {
		calls.Add(1)
		close(entered)
		<-resume
		return StoredState{}, ReviewUnavailable
	}
	result := make(chan CancelQueueOutcome, 1)
	go func() { result <- s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target) }()
	<-entered
	var wg sync.WaitGroup
	out := make(chan CancelQueueOutcome, 4)
	for n := 0; n < 4; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out <- s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target)
		}()
	}
	wg.Wait()
	close(out)
	for o := range out {
		if o != CancelDeferred {
			t.Fatal(o)
		}
	}
	// Owner-only time fault injection emulates a lease expiring during a stuck RPC.
	dueCancel(t, f)
	newer := CancellationTarget{Inspect: f.inspect.Inspect, Cancel: target.Cancel}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, newer); o != CancelQueued {
		t.Fatal(o)
	}
	close(resume)
	if o := <-result; o != CancelDeferred {
		t.Fatal("stale worker overwrote newer attempt", o)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	if phase, a := queueSnapshot(t, f); phase != "pending" || a != 2 {
		t.Fatal(phase, a)
	}
}
func TestOfflinePGCancelQueueBoundedEvidenceFailureAndAuditRollback(t *testing.T) {
	f, s, target := cancelQueueSetup(t)
	if _, err := f.admin.Exec("ALTER TABLE aitagent_cancel_queue_audit ADD CONSTRAINT queue_fault CHECK(event<>'claimed') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueueUnavailable {
		t.Fatal(o)
	}
	if _, a := queueSnapshot(t, f); a != 0 {
		t.Fatal("partial claim", a)
	}
	if _, err := f.admin.Exec("ALTER TABLE aitagent_cancel_queue_audit DROP CONSTRAINT queue_fault"); err != nil {
		t.Fatal(err)
	}
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	target.Cancel = func(ctx context.Context, k string, p Policy, r int64, op string) ReviewOutcome {
		return f.state.PropagateCancellation(ctx, k, p, r, op, func(context.Context) (CancellationEvidence, error) {
			return CancellationEvidence{}, errors.New("source evidence expired or unavailable")
		})
	}
	for n := 1; n <= 16; n++ {
		dueCancel(t, f)
		o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target)
		want := CancelQueued
		if n == 16 {
			want = CancelAttention
		}
		if o != want {
			t.Fatal(n, o)
		}
	}
	if o := s.QueueCancellation(context.Background(), f.candidate.DeliveryKey, f.p, cancelEvidence(f)); o != CancelAttention {
		t.Fatal("duplicate reset retry budget", o)
	}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelAttention {
		t.Fatal(o)
	}
	if r := f.view(t); r.State != "pending_authorization" {
		t.Fatal("invented cancellation", r)
	}
	var events int
	if err := f.admin.QueryRow("SELECT count(*) FROM aitagent_cancel_queue_audit").Scan(&events); err != nil || events != 33 {
		t.Fatal(events, err)
	}
}
func TestOfflinePGCancelQueuePermissionsAndWrongIdentity(t *testing.T) {
	f, s, target := cancelQueueSetup(t)
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	target.Inspect = func(ctx context.Context, k string, p Policy) (StoredState, ReviewOutcome) {
		r, o := f.inspect.Inspect(ctx, k, p)
		r.Fingerprint = "wrong"
		r.State = "cancelled"
		return r, o
	}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueued {
		t.Fatal(o)
	}
	dueCancel(t, f)
	target.Inspect = func(ctx context.Context, k string, p Policy) (StoredState, ReviewOutcome) {
		r, o := f.inspect.Inspect(ctx, k, p)
		if _, err := f.admin.Exec("UPDATE aitagent_test_grants SET active=false"); err != nil {
			t.Fatal(err)
		}
		return r, o
	}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueueDenied {
		t.Fatal(o)
	}
	if phase, _ := queueSnapshot(t, f); phase != "pending" {
		t.Fatal("revoked sender acknowledged")
	}
}

func TestOfflinePGCancelQueueResumeInNewProcess(t *testing.T) {
	if key := os.Getenv("AITAGENT_CANCEL_RECOVERY_KEY"); key != "" {
		// Child process opens fresh restricted-role connections, no parent objects.
		db := roleDB(t, "aitagent_app")
		reader := roleDB(t, "aitagent_reader")
		p := Policy{Enabled: true, KeyID: "test-key", OctopActorID: 9, OctopOrganizationID: "target-org", OctopEmployeeID: "target-worker"}
		intake := func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
			return roleAuth(ctx, tx, p, c, ReviewAction("intake"))
		}
		cancelAuth := func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
			return roleAuth(ctx, tx, p, c, ReviewCancel)
		}
		s := NewOfflineOutboxWithCancellation(true, db, intake, cancelAuth)
		machine := NewOfflineStateMachine(true, db, roleAuth)
		inspect := NewOfflineStateMachine(true, reader, roleAuth)
		source := func(context.Context) (CancellationEvidence, error) {
			return CancellationEvidence{DeliveryKey: key, Fingerprint: os.Getenv("AITAGENT_CANCEL_RECOVERY_FP"), Version: 50, Cancelled: true}, nil
		}
		target := CancellationTarget{Inspect: inspect.Inspect, Cancel: func(ctx context.Context, k string, p Policy, r int64, op string) ReviewOutcome {
			return machine.PropagateCancellation(ctx, k, p, r, op, source)
		}}
		keys, o := s.DueCancellations(context.Background(), p)
		if o != CancelQueued || len(keys) != 1 || keys[0] != key {
			t.Fatal(keys, o)
		}
		if o = s.RetryCancellation(context.Background(), keys[0], p, target); o != CancelConfirmed {
			t.Fatal(o)
		}
		return
	}
	f, s, target := cancelQueueSetup(t)
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueued {
		t.Fatal(o)
	}
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	dueCancel(t, f)
	cmd := exec.Command(os.Args[0], "-test.run=^TestOfflinePGCancelQueueResumeInNewProcess$", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "AITAGENT_CANCEL_RECOVERY_KEY="+f.candidate.DeliveryKey, "AITAGENT_CANCEL_RECOVERY_FP="+f.candidate.Fingerprint)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolated child process: %v: %s", err, output)
	}
	if phase, a := queueSnapshot(t, f); phase != "confirmed" || a != 2 {
		t.Fatal(phase, a)
	}
	if r := f.view(t); r.State != "cancelled" {
		t.Fatal(r)
	}
}

func TestOfflinePGCancelQueueSeparatePermissionAndMissingHistory(t *testing.T) {
	f, s, target := cancelQueueSetup(t)
	s.cancelAuthorize = nil
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueueDenied {
		t.Fatal("intake rights allowed cancellation", o)
	}
	if keys, o := s.DueCancellations(context.Background(), f.p); o != CancelQueueDenied || len(keys) != 0 {
		t.Fatal(keys, o)
	}
	s.cancelAuthorize = func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
		return roleAuth(ctx, tx, p, c, ReviewCancel)
	}
	if _, err := f.admin.Exec("DELETE FROM aitagent_cancel_queue_audit"); err != nil {
		t.Fatal(err)
	}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueueUnavailable {
		t.Fatal("lost evidence ignored", o)
	}
	if _, a := queueSnapshot(t, f); a != 0 {
		t.Fatal(a)
	}
}

func TestOfflinePGCancelQueueLastClaimCrashBecomesAttention(t *testing.T) {
	f, s, target := cancelQueueSetup(t)
	calls := 0
	inspect := target.Inspect
	target.Inspect = func(ctx context.Context, k string, p Policy) (StoredState, ReviewOutcome) {
		calls++
		return inspect(ctx, k, p)
	}
	for n := 0; n < 15; n++ {
		dueCancel(t, f)
		if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueued {
			t.Fatal(n, o)
		}
	}
	dueCancel(t, f)
	s.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("last claim acknowledgement lost")
	}
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelQueueUnknown || calls != 15 {
		t.Fatal(o, calls)
	}
	s = NewOfflineOutboxWithCancellation(true, f.app, s.authorize, s.cancelAuthorize)
	dueCancel(t, f)
	if o := s.RetryCancellation(context.Background(), f.candidate.DeliveryKey, f.p, target); o != CancelAttention || calls != 15 {
		t.Fatal(o, calls)
	}
	if phase, a := queueSnapshot(t, f); phase != "attention" || a != 16 {
		t.Fatal(phase, a)
	}
}
