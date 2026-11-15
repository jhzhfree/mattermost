// See LICENSE.txt in the repository root for license information.

package main

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestOutOfScopeAccountsCannotSpendProcessWorkBudget(t *testing.T) {
	p, f, args := fixture()
	owner := *args
	// These represent other real session accounts supplied by the server.
	var outsiders []string
	for i := 0; i < 100; i++ {
		id := model.NewId()
		f.users[id] = &model.User{Id: id}
		outsiders = append(outsiders, id)
	}
	p.SetAPI(nil) // every rejected command must be entirely local
	for _, id := range outsiders {
		a := owner
		a.UserId = id
		if text := invoke(t, p, &a); !strings.Contains(text, notPermittedMessage) {
			t.Fatal(text)
		}
	}
	for i := 0; i < 100; i++ {
		a := owner
		a.ChannelId = model.NewId()
		a.TeamId = model.NewId()
		if text := invoke(t, p, &a); !strings.Contains(text, notPermittedMessage) {
			t.Fatal(text)
		}
	}
	if p.guard.started.Load() != 0 || len(p.guard.events) != 0 || p.guard.prefilterRejected.Load() != 200 || f.writes != 0 {
		t.Fatal("out-of-scope traffic spent shared work or allocated per-caller history")
	}
	p.SetAPI(f)
	if text := invoke(t, p, &owner); !strings.Contains(text, receiptState) {
		t.Fatal("legitimate request was starved", text)
	}
}

func TestEligibleButUnauthorizedAccountsStillShareAvailability(t *testing.T) {
	p, f, args := fixture()
	owner := *args
	var users []string
	for i := 0; i < maxProcessCommands; i++ {
		id := model.NewId()
		users = append(users, id)
		f.users[id] = &model.User{Id: id}
		f.members[id] = true
		f.config.AllowedRequesterIDs += "," + id
	}
	if err := p.OnConfigurationChange(); err != nil {
		t.Fatal(err)
	}
	// Upstream command permission may still be valid for these actors. The
	// target Bot's disabled state is a plugin-specific live check.
	f.users[f.config.BotUserID].DeleteAt = 1
	for _, id := range users {
		a := owner
		a.UserId = id
		if text := invoke(t, p, &a); !strings.Contains(text, notPermittedMessage) {
			t.Fatal(text)
		}
	}
	// This negative result is intentional: local resource protection is not
	// tenant fairness. Current membership/permission checks still require RPCs.
	f.users[f.config.BotUserID].DeleteAt = 0
	if text := invoke(t, p, &owner); !strings.Contains(text, localLimitMessage) {
		t.Fatal("test no longer demonstrates shared availability risk", text)
	}
	if f.writes != 0 || p.guard.started.Load() != maxProcessCommands {
		t.Fatal("denied work changed receipts or exceeded process work cap")
	}
}

func TestStaleSnapshotCanOnlyRejectNotAuthorize(t *testing.T) {
	p, f, args := fixture()
	other := model.NewId()
	f.users[other] = &model.User{Id: other}
	f.members[other] = true
	f.config.AllowedRequesterIDs += "," + other
	a := *args
	a.UserId = other
	if text := invoke(t, p, &a); !strings.Contains(text, notPermittedMessage) {
		t.Fatal("stale snapshot should reject newly added ID until refresh")
	}
	if p.guard.started.Load() != 0 {
		t.Fatal("cache miss issued RPC")
	}
	if err := p.OnConfigurationChange(); err != nil {
		t.Fatal(err)
	}
	if text := invoke(t, p, &a); !strings.Contains(text, receiptState) {
		t.Fatal(text)
	}
	// Revocation without a cache refresh must still be enforced by live checks.
	f.config.AllowedRequesterIDs = args.UserId
	if text := invoke(t, p, &a); !strings.Contains(text, notPermittedMessage) {
		t.Fatal("stale snapshot granted revoked access")
	}
	if len(readLedger(t, f).Receipts) != 1 {
		t.Fatal("revoked account wrote another receipt")
	}
}

func TestConfigurationFailureAndDisabledStateStayLocal(t *testing.T) {
	p, f, args := fixture()
	f.configErr = errors.New("private configuration diagnostic")
	if err := p.OnConfigurationChange(); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("refresh failure not sanitized")
	}
	p.SetAPI(nil)
	if text := invoke(t, p, args); !strings.Contains(text, configUnavailableMessage) {
		t.Fatal(text)
	}
	if p.guard.started.Load() != 0 || p.guard.configRefreshFailed.Load() != 1 {
		t.Fatal("failed refresh did not close intake")
	}
	p.SetAPI(f)
	f.configErr = nil
	f.config.Enabled = false
	if err := p.OnConfigurationChange(); err != nil {
		t.Fatal(err)
	}
	p.SetAPI(nil)
	if text := invoke(t, p, args); !strings.Contains(text, configUnavailableMessage) {
		t.Fatal(text)
	}
	w := httptest.NewRecorder()
	p.ServeMetrics(nil, w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), "aitagent_process_config_prefilter_ready 0") {
		t.Fatal(w.Body.String())
	}
}

type blockedConfigurationAPI struct {
	*fakeAPI
	snapshot configuration
	entered  chan struct{}
	release  chan struct{}
	calls    atomic.Int32
}

func (a *blockedConfigurationAPI) LoadPluginConfiguration(dest any) error {
	a.calls.Add(1)
	*dest.(*configuration) = a.snapshot
	a.entered <- struct{}{}
	<-a.release
	return nil
}

