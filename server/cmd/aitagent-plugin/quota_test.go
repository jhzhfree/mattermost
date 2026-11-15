// See LICENSE.txt in the repository root for license information.

package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

func proposal(c configuration, args *model.CommandArgs, nonce string) *receipt {
	return &receipt{ID: digest(nonce), Source: "mattermost_plugin_command", OperationMode: "user_requested", ActorUserID: args.UserId,
		OrganizationID: c.OrganizationID, EmployeeID: c.EmployeeID, BotUserID: c.BotUserID, TeamID: args.TeamId, ChannelID: args.ChannelId,
		BindingHash: c.hash(), Goal: "synthetic goal", GoalHash: digest("synthetic goal"), State: receiptState}
}

func TestQuotaScopesAndRollingWindow(t *testing.T) {
	cases := []struct {
		name                       string
		limit                      int
		sameUser, sameOrg, pending bool
	}{
		{"user rate across organizations", maxUserRate, true, false, false},
		{"organization rate", maxOrgRate, false, true, false},
		{"installation rate", maxRate, false, false, false},
		{"user pending across organizations", maxUserPending, true, false, true},
		{"organization pending", maxOrgPending, false, true, true},
		{"installation pending", maxPending, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, f, args := fixture()
			now := model.GetMillis()
			p.clock = func() int64 { return now }
			for i := 0; i <= tc.limit; i++ {
				c := f.config
				if !tc.sameUser {
					args.UserId = model.NewId()
				}
				if !tc.sameOrg {
					c.OrganizationID = model.NewId()
				}
				if tc.pending {
					now += rateWindowMillis + 1
				}
				r, err := p.admit(proposal(c, args, model.NewId()), c, args)
				if i < tc.limit && (err != nil || r == nil) {
					t.Fatalf("early rejection at %d: %v", i, err)
				}
				if i == tc.limit && err != errQuota {
					t.Fatalf("quota not enforced: %v", err)
				}
			}
			if len(readLedger(t, f).Receipts) != tc.limit {
				t.Fatal("rejected admission changed receipt count")
			}
		})
	}
	// Exact rolling boundary, not a wall-clock bucket reset.
	p, f, args := fixture()
	now := model.GetMillis()
	p.clock = func() int64 { return now }
	for i := 0; i < maxUserRate; i++ {
		if _, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != nil {
			t.Fatal(e)
		}
	}
	now += rateWindowMillis - 1
	if _, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != errQuota {
		t.Fatal("window reset early")
	}
	now++
	if _, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != nil {
		t.Fatal(e)
	}
}

func TestCancelReleasesPendingButNotRateOrReplay(t *testing.T) {
	p, f, args := fixture()
	now := model.GetMillis()
	p.clock = func() int64 { return now }
	var first *receipt
	for i := 0; i < maxUserPending; i++ {
		now += rateWindowMillis
		r, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args)
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			first = r
		}
	}
	if _, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != errQuota {
		t.Fatal("pending cap missing")
	}
	r, e := p.lookup(first.ID, f.config, args, true)
	if e != nil || r.State != cancelledState || r.Goal != "" {
		t.Fatalf("cancel: %v", e)
	}
	if _, e = p.lookup(first.ID, f.config, args, true); e != nil {
		t.Fatal(e)
	} // repeated cancellation does not double-release
	if r, e = p.admit(first, f.config, args); e != nil || r.State != cancelledState {
		t.Fatal("replay resurrected cancelled request")
	}
	if _, e = p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != nil {
		t.Fatal("pending slot not released", e)
	}
	if _, e = p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != errQuota {
		t.Fatal("double release")
	}
	p, f, args = fixture()
	p.clock = func() int64 { return now }
	for i := 0; i < maxUserRate; i++ {
		r, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.lookup(r.ID, f.config, args, true); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != errQuota {
		t.Fatal("cancel reset rate")
	}
}

func TestRetainedReceiptCapAndExpiryReclamation(t *testing.T) {
	p, f, args := fixture()
	now := model.GetMillis()
	p.clock = func() int64 { return now }
	for i := 0; i < maxReceipts; i++ {
		now += rateWindowMillis
		r, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args)
		if e != nil {
			t.Fatal(i, e)
		}
		if _, e = p.lookup(r.ID, f.config, args, true); e != nil {
			t.Fatal(e)
		}
	}
	now += rateWindowMillis
	if _, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != errQuota {
		t.Fatal("tombstones allowed unbounded growth")
	}
	now += receiptTTL * 1000
	if _, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != nil {
		t.Fatal(e)
	}
	if len(readLedger(t, f).Receipts) != 1 {
		t.Fatal("expiry did not atomically prune old records")
	}
}

