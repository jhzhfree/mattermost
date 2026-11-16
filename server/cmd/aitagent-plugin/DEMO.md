# Cancellation delivery demonstration and two-service readiness

This is a runnable **isolated simulation**, not a Mattermost/Octop deployment.
Neither publishing a chat message nor executing a digital employee task is enabled.
No credentials, real DSN or service URL are accepted by the demo runner.

## Reproduce the delivered demonstration

Prerequisites: repository-required Go toolchain/dependency cache, Python 3,
Docker socket at `/var/run/docker.sock`, and the already available official
`postgres:15` image. The runner uses `GOPROXY=off`, `--pull=never`, `--network none`,
a tmpfs database, local Unix socket connections, no host ports and automatic removal.
It fails if prerequisites are unavailable; it does not install dependencies or
relax network rules. The temporary PostgreSQL uses fixture-only local trust, not
production credentials or a production authentication pattern.

From `server/cmd/aitagent-plugin`:

```sh
GOPATH=/workspace/toolchains/gopath GOCACHE=/workspace/toolchains/go-cache \
  make demo GO=/workspace/toolchains/go/bin/go REPORT=dist/demo-report.json
```

Adjust toolchain/cache paths for an equivalent approved environment. `make demo`
runs two complementary cases and writes a dated JSON report only after both pass:

| Case | Observable evidence | What it does not prove |
| --- | --- | --- |
| `source_adapter_chain` | Fake Mattermost command → primary-CAS source association → sender journal → receiver kernel → automatic cancellation confirmation | A running Mattermost server, authenticated network service or Octop ingress |
| `late_intake_auto_cancel` | First automatic attempt finds no receiver row; a late intake commits; the next scheduled attempt records cancellation and confirms it, with exactly two durable attempts in this scenario | Guaranteed global delivery, real network timing or exactly-once execution |

The second case uses separate application and inspection PostgreSQL roles. The
first uses an administrator fixture connection for receiver setup/operations and
fake plugin APIs; do not count it as production least-privilege acceptance. These
are two scenarios, not two independently deployed services talking to each other.
The report deliberately states `real_mattermost=false`, `real_octop=false`,
`outbound_enabled=false` and `execution_enabled=false`.

The scheduler runs one pinned target scope serially, immediately scans and then
waits 1–60 seconds between completed passes. It discovers at most 16 authorized
due identities per pass, honors the existing durable five-second claim and
1–60 second retry delays, and never overlaps its own passes. Multiple process
instances still rely on database claim fencing. Normal constructor defaults deny
work; no plugin startup hook or configuration setting installs this scheduler.
A missing cancellation permission adapter cannot be replaced with intake rights.

Local statistics/events expose attempts, confirmation, attention and failures
without goals, keys or actor details. The event buffer is bounded and may drop
observations (with a counter); **this is not an operator alerting service**. Database
state/audit remains the recovery source. No Slack, email, webhook or chat alert is
sent. An uncooperative RPC may stall the one worker; it is not detached, killed or
silently retried concurrently by this scheduler.

Full safety regression:

```sh
GOPATH=/workspace/toolchains/gopath GOCACHE=/workspace/toolchains/go-cache \
  make test GO=/workspace/toolchains/go/bin/go
GOPATH=/workspace/toolchains/gopath GOCACHE=/workspace/toolchains/go-cache \
  python3 tests/postgres_inbox.py --go /workspace/toolchains/go/bin/go
```

The full suite includes permission denial, concurrent claims, missing audit,
unknown commits, late-worker fencing, source expiry and a new OS process discovering
persisted work. Database restart/failover and production recovery are not covered.
Cloud workspace reports and build bundles are temporary, not permanent storage.

## Actual two-end gap, checked 2026-11-16

