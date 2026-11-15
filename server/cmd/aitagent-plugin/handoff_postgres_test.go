//go:build offlinepg

// See LICENSE.txt in the repository root for license information.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "github.com/lib/pq"
	"github.com/mattermost/mattermost/server/v8/cmd/aitagent-plugin/internal/handoff"
)

func TestOfflinePGSourceToPendingInbox(t *testing.T) {
	if os.Getenv("AITAGENT_OFFLINE_PG") != "1" {
		t.Skip("isolated container harness only")
	}
	db, err := sql.Open("postgres", "host=/var/run/postgresql user=postgres dbname=postgres sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`DROP TABLE IF EXISTS aitagent_intake_audit,aitagent_intake,aitagent_test_receiver CASCADE;` + handoff.OfflineInboxSchema + `
 CREATE TABLE aitagent_test_receiver(actor bigint,org text,worker text,key_id text,enabled boolean);
 INSERT INTO aitagent_test_receiver VALUES(42,'target-org','target-worker','test-key',true);`)
	if err != nil {
		t.Fatal(err)
	}
	p, f, args := fixture()
	const now = int64(1800000000000)
	p.clock = func() int64 { return now }
	invoke(t, p, args)
	var r *receipt
	for _, item := range readLedger(t, f).Receipts {
		r = item
	}
	i, err := handoffDraft(r, f.config, args, "test-mm", "test-octop", now)
	if err != nil {
		t.Fatal(err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := handoff.Policy{Enabled: true, KeyID: "test-key", PublicKey: pub, Binding: i.Binding, OctopActorID: 42, OctopOrganizationID: "target-org", OctopEmployeeID: "target-worker"}
	proof, err := handoff.Seal(i, policy.KeyID, key, now/1000, now/1000+60)
	if err != nil {
		t.Fatal(err)
	}
	authorize := func(ctx context.Context, tx *sql.Tx, policy handoff.Policy, c handoff.Candidate) error {
		var enabled bool
		err := tx.QueryRowContext(ctx, `SELECT enabled FROM aitagent_test_receiver WHERE actor=$1 AND org=$2 AND worker=$3 AND key_id=$4 FOR SHARE`, c.OctopActorID, c.OctopOrganizationID, c.OctopEmployeeID, policy.KeyID).Scan(&enabled)
		if err != nil || !enabled {
			return errors.New("denied")
		}
		return nil
	}
	inbox := handoff.NewOfflineInbox(true, db, authorize)
	source := p.handoffSourceReader(sourceBinding{Enabled: true, Binding: i.Binding})
	accept := func() handoff.IntakeOutcome {
		return inbox.Accept(context.Background(), proof, policy, func() int64 { return now / 1000 }, source)
	}
	if got := accept(); got != handoff.IntakePending {
		t.Fatal(got)
	}
	if got := accept(); got != handoff.IntakeDuplicate {
		t.Fatal(got)
	}
	machine := handoff.NewOfflineStateMachine(true, db, func(ctx context.Context, tx *sql.Tx, p handoff.Policy, c handoff.Candidate, _ handoff.ReviewAction) error {
		return authorize(ctx, tx, p, c)
	})
	if got := machine.Review(context.Background(), proof, policy, 1, digest("authorize"), handoff.ReviewAuthorization, func() int64 { return now / 1000 }, source); got != handoff.ReviewRecorded {
		t.Fatal(got)
	}
	if got := machine.Review(context.Background(), proof, policy, 2, digest("execution-review"), handoff.ReviewExecution, func() int64 { return now / 1000 }, source); got != handoff.ReviewRecorded {
		t.Fatal(got)
	}
	f.permission = false
	if got := accept(); got != handoff.IntakeRejected {
		t.Fatal("source revocation ignored", got)
	}
	f.permission = true
	args.Command = "/aitagent cancel " + r.ID
	invoke(t, p, args)
	if got := machine.Review(context.Background(), proof, policy, 3, digest("cancelled-source"), handoff.ReviewAuthorization, func() int64 { return now / 1000 }, source); got != handoff.ReviewDenied {
		t.Fatal("source cancellation ignored", got)
	}
	candidate, err := handoff.Verify(proof, policy, now/1000)
	if err != nil {
		t.Fatal(err)
	}
	stored, outcome := machine.Inspect(context.Background(), candidate.DeliveryKey, policy)
	if outcome != handoff.ReviewRecorded || stored.State != "blocked" || stored.Revision != 4 {
		t.Fatal("reconciliation lost blocked state", stored, outcome)
	}
	if got := machine.Cancel(context.Background(), candidate.DeliveryKey, policy, 4, digest("local-cancel")); got != handoff.ReviewCancelled {
		t.Fatal(got)
	}
	for table, want := range map[string]int{"aitagent_intake": 1, "aitagent_intake_audit": 5} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != want {
			t.Fatal("duplicate or lost transaction", err, count)
		}
	}
}
