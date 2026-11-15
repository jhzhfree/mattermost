// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"testing"
)

func TestLifecycleDefaultsClosed(t *testing.T) {
	for _, s := range []*Lifecycle{nil, {}, NewOfflineLifecycle(false, nil), NewOfflineLifecycle(true, nil)} {
		for _, archive := range []bool{false, true} {
			if n, o := s.Run(context.Background(), archive); n != 0 || o != IntakeRejected {
				t.Fatal(n, o)
			}
		}
	}
}
