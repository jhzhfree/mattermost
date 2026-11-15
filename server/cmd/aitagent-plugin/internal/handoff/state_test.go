// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestStateMachineDefaultAndInvalidRequestsStayClosed(t *testing.T) {
	_, p, _ := fixture(t)
	source := func(context.Context, Binding, string) (SourceRecord, error) {
		t.Fatal("closed state machine reached source")
		return SourceRecord{}, nil
	}
	for _, machine := range []*StateMachine{nil, {}, NewOfflineStateMachine(false, &sql.DB{}, nil), NewOfflineStateMachine(true, &sql.DB{}, nil)} {
		if got := machine.Review(context.Background(), nil, p, 1, strings.Repeat("a", 64), ReviewAuthorization, func() int64 { return 100 }, source); got != ReviewDenied {
			t.Fatal(got)
		}
		if _, got := machine.Inspect(context.Background(), strings.Repeat("a", 64), p); got != ReviewDenied {
			t.Fatal(got)
		}
		if got := machine.Cancel(context.Background(), strings.Repeat("a", 64), p, 1, strings.Repeat("b", 64)); got != ReviewDenied {
			t.Fatal(got)
		}
	}
	if validKey(strings.Repeat("a", 100000)) || validKey(strings.Repeat("A", 64)) {
		t.Fatal("unbounded or noncanonical operation ID accepted")
	}
}
