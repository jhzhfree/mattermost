// See LICENSE.txt in the repository root for license information.

package handoff

import (
	"context"
	"database/sql"
	_ "embed"
	"time"
)

// OfflineInboxSchema is applied explicitly by the isolated harness only. There
// is no automatic migration or live Octop DB integration.
//
//go:embed inbox.sql
var OfflineInboxSchema string

type IntakeOutcome string

const (
	IntakeRejected    IntakeOutcome = "rejected"
	IntakeUnavailable IntakeOutcome = "unavailable"
	IntakeUnknown     IntakeOutcome = "commit_unknown"
	IntakeConflict    IntakeOutcome = "conflict"
	IntakeFull        IntakeOutcome = "retained_capacity_reached"
	IntakePending     IntakeOutcome = "pending_authorization"
	IntakeDuplicate   IntakeOutcome = "duplicate_retained_intake"
)

// ReceiverAuthorizer must lock/recheck current receiver trust, mapping, account,
// organization and Agent rights in this transaction, including on duplicates.
// This package deliberately has no permissive default or production adapter.
// The adapter must perform only authorization reads/locks: it must not commit,
// roll back, mutate intake, or perform external actions inside this callback.
// Do not log the candidate: its untrusted goal is transient input, not DB data.
type ReceiverAuthorizer func(context.Context, *sql.Tx, Policy, Candidate) error

// Inbox is a standalone PostgreSQL pending-intake kernel. Zero value/disabled
// instances fail before source/DB calls. Only the isolated harness constructs it.
// It has no execution, sending, cleanup, SQL schema installation or HTTP path.
type Inbox struct {
	enabled   bool
	db        *sql.DB
	authorize ReceiverAuthorizer
	commit    func(*sql.Tx) error // private fault-injection seam; normal path is tx.Commit
}

func NewOfflineInbox(enabled bool, db *sql.DB, authorize ReceiverAuthorizer) *Inbox {
	return &Inbox{enabled: enabled, db: db, authorize: authorize, commit: func(tx *sql.Tx) error { return tx.Commit() }}
}

// Accept records only pending metadata plus audit. Source confirmation precedes
// the DB transaction: this does NOT serialize source cancellation with receiver
// commit. No result authorizes execution. The caller must reconcile unknown
// commits with the original identity; this method never retries a commit.
func (s *Inbox) Accept(ctx context.Context, proof []byte, policy Policy, now func() int64, source SourceReader) IntakeOutcome {
	if s == nil || !s.enabled || s.db == nil || s.authorize == nil || ctx == nil || ctx.Err() != nil || now == nil {
		return IntakeRejected
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	lastCheck := now()
	rewound := false
	checkedNow := func() int64 {
		next := now()
		if next < lastCheck {
			rewound = true
		}
		lastCheck = next
		return next
	}
	current := func() bool {
		next := checkedNow()
		_, err := Verify(proof, policy, next)
		return !rewound && err == nil && ctx.Err() == nil
	}
	candidate, observation, err := observeVerified(ctx, proof, policy, checkedNow, source)
	if err != nil {
		return IntakeRejected
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return IntakeUnavailable
	}
	defer tx.Rollback()
	// SQL deadlines bound lock/statement waits independently of process admission.
	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout = '250ms'"); err != nil {
		return IntakeUnavailable
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL statement_timeout = '1000ms'"); err != nil {
		return IntakeUnavailable
	}
	if err = s.authorize(ctx, tx, policy, candidate); err != nil {
		return IntakeRejected
	}
	// One table lock deliberately serializes this small offline inbox, including
	// the retained-row cap. It is not a scalable or tenant-fair queue. Bounded SQL
	// waits fail closed; no expired record is deleted to reopen replay admission.
	if _, err = tx.ExecContext(ctx, "LOCK TABLE aitagent_intake IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return IntakeUnavailable
	}
	if !current() {
		return IntakeRejected
	}
	var fingerprint, state string
	var sourceVersion int64
	err = tx.QueryRowContext(ctx, "SELECT fingerprint,state,source_version FROM aitagent_intake WHERE delivery_key=$1", candidate.DeliveryKey).Scan(&fingerprint, &state, &sourceVersion)
	if err == nil {
		if !current() {
			return IntakeRejected
		}
		if state == "cancelled" || observation.Version < uint64(sourceVersion) {
			return IntakeRejected
		}
		if fingerprint != candidate.Fingerprint {
			return IntakeConflict
		}
		return IntakeDuplicate // no mutation; deferred rollback releases locks
	}
	if err != sql.ErrNoRows {
		return IntakeUnavailable
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aitagent_intake").Scan(&count); err != nil {
		return IntakeUnavailable
	}
	if count >= 128 {
		return IntakeFull
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO aitagent_intake(delivery_key,fingerprint,organization_id,employee_id,actor_id,source_expires_at,state,source_version) VALUES($1,$2,$3,$4,$5,$6,'pending_authorization',$7)`, candidate.DeliveryKey, candidate.Fingerprint, candidate.OctopOrganizationID, candidate.OctopEmployeeID, candidate.OctopActorID, candidate.Intent.ReceiptExpiresAt, int64(observation.Version))
	if err != nil {
		return IntakeUnavailable
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO aitagent_intake_audit(delivery_key,fingerprint,action) VALUES($1,$2,'pending_intake_recorded')`, candidate.DeliveryKey, candidate.Fingerprint)
	if err != nil {
		return IntakeUnavailable
	}
	if !current() {
		return IntakeRejected
	}
	// A commit error may mean the server committed and its acknowledgement was
	// lost. Never label it absent, delete the row, or create another identity.
	if err = s.commit(tx); err != nil {
		return IntakeUnknown
	}
	return IntakePending
}
