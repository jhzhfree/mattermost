// See LICENSE.txt in the repository root for license information.

package main

import (
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

// handoffDraft is an offline conversion boundary, not an authorization check.
// A future issuer must obtain a primary-confirmed receipt, recheck live access
// and cancellation, and bind its isolated signer to deployment-owned instance
// and audience. No command, hook, worker, HTTP route or key provider calls this.
// Configuration/receipt snapshots alone cannot prove live permission or safe
// dispatch. This function deliberately does not sign, mutate, or send anything.
func handoffDraft(r *receipt, c configuration, args *model.CommandArgs, instance, audience string, now int64) (handoff.Intent, error) {
	if args == nil || r == nil || now <= 0 || !c.valid() || !ownedReceiptAt(r, r.ID, c, args, now) ||
		r.State != receiptState || r.CreatedAt <= 0 || r.CreatedAt > now || r.GoalHash != digest(r.Goal) {
		return handoff.Intent{}, handoff.ErrRejected
	}
	return handoff.Intent{
		Binding: handoff.Binding{Instance: instance, Audience: audience, Organization: r.OrganizationID,
			Employee: r.EmployeeID, User: r.ActorUserID, Team: r.TeamID, Channel: r.ChannelID, Bot: r.BotUserID},
		Receipt: r.ID, ReceiptExpiresAt: r.ExpiresAt, Goal: r.Goal, Mode: r.OperationMode,
	}, nil
}
