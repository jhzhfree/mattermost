// See LICENSE.txt in the repository root for license information.

package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

type workTestClock struct{ nanos atomic.Int64 }

func newWorkClock() *workTestClock {
	c := &workTestClock{}
	c.nanos.Store(time.Now().UnixNano())
	return c
}
func (c *workTestClock) now() time.Time          { return time.Unix(0, c.nanos.Load()) }
func (c *workTestClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

func TestLocalReadCancelAndDuplicateLimits(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		limit int
	}{{"status", 6}, {"cancel", 4}, {"request", 4}} {
		t.Run(tc.mode, func(t *testing.T) {
			p, f, args := fixture()
			clock := newWorkClock()
			p.workClock = clock.now
			invoke(t, p, args)
			r := readOnlyReceipt(t, f)
			// A new process has an independent limiter but the same shared ledger.
			p = &Plugin{workClock: clock.now}
			p.SetAPI(f)
			if tc.mode != "request" {
				args.Command = "/aitagent " + tc.mode + " " + r.ID
			}
			for i := 0; i < tc.limit; i++ {
				if text := invoke(t, p, args); strings.Contains(text, localLimitMessage) {
					t.Fatal("early local rejection")
				}
			}
			writes := f.writes
			if text := invoke(t, p, args); !strings.Contains(text, localLimitMessage) {
				t.Fatal(text)
			}
			if f.writes != writes {
				t.Fatal("limited request reached storage")
			}
			clock.advance(time.Minute - time.Nanosecond)
			if text := invoke(t, p, args); !strings.Contains(text, localLimitMessage) {
				t.Fatal("rolling window reset early")
			}
			clock.advance(time.Nanosecond)
			if text := invoke(t, p, args); strings.Contains(text, localLimitMessage) {
				t.Fatal(text)
			}
			if len(readLedger(t, f).Receipts) != 1 {
				t.Fatal("duplicate or cancel consumed a new shared receipt")
			}
		})
	}
}

func TestProcessRateStateIsBoundedAndNotClusterGlobal(t *testing.T) {
	clock := newWorkClock()
	var g processGuard
	for i := 0; i < maxProcessCommands; i++ {
		b, msg := g.enter(model.NewId(), "status", clock.now)
		if b == nil {
			t.Fatal(msg)
		}
		g.leave(b)
	}
	if b, msg := g.enter(model.NewId(), "status", clock.now); b != nil || msg != localLimitMessage {
		t.Fatal("missing process cap")
	}
	if len(g.events) != maxProcessCommands {
		t.Fatal("unbounded limiter history")
	}
	var other processGuard
	b, msg := other.enter(model.NewId(), "status", clock.now)
	if b == nil {
		t.Fatal(msg)
	}
	other.leave(b)
	clock.advance(time.Minute)
	b, msg = g.enter(model.NewId(), "status", clock.now)
	if b == nil {
		t.Fatal(msg)
	}
	g.leave(b)
	if len(g.events) != 1 {
		t.Fatal("old per-user memory not pruned")
	}
	clock.advance(-time.Second)
	if b, _ = g.enter(model.NewId(), "status", clock.now); b != nil {
		t.Fatal("backward local clock accepted")
	}
}

