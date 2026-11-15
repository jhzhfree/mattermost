// See LICENSE.txt in the repository root for license information.

package main

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

const (
	commandBudget      = 2 * time.Second
	processCooldown    = 30 * time.Second
	maxProcessCommands = 60
	maxUserCommands    = 12
)

const budgetMessage = "The local command work budget was exceeded. An issued storage operation may have committed; no execution or external delivery was attempted. Reconcile the original receipt when storage recovers. A missing receipt is not proof that no receipt was stored; do not automatically submit a new request."
const localLimitMessage = "This plugin process is rate-limited; retry later. Request, status, and cancellation attempts all consume local limits. These are not cluster-wide request limits."
const overloadMessage = "This plugin process is overloaded, has an overdue API call, or is cooling down. Retry later; do not automatically resubmit an uncertain request."

type workBudget struct {
	started  time.Time
	deadline time.Time
	now      func() time.Time
}

func (b *workBudget) expired() bool { return !b.now().Before(b.deadline) }

type commandEvent struct {
	at         time.Time
	user, mode string
}
type processGuard struct {
	mu              sync.Mutex
	events          []commandEvent           // bounded by maxProcessCommands; no unbounded per-user map
	running         map[*workBudget]struct{} // at most four synchronous calls; never abandoned
	cooldownUntil   time.Time
	lastNow         time.Time
	active          atomic.Int32
	started         atomic.Uint64
	rateRejected    atomic.Uint64
	overloaded      atomic.Uint64
	budgetExceeded  atomic.Uint64
	casNotConfirmed atomic.Uint64
	casExhausted    atomic.Uint64
	storageErrors   atomic.Uint64
	uncertainWrites atomic.Uint64
}

func (p *Plugin) observation() *processGuard {
	if p.sharedGuard != nil {
		return p.sharedGuard
	}
	return &p.guard
}
func (p *Plugin) wallNow() time.Time {
	if p.workClock != nil {
		return p.workClock()
	}
	return time.Now()
}

func (g *processGuard) enter(user, mode string, now func() time.Time) (*workBudget, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	t := now()
	if t.Before(g.lastNow) || t.Before(g.cooldownUntil) || len(g.running) >= 4 {
		g.overloaded.Add(1)
		return nil, overloadMessage
	}
	for b := range g.running {
		if !t.Before(b.deadline) {
			g.overloaded.Add(1)
			return nil, overloadMessage
		}
	}
	g.lastNow = t
	kept := g.events[:0]
	userCount, modeCount := 0, 0
	for _, event := range g.events {
		if event.at.After(t.Add(-time.Minute)) {
			kept = append(kept, event)
			if event.user == user {
				userCount++
				if event.mode == mode {
					modeCount++
				}
			}
		}
	}
	// Clear removed user IDs from the backing array as well as the slice.
	clear(g.events[len(kept):])
	g.events = kept
	modeLimit := 4
	if mode == "status" {
		modeLimit = 6
	}
	if len(g.events) >= maxProcessCommands || userCount >= maxUserCommands || modeCount >= modeLimit {
		g.rateRejected.Add(1)
		return nil, localLimitMessage
	}
	g.events = append(g.events, commandEvent{t, user, mode})
	b := &workBudget{started: t, deadline: t.Add(commandBudget), now: now}
	if g.running == nil {
		g.running = make(map[*workBudget]struct{})
	}
	g.running[b] = struct{}{}
	g.active.Add(1)
	g.started.Add(1)
	return b, ""
}
func (g *processGuard) leave(b *workBudget) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.running, b)
	g.active.Add(-1)
	if b.expired() {
		g.budgetExceeded.Add(1)
		g.cooldownUntil = b.now().Add(processCooldown)
	}
}

func (p *Plugin) checkBudget() bool { return p.budget != nil && p.budget.expired() }

func (p *Plugin) waitForRetry(attempt int) bool {
	delay := time.Duration(10<<attempt) * time.Millisecond
	if delay > 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	delay += time.Duration(rand.IntN(11)) * time.Millisecond
	if p.budget != nil && !p.wallNow().Add(delay).Before(p.budget.deadline) {
		return false
	}
	if p.retrySleep != nil {
		p.retrySleep(delay)
	} else {
		time.Sleep(delay)
	}
	return !p.checkBudget()
}

