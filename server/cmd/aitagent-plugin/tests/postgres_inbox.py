"""Run offline Go source/inbox integration tests in an isolated cached PostgreSQL.

No host ports, no external network, no real DSN/credentials, no image download.
The Go binaries connect only to the container's ephemeral local Unix socket.
Builds use the existing module/cache with GOPROXY=off; no dependencies are added.
"""

import argparse
import datetime
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--go", default="go")
    parser.add_argument("--demo", action="store_true", help="Run two complementary isolated scenarios, not live services")
    parser.add_argument("--report", help="Write a successful demo report as JSON")
    args = parser.parse_args()
    if args.report and not args.demo:
        parser.error("--report requires --demo")
    root = Path(__file__).resolve().parents[1]
    env = os.environ.copy()
    for key in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"):
        env.pop(key, None)
    env.update(GOPROXY="off", CGO_ENABLED="0")
    base = ["docker", "--host=unix:///var/run/docker.sock"]
    name = "aitagent-inbox-test-" + uuid.uuid4().hex[:12]

    def docker(*argv, check=True, timeout=30):
        return subprocess.run(base + list(argv), env=env, capture_output=True, text=True,
                              check=check, timeout=timeout)

    docker("image", "inspect", "postgres:15")
    with tempfile.TemporaryDirectory(prefix="aitagent-offline-inbox-") as directory:
        binaries = []
        for package, binary in ((".", "source.test"), ("./internal/handoff", "inbox.test")):
            target = str(Path(directory) / binary)
            subprocess.run([args.go, "test", "-c", "-tags=offlinepg", "-o", target, package],
                           cwd=root, env=env, check=True, timeout=120)
            binaries.append((target, binary))
        created = False
        scenarios = []
        try:
            docker("run", "--detach", "--rm", "--pull=never", "--name", name,
                   "--network", "none", "--tmpfs", "/var/lib/postgresql/data",
                   "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:15")
            created = True
            for _ in range(100):
                if docker("exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", check=False).returncode == 0:
                    break
                time.sleep(0.1)
            else:
                raise RuntimeError("isolated PostgreSQL did not become ready")
            print(docker("exec", name, "psql", "-U", "postgres", "-Atc", "SHOW server_version").stdout.strip(), flush=True)
            for source, binary in binaries:
                docker("cp", source, name + ":/tmp/" + binary)
                result = docker("exec", "-e", "AITAGENT_OFFLINE_PG=1", name,
                                "/tmp/" + binary, "-test.v", "-test.run=" + ("^(TestOfflinePGSourceToPendingInbox|TestOfflinePGCancellationSchedulerDemo)$" if args.demo else "^TestOfflinePG"), "-test.timeout=60s",
                                check=False, timeout=90)
                print(result.stdout, end="", flush=True)
                if args.demo:
                    for line in result.stdout.splitlines():
                        if "AITAGENT_DEMO " in line:
                            scenarios.append(json.loads(line.split("AITAGENT_DEMO ", 1)[1]))
                if result.returncode:
                    print(result.stderr, end="", flush=True)
                    raise RuntimeError("offline Go integration tests failed")
            if args.demo:
                if {s["scenario"] for s in scenarios} != {"source_adapter_chain", "late_intake_auto_cancel"}:
                    raise RuntimeError("incomplete isolated demo report")
                report = {"mode": "isolated_simulation_not_live_integration", "status": "passed",
                          "created_at_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
                          "network": "none", "real_mattermost": False, "real_octop": False,
                          "plugin_version": json.loads((root / "plugin.json").read_text())["version"],
                          "git_head": subprocess.run(["git", "rev-parse", "HEAD"], cwd=root, check=True, capture_output=True, text=True).stdout.strip(),
                          "working_tree_dirty": bool(subprocess.run(["git", "status", "--porcelain"], cwd=root, check=True, capture_output=True, text=True).stdout.strip()),
                          "scenarios": scenarios}
                if args.report:
                    target = Path(args.report)
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_text(json.dumps(report, indent=2) + "\n")
                print(json.dumps(report), flush=True)
        finally:
            if created:
                docker("stop", name)
                print("Removed isolated PostgreSQL inbox test container", flush=True)


if __name__ == "__main__":
    main()
