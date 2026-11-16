// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"testing"
)

func TestOutboxDefaultsClosed(t *testing.T) {
	for _, s := range []*Outbox{nil, {}, NewOfflineOutbox(false, nil, nil), NewOfflineOutbox(true, nil, nil)} {
		if o := s.Prepare(context.Background(), nil, Policy{}, nil, nil); o != OutboxDenied {
			t.Fatal(o)
		}
		if o := s.BeginAttempt(context.Background(), nil, Policy{}, nil, nil); o != OutboxDenied {
			t.Fatal(o)
		}
		if o := s.Reconcile(context.Background(), "", Policy{}, nil); o != OutboxDenied {
			t.Fatal(o)
		}
	}
}
