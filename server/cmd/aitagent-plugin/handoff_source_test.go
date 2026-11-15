// See LICENSE.txt in the repository root for license information.

package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

func TestGuardedSourceUsesConfirmedKVAndRedactsGoal(t *testing.T) {
	p, f, args := fixture()
	invoke(t, p, args)
	var r *receipt
	for _, item := range readLedger(t, f).Receipts {
		r = item
	}
	i, err := handoffDraft(r, f.config, args, "test-mm", "test-octop", p.nowMillis())
	if err != nil {
		t.Fatal(err)
	}
	reader := p.handoffSourceReader(sourceBinding{Enabled: true, Binding: i.Binding})
	observation, err := reader(context.Background(), i.Binding, r.ID)
	if err != nil || observation.Commitment != handoff.IntentCommitment(i) || f.writes != 2 || observation.Version != readLedger(t, f).Revision {
		t.Fatal("source was not primary-confirmed", err)
	}
	encoded, _ := json.Marshal(observation)
	if strings.Contains(string(encoded), r.Goal) || strings.Contains(strings.ToLower(string(encoded)), "goal") {
		t.Fatal("source returned goal")
	}
	wrong := i.Binding
	wrong.User = f.config.BotUserID
	writes := f.writes
	if _, err := reader(context.Background(), wrong, r.ID); err != handoff.ErrRejected || f.writes != writes {
		t.Fatal("caller identity overrode pinned binding")
	}
	f.permission = false
	if _, err := reader(context.Background(), i.Binding, r.ID); err != handoff.ErrRejected {
		t.Fatal("source permission revoke ignored")
	}
	f.permission = true
	cancelArgs := *args
	cancelArgs.Command = "/aitagent cancel " + r.ID
	invoke(t, p, &cancelArgs)
	if _, err := reader(context.Background(), i.Binding, r.ID); err != handoff.ErrRejected {
		t.Fatal("cancelled source accepted")
	}
}

func TestSourceDisabledDoesNotTouchAPI(t *testing.T) {
	p := &Plugin{}
	reader := p.handoffSourceReader(sourceBinding{})
	if _, err := reader(context.Background(), handoff.Binding{}, digest("receipt")); err != handoff.ErrRejected {
		t.Fatal("default source open")
	}
}

func TestSourceRejectsStaleReplicaAndUnknownConfirmation(t *testing.T) {
	for _, mode := range []string{"stale", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			p, f, args := fixture()
			invoke(t, p, args)
			var r *receipt
			for _, item := range readLedger(t, f).Receipts {
				r = item
			}
			i, err := handoffDraft(r, f.config, args, "test-mm", "test-octop", p.nowMillis())
			if err != nil {
				t.Fatal(err)
			}
			reader := p.handoffSourceReader(sourceBinding{Enabled: true, Binding: i.Binding})
			if mode == "stale" {
				snapshot := append([]byte(nil), f.values[ledgerKey]...)
				args.Command = "/aitagent cancel " + r.ID
				invoke(t, p, args)
				lag := &laggingAPI{fakeAPI: f, snapshot: snapshot}
				p.SetAPI(lag)
				p.retrySleep = func(time.Duration) {}
				if result, err := reader(context.Background(), i.Binding, r.ID); err != handoff.ErrRejected || result.Commitment != "" || lag.attempts != maxCASAttempts {
					t.Fatal("stale replica escaped confirmation", err)
				}
			} else {
				p.SetAPI(&ambiguousAPI{fakeAPI: f, commit: true})
				if result, err := reader(context.Background(), i.Binding, r.ID); err != handoff.ErrRejected || result.Commitment != "" {
					t.Fatal("unknown confirmation returned evidence", err)
				}
			}
		})
	}
}

func TestSourceCancelledContextDoesNotAbandonBlockedRPC(t *testing.T) {
	p, f, args := fixture()
	invoke(t, p, args)
	var r *receipt
	for _, item := range readLedger(t, f).Receipts {
		r = item
	}
	i, err := handoffDraft(r, f.config, args, "test-mm", "test-octop", p.nowMillis())
	if err != nil {
		t.Fatal(err)
	}
	block := &blockingAPI{fakeAPI: f, entered: make(chan struct{}, 1), release: make(chan struct{})}
	p.SetAPI(block)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := p.handoffSourceReader(sourceBinding{Enabled: true, Binding: i.Binding})
	done := make(chan error, 1)
	release := sync.OnceFunc(func() { close(block.release) })
	joined := false
	defer func() {
		release()
		if !joined {
			<-done
		}
	}()
	go func() { _, err := reader(ctx, i.Binding, r.ID); done <- err }()
	<-block.entered
	cancel()
	select {
	case <-done:
		joined = true
		t.Fatal("claimed to cancel synchronous plugin RPC")
	default:
	}
	if p.guard.active.Load() != 1 {
		t.Fatal("blocked source released its slot")
	}
	release()
	err = <-done
	joined = true
	if err != handoff.ErrRejected || p.guard.active.Load() != 0 || f.writes != 1 {
		t.Fatal("late source result accepted, additional write, or slot leaked", err)
	}
}
