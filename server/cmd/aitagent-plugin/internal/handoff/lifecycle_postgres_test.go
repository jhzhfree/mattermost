//go:build offlinepg

// See LICENSE.txt in the repository root for license information.
package handoff

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"testing"
)

//go:embed testdata/roles.sql
var offlineRoles string

type lifecycleFixture struct {
	admin, app, reader, maintenance *sql.DB
	i                               Intent
	p                               Policy
	proof                           []byte
	candidate                       Candidate
	source                          SourceReader
	inbox                           *Inbox
	state, inspect                  *StateMachine
	lifecycle                       *Lifecycle
}

func roleDB(t *testing.T, role string) *sql.DB {
	t.Helper()
	switch role {
	case "aitagent_app", "aitagent_reader", "aitagent_maintenance":
	default:
		t.Fatal("invalid fixture role")
	}
	db, err := sql.Open("postgres", strings.Replace(offlineDSN, "user=postgres", "user="+role, 1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var current string
	var super bool
	if err = db.QueryRow("SELECT current_user,(SELECT rolsuper FROM pg_roles WHERE rolname=current_user)").Scan(&current, &super); err != nil || current != role || super {
		t.Fatal("not a direct non-superuser session", current, super, err)
	}
	return db
}
func roleAuth(ctx context.Context, tx *sql.Tx, p Policy, c Candidate, action ReviewAction) error {
	var allowed bool
	err := tx.QueryRowContext(ctx, "SELECT public.aitagent_fixture_authorized($1,$2,$3,$4,$5)", c.OctopOrganizationID, c.OctopEmployeeID, c.OctopActorID, p.KeyID, string(action)).Scan(&allowed)
	if err != nil || !allowed {
		return errors.New("fixture authorization denied")
	}
	return nil
}
func lifecycleSetup(t *testing.T) *lifecycleFixture {
	admin := resetOfflineDB(t)
	if _, err := admin.Exec(OfflineLifecycleSchema + offlineRoles); err != nil {
		t.Fatal(err)
	}
	i, p, key := fixture(t)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	candidate, _ := Verify(proof, p, 101)
	f := &lifecycleFixture{admin: admin, app: roleDB(t, "aitagent_app"), reader: roleDB(t, "aitagent_reader"), maintenance: roleDB(t, "aitagent_maintenance"), i: i, p: p, proof: proof, candidate: candidate}
	f.source = func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(i, "awaiting_octop_authorization"), nil
	}
	f.inbox = NewOfflineInbox(true, f.app, func(ctx context.Context, tx *sql.Tx, p Policy, c Candidate) error {
		return roleAuth(ctx, tx, p, c, ReviewAction("intake"))
	})
	f.state = NewOfflineStateMachine(true, f.app, roleAuth)
	f.inspect = NewOfflineStateMachine(true, f.reader, roleAuth)
	f.lifecycle = NewOfflineLifecycle(true, f.maintenance)
	return f
}
func (f *lifecycleFixture) accept() IntakeOutcome {
	return f.inbox.Accept(context.Background(), f.proof, f.p, func() int64 { return 101 }, f.source)
}
func (f *lifecycleFixture) view(t *testing.T) StoredState {
	t.Helper()
	s, o := f.inspect.Inspect(context.Background(), f.candidate.DeliveryKey, f.p)
	if o != ReviewRecorded {
		t.Fatal(o)
	}
	return s
}
func maintenance(t *testing.T, f *lifecycleFixture, archive bool, want int) {
	t.Helper()
	n, o := f.lifecycle.Run(context.Background(), archive)
	if o != IntakeOutcome("maintenance_recorded") || n != want {
		t.Fatal(n, o, want)
	}
}
func ageTerminals(t *testing.T, f *lifecycleFixture) {
	t.Helper()
	_, err := f.admin.Exec("BEGIN; SET LOCAL ROLE aitagent_owner; UPDATE aitagent_intake SET terminal_at=clock_timestamp()-interval '31 days' WHERE state IN ('cancelled','expired'); COMMIT")
	if err != nil {
		t.Fatal(err)
	}
}
func deniedSQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err == nil {
		t.Fatal("unexpected privilege or constraint bypass", query)
	}
}

