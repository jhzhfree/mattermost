// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"strings"
	"testing"
)

func TestCancellationPropagationDefaultsClosed(t *testing.T) {
	called := false
	read := func(context.Context) (CancellationEvidence, error) { called = true; return CancellationEvidence{}, nil }
	for _, s := range []*StateMachine{nil, {}, NewOfflineStateMachine(false, nil, nil)} {
		if o := s.PropagateCancellation(context.Background(), strings.Repeat("a", 64), Policy{}, 1, strings.Repeat("b", 64), read); o != ReviewDenied {
			t.Fatal(o)
		}
	}
	if called {
		t.Fatal("disabled path queried source")
	}
}
