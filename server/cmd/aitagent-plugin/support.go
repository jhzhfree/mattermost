// See LICENSE.txt in the repository root for license information.
package main

import (
	"encoding/json"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

// GenerateSupportData uses the standard, administrator-selected support-packet
// hook. It never calls an API, reads KV/config/credentials, logs, or sends alerts.
// Opt-in is enforced by Mattermost through manifest.props.support_packet; this
// hook has no independent HTTP route or authentication bypass.
func (p *Plugin) GenerateSupportData(_ *plugin.Context) ([]*model.FileData, error) {
	metrics := map[string]any{}
	for _, m := range p.localMetrics() {
		metrics[m.name] = m.value
	}
	hints := []string{}
	if metrics["aitagent_process_config_prefilter_ready"] == 0 {
		hints = append(hints, "configuration_disabled_or_unready")
	}
	if metrics["aitagent_process_config_refresh_inflight"] == 1 {
		hints = append(hints, "configuration_refresh_in_progress")
	}
	if metrics["aitagent_process_overdue_commands"].(int) > 0 {
		hints = append(hints, "overdue_work_requires_dependency_review")
	}
	for _, item := range []struct{ metric, hint string }{
		{"uncertain_writes", "historical_unknown_commit_reconcile_original_receipt"},
		{"observed_storage_errors", "historical_storage_errors"},
		{"cas_exhausted", "historical_cas_confirmation_failures"},
		{"rate_rejected", "historical_process_rate_limit"},
		{"config_refresh_failed", "historical_configuration_refresh_failures"},
	} {
		if metrics["aitagent_process_"+item.metric+"_total"].(uint64) > 0 {
			hints = append(hints, item.hint)
		}
	}
	report := struct {
		SchemaVersion int            `json:"schema_version"`
		Scope         string         `json:"scope"`
		Authoritative bool           `json:"authoritative_receipt_or_permission_evidence"`
		Durable       bool           `json:"durable_audit"`
		Metrics       map[string]any `json:"metrics"`
		Hints         []string       `json:"recovery_hints"`
		Runbook       string         `json:"runbook"`
	}{1, "single_plugin_process_approximate_snapshot", false, false, metrics, hints, "OPERATIONS.md"}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	return []*model.FileData{{Filename: "aitagent-local-diagnostics.json", Body: append(body, '\n')}}, nil
}
