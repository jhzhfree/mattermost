// See LICENSE.txt in the repository root for license information.

// Package handoff implements an offline, proposed request-proof boundary.
// It has no transport, key discovery, persistence, or execution capability.
// This is not an implemented Octop API or an authorization protocol.
package handoff

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const MaxProofBytes = 32768
const domain = "mattermost-octop-intent-proof/v1\x00"

var ErrRejected = errors.New("request proof rejected")

// Binding must come from receiver-owned configuration, never the proof itself.
// A matching source account is not proof of a human at the keyboard.
type Binding struct {
	Instance     string `json:"instance"`
	Audience     string `json:"audience"`
	Organization string `json:"organization"`
	Employee     string `json:"employee"`
	User         string `json:"user"`
	Team         string `json:"team"`
	Channel      string `json:"channel"`
	Bot          string `json:"bot"`
}

type Intent struct {
	Binding Binding `json:"binding"`
	Receipt string  `json:"receipt"`
	Goal    string  `json:"goal"`
	Mode    string  `json:"mode"`
}

type claims struct {
	Version int    `json:"version"`
	KeyID   string `json:"key_id"`
	Issued  int64  `json:"issued"`
	Expires int64  `json:"expires"`
	Intent  Intent `json:"intent"`
}

type envelope struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

// Policy is trusted local receiver state. The mapped actor is deliberately not
// supplied by the sender. Current organization/Agent permissions still need an
// independent, transactionally consistent check before accepting any task.
type Policy struct {
	Enabled      bool
	KeyID        string
	PublicKey    ed25519.PublicKey
	Binding      Binding
	OctopActorID int64
}

// Candidate is evidence of a signed intent only. It is not a task or approval.
// The receiver must atomically persist DeliveryKey+Fingerprint with its pending
// intake, audit and authorization checks. Verify itself does not consume a key.
type Candidate struct {
	Intent       Intent
	OctopActorID int64
	DeliveryKey  string
	Fingerprint  string
}

func token(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func mmID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func validIntent(i Intent) bool {
	if len(i.Receipt) != 64 || len(i.Goal) > 8000 {
		return false
	}
	b := i.Binding
	r, err := hex.DecodeString(i.Receipt)
	return token(b.Instance, 128) && token(b.Audience, 128) && token(b.Organization, 128) && token(b.Employee, 128) &&
		mmID(b.User) && mmID(b.Team) && mmID(b.Channel) && mmID(b.Bot) && b.User != b.Bot &&
		err == nil && len(r) == 32 && hex.EncodeToString(r) == i.Receipt &&
		i.Mode == "user_requested" && utf8.ValidString(i.Goal) && strings.TrimSpace(i.Goal) != "" && utf8.RuneCountInString(i.Goal) <= 2000
}

// Seal signs an intent using an explicitly supplied key. No keys are generated,
// loaded from files/environment, or retained by this package. Only a trusted
// issuer may construct Intent from a live authorized receipt. Do not expose this
// function as a caller-controlled signing endpoint. No runtime caller exists.
func Seal(i Intent, keyID string, key ed25519.PrivateKey, issued, expires int64) ([]byte, error) {
	if !validIntent(i) || !token(keyID, 128) || len(key) != ed25519.PrivateKeySize || issued <= 0 || expires <= issued || expires-issued > 120 {
		return nil, ErrRejected
	}
	payload, err := json.Marshal(claims{1, keyID, issued, expires, i})
	if err != nil {
		return nil, ErrRejected
	}
	return json.Marshal(envelope{payload, ed25519.Sign(key, append([]byte(domain), payload...))})
}

// canonical rejects duplicate/unknown keys, alternate encodings and trailing
// content. The proposed wire representation is exactly Go's JSON encoding of
// these ordered structs (including HTML escaping); another implementation must
// reproduce it and pass cross-language vectors before interoperability claims.
func canonical(data []byte, into any) bool {
	if json.Unmarshal(data, into) != nil {
		return false
	}
	encoded, err := json.Marshal(into)
	return err == nil && bytes.Equal(data, encoded)
}

// Verify is deterministic and side-effect free. now is receiver-owned UTC Unix
// seconds, never sender input. Replay yields the same candidate: durable replay
// handling and business idempotency are NOT provided by this function.
func Verify(data []byte, p Policy, now int64) (Candidate, error) {
	var e envelope
	var c claims
	if !p.Enabled || p.OctopActorID <= 0 || len(p.PublicKey) != ed25519.PublicKeySize || len(data) > MaxProofBytes ||
		!canonical(data, &e) || !canonical(e.Payload, &c) || c.Version != 1 || !token(c.KeyID, 128) || c.KeyID != p.KeyID ||
		c.Issued <= 0 || c.Issued > now || c.Expires <= now || c.Expires <= c.Issued || c.Expires-c.Issued > 120 ||
		!validIntent(c.Intent) || c.Intent.Binding != p.Binding ||
		!ed25519.Verify(p.PublicKey, append([]byte(domain), e.Payload...), e.Signature) {
		return Candidate{}, ErrRejected
	}
	intentJSON, _ := json.Marshal(struct {
		Intent Intent
		Actor  int64
	}{c.Intent, p.OctopActorID})
	fingerprint := sha256.Sum256(intentJSON)
	// Tuple encoding prevents concatenation ambiguity. Key rotation and fresh
	// proof timestamps cannot turn a retry of one receipt into a new delivery.
	identity, _ := json.Marshal([]string{"mattermost-octop-delivery/v1", c.Intent.Binding.Instance, c.Intent.Binding.Audience, c.Intent.Receipt})
	delivery := sha256.Sum256(identity)
	return Candidate{c.Intent, p.OctopActorID, hex.EncodeToString(delivery[:]), hex.EncodeToString(fingerprint[:])}, nil
}
