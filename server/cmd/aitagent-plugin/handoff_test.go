// See LICENSE.txt in the repository root for license information.

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

func TestReceiptToOfflineVerifiedIntent(t *testing.T) {
	p, f, args := fixture()
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
	policy := handoff.Policy{Enabled: true, KeyID: "ephemeral-test", PublicKey: pub, Binding: draft.Binding, OctopActorID: 42}
	candidate, err := handoff.Verify(proof, policy, now/1000)
	if err != nil || candidate.Intent.Goal != "Inspect service health" || candidate.OctopActorID != 42 {
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
}
