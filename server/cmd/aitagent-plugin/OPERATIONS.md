# Local diagnostics and manual recovery (0.2.16)

This phase adds an actual operator entry point using Mattermost's standard support
packet hook. It does not add a second metrics system, external alert sender,
scheduler, HTTP endpoint, automatic retry or storage reset.

## Collect an intentionally selected local snapshot

When generating a support packet through the existing Mattermost administrator UI,
select **AI-TAgent** in the plugin data options. The manifest `props.support_packet`
provides this opt-in; core `channels/app/support_packet.go` calls the hook only when
the plugin is selected. The plugin produces `aitagent-local-diagnostics.json`.
Follow the server's normal permissions, configuration and license requirements;
this hook does not enable or bypass any of them. Actual installed UI/RPC collection
remains untested because the full server build is blocked (DEPLOYMENT.md).

The JSON contains schema version, fixed process scope, the same 16 values as the
existing metrics hook, fixed recovery hint codes, and this runbook filename.
Both hooks use one snapshot implementation. Counters remain integer values; values
above JavaScript's exact integer range are not converted to floats by the plugin.
Consumers needing exact counters must preserve JSON integer precision.

Collection reads bounded local state and atomics only. It does not invoke plugin
APIs, KV, SQL, signers, source/receiver services or filesystem configuration, even
when four command slots are occupied. It includes no user/org/team/channel/receipt
IDs, goal, proof, signature, token, session, configuration or arbitrary error text.
The report explicitly says it is neither authoritative receipt/permission evidence
nor a durable audit. Snapshot fields can reflect slightly different concurrent
instants; counters are process-local and reset on restart. A new zero value is
not proof that an older uncertain write was resolved.

The **entire server support packet** may separately contain logs, config or other
plugins' data. This plugin's redaction does not sanitize that content or make it
safe to share automatically. Review the server's selection/retention rules and the
whole packet before sharing it. This implementation sends nothing to a recipient.

## Interpret and recover without guessing a request outcome

| Hint or observation | Meaning | Manual next step |
| --- | --- | --- |
| `configuration_disabled_or_unready` | The local prefilter is closed; intentional disablement and failed/stale refresh are indistinguishable here | Check intended configuration in the normal admin controls. Do not turn on execution or invent a storage-readiness acknowledgement just to clear the hint. |
| `configuration_refresh_in_progress` | A configuration load is active | Check plugin/server dependency health. Do not create repeated refreshes while a prior load is blocked. A successful later supported refresh is needed before intake reopens. |
| `overdue_work_requires_dependency_review` | At least one synchronous command/API call exceeded its budget | Stop new submissions in the affected workflow; investigate plugin RPC and database health. The plugin keeps the slot occupied rather than spawning replacement calls. Capture evidence before a controlled operational restart. |
| `historical_unknown_commit_reconcile_original_receipt` | This process observed an uncertain write at some time, with no per-request identity in this snapshot | Use the original receipt retained by its owner. After dependency recovery, an authorized owner can use `/aitagent status <original-id>`. Missing/denied/expired is not proof of absence; do not create another request to resolve uncertainty. |
| `historical_storage_errors` | One or more surfaced storage errors occurred; may already have recovered | Compare against a later snapshot from the same process lifetime and server health. Do not clear ledger rows or audit history. |
| `historical_cas_confirmation_failures` | CAS retries exhausted; contention, replica lag or ambiguous SDK errors are possible | Investigate primary/replica and transport health. Preserve the original identity; no unconditional KV overwrite or retry-by-new-ID. |
| `historical_process_rate_limit` | This process rejected work under its shared rate budget | Reduce repeated request/status/cancel attempts and respect cooldown. This is not a tenant-specific or cluster-wide quota report. |
| `historical_configuration_refresh_failures` | A refresh failed or was superseded at some earlier time | Correlate with the current ready/inflight gauges. A historical count alone does not mean current configuration is broken. |

An empty hint list does not prove that Mattermost/Octop is healthy, authorized,
connected or reconciled. These counters do not expose ledger contents, older
processes, swallowed SDK failures or the separate offline cancellation queue.
No UI button in this phase re-arms exhausted retries, resets counters/quotas,
changes roles, or deletes a tombstone. Current cancellation queue components are
not wired to a real receiver, so the support packet does not fabricate queue
backlog or end-to-end cancellation status.

If an RPC never returns, a controlled plugin/server restart may be operationally
necessary, but it does not roll back an issued database write or certify an outcome.
Preserve original identities, assess dependencies, and use approved incident
procedures. Reconcile after recovery. Do not erase the ledger, reset the source
version floor, re-enable migrated Octop brokers or replay chat text as recovery.

## Use existing monitoring, not a new alert channel

Where the deployment already supports/protects Mattermost's metrics listener,
reuse `aitagent_process_overdue_commands`, `config_refresh_inflight`,
`config_prefilter_ready`, and increases of uncertain-write/storage/CAS counters.
An overdue gauge persisting across scrapes is a useful human investigation signal;
historical counters alone are not an active incident. Planned disablement must not
page as a failed service. Scrape failure and counter reset are separate signals.
Alert routing/thresholds belong to the organization's existing monitoring setup;
no rule is installed, no email/Slack/chat message is sent, and paid metrics controls
are unchanged. The opt-in support snapshot supplements those metrics during
manual diagnosis; it is not a replacement for authenticated live monitoring.

## Acceptance and remaining boundaries

Tests verify opt-in manifest presence, nil-API collection, no identity/context
leakage, a fixed bounded schema, preservation of large counters, agreement with
the metrics hook, four overdue slots without state mutation, historical failure
hints, fresh-process limitations and concurrent collection under the race detector.
This is tested hook behavior, not a claim that the actual administrator UI,
cluster support-packet aggregation or plugin RPC remains available during an outage.

No source/sender trust or permission guarantee is added. Receipt CAS still does
not make cached/replica-backed permission checks a primary authorization snapshot.
Real build dependencies, authoritative revocation strategy, runtime reservation,
independent signers/registry, TLS and dual-service acceptance remain blocked as
listed in DEPLOYMENT.md. The seven-day local access period does not govern server
audit logs or exported support packets. No production instance was accessed.