func TestConcurrentLocalLimitsAndCombinedUserCap(t *testing.T) {
	clock := newWorkClock()
	var g processGuard
	user := model.NewId()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _ := g.enter(user, "status", clock.now)
			if b != nil {
				accepted.Add(1)
				g.leave(b)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() > 6 || accepted.Load() < 1 {
		t.Fatal("concurrent status limit violated")
	}
	for accepted.Load() < 6 {
		b, msg := g.enter(user, "status", clock.now)
		if b == nil {
			t.Fatal(msg)
		}
		accepted.Add(1)
		g.leave(b)
	}
	for i := 0; i < 4; i++ {
		b, msg := g.enter(user, "cancel", clock.now)
		if b == nil {
			t.Fatal(msg)
		}
		g.leave(b)
	}
	for i := 0; i < 2; i++ {
		b, msg := g.enter(user, "request", clock.now)
		if b == nil {
			t.Fatal(msg)
		}
		g.leave(b)
	}
	if b, msg := g.enter(user, "request", clock.now); b != nil || msg != localLimitMessage {
		t.Fatal("combined per-user cap missing")
	}
}

func TestBlockedRPCStaysBoundedWithoutPretendCancellation(t *testing.T) {
	p, f, args := fixture()
	clock := newWorkClock()
	p.workClock = clock.now
	blocked := &blockingAPI{fakeAPI: f, entered: make(chan struct{}, 64), release: make(chan struct{})}
	p.SetAPI(blocked)
	results := make(chan string, 4)
	var wg sync.WaitGroup
	release := sync.OnceFunc(func() { close(blocked.release) })
	defer func() { release(); wg.Wait() }()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); response, _ := p.ExecuteCommand(nil, args); results <- response.Text }()
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
	clock.advance(commandBudget + time.Second)
	select {
	case <-results:
		t.Error("uncancellable API call was abandoned")
	default:
	}
	if text := invoke(t, p, args); !strings.Contains(text, overloadMessage) {
		t.Error(text)
	}
	w := httptest.NewRecorder()
	p.ServeMetrics(nil, w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), "aitagent_process_overdue_commands 4") || !strings.Contains(w.Body.String(), "aitagent_process_inflight 4") {
		t.Error(w.Body.String())
	}
	release()
	wg.Wait()
	close(results)
	for text := range results {
		if !strings.Contains(text, budgetMessage) {
			t.Error(text)
		}
	}
	if f.writes != 0 || p.guard.active.Load() != 0 || p.guard.budgetExceeded.Load() != 4 {
		t.Fatal("late read caused write or leaked slot")
	}
	if text := invoke(t, p, args); !strings.Contains(text, overloadMessage) {
		t.Fatal("cooldown did not reject")
	}
	clock.advance(time.Minute)
	if text := invoke(t, p, args); !strings.Contains(text, receiptState) {
		t.Fatal("capacity did not recover", text)
	}
}

type slowWriteAPI struct {
	*fakeAPI
	clock *workTestClock
}

func (f *slowWriteAPI) KVSetWithOptions(k string, v []byte, o model.PluginKVSetOptions) (bool, *model.AppError) {
	ok, e := f.fakeAPI.KVSetWithOptions(k, v, o)
	f.clock.advance(commandBudget)
	return ok, e
}

type slowAuthAPI struct {
	*fakeAPI
	clock *workTestClock
}

func (f *slowAuthAPI) GetUser(id string) (*model.User, *model.AppError) {
	user, e := f.fakeAPI.GetUser(id)
	f.clock.advance(commandBudget)
	return user, e
}

func TestLateWriteRemainsUncertainAndLateAuthStops(t *testing.T) {
	p, f, args := fixture()
	clock := newWorkClock()
	p.workClock = clock.now
	p.SetAPI(&slowWriteAPI{f, clock})
	if text := invoke(t, p, args); !strings.Contains(text, budgetMessage) || !strings.Contains(text, digest(args.TriggerId+"\x00"+args.UserId+"\x00"+args.ChannelId)) {
		t.Fatal(text)
	}
	if len(readLedger(t, f).Receipts) != 1 || p.guard.uncertainWrites.Load() != 1 {
		t.Fatal("late write lost or falsely confirmed")
	}
	p.SetAPI(f)
	clock.advance(time.Minute)
	if text := invoke(t, p, args); !strings.Contains(text, receiptState) {
		t.Fatal(text)
	}
	if len(readLedger(t, f).Receipts) != 1 {
		t.Fatal("late write caused duplicate")
	}
	p, f, args = fixture()
	p.workClock = clock.now
	p.SetAPI(&slowAuthAPI{f, clock})
	if text := invoke(t, p, args); !strings.Contains(text, budgetMessage) {
		t.Fatal(text)
	}
	if len(f.values) != 0 {
		t.Fatal("continued work after late authorization RPC")
	}
}

type conflictAPI struct {
	*fakeAPI
	calls int
}

func (f *conflictAPI) KVSetWithOptions(string, []byte, model.PluginKVSetOptions) (bool, *model.AppError) {
	f.calls++
	return false, nil
}

