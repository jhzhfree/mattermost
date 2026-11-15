// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"database/sql"
	"testing"
)

func TestInboxDefaultsAndMissingAuthorizerStayClosed(t *testing.T) {
	source := func(context.Context, Binding, string) (SourceRecord, error) {
		t.Fatal("closed inbox called source")
		return SourceRecord{}, nil
	}
	for _, inbox := range []*Inbox{nil, {}, NewOfflineInbox(false, &sql.DB{}, nil), NewOfflineInbox(true, &sql.DB{}, nil)} {
		if got := inbox.Accept(context.Background(), nil, Policy{}, func() int64 { return 100 }, source); got != IntakeRejected {
			t.Fatal(got)
		}
	}
}