Mattermost baseline: `b9b7ae0c7eb188ab5cbc5dea3d3dc98fd3ddb21f` plus this scheduler
change. Octop was freshly fetched **read-only** at
[`f5efc2db4d7a630f643d624b79a60c9c250af2fb`](https://github.com/jhzhfree/Octop/tree/f5efc2db4d7a630f643d624b79a60c9c250af2fb),
branch `feature/akb-octop-litellm-dev`. Its code/tests were inspected, not executed
or modified. This supersedes reliance on the older Octop snapshot in historical
README sections; it does not certify later commits.

| Area | Actual source behavior | Gate before a genuine demonstration |
| --- | --- | --- |
| Mattermost input | Plugin slash-command intake binds server-owned user/team/channel/Bot identity; handoff methods have no live route | Install into a disposable Mattermost and implement/review authenticated broker endpoints |
| Octop organization/employee intent | `src/octop/infra/digital_workers/mattermost.py` reports unverified/unbound, disconnected, no send/execution permission | Server-owned stable ID mapping and current organization/Agent authorization; never map by display name |
| Octop identity verification | `mattermost_verification.py:observe_bot_identity` requires exact `httpx.MockTransport`; real transport is rejected | Independent broker/secret provider and executor isolation acceptance; preserve the hard gate until reviewed |
| Octop publication | `api/routers/mattermost_publication.py` supports local candidate, preview, approval and revocation; connected/dispatch/execution remain false | Separate reviewed outbound protocol; preview approval must not be treated as a send API |
| Incoming handoff | No implementation matching this Go proof domain, source version protocol or inbox schema was found in the inspected Octop source | Implement a real ingress/inspection/cancellation adapter with canonical byte compatibility and transactional audit |
| Persistence/operations | Current SQL kernels are standalone fixtures; bounded retry and simulation scheduler now run | Actual Octop database migration, runtime roles, retention, monitoring, operator recovery and restore epochs |

Octop's [readiness document](https://github.com/jhzhfree/Octop/blob/f5efc2db4d7a630f643d624b79a60c9c250af2fb/docs/mattermost-integration-readiness.md)
identifies a concrete secret-isolation blocker: local Agent execution can share
host-root access/environment and UID privilege with the API. File permissions,
`SecretStr` or a config flag do not resolve this. Do not put a real Bot token into
that API/Agent permission domain. Its
[publication document](https://github.com/jhzhfree/Octop/blob/f5efc2db4d7a630f643d624b79a60c9c250af2fb/docs/mattermost-publication-preview.md)
also explicitly keeps dispatch disabled. This work changes neither project's
license nor these safety gates, and copies no Octop implementation into Mattermost.

## Reproducible future two-service acceptance plan — blocked today

This plan is not an executable live connector and contains no invented API URLs.
There is currently no safe `curl` command that joins the two products.

1. **Prepare isolated services and record their exact revisions.** Use a disposable
   Mattermost, an Octop test deployment, dedicated test databases and a separate
   broker permission domain. No production tenants, credentials, network routes or
   executor access to secret volumes, broker memory, Docker socket or host PID
   namespace. Demonstrate isolation failures are denied before provisioning even
   test service credentials. Keep all external execution/posting disabled.
2. **Define and implement the missing API contract.** Pin issuer/audience, stable
   instance/organization/employee IDs, authenticated source principal, allowed
   Bot/team/channel and mapped receiver actor. Specify canonical signed bytes,
   expiry, key rotation, current source reads, cancellation, original-identity
   inspection and restore epochs. Review TLS/service identity and independent
   local authorization. Neither chat text nor claimed actor fields authorize work.
3. **Create one synthetic business scenario through authenticated surfaces.** A
   human requester asks the digital employee to inspect a test case. Record the
   human initiator separately from the Bot/service actor; exercise both human-led
   and automatic modes only as non-executing provenance records. Verify pending
   intake plus audit and exact key/fingerprint at both ends; do not send a post.
4. **Inject cancellation and uncertain outcomes in the test environment.** Delay
   intake commit until after cancellation, lose the receiver response after commit,
   restart only the test sender worker, deliver duplicates and let an old claim
   expire. Reconcile the same identity. Require observed receiver terminal state,
   immutable audits, no new receipt and no execution/post side effect.
5. **Exercise actual authorization and operational faults.** Revoke requester,
   organization, Agent and Bot/channel rights; change mapping/config/key versions;
   send wrong-audience/cross-organization/expired/conflicting requests. Verify
   denial without leakage. Test database restart/restore, source pruning, 16-claim
   exhaustion, 128/4096 capacity boundaries, failure alerts and an explicitly
   authorized manual recovery procedure. No production acceptance until these
   mechanisms exist and pass; do not reset/delete unknown records to make a demo
   succeed.

Acceptance evidence must include pinned revisions, authenticated actor provenance,
permission-isolation results, redacted state/audit transitions, actual runtime
roles, failure/restart results, retention/upgrade decisions and proof that no
external action occurred. Passing the delivered isolated demo satisfies only the
local scheduling/cancellation portion of this plan, not 1.0 or live integration.