func TestIndependentInstancesShareAtomicQuota(t *testing.T) {
	_, f, args := fixture()
	now := model.GetMillis()
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &Plugin{clock: func() int64 { return now }}
			p.SetAPI(f)
			_, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args)
			if e != nil && e != errQuota && e != errBusy {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	count := len(readLedger(t, f).Receipts)
	if count < 1 || count > maxUserRate {
		t.Fatalf("shared quota violated: %d", count)
	}
	p := &Plugin{clock: func() int64 { return now }}
	p.SetAPI(f)
	for i := count; i < maxUserRate; i++ {
		if _, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != errQuota {
		t.Fatal("shared quota not full")
	}
}

// Reads can lag, but CAS compares exact bytes against the authoritative map.
type laggingAPI struct {
	*fakeAPI
	snapshot []byte
	attempts int
}

func (f *laggingAPI) KVGet(string) ([]byte, *model.AppError) {
	return append([]byte(nil), f.snapshot...), nil
}
func (f *laggingAPI) KVSetWithOptions(k string, v []byte, o model.PluginKVSetOptions) (bool, *model.AppError) {
	f.attempts++
	return f.fakeAPI.KVSetWithOptions(k, v, o)
}

type ambiguousAPI struct {
	*fakeAPI
	commit bool
}

func (f *ambiguousAPI) KVSetWithOptions(k string, v []byte, o model.PluginKVSetOptions) (bool, *model.AppError) {
	if f.commit {
		f.fakeAPI.KVSetWithOptions(k, v, o)
	}
	return false, model.NewAppError("test", "test.timeout", nil, "synthetic-private-diagnostic", 500)
}

func TestReplicaLagAndUncertainCommit(t *testing.T) {
	p, f, args := fixture()
	now := model.GetMillis()
	p.clock = func() int64 { return now }
	r, e := p.admit(proposal(f.config, args, "first"), f.config, args)
	if e != nil {
		t.Fatal(e)
	}
	stale := append([]byte(nil), f.values[ledgerKey]...)
	if _, e = p.lookup(r.ID, f.config, args, true); e != nil {
		t.Fatal(e)
	}
	for _, snapshot := range [][]byte{nil, stale} {
		lag := &laggingAPI{fakeAPI: f, snapshot: snapshot}
		p.SetAPI(lag)
		if _, e = p.admit(proposal(f.config, args, model.NewId()), f.config, args); e != errBusy {
			t.Fatal(e)
		}
		if lag.attempts != maxCASAttempts {
			t.Fatalf("unbounded retries: %d", lag.attempts)
		}
		if readOnlyReceipt(t, f).State != cancelledState {
			t.Fatal("stale replica overwrote cancellation")
		}
	}
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "before commit", true: "after commit"}[commit], func(t *testing.T) {
			p, f, args := fixture()
			r := proposal(f.config, args, "uncertain")
			p.SetAPI(&ambiguousAPI{fakeAPI: f, commit: commit})
			if _, e := p.admit(r, f.config, args); e != errUncertain {
				t.Fatal(e)
			}
			p.SetAPI(f)
			if _, e := p.admit(r, f.config, args); e != nil {
				t.Fatal(e)
			}
			if len(readLedger(t, f).Receipts) != 1 {
				t.Fatal("uncertain write duplicated reservation")
			}
			p.SetAPI(&ambiguousAPI{fakeAPI: f, commit: commit})
			if _, e := p.lookup(r.ID, f.config, args, true); e != errUncertain {
				t.Fatal(e)
			}
			want := receiptState
			if commit {
				want = cancelledState
			}
			if readOnlyReceipt(t, f).State != want {
				t.Fatal("cancellation and release diverged")
			}
			p.SetAPI(f)
			if _, e := p.lookup(r.ID, f.config, args, true); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestStorageGateCorruptionAndClock(t *testing.T) {
	p, f, args := fixture()
	f.config.QuotaStorageReady = false
	if text := invoke(t, p, args); !strings.Contains(text, "not configured") || f.writes != 0 {
		t.Fatal(text)
	}
	f.config.QuotaStorageReady = true
	f.values["request_legacy"] = []byte("old")
	if text := invoke(t, p, args); !strings.Contains(text, "Legacy") || f.writes != 0 {
		t.Fatal(text)
	}
	delete(f.values, "request_legacy")
	for _, raw := range [][]byte{[]byte("invalid"), []byte(`{"version":99}`), []byte(strings.Repeat("x", maxLedgerBytes+1))} {
		f.values[ledgerKey] = raw
		p = &Plugin{}
		p.SetAPI(f) // independent process for each corrupt-state case
		if text := invoke(t, p, args); !strings.Contains(text, "unavailable") || f.writes != 0 {
			t.Fatal("unsafe recovery", text)
		}
	}
	delete(f.values, ledgerKey)
	now := model.GetMillis()
	p.clock = func() int64 { return now }
	invoke(t, p, args)
	now--
	if text := invoke(t, p, args); !strings.Contains(text, "clock moved backwards") {
		t.Fatal(text)
	}
}

type blockingAPI struct {
	*fakeAPI
	entered chan struct{}
	release chan struct{}
}

func (f *blockingAPI) KVGet(k string) ([]byte, *model.AppError) {
	f.entered <- struct{}{}
	<-f.release
	return f.fakeAPI.KVGet(k)
}
func TestPerProcessInFlightBound(t *testing.T) {
	p, f, args := fixture()
	blocked := &blockingAPI{fakeAPI: f, entered: make(chan struct{}, 64), release: make(chan struct{})}
	p.SetAPI(blocked)
	var wg sync.WaitGroup
	release := sync.OnceFunc(func() { close(blocked.release) })
	defer func() { release(); wg.Wait() }()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); invoke(t, p, args) }()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-blocked.entered:
		case <-time.After(5 * time.Second):
			release()
			wg.Wait()
			t.Fatal("did not enter storage")
		}
	}
	if text := invoke(t, p, args); !strings.Contains(text, "overloaded") {
		t.Error(text)
	}
	release()
	wg.Wait()
	if p.guard.active.Load() != 0 {
		t.Fatal("in-flight permit leak")
	}
}

