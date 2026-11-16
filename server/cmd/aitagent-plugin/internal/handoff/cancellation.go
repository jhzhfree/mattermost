// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"time"
)

// CancellationEvidence comes from an independently authenticated source adapter,
// not client JSON or a signed old proof. It carries no goal or execution grant.
type CancellationEvidence struct {
	// Original commitment survives source goal removal; required by Octop ingress.
	Commitment       string
	ReceiptExpiresAt int64
	DeliveryKey      string
	Fingerprint      string
	Version          uint64
	Cancelled        bool
}
type CancellationReader func(context.Context) (CancellationEvidence, error)

// PropagateCancellation records a local terminal fence only for an already
// retained exact identity. It cannot cancel an absent receipt, undo an external
// action or atomically acknowledge back to the source. No runtime calls it.
func (s *StateMachine) PropagateCancellation(ctx context.Context, key string, p Policy, expectedRevision int64, operationID string, read CancellationReader) ReviewOutcome {
	if !s.ready(ctx, p) || !validKey(key) || !validKey(operationID) || expectedRevision < 1 || expectedRevision > 8 || read == nil {
		return ReviewDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Authenticate locally before invoking the source, then release DB locks.
	tx, err := s.transaction(ctx)
	if err != nil {
		return ReviewUnavailable
	}
	c, _, err := loadState(ctx, tx, key)
	if err != nil {
		tx.Rollback()
		return ReviewUnavailable
	}
	if !targetMatches(c, p) || s.authorize(ctx, tx, p, c, ReviewCancel) != nil {
		tx.Rollback()
		return ReviewDenied
	}
	tx.Rollback()
	evidence, err := read(ctx)
	if err != nil || ctx.Err() != nil || !evidence.Cancelled || evidence.DeliveryKey != key || evidence.Fingerprint != c.Fingerprint || evidence.Version == 0 || evidence.Version > uint64(1<<63-1) {
		return ReviewDenied
	}
	tx, err = s.transaction(ctx)
	if err != nil {
		return ReviewUnavailable
	}
	defer tx.Rollback()
	current, state, err := loadState(ctx, tx, key)
	if err != nil {
		return ReviewUnavailable
	}
	if current.Fingerprint != c.Fingerprint || !targetMatches(current, p) || s.authorize(ctx, tx, p, current, ReviewCancel) != nil {
		return ReviewDenied
	}
	if state.State == "cancelled" {
		return ReviewCancelled
	}
	if state.State == "expired" {
		return ReviewExpired
	}
	if state.Revision != expectedRevision {
		return ReviewConflict
	}
	if evidence.Version <= state.SourceVersion {
		return ReviewDenied
	}
	if err = storeState(ctx, tx, current, state, "cancelled", evidence.Version, "source-cancel:"+operationID); err != nil {
		return ReviewUnavailable
	}
	if s.commit(tx) != nil {
		return ReviewUnknown
	}
	return ReviewCancelled
}
