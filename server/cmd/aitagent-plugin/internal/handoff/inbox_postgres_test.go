//go:build offlinepg

// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

const offlineDSN = "host=/var/run/postgresql user=postgres dbname=postgres sslmode=disable"

func openOfflineDB(t *testing.T) *sql.DB {
	t.Helper()
	if os.Getenv("AITAGENT_OFFLINE_PG") != "1" {
		t.Skip("isolated container harness only")
	}
	db, err := sql.Open("postgres", offlineDSN)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	return db
}
func resetOfflineDB(t *testing.T) *sql.DB {
	db := openOfflineDB(t)
	if _, err := db.Exec(`DROP FUNCTION IF EXISTS aitagent_guard_row(),aitagent_require_audit(),aitagent_validate_event() CASCADE; DROP TABLE IF EXISTS aitagent_capacity_guard,aitagent_intake_audit,aitagent_intake,aitagent_test_grants CASCADE;` + OfflineInboxSchema + `
 CREATE TABLE aitagent_test_grants(org text,worker text,actor bigint,key_id text,active boolean,PRIMARY KEY(org,worker,actor));
 INSERT INTO aitagent_test_grants VALUES('target-org','target-worker',9,'test-key',true);`); err != nil {
		t.Fatal(err)
	}
	return db
}
func fixtureAuth(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
	var active bool
	var keyID string
	err := tx.QueryRowContext(ctx, `SELECT active,key_id FROM aitagent_test_grants WHERE org=$1 AND worker=$2 AND actor=$3 FOR SHARE`, c.OctopOrganizationID, c.OctopEmployeeID, c.OctopActorID).Scan(&active, &keyID)
	if err != nil || !active || keyID != p.KeyID {
		return errors.New("denied")
	}
	return nil
}
func countRows(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	for _, table := range []string{"aitagent_intake", "aitagent_intake_audit"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, n, want, err)
		}
	}
}

func TestOfflinePGWorker(t *testing.T) {
	file := os.Getenv("AITAGENT_PUBLIC_FIXTURE")
	if file == "" {
		t.Skip("child only")
	}
	db := openOfflineDB(t)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v wireVector
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	source := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(v.Expected.Intent, "awaiting_octop_authorization"), nil
	}
	result := NewOfflineInbox(true, db, fixtureAuth).Accept(context.Background(), v.Proof, v.Policy, func() int64 { return v.Now }, source)
	if result != IntakePending && result != IntakeDuplicate {
		t.Fatal(result)
	}
	fmt.Println("OUTCOME=" + string(result))
}

func TestOfflinePGConcurrentProcesses(t *testing.T) {
	db := resetOfflineDB(t)
	i, p, key := fixture(t)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	candidate, _ := Verify(proof, p, 101)
	raw, _ := json.Marshal(wireVector{Proof: proof, Policy: p, Now: 101, Expected: candidate})
	path := filepath.Join(t.TempDir(), "public-fixture.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	outcomes := make(chan string, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestOfflinePGWorker$")
			cmd.Env = append(os.Environ(), "AITAGENT_PUBLIC_FIXTURE="+path)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("child failed: %v %s", err, out)
			}
			outcomes <- string(out)
		}()
	}
	wg.Wait()
	close(outcomes)
	winners := 0
	for output := range outcomes {
		if strings.Contains(output, "OUTCOME=pending_authorization") {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	countRows(t, db, 1)
	// Independent pool simulates restart/reconnect; storage, not memory, dedupes.
	other := openOfflineDB(t)
	source := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(i, "awaiting_octop_authorization"), nil
	}
	if got := NewOfflineInbox(true, other, fixtureAuth).Accept(context.Background(), proof, p, func() int64 { return 101 }, source); got != IntakeDuplicate {
		t.Fatal(got)
	}
	var stored string
	if err := db.QueryRow("SELECT row_to_json(x)::text FROM aitagent_intake x").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, i.Goal) || strings.Contains(stored, "payload") || strings.Contains(stored, "signature") {
		t.Fatal("sensitive input persisted")
	}
}

