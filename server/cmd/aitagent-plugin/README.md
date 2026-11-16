# AI-TAgent local request intake (0.2.13)

## Actual isolated peer contract (0.2.13)

[INTEROP.md](INTEROP.md) pins Octop **247b456**, executes its unmodified ASGI,
authentication and SQLite intake/cancel code with Go-signed requests, and documents
21 HTTP attempts. It supersedes the old ingress-absent finding below. The source
uses fake Mattermost API snapshots; neither installed services nor TLS is tested.
Cancellation evidence now preserves original commitment and expiry in the source
journal. Legacy associations fail closed for remote handoff. Real execution and
sending remain disabled; identity isolation, live source authentication, deployment,
capacity lifecycle and real dual-service acceptance are still required.

## Historical peer protocol audit (0.2.12; superseded)

The following findings apply only to c9fd4021, not to 247b456.

[PROTOCOL.md](PROTOCOL.md) separates the eight existing Octop APIs at reviewed
commit `c9fd4021` from a proposed, **unimplemented** incoming handoff contract.
The machine-readable [snapshot](protocol/octop-contract.json) and local-only
[source checker](protocol/check_octop_contract.py) reproduce known incompatibility:
current user-session configuration/publication APIs cannot consume the plugin
proof or provide original-receipt inspection/cancellation. The checker uses no
credentials, HTTP or source-module execution; a successful blocker check never
means live compatibility. Default exit code 2 explicitly means blocked.

This phase adds no runtime component or simulated endpoint. It documents exact
identity, byte, error and retry requirements and the missing peer-side work.
Octop production verification remains MockTransport-only; broker identity,
secret isolation, ingress, storage migrations and live authentication remain gates.
Only this repository is changed. Historical Octop snapshots below are superseded
for this review by the pinned `c9fd4021` evidence, not by a live deployment claim.

## Runnable automatic cancellation demonstration (0.2.11)

Start with [DEMO.md](DEMO.md): it provides `make demo`, a machine-readable result,
a fresh read-only review of Octop `f5efc2db`, and the gated two-service acceptance
plan. The demo now automatically polls durable cancellation work rather than
requiring the test to call each retry manually. It demonstrates late intake then
automatic terminal confirmation in two attempts. This is an isolated simulation,
not installed Mattermost/Octop services or a production connector.

The scheduler is serial, single-scope, default-disabled and not wired to plugin
startup. It preserves separate cancellation permissions, bounded discovery, claim
fencing and retry limits. Local nonblocking events/statistics are not external
alerts. Real source/receiver service authentication, executor-secret isolation,
Octop ingress/migrations, runtime installation, operational recovery and live
fault tests remain blockers. Execution, posting and production verification stay
closed; no Octop source was modified and its tests were not run this round.

## Durable bounded cancellation retries and confirmation (0.2.10)

The offline chain now persists a cancellation job linked to the original sender
identity. It can recover when cancellation arrives before the receiver commits
intake: the first query remains unresolved, a later due attempt queries the same
identity again, and only an independently authenticated receiver inspection of
`cancelled` or `expired` records terminal confirmation. A propagation response,
missing row, timeout or expired lease is never sufficient confirmation. No result
means an external action was undone or a task was successfully executed.

### Persistence and recovery contract

`QueueCancellation` checks explicit cancellation permission and a fresh trusted
source cancellation observation before storing the original fingerprint and source
revision as metadata. It checks permission again in the transaction. This captured
observation is **not reusable authorization**. Every propagation attempt still uses
the receiver adapter's live source and local permission checks; the source cannot
be replaced with the queued observation. Cancellation queue rights are a separate
required adapter: `NewOfflineOutbox` alone cannot use the queue.

`DueCancellations` returns at most 16 currently authorized keys in the pinned
organization/employee/actor scope. Discovery requires explicit scope permission
(empty candidate delivery key), then per-key permission. These are hints, not
claims. `RetryCancellation` performs one due attempt and rechecks permission;
there is no automatic polling loop, plugin worker, HTTP route or network client.
The isolated source → journal → inbox → cancellation → confirmation test actually
calls these components; none is enabled for real accounts or execution.

A claim increments the durable attempt counter, writes its audit event and commits
a random lease token before any receiver call. Unknown claim commits make **no
receiver call**. The database sets a five-second lease; acknowledgements require
the matching token, attempt and an unexpired lease. No DB lock is held across the
adapter call. An expired worker can overlap a new worker if its RPC ignores context,
but its late result cannot overwrite the newer attempt. Receiver cancellation is
identity-bound, revision-checked and terminal/idempotent; this mechanism must not
be reused as an execution lease or exactly-once action mechanism.

A completed unsuccessful attempt persists exponential retry delays of 1–60 seconds
using database time. Up to 16 claims are allowed, including claims whose worker
crashes before calling the receiver. Exhaustion becomes `attention`, **not success**;
a last-claim crash becomes attention after lease expiry. Duplicate enqueue never
resets attempts. A confirmed job stores which terminal state was observed. Its
confirmation is historical, not proof of current service authorization or remote
execution results. Sender cancellation permission is checked again before storing
confirmation. Current receiver inspection is independently authorized.

State changes and audit events commit together. The kernel checks the initial
queue event, every sequential claim and the last recorded outcome. An absent old
acknowledgement can mean a crashed worker, so this does not establish a complete
owner-resistant history. Runtime roles cannot delete audit or change identity,
and cannot reset terminal jobs. A schema owner/superuser remains trusted. During
normal kernel operation a job has at most 33 events. No goal, signed proof, secret
or raw RPC error is persisted.

### What recovery does and does not cover

A fresh OS test process opens new restricted-role PostgreSQL connections, discovers
a persisted due key and confirms its late-arriving receiver record. Tests also
cover concurrent claimants, old-worker fencing, lost claim/receiver/confirmation
acknowledgements, separate cancellation permission, permission revocation during
confirmation, wrong identity, missing audit, audit-write rollback and evidence
failure through retry exhaustion. Fixture-only owner operations age due dates and
leases to simulate elapsed time without changing system clocks. This is process
recovery against a running isolated database, **not** PostgreSQL restart, disk loss,
replica promotion, backup restore or live Mattermost/Octop service recovery.

The source receipt and its association still expire through seven-day lazy cleanup.
If fresh source evidence or permission disappears before receiver cancellation,
the job remains unresolved and may exhaust its retries. Queued metadata cannot
bypass that loss. Existing sender capacity bounds jobs to 128 permanent identities;
there is no cleanup, deletion, evidence archival or manual reset API. Retry pacing,
limits, retention, alerting and an authorized operator recovery procedure require
production design. `attention` must not be silently retried forever or deleted to
restore capacity. There is no automatic monitor to notify an operator today.

The new SQL is an explicit isolated schema, not an upgrade migration. Source JSON
still requires stopping old writers that can discard the association; restoring or
downgrading databases must preserve queue/audit/outbox identity history. No schema
version negotiation, restore epoch or safe rolling upgrade is supplied. Claims
and confirmations are local transactions; source and receiver changes are not
atomic with them. Authenticated service adapters, secure transport, deployment
admission/fairness, cooperative RPC cancellation, scheduling/operations and true
two-service tests remain 1.0 gates. Real sending and execution remain unwired and
closed. The older source, privacy, capacity and cancellation-race limitations below
remain unless explicitly superseded by this bounded offline retry kernel.

## Same-receipt association and offline cancellation propagation (0.2.9)

The isolated chain now calls the guarded Mattermost source adapter to reserve a
handoff identity in the original receipt, prepares the sender journal, records an
attempt, invokes the local receiver simulation, propagates source cancellation,
and reconciles the original identity. No command, hook, HTTP route, configured
worker, sender, signer or executor enables this path. All new methods remain
unwired outside tests, with deployment-owned binding/policy and independent local
authorization required. This remains pre-1.0, not a production integration.

### Actual atomic boundary

Repository `channels/store/sqlstore/plugin_store.go` implements `Get` on a replica.
`SetWithOptions(Atomic:true, OldValue:...)` delegates to `CompareAndSet`, whose
conditional update runs on the primary. The plugin's bounded `mutate` loop uses
that exact old ledger value and advances its revision; it never treats a replica
read alone as confirmation. This protects one KV key, **not** a transaction with
PostgreSQL or another service. Replica lag may cause failure and commit errors
may leave a successful write without an acknowledgement.

The private `reserveHandoff` method checks the current proof, pinned mapping,
current permissions and exact stored intent, then adds an immutable delivery key,
fingerprint and binding digest inside the existing receipt's ledger CAS value.
A conflicting reservation cannot replace it. This is an atomic **association with
an existing request**, not simultaneous creation of the original request and a
PostgreSQL outbox. A crash before reservation leaves the original receipt without
an association and must not cause sending. Source observations now return this
association when present. Sender `Prepare` and `BeginAttempt` both reject an absent
or mismatching association, even if the proof and source state otherwise pass.

Cancellation already updates the same receipt state and clears its goal in that
same ledger CAS; it preserves the immutable association. There is deliberately
no separately written cancellation flag which could diverge. The new guarded
cancellation reader primary-confirms the association plus current cancelled state
and revision without returning a goal or proof. A cancelled request cannot acquire
a new reservation. Unknown reservation commits require rereading/reconciling that
same receipt, never generating another identity.