// No timer goroutine races an uncancellable RPC. Check before issuing it and
// after it returns; a late write remains uncertain, not safely rolled back.
func guardedCall[T any](b *workBudget, call func() (T, *model.AppError)) (T, *model.AppError) {
	var zero T
	if b.expired() {
		return zero, workError()
	}
	value, err := call()
	if b.expired() {
		return zero, workError()
	}
	return value, err
}
func workError() *model.AppError {
	return model.NewAppError("aitagent", "aitagent.work_budget", nil, "Local work budget exceeded", http.StatusServiceUnavailable)
}

type budgetAPI struct {
	plugin.API
	budget *workBudget
}

func (a *budgetAPI) LoadPluginConfiguration(dest any) error {
	if a.budget.expired() {
		return workError()
	}
	err := a.API.LoadPluginConfiguration(dest)
	if a.budget.expired() {
		return workError()
	}
	return err
}
func (a *budgetAPI) GetUser(id string) (*model.User, *model.AppError) {
	return guardedCall(a.budget, func() (*model.User, *model.AppError) { return a.API.GetUser(id) })
}
func (a *budgetAPI) GetChannel(id string) (*model.Channel, *model.AppError) {
	return guardedCall(a.budget, func() (*model.Channel, *model.AppError) { return a.API.GetChannel(id) })
}
func (a *budgetAPI) GetChannelMember(channel, user string) (*model.ChannelMember, *model.AppError) {
	return guardedCall(a.budget, func() (*model.ChannelMember, *model.AppError) { return a.API.GetChannelMember(channel, user) })
}
func (a *budgetAPI) HasPermissionToChannel(user, channel string, permission *model.Permission) bool {
	ok, err := guardedCall(a.budget, func() (bool, *model.AppError) { return a.API.HasPermissionToChannel(user, channel, permission), nil })
	return err == nil && ok
}
func (a *budgetAPI) KVGet(key string) ([]byte, *model.AppError) {
	return guardedCall(a.budget, func() ([]byte, *model.AppError) { return a.API.KVGet(key) })
}
func (a *budgetAPI) KVListWithOptions(page, perPage int, options model.PluginKVListOptions) ([]string, *model.AppError) {
	return guardedCall(a.budget, func() ([]string, *model.AppError) { return a.API.KVListWithOptions(page, perPage, options) })
}
func (a *budgetAPI) KVSetWithOptions(key string, value []byte, options model.PluginKVSetOptions) (bool, *model.AppError) {
	return guardedCall(a.budget, func() (bool, *model.AppError) { return a.API.KVSetWithOptions(key, value, options) })
}

// ServeMetrics uses only bounded local state/atomics; never another plugin API
// call, ledger read, log write, goal text, user ID, or organization label.
func (p *Plugin) ServeMetrics(_ *plugin.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	g := p.observation()
	now := p.wallNow()
	oldest := float64(0)
	overdue := 0
	cooldown := float64(0)
	g.mu.Lock()
	for b := range g.running {
		age := now.Sub(b.started).Seconds()
		if age > oldest {
			oldest = age
		}
		if !now.Before(b.deadline) {
			overdue++
		}
	}
	if now.Before(g.cooldownUntil) {
		cooldown = g.cooldownUntil.Sub(now).Seconds()
	}
	active := len(g.running)
	g.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "aitagent_process_inflight %d\naitagent_process_oldest_work_seconds %g\naitagent_process_overdue_commands %d\naitagent_process_cooldown_seconds %g\n", active, oldest, overdue, cooldown)
	for _, metric := range []struct {
		name  string
		value uint64
	}{
		{"commands_started", g.started.Load()}, {"rate_rejected", g.rateRejected.Load()}, {"overload_rejected", g.overloaded.Load()},
		{"budget_exceeded", g.budgetExceeded.Load()}, {"cas_not_confirmed", g.casNotConfirmed.Load()}, {"cas_exhausted", g.casExhausted.Load()},
		{"observed_storage_errors", g.storageErrors.Load()}, {"uncertain_writes", g.uncertainWrites.Load()},
	} {
		fmt.Fprintf(w, "aitagent_process_%s_total %d\n", metric.name, metric.value)
	}
}
