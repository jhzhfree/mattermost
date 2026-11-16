//go:build octopcontract

// See LICENSE.txt in the repository root for license information.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

// Export only synthetic signed requests and public evidence. No private key is
// serialized. The Python runner invokes the pinned Octop ASGI/service/repository.
func TestExportOctopContract(t *testing.T) {
	path := os.Getenv("AITAGENT_OCTOP_EXPORT")
	if path == "" {
		t.Skip("isolated contract runner only")
	}
	p, f, args, proof, policy, b := journalFixture(t)
	r := readOnlyReceipt(t, f)
	if err := p.reserveHandoff(context.Background(), b, proof, policy); err != nil {
		t.Fatal(err)
	}
	active, err := p.handoffSourceReader(b)(context.Background(), b.Binding, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.lookup(r.ID, f.config, args, true); err != nil {
		t.Fatal(err)
	}
	cancelled, err := p.handoffCancellationReader(b, r.ID)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Commitment != active.Commitment || cancelled.ReceiptExpiresAt != active.ReceiptExpiresAt {
		t.Fatal("original commitment lost on cancel")
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := p.nowMillis() / 1000
	candidate, err := handoff.Verify(proof, policy, now)
	if err != nil {
		t.Fatal(err)
	}
	base := handoff.OctopIntakePath
	item := base + "/" + candidate.DeliveryKey
	type request struct {
		Method, Path string
		Body         []byte
		Headers      map[string]string
	}
	requests := map[string]request{}
	add := func(name, method, url string, body []byte, at int64) {
		h, e := handoff.SignOfflineBrokerRequest("fixture-broker", key, policy.PublicKey, method, url, body, at)
		if e != nil {
			t.Fatal(e)
		}
		requests[name] = request{method, url, body, h}
	}
	cancelBody, _ := json.Marshal(map[string]any{"operation_id": digest("contract-cancel"), "expected_revision": 1})
	for _, name := range []string{"create", "duplicate", "revoked", "source_error", "revive"} {
		add(name, "POST", base, proof, now)
	}
	for _, name := range []string{"inspect", "inspect_after_restart", "wrong_body", "wrong_path", "missing_auth"} {
		add(name, "GET", item, nil, now)
	}
	add("expired_auth", "GET", item, nil, now-30)
	add("cancel_not_ready", "POST", item+"/cancellations", cancelBody, now)
	add("cancel", "POST", item+"/cancellations", cancelBody, now)
	add("cancel_duplicate", "POST", item+"/cancellations", cancelBody, now)
	add("inspect_expired_proof", "GET", item, nil, now+61)
	add("bad_cancel", "POST", item+"/cancellations", []byte(`{"operation_id":"bad","expected_revision":1,"actor_id":1}`), now)
	out := map[string]any{"now": now, "policy": policy, "broker_public_key": hex.EncodeToString(pub), "active": active, "cancelled": cancelled, "requests": requests, "delivery_key": candidate.DeliveryKey, "fingerprint": candidate.Fingerprint}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