### Receiver cancellation and acknowledgement limits

`PropagateCancellation` first checks local cancellation permission, releases DB
locks, obtains an authenticated source observation, checks exact delivery key and
fingerprint, then rechecks local permission and expected receiver revision. For a
nonterminal row it requires a source revision newer than the last observation.
It atomically records `cancelled` and a `source-cancel:` audit operation. A repeated
confirmed cancellation cannot revive or duplicate the receiver transition. Unknown
receiver commits are resolved by independent `Inspect`/same-identity retry; no
compensating delete occurs. A terminal `expired` row stays expired. This fence
cannot undo an external action, and no state grants permission to execute one.

If cancellation propagation finds no receiver row while intake is in flight, it
returns unavailable and creates no tombstone. That intake can subsequently commit
a pending row after its earlier active-source observation. A negative concurrency
test demonstrates that propagation must then be retried; the current kernel has no
worker to guarantee this retry. This is another reason execution stays disabled.

The cancellation reader is a trusted in-process adapter seam, not a network
protocol, historical signed cancellation proof or user-supplied JSON. There is no
service authenticator or transport. Revoked source access, disabled configuration,
expired/pruned receipts, wrong binding or unavailable/lagging KV fail closed;
those failures do not prove cancellation or absence. Since there is no independent
service cancellation privilege yet, removing the original user's permission can
also prevent cancellation propagation. This availability/security tradeoff blocks
production enablement. Local receiver authorization is checked on both sides of
the source call; the adapter must supply a real authenticated local principal.

### Upgrade, retention and remaining gaps

The optional source marker is created only by isolated test calls. Older plugin
binaries can discard unknown JSON fields when rewriting the v2 ledger. Before any
future use, **all old writers must be stopped and primary/replica state verified**;
`QuotaStorageReady` is only an acknowledgement, not a migration or writer fence.
No backfill or safe mixed-version rollout has been implemented. Do not enable a
sender by merely adding this marker field to an old installation.

Markers inherit the receipt's seven-day lazy pruning. An unresolved delivery or
unpropagated cancellation may outlive that evidence; pruning is not acknowledgement.
There is no cancellation scheduler, durable source acknowledgement, delivery scan,
retention extension, sender journal cleanup or restore epoch. The sender's 128-row
permanent cap and receiver's 4096-identity cap remain. These bounds, upgrade safety,
source-ledger crash recovery, authentic service identities, secure transport,
real two-service fault tests and safe execution remain 1.0 blockers. Source
cancellation can still race after a successful observation. No cross-system
atomicity, guaranteed cancellation delivery or exactly-once behavior is claimed.

Tests exercise same-row association/cancellation, conflicting target mappings,
CAS acknowledgement loss before/after commit, stale replicas, concurrent source
reservation/cancellation, disabled/revoked/expired access, missing associations,
receiver audit rollback, concurrent propagation and lost cancellation commits.
The full source-to-receiver simulation uses fake plugin APIs and isolated PostgreSQL
with administrator fixture setup; separate cancellation and sender cases use the
restricted application role. This is not real Mattermost/Octop communication or
production permission acceptance. Synthetic admission clocks are advanced in the
long chain test to respect, rather than remove, existing status rate budgets.

## Offline sender journal and identity-bound reconciliation (0.2.8)

This phase adds a **partial outbox kernel**, called by isolated PostgreSQL tests.
It is not a production sender outbox: no plugin route, network client, scheduler,
executor, credentials or automatic migration is added. Default constructors deny
work. The existing plugin settings cannot enable this kernel. Only test code
uses its successful attempt result to call the local `Inbox.Accept` simulation.

`Prepare` verifies a signed proof and live source observation, checks independent
sender permission, and durably reserves the original delivery key, fingerprint,
target and source expiry with an audit event. It persists no goal, proof, token or
key. Existing identities cannot be repurposed. `BeginAttempt` repeats proof/source,
clock and local permission checks, locks that row and commits `delivery_unknown`
plus its audit **before** the test caller invokes the receiver. Concurrent callers
cannot each acquire an initial attempt. An unknown sender commit never permits a
receiver call. Once this phase is recorded, no API resets it to prepared or grants
a second attempt. Permission callbacks must capture a trusted authenticated local
caller; mapped actor IDs and chat content are not credentials.

`Reconcile` checks current sender authorization, then calls an independently
authenticated receiver inspector without holding sender transaction locks, and
checks sender authorization again before recording the result. `Inspect` now
returns the stored delivery key and fingerprint, including after audit compaction;
the sender requires both to match its durable record. It does not need a still-valid
proof or a reachable source, so a receiver commit can be confirmed after source
expiry or cancellation. `receiver_recorded` means only **historical existence**:
a cancelled/expired/blocked receiver record also confirms delivery, never execution
or current permission. This journal does not mirror receiver state or propagate
cancellation. Missing, denied, unavailable or wrong-identity responses never prove
non-delivery and never enable retransmission. No delivery acknowledgement alone
updates the journal; the isolated path uses authenticated inspection instead.

State transitions and audit append commit together; reads validate the bounded
audit sequence. Receiver acknowledgement loss, sender acknowledgement loss,
reconciliation acknowledgement loss and process-object restart retain the same
identity. After a crash between recording an attempt and calling the receiver,
the journal can remain unresolved forever: it deliberately sacrifices liveness
rather than guess whether delivery happened. This is not exactly-once delivery.
A safe retry protocol, receiver durable deduplication across restore epochs and
authenticated absence/acceptance semantics still need a production design.

The journal has a separate fixed **128 permanent identity** cap and three events
per identity, with no cleanup or retry scheduler. It does not reuse the receiver's
compaction policy. Retention/capacity management is an explicit unfinished gate;
do not delete rows to restore sending. It does not durably preserve the payload
needed for reconstruction, and its transaction is **not atomic with the Mattermost
KV receipt**. Source-ledger crash recovery, payload retention, transactional outbox
creation and cancellation propagation remain missing. The source may cancel just
after the last observation; a negative race test demonstrates that recording the
attempt cannot eliminate that gap. No result grants execution authority.

The six new PostgreSQL tests use the restricted application role for journal
operations, with column grants and immutable audit access; administrators install
fixtures and inject faults only. They exercise unknown commits, original-identity
reconciliation after expiry/compaction, absent receiver records, eight concurrent
attempts, wrong fingerprints, permission revocation during reconciliation, audit
failure/missing history, capacity, clock rewind and lock timeout. Sender and receiver
use separate tables/connections in **one isolated database**, not independent live
services or a distributed fault test. The source is simulated. The sender permission
helper reuses synthetic fixture grants and is not a deployed identity adapter.
SQL/context deadlines do not forcibly preempt an uncooperative callback; a future
transport must implement bounded cooperative cancellation and process admission.

The 0.2.7 receiver lifecycle limits, shared plugin budgets, blocking RPC limitation,
upgrade constraints, source audit privacy and default-off execution all remain.
The historical sections below describe earlier phases, not additional runtime
capabilities enabled by this version.

## Offline inbox lifecycle and restricted database roles (0.2.7)

This revision extends the **isolated fixture**, not the live plugin integration.
Execution and outbound networking remain disabled and unwired. `Lifecycle.Run`
is actually called by the disposable PostgreSQL tests; there is no installed
scheduler, production migration, real receiver identity adapter or cleanup job.

### Terminal states, retention and replay boundary

`cancelled` and `expired` are terminal non-executing states. Local cancellation
records the database time and session role; maintenance marks source-expired
records `expired`, appending the matching event in the same transaction. Expiry
is not successful completion, proof of remote cancellation or an execution result.
All other states, including `execution_observed`, remain non-executable historical
observations. The eighth revision is reserved for a terminal transition.

The maintenance capability takes no caller-supplied cutoff, identity or SQL.
Each call processes at most 32 records under bounded SQL waits. Compaction requires
**both** source expiry and terminal database time to be at least 30 days old.
It checks complete sequential audit history, then atomically stores a SHA-256
summary, event count, final operation ID, database timestamp and maintenance
session role before deleting detailed audit events. The summary is not a backup
of those events and cannot reconstruct them. This fixed fixture retention policy
requires an explicit production retention/privacy review; it is not a legal
retention recommendation. A database owner can alter history and the summary:
this is not owner-resistant or externally anchored tamper evidence.

Compacted rows retain the original primary key, fingerprint and target metadata
permanently. No goal, proof or secret is stored. Their 128-slot **unarchived**
capacity is released, but the identity tombstone still rejects replay, including
a newly signed proof with an extended expiry. There is a separate 4096-identity
registry limit which fails closed. This fixes the 128-record permanent stall in
the offline kernel, not unlimited retention or throughput. Registry partitioning,
capacity operations and approved identity archival are still 1.0 blockers. Never
delete tombstones merely to admit retries. Without scheduled maintenance,
retention and capacity release are delayed indefinitely. The Mattermost KV ledger
and server audit retention remain independent and are unchanged by this feature.

