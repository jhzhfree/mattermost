// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"database/sql"
	_ "embed"
	"time"
)

// OfflineLifecycleSchema is installed explicitly in disposable fixtures only.
// A distinct maintenance role gets EXECUTE on two bounded owner-defined funcs.
// No scheduler, production migration, credentials or connection is installed.
//
//go:embed lifecycle.sql
var OfflineLifecycleSchema string

type Lifecycle struct {
	enabled bool
	db      *sql.DB
	commit  func(*sql.Tx) error
}

func NewOfflineLifecycle(enabled bool, db *sql.DB) *Lifecycle {
	return &Lifecycle{enabled, db, func(tx *sql.Tx) error { return tx.Commit() }}
}

// Run expires or archives eligible records using DATABASE time and fixed policy.
// Unknown commit returns no count. Retrying never deletes a key or revives work;
// reconcile original keys to distinguish historical results after unknown commits.
func (s *Lifecycle) Run(ctx context.Context, archive bool) (int, IntakeOutcome) {
	if s == nil || !s.enabled || s.db == nil || ctx == nil || ctx.Err() != nil {
		return 0, IntakeRejected
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, IntakeUnavailable
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='1000ms'"); err != nil {
		return 0, IntakeUnavailable
	}
	query := "SELECT public.aitagent_expire_batch()"
	if archive {
		query = "SELECT public.aitagent_archive_batch()"
	}
	var count int
	if err = tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, IntakeUnavailable
	}
	if count < 0 || count > 32 {
		return 0, IntakeUnavailable
	}
	if err = s.commit(tx); err != nil {
		return 0, IntakeUnknown
	}
	return count, IntakeOutcome("maintenance_recorded")
}