func TestOfflinePGLifecycleMinimalRolesAndRetention(t *testing.T) {
	f := lifecycleSetup(t)
	if got := f.accept(); got != IntakePending {
		t.Fatal(got)
	}
	if got := f.accept(); got != IntakeDuplicate {
		t.Fatal(got)
	}
	if s := f.view(t); s.State != "pending_authorization" || s.Archived {
		t.Fatal(s)
	}
	for _, q := range []string{
		"UPDATE aitagent_intake SET fingerprint=repeat('b',64)", "DELETE FROM aitagent_intake", "TRUNCATE aitagent_intake CASCADE",
		"UPDATE aitagent_intake_audit SET action='blocked'", "DELETE FROM aitagent_intake_audit", "TRUNCATE aitagent_intake_audit",
		"UPDATE aitagent_test_grants SET active=true", "SELECT * FROM aitagent_test_grants", "DROP TABLE aitagent_intake_audit",
		"SET ROLE aitagent_owner", "SELECT public.aitagent_archive_batch()",
	} {
		deniedSQL(t, f.app, q)
	}
	deniedSQL(t, f.reader, "UPDATE aitagent_intake SET state='blocked'")
	deniedSQL(t, f.maintenance, "SELECT * FROM aitagent_intake")
	deniedSQL(t, f.maintenance, "DELETE FROM aitagent_intake_audit")
	deniedSQL(t, f.maintenance, "SELECT public.aitagent_fixture_authorized('target-org','target-worker',9,'test-key','inspect')")
	// Omitting the event must fail even if application code bypasses the kernel.
	deniedSQL(t, f.app, "UPDATE aitagent_intake SET state='blocked',revision=revision+1")
	if got := f.inspect.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1)); got != ReviewUnavailable {
		t.Fatal("reader cancelled", got)
	}
	if got := f.state.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1)); got != ReviewCancelled {
		t.Fatal(got)
	}
	s := f.view(t)
	if s.TerminalBy != "aitagent_app" || s.TerminalAt.IsZero() {
		t.Fatal("terminal principal missing", s)
	}
	maintenance(t, f, true, 0) // fresh terminal is not eligible despite old synthetic source expiry
	deniedSQL(t, f.app, "UPDATE aitagent_intake SET terminal_at=clock_timestamp()-interval '40 days'")
	ageTerminals(t, f)
	maintenance(t, f, true, 1)
	s = f.view(t)
	if !s.Archived || len(s.Events) != 0 || s.ArchiveEventCount != 2 || s.ArchivedBy != "aitagent_maintenance" || !validKey(s.ArchiveDigest) || s.TerminalOperationID != "cancel:"+op(1) {
		t.Fatal("archive summary invalid", s)
	}
	var audit int
	if err := f.admin.QueryRow("SELECT count(*) FROM aitagent_intake_audit").Scan(&audit); err != nil || audit != 0 {
		t.Fatal("detail not removed", audit, err)
	}
	maintenance(t, f, true, 0)
	if got := f.accept(); got != IntakeRejected {
		t.Fatal("archived identity re-admitted", got)
	}
	deniedSQL(t, f.app, "INSERT INTO aitagent_intake_audit(delivery_key,fingerprint,action) SELECT delivery_key,fingerprint,'pending_intake_recorded' FROM aitagent_intake")
	if _, err := f.admin.Exec("UPDATE aitagent_test_grants SET active=false"); err != nil {
		t.Fatal(err)
	}
	if _, got := f.inspect.Inspect(context.Background(), f.candidate.DeliveryKey, f.p); got != ReviewDenied {
		t.Fatal("archive bypassed current authorization", got)
	}
}

