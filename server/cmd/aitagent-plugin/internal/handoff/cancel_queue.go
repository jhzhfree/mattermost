// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"time"
)

// OfflineCancelQueueSchema is installed in disposable tests only.
//
//go:embed cancel_queue.sql
var OfflineCancelQueueSchema string

// CancellationAuthorizer checks explicit queue/propagation/confirmation rights
// for a trusted local caller and target, not merely request-intake permission.
// It follows ReceiverAuthorizer's read/lock-only transaction contract.
type CancellationAuthorizer func(context.Context, *sql.Tx, Policy, Candidate) error

// NewOfflineOutboxWithCancellation requires two independent authorization
// adapters. The existing NewOfflineOutbox grants no cancellation queue access.
func NewOfflineOutboxWithCancellation(enabled bool, db *sql.DB, intake OutboxAuthorizer, cancel CancellationAuthorizer) *Outbox {
	s := NewOfflineOutbox(enabled, db, intake)
	s.cancelAuthorize = cancel
	return s
}

type CancelQueueOutcome string

const (
	CancelQueued           CancelQueueOutcome = "cancel_pending"
	CancelConfirmed        CancelQueueOutcome = "receiver_terminal_confirmed"
	CancelAttention        CancelQueueOutcome = "cancel_requires_attention"
	CancelDeferred         CancelQueueOutcome = "cancel_not_due_or_claimed"
	CancelQueueDenied      CancelQueueOutcome = "cancel_queue_denied"
	CancelQueueUnavailable CancelQueueOutcome = "cancel_queue_unavailable"
	CancelQueueUnknown     CancelQueueOutcome = "cancel_queue_commit_unknown"
)

// CancellationTarget is an independently authenticated, cooperative adapter.
// Cancel MUST call current source/receiver authorization (as PropagateCancellation
// does); queued evidence alone must never authorize it. No network adapter exists.
type CancellationTarget struct {
	Inspect ReceiverInspector
	Cancel  func(context.Context, string, Policy, int64, string) ReviewOutcome
}

func queueEvent(ctx context.Context, tx *sql.Tx, key string, attempt int, event string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO aitagent_cancel_queue_audit(delivery_key,attempt,event) VALUES($1,$2,$3)", key, attempt, event)
	return err
}

