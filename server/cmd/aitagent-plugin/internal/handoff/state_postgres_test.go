//go:build offlinepg

// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stateFixture struct {
	db          *sql.DB
	i           Intent
	p           Policy
	proof       []byte
	candidate   Candidate
	machine     *StateMachine
	source      SourceReader
	version     atomic.Uint64
	unavailable atomic.Bool
	clock       atomic.Int64
}

func stateSetup(t *testing.T) *stateFixture {
	db := resetOfflineDB(t)
	if _, err := db.Exec(`DROP TABLE IF EXISTS aitagent_test_actions;
 CREATE TABLE aitagent_test_actions(action text PRIMARY KEY,allowed boolean);
 INSERT INTO aitagent_test_actions VALUES('authorization_observed',true),('execution_observed',true),('inspect',true),('cancelled',true);`); err != nil {
		t.Fatal(err)
	}
	i, p, key := fixture(t)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	candidate, _ := Verify(proof, p, 101)
	f := &stateFixture{db: db, i: i, p: p, proof: proof, candidate: candidate}
	f.clock.Store(101)
	f.source = func(context.Context, Binding, string) (SourceRecord, error) {
		if f.unavailable.Load() {
			return SourceRecord{}, errors.New("synthetic unavailable")
		}
		record := observeSource(i, "awaiting_octop_authorization")
		record.Version = f.version.Add(1)
		return record, nil
	}
	if got := NewOfflineInbox(true, db, fixtureAuth).Accept(context.Background(), proof, p, f.clock.Load, f.source); got != IntakePending {
		t.Fatal(got)
	}
	f.machine = NewOfflineStateMachine(true, db, stateAuth)
	return f
}
func stateAuth(ctx context.Context, tx *sql.Tx, p Policy, c Candidate, action ReviewAction) error {
	if err := fixtureAuth(ctx, tx, p, c); err != nil {
		return err
	}
	var allowed bool
	err := tx.QueryRowContext(ctx, "SELECT allowed FROM aitagent_test_actions WHERE action=$1 FOR SHARE", string(action)).Scan(&allowed)
	if err != nil || !allowed {
		return errors.New("action denied")
	}
	return nil
}
func op(n int) string { return fmt.Sprintf("%064x", n) }
func (f *stateFixture) review(rev int64, id int, action ReviewAction) ReviewOutcome {
	return f.machine.Review(context.Background(), f.proof, f.p, rev, op(id), action, f.clock.Load, f.source)
}
func (f *stateFixture) inspect(t *testing.T) StoredState {
	t.Helper()
	state, outcome := f.machine.Inspect(context.Background(), f.candidate.DeliveryKey, f.p)
	if outcome != ReviewRecorded {
		t.Fatal(outcome)
	}
	return state
}
func assertState(t *testing.T, f *stateFixture, name string, revision int64) {
	t.Helper()
	s := f.inspect(t)
	if s.State != name || s.Revision != revision {
		t.Fatalf("state=%+v want %s/%d", s, name, revision)
	}
	var n int
	if err := f.db.QueryRow("SELECT count(*) FROM aitagent_intake_audit").Scan(&n); err != nil || n != int(revision) {
		t.Fatal("audit not atomic", n, err)
	}
}

