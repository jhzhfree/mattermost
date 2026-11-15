# AI-TAgent local request intake (0.2.4)

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

The Linux amd64 bundle is `dist/ai-tagent-intake-0.2.4-linux-amd64.tar.gz`.
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