func TestConcurrentRefreshCannotPublishAnOlderConfiguration(t *testing.T) {
	p, f, args := fixture()
	a := &blockedConfigurationAPI{fakeAPI: f, snapshot: f.config, entered: make(chan struct{}, 4), release: make(chan struct{})}
	p.SetAPI(a)
	done := make(chan error, 1)
	release := sync.OnceFunc(func() { close(a.release) })
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); done <- p.OnConfigurationChange() }()
	defer func() { release(); wg.Wait() }()
	select {
	case <-a.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("configuration load did not start")
	}
	if text := invoke(t, p, args); !strings.Contains(text, configUnavailableMessage) {
		t.Fatal("requests must close during refresh")
	}
	f.config.AllowedRequesterIDs = model.NewId() // second configuration event, first load has an old snapshot
	if err := p.OnConfigurationChange(); err == nil {
		t.Fatal("overlapping refresh must fail closed without another RPC")
	}
	if a.calls.Load() != 1 {
		t.Fatal("configuration storm queued uncancellable RPCs")
	}
	release()
	wg.Wait()
	if err := <-done; err == nil {
		t.Fatal("older refresh unexpectedly published")
	}
	if snapshot := p.cachedConfig.Load(); snapshot != nil && snapshot.ready {
		t.Fatal("superseded cache granted eligibility")
	}
	p.SetAPI(f)
	if err := p.OnConfigurationChange(); err != nil {
		t.Fatal(err)
	}
	if text := invoke(t, p, args); !strings.Contains(text, notPermittedMessage) {
		t.Fatal("refresh did not install current rejection rules")
	}
}

func TestOneOverdueRPCStopsNewWorkDespiteThreeFreeSlots(t *testing.T) {
	p, f, args := fixture()
	clock := newWorkClock()
	p.workClock = clock.now
	blocked := &blockingAPI{fakeAPI: f, entered: make(chan struct{}, 8), release: make(chan struct{})}
	p.SetAPI(blocked)
	result := make(chan string, 1)
	release := sync.OnceFunc(func() { close(blocked.release) })
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r, _ := p.ExecuteCommand(nil, args); result <- r.Text }()
	defer func() { release(); wg.Wait() }()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not block")
	}
	clock.advance(commandBudget + time.Nanosecond)
	if p.guard.active.Load() != 1 {
		t.Fatal("expected one occupied slot")
	}
	if text := invoke(t, p, args); !strings.Contains(text, overloadMessage) {
		t.Fatal("one overdue call should stop new work", text)
	}
	if len(blocked.entered) != 0 || p.guard.active.Load() != 1 {
		t.Fatal("overdue rejection started another RPC")
	}
	w := httptest.NewRecorder()
	p.ServeMetrics(nil, w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), "aitagent_process_overdue_commands 1") || !strings.Contains(w.Body.String(), "aitagent_process_inflight 1") {
		t.Fatal(w.Body.String())
	}
	select {
	case <-result:
		t.Fatal("blocking RPC was abandoned")
	default:
	}
	release()
	wg.Wait()
	if text := <-result; !strings.Contains(text, budgetMessage) {
		t.Fatal(text)
	}
	if p.guard.active.Load() != 0 || f.writes != 0 {
		t.Fatal("late read wrote or leaked a slot")
	}
	if text := invoke(t, p, args); !strings.Contains(text, overloadMessage) {
		t.Fatal("missing cooldown")
	}
	clock.advance(processCooldown + time.Second)
	if text := invoke(t, p, args); !strings.Contains(text, receiptState) {
		t.Fatal("capacity failed to recover", text)
	}
}

func TestOversizedConfigurationDoesNotBuildCallerCache(t *testing.T) {
	p, f, args := fixture()
	f.config.AllowedRequesterIDs = strings.Repeat(model.NewId()+",", 1000)
	if err := p.OnConfigurationChange(); err != nil {
		t.Fatal(err)
	}
	if snapshot := p.cachedConfig.Load(); snapshot.ready || len(snapshot.allowed) != 0 {
		t.Fatal("oversized configured allowlist allocated cache")
	}
	p.SetAPI(nil)
	if text := invoke(t, p, args); !strings.Contains(text, configUnavailableMessage) {
		t.Fatal(text)
	}
}

type countingConfigurationAPI struct {
	*fakeAPI
	loads atomic.Int32
}

func (a *countingConfigurationAPI) LoadPluginConfiguration(dest any) error {
	a.loads.Add(1)
	return a.fakeAPI.LoadPluginConfiguration(dest)
}

func TestConcurrentForeignTrafficDoesNotReachConfigurationRPC(t *testing.T) {
	p, f, args := fixture()
	a := &countingConfigurationAPI{fakeAPI: f}
	p.SetAPI(a)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		foreign := *args
		foreign.UserId = model.NewId()
		foreign.ChannelId = model.NewId()
		wg.Add(1)
		go func(arg model.CommandArgs) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if text := invoke(t, p, &arg); !strings.Contains(text, notPermittedMessage) {
					t.Error(text)
				}
			}
		}(foreign)
	}
	if text := invoke(t, p, args); !strings.Contains(text, receiptState) {
		t.Error(text)
	}
	wg.Wait()
	if a.loads.Load() != 1 || p.guard.started.Load() != 1 || p.guard.prefilterRejected.Load() != 640 {
		t.Fatal("foreign traffic reached RPC or spent local capacity")
	}
}
