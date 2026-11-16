// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

var (
	ErrSchedulerDisabled      = errors.New("offline cancellation scheduler disabled")
	ErrSchedulerConfiguration = errors.New("invalid offline scheduler configuration")
	ErrSchedulerRunning       = errors.New("scheduler already running")
)

// CancellationWork provides durable discovery and fenced retry, never arbitrary
// task execution. Outbox is the implementation; fake implementations test control.
type CancellationWork interface {
	DueCancellations(context.Context, Policy) ([]string, CancelQueueOutcome)
	RetryCancellation(context.Context, string, Policy, CancellationTarget) CancelQueueOutcome
}
type SchedulerEvent struct{ Outcome CancelQueueOutcome }
type SchedulerStats struct{ Passes, Attempts, Confirmed, Attention, Failures, DroppedEvents int64 }

// CancellationScheduler services ONE trusted policy scope, serially. It has no
// HTTP/client/credential/config hook and is instantiated only by isolated tests.
// Events are bounded, nonblocking local observations, not delivered alerts.
type CancellationScheduler struct {
	enabled                                                   bool
	work                                                      CancellationWork
	policy                                                    Policy
	target                                                    CancellationTarget
	interval                                                  time.Duration
	running                                                   atomic.Bool
	events                                                    chan SchedulerEvent
	passes, attempts, confirmed, attention, failures, dropped atomic.Int64
}

func NewOfflineCancellationScheduler(enabled bool, work CancellationWork, p Policy, target CancellationTarget, interval time.Duration) *CancellationScheduler {
	return &CancellationScheduler{enabled: enabled, work: work, policy: p, target: target, interval: interval, events: make(chan SchedulerEvent, 32)}
}

// Events is a cumulative, lossy local observation stream. It stays open across
// Run calls; consumers must stop on their own context. It is not a receipt.
func (s *CancellationScheduler) Events() <-chan SchedulerEvent { return s.events }
func (s *CancellationScheduler) Stats() SchedulerStats {
	return SchedulerStats{s.passes.Load(), s.attempts.Load(), s.confirmed.Load(), s.attention.Load(), s.failures.Load(), s.dropped.Load()}
}
func (s *CancellationScheduler) record(o CancelQueueOutcome) {
	switch o {
	case CancelConfirmed:
		s.confirmed.Add(1)
	case CancelAttention:
		s.attention.Add(1)
	case CancelQueued, CancelDeferred:
	default:
		s.failures.Add(1)
	}
	select {
	case s.events <- SchedulerEvent{Outcome: o}:
	default:
		s.dropped.Add(1)
	}
}

// Run polls only after the previous pass finishes. No overlapping goroutines or
// detached RPCs are created. DB methods have deadlines, but an uncooperative
// adapter can still block this worker until it returns; Run cannot preempt it.
func (s *CancellationScheduler) Run(ctx context.Context) error {
	if s == nil || !s.enabled {
		return ErrSchedulerDisabled
	}
	if ctx == nil || s.work == nil || !s.policy.Enabled || s.target.Inspect == nil || s.target.Cancel == nil || s.interval < time.Second || s.interval > time.Minute {
		return ErrSchedulerConfiguration
	}
	if !s.running.CompareAndSwap(false, true) {
		return ErrSchedulerRunning
	}
	defer s.running.Store(false)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.passes.Add(1)
		keys, outcome := s.work.DueCancellations(ctx, s.policy)
		if outcome != CancelQueued {
			s.record(outcome)
		} else if len(keys) > 16 {
			s.record(CancelQueueUnavailable)
		} else {
			for _, key := range keys {
				if ctx.Err() != nil {
					break
				}
				if !validKey(key) {
					s.record(CancelQueueUnavailable)
					continue
				}
				s.attempts.Add(1)
				s.record(s.work.RetryCancellation(ctx, key, s.policy, s.target))
			}
		}
		timer := time.NewTimer(s.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