func TestOfflinePGStateStagesAndUnknownRecovery(t *testing.T) {
	f := stateSetup(t)
	if got := f.review(1, 1, ReviewExecution); got != ReviewDenied {
		t.Fatal("execution review skipped authorization", got)
	}
	if got := f.review(1, 2, ReviewAuthorization); got != ReviewRecorded {
		t.Fatal(got)
	}
	assertState(t, f, "authorization_observed", 2)
	if got := f.review(2, 3, ReviewExecution); got != ReviewRecorded {
		t.Fatal(got)
	}
	assertState(t, f, "execution_observed", 3)
	if got := f.review(2, 3, ReviewExecution); got != ReviewDuplicate {
		t.Fatal(got)
	}
	assertState(t, f, "execution_observed", 3)
	if _, err := f.db.Exec("UPDATE aitagent_test_actions SET allowed=false WHERE action='authorization_observed'"); err != nil {
		t.Fatal(err)
	}
	if got := f.review(3, 4, ReviewAuthorization); got != ReviewDenied {
		t.Fatal("action revocation ignored", got)
	}
	if _, err := f.db.Exec("UPDATE aitagent_test_actions SET allowed=true"); err != nil {
		t.Fatal(err)
	}
	if got := f.review(3, 4, ReviewAuthorization); got != ReviewRecorded {
		t.Fatal(got)
	}
	f.unavailable.Store(true)
	if got := f.review(4, 5, ReviewExecution); got != ReviewDenied {
		t.Fatal(got)
	}
	assertState(t, f, "blocked", 5)
	f.unavailable.Store(false)
	f.machine.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost acknowledgement")
	}
	if got := f.review(5, 6, ReviewAuthorization); got != ReviewUnknown {
		t.Fatal(got)
	}
	f.machine = NewOfflineStateMachine(true, openOfflineDB(t), stateAuth)
	assertState(t, f, "authorization_observed", 6)
	events := f.inspect(t).Events
	if events[len(events)-1].OperationID != op(6) {
		t.Fatal("unknown operation could not be reconciled by its original ID")
	}
	if got := f.review(5, 6, ReviewAuthorization); got != ReviewDuplicate {
		t.Fatal(got)
	}
	// Reconcile and terminal cancellation need current local permission, not an
	// expired proof, source availability, or a guessed replacement request.
	f.clock.Store(200)
	f.unavailable.Store(true)
	assertState(t, f, "authorization_observed", 6)
	if got := f.machine.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 6, op(7)); got != ReviewCancelled {
		t.Fatal(got)
	}
	assertState(t, f, "cancelled", 7)
	if got := f.machine.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 6, op(7)); got != ReviewCancelled {
		t.Fatal(got)
	}
	f.clock.Store(101)
	f.unavailable.Store(false)
	if got := f.review(7, 8, ReviewAuthorization); got != ReviewDenied {
		t.Fatal("terminal cancellation revived", got)
	}
	if got := NewOfflineInbox(true, f.db, fixtureAuth).Accept(context.Background(), f.proof, f.p, f.clock.Load, f.source); got != IntakeRejected {
		t.Fatal("intake revived cancelled row", got)
	}
	wrong := f.p
	wrong.OctopActorID++
	if _, got := f.machine.Inspect(context.Background(), f.candidate.DeliveryKey, wrong); got != ReviewDenied {
		t.Fatal("cross-actor reconciliation", got)
	}
}

func TestOfflinePGStateStaleVersionAuditRollbackAndBound(t *testing.T) {
	f := stateSetup(t)
	stale := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(f.i, "awaiting_octop_authorization"), nil
	}
	if got := f.machine.Review(context.Background(), f.proof, f.p, 1, op(1), ReviewAuthorization, f.clock.Load, stale); got != ReviewDenied {
		t.Fatal(got)
	}
	assertState(t, f, "blocked", 2)
	if _, err := f.db.Exec("ALTER TABLE aitagent_intake_audit ADD CONSTRAINT fail_review CHECK(action='pending_intake_recorded') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if got := f.review(2, 2, ReviewAuthorization); got != ReviewUnavailable {
		t.Fatal(got)
	}
	assertState(t, f, "blocked", 2)
	if _, err := f.db.Exec("ALTER TABLE aitagent_intake_audit DROP CONSTRAINT fail_review"); err != nil {
		t.Fatal(err)
	}
	for revision := int64(2); revision < 7; revision++ {
		if got := f.review(revision, int(revision)+10, ReviewAuthorization); got != ReviewRecorded {
			t.Fatal(got)
		}
	}
	assertState(t, f, "authorization_observed", 7)
	if got := f.review(7, 30, ReviewExecution); got != ReviewDenied {
		t.Fatal("history cap exceeded", got)
	}
	if got := f.machine.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 7, op(31)); got != ReviewCancelled {
		t.Fatal("reserved cancel slot unavailable", got)
	}
	assertState(t, f, "cancelled", 8)
}

func TestOfflinePGLocalCancellationFencesInFlightReview(t *testing.T) {
	f := stateSetup(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	reader := func(ctx context.Context, b Binding, id string) (SourceRecord, error) {
		record, err := f.source(ctx, b, id)
		close(entered)
		<-release
		return record, err
	}
	done := make(chan ReviewOutcome, 1)
	unblock := sync.OnceFunc(func() { close(release) })
	joined := false
	defer func() {
		unblock()
		if !joined {
			<-done
		}
	}()
	go func() {
		done <- f.machine.Review(context.Background(), f.proof, f.p, 1, op(1), ReviewAuthorization, f.clock.Load, reader)
	}()
	<-entered
	other := NewOfflineStateMachine(true, openOfflineDB(t), stateAuth)
	if got := other.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(2)); got != ReviewCancelled {
		t.Fatal(got)
	}
	unblock()
	got := <-done
	joined = true
	if got != ReviewDenied {
		t.Fatal("in-flight review revived local cancellation", got)
	}
	assertState(t, f, "cancelled", 2)
}