Intake and maintenance serialize capacity accounting on one singleton guard row;
state changes use row locks and expected revisions. This is not a fair tenant
queue. Detailed audit appends and state changes must commit together, enforced by
deferred constraints. Missing history aborts compaction. A commit error returns
`commit_unknown` with no claimed count: reconcile the original identity using the
independently authorized `Inspect`, which now returns either full events or the
terminal archive summary from one repeatable-read snapshot. Compacted summaries
preserve only the last operation ID, not per-operation reconciliation forever.
Never infer non-delivery from an unavailable read or create a new receipt to retry.

### What the role tests establish

The network-isolated PostgreSQL fixture connects directly as three distinct
non-superuser, non-owner roles: application, reader and maintenance. The owner is
NOLOGIN; none of those roles may assume it. Application grants are column-scoped;
identity changes, deleting/truncating audit, changing grants and DDL are denied.
The reader can inspect but cannot mutate; maintenance has only two narrowly scoped
SECURITY DEFINER function capabilities and no direct table access. Functions have
fixed search paths, and PUBLIC cannot create objects in the fixture schema.
Database triggers enforce terminal fencing and matching audit events. The test-only
authorization helper is a synthetic grant check, **not** a real caller authenticator.
These roles are scoped to this kernel; production tenant/RLS isolation, connection
credentials, owner controls and real Octop permissions still require design and
validation. Administrator connections only install/reset schemas and inject aged
records, audit faults and large synthetic capacity fixtures. Earlier state tests
still use the administrator and do not establish least-privilege operation.

### Validation of this phase

`make test` passes race-enabled package tests and 10 Go/Python/OpenSSL vectors.
`python3 tests/postgres_inbox.py --go /workspace/toolchains/go/bin/go` passes 20
isolated PostgreSQL cases (15 existing, 5 lifecycle cases), including direct
restricted-role sessions, 128-slot reuse without identity reuse, separate registry
exhaustion, retention on both clocks, concurrent maintenance, lock timeout, event
failure rollback, corrupt-history rejection and lost commit acknowledgements for
expiry and compaction. The container uses cached official PostgreSQL 15.20 with
no network or published ports and is removed afterward. Tagged vet and the Linux
plugin bundle build also pass. These are synthetic source/SQL tests, not a running
Mattermost-to-Octop deployment, production permission acceptance or crash/restore
and replica-failover validation.

### Cancellation race and sender outbox still block 1.0

A source can cancel immediately after the last remote check; a receiver transaction
cannot atomically commit with Mattermost KV. No lifecycle state removes this race
or grants execution. A real action must repeat current source/version/expiry,
target and local authorization checks at its execution boundary, with an explicit
policy for cancellation during an irreversible action. No distributed atomicity
or exactly-once guarantee is claimed.

A future sender outbox must durably retain the original delivery identity and
fingerprint before delivery, use authenticated reconciliation for unknown commits,
and recover cancellation propagation without deleting or inventing new identities.
The current KV request ledger is not that outbox. Durable cancellation delivery,
receiver authentication, secure transport, restore epochs, real two-service tests,
archival of outbox reconciliation history and safe execution remain unimplemented.
The following sections describe earlier phases; their original 128-retained-row
and administrator-only fixture limitations are superseded only where stated above.

## Versioned review, local cancellation and independent reconciliation (0.2.6)

The offline PostgreSQL kernel now has a persistent review state machine. It is
called by the isolated source → inbox integration test, but **no state is an
execution permission**. There is still no executor, automatic task, message send,
production identity adapter, runtime route or setting enabling this path. The
zero value and disabled/missing-authorizer constructors reject work. A valid
signature or a past positive observation never suffices for a new review.

### State and transition contract

| Stored state | Meaning and allowed next actions |
| --- | --- |
| `pending_authorization` | Intake metadata only; authorization review or local cancellation |
| `authorization_observed` | Historical live-check observation; execution review must repeat all checks |
| `execution_observed` | Historical pre-execution check, **not runnable**; another cycle requires authorization review |
| `blocked` | A review failed source/version/time checks; explicit fresh authorization review is required to recover |
| `cancelled` | Terminal local fence; no review or duplicate intake can revive it |

`StateMachine.Review` takes an explicit authorization-review or execution-review
action, an expected local revision and a bounded canonical operation ID. Every
attempt, including retries, verifies the proof and target binding, calls the
current source reader, checks source state/expiry/commitment, and requires a
confirmed source revision **newer than the last persisted observation**. It then
locks the inbox row, checks the exact fingerprint/target and action-specific
receiver rights/trust in the transaction, and rechecks proof time before commit.
Execution review requires a prior authorization observation but does not return
a lease, credential, reusable approval or execution token. Result codes explicitly
say `no_execution`. There is no transition to `running`, `executed` or `completed`.

Source errors, cancelled/unknown/missing state, stale version and expiry deny the
operation. If local rights, expected revision, context and history capacity permit,
the kernel additionally commits a `blocked` state and audit together. If it cannot
persist that evidence, it still denies/returns unavailable or commit-unknown;
an older positive state is only history and cannot authorize another operation.
A timed-out source lookup may therefore leave the previous historical state.
Local permission failure cannot be used by an unauthorized caller to mutate it.
Concurrent stale expected revisions do not overwrite a newer transition.

The source adapter reports the **ledger revision written by its successful CAS**,
not a caller-supplied counter. This is installation-wide observation metadata,
not a per-receipt mutation version or tenant-isolated counter. Missing/zero/out-of-
range versions are rejected. It helps detect non-increasing/replayed observations,
but is not authentication, a lease or a rollback-proof epoch. A database restore
and subsequent counter catch-up can evade simple monotonic comparisons. Real
deployment needs a trusted instance/restore epoch and an explicit rollback/upgrade
protocol; neither this counter nor key rotation can establish that guarantee.

### Local terminal cancellation and recovery without an old proof

`StateMachine.Cancel` uses the original delivery key, expected local revision and
operation ID. It rechecks independent local action rights and exact stored target,
then atomically writes terminal cancellation and its audit. No source availability
or unexpired proof is needed. A competing in-flight review locks the same row and
cannot revive a committed cancellation. Duplicate cancellation rechecks rights
and remains terminal without another audit write. This cancels **only local
pending metadata**; it does not propagate cancellation to Mattermost or stop an
external process. There is no such process in this implementation.

`StateMachine.Inspect` separately authorizes the current local caller and target,
then returns stored state, revision, last source revision and bounded operation
history. It does not call the source or require the old proof to remain valid.
History contains action/revision/operation ID only, with no goal or proof. The
kernel checks sequential audit revisions and matching fingerprints before returning
it; this detects some corruption, not tampering by a database owner. Missing/error
is reported as unavailable, **not proof of non-commit**. Callers must query the
correct primary and reconcile original operation IDs after lost acknowledgements,
never invent a new delivery key or delete/reinsert to guess the outcome.

Policy's mapped actor identifies the resource owner, **not an authenticated local
caller**. The required action authorizer must obtain that caller from a separate,
verified server context/closure. Do not deserialize policy or accept a submitted
actor/organization ID as login evidence. The tests use a synthetic action-permission
table and locks; actual Octop session/manager/Agent authorization is not wired.
Revoked local rights appropriately block recovery queries too; privileged operator
recovery needs its own reviewed authorization path, not an auth bypass.

### Persistent idempotency, audit and bounded history

Intake duplicates now report `duplicate_retained_intake`, without changing the
stored review state or suggesting it is executable. Each row has a local revision;
state change and the corresponding audit event are
one transaction. `(delivery_key, operation_id)` is unique. A retry of a recorded
review is recognized only after current proof/source/local checks; it cannot
restore a subsequently blocked or cancelled state. Changing the action with the
same operation ID conflicts. Commit errors remain `commit_unknown_no_execution`.
Tests inject errors after real review/cancellation commits and recover via a new
connection, original key and recorded operation IDs. Audit failure rolls back
the state transition. The persistent record is not task-level business idempotency.

History is intentionally bounded: initial intake is revision 1, reviews/blocked
transitions stop at revision 7, and revision 8 is reserved for terminal cancellation.
Exhaustion denies further review, not permission to reset history. The installation
still retains at most 128 inbox rows. Cancellation/expiry does **not** release that
cap, and there is no completion, pruning, tombstone archive or reset API: after
128 retained rows the prototype stops accepting new intake indefinitely. A safe
retention/replay horizon, migration and archival design remains a 1.0 prerequisite.

### What remains impossible to infer from these checks

The last source read and local commit are not a distributed atomic transaction.
A retained negative test makes the source unavailable/cancelled after its last
positive read and still records `execution_observed`; the next review fails and
can block the row. This demonstrates why historical observations must never be
consumed as executable approvals. A real execution path needs an authenticated
source claim/cancellation protocol and local execution fencing/idempotency, plus
defined in-flight cancellation and crash-recovery semantics. Repeating a read
cannot eliminate the final race. No distributed absolute atomicity is promised.

