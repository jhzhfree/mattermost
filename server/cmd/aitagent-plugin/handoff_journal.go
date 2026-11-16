// See LICENSE.txt in the repository root for license information.
package main

import (
	"context"
	"encoding/json"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

// sourceJournal is an immutable association inside the receipt's CAS value.
// It has no delivery/ack flag. Cancellation derives from the SAME receipt state,
// never a second write. No command/runtime path creates these offline markers.
type sourceJournal struct {
	Commitment    string `json:"commitment,omitempty"`
	DeliveryKey   string `json:"delivery_key"`
	Fingerprint   string `json:"fingerprint"`
	BindingDigest string `json:"binding_digest"`
}

func (j *sourceJournal) valid() bool {
	return j != nil && validReceiptID(j.DeliveryKey) && validReceiptID(j.Fingerprint) && validReceiptID(j.BindingDigest) && (j.Commitment == "" || validReceiptID(j.Commitment))
}
func journalBinding(b handoff.Binding) string { raw, _ := json.Marshal(b); return digest(string(raw)) }

// reserveHandoff associates the exact proof/receiver mapping with an EXISTING
// receipt atomically. It does not create the original receipt atomically with a
// PostgreSQL outbox, schedule work, sign, send or authorize execution. Policy and
// binding are deployment-owned inputs; never deserialize them from a request.
func (p *Plugin) reserveHandoff(ctx context.Context, trusted sourceBinding, proof []byte, policy handoff.Policy) error {
	c, err := handoff.Verify(proof, policy, p.nowMillis()/1000)
	if err != nil || policy.Binding != trusted.Binding {
		return handoff.ErrRejected
	}
	_, _, _, err = p.withHandoffReceipt(ctx, trusted, c.Intent.Binding, c.Intent.Receipt, func(r *receipt, config configuration, args *model.CommandArgs, at int64) error {
		fresh, err := handoff.Verify(proof, policy, at/1000)
		if err != nil {
			return err
		}
		intent, err := handoffDraft(r, config, args, trusted.Binding.Instance, trusted.Binding.Audience, at)
		if err != nil || intent != fresh.Intent {
			return handoff.ErrRejected
		}
		wanted := sourceJournal{Commitment: handoff.IntentCommitment(intent), DeliveryKey: fresh.DeliveryKey, Fingerprint: fresh.Fingerprint, BindingDigest: journalBinding(trusted.Binding)}
		if r.Journal != nil && *r.Journal != wanted {
			return handoff.ErrRejected
		}
		r.Journal = &wanted
		return nil
	})
	// Any error (including post-commit permission failure) may have left a marker.
	// Reconcile the same receipt; never infer absence or select a new identity.
	return err
}

// handoffCancellationReader returns only the same-row association and current
// cancelled state after guarded primary confirmation. It has no HTTP/service
// authenticator. Expired/pruned receipts and revoked callers fail closed.
func (p *Plugin) handoffCancellationReader(trusted sourceBinding, id string) handoff.CancellationReader {
	return func(ctx context.Context) (handoff.CancellationEvidence, error) {
		r, _, version, err := p.withHandoffReceipt(ctx, trusted, trusted.Binding, id, nil)
		if err != nil || r.Journal == nil || r.Journal.Commitment == "" || r.Journal.BindingDigest != journalBinding(trusted.Binding) {
			return handoff.CancellationEvidence{}, handoff.ErrRejected
		}
		return handoff.CancellationEvidence{Commitment: r.Journal.Commitment, ReceiptExpiresAt: r.ExpiresAt, DeliveryKey: r.Journal.DeliveryKey, Fingerprint: r.Journal.Fingerprint, Version: version, Cancelled: r.State == cancelledState}, nil
	}
}
