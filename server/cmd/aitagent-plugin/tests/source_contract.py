"""Pinned Octop source adapter -> pipe -> actual Go query boundary/fake MM API.
No socket, service, credential loading, dependency installation or remote fetch.
"""
import argparse
import asyncio
import base64
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
REVISION = '0e9504525601ed16aae154b778582d6e0ec5d8ab'


def deny(*args, **kwargs):
    raise AssertionError('network forbidden')


async def exercise(proc):
    import httpx
    from octop.infra.digital_workers.mattermost_source import OfflineMattermostSourceReader, SourceTarget, SourceUnavailable

    def receive():
        line = proc.stdout.readline()
        assert line.startswith('BRIDGE '), line
        return json.loads(line[7:])

    fixture = receive()

    def rpc(operation, body=b'', signature=''):
        proc.stdin.write(json.dumps({'Operation': operation, 'Body': base64.b64encode(body).decode(), 'Signature': signature}) + '\n')
        proc.stdin.flush()
        return receive()

    trust = fixture['trust']
    mapping = {'broker_id': trust['Broker'], 'revision': trust['Revision'],
               'binding_json': json.dumps(trust['Binding']),
               'broker_public_key': bytes(trust['BrokerKey']).hex(),
               'proof_public_key': bytes(trust['ProofKey']).hex()}
    target = SourceTarget(broker_id=trust['Broker'], mapping_revision=trust['Revision'],
                          binding=trust['Binding'], key_id=trust['KeyID'], public_key=bytes(trust['SourceKey']))

    async def resolve(mapping):
        return target

    async def sign(target, message):
        reply = rpc('sign', message)
        assert not reply['rejected']
        return base64.b64decode(reply['data'])

    cache = {}
    async def handle(request):
        assert request.method == 'POST' and request.url.path == '/api/integrations/octop/v1/source-observations'
        if cache.get('replay_response'):
            return httpx.Response(200, content=cache['response'], headers={'content-type': 'application/json'})
        cache['request'] = request.content
        cache['signature'] = request.headers['x-octop-source-signature']
        reply = rpc('query', request.content, cache['signature'])
        if reply['rejected']:
            return httpx.Response(503, json={'code': 'SOURCE_UNAVAILABLE'})
        cache['response'] = base64.b64decode(reply['data'])
        return httpx.Response(200, content=cache['response'], headers={'content-type': 'application/json'})

    reader = OfflineMattermostSourceReader(transport=httpx.MockTransport(handle), resolve=resolve, sign=sign, clock=lambda: fixture['now'])
    cases = []
    active = await reader(mapping, fixture['receipt'])
    assert active.state == 'awaiting_octop_authorization'
    cases.append('authenticated_primary_confirmed_active')
    assert rpc('query', cache['request'], cache['signature'])['rejected']
    cases.append('persistent_query_nonce_replay_rejected')
    cache['replay_response'] = True
    try:
        await reader(mapping, fixture['receipt'])
    except SourceUnavailable:
        cases.append('old_response_new_challenge_rejected')
    else:
        raise AssertionError('cached response accepted')
    cache['replay_response'] = False
    assert not rpc('cancel')['rejected']
    cancelled = await reader(mapping, fixture['receipt'])
    assert cancelled.state == 'cancelled' and cancelled.version > active.version
    assert cancelled.commitment == active.commitment and cancelled.delivery_key == active.delivery_key
    cases.append('current_cancel_preserves_identity_and_advances_version')
    for operation in ('revoke', 'registry_revoke'):
        rpc(operation)
        try:
            await reader(mapping, fixture['receipt'])
        except SourceUnavailable:
            cases.append(operation + '_rejected')
        else:
            raise AssertionError('revoked source accepted')
        rpc('restore')
    return {'status': 'passed', 'octop_revision': REVISION, 'cases': cases,
            'source': 'actual Go wire boundary and guarded CAS through fake Mattermost API',
            'receiver': 'unmodified Octop OfflineMattermostSourceReader with MockTransport',
            'live_services': False, 'tls_tested': False, 'execution_enabled': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--octop-git-dir', required=True)
    parser.add_argument('--go', default='go')
    parser.add_argument('--report', required=True)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='aitagent-source-contract-') as tmp:
        directory = Path(tmp)
        env = dict(os.environ, GIT_ALLOW_PROTOCOL='', GIT_NO_LAZY_FETCH='1', GIT_TERMINAL_PROMPT='0')
        raw = subprocess.check_output(['git', '--git-dir=' + args.octop_git_dir, 'archive', REVISION, 'src/octop'], env=env)
        with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
            archive.extractall(directory / 'peer', filter='data')
        binary = directory / 'bridge.test'
        env = dict(os.environ, GOPROXY='off', CGO_ENABLED='0', AITAGENT_SOURCE_BRIDGE='1')
        subprocess.run([args.go, 'test', '-c', '-tags=octopcontract', '-o', str(binary), '.'], cwd=ROOT, env=env, check=True, timeout=120)
        sys.path.insert(0, str(directory / 'peer/src'))
        sys.dont_write_bytecode = True
        os.environ['OCTOP_HOME'] = str(directory / 'home')
        for name in ('connect', 'connect_ex', 'sendto'):
            setattr(socket.socket, name, deny)
        socket.getaddrinfo = deny
        socket.create_connection = deny
        proc = subprocess.Popen([str(binary), '-test.run=^TestOfflineSourceBridge$', '-test.timeout=20s'], cwd=ROOT, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        try:
            report = asyncio.run(exercise(proc))
            proc.stdin.close()
            assert proc.wait(timeout=10) == 0
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.wait(timeout=10)
        report['mattermost_git_head'] = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        report['working_tree_dirty'] = bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT, text=True).strip())
        Path(args.report).parent.mkdir(parents=True, exist_ok=True)
        Path(args.report).write_text(json.dumps(report, indent=2) + '\n')
        print(json.dumps(report))


if __name__ == '__main__':
    main()