Independent stored-state reconciliation is now executable in the offline kernel,
but there is no authenticated HTTP reconciliation endpoint or sender outbox.
Durable sender identity/retry state, cancellation propagation, restore epochs,
service authentication/TLS freshness, secret/executor isolation and actual Octop
permissions are still wiring gates. Source/receiver callbacks must honor context;
an issued Mattermost RPC remains uncancellable. Earlier process-budget sharing,
capacity and physical-retention/audit limitations remain unchanged.

### Test and database-privilege limits

The cached, network-isolated PostgreSQL 15.20 harness now exercises actual source
CAS → intake → authorization review → execution review → source cancellation →
blocked history → independently authorized local cancellation. Other tests cover
concurrent revisions, local cancellation racing an in-flight review, stale source
versions, action/key revocation, rotation, audit rollback, review/cancel unknown
commits, independent recovery after expiry, source/SQL timeouts and bounded history.
The ordinary Go suite runs with `-race`; the isolated PG binaries remain non-race
builds. Public Go/Python/OpenSSL proof vectors remain valid (signed proof v2 is
unchanged; trusted source observation now additionally requires a version).

**The fixture uses the PostgreSQL superuser.** It proves transaction behavior under
these tests, not least-privilege runtime access or audit tamper resistance. Its
Mattermost side is a fake API/KV implementation, not a live second service. No
production privileges were inspected or changed. Before deployment, design a
separate schema/migration owner, constrained runtime transition permissions,
read-only reconciliation access and audit append-only permissions; test denial of
audit UPDATE/DELETE/TRUNCATE and unauthorized metadata/grant changes. In particular,
do not grant broad permission-table UPDATE merely to obtain row locks. A privileged
DB owner can rewrite local history; resisting that requires an independent audit
trust/retention design. None of these role controls is implemented by this DDL.

The expanded SQL schema is created only in the disposable harness. No migration
from the earlier offline fixture or a real Octop schema is supplied. Do not apply
it to existing data or clear rows to upgrade. There is no new production enable
switch. These additions improve a testable safety boundary, not 1.0 readiness.

## Confirmed source observation → durable pending inbox (0.2.5)

The isolated integration harness now actually calls the plugin source adapter,
checks a signed intent, and commits pending metadata plus audit to PostgreSQL.
This is a **standalone offline kernel**, not an Octop migration, authenticated
source endpoint, production queue, or working task/execution integration. Plugin
commands still have no external client. Both source binding and inbox default
closed; only tests construct enabled instances. There is no runtime setting,
HTTP route, service credential, scheduler or worker that connects these pieces.

### Verified platform boundaries

`server/channels/app/plugin_requests.go` removes incoming `Mattermost-User-Id`
and sets it only after session/MFA/CSRF checks, but also invokes plugin handlers
for unauthenticated/invalid-token requests. That header is not an independent
service identity and cannot be trusted on a standalone HTTP listener. A client
cannot be allowed to select a user/organization by supplying it. No header-based
authentication or public `ServeHTTP` hook is added here.

`server/channels/store/sqlstore/plugin_store.go` reads KV on `GetReplica()` and
performs exact-old-value CAS on `GetMaster()`. A replica observation alone is not
current state. The generated synchronous plugin RPC API still has no cancellable
request context. A CAS false result can also hide transport failure. Existing
primary-confirmation, bounded retries and unknown-write handling are reused.

### Callable source adapter without goal disclosure

`handoffSourceReader` accepts only a deployment-owned `sourceBinding`; it rejects
a requested binding differing in any field before API calls. It derives the
actor/team/channel from that pinned binding, not a request header or body. It
uses the existing rejection prefilter and process **status** budget, then loads
current configuration, checks user/Bot/member/post permissions, performs the
existing ledger lookup with primary-side CAS confirmation, and reloads/rechecks
configuration and permissions before returning. Cancelled, expired, missing,
changed, uncertain or unconfirmed observations fail closed. The normal lookup
increments ledger revision; this is a confirmed observation, not a read-only SQL
operation or a source reservation.

`SourceRecord` now contains only an intent commitment, state and source expiry;
it contains no goal or proof. `VerifyWithSource` compares the commitment and
expiry against its already-verified intent. The commitment is a domain-separated
SHA-256 hash of the full intent; it is **not a secret, authorization token or
proof of authenticity**. Protect source access and logs: low-entropy text hashes
can still permit guessing. Real transport must authenticate the source and bind
freshness/replay protection to each lookup; no signed/challenge-bound network
response protocol is implemented. The direct in-process test call cannot prove
that a remote service is live or its response is fresh.

Cancellation of the Go context stops subsequent API work after control returns,
but does not interrupt an issued RPC. A private budget copy observes cancellation;
the shared process slot remains held until the synchronous call actually returns.
Tests cover blocked-RPC release, no subsequent write after cancellation, stale
replica rejection, ambiguous CAS confirmation, live source permission revocation,
scope mismatch and goal-free source serialization. Existing process sharing,
overdue-call shutdown/cooldown and cluster limitations are unchanged.

### Actual persistent receipt-level intake contract

`internal/handoff.Inbox.Accept` invokes source verification before opening a DB
transaction. It requires an explicit `ReceiverAuthorizer` to recheck and lock
current receiver trust/mapping/account/organization/Agent rights in that same
transaction, including on duplicates. There is no permissive default. The test
adapter locks synthetic authorization rows; a correct adapter for Octop's real
permission tables and revocation model is **not implemented**.

The PostgreSQL kernel atomically writes:

- a unique delivery key and full conflict fingerprint;
- stable mapped organization/employee/actor IDs, source expiry and the fixed
  `pending_authorization` state;
- one corresponding `pending_intake_recorded` audit row.

No raw goal, proof, signature, token or key is stored. Identical authorized retries
return `duplicate_pending_authorization` without adding another row/audit. A
different fingerprint returns `conflict`. Renewed proofs and signing-key rotation
retain the same delivery identity; current receiver key policy is still checked.
Audit failure rolls back intake. Any commit error returns `commit_unknown`, with
no compensating delete or automatic retry/new key. Tests inject an error **after
a real successful commit**, reconnect and reconcile the same key as a duplicate.
Actual socket-loss-at-commit behavior has not been exercised.

A table lock serializes the fixture inbox and its 128-retained-row cap across
processes. SQL lock waits are limited to 250 ms, statements to 1 second, with a
2-second cooperative operation context. This is intentionally a small bounded
experiment, not a tenant-fair or scalable queue. It has no pruning/reset API:
expired/cancelled rows still consume retained capacity, and it eventually fails
closed at 128. A production retention/tombstone/reconciliation policy must be
designed without reopening replay admission. Database pools, ingress rate limits
and tenant quotas also need deployment-specific enforcement.

### Cancellation and recovery are still wiring gates

The source CAS and receiver transaction are separate. A negative integration test
**demonstrates** cancellation after source confirmation can still leave a pending
receiver row. Nothing executes that row, but it cannot be advertised as current
source permission. There is no distributed claim/lease, cancellation propagation,
task-level idempotency, sender outbox or execution protocol. Zero-window revocation
is not provided. Receiver policy checks must lock all relevant real permission
and key/mapping revisions before commit; a constant-true callback is unacceptable.

After an unknown outcome, retry/reconcile only the original identity when current
source and receiver authorization still permit it. If the source expired or was
cancelled, `Accept` rejects and cannot establish whether an older pending record
exists. A separately authenticated reconciliation/cancellation endpoint is still
needed; absence/error/replica lag must never trigger automatic new requests.
Production remains unwired pending that protocol, durable sender recovery,
trusted identity provisioning, authenticated TLS/freshness, independent secret
isolation and actual Octop authorization/storage integration.

### Reproducible isolated validation

```sh
# Uses installed Go + cached modules and an already-cached official postgres:15.
# Never downloads images, accepts a production DSN, or publishes a host port.
python3 tests/postgres_inbox.py --go /path/to/go
```

The harness compiles Go tests offline and starts its own temporary container with
`--network none`, a tmpfs database and no host ports. Test binaries connect only
to its Unix socket; local synthetic trust auth and `sslmode=disable` on that
socket are not a production TLS configuration. It removes its container and test
binaries afterward. No private keys are persisted; worker fixture files contain
only public synthetic proofs/data and are temporary.

Verified on PostgreSQL 15.20: command → guarded source → SQL pending/audit; eight
independent receiver processes with one winner; reconnect deduplication; conflict,
receiver/source revocation and key rotation; audit rollback; injected lost commit
acknowledgement; expiry during authorization; retention exhaustion; bounded lock
failure; and the unresolved post-read cancellation race. The Mattermost API/KV
side uses the existing fake with reviewed replica/CAS semantics; this is **not**
a live Mattermost/Octop test, replica deployment test or production auth test.
The optional `offlinepg` binaries are compiled without the race runtime; ordinary
Go tests run with `-race`. Earlier privacy, migration, source RPC, audit retention
and 1.0 blockers remain applicable. Historical phase descriptions below describe
what was available in those versions, not an assertion of production readiness.

## Source expiry, revocation and ID compatibility (0.2.4)

