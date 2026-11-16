// See LICENSE.txt in the repository root for license information.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

func queryFixture(t *testing.T) (*sourceQueryBoundary, *fakeAPI, *model.CommandArgs, sourceQueryTrust, ed25519.PrivateKey, string) {
	t.Helper()
	p, f, args, proof, policy, b := journalFixture(t)
	if err := p.reserveHandoff(context.Background(), b, proof, policy); err != nil {
		t.Fatal(err)
	}
	broker, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	source, secret, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := sourceQueryTrust{Enabled: true, Broker: "fixture-broker", KeyID: "fixture-source", Revision: 1, Binding: b.Binding}
	copy(trust.BrokerKey[:], broker)
	copy(trust.SourceKey[:], source)
	copy(trust.ProofKey[:], policy.PublicKey)
	boundary := &sourceQueryBoundary{plugin: p, resolve: func(context.Context) (sourceQueryTrust, error) { return trust, nil }, sign: func(_ context.Context, m []byte) ([]byte, error) { return ed25519.Sign(secret, m), nil }}
	return boundary, f, args, trust, key, readOnlyReceipt(t, f).ID
}
func queryBytes(t *testing.T, s *sourceQueryBoundary, trust sourceQueryTrust, key ed25519.PrivateKey, id, nonce string) ([]byte, string) {
	t.Helper()
	raw, err := json.Marshal(sourceQuery{1, trust.Broker, digest(nonce), s.plugin.nowMillis() / 1000, trust.Binding, id})
	if err != nil {
		t.Fatal(err)
	}
	return raw, base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(sourceQueryDomain), raw...)))
}
func callQuery(s *sourceQueryBoundary, raw []byte, sig string) ([]byte, error) {
	return s.query(context.Background(), "POST", sourceQueryPath, "application/json", "fixture-broker", sig, raw)
}

