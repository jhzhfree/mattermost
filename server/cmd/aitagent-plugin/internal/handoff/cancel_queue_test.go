// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"testing"
)

func TestCancelQueueDefaultsClosed(t *testing.T) {
	for _, s := range []*Outbox{nil, {}, NewOfflineOutbox(false, nil, nil)} {
		if o := s.QueueCancellation(context.Background(), "", Policy{}, nil); o != CancelQueueDenied {
			t.Fatal(o)
		}
		if o := s.RetryCancellation(context.Background(), "", Policy{}, CancellationTarget{}); o != CancelQueueDenied {
			t.Fatal(o)
		}
	}
}
