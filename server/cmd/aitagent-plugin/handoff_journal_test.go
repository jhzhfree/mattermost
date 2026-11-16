// See LICENSE.txt in the repository root for license information.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

func journalFixture(t *testing.T) (*Plugin, *fakeAPI, *model.CommandArgs, []byte, handoff.Policy, sourceBinding) {
	t.Helper()
	p, f, args := fixture()
	now := model.GetMillis()
	p.clock = func() int64 { return now }
	invoke(t, p, args)
	r := readOnlyReceipt(t, f)
	i, err := handoffDraft(&r, f.config, args, "test-mm", "test-octop", now)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := handoff.Policy{Enabled: true, KeyID: "test-key", PublicKey: pub, Binding: i.Binding, OctopActorID: 42, OctopOrganizationID: "target-org", OctopEmployeeID: "target-worker"}
	proof, err := handoff.Seal(i, policy.KeyID, key, now/1000, now/1000+60)
	if err != nil {
		t.Fatal(err)
	}
	return p, f, args, proof, policy, sourceBinding{Enabled: true, Binding: i.Binding}
}
func TestSourceJournalReservationAndCancellationShareCASValue(t *testing.T) {
	p, f, args, proof, policy, b := journalFixture(t)
	r := readOnlyReceipt(t, f)
	if err := p.reserveHandoff(context.Background(), b, proof, policy); err != nil {
		t.Fatal(err)
	}
	marked := readOnlyReceipt(t, f)
	if marked.Journal == nil || !marked.Journal.valid() || marked.ID != r.ID || marked.GoalHash != r.GoalHash {
		t.Fatal("association missing")
	}
	before := *marked.Journal
	if err := p.reserveHandoff(context.Background(), b, proof, policy); err != nil {
		t.Fatal(err)
	}
	// Different receiver mapping changes fingerprint but cannot replace association.
	changed := policy
	changed.OctopActorID++
	if err := p.reserveHandoff(context.Background(), b, proof, changed); err == nil {
		t.Fatal("remapped identity")
	}
	if _, err := p.lookup(r.ID, f.config, args, true); err != nil {
		t.Fatal(err)
	}
	cancelled := readOnlyReceipt(t, f)
	if cancelled.State != cancelledState || cancelled.Goal != "" || cancelled.Journal == nil || *cancelled.Journal != before {
		t.Fatal("cancel lost association")
	}
	evidence, err := p.handoffCancellationReader(b, r.ID)(context.Background())
	if err != nil || !evidence.Cancelled || evidence.DeliveryKey != before.DeliveryKey || evidence.Fingerprint != before.Fingerprint || evidence.Version != readLedger(t, f).Revision {
		t.Fatal(evidence, err)
	}
	raw, _ := json.Marshal(evidence)
	if strings.Contains(string(raw), r.Goal) {
		t.Fatal("goal disclosed")
	}
	f.permission = false
	if _, err = p.handoffCancellationReader(b, r.ID)(context.Background()); err == nil {
		t.Fatal("permission revocation ignored")
	}
}
func TestSourceJournalUnknownCommitAndReplicaLag(t *testing.T) {
	for _, commit := range []bool{false, true} {
		p, f, _, proof, policy, b := journalFixture(t)
		id := readOnlyReceipt(t, f).ID
		p.SetAPI(&ambiguousAPI{fakeAPI: f, commit: commit})
		if err := p.reserveHandoff(context.Background(), b, proof, policy); err == nil {
			t.Fatal("claimed known commit")
		}
		p.SetAPI(f)
		if got := readOnlyReceipt(t, f).Journal != nil; got != commit {
			t.Fatal(got, commit)
		}
		if err := p.reserveHandoff(context.Background(), b, proof, policy); err != nil {
			t.Fatal(err)
		}
		if readOnlyReceipt(t, f).ID != id {
			t.Fatal("changed original identity")
		}
	}
	p, f, args, proof, policy, b := journalFixture(t)
	snapshot := append([]byte(nil), f.values[ledgerKey]...)
	if err := p.reserveHandoff(context.Background(), b, proof, policy); err != nil {
		t.Fatal(err)
	}
	id := readOnlyReceipt(t, f).ID
	if _, err := p.lookup(id, f.config, args, true); err != nil {
		t.Fatal(err)
	}
	p.SetAPI(&laggingAPI{fakeAPI: f, snapshot: snapshot})
	if _, err := p.handoffCancellationReader(b, id)(context.Background()); err == nil {
		t.Fatal("stale replica accepted")
	}
	if r := readOnlyReceipt(t, f); r.State != cancelledState || r.Journal == nil {
		t.Fatal("stale write replaced marker or cancellation")
	}
}
func TestSourceJournalConcurrentCancelNeverSplitsIdentity(t *testing.T) {
	for n := 0; n < 12; n++ {
		p, f, args, proof, policy, b := journalFixture(t)
		id := readOnlyReceipt(t, f).ID
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = p.reserveHandoff(context.Background(), b, proof, policy) }()
		go func() { defer wg.Done(); _, _ = p.lookup(id, f.config, args, true) }()
		wg.Wait()
		r := readOnlyReceipt(t, f)
		if r.State != cancelledState || r.Goal != "" || (r.Journal != nil && !r.Journal.valid()) {
			t.Fatal("split source state")
		}
		if err := p.reserveHandoff(context.Background(), b, proof, policy); err == nil {
			t.Fatal("revived cancelled source")
		}
	}
}

func TestSourceJournalMissingDisabledAndExpiredFailClosed(t *testing.T) {
	p, f, args, proof, policy, b := journalFixture(t)
	r := readOnlyReceipt(t, f)
	if _, err := p.handoffCancellationReader(b, r.ID)(context.Background()); err == nil {
		t.Fatal("invented missing association")
	}
	disabled := b
	disabled.Enabled = false
	writes := f.writes
	if err := p.reserveHandoff(context.Background(), disabled, proof, policy); err == nil || f.writes != writes {
		t.Fatal("disabled reservation wrote")
	}
	if err := p.reserveHandoff(context.Background(), b, proof, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := p.lookup(r.ID, f.config, args, true); err != nil {
		t.Fatal(err)
	}
	p.clock = func() int64 { return r.ExpiresAt }
	if _, err := p.handoffCancellationReader(b, r.ID)(context.Background()); err == nil {
		t.Fatal("expired association treated as current")
	}
	// Pruning is not an acknowledgement that the receiver was cancelled.
	if len(readLedger(t, f).Receipts) != 0 {
		t.Fatal("expected lazy source expiry")
	}
}