func TestSourceQueryPrimaryCancellationAndPersistentReplay(t *testing.T) {
	s, f, args, trust, key, id := queryFixture(t)
	raw, sig := queryBytes(t, s, trust, key, id, "first")
	encoded, err := callQuery(s, raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct{ Payload, Signature []byte }
	if json.Unmarshal(encoded, &envelope) != nil || !ed25519.Verify(trust.SourceKey[:], append([]byte(sourceResponseDomain), envelope.Payload...), envelope.Signature) {
		t.Fatal("signature")
	}
	var response struct{ Observation sourceWireObservation }
	if json.Unmarshal(envelope.Payload, &response) != nil || response.Observation.State != receiptState {
		t.Fatal("active response")
	}
	// New boundary instance cannot refund persisted nonce.
	restarted := *s
	if _, err = callQuery(&restarted, raw, sig); err == nil {
		t.Fatal("replay")
	}
	if _, err = s.plugin.lookup(id, f.config, args, true); err != nil {
		t.Fatal(err)
	}
	raw, sig = queryBytes(t, s, trust, key, id, "cancelled")
	encoded, err = callQuery(s, raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(encoded, &envelope)
	json.Unmarshal(envelope.Payload, &response)
	if response.Observation.State != cancelledState || response.Observation.Commitment == "" {
		t.Fatal(response)
	}
}

func TestSourceQueryRejectsCurrentAuthorityAndUnconfirmedState(t *testing.T) {
	for _, mode := range []string{"revoked", "registry", "rollback", "stale", "unknown", "timeout", "clock", "key", "disabled", "expired", "wrong_binding", "bad_signature", "duplicates"} {
		t.Run(mode, func(t *testing.T) {
			s, f, _, trust, key, id := queryFixture(t)
			raw, sig := queryBytes(t, s, trust, key, id, mode)
			switch mode {
			case "revoked":
				f.permission = false
			case "registry":
				calls := 0
				s.resolve = func(context.Context) (sourceQueryTrust, error) {
					calls++
					if calls > 1 {
						trust.Revision++
					}
					return trust, nil
				}
			case "rollback":
				trust.MinimumVersion = readLedger(t, f).Revision + 10
				s.resolve = func(context.Context) (sourceQueryTrust, error) { return trust, nil }
			case "stale":
				snapshot := append([]byte(nil), f.values[ledgerKey]...)
				r := readLedger(t, f)
				r.Revision++
				f.values[ledgerKey], _ = json.Marshal(r)
				s.plugin.SetAPI(&laggingAPI{fakeAPI: f, snapshot: snapshot})
				s.plugin.retrySleep = func(time.Duration) {}
			case "unknown":
				s.plugin.SetAPI(&ambiguousAPI{fakeAPI: f, commit: true})
			case "timeout":
				old := s.sign
				s.sign = func(ctx context.Context, m []byte) ([]byte, error) { <-ctx.Done(); return old(ctx, m) }
			case "clock":
				now := s.plugin.nowMillis()
				s.plugin.clock = func() int64 { return now - 1000 }
			case "key":
				trust.SourceKey = trust.BrokerKey
				s.resolve = func(context.Context) (sourceQueryTrust, error) { return trust, nil }
			case "disabled":
				trust.Enabled = false
				s.resolve = func(context.Context) (sourceQueryTrust, error) { return trust, nil }
			case "expired":
				now := s.plugin.nowMillis()
				s.plugin.clock = func() int64 { return now + 5000 }
			case "wrong_binding":
				trust.Binding.Channel = model.NewId()
				s.resolve = func(context.Context) (sourceQueryTrust, error) { return trust, nil }
			case "bad_signature":
				sig = base64.StdEncoding.EncodeToString(make([]byte, 64))
			case "duplicates":
				raw = append([]byte(`{"protocol_version":1,`), raw[1:]...)
				sig = base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(sourceQueryDomain), raw...)))
			}
			if result, err := callQuery(s, raw, sig); err == nil || len(result) != 0 {
				t.Fatal("returned signed evidence", mode)
			}
		})
	}
}
func TestSourceQueryConcurrentNonceConsumedOnce(t *testing.T) {
	s, _, _, trust, key, id := queryFixture(t)
	raw, sig := queryBytes(t, s, trust, key, id, "same")
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := callQuery(s, raw, sig); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal(successes.Load())
	}
}
func TestSourceQueryCapacityAndExpiredNonceCleanup(t *testing.T) {
	s, f, _, trust, key, id := queryFixture(t)
	state := readLedger(t, f)
	now := s.plugin.nowMillis()
	state.SourceNonces = map[string]int64{}
	for i := range 128 {
		state.SourceNonces[digest(fmt.Sprint(i))] = now + 5000
	}
	f.values[ledgerKey], _ = json.Marshal(state)
	raw, sig := queryBytes(t, s, trust, key, id, "capacity")
	if _, err := callQuery(s, raw, sig); err == nil {
		t.Fatal("capacity bypass")
	}
	s.plugin.clock = func() int64 { return now + 5001 }
	raw, sig = queryBytes(t, s, trust, key, id, "after-expiry")
	if _, err := callQuery(s, raw, sig); err != nil {
		t.Fatal(err)
	}
	if len(readLedger(t, f).SourceNonces) != 1 {
		t.Fatal("expired nonces not pruned")
	}
}
func TestSourceQueryDefaultClosed(t *testing.T) {
	var s sourceQueryBoundary
	if _, err := callQuery(&s, nil, ""); err == nil {
		t.Fatal("default open")
	}
}

func TestSourceQueryUnknownCommitBurnsChallengeAndWrongRouteDoesNotRead(t *testing.T) {
	s, f, _, trust, key, id := queryFixture(t)
	raw, sig := queryBytes(t, s, trust, key, id, "unknown")
	before := f.writes
	for _, route := range []struct{ method, path string }{{"GET", sourceQueryPath}, {"POST", sourceQueryPath + "?x=1"}, {"POST", "/api/v4/posts"}} {
		if _, err := s.query(context.Background(), route.method, route.path, "application/json", trust.Broker, sig, raw); err == nil {
			t.Fatal("route accepted")
		}
	}
	if f.writes != before {
		t.Fatal("rejected route touched storage")
	}
	s.plugin.SetAPI(&ambiguousAPI{fakeAPI: f, commit: true})
	if _, err := callQuery(s, raw, sig); err == nil {
		t.Fatal("unknown commit returned evidence")
	}
	s.plugin.SetAPI(f)
	if _, err := callQuery(s, raw, sig); err == nil {
		t.Fatal("unknown committed nonce replayed")
	}
	raw, sig = queryBytes(t, s, trust, key, id, "reconcile-new-challenge")
	if _, err := callQuery(s, raw, sig); err != nil {
		t.Fatal(err)
	}
}
