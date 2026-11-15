// See LICENSE.txt in the repository root for license information.

package handoff

import (
	"context"
	"database/sql"
	"encoding/hex"
	"strconv"
	"time"
)

type ReviewAction string

const (
	ReviewAuthorization ReviewAction = "authorization_observed"
	ReviewExecution     ReviewAction = "execution_observed"
	ReviewInspect       ReviewAction = "inspect"
	ReviewCancel        ReviewAction = "cancelled"
)

type ReviewOutcome string

const (
	ReviewDenied      ReviewOutcome = "denied_no_execution"
	ReviewRecorded    ReviewOutcome = "observation_recorded_no_execution"
	ReviewDuplicate   ReviewOutcome = "already_recorded_no_execution"
	ReviewConflict    ReviewOutcome = "revision_or_operation_conflict"
	ReviewUnavailable ReviewOutcome = "unavailable_no_execution"
	ReviewUnknown     ReviewOutcome = "commit_unknown_no_execution"
	ReviewCancelled   ReviewOutcome = "cancelled"
)

// StoredState is historical metadata only. None of its states is a runnable
// permission, approval, lease, or token. Inspect is independent of source expiry.
type StoredState struct {
	State         string
	Revision      int64
	SourceVersion uint64
	Events        []StoredEvent // populated only by independently authorized Inspect
}
type StoredEvent struct {
	Action      string
	Revision    int64
	OperationID string
}

// StateAuthorizer must lock/recheck current receiver permissions and trust for
// this specific action. Inspect/Cancel receive row-owned target IDs and no goal
// or sender intent. No permissive/default/production implementation is provided.
// It must only read/lock, not commit, roll back or perform external actions.
// Independent Inspect/Cancel authorization must use an authenticated LOCAL
// caller captured by trusted context/closure. Policy's mapped actor is a resource
// owner, NOT evidence that the caller is that person. Never deserialize Policy
// from a request or accept actor IDs as login credentials.
type StateAuthorizer func(context.Context, *sql.Tx, Policy, Candidate, ReviewAction) error
type StateMachine struct {
	enabled   bool
	db        *sql.DB
	authorize StateAuthorizer
	commit    func(*sql.Tx) error
}

func NewOfflineStateMachine(enabled bool, db *sql.DB, authorize StateAuthorizer) *StateMachine {
	return &StateMachine{enabled: enabled, db: db, authorize: authorize, commit: func(tx *sql.Tx) error { return tx.Commit() }}
}
func validKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(b) == s
}
func (s *StateMachine) ready(ctx context.Context, p Policy) bool {
	return s != nil && s.enabled && s.db != nil && s.authorize != nil && ctx != nil && ctx.Err() == nil && p.Enabled && p.OctopActorID > 0 && opaqueID(p.OctopOrganizationID) && opaqueID(p.OctopEmployeeID)
}
func (s *StateMachine) transaction(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='1000ms'"); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func loadState(ctx context.Context, tx *sql.Tx, key string) (Candidate, StoredState, error) {
	c := Candidate{DeliveryKey: key}
	var state StoredState
	err := tx.QueryRowContext(ctx, `SELECT fingerprint,organization_id,employee_id,actor_id,source_expires_at,state,revision,source_version FROM aitagent_intake WHERE delivery_key=$1 FOR UPDATE`, key).Scan(&c.Fingerprint, &c.OctopOrganizationID, &c.OctopEmployeeID, &c.OctopActorID, &c.Intent.ReceiptExpiresAt, &state.State, &state.Revision, &state.SourceVersion)
	return c, state, err
}
func targetMatches(c Candidate, p Policy) bool {
	return c.OctopOrganizationID == p.OctopOrganizationID && c.OctopEmployeeID == p.OctopEmployeeID && c.OctopActorID == p.OctopActorID
}

