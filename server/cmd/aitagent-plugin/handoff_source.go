// See LICENSE.txt in the repository root for license information.

package main

import (
	"context"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

// sourceBinding is deployment-owned, never decoded from a request or header.
// There is no runtime constructor/setting/HTTP route: only the offline integration
// harness installs a binding. A future authenticated broker must pin this mapping
// to its service identity; Mattermost user headers cannot establish that identity.
type sourceBinding struct {
	Enabled bool
	Binding handoff.Binding
}

// handoffSourceReader really calls the plugin's guarded authorization and
// primary-confirming ledger CAS. It returns a commitment, never goal/proof data.
// This is not a claim/lease: cancellation can race after the confirmed read.
func (p *Plugin) handoffSourceReader(trusted sourceBinding) handoff.SourceReader {
	return func(ctx context.Context, requested handoff.Binding, id string) (handoff.SourceRecord, error) {
		deny := func() (handoff.SourceRecord, error) { return handoff.SourceRecord{}, handoff.ErrRejected }
		if ctx == nil || ctx.Err() != nil || !trusted.Enabled || requested != trusted.Binding || !validReceiptID(id) {
			return deny()
		}
		b := trusted.Binding
		args := &model.CommandArgs{UserId: b.User, TeamId: b.Team, ChannelId: b.Channel}
		if p.prefilter(args) != "" {
			return deny()
		}
		guard := p.observation()
		budget, _ := guard.enter(b.User, "status", p.wallNow)
		if budget == nil {
			return deny()
		}
		defer guard.leave(budget)
		// Keep the shared guard's budget immutable. The private API budget can
		// stop subsequent RPCs after cancellation without releasing the slot of
		// an already issued synchronous RPC or racing metrics/other admissions.
		apiBudget := *budget
		apiBudget.cancelled = func() bool { return ctx.Err() != nil }
		worker := &Plugin{clock: p.clock, workClock: p.workClock, retrySleep: p.retrySleep, budget: &apiBudget, sharedGuard: guard}
		worker.SetAPI(&budgetAPI{API: p.API, budget: &apiBudget})
		var config configuration
		if worker.API.LoadPluginConfiguration(&config) != nil || !config.valid() ||
			config.OrganizationID != b.Organization || config.EmployeeID != b.Employee || config.BotUserID != b.Bot ||
			!worker.authorize(config, args) {
			return deny()
		}
		r, err := worker.lookup(id, config, args, false)
		if err != nil {
			return deny()
		}
		var latest configuration
		if worker.API.LoadPluginConfiguration(&latest) != nil || latest.hash() != config.hash() ||
			!worker.authorize(latest, args) || ctx.Err() != nil || budget.expired() {
			return deny()
		}
		intent, err := handoffDraft(r, latest, args, b.Instance, b.Audience, worker.nowMillis())
		if err != nil {
			return deny()
		}
		return handoff.SourceRecord{Commitment: handoff.IntentCommitment(intent), State: r.State, ReceiptExpiresAt: r.ExpiresAt}, nil
	}
}
