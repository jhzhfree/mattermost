// See LICENSE.txt in the repository root for license information.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/plugin"
)

func supportReport(t *testing.T, p *Plugin) (map[string]any, []byte) {
	t.Helper()
	files, err := p.GenerateSupportData(&plugin.Context{SessionId: "SECRET_SESSION", UserAgent: "PRIVATE_AGENT"})
	if err != nil || len(files) != 1 || files[0].Filename != "aitagent-local-diagnostics.json" || len(files[0].Body) > 8192 {
		t.Fatal("unbounded or missing report", err)
	}
	var report map[string]any
	decoder := json.NewDecoder(bytes.NewReader(files[0].Body))
	decoder.UseNumber()
	if err := decoder.Decode(&report); err != nil {
		t.Fatal(err)
	}
	return report, files[0].Body
}
func TestSupportSnapshotNoAPIOrIdentityAndMatchesMetrics(t *testing.T) {
	p := &Plugin{} // nil API: diagnostics must not try to query blocked dependencies.
	p.cachedConfig.Store(&configSnapshot{ready: true, teamID: "PRIVATE_TEAM", channelID: "PRIVATE_CHANNEL", allowed: map[string]struct{}{"PRIVATE_USER": {}}})
	p.guard.events = []commandEvent{{user: "PRIVATE_USER", mode: "PRIVATE_GOAL"}}
	p.guard.uncertainWrites.Store(9007199254740993) // preserve uint64, not float64.
	report, raw := supportReport(t, p)
	for _, secret := range []string{"PRIVATE_", "SECRET_SESSION"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("identity leaked")
		}
	}
	if len(report) != 7 || report["authoritative_receipt_or_permission_evidence"] != false || report["durable_audit"] != false {
		t.Fatal("incorrect evidence claim")
	}
	metrics := report["metrics"].(map[string]any)
	allowed := []string{"config_prefilter_ready", "config_refresh_inflight", "inflight", "oldest_work_seconds", "overdue_commands", "cooldown_seconds", "commands_started_total", "rate_rejected_total", "overload_rejected_total", "budget_exceeded_total", "cas_not_confirmed_total", "cas_exhausted_total", "observed_storage_errors_total", "uncertain_writes_total", "prefilter_rejected_total", "config_refresh_failed_total"}
	if len(metrics) != len(allowed) {
		t.Fatal("unexpected dynamic fields")
	}
	recorder := httptest.NewRecorder()
	p.ServeMetrics(nil, recorder, httptest.NewRequest("GET", "/metrics", nil))
	for _, name := range allowed {
		full := "aitagent_process_" + name
		value, ok := metrics[full]
		if !ok || !strings.Contains(recorder.Body.String(), fmt.Sprint(full, " ", value, "\n")) {
			t.Fatal("support/metrics divergence", name)
		}
	}
	if !strings.Contains(string(raw), "historical_unknown_commit_reconcile_original_receipt") {
		t.Fatal("missing recovery hint")
	}
}
func TestSupportOverdueAndRefreshDoesNotResetWork(t *testing.T) {
	now := time.Now()
	p := &Plugin{workClock: func() time.Time { return now }}
	p.refreshing.Store(true)
	p.guard.running = map[*workBudget]struct{}{}
	for range 4 {
		p.guard.running[&workBudget{started: now.Add(-5 * time.Second), deadline: now.Add(-3 * time.Second)}] = struct{}{}
	}
	p.guard.configRefreshFailed.Add(1)
	_, raw := supportReport(t, p)
	for _, hint := range []string{"configuration_disabled_or_unready", "configuration_refresh_in_progress", "overdue_work_requires_dependency_review", "historical_configuration_refresh_failures"} {
		if !strings.Contains(string(raw), hint) {
			t.Fatal("missing hint", hint)
		}
	}
	if len(p.guard.running) != 4 || !p.refreshing.Load() {
		t.Fatal("diagnostics mutated recovery state")
	}
	// A fresh process's zero counters cannot imply old unknown writes were resolved.
	report, _ := supportReport(t, &Plugin{})
	if report["authoritative_receipt_or_permission_evidence"] != false || report["durable_audit"] != false {
		t.Fatal("restart manufactured evidence")
	}
}
func TestSupportSnapshotConcurrentCollection(t *testing.T) {
	p := &Plugin{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				p.guard.storageErrors.Add(1)
				files, err := p.GenerateSupportData(nil)
				if err != nil || len(files) != 1 {
					t.Error("concurrent collection failed")
				}
			}
		}()
	}
	wg.Wait()
}
func TestSupportPacketExplicitSelectionManifest(t *testing.T) {
	raw, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Props map[string]string `json:"props"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Props["support_packet"] == "" {
		t.Fatal("missing standard support packet opt-in")
	}
}
