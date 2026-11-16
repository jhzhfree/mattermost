//go:build offlinepg

// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestOfflinePGCancellationSchedulerDemo(t *testing.T) {
	f, outbox, target := cancelQueueSetup(t)
	scheduler := NewOfflineCancellationScheduler(true, outbox, f.p, target, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	lateIntake := false
	for {
		select {
		case e := <-scheduler.Events():
			if e.Outcome == CancelQueued && !lateIntake {
				if phase, attempt := queueSnapshot(t, f); phase != "pending" || attempt != 1 {
					t.Fatal(phase, attempt)
				}
				// A receiver transaction based on the earlier active-source observation
				// commits only after the first automatic cancellation attempt found no row.
				if o := f.accept(); o != IntakePending {
					t.Fatal(o)
				}
				lateIntake = true
			} else if e.Outcome == CancelConfirmed {
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				state := f.view(t)
				phase, attempts := queueSnapshot(t, f)
				if !lateIntake || state.State != "cancelled" || phase != "confirmed" || attempts != 2 {
					t.Fatal(state, phase, attempts)
				}
				report, _ := json.Marshal(map[string]any{"scenario": "late_intake_auto_cancel", "real_services": false, "source": "synthetic", "receiver": "standalone_postgresql", "runtime_role": "aitagent_app", "late_intake_after_first_attempt": lateIntake, "receiver_terminal": state.State, "queue_phase": phase, "durable_attempts": attempts, "scheduler": scheduler.Stats(), "outbound_enabled": false, "execution_enabled": false})
				t.Logf("AITAGENT_DEMO %s", report)
				return
			} else {
				t.Fatal("unexpected scheduler outcome", e.Outcome)
			}
		case <-ctx.Done():
			t.Fatal("automatic cancellation did not confirm within fixture deadline")
		}
	}
}
func TestOfflinePGSchedulerDoesNotBypassRevokedPermission(t *testing.T) {
	f, outbox, target := cancelQueueSetup(t)
	if _, err := f.admin.Exec("UPDATE aitagent_test_grants SET active=false"); err != nil {
		t.Fatal(err)
	}
	scheduler := NewOfflineCancellationScheduler(true, outbox, f.p, target, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	select {
	case e := <-scheduler.Events():
		if e.Outcome != CancelQueueDenied {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("missing denial observation")
	}
	cancel()
	<-done
	if scheduler.Stats().Attempts != 0 {
		t.Fatal("revoked scope dispatched work")
	}
	if phase, a := queueSnapshot(t, f); phase != "pending" || a != 0 {
		t.Fatal(phase, a)
	}
}