func TestOfflinePGConflictRevocationAndRotation(t *testing.T) {
	db := resetOfflineDB(t)
	i, p, key := fixture(t)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	source := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(i, "awaiting_octop_authorization"), nil
	}
	inbox := NewOfflineInbox(true, db, fixtureAuth)
	accept := func(b []byte, p Policy) IntakeOutcome {
		return inbox.Accept(context.Background(), b, p, func() int64 { return 101 }, source)
	}
	if got := accept(proof, p); got != IntakePending {
		t.Fatal(got)
	}
	original := i
	i.Goal = "changed"
	changed, _ := Seal(i, p.KeyID, key, 100, 160)
	if got := accept(changed, p); got != IntakeConflict {
		t.Fatal(got)
	}
	i = original
	if _, err := db.Exec("UPDATE aitagent_test_grants SET active=false"); err != nil {
		t.Fatal(err)
	}
	if got := accept(proof, p); got != IntakeRejected {
		t.Fatal("duplicate bypassed current receiver rights", got)
	}
	if _, err := db.Exec("UPDATE aitagent_test_grants SET active=true,key_id='rotated'"); err != nil {
		t.Fatal(err)
	}
	if got := accept(proof, p); got != IntakeRejected {
		t.Fatal("old policy snapshot bypassed revoked key", got)
	}
	pub, key2, _ := ed25519.GenerateKey(rand.Reader)
	p.PublicKey = pub
	p.KeyID = "rotated"
	fresh, _ := Seal(i, p.KeyID, key2, 100, 160)
	if got := accept(fresh, p); got != IntakeDuplicate {
		t.Fatal(got)
	}
	cancelled := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(i, "cancelled"), nil
	}
	if got := inbox.Accept(context.Background(), fresh, p, func() int64 { return 101 }, cancelled); got != IntakeRejected {
		t.Fatal(got)
	}
	countRows(t, db, 1)
}

func TestOfflinePGAuditRollbackCommitUnknownAndLimits(t *testing.T) {
	db := resetOfflineDB(t)
	i, p, key := fixture(t)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	source := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(i, "awaiting_octop_authorization"), nil
	}
	inbox := NewOfflineInbox(true, db, fixtureAuth)
	accept := func() IntakeOutcome {
		return inbox.Accept(context.Background(), proof, p, func() int64 { return 101 }, source)
	}
	if _, err := db.Exec("ALTER TABLE aitagent_intake_audit ADD CONSTRAINT synthetic_failure CHECK(false) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if got := accept(); got != IntakeUnavailable {
		t.Fatal(got)
	}
	countRows(t, db, 0)
	if _, err := db.Exec("ALTER TABLE aitagent_intake_audit DROP CONSTRAINT synthetic_failure"); err != nil {
		t.Fatal(err)
	}
	inbox.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("synthetic lost acknowledgement")
	}
	if got := accept(); got != IntakeUnknown {
		t.Fatal(got)
	}
	countRows(t, db, 1)
	inbox.commit = func(tx *sql.Tx) error { return tx.Commit() }
	if got := accept(); got != IntakeDuplicate {
		t.Fatal(got)
	}
	countRows(t, db, 1)
	// A retained cap is deliberately not reset by source expiry or cancellation.
	for n := 0; n < 127; n++ {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO aitagent_intake(delivery_key,fingerprint,organization_id,employee_id,actor_id,source_expires_at,state) VALUES($1,$1,'synthetic','synthetic',1,1,'pending_authorization')`, fmt.Sprintf("%064x", n)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO aitagent_intake_audit(delivery_key,fingerprint,action) VALUES($1,$1,'pending_intake_recorded')`, fmt.Sprintf("%064x", n)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	i.Receipt = strings.Repeat("b", 64)
	proof, _ = Seal(i, p.KeyID, key, 100, 160)
	if got := accept(); got != IntakeFull {
		t.Fatal(got)
	}
	locker, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Rollback()
	if _, err = locker.Exec("LOCK TABLE aitagent_intake IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if got := accept(); got != IntakeUnavailable {
		t.Fatal(got)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("lock wait unbounded")
	}
}

func TestOfflinePGSourceCancellationAfterReadRemainsAGate(t *testing.T) {
	db := resetOfflineDB(t)
	i, p, key := fixture(t)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	cancelled := false
	source := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(i, "awaiting_octop_authorization"), nil
	}
	authorize := func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
		cancelled = true
		return fixtureAuth(ctx, tx, p, c)
	}
	got := NewOfflineInbox(true, db, authorize).Accept(context.Background(), proof, p, func() int64 { return 101 }, source)
	if !cancelled || got != IntakePending {
		t.Fatal("negative race scenario changed", got)
	}
	// This deliberately demonstrates the unresolved cross-service race. The row
	// is pending metadata only; no task/executor exists and production stays closed.
	countRows(t, db, 1)
}

func TestOfflinePGExpiryDuringAuthorizationRollsBack(t *testing.T) {
	db := resetOfflineDB(t)
	i, p, key := fixture(t)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	now := int64(101)
	source := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(i, "awaiting_octop_authorization"), nil
	}
	authorize := func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
		now = 160
		return fixtureAuth(ctx, tx, p, c)
	}
	if got := NewOfflineInbox(true, db, authorize).Accept(context.Background(), proof, p, func() int64 { return now }, source); got != IntakeRejected {
		t.Fatal(got)
	}
	countRows(t, db, 0)
}
