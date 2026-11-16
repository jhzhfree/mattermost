// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"time"
)

// OfflineOutboxSchema has no runtime installer or source-ledger integration.
//
//go:embed outbox.sql
var OfflineOutboxSchema string

type OutboxOutcome string

const (
	OutboxPrepared    OutboxOutcome = "prepared_no_delivery"
	OutboxAttempt     OutboxOutcome = "attempt_recorded_no_execution"
	OutboxUnresolved  OutboxOutcome = "delivery_unresolved_do_not_resend"
	OutboxRecorded    OutboxOutcome = "receiver_record_confirmed_no_execution"
	OutboxDenied      OutboxOutcome = "outbox_denied"
	OutboxUnavailable OutboxOutcome = "outbox_unavailable"
	OutboxUnknown     OutboxOutcome = "outbox_commit_unknown_do_not_send"
	OutboxFull        OutboxOutcome = "outbox_identity_capacity_reached"
)

// OutboxAuthorizer requires a trusted local caller and current sender permission
// and trust, locked in the transaction. Mapped actor IDs are not login evidence.
// It must not commit, mutate rows or perform external actions. Fixture only.
type OutboxAuthorizer func(context.Context, *sql.Tx, Policy, Candidate) error

// ReceiverInspector must authenticate to the receiver independently of the
// expired proof/source. A missing/unavailable response never proves absence.
type ReceiverInspector func(context.Context, string, Policy) (StoredState, ReviewOutcome)

// Outbox persists only identity/target metadata, never goal, proof or keys.
// There is no sender, scheduler, credentials, endpoint or execution capability.
type Outbox struct {
	enabled   bool
	db        *sql.DB
	authorize OutboxAuthorizer
	commit    func(*sql.Tx) error
}

func NewOfflineOutbox(enabled bool, db *sql.DB, auth OutboxAuthorizer) *Outbox {
	return &Outbox{enabled, db, auth, func(tx *sql.Tx) error { return tx.Commit() }}
}
func (s *Outbox) ready(ctx context.Context, p Policy) bool {
	return s != nil && s.enabled && s.db != nil && s.authorize != nil && ctx != nil && ctx.Err() == nil && p.Enabled && p.OctopActorID > 0 && opaqueID(p.OctopOrganizationID) && opaqueID(p.OctopEmployeeID)
}
func (s *Outbox) transaction(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='1000ms'"); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func loadOutbox(ctx context.Context, tx *sql.Tx, key string) (Candidate, string, error) {
	c := Candidate{DeliveryKey: key}
	var phase string
	err := tx.QueryRowContext(ctx, `SELECT fingerprint,organization_id,employee_id,actor_id,source_expires_at,phase FROM aitagent_outbox WHERE delivery_key=$1 FOR UPDATE`, key).Scan(&c.Fingerprint, &c.OctopOrganizationID, &c.OctopEmployeeID, &c.OctopActorID, &c.Intent.ReceiptExpiresAt, &phase)
	if err != nil {
		return c, phase, err
	}
	var count, last int
	var matching bool
	err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(max(revision),0),coalesce(bool_and(phase=CASE revision WHEN 1 THEN 'prepared' WHEN 2 THEN 'delivery_unknown' WHEN 3 THEN 'receiver_recorded' END),false) FROM aitagent_outbox_audit WHERE delivery_key=$1`, key).Scan(&count, &last, &matching)
	expected := map[string]int{"prepared": 1, "delivery_unknown": 2, "receiver_recorded": 3}[phase]
	if err == nil && (count != expected || last != expected || !matching) {
		err = errors.New("incomplete outbox history")
	}
	return c, phase, err
}
func outboxEvent(ctx context.Context, tx *sql.Tx, key, phase string, revision int) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO aitagent_outbox_audit(delivery_key,revision,phase) VALUES($1,$2,$3)", key, revision, phase)
	return err
}

// Prepare verifies live source evidence before durable identity reservation.
// This transaction is NOT atomic with creation/cancellation of the MM receipt.
func (s *Outbox) Prepare(ctx context.Context, proof []byte, p Policy, now func() int64, source SourceReader) OutboxOutcome {
	if !s.ready(ctx, p) || now == nil {
		return OutboxDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	last := now()
	rewound := false
	checked := func() int64 {
		n := now()
		if n < last {
			rewound = true
		}
		last = n
		return n
	}
	c, _, err := observeVerified(ctx, proof, p, checked, source)
	if err != nil {
		return OutboxDenied
	}
	tx, err := s.transaction(ctx)
	if err != nil {
		return OutboxUnavailable
	}
	defer tx.Rollback()
	if s.authorize(ctx, tx, p, c) != nil {
		return OutboxDenied
	}
	var guard bool
	if err = tx.QueryRowContext(ctx, "SELECT id FROM aitagent_outbox_guard WHERE id=true FOR UPDATE").Scan(&guard); err != nil || !guard {
		return OutboxUnavailable
	}
	old, phase, err := loadOutbox(ctx, tx, c.DeliveryKey)
	if err == nil {
		if old.Fingerprint != c.Fingerprint {
			return OutboxDenied
		}
		if phase == "prepared" {
			return OutboxPrepared
		}
		if phase == "receiver_recorded" {
			return OutboxRecorded
		}
		return OutboxUnresolved
	}
	if err != sql.ErrNoRows {
		return OutboxUnavailable
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aitagent_outbox").Scan(&count); err != nil {
		return OutboxUnavailable
	}
	if count >= 128 {
		return OutboxFull
	}
	end := checked()
	if _, err = Verify(proof, p, end); err != nil || rewound {
		return OutboxDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO aitagent_outbox(delivery_key,fingerprint,organization_id,employee_id,actor_id,source_expires_at,phase,revision) VALUES($1,$2,$3,$4,$5,$6,'prepared',1)`, c.DeliveryKey, c.Fingerprint, c.OctopOrganizationID, c.OctopEmployeeID, c.OctopActorID, c.Intent.ReceiptExpiresAt)
	if err != nil || outboxEvent(ctx, tx, c.DeliveryKey, "prepared", 1) != nil {
		return OutboxUnavailable
	}
	if _, err = Verify(proof, p, checked()); err != nil || rewound || ctx.Err() != nil {
		return OutboxDenied
	}
	if s.commit(tx) != nil {
		return OutboxUnknown
	}
	return OutboxPrepared
}