// QueueCancellation retains a verified observation as audit metadata, not as a
// reusable permission. The immutable sender identity must already be durable.
func (s *Outbox) QueueCancellation(ctx context.Context, key string, p Policy, read CancellationReader) CancelQueueOutcome {
	if !s.ready(ctx, p) || s.cancelAuthorize == nil || !validKey(key) || read == nil {
		return CancelQueueDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.transaction(ctx)
	if err != nil {
		return CancelQueueUnavailable
	}
	c, phase, err := loadOutbox(ctx, tx, key)
	if err != nil {
		tx.Rollback()
		return CancelQueueUnavailable
	}
	if phase == "prepared" || !targetMatches(c, p) || s.cancelAuthorize(ctx, tx, p, c) != nil {
		tx.Rollback()
		return CancelQueueDenied
	}
	tx.Rollback()
	e, err := read(ctx)
	if err != nil || ctx.Err() != nil || !e.Cancelled || e.DeliveryKey != key || e.Fingerprint != c.Fingerprint || e.Version == 0 || e.Version > uint64(1<<63-1) {
		return CancelQueueDenied
	}
	tx, err = s.transaction(ctx)
	if err != nil {
		return CancelQueueUnavailable
	}
	defer tx.Rollback()
	current, phase, err := loadOutbox(ctx, tx, key)
	if err != nil {
		return CancelQueueUnavailable
	}
	if phase == "prepared" || current.Fingerprint != c.Fingerprint || !targetMatches(current, p) || s.cancelAuthorize(ctx, tx, p, current) != nil {
		return CancelQueueDenied
	}
	var existing, fp string
	err = tx.QueryRowContext(ctx, "SELECT phase,fingerprint FROM aitagent_cancel_queue WHERE delivery_key=$1", key).Scan(&existing, &fp)
	if err == nil {
		if !queueHistoryOK(ctx, tx, key) {
			return CancelQueueUnavailable
		}
		if fp != c.Fingerprint {
			return CancelQueueDenied
		}
		switch existing {
		case "confirmed":
			return CancelConfirmed
		case "attention":
			return CancelAttention
		case "pending":
			return CancelQueued
		}
		return CancelQueueUnavailable
	}
	if err != sql.ErrNoRows {
		return CancelQueueUnavailable
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO aitagent_cancel_queue(delivery_key,fingerprint,observed_source_version,phase) VALUES($1,$2,$3,'pending')", key, c.Fingerprint, int64(e.Version))
	if err != nil || queueEvent(ctx, tx, key, 0, "queued") != nil {
		return CancelQueueUnavailable
	}
	if s.commit(tx) != nil {
		return CancelQueueUnknown
	}
	return CancelQueued
}

// RetryCancellation performs ONE bounded due attempt. The lease is committed
// before any receiver call; unknown claim commits never authorize a call. There
// is no scheduler or goroutine here. A new process can resume the persisted key
// after lease expiry. Terminal confirmation is historical, never execution proof.
func (s *Outbox) RetryCancellation(ctx context.Context, key string, p Policy, target CancellationTarget) CancelQueueOutcome {
	if !s.ready(ctx, p) || s.cancelAuthorize == nil || !validKey(key) || target.Inspect == nil || target.Cancel == nil {
		return CancelQueueDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return CancelQueueUnavailable
	}
	token := hex.EncodeToString(tokenBytes[:])
	tx, err := s.transaction(ctx)
	if err != nil {
		return CancelQueueUnavailable
	}
	c, _, err := loadOutbox(ctx, tx, key)
	if err != nil {
		tx.Rollback()
		return CancelQueueUnavailable
	}
	if !targetMatches(c, p) || s.cancelAuthorize(ctx, tx, p, c) != nil {
		tx.Rollback()
		return CancelQueueDenied
	}
	var phase, fp string
	var attempts int
	var due bool
	err = tx.QueryRowContext(ctx, `SELECT phase,fingerprint,attempts,next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()) FROM aitagent_cancel_queue WHERE delivery_key=$1 FOR UPDATE`, key).Scan(&phase, &fp, &attempts, &due)
	if err != nil {
		tx.Rollback()
		return CancelQueueUnavailable
	}
	if !queueHistoryOK(ctx, tx, key) {
		tx.Rollback()
		return CancelQueueUnavailable
	}
	if fp != c.Fingerprint {
		tx.Rollback()
		return CancelQueueDenied
	}
	if phase == "confirmed" {
		tx.Rollback()
		return CancelConfirmed
	}
	if phase == "attention" {
		tx.Rollback()
		return CancelAttention
	}
	if !due {
		tx.Rollback()
		return CancelDeferred
	}
	// A crash on the last claim still reaches a durable, non-success terminal
	// attention state after its lease expires, instead of retrying without bound.
	if attempts == 16 {
		_, err = tx.ExecContext(ctx, "UPDATE aitagent_cancel_queue SET phase='attention',lease_token=NULL,lease_until=NULL WHERE delivery_key=$1", key)
		if err != nil || queueEvent(ctx, tx, key, attempts, "attention") != nil {
			tx.Rollback()
			return CancelQueueUnavailable
		}
		if s.commit(tx) != nil {
			tx.Rollback()
			return CancelQueueUnknown
		}
		return CancelAttention
	}
	attempts++
	_, err = tx.ExecContext(ctx, "UPDATE aitagent_cancel_queue SET attempts=$2,lease_token=$3,lease_until=clock_timestamp()+interval '5 seconds' WHERE delivery_key=$1", key, attempts, token)
	if err != nil || queueEvent(ctx, tx, key, attempts, "claimed") != nil {
		tx.Rollback()
		return CancelQueueUnavailable
	}
	if s.commit(tx) != nil {
		tx.Rollback()
		return CancelQueueUnknown
	}
	// No locks across callbacks. A lease cannot forcibly interrupt a callback;
	// overlapping late attempts are fenced at acknowledgement and receiver state.
	terminal := ""
	check := func() (StoredState, bool) {
		r, o := target.Inspect(ctx, key, p)
		valid := o == ReviewRecorded && r.DeliveryKey == key && r.Fingerprint == c.Fingerprint && r.Revision >= 1 && r.Revision <= 8
		return r, valid
	}
	r, valid := check()
	if valid && (r.State == "cancelled" || r.State == "expired") {
		terminal = r.State
	} else if valid && ctx.Err() == nil {
		hash := sha256.Sum256([]byte("aitagent-cancel-retry/v1\x00" + key + "\x00" + c.Fingerprint))
		target.Cancel(ctx, key, p, r.Revision, hex.EncodeToString(hash[:])) // response alone cannot confirm
		if ctx.Err() == nil {
			r, valid = check()
			if valid && (r.State == "cancelled" || r.State == "expired") {
				terminal = r.State
			}
		}
	}
	if ctx.Err() != nil {
		return CancelQueueUnavailable
	} // retained lease enables later recovery
	tx, err = s.transaction(ctx)
	if err != nil {
		return CancelQueueUnavailable
	}
	defer tx.Rollback()
	current, _, err := loadOutbox(ctx, tx, key)
	if err != nil {
		return CancelQueueUnavailable
	}
	if current.Fingerprint != c.Fingerprint || !targetMatches(current, p) || s.cancelAuthorize(ctx, tx, p, current) != nil {
		return CancelQueueDenied
	}
	if !queueHistoryOK(ctx, tx, key) {
		return CancelQueueUnavailable
	}
	phase = "pending"
	event := "retry"
	result := CancelQueued
	if terminal != "" {
		phase = "confirmed"
		event = "confirmed"
		result = CancelConfirmed
	} else if attempts == 16 {
		phase = "attention"
		event = "attention"
		result = CancelAttention
	}
	delay := 1 << min(attempts-1, 6)
	if delay > 60 {
		delay = 60
	}
	update, err := tx.ExecContext(ctx, `UPDATE aitagent_cancel_queue SET phase=$4,lease_token=NULL,lease_until=NULL,next_attempt_at=clock_timestamp()+$5*interval '1 second',receiver_terminal=NULLIF($6,'') WHERE delivery_key=$1 AND phase='pending' AND attempts=$2 AND lease_token=$3 AND lease_until>clock_timestamp()`, key, attempts, token, phase, delay, terminal)
	if err != nil {
		return CancelQueueUnavailable
	}
	n, err := update.RowsAffected()
	if err != nil || n != 1 {
		return CancelDeferred
	}
	if queueEvent(ctx, tx, key, attempts, event) != nil {
		return CancelQueueUnavailable
	}
	if s.commit(tx) != nil {
		return CancelQueueUnknown
	}
	return result
}

// DueCancellations returns at most 16 authorized identity hints in a pinned
// target scope, allowing a restarted worker to discover durable work. It grants
// no claim or execution permission; RetryCancellation must authorize and claim
// each key again. There is no global tenant enumeration or polling loop here.
func (s *Outbox) DueCancellations(ctx context.Context, p Policy) ([]string, CancelQueueOutcome) {
	if !s.ready(ctx, p) || s.cancelAuthorize == nil {
		return nil, CancelQueueDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.transaction(ctx)
	if err != nil {
		return nil, CancelQueueUnavailable
	}
	defer tx.Rollback()
	// An empty delivery key requests explicit scope-discovery permission, even
	// if the scope currently has no due rows. Per-key checks follow below.
	scope := Candidate{OctopOrganizationID: p.OctopOrganizationID, OctopEmployeeID: p.OctopEmployeeID, OctopActorID: p.OctopActorID}
	if s.cancelAuthorize(ctx, tx, p, scope) != nil {
		return nil, CancelQueueDenied
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.delivery_key,o.fingerprint,o.organization_id,o.employee_id,o.actor_id,o.source_expires_at FROM aitagent_cancel_queue q JOIN aitagent_outbox o ON o.delivery_key=q.delivery_key AND o.fingerprint=q.fingerprint WHERE q.phase='pending' AND q.next_attempt_at<=clock_timestamp() AND (q.lease_until IS NULL OR q.lease_until<=clock_timestamp()) AND o.organization_id=$1 AND o.employee_id=$2 AND o.actor_id=$3 ORDER BY o.delivery_key LIMIT 16`, p.OctopOrganizationID, p.OctopEmployeeID, p.OctopActorID)
	if err != nil {
		return nil, CancelQueueUnavailable
	}
	var candidates []Candidate
	for rows.Next() {
		var c Candidate
		if err = rows.Scan(&c.DeliveryKey, &c.Fingerprint, &c.OctopOrganizationID, &c.OctopEmployeeID, &c.OctopActorID, &c.Intent.ReceiptExpiresAt); err != nil {
			rows.Close()
			return nil, CancelQueueUnavailable
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, CancelQueueUnavailable
	}
	keys := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if s.cancelAuthorize(ctx, tx, p, c) != nil {
			return nil, CancelQueueDenied
		}
		keys = append(keys, c.DeliveryKey)
	}
	return keys, CancelQueued
}

// Each attempt must have its durable claim. A missing older acknowledgement can
// also mean a crashed worker, so this is not owner-resistant audit validation.
func queueHistoryOK(ctx context.Context, tx *sql.Tx, key string) bool {
	var phase string
	var attempts int
	var leased bool
	if err := tx.QueryRowContext(ctx, "SELECT phase,attempts,lease_token IS NOT NULL FROM aitagent_cancel_queue WHERE delivery_key=$1", key).Scan(&phase, &attempts, &leased); err != nil || attempts < 0 || attempts > 16 {
		return false
	}
	rows, err := tx.QueryContext(ctx, "SELECT attempt,event FROM aitagent_cancel_queue_audit WHERE delivery_key=$1 LIMIT 34", key)
	if err != nil {
		return false
	}
	defer rows.Close()
	claims := map[int]bool{}
	acks := map[int]string{}
	queued := false
	count := 0
	for rows.Next() {
		var a int
		var event string
		if rows.Scan(&a, &event) != nil {
			return false
		}
		count++
		if count > 33 || a < 0 || a > attempts {
			return false
		}
		if a == 0 {
			if event != "queued" || queued {
				return false
			}
			queued = true
			continue
		}
		if event == "claimed" {
			if claims[a] {
				return false
			}
			claims[a] = true
			continue
		}
		if event != "retry" && event != "confirmed" && event != "attention" {
			return false
		}
		if acks[a] != "" {
			return false
		}
		acks[a] = event
	}
	if rows.Err() != nil || !queued {
		return false
	}
	for a := 1; a <= attempts; a++ {
		if !claims[a] {
			return false
		}
		if a < attempts && (acks[a] == "confirmed" || acks[a] == "attention") {
			return false
		}
	}
	if attempts == 0 {
		return phase == "pending" && !leased
	}
	if leased {
		return phase == "pending" && acks[attempts] == ""
	}
	if phase == "pending" {
		return acks[attempts] == "retry"
	}
	return (phase == "confirmed" || phase == "attention") && acks[attempts] == phase
}
