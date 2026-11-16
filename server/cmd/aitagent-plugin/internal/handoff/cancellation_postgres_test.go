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

func cancelEvidence(f *lifecycleFixture) CancellationReader {
	return func(context.Context) (CancellationEvidence, error) {
		return CancellationEvidence{DeliveryKey: f.candidate.DeliveryKey, Fingerprint: f.candidate.Fingerprint, Version: 50, Cancelled: true}, nil
	}
}
func TestOfflinePGCancellationPropagationUnknownAndRetry(t *testing.T) {
	f := lifecycleSetup(t)
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	f.state.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost cancellation ack")
	}
	if o := f.state.PropagateCancellation(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1), cancelEvidence(f)); o != ReviewUnknown {
		t.Fatal(o)
	}
	s := f.view(t)
	if s.State != "cancelled" || s.Events[1].OperationID != "source-cancel:"+op(1) {
		t.Fatal(s)
	}
	f.state = NewOfflineStateMachine(true, f.app, roleAuth)
	if o := f.state.PropagateCancellation(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1), cancelEvidence(f)); o != ReviewCancelled {
		t.Fatal(o)
	}
	if len(f.view(t).Events) != 2 {
		t.Fatal("duplicate event")
	}
}
func TestOfflinePGCancellationPropagationIdentityAndRevocation(t *testing.T) {
	f := lifecycleSetup(t)
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	for _, mutate := range []func(*CancellationEvidence){func(e *CancellationEvidence) { e.Fingerprint = op(99) }, func(e *CancellationEvidence) { e.DeliveryKey = op(99) }, func(e *CancellationEvidence) { e.Cancelled = false }, func(e *CancellationEvidence) { e.Version = 1 }} {
		read := func(ctx context.Context) (CancellationEvidence, error) {
			e, err := cancelEvidence(f)(ctx)
			mutate(&e)
			return e, err
		}
		if o := f.state.PropagateCancellation(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1), read); o != ReviewDenied {
			t.Fatal(o)
		}
	}
	read := func(ctx context.Context) (CancellationEvidence, error) {
		e, err := cancelEvidence(f)(ctx)
		if _, x := f.admin.Exec("UPDATE aitagent_test_grants SET active=false"); x != nil {
			t.Fatal(x)
		}
		return e, err
	}
	if o := f.state.PropagateCancellation(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1), read); o != ReviewDenied {
		t.Fatal(o)
	}
	var state string
	if err := f.admin.QueryRow("SELECT state FROM aitagent_intake").Scan(&state); err != nil || state != "pending_authorization" {
		t.Fatal(state, err)
	}
}
func TestOfflinePGCancellationPropagationConcurrentAndRollback(t *testing.T) {
	f := lifecycleSetup(t)
	if o := f.accept(); o != IntakePending {
		t.Fatal(o)
	}
	if _, err := f.admin.Exec("ALTER TABLE aitagent_intake_audit ADD CONSTRAINT cancel_fault CHECK(action<>'cancelled') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if o := f.state.PropagateCancellation(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1), cancelEvidence(f)); o != ReviewUnavailable {
		t.Fatal(o)
	}
	if f.view(t).Revision != 1 {
		t.Fatal("partial cancel")
	}
	if _, err := f.admin.Exec("ALTER TABLE aitagent_intake_audit DROP CONSTRAINT cancel_fault"); err != nil {
		t.Fatal(err)
	}
	results := make(chan ReviewOutcome, 2)
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.state.PropagateCancellation(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1), cancelEvidence(f))
		}()
	}
	wg.Wait()
	close(results)
	for o := range results {
		if o != ReviewCancelled {
			t.Fatal(o)
		}
	}
	if len(f.view(t).Events) != 2 {
		t.Fatal("duplicate cancel")
	}
}

func TestOfflinePGCancellationBeforeIntakeCommitRequiresRetry(t *testing.T) {
	f := lifecycleSetup(t)
	observed := make(chan struct{})
	resume := make(chan struct{})
	result := make(chan IntakeOutcome, 1)
	auth := f.inbox.authorize
	f.inbox.authorize = func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
		if err := auth(ctx, tx, p, c); err != nil {
			return err
		}
		close(observed)
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	go func() { result <- f.accept() }()
	<-observed // source was observed active; receiver transaction has not inserted
	got := f.state.PropagateCancellation(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1), cancelEvidence(f))
	close(resume)
	if got != ReviewUnavailable {
		t.Fatal(got)
	}
	if o := <-result; o != IntakePending {
		t.Fatal(o)
	}
	// No absent-row tombstone or distributed transaction was invented. A retry is
	// necessary after the in-flight intake commits; execution stays disabled.
	if o := f.state.PropagateCancellation(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1), cancelEvidence(f)); o != ReviewCancelled {
		t.Fatal(o)
	}
}
