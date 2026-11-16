# Current source query boundary (0.2.14; deployment limits updated in 0.2.15)

See [DEPLOYMENT.md](DEPLOYMENT.md): primary ledger CAS does not guarantee primary
authorization reads; the rechecked plugin APIs may use cached/replica views. Current
source-contract runner pins 0e950452; 43506ab below is the original protocol baseline.

Pinned peer: [Octop 43506ab70675efd82d793a7ee6d9bc3e8de55436](https://github.com/jhzhfree/Octop/tree/43506ab70675efd82d793a7ee6d9bc3e8de55436),
`docs/ai-tagent/mattermost-source-adapter.md` and
`src/octop/infra/digital_workers/mattermost_source.py`.
The plugin now implements that isolated query/response profile. It is a private
wire handler, **not ServeHTTP**, and has no route, plugin configuration switch,
production constructor, network client or credential loader. A zero value denies
all work. Only test composition supplies trust resolution and signing callbacks.
No real source endpoint has been enabled.

## Actual authentication and current state

Only POST `/api/integrations/octop/v1/source-observations`, exact
`application/json`, and bodies ≤4096 bytes are accepted. Broker ID and signature
are supplied by the hypothetical HTTP adapter's `X-Octop-Source-Broker` and
`X-Octop-Source-Signature` headers. No HTTP adapter is installed. The Go boundary
verifies Ed25519 over `octop-mattermost-source-query/v1\0` plus the **received
bytes**, against the currently resolved broker key. It rejects unknown, duplicate,
mis-cased or missing JSON keys; no Go canonical re-encoding is required.

Request fields are exactly `protocol_version=1`, `broker_id`, `nonce` (64 lower
hex), `issued_at` (UTC seconds), `binding`, `receipt` (64 lower hex). Binding fields
are exactly instance/audience/organization/employee/user/team/channel/bot, all
matched to the trusted resolver value; a caller cannot select another actor or
organization. Request time must satisfy `issued <= now < issued + 5`.
Broker, proof and source response public keys must all differ. Distinct keys are
necessary but do not prove separate secret/process isolation.

The trusted resolver runs before access, after guarded KV confirmation and after
signing. Changed enablement, keys, binding, registry revision or minimum ledger
version discards the result. Its state is injected in tests, not a deployed trust
registry. The signer is purpose-scoped by composition and its output is verified
against the pinned source public key before return. Neither callback reads secrets
from files/environment in this implementation.

The source lookup reuses `withHandoffReceipt`: current plugin configuration,
actor/Bot, channel/team and allowlist permissions are checked before access and
again after CAS. The exact journal binding, original commitment, identity and
expiry must exist. For an active receipt the commitment is recomputed from the
current receipt. Cancelled receipts preserve their original journal commitment
without restoring goal text. Expired/missing/legacy receipts yield unavailable,
never an absence or cancellation confirmation.

### Primary confirmation and durable replay boundary

`KVGet` may read a replica. A successful `KVSetWithOptions(Atomic:true,
OldValue:raw)` compares the entire ledger value on the primary; no observation
escapes without that confirmation. The same CAS consumes the authenticated nonce,
updates the ledger revision and reads the receipt. Concurrent identical queries
can produce at most one successful response. Unknown commit returns no evidence;
a new nonce querying the **original receipt** is the recovery path. Restarting the
boundary does not reset nonce state. This is not an exactly-once response guarantee.

`source_nonces` is an optional map in the existing ledger, capped at 128 entries
for the **whole installation**, not per tenant. Keys hash broker ID and nonce;
values expire when the signed query expires. Pruning is lazy during authorized
queries. No source nonce survives indefinitely as an accepted replay: a request
at/after its original deadline is rejected even when its record is pruned. This
map does not contain goal, proof, signatures or private keys. Shared ledger/process
budgets can still create cross-tenant availability interference.

The CAS version must be at least the trusted `MinimumVersion` and remain within
signed int64. This detects a restored ledger below an externally retained floor;
it does NOT automatically maintain that floor or detect simultaneous rollback of
DB and trust registry. A durable restore epoch/watermark protocol remains a gate.
The existing ledger LastNow rejects clock rollback relative to shared state.

### Response and cancellation race

Envelope fields: `key_id`, `payload`, `signature`; latter two standard Base64.
Response signature covers `mattermost-octop-source-observation/v1\0` plus decoded
payload bytes. Payload fields: `protocol_version=1`, `request_sha256`,
`observed_at`, `expires_at`, `observation`. Expiry is the original query's five-second
deadline, never extended by signing latency. Observation contains only delivery_key,
fingerprint, commitment, state, receipt_expires_at (milliseconds) and version
(decimal string). State is awaiting_octop_authorization or cancelled.

The digest binds the exact query including its random challenge. No goal or old
proof is returned. This is evidence of a guarded observation, not an execution
lease. Cancellation/revocation may race after the final check; source CAS and
Octop's transaction are not atomic. A signature cannot authorize execution.

A one-second context bounds cooperative work; clock/deadline checks occur before
return. There is no goroutine that abandons blocked RPC/signing work. Existing
budgetAPI keeps occupied Mattermost RPC slots until completion. A non-cooperative
callback/RPC can still block its caller; this is not hard process isolation or a
production wall-clock timeout guarantee. Invalid requests do not reach KV, but
pre-auth CPU/network admission limits require a real deployment boundary.

## Upgrade and rollout gates

Stop **all** old writers before enabling any future source route. Older JSON
writers discard `source_nonces` and may discard journal commitment. Do not roll
versions concurrently. Verify primary/replicas and legacy associations; wait out
old query windows under a validated clock policy. QuotaStorageReady remains an
operator declaration, not automatic migration. Current source code remains off.

Still required: independent broker/source signer deployment, durable current trust
registry and restore floor, key rotation/revocation procedures, trusted HTTP header
and size/error mapping, TLS/target restrictions, real plugin primary/replica tests,
source failover and physical restore tests, and Octop production mounting review.
Do not weaken its exact MockTransport restriction. Keep pending execution and
actual sending off. Existing capacity/lifecycle, uncancellable RPC and server audit
retention blockers in INTEROP.md remain. Seven-day local retention does not delete
server audit logs; never put secrets or sensitive information in slash commands.

## Reproduce the actual cross-language boundary

Use the already prepared dependencies from [INTEROP.md](INTEROP.md). The peer
`uv.lock` is byte-identical between 247b456 and 43506ab (SHA-256
`b1c0d702fb6f8076e7c37c8282147ff85ca97973d58f27850db466575f872f75`).
The runner never installs or fetches. Local Git objects must contain the pinned
commit. It exports unmodified peer source to a temporary directory.

```sh
PYTHONPATH="$CONTRACT_DEPS" GOPROXY=off make source-contract \
  GO=/path/to/go OCTOP_GIT_DIR="$OCTOP_GIT_DIR" REPORT=/tmp/source-contract.json
```

Octop's **actual** OfflineMattermostSourceReader creates/signs a fresh query;
its exact MockTransport passes the bytes over a local pipe to the actual Go wire
boundary. Go uses the fake Mattermost plugin API implementing CAS, not a real MM
server, then returns a real signed response verified by the original Python code.
Three independent ephemeral keypairs stay in the Go test process. No private key
is serialized. The synthetic bridge is compiled only with `octopcontract` and
activated only by the explicit test harness. Python socket/DNS calls are denied;
the bridge uses pipes and exits under the Go test timeout. No production services,
HTTP sockets, TLS, real accounts or remote messages are involved.

Six cross-language assertions: active state, persisted nonce replay rejection,
old response/new challenge rejection, cancellation preserving original identity
and advancing version, current Mattermost permission revocation, current source
broker-registry revocation. This checks the source adapter, not the entire Octop
intake transaction chain; the separate 247b456 ingress test remains available.
Go race tests additionally cover concurrent duplicate query, unknown commit then
reconciliation, replica lag, registry change, version floor, clock rollback, query
expiry, cooperative signer timeout, key reuse, wrong binding/signature/route,
strict JSON, capacity and expired nonce cleanup. They do not prove cross-process
real Mattermost storage behavior or a deployed trust store.

## Recorded checks (2026-11-16)

Six actual Go/Python source-adapter contract cases passed at the pinned peer.
The separate 247b456 ingress contract still passed all 21 HTTP attempts (one
intake/two audit rows). Go race suites, 10 public Go/Python/OpenSSL vectors,
`go vet -tags=octopcontract ./...`, and 41 isolated Go PostgreSQL cases passed;
the disposable database container was removed. A linux-amd64 0.2.14 bundle was
built with unchanged repository license. These are plugin-focused tests, not full
Mattermost/Octop suites or production acceptance.
