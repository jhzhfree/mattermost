"""Local-only readiness inventory. Never starts services, fetches, or reads credentials.

Exit 2 means deployment blocked (expected today), 1 means inventory failed.
A successful compiler probe never overrides missing trust/transport acceptance.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
SERVER = ROOT.parents[1]
REPO = SERVER.parent
EVIDENCE = [
    'server/channels/app/plugin_requests.go',
    'server/channels/app/channels.go',
    'server/channels/app/plugin_api.go',
    'server/channels/app/authorization.go',
    'server/channels/store/sqlstore/plugin_store.go',
    'server/channels/store/localcachelayer/user_layer.go',
    'server/channels/store/sqlstore/channel_store.go',
    'server/cmd/aitagent-plugin/source_query.go',
    'server/cmd/aitagent-plugin/main.go',
]
GATES = [
    'no_source_http_route_or_production_constructor',
    'no_runtime_receipt_reservation_or_sender_provider',
    'no_independent_source_signer_and_broker_trust_registry',
    'plugin_permission_rechecks_are_not_primary_authorization_snapshot',
    'no_durable_independent_restore_watermark',
    'octop_source_transport_is_mock_only',
    'real_tls_identity_revocation_and_dual_service_acceptance_unverified',
]


def run(command, *, cwd=ROOT, env=None, timeout=20):
    try:
        r = subprocess.run(command, cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout)
        return r.returncode, r.stdout + r.stderr
    except FileNotFoundError:
        return 127, 'tool unavailable'
    except subprocess.TimeoutExpired:
        return 124, 'probe timeout'


def inventory(go, probe_build, probe_isolation):
    checks = {}
    for name, command in [('go', [go, 'version']), ('node', ['node', '--version'])]:
        code, text = run(command)
        checks[name] = {'exit_code': code, 'version': text.strip()[:200]}
    docker = ['docker', '--host=unix:///var/run/docker.sock']
    code, text = run(docker + ['image', 'ls', '--format', '{{.Repository}}:{{.Tag}}'])
    checks['cached_images'] = {'exit_code': code, 'names': text.splitlines() if code == 0 else []}
    checks['bubblewrap'] = {'installed': shutil.which('bwrap') is not None, 'probe_run': probe_isolation}
    if probe_isolation:
        code, text = run(['bwrap', '--unshare-net', '--ro-bind', '/', '/', '--', 'true'])
        checks['bubblewrap'].update(exit_code=code, diagnostic=text.strip()[:300])
    checks['server_build'] = {'probe_run': probe_build, 'network': 'disabled via GOPROXY=off/GOSUMDB=off/GOTOOLCHAIN=local'}
    if probe_build:
        env = dict(os.environ, GOPROXY='off', GOSUMDB='off', GOTOOLCHAIN='local')
        with tempfile.TemporaryDirectory(prefix='aitagent-server-preflight-') as tmp:
            code, text = run([go, 'build', '-o', str(Path(tmp) / 'mattermost'), './cmd/mattermost'], cwd=SERVER, env=env, timeout=60)
        checks['server_build'].update(exit_code=code,
            missing_modules=sorted(set(re.findall(r'go: downloading ([^\s]+ v[^\s]+)', text))),
            cache_incomplete='module lookup disabled' in text,
            timed_out=code == 124)
    blockers = list(GATES)
    if not probe_build or checks['server_build'].get('exit_code') != 0:
        blockers.append('matching_mattermost_server_build_unavailable_or_unverified')
    if checks['cached_images']['exit_code'] != 0:
        blockers.append('docker_inventory_unavailable')
    # No eligibility switch: a local inventory cannot certify deployment security.
    return {'status': 'blocked', 'ready_for_live_integration': False, 'checks': checks,
            'blockers': blockers, 'network_probe_performed': False, 'services_started': False,
            'source_evidence_sha256': {p: hashlib.sha256((REPO / p).read_bytes()).hexdigest() for p in EVIDENCE}}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--go', default='go')
    p.add_argument('--probe-build', action='store_true')
    p.add_argument('--probe-isolation', action='store_true')
    p.add_argument('--report', required=True)
    args = p.parse_args()
    report = inventory(args.go, args.probe_build, args.probe_isolation)
    code, head = run(['git', 'rev-parse', 'HEAD'])
    if code:
        raise RuntimeError('cannot identify source revision')
    report['mattermost_git_head'] = head.strip()
    code, changes = run(['git', 'status', '--porcelain'])
    if code:
        raise RuntimeError('cannot determine working tree')
    report['working_tree_dirty'] = bool(changes.strip())
    Path(args.report).parent.mkdir(parents=True, exist_ok=True)
    Path(args.report).write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report))
    return 2


if __name__ == '__main__':
    raise SystemExit(main())