func TestCASBackoffAndCooperativeBudget(t *testing.T) {
	p, f, args := fixture()
	clock := newWorkClock()
	p.workClock = clock.now
	api := &conflictAPI{fakeAPI: f}
	p.SetAPI(api)
	var delays []time.Duration
	p.retrySleep = func(d time.Duration) { delays = append(delays, d); clock.advance(d) }
	if text := invoke(t, p, args); !strings.Contains(text, "busy") {
		t.Fatal(text)
	}
	if api.calls != maxCASAttempts || len(delays) != maxCASAttempts-1 || p.guard.casExhausted.Load() != 1 {
		t.Fatal("unbounded/missing CAS attempts")
	}
	for _, d := range delays {
		if d < 10*time.Millisecond || d > 110*time.Millisecond {
			t.Fatal("invalid jitter bound", d)
		}
	}
	p.budget = &workBudget{started: clock.now(), deadline: clock.now().Add(5 * time.Millisecond), now: clock.now}
	api.calls = 0
	delays = nil
	if _, e := p.admit(proposal(f.config, args, "short-budget"), f.config, args); e != errBusy {
		t.Fatal(e)
	}
	if api.calls != 1 || len(delays) != 0 {
		t.Fatal("retry ignored remaining work budget")
	}
}

func TestExpiredBudgetCallsNoAPI(t *testing.T) {
	clock := newWorkClock()
	b := &workBudget{deadline: clock.now(), now: clock.now}
	a := &budgetAPI{budget: b} // nil API must not be invoked
	if a.LoadPluginConfiguration(&configuration{}) == nil {
		t.Fatal("configuration called")
	}
	if _, e := a.GetUser("user"); e == nil {
		t.Fatal("user called")
	}
	if _, e := a.GetChannel("channel"); e == nil {
		t.Fatal("channel called")
	}
	if _, e := a.GetChannelMember("channel", "user"); e == nil {
		t.Fatal("member called")
	}
	if a.HasPermissionToChannel("user", "channel", model.PermissionCreatePost) {
		t.Fatal("permission called")
	}
	if _, e := a.KVGet("key"); e == nil {
		t.Fatal("get called")
	}
	if _, e := a.KVListWithOptions(0, 1, model.PluginKVListOptions{}); e == nil {
		t.Fatal("list called")
	}
	if _, e := a.KVSetWithOptions("key", nil, model.PluginKVSetOptions{}); e == nil {
		t.Fatal("write called")
	}
}

func TestMissingReplicaReceiptIsNotProofOfAbsence(t *testing.T) {
	p, f, args := fixture()
	invoke(t, p, args)
	r := readOnlyReceipt(t, f)
	p.SetAPI(&laggingAPI{fakeAPI: f, snapshot: nil})
	args.Command = "/aitagent status " + r.ID
	if text := invoke(t, p, args); !strings.Contains(text, "not proof of absence") || !strings.Contains(text, "Do not automatically") {
		t.Fatal(text)
	}
	if len(readLedger(t, f).Receipts) != 1 {
		t.Fatal("missing replica read changed primary")
	}
}

func TestMetricsAreLocalBoundedAndContainNoIdentity(t *testing.T) {
	p := &Plugin{} // nil API: collection must never call it
	p.guard.rateRejected.Add(3)
	w := httptest.NewRecorder()
	p.ServeMetrics(nil, w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "aitagent_process_rate_rejected_total 3") || len(w.Body.String()) > 2048 {
		t.Fatal(w.Body.String())
	}
	if strings.ContainsAny(w.Body.String(), "{}") || strings.Contains(w.Body.String(), "user_id") {
		t.Fatal("unexpected dynamic labels")
	}
	w = httptest.NewRecorder()
	p.ServeMetrics(nil, w, httptest.NewRequest("POST", "/metrics", nil))
	if w.Code != 405 {
		t.Fatal("unexpected metrics method")
	}
}

// The generated SDK can return false,nil after logging a transport error.
// Simulate a committed write whose acknowledgement was lost in that fashion.
type lostAcknowledgementAPI struct {
	*fakeAPI
	lost bool
}

func (f *lostAcknowledgementAPI) KVSetWithOptions(k string, v []byte, o model.PluginKVSetOptions) (bool, *model.AppError) {
	ok, err := f.fakeAPI.KVSetWithOptions(k, v, o)
	if ok && !f.lost {
		f.lost = true
		return false, nil
	}
	return ok, err
}
func TestLostTransportAcknowledgementCannotDuplicateAdmission(t *testing.T) {
	p, f, args := fixture()
	p.SetAPI(&lostAcknowledgementAPI{fakeAPI: f})
	if text := invoke(t, p, args); !strings.Contains(text, receiptState) {
		t.Fatal(text)
	}
	if len(readLedger(t, f).Receipts) != 1 || p.guard.casNotConfirmed.Load() != 1 {
		t.Fatal("lost acknowledgement was not reconciled with same dispatch")
	}
	if p.guard.storageErrors.Load() != 0 {
		t.Fatal("cannot claim to observe a transport error hidden by the SDK")
	}
}