// storeState writes state/version and its audit atomically. Observation history
// is bounded at revision 7, reserving revision 8 for terminal local cancellation.
func storeState(ctx context.Context, tx *sql.Tx, c Candidate, old StoredState, next string, sourceVersion uint64, op string) error {
	result, err := tx.ExecContext(ctx, `UPDATE aitagent_intake SET state=$1,revision=revision+1,source_version=$2 WHERE delivery_key=$3 AND revision=$4 AND state<>'cancelled'`, next, int64(sourceVersion), c.DeliveryKey, old.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRejected
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO aitagent_intake_audit(delivery_key,fingerprint,action,revision,operation_id) VALUES($1,$2,$3,$4,$5)`, c.DeliveryKey, c.Fingerprint, next, old.Revision+1, op)
	return err
}

// Review rechecks source state, a newer confirmed ledger revision, proof expiry,
// exact target and action-specific local permissions on EVERY attempt, including
// retries. Execution review requires a prior authorization observation, but does
// not consume or return execution authority. There is intentionally no executor.
// Cancellation after the last remote observation can still race this local
// transaction; callers must not use this result as a transferable permission.
func (s *StateMachine) Review(ctx context.Context, proof []byte, p Policy, expectedRevision int64, operationID string, action ReviewAction, now func() int64, source SourceReader) ReviewOutcome {
	if !s.ready(ctx, p) || now == nil || !validKey(operationID) || expectedRevision < 1 || expectedRevision > 8 || (action != ReviewAuthorization && action != ReviewExecution) {
		return ReviewDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	last := now()
	rewound := false
	checkedNow := func() int64 {
		at := now()
		if at < last {
			rewound = true
		}
		last = at
		return at
	}
	candidate, err := Verify(proof, p, last)
	if err != nil {
		return ReviewDenied
	}
	_, observation, sourceErr := observeVerified(ctx, proof, p, checkedNow, source)
	if ctx.Err() != nil {
		return ReviewDenied
	}
	current := func() bool {
		at := checkedNow()
		_, err := Verify(proof, p, at)
		return !rewound && err == nil && ctx.Err() == nil
	}
	tx, err := s.transaction(ctx)
	if err != nil {
		return ReviewUnavailable
	}
	defer tx.Rollback()
	stored, state, err := loadState(ctx, tx, candidate.DeliveryKey)
	if err != nil {
		return ReviewUnavailable
	}
	if !targetMatches(stored, p) || stored.Fingerprint != candidate.Fingerprint {
		return ReviewDenied
	}
	if s.authorize(ctx, tx, p, candidate, action) != nil {
		return ReviewDenied
	}
	if state.State == "cancelled" {
		return ReviewDenied
	}
	// Failure never returns an observation. When possible persist a blocked state
	// as additional evidence; failure to persist it still denies the operation.
	if sourceErr != nil || observation.Version <= state.SourceVersion || !current() {
		if state.Revision != expectedRevision {
			return ReviewConflict
		}
		if state.State == "blocked" || state.Revision >= 7 {
			return ReviewDenied
		}
		if err := storeState(ctx, tx, stored, state, "blocked", state.SourceVersion, "blocked:"+operationID+":"+strconv.FormatInt(state.Revision, 10)); err != nil {
			return ReviewUnavailable
		}
		if err := s.commit(tx); err != nil {
			return ReviewUnknown
		}
		return ReviewDenied
	}
	var previousAction string
	err = tx.QueryRowContext(ctx, `SELECT action FROM aitagent_intake_audit WHERE delivery_key=$1 AND operation_id=$2`, candidate.DeliveryKey, operationID).Scan(&previousAction)
	if err == nil {
		if previousAction != string(action) {
			return ReviewConflict
		}
		// A prior observation cannot recover a subsequently blocked state.
		if state.State == "blocked" || !current() {
			return ReviewDenied
		}
		return ReviewDuplicate
	}
	if err != sql.ErrNoRows {
		return ReviewUnavailable
	}
	if state.Revision != expectedRevision {
		return ReviewConflict
	}
	if state.Revision >= 7 {
		return ReviewDenied
	}
	if action == ReviewExecution && state.State != "authorization_observed" {
		return ReviewDenied
	}
	if err = storeState(ctx, tx, stored, state, string(action), observation.Version, operationID); err != nil {
		return ReviewUnavailable
	}
	if !current() {
		return ReviewDenied
	}
	if err = s.commit(tx); err != nil {
		return ReviewUnknown
	}
	return ReviewRecorded
}

// Inspect provides independently authorized reconciliation using the original
// delivery key, even after proof/source expiry/cancellation. It reports stored
// history only, never current source permission. Error/missing is not proof of
// non-commit: reconcile on the primary with appropriate operator authorization.
func (s *StateMachine) Inspect(ctx context.Context, key string, p Policy) (StoredState, ReviewOutcome) {
	if !s.ready(ctx, p) || !validKey(key) {
		return StoredState{}, ReviewDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.transaction(ctx)
	if err != nil {
		return StoredState{}, ReviewUnavailable
	}
	defer tx.Rollback()
	c, state, err := loadState(ctx, tx, key)
	if err != nil {
		return StoredState{}, ReviewUnavailable
	}
	if !targetMatches(c, p) || s.authorize(ctx, tx, p, c, ReviewInspect) != nil {
		return StoredState{}, ReviewDenied
	}
	rows, err := tx.QueryContext(ctx, `SELECT action,revision,operation_id,fingerprint FROM aitagent_intake_audit WHERE delivery_key=$1 ORDER BY revision LIMIT 9`, key)
	if err != nil {
		return StoredState{}, ReviewUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var event StoredEvent
		var fingerprint string
		if err := rows.Scan(&event.Action, &event.Revision, &event.OperationID, &fingerprint); err != nil || fingerprint != c.Fingerprint || event.Revision != int64(len(state.Events)+1) || len(state.Events) >= 8 {
			return StoredState{}, ReviewUnavailable
		}
		state.Events = append(state.Events, event)
	}
	if rows.Err() != nil || len(state.Events) != int(state.Revision) {
		return StoredState{}, ReviewUnavailable
	}
	return state, ReviewRecorded
}

// Cancel is a local terminal fence independent of the source/proof. It requires
// current local action permission and exact mapped target; it neither cancels
// the source receipt nor any external operation. No transition revives this row.
func (s *StateMachine) Cancel(ctx context.Context, key string, p Policy, expectedRevision int64, operationID string) ReviewOutcome {
	if !s.ready(ctx, p) || !validKey(key) || !validKey(operationID) || expectedRevision < 1 || expectedRevision > 8 {
		return ReviewDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.transaction(ctx)
	if err != nil {
		return ReviewUnavailable
	}
	defer tx.Rollback()
	c, state, err := loadState(ctx, tx, key)
	if err != nil {
		return ReviewUnavailable
	}
	if !targetMatches(c, p) || s.authorize(ctx, tx, p, c, ReviewCancel) != nil {
		return ReviewDenied
	}
	if state.State == "cancelled" {
		return ReviewCancelled
	}
	if state.Revision != expectedRevision {
		return ReviewConflict
	}
	if err = storeState(ctx, tx, c, state, "cancelled", state.SourceVersion, "cancel:"+operationID); err != nil {
		return ReviewUnavailable
	}
	if err = s.commit(tx); err != nil {
		return ReviewUnknown
	}
	return ReviewCancelled
}
