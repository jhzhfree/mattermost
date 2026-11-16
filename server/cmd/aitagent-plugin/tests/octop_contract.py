"""Execute Go-signed requests against unmodified Octop 247b456 ASGI/SQLite code.

No source checkout changes, no production app, no socket/TLS or real credentials.
Dependencies must already be installed; this runner never installs or fetches.
"""
from __future__ import annotations
import argparse
import asyncio
import base64
import importlib.metadata
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tarfile
import tempfile

REVISION = "247b4567f2feb20756f79cc9a461b2daa118c116"
ROOT = Path(__file__).resolve().parents[1]


def deny_network(*args, **kwargs):
    raise AssertionError("contract runtime forbids network/DNS")


async def exercise(fixture, directory):
    # Import actual peer classes, with NO auth dependency override or repo stub.
    from fastapi import FastAPI
    import httpx
    from octop.api.routers.mattermost_ingress import create_router
    from octop.infra.db.pool import SqlitePool
    from octop.infra.db.migrate import run_migrations
    from octop.infra.db.repos.mattermost_ingress import MattermostIngressRepo
    from octop.infra.digital_workers.mattermost_ingress import MattermostIngressService, SourceObservation

    policy = fixture["policy"]
    dbpath = directory / "contract.db"
    db = SqlitePool(dbpath)
    run_migrations(db)
    actor, org, worker = policy["OctopActorID"], policy["OctopOrganizationID"], policy["OctopEmployeeID"]
    with db.transaction() as c:
        c.execute("INSERT INTO users(id,username,password_hash,role,permissions,created_at) VALUES(?,'contract-user','synthetic-only','user','[\"tasks\"]',1)", (actor,))
        c.execute("INSERT INTO agents(agent_id,name,user_id,created_at,updated_at) VALUES('contract-agent','Synthetic Agent',?,1,1)", (actor,))
        c.execute("INSERT INTO organizations(organization_id,owner_user_id,name,created_at,updated_at) VALUES(?,?,'Synthetic organization',1,1)", (org, actor))
        c.execute("INSERT INTO organization_members(organization_id,user_id,role,created_at) VALUES(?,?,'owner',1)", (org, actor))
        c.execute("INSERT INTO organization_positions(position_id,organization_id,title,created_at,updated_at) VALUES('contract-position',?,'Synthetic position',1,1)", (org,))
        c.execute("INSERT INTO organization_assignments(assignment_id,position_id,subject_type,subject_id,created_at) VALUES('contract-assignment','contract-position','agent','contract-agent',1)")
        c.execute("INSERT INTO digital_workers(worker_id,organization_id,position_id,agent_id,owner_user_id,name,status,capabilities_json,created_at,updated_at) VALUES(?,?,'contract-position','contract-agent',?,'Synthetic worker','active','{}',1,1)", (worker, org, actor))
        c.execute("INSERT INTO mattermost_brokers VALUES(?,?,1,1,?,?,?,1,?,?,?)", ("fixture-broker", fixture["broker_public_key"], org, worker, actor, policy["KeyID"], base64.b64decode(policy["PublicKey"]).hex(), json.dumps(policy["Binding"])))
    state = {"now": fixture["now"], "cancelled": False, "error": False, "calls": 0}

    async def source(mapping, receipt):
        state["calls"] += 1
        assert mapping["broker_id"] == "fixture-broker"
        if state["error"]:
            raise RuntimeError("synthetic source unavailable")
        x = fixture["cancelled"] if state["cancelled"] else fixture["active"]
        return SourceObservation(delivery_key=x["DeliveryKey"], fingerprint=x["Fingerprint"],
            commitment=x["Commitment"], state="cancelled" if state["cancelled"] else x["State"],
            receipt_expires_at=x["ReceiptExpiresAt"], version=x["Version"])

    def app_for(database):
        app = FastAPI()
        app.include_router(create_router(MattermostIngressService(MattermostIngressRepo(database), source=source, clock=lambda: state["now"])))
        return app

    app = app_for(db)
    results = []

    async def send(name, status, *, headers=None, path=None, body=None, selected_app=None, label=None):
        request = fixture["requests"][name]
        raw = base64.b64decode(request["Body"] or "") if body is None else body
        async with httpx.AsyncClient(transport=httpx.ASGITransport(app=selected_app or app), base_url="https://fixture.invalid", trust_env=False) as client:
            response = await client.request(request["Method"], path or request["Path"], content=raw, headers=request["Headers"] if headers is None else headers)
        assert response.status_code == status, (name, response.status_code, response.text)
        assert response.headers["cache-control"] == "no-store"
        value = response.json()
        if status in (200, 201):
            assert set(value) == {"protocol_version", "delivery_key", "fingerprint", "state", "revision", "source_version", "execution_authorized"}
            assert value["delivery_key"] == fixture["delivery_key"] and value["fingerprint"] == fixture["fingerprint"]
            assert value["execution_authorized"] is False and isinstance(value["source_version"], str)
        if status not in (200, 201):
            assert set(value) == {"code"} and isinstance(value["code"], str), value
        results.append({"case": label or name, "http_status": status, "code": value.get("code")})
        return value

    try:
        await send("missing_auth", 401, headers={})
        await send("missing_auth", 401, headers={"authorization": "Bearer synthetic-not-a-broker"}, label="jwt_not_broker")
        await send("wrong_path", 401, path=fixture["requests"]["wrong_path"]["Path"][:-64] + "b" * 64)
        await send("wrong_body", 401, body=b"changed")
        await send("expired_auth", 401)
        with db.transaction() as c:
            c.execute("UPDATE mattermost_brokers SET broker_public_key=proof_public_key")
        await send("missing_auth", 503, label="shared_key_rejected")  # distinct source/proof keys required
        with db.transaction() as c:
            c.execute("UPDATE mattermost_brokers SET broker_public_key=?", (fixture["broker_public_key"],))
        state["error"] = True
        await send("source_error", 503)
        state["error"] = False
        await send("source_error", 409, label="failed_business_nonce_replay")  # failed business call did not refund nonce
        first = await send("create", 201)
        duplicate = await send("duplicate", 200)
        assert first == duplicate and first["state"] == "pending_authorization"
        await send("create", 409, label="accepted_nonce_replay")
        await send("inspect", 200)
        with db.transaction() as c:
            c.execute("UPDATE users SET permissions='[]' WHERE id=?", (actor,))
        await send("revoked", 403)
        with db.transaction() as c:
            c.execute("UPDATE users SET permissions='[\"tasks\"]' WHERE id=?", (actor,))
        await send("bad_cancel", 422)
        await send("cancel_not_ready", 403)
        state["cancelled"] = True
        cancelled = await send("cancel", 200)
        assert cancelled["state"] == "cancelled" and cancelled["revision"] == 2
        assert await send("cancel_duplicate", 200) == cancelled
        state["cancelled"] = False  # stale/re-enabled active observation cannot revive identity
        await send("revive", 410)
        state["cancelled"] = True
        db.close()
        db = SqlitePool(dbpath)
        app = app_for(db)
        assert await send("inspect_after_restart", 200) == cancelled
        state["now"] += 61  # Go proof expired; independently signed GET remains valid
        calls = state["calls"]
        assert await send("inspect_expired_proof", 200) == cancelled
        assert state["calls"] == calls
        unavailable = FastAPI()
        unavailable.include_router(create_router())
        await send("inspect_expired_proof", 503, selected_app=unavailable, label="default_disabled")
        with db.connect() as c:
            count = c.execute("SELECT count(*) FROM mattermost_intakes").fetchone()[0]
            audit = c.execute("SELECT count(*) FROM audit_log WHERE action LIKE 'mattermost.intake.%'").fetchone()[0]
            schema = c.execute("SELECT version FROM _schema_version").fetchone()[0]
        assert count == 1 and audit == 2, (count, audit)
        return {"status": "passed", "octop_revision": REVISION, "cases": results,
                "intake_rows": count, "ingress_audit_rows": audit, "schema_version": schema,
                "source": "Go fake-plugin-API snapshots, not a live source service",
                "receiver": "unmodified Octop ASGI/service/repository/SQLite migrations",
                "tls_tested": False, "production_mounted": False, "execution_enabled": False,
                "dependencies": {p: importlib.metadata.version(p) for p in ("fastapi", "pydantic", "httpx", "cryptography", "octop-gateway", "octop-harness")}}
    finally:
        db.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--octop-git-dir", required=True)
    parser.add_argument("--go", default="go")
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="aitagent-octop-contract-") as temp:
        directory = Path(temp)
        env = dict(os.environ, GIT_ALLOW_PROTOCOL="", GIT_NO_LAZY_FETCH="1", GIT_TERMINAL_PROMPT="0")
        archive = subprocess.check_output(["git", "--git-dir=" + args.octop_git_dir, "archive", REVISION, "src/octop"], env=env)
        source_root = directory / "peer"
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
            tar.extractall(source_root, filter="data")
        fixture_path = directory / "requests.json"
        go_env = dict(os.environ, GOPROXY="off", CGO_ENABLED="0", AITAGENT_OCTOP_EXPORT=str(fixture_path))
        subprocess.run([args.go, "test", "-tags=octopcontract", "-run=^TestExportOctopContract$", "-count=1", "."], cwd=ROOT, env=go_env, check=True, timeout=120)
        sys.path.insert(0, str(source_root / "src"))
        sys.dont_write_bytecode = True
        os.environ["OCTOP_HOME"] = str(directory / "home")
        for name in ("connect", "connect_ex", "sendto"):
            setattr(socket.socket, name, deny_network)
        socket.getaddrinfo = deny_network
        socket.create_connection = deny_network
        report = asyncio.run(exercise(json.loads(fixture_path.read_text()), directory))
        report["mattermost_git_head"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
        report["working_tree_dirty"] = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True).strip())
        Path(args.report).parent.mkdir(parents=True, exist_ok=True)
        Path(args.report).write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report))


if __name__ == "__main__":
    main()