Independent review found two **pre-wiring blockers** in the 0.2.3 offline proof:
its lifetime could extend beyond the source receipt's expiry, and a signature
alone remained valid after source cancellation. There is no deployed receiver or
runtime signer here; this is not a claim of an exploited or live integration flaw.

The proposed proof is now **version 2**, with signing domain
`mattermost-octop-intent-proof/v2\0`. It carries the original receipt's
`receipt_expires_at` in Unix milliseconds, copied from the source receipt and
included in the signed intent and conflict fingerprint. Both signer and verifier
require `proof.expires <= floor(receipt_expires_at / 1000)` as well as the existing
120-second maximum. They reject an excessive lifetime rather than silently
extend/clamp it; subsecond expiry rounds down conservatively. The receiver rejects
old v1 proofs; no permissive fallback or persistent ledger migration is added.
The receipt delivery-key domain remains v1 so changing the proof format does not
invent another identity for the same receipt. Changed source expiry or mapping
must conflict with any older inbox fingerprint, not reset deduplication.

### Signature verification is not current-source authorization

`Verify` remains a pure signature/binding/time check. **It still cannot detect
cancellation** and is never sufficient for intake or execution. New offline
`VerifyWithSource` requires an explicit trusted `SourceReader`, invoked after
proof validation and on every retry/duplicate. It compares the returned current
intent, including exact binding, receipt, goal and source expiry, and accepts
only `awaiting_octop_authorization`. Missing/error/unknown/cancelled/changed
records fail closed with a fixed error. It checks the proof again after the
lookup, rejecting lookup-time expiry, clock rewind or a cancelled context. There
is no installed reader, endpoint, secret provider or network path in this plugin.

A future reader must authenticate the source service and perform a primary-
confirmed lookup scoped by instance/binding/receipt, rechecking live source user,
Bot, membership, config and cancellation. It must reconstruct the record from
trusted state, not echo signed claims or return a cached/replica observation.
The adapter must impose and honor bounded deadlines; the Go seam cannot terminate
a callback that ignores context and creates no replacement timeout goroutines.
The receiver must separately recheck current Octop account/organization/Agent
access and trust-policy revocation. Policy snapshots and signatures cannot prove
those permissions are still current.

**A fresh read is still not a distributed cancellation lock.** Cancellation after
the lookup can race receiver commit. Before runtime wiring, define an auditable
source claim/cancellation state machine (including lost acknowledgements and
crash recovery), or a documented pending-intent cancellation protocol with a
fresh pre-execution check. No claim/lease/receipt-version CAS or zero-window
revocation guarantee is implemented here. Until that protocol is implemented and
tested, proof candidates must not trigger execution or be treated as task intake.

Tests use the actual local command fixture to cancel a previously signed receipt:
`Verify` still succeeds while `VerifyWithSource` rejects it. Additional cases
cover overlong signed proof expiry, conservative millisecond boundaries, missing
providers, failures, changed source content/expiry, repeated source checks and
expiry/cancellation during the lookup. These are offline checks, not evidence of
real source authentication or current permissions across two services.

### Explicit stable machine-ID mapping and legacy configuration

Source organization/employee fields now accept nonblank, valid UTF-8 opaque IDs
up to **128 bytes**, preserving the existing configuration's Unicode, punctuation
and significant whitespace. No trim, slug, case folding or Unicode normalization
occurs. Invalid UTF-8 cannot be represented losslessly by this JSON format and is
rejected; it is not automatically repaired. Two visually identical normalized
forms are different IDs. These strings are never interpreted as paths or URLs.
Instance/audience/key aliases keep the bounded ASCII-token format; Mattermost IDs
keep their 26-character stable-ID format.

The receiver-owned policy must explicitly map the exact source binding to stable
Octop **organization ID, worker ID and actor ID**. No display-name lookup, automatic
provisioning, or assumption that legacy labels equal Octop database IDs is allowed.
The new target IDs are required and returned only from trusted policy, never from
proof claims. All three participate in the conflict fingerprint; remapping cannot
silently reattribute an existing receipt. Future deployment provisioning must
verify those targets exist and are correctly related, version/revoke mappings,
and check live access transactionally. This code does not provide that registry or
resolve its targets. Tests preserve Unicode legacy configuration end to end and
reject missing mapping, normalization aliases and oversized IDs.

### Public cross-language vectors and remaining transport/inbox gates

`internal/handoff/testdata/vectors.json` contains ten **synthetic public** vectors,
with public keys only. `go test ./...` and
`python3 internal/handoff/testdata/check_vectors.py` verify the same proofs and
delivery/conflict hashes. The Python standard-library reference uses local
OpenSSL Ed25519 verification (Python 3 and OpenSSL with Ed25519 support required).
`make test` runs both. Vectors cover Unicode/HTML/line-separator escaping, expiry,
wrong binding, revoked trust, missing mapping, tampering and old protocol rejection.
They are not production credentials and do not connect to any service.

This is a tested **ordered Go JSON representation**, not an adopted canonical
JSON standard, JCS, JWT or a production Python/Octop verifier. The Python program
is a vector checker, not a hardened ingress parser. Broader differential/fuzz
testing and an agreed, versioned protocol are still required. The base64 payload
is decodable plaintext, **not encryption**: authenticated TLS, precise destination
policy, secret/executor isolation and restricted inbox storage/retention must be
implemented before transport. Never log proof bodies, goals or credential data.

Verification has no persistent replay prevention. A production receiver needs a
unique delivery key plus conflict fingerprint, pending-intent record and audit in
one durable transaction, with current authorization and quota checks. Verify
source/trust state before recognizing duplicates; unknown commit outcomes require
reconciliation, not a new request. Define tombstone retention and task-level
business idempotency separately. None of those persistent receiver guarantees,
network protections or runtime execution capabilities are delivered by this
offline slice. All 0.2.2/0.2.3 blockers below continue to apply.

## Offline request-proof boundary added in 0.2.3

This version adds executable offline validation for a proposed Mattermost →
Octop handoff, **not a connected handoff or an implemented Octop protocol**.
`handoff.go` converts an owned, pending, unexpired receipt snapshot into an intent;
`internal/handoff` signs and verifies that intent using Ed25519 and SHA-256 from
the Go standard library. No command, hook, HTTP route or background worker invokes
the conversion/signer. There is no network client, key loader/provider, configured
secret, endpoint, new enable switch or receipt-state transition. Plugin behavior
and its existing default-off settings remain unchanged.

### Source findings and why runtime wiring remains blocked