func TestExpiredReadPrunesWithoutNewRequest(t *testing.T) {
	p, f, args := fixture()
	now := model.GetMillis()
	p.clock = func() int64 { return now }
	r, e := p.admit(proposal(f.config, args, "expiry"), f.config, args)
	if e != nil {
		t.Fatal(e)
	}
	now = r.ExpiresAt
	if _, e = p.lookup(r.ID, f.config, args, false); e != errMissing {
		t.Fatal(e)
	}
	if len(readLedger(t, f).Receipts) != 0 {
		t.Fatal("expired goal remains after successful cleanup")
	}
	// Stable revision means old snapshots cannot match again after reclamation.
	var state ledger
	if json.Unmarshal(f.values[ledgerKey], &state) != nil || state.Revision < 2 {
		t.Fatal("missing monotonic revision")
	}
}

func TestCancelReauthorizesAndExpiryFailureKeepsReservation(t *testing.T) {
	p, f, args := fixture()
	now := model.GetMillis()
	p.clock = func() int64 { return now }
	invoke(t, p, args)
	r := readOnlyReceipt(t, f)
	args.Command = "/aitagent cancel " + r.ID
	f.members[args.UserId] = false
	if text := invoke(t, p, args); !strings.Contains(text, "not permitted") {
		t.Fatal(text)
	}
	if readOnlyReceipt(t, f).State != receiptState {
		t.Fatal("revoked user released quota")
	}
	f.members[args.UserId] = true
	now = r.ExpiresAt
	f.writeErr = model.NewAppError("test", "test.write", nil, "synthetic", 500)
	if _, err := p.lookup(r.ID, f.config, args, false); err != errUncertain {
		t.Fatal(err)
	}
	if len(readLedger(t, f).Receipts) != 1 {
		t.Fatal("failed cleanup changed authoritative state")
	}
	f.writeErr = nil
	if _, err := p.admit(proposal(f.config, args, "after-expiry"), f.config, args); err != nil {
		t.Fatal(err)
	}
	if got := readOnlyReceipt(t, f); got.ID == r.ID {
		t.Fatal("expired receipt not reclaimed on successful CAS")
	}
}

func TestCancellationRacingAdmissionCannotOverbook(t *testing.T) {
	p, f, args := fixture()
	now := model.GetMillis()
	p.clock = func() int64 { return now }
	var first *receipt
	for i := 0; i < maxUserPending; i++ {
		now += rateWindowMillis
		r, err := p.admit(proposal(f.config, args, model.NewId()), f.config, args)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = r
		}
	}
	now += rateWindowMillis
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			node := &Plugin{clock: func() int64 { return now }}
			node.SetAPI(f)
			_, err := node.admit(proposal(f.config, args, model.NewId()), f.config, args)
			if err != nil && err != errQuota && err != errBusy {
				t.Error(err)
			}
		}()
	}
	if _, err := p.lookup(first.ID, f.config, args, true); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	state := readLedger(t, f)
	pending := 0
	for _, r := range state.Receipts {
		if r.State == receiptState {
			pending++
		}
	}
	if pending > maxUserPending || len(state.Receipts) > maxUserPending+1 || state.Receipts[first.ID].State != cancelledState {
		t.Fatal("racing cancellation overbooked or lost the tombstone")
	}
}
