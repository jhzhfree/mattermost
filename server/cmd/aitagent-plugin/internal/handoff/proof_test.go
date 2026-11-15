// See LICENSE.txt in the repository root for license information.

package handoff

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func fixture(t testing.TB) (Intent, Policy, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	i := Intent{Binding: Binding{"mm-test", "octop-test", "org", "employee", strings.Repeat("u", 26), strings.Repeat("t", 26), strings.Repeat("c", 26), strings.Repeat("b", 26)}, Receipt: strings.Repeat("a", 64), Goal: "Check health; <untrusted> 执行文本不能授权", Mode: "user_requested"}
	return i, Policy{Enabled: true, KeyID: "test-key", PublicKey: pub, Binding: i.Binding, OctopActorID: 9}, priv
}

func TestStrictProofAndBinding(t *testing.T) {
	i, p, key := fixture(t)
	proof, err := Seal(i, p.KeyID, key, 100, 160)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(proof, p, 101); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Policy){
		"disabled": func(p *Policy) { p.Enabled = false }, "wrong-key": func(p *Policy) { p.PublicKey = make([]byte, 32) },
		"revoked-key-id": func(p *Policy) { p.KeyID = "rotated" }, "unmapped": func(p *Policy) { p.OctopActorID = 0 },
		"instance": func(p *Policy) { p.Binding.Instance = "other" }, "audience": func(p *Policy) { p.Binding.Audience = "other" },
		"organization": func(p *Policy) { p.Binding.Organization = "other" }, "employee": func(p *Policy) { p.Binding.Employee = "other" },
		"user": func(p *Policy) { p.Binding.User = strings.Repeat("x", 26) }, "team": func(p *Policy) { p.Binding.Team = strings.Repeat("x", 26) },
		"channel": func(p *Policy) { p.Binding.Channel = strings.Repeat("x", 26) }, "bot": func(p *Policy) { p.Binding.Bot = strings.Repeat("x", 26) },
	} {
		t.Run(name, func(t *testing.T) {
			copy := p
			change(&copy)
			if _, err := Verify(proof, copy, 101); err != ErrRejected {
				t.Fatal("accepted altered receiver policy")
			}
		})
	}
	for _, now := range []int64{-1, 99, 160, 161} {
		if _, err := Verify(proof, p, now); err != ErrRejected {
			t.Fatal("accepted invalid time")
		}
	}
	var e envelope
	_ = json.Unmarshal(proof, &e)
	var c claims
	_ = json.Unmarshal(e.Payload, &c)
	c.Intent.Goal = "execute anything"
	e.Payload, _ = json.Marshal(c)
	tampered, _ := json.Marshal(e)
	for _, bad := range [][]byte{nil, []byte("null"), append(proof, ' '), append(proof, proof...), bytes.Repeat([]byte("x"), MaxProofBytes+1), tampered,
		append(append([]byte{}, proof[:len(proof)-1]...), []byte(",\"execution_authorized\":true}")...),
	} {
		if candidate, err := Verify(bad, p, 101); err != ErrRejected || candidate.DeliveryKey != "" {
			t.Fatal("invalid proof accepted or returned data")
		}
	}
}

func TestSignedMalformedClaimsRejected(t *testing.T) {
	i, p, key := fixture(t)
	base := claims{1, p.KeyID, 100, 160, i}
	for name, change := range map[string]func(*claims){
		"version": func(c *claims) { c.Version = 2 }, "lifetime": func(c *claims) { c.Expires = 221 },
		"bot-actor": func(c *claims) { c.Intent.Binding.User = c.Intent.Binding.Bot },
		"automatic": func(c *claims) { c.Intent.Mode = "scheduled" }, "empty": func(c *claims) { c.Intent.Goal = "  " },
		"long-goal": func(c *claims) { c.Intent.Goal = strings.Repeat("界", 2001) },
		"receipt":   func(c *claims) { c.Intent.Receipt = "not-a-receipt" },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			change(&c)
			payload, _ := json.Marshal(c)
			proof, _ := json.Marshal(envelope{payload, ed25519.Sign(key, append([]byte(domain), payload...))})
			if _, err := Verify(proof, p, 101); err != ErrRejected {
				t.Fatal("accepted malformed signed claims")
			}
		})
	}
	payload, _ := json.Marshal(base)
	for _, bad := range [][]byte{
		append(append([]byte{}, payload[:len(payload)-1]...), []byte(",\"version\":1}")...),
		append(append([]byte{}, payload[:len(payload)-1]...), []byte(",\"execution_authorized\":true}")...),
	} {
		proof, _ := json.Marshal(envelope{bad, ed25519.Sign(key, append([]byte(domain), bad...))})
		if _, err := Verify(proof, p, 101); err != ErrRejected {
			t.Fatal("accepted duplicate/unknown signed field")
		}
	}
	if _, err := Seal(i, p.KeyID, nil, 100, 160); err != ErrRejected {
		t.Fatal("invalid signer key")
	}
}

func TestConcurrentReceiverSimulationAndConflictingRetries(t *testing.T) {
	i, p, key := fixture(t)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	// Test-only inbox demonstrates the required atomic comparison. This map is
	// neither production persistence nor evidence of a running Octop receiver.
	var mu sync.Mutex
	inbox := map[string]string{}
	accept := func(proof []byte, p Policy) string {
		c, err := Verify(proof, p, 110)
		if err != nil {
			return "rejected"
		}
		mu.Lock()
		defer mu.Unlock()
		if previous, exists := inbox[c.DeliveryKey]; exists {
			if previous == c.Fingerprint {
				return "duplicate"
			}
			return "conflict"
		}
		inbox[c.DeliveryKey] = c.Fingerprint
		return "pending_authorization"
	}
	var wg sync.WaitGroup
	for n := 0; n < 32; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := accept(proof, p)
			if result != "pending_authorization" && result != "duplicate" {
				t.Error(result)
			}
		}()
	}
	wg.Wait()
	if len(inbox) != 1 {
		t.Fatal("duplicate receipt created multiple intents")
	}
	// Proof refresh and signing-key rotation must not produce a new delivery.
	pub2, key2, _ := ed25519.GenerateKey(rand.Reader)
	rotated := p
	rotated.PublicKey = pub2
	rotated.KeyID = "new-key"
	fresh, _ := Seal(i, rotated.KeyID, key2, 105, 165)
	if accept(fresh, rotated) != "duplicate" {
		t.Fatal("rotation reset receipt identity")
	}
	i.Goal = "different instruction"
	different, _ := Seal(i, p.KeyID, key, 100, 160)
	if accept(different, p) != "conflict" {
		t.Fatal("changed content accepted as same intent")
	}
	remapped := p
	remapped.OctopActorID = 10
	if accept(proof, remapped) != "conflict" {
		t.Fatal("changed actor mapping accepted as same intent")
	}
	p.Enabled = false
	if accept(proof, p) != "rejected" {
		t.Fatal("duplicate bypassed revocation")
	}
}

func FuzzVerify(f *testing.F) {
	i, p, key := fixture(f)
	proof, err := Seal(i, p.KeyID, key, 100, 160)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(proof)
	f.Add([]byte("{}"))
	f.Fuzz(func(t *testing.T, data []byte) {
		candidate, err := Verify(data, p, 101)
		if err == nil && (candidate.Intent != i || candidate.OctopActorID != p.OctopActorID) {
			t.Fatal("accepted an altered signed intent")
		}
		if err != nil && (err != ErrRejected || candidate.DeliveryKey != "") {
			t.Fatal("rejection leaked data")
		}
	})
}
