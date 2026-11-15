// See LICENSE.txt in the repository root for license information.

package handoff

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestProofCannotOutliveReceipt(t *testing.T) {
	i, p, key := fixture(t)
	i.ReceiptExpiresAt = 160999 // floor to 160s, never round up beyond source expiry
	if _, err := Seal(i, p.KeyID, key, 100, 161); err != ErrRejected {
		t.Fatal("signer exceeded source expiry")
	}
	proof, err := Seal(i, p.KeyID, key, 100, 160)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(proof, p, 159); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(proof, p, 160); err != ErrRejected {
		t.Fatal("expired proof accepted")
	}
	// Independently signed malformed proofs must fail at the receiver too.
	for _, expiry := range []int64{0, -1, 159999} {
		i.ReceiptExpiresAt = expiry
		payload, _ := json.Marshal(claims{2, p.KeyID, 100, 160, i})
		bad, _ := json.Marshal(envelope{payload, ed25519.Sign(key, append([]byte(domain), payload...))})
		if _, err := Verify(bad, p, 101); err != ErrRejected {
			t.Fatal("receiver trusted source expiry violation")
		}
	}
}

func TestSourceRecheckFailsClosedAndRechecksTime(t *testing.T) {
	i, p, key := fixture(t)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	clock := func() int64 { return 101 }
	ctx := context.Background()
	for name, reader := range map[string]SourceReader{
		"zero-version": func(context.Context, Binding, string) (SourceRecord, error) {
			r := observeSource(i, "awaiting_octop_authorization")
			r.Version = 0
			return r, nil
		},
		"overflow-version": func(context.Context, Binding, string) (SourceRecord, error) {
			r := observeSource(i, "awaiting_octop_authorization")
			r.Version = ^uint64(0)
			return r, nil
		},
		"missing-provider": nil,
		"unavailable": func(context.Context, Binding, string) (SourceRecord, error) {
			return SourceRecord{}, errors.New("private upstream detail")
		},
		"cancelled": func(context.Context, Binding, string) (SourceRecord, error) {
			return observeSource(i, "cancelled"), nil
		},
		"missing": func(context.Context, Binding, string) (SourceRecord, error) { return SourceRecord{}, nil },
		"changed-goal": func(context.Context, Binding, string) (SourceRecord, error) {
			other := i
			other.Goal = "changed"
			return observeSource(other, "awaiting_octop_authorization"), nil
		},
		"changed-expiry": func(context.Context, Binding, string) (SourceRecord, error) {
			other := i
			other.ReceiptExpiresAt--
			return observeSource(other, "awaiting_octop_authorization"), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := VerifyWithSource(ctx, proof, p, clock, reader)
			if err != ErrRejected || c.DeliveryKey != "" {
				t.Fatal("source failure did not close")
			}
		})
	}
	calls := 0
	reader := func(_ context.Context, b Binding, id string) (SourceRecord, error) {
		calls++
		if b != i.Binding || id != i.Receipt {
			t.Fatal("wrong lookup scope")
		}
		return observeSource(i, "awaiting_octop_authorization"), nil
	}
	for n := 0; n < 2; n++ {
		if _, err := VerifyWithSource(ctx, proof, p, clock, reader); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("duplicate skipped source lookup")
	}
	for _, after := range []int64{99, 160} {
		now := int64(101)
		slow := func(context.Context, Binding, string) (SourceRecord, error) {
			now = after
			return observeSource(i, "awaiting_octop_authorization"), nil
		}
		if _, err := VerifyWithSource(ctx, proof, p, func() int64 { return now }, slow); err != ErrRejected {
			t.Fatal("clock rewind or expiry during lookup accepted")
		}
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancelReader := func(context.Context, Binding, string) (SourceRecord, error) {
		cancel()
		return observeSource(i, "awaiting_octop_authorization"), nil
	}
	if _, err := VerifyWithSource(cancelCtx, proof, p, clock, cancelReader); err != ErrRejected {
		t.Fatal("cancelled context accepted")
	}
	before := calls
	_, _ = VerifyWithSource(cancelCtx, proof, p, clock, reader)
	_, _ = VerifyWithSource(ctx, []byte("invalid"), p, clock, reader)
	if calls != before {
		t.Fatal("invalid work reached source")
	}
}

func TestOpaqueIDsAndExplicitStableTargetMapping(t *testing.T) {
	i, p, key := fixture(t)
	i.Binding.Organization = "组织/运营 A"
	i.Binding.Employee = " 岗位:分析员 "
	p.Binding = i.Binding
	proof, err := Seal(i, p.KeyID, key, 100, 160)
	if err != nil {
		t.Fatal("legacy opaque ID rejected", err)
	}
	c, err := Verify(proof, p, 101)
	if err != nil || c.OctopOrganizationID != "target-org" || c.OctopEmployeeID != "target-worker" {
		t.Fatal("receiver mapping lost", err)
	}
	remapped := p
	remapped.OctopOrganizationID = "another-stable-org"
	remapped.OctopEmployeeID = "another-stable-worker"
	other, err := Verify(proof, remapped, 101)
	if err != nil || other.DeliveryKey != c.DeliveryKey || other.Fingerprint == c.Fingerprint {
		t.Fatal("target remapping did not conflict on the original delivery identity")
	}
	for _, change := range []func(*Policy){
		func(p *Policy) { p.Binding.Employee = strings.TrimSpace(p.Binding.Employee) },
		func(p *Policy) { p.OctopOrganizationID = "" },
		func(p *Policy) { p.OctopEmployeeID = "" },
	} {
		copy := p
		change(&copy)
		if _, err := Verify(proof, copy, 101); err != ErrRejected {
			t.Fatal("normalized or unmapped identity accepted")
		}
	}
	for _, id := range []string{strings.Repeat("界", 43), "\xff", "   "} {
		i.Binding.Employee = id
		if _, err := Seal(i, p.KeyID, key, 100, 160); err != ErrRejected {
			t.Fatal("invalid opaque ID accepted")
		}
	}
	// Unicode normalization is never implicit: visually identical keys differ.
	i.Binding.Employee = "é"
	p.Binding = i.Binding
	proof, _ = Seal(i, p.KeyID, key, 100, 160)
	p.Binding.Employee = "e\u0301"
	if _, err := Verify(proof, p, 101); err != ErrRejected {
		t.Fatal("normalization aliased stable identity")
	}
}

func observeSource(i Intent, state string) SourceRecord {
	return SourceRecord{Commitment: IntentCommitment(i), State: state, ReceiptExpiresAt: i.ReceiptExpiresAt, Version: 1}
}
