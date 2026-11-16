// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type schedulerFake struct {
	due   func(context.Context) ([]string, CancelQueueOutcome)
	retry func(context.Context) CancelQueueOutcome
}

func (f schedulerFake) DueCancellations(ctx context.Context, _ Policy) ([]string, CancelQueueOutcome) {
	return f.due(ctx)
}
func (f schedulerFake) RetryCancellation(ctx context.Context, _ string, _ Policy, _ CancellationTarget) CancelQueueOutcome {
	return f.retry(ctx)
}
func schedulerTarget() CancellationTarget {
	return CancellationTarget{Inspect: func(context.Context, string, Policy) (StoredState, ReviewOutcome) {
		return StoredState{}, ReviewUnavailable
	}, Cancel: func(context.Context, string, Policy, int64, string) ReviewOutcome { return ReviewDenied }}
}
func TestSchedulerClosedAndBoundedConfiguration(t *testing.T) {
	var calls atomic.Int32
	work := schedulerFake{due: func(context.Context) ([]string, CancelQueueOutcome) { calls.Add(1); return nil, CancelQueued }}
	for _, s := range []*CancellationScheduler{nil, {}, NewOfflineCancellationScheduler(false, work, Policy{Enabled: true}, schedulerTarget(), time.Second)} {
		if err := s.Run(context.Background()); err != ErrSchedulerDisabled {
			t.Fatal(err)
		}
	}
	s := NewOfflineCancellationScheduler(true, work, Policy{Enabled: true}, schedulerTarget(), time.Millisecond)
	if err := s.Run(context.Background()); err != ErrSchedulerConfiguration || calls.Load() != 0 {
		t.Fatal(err, calls.Load())
	}
}
func TestSchedulerSerialProgressAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	work := schedulerFake{due: func(context.Context) ([]string, CancelQueueOutcome) {
		return []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}, CancelQueued
	}, retry: func(context.Context) CancelQueueOutcome { calls++; cancel(); return CancelConfirmed }}
	s := NewOfflineCancellationScheduler(true, work, Policy{Enabled: true}, schedulerTarget(), time.Second)
	if err := s.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 1 || s.Stats().Confirmed != 1 || s.Stats().Attempts != 1 {
		t.Fatal(calls, s.Stats())
	}
}
func TestSchedulerRejectsConcurrentRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	work := schedulerFake{due: func(ctx context.Context) ([]string, CancelQueueOutcome) {
		close(entered)
		<-ctx.Done()
		return nil, CancelQueueUnavailable
	}}
	s := NewOfflineCancellationScheduler(true, work, Policy{Enabled: true}, schedulerTarget(), time.Second)
	go func() { done <- s.Run(ctx) }()
	<-entered
	if err := s.Run(ctx); err != ErrSchedulerRunning {
		t.Fatal(err)
	}
	cancel()
	<-done
}