func seedPending(t *testing.T, f *lifecycleFixture, first, last int) {
	t.Helper()
	tx, err := f.admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO aitagent_intake(delivery_key,fingerprint,organization_id,employee_id,actor_id,source_expires_at,state)
 SELECT lpad(to_hex(n),64,'0'),lpad(to_hex(n),64,'0'),'target-org','target-worker',9,1,'pending_authorization' FROM generate_series($1::int,$2::int) n`, first, last)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO aitagent_intake_audit(delivery_key,fingerprint,action) SELECT lpad(to_hex(n),64,'0'),lpad(to_hex(n),64,'0'),'pending_intake_recorded' FROM generate_series($1::int,$2::int) n`, first, last)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestOfflinePGLifecycleReleases128SlotsWithoutReopeningIdentity(t *testing.T) {
	f := lifecycleSetup(t)
	if got := f.accept(); got != IntakePending {
		t.Fatal(got)
	}
	seedPending(t, f, 1, 127)
	newIntent, newPolicy, key := fixture(t)
	newIntent.Receipt = strings.Repeat("b", 64)
	proof, _ := Seal(newIntent, newPolicy.KeyID, key, 100, 160)
	newSource := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(newIntent, "awaiting_octop_authorization"), nil
	}
	acceptNew := func() IntakeOutcome {
		return f.inbox.Accept(context.Background(), proof, newPolicy, func() int64 { return 101 }, newSource)
	}
	if got := acceptNew(); got != IntakeFull {
		t.Fatal(got)
	}
	for n := 0; n < 4; n++ {
		maintenance(t, f, false, 32)
	}
	maintenance(t, f, false, 0)
	maintenance(t, f, true, 0)
	ageTerminals(t, f)
	for n := 0; n < 4; n++ {
		maintenance(t, f, true, 32)
	}
	var total, active int
	if err := f.admin.QueryRow("SELECT count(*),count(*) FILTER(WHERE archived_at IS NULL) FROM aitagent_intake").Scan(&total, &active); err != nil || total != 128 || active != 0 {
		t.Fatal(total, active, err)
	}
	if got := acceptNew(); got != IntakePending {
		t.Fatal("capacity not released", got)
	}
	if got := f.accept(); got != IntakeRejected {
		t.Fatal("original receipt resurrected", got)
	}
	// A new proof/key with an extended source expiry cannot reuse the old key.
	replayIntent, replayPolicy, replayKey := fixture(t)
	replayIntent.Receipt = f.i.Receipt
	replayIntent.ReceiptExpiresAt = 300000
	replay, _ := Seal(replayIntent, replayPolicy.KeyID, replayKey, 200, 260)
	replaySource := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(replayIntent, "awaiting_octop_authorization"), nil
	}
	if got := f.inbox.Accept(context.Background(), replay, replayPolicy, func() int64 { return 201 }, replaySource); got != IntakeRejected {
		t.Fatal("extended proof revived archived identity", got)
	}
	t.Log(fmt.Sprintf("retained %d permanent identities; active slots reused", total))
}

