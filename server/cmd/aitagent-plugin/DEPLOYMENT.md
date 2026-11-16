# Real-instance acceptance preparation (0.2.15)

**Blocked. No real Mattermost/Octop deployment was started.** This runbook records
actual prerequisites and acceptance criteria. It is not a runnable production
launcher, and no boolean setting turns the offline helpers into a service.

## Environment evidence, 2026-11-16

- Go 1.26.10 and Node 24.22.1 are installed; plugin compilation works.
- Docker is reachable; only `postgres:15` is cached. No matching Mattermost image
  or built server binary was found in the prepared toolchain. PostgreSQL-only
  fixtures do not constitute a Mattermost instance.
- Offline `go build ./cmd/mattermost` fails because seven required modules are
  missing: imagemeta 1.1.0, circl 1.6.3, compress 1.20.3, mattermost-plugin-ai
  1.14.2, minlz 1.2.3, lz4/v4 4.1.33 and modernc.org/sqlite 1.61.1.
- One normal official-proxy attempt for `github.com/klauspost/compress@v1.20.3`
  redirected to `storage.googleapis.com` and failed with `Forbidden`. No alternate
  proxy, direct-source substitution, checksum bypass or network-policy change was
  attempted. Other missing modules were not individually probed.
- `bwrap` exists, but `bwrap --unshare-net --ro-bind / / -- true` fails with
  `setting up uid map: Read-only file system`. This does not establish usable
  bubblewrap isolation; no kernel/security setting was changed.

Reproduce the local inventory, including offline compilation and isolation probes:

```sh
GOPATH=/path/to/prepared/gopath GOCACHE=/path/to/prepared/go-cache \
python3 tests/live_preflight.py --go /path/to/go \
  --probe-build --probe-isolation --report /tmp/live-preflight.json
```

Exit **2** means blocked; **1** is an inventory error. It never returns deployment
approval. Without probe flags it records those checks as unverified. The script
never downloads, starts services, reads credentials or searches for user instances.
It records source hashes and revision, not raw environment/DSN/secrets. Build probes
force GOPROXY/GOSUMDB off and the local toolchain; a 60-second timeout is reported as
unverified, not a compiler failure. Network evidence above was a separate one-off
check; the reproducible inventory itself performs no external network probe.

## Source review uncovered two additional integration gaps

1. **KV primary confirmation does not certify primary authorization.**
   `channels/store/sqlstore/plugin_store.go` uses GetReplica for Get and GetMaster
   for CompareAndSet. Our old-value CAS confirms the receipt/nonce ledger on the
   primary. However PluginAPI.GetUser delegates to the user service/store;
   `localcachelayer/user_layer.go:Get` can return a cached profile. Invalidated
   profiles may force a master read, but the API call itself cannot require it.
   `app/authorization.go:HasPermissionToChannel` explicitly uses cache-backed
   GetAllChannelMembersForUser. Membership reads use DBXFromContext; our plugin
   API cannot choose a primary-only request context or a transaction covering all
   rights and receipt state. Repeating these APIs before/after CAS checks again
   against their current view, not a linearizable snapshot of revocation.
   A single-node/single-DB fixture alone cannot close cache/failover races. No
   direct SQL permission rewrite or server-license change was made.
2. **There is no runtime reservation or sender composition.** `reserveHandoff`
   has test-only call sites. A real `/aitagent` receipt lacks the correlated journal
   until a trusted reserver validates/signs the original intent and atomically
   associates it. Mounting a route by itself cannot make existing local receipts
   queryable. The default source reader correctly rejects missing journals.

`SOURCE.md`'s “current checks” mean re-invocation of the plugin APIs, subject to
these consistency limits. They must not be described as guaranteed instantaneous
primary authorization or a revocation fence. Production readiness stays false.

## Exact route and service identity preparation

Mattermost core mounts `/plugins/{plugin_id}/…` (`app/channels.go`) and strips the
plugin prefix before invoking ServeHTTP (`app/plugin_requests.go`). Thus a future
reviewed plugin adapter would receive:

- external MM route: `/plugins/com.aitagent.intake/api/integrations/octop/v1/source-observations`
- plugin-local path: `/api/integrations/octop/v1/source-observations`

Octop currently fixes its source URL to `https://mattermost-source.invalid/api/…`
and accepts only exact MockTransport. These are **not directly deployable paths**.
A separately reviewed TLS ingress/target registry must map that origin/path to the
plugin prefix while preserving the original body and rejecting arbitrary origins,
redirects, duplicate auth headers, query parameters, encoding and oversized bodies.
Do not relax the transport restriction merely to get a green demo.

Core removes client-supplied Mattermost-User-Id and Mattermost-Plugin-ID on external
requests and may populate session identity. Neither header is an Octop service
credential. The future adapter must validate the independently registered broker
signature; a user login, admin token or bot token must never stand in for it.
No live ServeHTTP implementation, signer loader or resolver is added in this phase.