func TestOfflinePGStateCannotEliminateLastRemoteReadRace(t *testing.T) {
	f := stateSetup(t)
	if got := f.review(1, 1, ReviewAuthorization); got != ReviewRecorded {
		t.Fatal(got)
	}
	f.machine.authorize = func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate, a ReviewAction) error {
		f.unavailable.Store(true)
		return stateAuth(ctx, tx, p, c, a)
	}
	if got := f.review(2, 2, ReviewExecution); got != ReviewRecorded {
		t.Fatal("negative race scenario changed", got)
	}
	// A remote cancellation/unknown outcome after the read can still leave this
	// HISTORICAL observation. It grants no execution permission and has no runner.
	assertState(t, f, "execution_observed", 3)
	if got := f.review(3, 3, ReviewAuthorization); got != ReviewDenied {
		t.Fatal(got)
	}
	assertState(t, f, "blocked", 4)
}

func TestOfflinePGConcurrentStateRevisionHasOneWinner(t *testing.T) {
	f := stateSetup(t)
	var wg sync.WaitGroup
	out := make(chan ReviewOutcome, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) { defer wg.Done(); out <- f.review(1, n+1, ReviewAuthorization) }(n)
	}
	wg.Wait()
	close(out)
	wins := 0
	for got := range out {
		if got == ReviewRecorded {
			wins++
		} else if got != ReviewConflict {
			t.Fatal(got)
		}
	}
	if wins != 1 {
		t.Fatal("revision admitted multiple state transitions", wins)
	}
	assertState(t, f, "authorization_observed", 2)
}

func TestOfflinePGStateKeyRotationRequiresCurrentLocalTrust(t *testing.T) {
	f := stateSetup(t)
	if got := f.review(1, 1, ReviewAuthorization); got != ReviewRecorded {
		t.Fatal(got)
	}
	if _, err := f.db.Exec("UPDATE aitagent_test_grants SET key_id='rotated'"); err != nil {
		t.Fatal(err)
	}
	if got := f.review(2, 2, ReviewExecution); got != ReviewDenied {
		t.Fatal("old key snapshot authorized review", got)
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	f.p.PublicKey = pub
	f.p.KeyID = "rotated"
	f.proof, _ = Seal(f.i, f.p.KeyID, key, 100, 160)
	if got := f.review(2, 2, ReviewExecution); got != ReviewRecorded {
		t.Fatal(got)
	}
	assertState(t, f, "execution_observed", 3)
}

func TestOfflinePGCancelUnknownAndReviewTimeouts(t *testing.T) {
	f := stateSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	slow := func(ctx context.Context, _ Binding, _ string) (SourceRecord, error) {
		<-ctx.Done()
		return SourceRecord{}, ctx.Err()
	}
	if got := f.machine.Review(ctx, f.proof, f.p, 1, op(1), ReviewAuthorization, f.clock.Load, slow); got != ReviewDenied {
		t.Fatal(got)
	}
	assertState(t, f, "pending_authorization", 1)
	locker, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback()
	if _, err := locker.Exec("SELECT delivery_key FROM aitagent_intake FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	if got := f.review(1, 2, ReviewAuthorization); got != ReviewUnavailable {
		t.Fatal("lock timeout not closed", got)
	}
	locker.Rollback()
	assertState(t, f, "pending_authorization", 1)
	f.machine.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost cancellation acknowledgement")
	}
	if got := f.machine.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(3)); got != ReviewUnknown {
		t.Fatal(got)
	}
	f.machine = NewOfflineStateMachine(true, openOfflineDB(t), stateAuth)
	assertState(t, f, "cancelled", 2)
	if f.inspect(t).Events[1].OperationID != "cancel:"+op(3) {
		t.Fatal("lost cancel could not be reconciled")
	}
	if got := f.machine.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(3)); got != ReviewCancelled {
		t.Fatal(got)
	}
}

func TestOfflinePGClockRewindAfterSourceCannotPassReview(t *testing.T) {
	f := stateSetup(t)
	calls := 0
	clock := func() int64 {
		calls++
		if calls == 2 || calls == 3 {
			return 102
		}
		return 101
	}
	if got := f.machine.Review(context.Background(), f.proof, f.p, 1, op(1), ReviewAuthorization, clock, f.source); got != ReviewDenied {
		t.Fatal("post-source clock rewind passed", got)
	}
	assertState(t, f, "blocked", 2)
}

func TestOfflinePGAuditGapMakesReconciliationUnavailable(t *testing.T) {
	f := stateSetup(t)
	// Superuser fault injection demonstrates corruption detection only, NOT
	// protection against a privileged owner rewriting both row and history.
	if _, err := f.db.Exec("DELETE FROM aitagent_intake_audit"); err != nil {
		t.Fatal(err)
	}
	if _, got := f.machine.Inspect(context.Background(), f.candidate.DeliveryKey, f.p); got != ReviewUnavailable {
		t.Fatal("incomplete history reported as reconciled", got)
	}
	var count int
	if err := f.db.QueryRow("SELECT count(*) FROM aitagent_intake").Scan(&count); err != nil || count != 1 {
		t.Fatal("audit failure erased intake", err, count)
	}
}
