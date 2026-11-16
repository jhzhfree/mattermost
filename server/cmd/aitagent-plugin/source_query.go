// See LICENSE.txt in the repository root for license information.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

const sourceQueryPath = "/api/integrations/octop/v1/source-observations"
const sourceQueryDomain = "octop-mattermost-source-query/v1\x00"
const sourceResponseDomain = "mattermost-octop-source-observation/v1\x00"

// Only trusted offline composition supplies this value. No plugin setting, HTTP
// route, key loader or production constructor enables source queries. A registry
// must retain revocations and a restore watermark outside the source DB before
// deployment; this value alone cannot detect rollback of the entire trust store.
type sourceQueryTrust struct {
	Enabled                        bool
	Broker, KeyID                  string
	Revision, MinimumVersion       uint64
	Binding                        handoff.Binding
	BrokerKey, ProofKey, SourceKey [32]byte
}
type sourceQueryBoundary struct {
	plugin  *Plugin
	resolve func(context.Context) (sourceQueryTrust, error)
	sign    func(context.Context, []byte) ([]byte, error)
}
type sourceQuery struct {
	ProtocolVersion int             `json:"protocol_version"`
	Broker          string          `json:"broker_id"`
	Nonce           string          `json:"nonce"`
	IssuedAt        int64           `json:"issued_at"`
	Binding         handoff.Binding `json:"binding"`
	Receipt         string          `json:"receipt"`
}
type sourceWireObservation struct {
	DeliveryKey      string `json:"delivery_key"`
	Fingerprint      string `json:"fingerprint"`
	Commitment       string `json:"commitment"`
	State            string `json:"state"`
	ReceiptExpiresAt int64  `json:"receipt_expires_at"`
	Version          string `json:"version"`
}