func TestOfflinePGLifecycleFailureUnknownAndRegistryBound(t *testing.T) {
	f := lifecycleSetup(t)
	if got := f.accept(); got != IntakePending {
		t.Fatal(got)
	}
	// Failure to append the terminal event must roll back state and capacity.
	if _, err := f.admin.Exec("ALTER TABLE aitagent_intake_audit ADD CONSTRAINT offline_failure CHECK(action<>'expired') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if n, o := f.lifecycle.Run(context.Background(), false); n != 0 || o != IntakeUnavailable {
		t.Fatal(n, o)
	}
	if s := f.view(t); s.State != "pending_authorization" {
		t.Fatal(s)
	}
	if _, err := f.admin.Exec("ALTER TABLE aitagent_intake_audit DROP CONSTRAINT offline_failure"); err != nil {
		t.Fatal(err)
	}
	f.lifecycle.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost acknowledgement")
	}
	if n, o := f.lifecycle.Run(context.Background(), false); n != 0 || o != IntakeUnknown {
		t.Fatal(n, o)
	}
	if s := f.view(t); s.State != "expired" || s.Revision != 2 {
		t.Fatal(s)
	}
	f.lifecycle.commit = func(tx *sql.Tx) error { return tx.Commit() }
	maintenance(t, f, false, 0)
	ageTerminals(t, f)
	// A corrupt/missing event prevents compaction rather than hiding the gap.
	if _, err := f.admin.Exec("DELETE FROM aitagent_intake_audit WHERE revision=1"); err != nil {
		t.Fatal(err)
	}
	if n, o := f.lifecycle.Run(context.Background(), true); n != 0 || o != IntakeUnavailable {
		t.Fatal(n, o)
	}
	var archived bool
	if err := f.admin.QueryRow("SELECT archived_at IS NOT NULL FROM aitagent_intake").Scan(&archived); err != nil || archived {
		t.Fatal(archived, err)
	}
	// Owner-only synthetic seeding reaches the separate permanent identity bound.
	seedPending(t, f, 1, 4095)
	i, p, key := fixture(t)
	i.Receipt = strings.Repeat("b", 64)
	proof, _ := Seal(i, p.KeyID, key, 100, 160)
	src := func(context.Context, Binding, string) (SourceRecord, error) {
		return observeSource(i, "awaiting_octop_authorization"), nil
	}
	if got := f.inbox.Accept(context.Background(), proof, p, func() int64 { return 101 }, src); got != IntakeRegistryFull {
		t.Fatal(got)
	}
}

func TestOfflinePGLifecycleConcurrentAndLockFailure(t *testing.T) {
	f := lifecycleSetup(t)
	if got := f.accept(); got != IntakePending {
		t.Fatal(got)
	}
	tx, err := f.admin.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("SELECT id FROM aitagent_capacity_guard FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	if n, o := f.lifecycle.Run(context.Background(), false); n != 0 || o != IntakeUnavailable {
		t.Fatal(n, o)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	results := make(chan int, 2)
	outcomes := make(chan IntakeOutcome, 2)
	for j := 0; j < 2; j++ {
		go func() { n, o := f.lifecycle.Run(context.Background(), false); results <- n; outcomes <- o }()
	}
	total := <-results
	total += <-results
	for j := 0; j < 2; j++ {
		if o := <-outcomes; o != IntakeOutcome("maintenance_recorded") {
			t.Fatal(o)
		}
	}
	if total != 1 {
		t.Fatal(total)
	}
	if s := f.view(t); s.Revision != 2 || len(s.Events) != 2 {
		t.Fatal(s)
	}
}

func TestOfflinePGLifecycleSourceRetentionAndArchiveUnknown(t *testing.T) {
	f := lifecycleSetup(t)
	if got := f.accept(); got != IntakePending {
		t.Fatal(got)
	}
	if got := f.state.Cancel(context.Background(), f.candidate.DeliveryKey, f.p, 1, op(1)); got != ReviewCancelled {
		t.Fatal(got)
	}
	ageTerminals(t, f)
	// Aged cancellation alone cannot bypass the source-expiry retention window.
	if _, err := f.admin.Exec("BEGIN; SET LOCAL ROLE aitagent_owner; UPDATE aitagent_intake SET source_expires_at=floor(extract(epoch FROM clock_timestamp())*1000)::bigint; COMMIT"); err != nil {
		t.Fatal(err)
	}
	maintenance(t, f, true, 0)
	if _, err := f.admin.Exec("BEGIN; SET LOCAL ROLE aitagent_owner; UPDATE aitagent_intake SET source_expires_at=1; COMMIT"); err != nil {
		t.Fatal(err)
	}
	f.lifecycle.commit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("lost archive acknowledgement")
	}
	if n, o := f.lifecycle.Run(context.Background(), true); n != 0 || o != IntakeUnknown {
		t.Fatal(n, o)
	}
	if s := f.view(t); !s.Archived || s.ArchiveEventCount != 2 {
		t.Fatal(s)
	}
	f.lifecycle.commit = func(tx *sql.Tx) error { return tx.Commit() }
	maintenance(t, f, true, 0)
}