// BeginAttempt durably fences one isolated delivery invocation before it can
// occur. Only OutboxAttempt permits the TEST caller to invoke its local receiver.
// An unknown commit or a preexisting unknown phase never permits another call.
// A crash between this commit and the call sacrifices liveness, not identity.
func (s *Outbox) BeginAttempt(ctx context.Context, proof []byte, p Policy, now func() int64, source SourceReader) OutboxOutcome {
	if !s.ready(ctx, p) || now == nil {
		return OutboxDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	last := now()
	rewound := false
	checked := func() int64 {
		n := now()
		if n < last {
			rewound = true
		}
		last = n
		return n
	}
	c, _, err := observeVerified(ctx, proof, p, checked, source)
	if err != nil {
		return OutboxDenied
	}
	tx, err := s.transaction(ctx)
	if err != nil {
		return OutboxUnavailable
	}
	defer tx.Rollback()
	old, phase, err := loadOutbox(ctx, tx, c.DeliveryKey)
	if err != nil {
		return OutboxUnavailable
	}
	if old.Fingerprint != c.Fingerprint || !targetMatches(old, p) || s.authorize(ctx, tx, p, old) != nil {
		return OutboxDenied
	}
	if phase == "receiver_recorded" {
		return OutboxRecorded
	}
	if phase != "prepared" {
		return OutboxUnresolved
	}
	end := checked()
	if _, err = Verify(proof, p, end); err != nil || rewound {
		return OutboxDenied
	}
	if _, err = tx.ExecContext(ctx, "UPDATE aitagent_outbox SET phase='delivery_unknown',revision=2 WHERE delivery_key=$1", c.DeliveryKey); err != nil || outboxEvent(ctx, tx, c.DeliveryKey, "delivery_unknown", 2) != nil {
		return OutboxUnavailable
	}
	if _, err = Verify(proof, p, checked()); err != nil || rewound || ctx.Err() != nil {
		return OutboxDenied
	}
	if s.commit(tx) != nil {
		return OutboxUnknown
	}
	return OutboxAttempt
}

// Reconcile proves only historical receipt existence for this exact fingerprint.
// It requires fresh local authorization both before and after remote observation.
// It intentionally does not require a still-valid proof or an available source.
func (s *Outbox) Reconcile(ctx context.Context, key string, p Policy, inspect ReceiverInspector) OutboxOutcome {
	if !s.ready(ctx, p) || !validKey(key) || inspect == nil {
		return OutboxDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Never hold DB locks across the callback.
	tx, err := s.transaction(ctx)
	if err != nil {
		return OutboxUnavailable
	}
	c, phase, err := loadOutbox(ctx, tx, key)
	if err != nil {
		tx.Rollback()
		return OutboxUnavailable
	}
	if !targetMatches(c, p) || s.authorize(ctx, tx, p, c) != nil {
		tx.Rollback()
		return OutboxDenied
	}
	tx.Rollback()
	if phase == "prepared" {
		return OutboxDenied
	}
	observed, outcome := inspect(ctx, key, p)
	if outcome != ReviewRecorded {
		return OutboxUnresolved
	}
	if observed.DeliveryKey != key || observed.Fingerprint != c.Fingerprint || observed.Revision < 1 || observed.Revision > 8 {
		return OutboxDenied
	}
	switch observed.State {
	case "pending_authorization", "authorization_observed", "execution_observed", "blocked", "cancelled", "expired":
	default:
		return OutboxDenied
	}
	tx, err = s.transaction(ctx)
	if err != nil {
		return OutboxUnavailable
	}
	defer tx.Rollback()
	current, phase, err := loadOutbox(ctx, tx, key)
	if err != nil {
		return OutboxUnavailable
	}
	if current.Fingerprint != c.Fingerprint || !targetMatches(current, p) || s.authorize(ctx, tx, p, current) != nil {
		return OutboxDenied
	}
	if phase == "receiver_recorded" {
		return OutboxRecorded
	}
	if phase != "delivery_unknown" {
		return OutboxDenied
	}
	if _, err = tx.ExecContext(ctx, "UPDATE aitagent_outbox SET phase='receiver_recorded',revision=3 WHERE delivery_key=$1", key); err != nil || outboxEvent(ctx, tx, key, "receiver_recorded", 3) != nil {
		return OutboxUnavailable
	}
	if s.commit(tx) != nil {
		return OutboxUnknown
	}
	return OutboxRecorded
}