// query is an offline wire handler, NOT ServeHTTP. RPC cancellation is cooperative:
// existing budgetAPI keeps occupied RPC slots until completion; no abandoned
// goroutine is spawned to manufacture a hard timeout.
func (s *sourceQueryBoundary) query(ctx context.Context, method, path, contentType, broker, signature string, raw []byte) ([]byte, error) {
	deny := func() ([]byte, error) { return nil, handoff.ErrRejected }
	if ctx == nil || ctx.Err() != nil || s == nil || s.plugin == nil || s.resolve == nil || s.sign == nil || method != "POST" || path != sourceQueryPath || contentType != "application/json" || len(raw) > 4096 {
		return deny()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	trust, err := s.resolve(ctx)
	if err != nil || !trust.Enabled || trust.Revision == 0 || trust.BrokerKey == [32]byte{} || trust.ProofKey == [32]byte{} || trust.SourceKey == [32]byte{} || !sourceToken(trust.Broker) || !sourceToken(trust.KeyID) || trust.SourceKey == trust.BrokerKey || trust.SourceKey == trust.ProofKey || trust.BrokerKey == trust.ProofKey || broker != trust.Broker {
		return deny()
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(signature)
	if err != nil || base64.StdEncoding.EncodeToString(sig) != signature || !ed25519.Verify(trust.BrokerKey[:], append([]byte(sourceQueryDomain), raw...), sig) {
		return deny()
	}
	if !strictSourceQuery(raw) {
		return deny()
	}
	var q sourceQuery
	if json.Unmarshal(raw, &q) != nil || q.ProtocolVersion != 1 || q.Broker != trust.Broker || q.Binding != trust.Binding || !validReceiptID(q.Nonce) || !validReceiptID(q.Receipt) || q.IssuedAt <= 0 || q.IssuedAt > math.MaxInt64/1000-5 {
		return deny()
	}
	started := s.plugin.nowMillis() / 1000
	if started < q.IssuedAt || started >= q.IssuedAt+5 {
		return deny()
	}
	binding := sourceBinding{Enabled: true, Binding: trust.Binding}
	var observation sourceWireObservation
	_, _, _, err = s.plugin.withHandoffReceipt(ctx, binding, q.Binding, q.Receipt, func(r *receipt, c configuration, args *model.CommandArgs, at int64, l *ledger) error {
		if at/1000 < started || at/1000 >= q.IssuedAt+5 || l.Revision < trust.MinimumVersion || l.Revision >= math.MaxInt64 || r.Journal == nil || r.Journal.Commitment == "" || r.Journal.BindingDigest != journalBinding(trust.Binding) {
			return handoff.ErrRejected
		}
		if r.State == receiptState {
			intent, e := handoffDraft(r, c, args, trust.Binding.Instance, trust.Binding.Audience, at)
			if e != nil || handoff.IntentCommitment(intent) != r.Journal.Commitment {
				return handoff.ErrRejected
			}
		}
		nonce := digest(trust.Broker + ":" + q.Nonce)
		if l.SourceNonces == nil {
			l.SourceNonces = map[string]int64{}
		}
		for n, expiry := range l.SourceNonces {
			if expiry <= at {
				delete(l.SourceNonces, n)
			}
		}
		if _, used := l.SourceNonces[nonce]; used || len(l.SourceNonces) >= 128 {
			return handoff.ErrRejected
		}
		l.SourceNonces[nonce] = (q.IssuedAt + 5) * 1000
		observation = sourceWireObservation{r.Journal.DeliveryKey, r.Journal.Fingerprint, r.Journal.Commitment, r.State, r.ExpiresAt, strconv.FormatUint(l.Revision+1, 10)}
		return nil
	})
	if err != nil || ctx.Err() != nil {
		return deny()
	}
	current, err := s.resolve(ctx)
	now := s.plugin.nowMillis() / 1000
	if err != nil || current != trust || ctx.Err() != nil || now < started || now >= q.IssuedAt+5 {
		return deny()
	}
	payload, err := json.Marshal(struct {
		ProtocolVersion int                   `json:"protocol_version"`
		RequestSHA256   string                `json:"request_sha256"`
		ObservedAt      int64                 `json:"observed_at"`
		ExpiresAt       int64                 `json:"expires_at"`
		Observation     sourceWireObservation `json:"observation"`
	}{1, digest(string(raw)), now, q.IssuedAt + 5, observation})
	if err != nil {
		return deny()
	}
	message := append([]byte(sourceResponseDomain), payload...)
	signed, err := s.sign(ctx, message)
	final, resolveErr := s.resolve(ctx)
	end := s.plugin.nowMillis() / 1000
	if err != nil || resolveErr != nil || final != trust || ctx.Err() != nil || end < now || end >= q.IssuedAt+5 || !ed25519.Verify(trust.SourceKey[:], message, signed) {
		return deny()
	}
	return json.Marshal(struct {
		KeyID     string `json:"key_id"`
		Payload   []byte `json:"payload"`
		Signature []byte `json:"signature"`
	}{trust.KeyID, payload, signed})
}

// Exact field names and duplicate rejection without requiring Go canonical JSON.
func strictSourceQuery(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var object func(map[string]bool) bool
	object = func(allowed map[string]bool) bool {
		t, e := d.Token()
		if e != nil || t != json.Delim('{') {
			return false
		}
		seen := map[string]bool{}
		for d.More() {
			t, e = d.Token()
			key, ok := t.(string)
			if e != nil || !ok || !allowed[key] || seen[key] {
				return false
			}
			seen[key] = true
			if key == "binding" {
				if !object(map[string]bool{"instance": true, "audience": true, "organization": true, "employee": true, "user": true, "team": true, "channel": true, "bot": true}) {
					return false
				}
			} else {
				var v json.RawMessage
				if d.Decode(&v) != nil {
					return false
				}
			}
		}
		t, e = d.Token()
		return e == nil && t == json.Delim('}') && len(seen) == len(allowed)
	}
	if !object(map[string]bool{"protocol_version": true, "broker_id": true, "nonce": true, "issued_at": true, "binding": true, "receipt": true}) {
		return false
	}
	_, e := d.Token()
	return e == io.EOF
}

func sourceToken(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
