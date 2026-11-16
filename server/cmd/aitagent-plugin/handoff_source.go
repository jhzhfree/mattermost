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
		r, c, v, err := p.withHandoffReceipt(ctx, trusted, requested, id, nil)
		if err != nil {
			return handoff.SourceRecord{}, err
		}
		b := trusted.Binding
		args := &model.CommandArgs{UserId: b.User, TeamId: b.Team, ChannelId: b.Channel}
		intent, err := handoffDraft(r, c, args, b.Instance, b.Audience, p.nowMillis())
		if err != nil {
			return handoff.SourceRecord{}, err
		}
		record := handoff.SourceRecord{Commitment: handoff.IntentCommitment(intent), State: r.State, ReceiptExpiresAt: r.ExpiresAt, Version: v}
		if r.Journal != nil && r.Journal.BindingDigest == journalBinding(trusted.Binding) && r.Journal.Commitment == record.Commitment {
			record.DeliveryKey = r.Journal.DeliveryKey
			record.Fingerprint = r.Journal.Fingerprint
		}
		return record, nil
	}
}

// withHandoffReceipt shares guarded, primary-confirmed access for the offline
// readers/reservation only. A callback mutates the SAME ledger value as receipt
// state; it never performs an external action. No live route calls this helper.
func (p *Plugin) withHandoffReceipt(ctx context.Context, trusted sourceBinding, requested handoff.Binding, id string, change func(*receipt, configuration, *model.CommandArgs, int64) error) (*receipt, configuration, uint64, error) {
	deny := func() (*receipt, configuration, uint64, error) { return nil, configuration{}, 0, handoff.ErrRejected }
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
	apiBudget := *budget
	apiBudget.cancelled = func() bool { return ctx.Err() != nil }
	worker := &Plugin{clock: p.clock, workClock: p.workClock, retrySleep: p.retrySleep, budget: &apiBudget, sharedGuard: guard}
	worker.SetAPI(&budgetAPI{API: p.API, budget: &apiBudget})
	var config configuration
	if worker.API.LoadPluginConfiguration(&config) != nil || !config.valid() || config.OrganizationID != b.Organization || config.EmployeeID != b.Employee || config.BotUserID != b.Bot || !worker.authorize(config, args) {
		return deny()
	}
	var confirmedVersion uint64
	r, err := worker.mutate(func(s *ledger, at int64) (*receipt, error) {
		r := s.Receipts[id]
		if !ownedReceiptAt(r, id, config, args, at) {
			return nil, errMissing
		}
		if change != nil {
			if err := change(r, config, args, at); err != nil {
				return nil, err
			}
		}
		confirmedVersion = s.Revision + 1
		return r, nil
	})
	if err != nil {
		return deny()
	}
	var latest configuration
	if worker.API.LoadPluginConfiguration(&latest) != nil || latest.hash() != config.hash() || !worker.authorize(latest, args) || !ownedReceiptAt(r, id, latest, args, worker.nowMillis()) || ctx.Err() != nil || budget.expired() {
		return deny()
	}
	return r, latest, confirmedVersion, nil
}
