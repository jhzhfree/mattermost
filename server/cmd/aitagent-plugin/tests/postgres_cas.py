"""Optional synthetic PostgreSQL CAS check; no Mattermost instance or network.

Requires an already-cached official postgres:15 image and the managed local
Docker socket. Creates and removes only its own isolated, temporary container.
This tests SQL primitives modeled on SqlPluginStore, not the plugin RPC stack.
"""

import concurrent.futures
import os
import subprocess
import time
import uuid


def main():
    env = os.environ.copy()
    for key in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"):
        env.pop(key, None)
    base = ["docker", "--host=unix:///var/run/docker.sock"]
    name = "aitagent-cas-test-" + uuid.uuid4().hex[:12]

    def docker(*args, check=True):
        return subprocess.run(base + list(args), env=env, capture_output=True, text=True, check=check, timeout=30)

    def sql(statement, check=True):
        return docker("exec", name, "psql", "-h", "127.0.0.1", "-U", "postgres", "-d", "postgres", "-qAt", "-v", "ON_ERROR_STOP=1", "-c", statement, check=check)

    # Never fetch an image or expose a host port as a side effect of this test.
    docker("image", "inspect", "postgres:15")
    created = False
    try:
        docker("run", "--detach", "--rm", "--pull=never", "--name", name, "--network", "none",
               "--tmpfs", "/var/lib/postgresql/data", "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:15")
        created = True
        for _ in range(50):
            # The image's temporary bootstrap server accepts Unix sockets;
            # loopback readiness waits for the final server instead.
            if docker("exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", check=False).returncode == 0:
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("test PostgreSQL did not become ready")
        print(sql("SHOW server_version").stdout.strip())
        sql("CREATE TABLE plugin_kv_test (pluginid text, pkey text, pvalue bytea, expireat bigint, PRIMARY KEY(pluginid,pkey))")

        def insert(_):
            result = sql("INSERT INTO plugin_kv_test VALUES ('test','ledger',decode('00','hex'),0) RETURNING 1", check=False)
            if result.returncode and "duplicate key value violates unique constraint" not in result.stderr:
                raise RuntimeError("unexpected database insertion error")
            return result.returncode == 0

        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            assert sum(pool.map(insert, range(16))) == 1, "nil-old-value race admitted multiple creators"
        print("PASS: concurrent absent-key inserts have one winner")

        def compare_and_set(i):
            return sql("UPDATE plugin_kv_test SET pvalue=decode('%02x','hex'),expireat=0 "
                       "WHERE pluginid='test' AND pkey='ledger' AND pvalue=decode('00','hex') "
                       "AND (expireat=0 OR expireat>1000) RETURNING 1" % (i + 1)).stdout.strip() == "1"

        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            assert sum(pool.map(compare_and_set, range(16))) == 1, "CAS race admitted multiple writers"
        assert sql("SELECT count(*) FROM plugin_kv_test").stdout.strip() == "1"
        print("PASS: concurrent exact-byte CAS updates have one winner")

        sql("UPDATE plugin_kv_test SET pvalue=decode('00','hex'),expireat=1000")
        assert sql("SELECT pvalue FROM plugin_kv_test WHERE expireat=0 OR expireat>1000").stdout.strip() == ""
        assert sql("DELETE FROM plugin_kv_test WHERE expireat!=0 AND expireat<1000 RETURNING 1").stdout.strip() == ""
        assert not insert(0), "expiry-boundary row must still conflict on insert"
        assert not compare_and_set(0), "expired non-nil CAS must not update"
        assert sql("DELETE FROM plugin_kv_test WHERE expireat!=0 AND expireat<1001 RETURNING 1").stdout.strip() == "1"
        assert insert(0)
        print("PASS: expiry boundary and later expired-key replacement match reviewed SQL")
    finally:
        if created:
            docker("stop", name)
            print("Removed isolated PostgreSQL test container")


if __name__ == "__main__":
    main()