Inspected Octop branch `feature/akb-octop-litellm-dev` at commit
[`e2c78d8b192daafbc7d8dcb5970afd0e9b40891b`](https://github.com/jhzhfree/Octop/tree/e2c78d8b192daafbc7d8dcb5970afd0e9b40891b):

- `src/octop/api/middleware/jwt_auth.py` and `api/deps.py` authenticate existing
  user API requests. They are not a Mattermost service-identity/mapped-user intake
  contract. Reusing an operator's JWT would confuse service and user identity.
- `infra/digital_workers/mattermost.py` checks organization manager/admin and
  Agent access for readiness/intents. Intents remain unverified and cannot grant
  execution or sending. `infra/db/repos/mattermost_intents.py` rechecks permission
  predicates and revisions during its configuration write; it is not a task inbox.
- `infra/digital_workers/mattermost_verification.py` requires an exact offline
  MockTransport even at the lowest entry point. The readiness document explicitly
  blocks live credentials until broker/executor secret isolation is implemented.
  Draft metadata, Bot observation and publication preview cannot authenticate an
  inbound Mattermost request or authorize task execution.

Accordingly this implementation does not route text into Octop chat, accept a
fixed shared key, invent a service endpoint, or relax TLS/credential isolation.
No code from Octop was copied and no Octop repository was changed.

### What the offline code establishes

The caller must supply receiver-owned public-key trust, enabled state, key ID,
exact instance/audience/organization/employee/user/team/channel/Bot binding, and
the mapped Octop actor ID. The actor ID is absent from the sender's proof. The
signed intent preserves `user_requested`: an account submitted text, not proof
of a human at the keyboard, an approval, or an automatic execution identity.
Goal text remains untrusted data regardless of the signature.

Proofs use an explicit domain prefix, an issued/expiry interval of at most 120
seconds, and receiver-owned current time. No future-issued or expired proof is
accepted. Input is capped at 32 KiB; goal at 2000 Unicode code points; IDs and
scope labels are bounded. Canonical JSON validation rejects duplicate/unknown
fields (including fabricated authorization flags), alternate representations and
trailing content. Invalid input returns one fixed error without the goal/proof.
The proposed ordered JSON format uses Go's encoding, including HTML escaping;
it is not JCS/JWT/AKB identity SDK interoperability. Cross-language vectors and
protocol review are required before implementing an Octop endpoint.

Successful verification returns only a `Candidate`. A delivery key binds source
instance, audience and receipt ID. A fingerprint additionally binds the full
intent and receiver-mapped actor. Renewing proof time or rotating the signing key
does not create a new delivery key; modified content or actor mapping changes the
fingerprint. This is **receipt-level identity**, not business idempotency: a new
slash-command dispatch has another receipt and can represent the same business
request. Verification itself has no replay cache and accepts an otherwise valid
replay. It does not lock current permission, cancellation or configuration.

### Verification and remaining handoff gates

`make test` now includes subpackages. Tests create ephemeral random signing keys
only in memory and run without sockets, DNS, credentials or external services.
The actual command fixture produces a ledger receipt, converts it, signs it and
verifies it; the ledger remains pending and unchanged. Tests cover corrupted,
cancelled, expired and mismatched receipt snapshots; proof tampering; cross-scope
and cross-audience rejection; revoked keys/policies; unknown/duplicate fields;
size/time limits; and 32 concurrent deliveries into a **test-only in-memory inbox**.
The simulated inbox compares key/fingerprint atomically, detects conflicting
retries and checks revocation before recognizing duplicates. It is not a durable
Octop inbox, does not test crash recovery and never executes a goal.

Before runtime wiring: establish an independently isolated signer/broker and
receiver trust provisioning, key rotation/revocation and authenticated TLS target
policy; implement a reviewed Octop ingress with live user mapping and organization/
Agent authorization; atomically commit durable pending intake, replay/conflict
record and audit; define retention and recovery for ambiguous acknowledgements;
add a persistent sender outbox and reconcile the original receipt rather than
blindly create another request. Primary-confirmed receipt reads and live permission/
cancellation checks are required at issuance, with explicit in-flight revocation
semantics. Receipt conversion alone cannot provide those guarantees. Signing
provides integrity, not confidentiality; proof payloads contain goal text and must
never enter metrics/logs or be treated as public artifacts. Bound receiver work
and test database/cluster failures and cross-language behavior before real TLS
and isolated instance acceptance. There is no production secret custody here.

The 0.2.2 shared-budget interference, uncancellable RPCs, migration requirements,
tombstone exhaustion, delayed physical cleanup, separate server audit retention
and replica/unknown-write limitations below remain 1.0 blockers. No completion,
execution, delivery, approval or exactly-once claim is introduced by this slice.

This independent server plugin implements a small collaboration entry point:
an explicitly allowed human submits a goal for one configured digital employee,
receives a private receipt, and can query or cancel that receipt. Shared atomic
quotas bound admission rates, pending work, and retained records. It uses the existing
Mattermost plugin SDK and plugin KV store, without changing Mattermost core.

**This is local intake, not an Octop connector or a 1.0 integration.** There is
no worker, outbound HTTP client, execution, approval, scheduled task, user-facing HTTP
endpoint, message listener, or channel-post creation. No plugin has been
installed into a live Mattermost instance as part of implementation validation.

**Never put secrets or sensitive information in a slash command.** Mattermost
can audit the complete command before the plugin checks permissions or rejects
it. An ephemeral reply does not make the submitted command private from server
auditing. Seven-day receipt access expiry does **not** delete server audit logs,
exports, collector copies, or backups. Review the retention guidance below
before enabling intake.

## Build and test

Use Go 1.26.10 and the workspace generated by `make setup-go-work` in `server/`.
From this directory:

```sh
make test
make bundle
```

The Linux amd64 bundle is `dist/ai-tagent-intake-0.2.6-linux-amd64.tar.gz`.
The manifest requires Mattermost 12.2.0, the repository's development baseline;
compatibility with older servers is not claimed. The bundle retains the root
license unchanged. Building a plugin does not grant a commercial feature license.

For an offline build, use `GOPROXY=off` with a previously populated module cache.
Do not disable checksum verification or change networking policies to fetch
missing modules. The plugin depends on the repository's existing public SDK;
no new dependency or module version is introduced.

## Configuration for a future isolated test installation

The plugin defaults to disabled. Installation/activation is a separate action.
An administrator must provide all of these settings:

| Setting | Meaning |
| --- | --- |
| `Enabled` | Explicitly enable local intake |
| `QuotaStorageReady` | Operator confirms fresh/quiesced storage and synchronized node clocks; see upgrade gate below |
| `OrganizationID` | External organization identifier, at most 128 bytes |
| `EmployeeID` | Business digital employee identifier, at most 128 bytes |
| `BotUserID` | Existing, active, dedicated Bot account for that employee |
| `TeamID`, `ChannelID` | One allowed team and channel |
| `AllowedRequesterIDs` | Comma-separated human user IDs explicitly allowed to submit intake |

The plugin does not create users or Bots and needs no token or external secret.
Both the submitting human and configured Bot must be current channel members.
The human must be active, not a Bot, and have channel post permission. The
channel must be active and belong to the configured team. Configuration,
membership, and permissions are checked for every request and status lookup.
The local allowlist is an **intake permission only**: organization and employee
identifiers are administrator-provided labels, not verified Octop bindings.

## Commands and data

```text
/aitagent request Inspect service health
/aitagent status <64-character-receipt-id>
/aitagent cancel <64-character-receipt-id>
```

Goals must contain 1–2000 Unicode characters, subject to an 8192-byte command
limit. Do not submit credentials, secrets, or sensitive production output.
Receipts retain the plaintext goal and its SHA-256, real submitting user ID,
employee and Bot IDs, scope, binding hash, source, mode, and timestamps in the
bounded plugin KV ledger. Receipts become inaccessible after seven days; expired
entries are removed on a subsequent successful ledger update. **Cleanup is lazy**:
if the plugin is inactive, storage fails, or no commands arrive, expired plaintext
can remain physically stored beyond seven days. The ledger key has no database
TTL, so expiration of an old record cannot erase newer reservations. Cancellation
removes the goal from the current ledger value, but retains a hash/identity
tombstone until expiry. Neither operation deletes audit, export, or backup copies.

Responses are ephemeral command responses to the submitting user; the plugin
does not echo the goal, change the response username, or create channel posts.
Status lookup requires the original user, channel, team, and unchanged binding.
Changing any configuration value invalidates access under that new binding;
restoring the identical binding can restore access until the receipt expires.
This is not an immutable audit log or a permanent revocation ledger.

## Server auditing and operator configuration

There are separate retention domains:

| Data | Storage and retention control |
| --- | --- |
| Plugin receipt, including plaintext goal | Bounded KV ledger: seven-day access expiry; lazy cleanup on successful state operations; old data can remain while inactive or on failure; backups separate |
| Full slash command, including its goal | Mattermost audit targets, if configured and active: server logging, rotation, collectors and organizational retention policy |
| Copies and exports | SIEM/collectors, exports, backup systems and any proxy request-body logs: each system's own access and deletion controls |

Verified against this repository's source:

- `server/channels/api4/command.go`, `executeCommand`, creates an audit record
  and adds `command_args` **before** checking channel permission or calling
  the plugin. `server/public/model/command_args.go`, `Auditable`, includes the
  complete `command`. `AddEventParameterAuditableToAuditRec` snapshots this map;
  subsequent plugin validation or text handling cannot retract it.
- `server/channels/web/context.go`, `LogAuditRec`, normally uses `audit-api`
  (100); permission failures can use `audit-permissions` (102). Removing only
  `audit-content` (101) does not remove command text from these records.
- `ExperimentalAuditSettings.FileEnabled` and `FileName` control the basic file
  target. In `server/config/logger.go`, that target uses `max_size=100` MB,
  `max_age=0`, `max_backups=0`, and compression. Zero age and backup count do
  not clean up old files automatically. These are server defaults, not plugin
  settings; there is no plugin setting that enforces server audit retention.

Operator recommendations (not applied by this plugin or its build):

1. Keep intake disabled until administrators have reviewed the effective audit
   targets and their access/retention policies. Inspect the basic file target,
   `ExperimentalAuditSettings.AdvancedLoggingJSON`, and any targets added by
   `MM_EXPERIMENTALAUDITSETTINGS_ADDITIONAL`. `EffectiveAuditConfig` combines
   these; setting `FileEnabled=false` alone does not disable all audit sinks.
   Do not disable required audit records as a substitute for safe input.
2. Where the deployment's existing license permits advanced audit logging,
   use supported per-file `options.max_age`, `max_backups`, `max_size` and
   `compress` settings in **ExperimentalAuditSettings.AdvancedLoggingJSON**,
   not in `LogSettings` or plugin settings. Preserve required existing targets
   and levels. The provided [example](audit-target.example.json) is a target
   map, not a complete `config.json`; never replace the entire audit config
   blindly. It illustrates 30 days for rotated files, 10 backups and 100 MB
   rotation, solely for review against an approved organizational policy.
   It is not a universal retention recommendation or a seven-day deletion rule.
3. Review the basic `_defAudit` file and every collector separately: adding a
   bounded advanced target does not shorten retention of other copies. Keep
   log paths inside the configured `MM_LOG_PATH` root (or the default `logs`
   directory), with restricted filesystem/collector access. If advanced settings
   are not licensed, use supported operational retention procedures for the
   existing sinks; do not bypass feature checks.
4. File `max_age` applies to **rotated** files using their rotation timestamp.
   It is not a per-record expiry for the active file; cleanup follows logger
   rotation/open activity. Validate actual rotation and deletion in an approved
   isolated environment. Backups and exports require their own lifecycle rules.
5. For sensitive work, submit only a non-sensitive opaque reference or minimal
   summary. Do not include bearer/signed URLs, access tokens, passwords, customer
   content, or confidential evidence. This plugin does not resolve references
   or provide an alternate secure upload path. If a secret is accidentally
   submitted, treat all relevant sinks as potentially affected and follow the
   organization's credential rotation and incident/retention procedures; deleting
   a KV receipt or waiting seven days is insufficient.

The example is checked offline with the repository's audit target validator
using a temporary file path. That validates configuration parsing and target
construction, not a deployment's license, effective configuration, permissions,
logging behavior, rotation timing, or deletion across systems. No live logging
configuration was inspected or changed. See also the
[official logging documentation](https://docs.mattermost.com/administration-guide/manage/logging).

## Trust boundary and replay behavior

- The entry point is Mattermost's server-side `ExecuteCommand` plugin hook.
  The server supplies the session user and generates a trigger before dispatch.
  There is no HTTP endpoint accepting caller-supplied identity headers or form
  fields. Only a trusted Mattermost server/plugin runtime may call the hook.
- A goal is untrusted text, never a source of identity, execution permission,
  approval, destination, or operation mode. The plugin stamps
  `source=mattermost_plugin_command`, `operation_mode=user_requested`, and
  `state=awaiting_octop_authorization` itself. Only an authorized owner can
  change a local pending receipt to `cancelled`; no execution state is invented.
- `user_requested` means an authenticated non-Bot account requested the work,
  not proof of a person at a keyboard: a user session/token can also be automated.
  This plugin does not perform the work or infer a human execution mode. Future human-operated and autonomous
  execution must record actual per-operation actors and authorization sources
  separately while preserving the same business employee identity.
- All Bot actors, including the target employee Bot, are rejected. There are
  no passive message hooks, so a Bot report cannot trigger intake recursively.
- A SHA-256 of the server trigger, actor, and channel identifies a dispatch.
  A shared-ledger CAS gives one receipt for duplicate delivery of that dispatch
  during seven-day retention; changed content or binding conflicts are rejected.
  The trigger token itself is not stored. A newly submitted slash command gets
  a new server trigger and is a new request, even if its text is identical.
  Replay protection is bounded by retention, not permanent.
- Storage errors return a generic response. An uncertain write includes the
  receipt ID for lookup and never initiates execution or external delivery.
  A missing/unavailable receipt is not proof that a write did not commit;
  never automatically create a new dispatch based on a missing replica read.

## Admission quotas and distributed consistency (0.2)

These fixed pilot limits apply to all configured bindings sharing this plugin
ID and Mattermost database. They do not span separate Mattermost instances.
Organization IDs are administrator-configured labels, not independently verified
Octop organizations. The installation cap remains in force when bindings change.

| Scope | New admissions in a rolling 60 seconds | Pending receipts |
| --- | --- | --- |
| Authenticated user ID, across bindings/organizations | 4 | 8 |
| Configured organization ID | 16 | 32 |
| Entire plugin installation | 32 | 64 |

Additional resource bounds: 128 retained receipts including cancellation
tombstones, 2 MiB serialized ledger, at most four in-flight commands **per plugin
process**, and at most eight CAS attempts per ledger operation. There is no
unbounded retry queue. Saturation returns a private busy/limit response. These
are admission limits, not a general API/DDoS rate limiter: rejected commands,
status reads and cancellation do not consume shared admission rate. In 0.2.1
they consume separate process-local work limits described below. Configure existing
Mattermost/reverse-proxy ingress protection separately; audits occur upstream.

Cancellation releases the pending slot only. It does not refund the rolling
rate or remove the tombstone from the 128-record cap. Repeating a cancelled
dispatch returns its cancelled receipt, never a new pending request. After a
successful operation prunes expired receipts, their slots can be reused. Because
there is no Octop worker, nothing is automatically marked completed. A binding
change never resets shared quotas; it can prevent cancellation under the new
binding until the original binding is restored or the receipt expires.

The 128-record cap intentionally can stop a pilot for the rest of its retention
window even when pending slots remain. Do not delete/reset the ledger to free
capacity: that would lose reservations, rate history and replay protection.

Source-level guarantees and limits:

- `server/channels/store/sqlstore/plugin_store.go` performs CAS against the
  primary database: nil old value inserts under a unique key; a non-nil old
  value updates only matching bytes of an unexpired row. A conflict returns
  false. `Get` and `ListWithOptions` read from replicas and may be stale. A nil
  insert's expired-row cleanup and insert are separate statements, not a
  multi-key transaction. Equality at the expiry boundary can temporarily fail
  both a read and an insert. The ledger deliberately uses **no row TTL**.
- All receipts, pending counts (derived from receipt states), and rolling rate
  history (derived from admission times, including cancelled records) live in
  one `intake_ledger_v2` value. One CAS commits admission/reservation or
  cancellation/release together. No independent counter can get out of sync.
  A monotonically increasing revision prevents a prior byte snapshot from
  matching again after state returns to superficially similar contents (ABA).
- Each successful status result is also confirmed by CAS, so a stale replica
  cannot return an obsolete pending status as confirmed current. Conflicts
  trigger a bounded reread/retry; continuing lag returns busy. A stale snapshot
  can conservatively reject a request even when newer state would permit it.
- Storage errors have an uncertain outcome: no blind retry or compensation is
  performed. Look up the receipt, then retry the same dispatch only if safe.
  A newly submitted slash command has a new server trigger. **Dispatch dedupe
  is not business idempotency**, an exactly-once task guarantee, or permanent
  replay protection. Future Octop handoff needs a durable business operation ID.
- Shared admission-rate windows and expiry use plugin-node wall clocks. Nodes must have
  synchronized clocks; backward time relative to the stored high-water mark
  fails closed. Forward jumps cannot be distinguished from elapsed time using
  this API and can shorten a real-time window. No strict wall-clock guarantee
  is claimed under arbitrary skew. Permission checks are fresh per command,
  but are not one database transaction with the ledger CAS. As with ordinary
  server requests, revocation racing an already-authorized command is not an
  instantaneous global fence. Downstream actions must reauthorize independently.
- There must be no external writer, restore, deletion or mixed-version process
  modifying the ledger. Malformed/oversized state fails closed; it is never
  silently replaced with zero counts. The byte-size check is after the KV API
  returns data; it bounds decoding/accepted writes, not upstream database/RPC
  allocation for externally corrupted oversized values.

### Upgrade/storage gate

Both `Enabled` and `QuotaStorageReady` default to false. The latter is an
**operator acknowledgement, not an automatic readiness test**. A fresh isolated
installation is the supported starting point. Before enabling an upgrade from
0.1.x, stop admission on all nodes, stop old plugin writers, wait out/reconcile
all legacy receipt lifetimes and verify authoritative database state and replica
catch-up. Verify node clock synchronization. Do not run a rolling mixed-version
upgrade or reset live data to pass this check. The new implementation has no
automatic migration and does not provide status access to legacy receipts.

When the ledger is absent, an additional prefix lookup rejects visible
`request_` legacy keys. Because that lookup uses a replica, an empty result
cannot certify a clean primary or the absence of old writers. If that deployment
verification cannot be performed, leave `QuotaStorageReady=false`: safe upgrade
is blocked. No deployment readiness was certified in the offline tests.

## Process-local overload protection (0.2.2)

After the rejection-only configuration prefilter described below, and before
per-command configuration/identity/KV RPCs, each process applies a rolling
60-second work limiter to eligible command attempts from a server-supplied user
ID. For prefilter-eligible callers, rejected live authorization, storage failure
and duplicate dispatch attempts still consume this local budget. There are no refunds on cancellation or errors.

| Process-local scope | Attempts per rolling 60 seconds |
| --- | --- |
| One user: `request`, including duplicate dispatches | 4 |
| One user: `status` | 6 |
| One user: `cancel` | 4 |
| One user: combined command types | 12 |
| One plugin process: all users/types | 60 |

The limiter keeps at most 60 events and four running command records. It does
not allocate an unbounded user map or queue. It uses the local monotonic clock
where available and has no distributed coordination: another node or a process
restart has a different/empty work budget. These are **not cluster-global read
or cancellation limits**. The existing shared admission/pending quotas still
apply independently. Requests rejected by the prefilter or local limiter do not call plugin APIs,
but can already have been captured by the server's command audit pipeline.

Successful `status`, same-dispatch replay and repeat cancellation still confirm
the snapshot with a whole-ledger CAS. This deliberately retains the primary-side
confirmation from 0.2; the new limits bound the frequency per process, rather
than silently downgrade status to a confirmed-looking replica read. Whole-ledger
write amplification and hot-key contention remain scaling limits. Cluster-wide
ingress budgets and a future authoritative bounded-read/store design must be
verified before 1.0; adding nodes multiplies the process-local allowance.

### Rejection-only scope prefilter (0.2.2)

In 0.2.1, the 60-attempt process budget was charged before allowlist/channel
checks. Other authenticated accounts invoking the command in their own channels
could consume that budget even though the plugin subsequently refused them.
That was cross-scope availability interference, not tenant isolation.

Version 0.2.2 loads a small immutable rejection snapshot during activation and
`OnConfigurationChange`. It contains the configured team/channel and a map of
the administrator-provided requester IDs; the existing 8192-byte configuration
limit bounds that map. Incoming caller IDs never grow the cache. Requests outside
that snapshot are rejected **before** the process limiter and all per-command
API calls, without storing per-caller history. The plugin trusts only command
metadata supplied by Mattermost; `api4/command.go` sets the user from the session
and derives team from the channel. Goal text cannot provide identity or scope.
This does not prevent the server's upstream parsing, permission checks or audit
work, so server-level ingress limits remain necessary.

The cache is **only a rejection filter**, never an authorization result. Every
eligible request still loads current configuration and checks current user/Bot,
membership and post permission through the guarded APIs. An old snapshot cannot
grant revoked access. A newly eligible caller may be conservatively rejected
until a successful configuration refresh; commands do not trigger refresh RPCs
on cache misses, which would recreate the unauthenticated resource problem.

Refresh invalidates the snapshot before loading. A failed or overlapping refresh
keeps intake closed; an old in-flight result cannot overwrite a newer
invalidation. At most one synchronous configuration load is allowed per process,
with no queued refresh workers. If overlapping events supersede the first load,
an administrator must retry a supported configuration refresh/reactivation after
recovery. This callback is an additional lifecycle RPC outside the four command
slots and has no cancellable SDK deadline. A permanently blocked refresh requires
runtime recovery; the plugin does not spawn retries or reset the ledger. The
`config_prefilter_ready` gauge means only that a valid configuration snapshot was
loaded, not that permissions, Bot status, transport or the deployment are ready.

Residual negative scenarios are tested and remain 1.0 gates:

- Configured, scope-matching callers still need live checks. For example,
  60 different listed accounts can have valid upstream command permission but
  fail the plugin check while the target Bot is disabled. Their attempts fill
  the shared process budget; after the Bot is re-enabled, another legitimate
  caller can still be rate-limited until the window advances. Stale cached scope
  can also conservatively admit work to a live check that will reject it. This
  mitigation is not tenant fairness, tenant-independent availability, or a
  cluster-global request limiter.
- A **single** overdue command RPC stops new eligible work even if three slots
  are free. This is the existing deliberate circuit behavior, not isolation of
  the caller causing the slow RPC. The slot remains occupied until the actual
  call returns, then the 30-second process cooldown applies. Tests demonstrate
  the rejection, metrics, absence of replacement RPCs, and recovery after release.
- Configuration refresh failure closes intake for all callers of that process.
  Config-event delivery/coherence across actual Mattermost nodes is not verified.

Before 1.0, tenant/account identity boundaries and fair, authenticated resource
budgets must be designed and validated at the appropriate server/ingress layer,
including known-caller abuse and backend failures. Neither this snapshot nor
`QuotaStorageReady` is proof of tenant isolation or safe migration.

### Work budget, RPC cancellation and recovery

Source inspection of `server/public/plugin/client_rpc_generated.go` shows that
`KVGet`, `KVSetWithOptions` and other APIs call synchronous `net/rpc.Client.Call`.
Their signatures accept no context/deadline. `plugin.Context` contains metadata,
not cancellation. `api_timer_layer_generated.go` records elapsed time after the
API returns; it does not cancel it. The supervisor's start timeout is not a
per-command deadline. The SDK can log a transport error and return zero values,
such as `nil,nil` for a read or `false,nil` for CAS. Therefore a missing read is
not proof of absence, and a false CAS is not proof that the write did not commit.

The plugin enforces a **cooperative two-second work budget**, checking before
and after each synchronous API call and before further CAS work. A late write
is treated as uncertain, with the original receipt ID for reconciliation. CAS
non-confirmation retries wait for exponential delays capped at 100 ms plus
0–10 ms jitter, at most seven waits/eight attempts; a retry is skipped if its
wait would exhaust the remaining work budget. API errors are not blindly
retried. No background goroutine races a timer against an API call, no timed-out
worker is abandoned, and no slot is released while the issued call is still
running.

**This is not a hard RPC timeout or a two-second response guarantee.** An issued
API call can block indefinitely. At most four such command paths can occupy a
process; new commands fail immediately when the process is full or any running
command is overdue. When a late call finally returns, its slot is released and
the process enters a 30-second cooldown. No cleanup worker, self-restart, or
automatic storage compensation is spawned. If a call never returns, recovery
requires the managed server/database/transport or a supervised plugin restart;
the plugin cannot safely provide cancellation on its own. Do not reset its
ledger as recovery. Authoritative outcome reconciliation is required before
retrying uncertain work, especially after a restart resets local limits.

### Observability without more API calls

The standard `ServeMetrics` hook exposes a small, fixed set of process-local
Prometheus-style metrics through the **existing Mattermost metrics listener**,
at `/plugins/com.aitagent.intake/metrics`, only when that listener/hook is
available under the deployment's normal configuration and license. This plugin
does not enable it, add a public `ServeHTTP` endpoint, alter authentication, or
bypass licensing. Administrators must protect the metrics listener and configure
scraping through supported deployment controls. Runtime exposure was not tested.

Metrics include `aitagent_process_inflight`, `oldest_work_seconds`,
`overdue_commands`, `cooldown_seconds`, and counters with the same
`aitagent_process_` prefix for `commands_started_total`, `rate_rejected_total`,
`overload_rejected_total`, `budget_exceeded_total`, `cas_not_confirmed_total`,
`cas_exhausted_total`, `observed_storage_errors_total`, and
`uncertain_writes_total`. Version 0.2.2 adds `config_prefilter_ready` and
`config_refresh_inflight` gauges, plus `prefilter_rejected_total` and
`config_refresh_failed_total` counters with the same prefix. A budget-exceeded counter increments when the command
returns; overdue gauges expose work that has not returned yet. CAS
non-confirmation includes false results that may hide transport failures; error
counters cannot detect failures swallowed by the SDK. Metrics are reset on
restart and are not a durable audit trail or cluster totals.

Collection uses only bounded local state and atomics, with no plugin API, KV,
log write, user/organization labels, goal content or receipt IDs. The handler
remains independent of command API calls in unit tests; actual plugin RPC and
metrics-listener availability under a real outage remain unverified.

## Validation and remaining gates

Unit tests exercise the real hook implementation against a strict in-memory
plugin API fake, including concurrent replay, spoofed identity in goal text,
Bot rejection, revoked membership/permission, disabled users/Bots, scope and
binding changes, expiration, storage errors, and private responses. Unexpected
API calls fail tests. These tests do not establish real server RPC dispatch,
database atomicity/expiration, installation, or end-to-end Octop delivery.

Quota tests use exact-byte CAS against an authoritative in-memory map, separate
stale-read snapshots, multiple independent plugin instances, and injected errors
both before and after commit. They cover all quota scopes, rolling-window
boundaries, cancellation without rate refunds, tombstone capacity, expiry,
failed cleanup, backward clocks, local concurrency bounds and malformed state.

Work-protection tests cover concurrent rate accounting, each command type,
combined limits, process isolation, expired-budget guards, retry jitter bounds,
late authorization/read/write returns, committed writes with lost transport
acknowledgements, missing replica reads, and a blocked-API test that retains all
four slots until the test explicitly releases the calls. The test verifies that
we do not pretend an uncancellable call has timed out or launch replacement
workers. All test goroutines are released/joined; no live server is contacted.

Scope tests additionally show 640 concurrent foreign requests consume no
configuration RPC or local work budget while legitimate intake succeeds, verify
cache invalidation and stale-refresh rejection, and preserve the negative tests
for eligible-but-denied callers exhausting shared capacity and a single overdue
RPC blocking other callers. Simulated blocked configuration calls are always
released/joined; there is no background refresh worker.

An optional `python3 tests/postgres_cas.py` check uses an already-cached official
`postgres:15` image on the managed Docker socket. It creates a disposable
network-disabled container with no host ports and synthetic data only, tests
the reviewed SQL insert/CAS/expiry primitives under concurrency, then removes
the container. It never pulls an image. This is not a test of Mattermost's store
wrapper, replica routing, plugin RPC, or deployment-specific database behavior.

Before broader use: validate the bundle on an authorized isolated Mattermost
instance; verify command dispatch, private responses and real KV behavior;
implement authenticated Octop identity mapping and per-action authorization;
define a durable handoff/outbox with reconciliation; add approval invalidation,
separate human/automatic execution evidence, deployment-level request rate limits, retention/deletion
operations and an external audit policy. Do not treat a local receipt as an
approved, queued, executed, or externally delivered Octop task.

Explicit 1.0 blockers also include end-to-end hard timeout/recovery behavior in
the supported plugin runtime, cluster-wide overload controls, hot-key/write
amplification assessment, live metrics routing, authoritative reconciliation of
uncertain/missing receipts, verified clock behavior, and a tested quiesced
upgrade path. `QuotaStorageReady` remains an operator declaration, not migration
proof. A full 128-record ledger can stop intake until retention expires; no
successful operations or an inactive plugin can delay physical goal cleanup.
None of these limits is waived by the local work limiter or metrics endpoint.