Prepare separate broker signer, source signer and proof signer security domains;
registry pins broker/audience/instance/org/employee/human/Bot/team/channel and current
key/revision. Keep ephemeral test-only keys out of models, chat, logs, browser and
source control. A human-authored goal is data, not execution authority. A durable
independent restore floor/epoch and current authorization strategy need review
before providing a runtime constructor.

## Staged isolated acceptance procedure (not yet executable end to end)

1. Obtain the exact source revision's missing dependencies via an allowed official
   path, with normal Go checksum verification, or a provenance-verified artifact
   built from that revision. Re-run the build probe. Do not substitute an unrelated
   released server and label it this fork. Preserve license/signature requirements.
2. Prepare disposable DBs, tmpfs data, no host credential mounts, no published host
   ports and no egress. Use separately isolated processes/containers for Mattermost,
   Octop and signers. A container-local shared network namespace may provide local
   connectivity, but is not key isolation. Prove file/UID and signer access denial
   from other workers. No host firewall/kernel changes are required or authorized.
3. Boot only the temporary matching server after isolation is verified. Use a
   unique empty database; disable diagnostics/Sentry, marketplace/prepackaged
   downloads, email/push/webhooks and external storage in its temporary config.
   Egress isolation is the enforcement boundary; config switches alone are not.
   Keep plugin signing policy intact, using a verified test signing process if
   required. Do not upload an unsigned bundle by weakening server security.
4. Provision only synthetic human/Bot accounts and a private test team/channel;
   validate allowlist and memberships. Do not import real backups, accounts or
   existing session tokens. No actual posts, notifications or worker execution are
   acceptance prerequisites. Obtain receipts through the controlled synthetic
   intake path only after verifying response delivery cannot reach real users.
5. Implement/review the missing trusted reservation composition, authoritative
   authorization strategy, HTTP adapter, independent signer/registry, and approved
   Octop transport as separate artifacts. Until then **stop here**: neither this
   runbook nor the preflight supplies these security components.
6. With those gates satisfied, exercise the matrix below using original identities,
   pinned TLS endpoints, disposable DB users and redacted evidence. Tear down all
   temporary containers/data/certificates afterwards; retain only revision/hash,
   expected/actual outcomes and non-sensitive counters. This cloud workspace is
   not durable evidence storage; the committed runbook is the reusable artifact.

## Acceptance matrix and required evidence

| Scenario | Required observable result | Current evidence |
| --- | --- | --- |
| Forged MM user/plugin headers, user JWT, wrong broker | No signed source evidence, no authority substitution | Code review + isolated signature tests; real core routing untested |
| Legitimate source query | Original journal identity; primary-confirmed receipt/nonce CAS; current independently verified rights | Fake-MM Go CAS only |
| Revoke actor/Bot/channel/role/config on another node | No fresh authorized evidence after the agreed revocation fence; bound cache/replica lag explicitly | Blocked on authority consistency strategy |
| Delayed replica / unknown CAS commit | No stale signed result; reconcile original receipt with fresh challenge | Fake API failures covered; actual replica topology untested |
| Nonce replay / restart / clock or version rollback | Durable rejection; externally retained restore floor survives source restore | Local persisted ledger simulation; real failover/epoch untested |
| TLS wrong host/key/expired certificate; signer cross-access | Fail closed before source or receiver acceptance | Not tested; no live transport |
| Octop 403/409/503 after observing newer source | Independently authenticated GET confirms both version fields; never infer zero writes or invent a new ID | 0e950452 ASGI/SQLite contract, not production |
| Cancel racing intake / permission change | Pending remains non-executable; correct reconciliation; no exactly-once claim | Isolated components only |
| Nonce/intake capacity and audit/cleanup failure | Bounded fail-closed operation, no automatic unsafe deletion | Existing limits; peer cleanup and role acceptance remain blocked |

For a live pass, record service build SHA, image digest, plugin hash/signature,
DB schema/role identity, actual transport peer identities, fault injection point,
original receipt/delivery identity (hashed for reports), both source version fields,
state/revision and cleanup result. Do not log goal, signed payload, private keys,
headers or session cookies. Seven-day plugin retention does not constrain server
audit retention. Full build, true dual-service integration and production deployment
remain explicitly **not verified**.

## This phase's recorded checks

Local preflight exited 2 with the expected blocked report; no instance was started.
Octop 0e950452 actual ASGI/SQLite ingress contract passed 24 HTTP attempts with
one intake, two business audit events and two source-observation events, schema 46.
Its original source adapter passed six Go/Python pipe contract assertions. Go race
suites, ten Go/Python/OpenSSL vectors and tagged go vet passed. Plugin bundle build
is separate from the failed full-server build. The earlier 41-case Go PostgreSQL
suite was not repeated this phase because no runtime Go/storage code changed; it
must not be counted as current real-instance or Octop PostgreSQL validation.
