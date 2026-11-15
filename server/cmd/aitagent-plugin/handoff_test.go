// See LICENSE.txt in the repository root for license information.

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

func TestReceiptToOfflineVerifiedIntent(t *testing.T) {
	p, f, args := fixture()
	f.config.OrganizationID = "组织/运营 A"
	f.config.EmployeeID = " 岗位:分析员 "
	const now = int64(1800000000000)
	p.clock = func() int64 { return now }
	invoke(t, p, args)
	state := readLedger(t, f)
	var r *receipt
	for _, item := range state.Receipts {
		r = item
	}
	draft, err := handoffDraft(r, f.config, args, "test-mm", "test-octop", now)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := handoff.Seal(draft, "ephemeral-test", priv, now/1000, now/1000+60)
	if err != nil {
		t.Fatal(err)
	}
	policy := handoff.Policy{Enabled: true, KeyID: "ephemeral-test", PublicKey: pub, Binding: draft.Binding, OctopActorID: 42, OctopOrganizationID: "target-org", OctopEmployeeID: "target-worker"}
	candidate, err := handoff.Verify(proof, policy, now/1000)
	if err != nil || candidate.Intent.Goal != "Inspect service health" || candidate.OctopActorID != 42 || candidate.Intent.ReceiptExpiresAt != r.ExpiresAt {
		t.Fatal("offline boundary failed", err)
	}
	if f.writes != 1 || readLedger(t, f).Receipts[r.ID].State != receiptState {
		t.Fatal("proof changed ledger or claimed delivery")
	}
	for _, change := range []func(*receipt){
		func(r *receipt) { r.State = cancelledState },
		func(r *receipt) { r.ExpiresAt = now },
		func(r *receipt) { r.Goal += " injected" },
		func(r *receipt) { r.ActorUserID = f.config.BotUserID },
		func(r *receipt) { r.BindingHash = "changed" },
		func(r *receipt) { r.CreatedAt = now + 1 },
	} {
		copy := *r
		change(&copy)
		if _, err := handoffDraft(&copy, f.config, args, "test-mm", "test-octop", now); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
	// The already signed proof remains cryptographically valid after source
	// cancellation. Only a fresh source read can detect this; never skip it for
	// retries. This adapter uses the actual command fixture's current ledger.
	source := func(_ context.Context, _ handoff.Binding, id string) (handoff.SourceRecord, error) {
		if !p.authorize(f.config, args) {
			return handoff.SourceRecord{}, handoff.ErrRejected
		}
		current := readLedger(t, f).Receipts[id]
		i, err := handoffDraft(current, f.config, args, "test-mm", "test-octop", now)
		if err != nil {
			return handoff.SourceRecord{}, err
		}
		return handoff.SourceRecord{Commitment: handoff.IntentCommitment(i), State: current.State, ReceiptExpiresAt: i.ReceiptExpiresAt}, nil
	}
	if _, err := handoff.VerifyWithSource(context.Background(), proof, policy, func() int64 { return now / 1000 }, source); err != nil {
		t.Fatal(err)
	}
	f.permission = false
	if _, err := handoff.VerifyWithSource(context.Background(), proof, policy, func() int64 { return now / 1000 }, source); err != handoff.ErrRejected {
		t.Fatal("source permission revocation ignored")
	}
	f.permission = true
	cancelArgs := *args
	cancelArgs.Command = "/aitagent cancel " + r.ID
	invoke(t, p, &cancelArgs)
	if _, err := handoff.Verify(proof, policy, now/1000); err != nil {
		t.Fatal("cryptographic validity unexpectedly changed")
	}
	if _, err := handoff.VerifyWithSource(context.Background(), proof, policy, func() int64 { return now / 1000 }, source); err != handoff.ErrRejected {
		t.Fatal("cancelled source proof accepted")
	}
}
