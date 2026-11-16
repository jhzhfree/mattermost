# Isolated Go ↔ Octop contract verification (0.2.13)

Pinned receiver: [Octop 247b4567f2feb20756f79cc9a461b2daa118c116](https://github.com/jhzhfree/Octop/tree/247b4567f2feb20756f79cc9a461b2daa118c116).
This supersedes the **historical** c9fd4021 ingress-absent finding in PROTOCOL.md.
The new receiver factory exists, but is not mounted in Octop's production app.
This is an executable interoperability check, not installed dual-service acceptance.

## Actual bytes and identity

The Go exporter creates synthetic receipts through the plugin's fake Mattermost
API, reserves the source journal through the same ledger CAS, and obtains active
and cancelled source observations from the guarded helpers. It signs proof v2
and independent broker requests with two ephemeral Ed25519 keys. No private key
is saved. The Python harness exports the exact receiver Git object into a temporary
directory and calls its **unmodified** FastAPI router, service, proof verifier,
authorization repository and SQLite migrations through in-process ASGI. It does
not replace authentication dependencies or repository methods. The source callback
uses exported Go snapshots: it is NOT a live, fresh Mattermost RPC or a cancellation
race test. The harness forbids Python socket/DNS calls during receiver execution;
this test guard is not a general sandbox. It does not modify an Octop checkout.

| Method/path | Request | Verified result |
| --- | --- | --- |
| POST `/api/integrations/mattermost/v1/intakes` | Exact Go v2 proof bytes; octet-stream, ≤32768 bytes | 201 pending; same identity + fresh nonce returns 200 |
| GET `…/intakes/{delivery_key}` | Empty body, no query | Original identity inspection, including after proof expiry |
| POST `…/intakes/{delivery_key}/cancellations` | Strict JSON `operation_id` (64 lower hex), `expected_revision` (1–8); ≤1024 bytes | Current cancelled source evidence → terminal revision 2; duplicate stable |

Broker headers are `x-octop-broker`, `x-octop-broker-time`,
`x-octop-broker-nonce`, `x-octop-broker-signature`. The signing input is
`octop-mattermost-broker-request/v1\0` followed by UTF-8 newline-joined broker ID,
decimal UTC seconds, 64-lower-hex nonce, uppercase method, exact path, and
lower-hex SHA-256 of the exact body; no final newline. Signature is standard
base64 Ed25519. A fresh random nonce is required for **every attempt**, including
retries following a 503: authentication consumes the nonce before business work.
The receiver checks `issued <= now < issued + 30`. User JWTs do not substitute for
broker identity. Binding/actor/org/worker are server-registered, not client claims.

Successful responses have exactly `protocol_version`, `delivery_key`,
`fingerprint`, `state`, `revision`, `source_version` (decimal **string**), and
`execution_authorized=false`; responses use `Cache-Control: no-store`.
No response is permission to execute. A missing/unknown response must be reconciled
using the original identity and new broker authentication, never a new request ID.
The signer is a byte-level helper with no HTTP client, credential loader or runtime
caller. It does not yet implement a deployed sender adapter/response parser.

## Compatibility fix and upgrade boundary

Octop cancellation verifies the **original intent commitment and source expiry**,
as well as key/fingerprint/current version. Previously our cancellation evidence
omitted those fields; clearing the goal on cancellation prevents recomputation.
The journal now saves the original commitment in the same CAS as the receipt
association and returns it with the unchanged expiry after cancellation. A legacy
journal with no commitment remains readable for local status/cancellation, but
cannot export a correlated source observation, reserve again, or provide remote
cancellation evidence. It is not silently upgraded or assigned a new identity.
Stop all old writers before upgrading; old JSON writers can drop this field.
Inventory legacy associations and reconcile under the original identity before
any live rollout. This change does not make QuotaStorageReady an automatic migration.

## Reproduce without a service or network

Prerequisites: existing local Git objects at the exact peer SHA; Go 1.26.10 and
cached Mattermost modules; Python 3.12+ with peer dependencies available. The runner
never fetches/installs. Use an isolated Python environment and peer `uv.lock`
constraints; do not use arbitrary latest versions. For the reviewed run the used
versions included FastAPI 0.138.2, Pydantic 2.13.4, cryptography 49.0.0,
httpx 0.28.1, octop-gateway 1.0.0 and octop-harness 1.0.2. Packages were installed
from `https://pypi.org/simple` into a dedicated test directory. To reproduce the
constraint file from the already available Git object:

```sh
git --git-dir="$OCTOP_GIT_DIR" show 247b4567f2feb20756f79cc9a461b2daa118c116:uv.lock > /tmp/octop-contract.lock
python3 - <<'PY' > /tmp/octop-contract-constraints.txt
import tomllib
from collections import defaultdict
with open('/tmp/octop-contract.lock', 'rb') as f:
    packages = tomllib.load(f)['package']
versions = defaultdict(set)
for p in packages:
    if p.get('source', {}).get('registry') == 'https://pypi.org/simple':
        versions[p['name']].add(p['version'])
for name, values in sorted(versions.items()):
    if len(values) == 1:
        print(name + '==' + next(iter(values)))
PY
# Dependency preparation only, when official PyPI access is permitted:
uv pip install --target "$CONTRACT_DEPS" --index-url https://pypi.org/simple \
  --constraint /tmp/octop-contract-constraints.txt octop-harness==1.0.2 \
  octop-gateway==1.0.0 fastapi==0.138.2 pydantic==2.13.4 cryptography==49.0.0
# Verification itself uses no network:
PYTHONPATH="$CONTRACT_DEPS" GOPROXY=off make interop \
  GO=/path/to/go OCTOP_GIT_DIR="$OCTOP_GIT_DIR" REPORT=/tmp/interop.json
```

21 HTTP attempts cover missing/JWT/substituted body/path/expired authentication,
shared proof/broker key rejection, source failure and burnt-nonce replay, valid
intake and retry, current permission revocation, strict cancel validation,
active-source cancel rejection, terminal cancel and duplicate, stale-active
resubmission rejection, database close/reopen recovery, inspection after proof
expiry, and default-disabled router. Assertions require one intake and exactly
two transactional ingress audit events. Reports record both revisions, working
tree dirtiness, dependency versions and explicit test boundaries. Existing race,
Go/Python/OpenSSL vector and isolated PostgreSQL suites remain separate checks.

## Remaining gates to 1.0

- No live Mattermost source adapter, independently isolated broker identity/key
  provider, key rotation/revocation rollout or target-origin registry is deployed.
  Distinct public keys do not prove separate security domains.
- No network HTTP/TLS/mTLS, certificate validation, proxy path preservation,
  real account/permission revocation, real installed plugin or production routing
  was tested. The `https://fixture.invalid` ASGI base URL does not test TLS.
- SQLite close/reopen is not process crash, multi-node concurrency or partial
  commit fault injection. Octop PostgreSQL migration/least-privilege roles and
  audit tamper resistance were not verified here. The separate local Go PostgreSQL
  suite does not prove Octop production DB permissions.
- Source cancellation may race after observation. Pending authorization cannot
  become executable without fresh source/target/version/expiry and local authority
  checks. No distributed atomicity or exactly-once guarantee is claimed.
- The peer's 128 intake rows per broker and 4096 retained nonce cap lack a tested
  production lifecycle/cleanup policy; long-running use may fail closed at capacity.
  Our prior lifecycle components are not automatically wired to this peer schema.
- Keep existing shared process budget/uncancellable RPC, primary/replica ambiguity,
  old-writer migration and delayed physical cleanup limitations. Seven-day plugin
  retention does not constrain Mattermost audit logs; never place secrets/sensitive
  data in slash commands. Signed proof is readable, not encrypted: transport,
  inbox and logs require protection. No goal belongs in operational diagnostics.

Execution, posting, automatic network sending and production verification remain
closed. Only this Mattermost repository is changed; no license or paid-feature
restriction is modified.

## Recorded validation (2026-11-16)

`make interop`: 21 expected HTTP outcomes, one intake, two audit rows, schema 45.
`make test`: Go race suites and 10 Go/Python/OpenSSL vectors passed.
`go vet -tags=octopcontract ./...`: passed.
`tests/postgres_inbox.py`: all 41 existing isolated Go PostgreSQL cases passed;
the disposable container was removed. This is not the Octop PostgreSQL suite.
`make bundle`: linux-amd64 0.2.13 bundle built with unchanged repository license.
No full Mattermost server/webapp build or Octop full test suite was run.
