#!/usr/bin/env python3
"""Provision one approved task policy and publish its app runner registration.

Run as operator/root, outside the application. A task ID is never reused, so an
expired/exhausted assessment cannot acquire a fresh budget by replacing files.
"""
import argparse
import datetime as dt
import grp
import ipaddress
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import subprocess
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--policy', type=Path, required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--port', type=int, required=True)
    parser.add_argument('--slot', type=int, required=True, help='Unique network slot 1..250')
    parser.add_argument('--workspace-root', type=Path, default=Path('/var/lib/artex/data'))
    parser.add_argument('--host-workspace', type=Path,
                        help='Own-task app path alias when the source workspace has a protected bind anchor')
    parser.add_argument('--runtime-root', type=Path, default=Path('/var/lib/artex/runtime'))
    parser.add_argument('--registry', type=Path, default=Path('/etc/artex/runners'))
    parser.add_argument('--fixture', action='store_true', help='Add the private synthetic fixture')
    parser.add_argument('--finish-existing', action='store_true',
                        help='Finish an unpublished registration using its unchanged existing state')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('Run as the host operator, not inside the application')
    policy = json.loads(args.policy.read_text())
    task = policy.get('task_id', '')
    if not isinstance(task, str) or not re.fullmatch(r'[1-9][0-9]{0,17}', task):
        parser.error('Policy task_id must be a canonical positive ID')
    if not 1 <= args.slot <= 250 or not 1024 <= args.port <= 65535:
        parser.error('Invalid network slot or loopback control port')
    expiry = dt.datetime.fromisoformat(policy['expires_at'].replace('Z', '+00:00'))
    if expiry.tzinfo is None or expiry <= dt.datetime.now(dt.timezone.utc):
        parser.error('Approved policy is already expired or lacks a timezone')
    if policy.get('min_interval_ms', 0) < 1500 or policy.get('http_requests', 0) <= 0:
        parser.error('Policy needs a positive HTTP cap and at least 1500ms pacing')
    targets = policy.get('targets', [])
    if not targets:
        parser.error('An explicit pinned target scope is required')
    for target in targets:
        if not target.get('host') or not target.get('ips'):
            parser.error('Every target needs a host and pinned IPs')
        for address in target['ips']:
            ipaddress.ip_address(address)
    uid, gid = pwd.getpwnam('artex').pw_uid, grp.getgrnam('artex').gr_gid
    state = args.runtime_root / ('task-' + task)
    registration = args.registry / (task + '.json')
    if registration.exists() or (state.exists() and not args.finish_existing):
        parser.error('Task already provisioned; create a new task instead of resetting its budget')
    if args.finish_existing and not state.is_dir():
        parser.error('No existing provisioning state to finish')
    workspace = args.workspace_root / 'tasks' / task
    host_workspace = args.host_workspace or workspace
    if (not host_workspace.is_absolute() or str(host_workspace) != str(host_workspace.absolute())
            or host_workspace.parts[-2:] != ('tasks', task)):
        parser.error('Host workspace alias must be the absolute own-task app path')
    workspace_fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY)
    try:
        for component in workspace.absolute().parts[1:]:
            try:
                child_fd = os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=workspace_fd)
            except FileNotFoundError:
                os.mkdir(component, mode=0o700, dir_fd=workspace_fd)
                child_fd = os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=workspace_fd)
            os.close(workspace_fd)
            workspace_fd = child_fd
        os.fchown(workspace_fd, uid, gid)
    finally:
        os.close(workspace_fd)
    if not args.finish_existing:
        state.mkdir(parents=True, mode=0o700)
        os.chmod(state, 0o700)
        for name in ['config', 'broker', 'secrets']:
            (state / name).mkdir(mode=0o755 if name == 'config' else 0o700)
        os.chown(state / 'broker', uid, gid)
    token = ((state / 'secrets/runner-token').read_text().strip() if args.finish_existing
             else secrets.token_urlsafe(48))

    def save(path, value, mode=0o600):
        protected = path.parent in [state / 'config', state / 'secrets'] or path == state / 'compose.env'
        if args.finish_existing and protected:
            old = path.read_text()
            matches = json.loads(old) == value if isinstance(value, dict) else old == value
            if not matches:
                raise SystemExit('Existing runtime differs from requested configuration; no state was reset')
            return
        path.write_text(value if isinstance(value, str) else json.dumps(value, indent=2))
        path.chmod(mode)

    broker = f'172.30.{args.slot}.2'
    save(state / 'secrets/runner-token', token)
    save(state / 'config/policy.json', policy, 0o644)
    save(state / 'config/broker.json', {'state': '/state', 'policy_file': '/config/policy.json',
         'proxy_listen': ':3128', 'api_listen': ':8081', 'canary_listen': ':3130'}, 0o644)
    save(state / 'config/runner.json', {'listen': ':8080', 'task_id': task, 'workspace': '/workspace',
         'host_workspace': str(host_workspace),
         'token_file': '/secrets/runner-token', 'proxy_url': f'http://{broker}:3128',
         'broker_api': f'http://{broker}:8081', 'ca_file': '/run/artex-public/ca.pem',
         'denied_probe_address': f'{broker}:3130', 'expires_at': policy['expires_at']}, 0o644)
    env = {'ARTEX_RUN_DIR': str(state), 'ARTEX_IMAGE': args.image, 'ARTEX_TASK_ID': task,
           'ARTEX_WORKSPACE_ROOT': str(args.workspace_root), 'ARTEX_RUNNER_PORT': str(args.port),
           'ARTEX_TOOL_UID': str(uid), 'ARTEX_TOOL_GID': str(gid), 'ARTEX_BROKER_IP': broker,
           'ARTEX_RUNNER_IP': f'172.30.{args.slot}.3', 'ARTEX_FIXTURE_IP': f'172.30.{args.slot}.4',
           'ARTEX_RUNNER_SUBNET': f'172.30.{args.slot}.0/24', 'ARTEX_OUTBOUND_SUBNET': f'172.31.{args.slot}.0/24'}
    if args.fixture:
        if args.finish_existing:
            existing_env = dict(line.split('=', 1) for line in (state / 'compose.env').read_text().splitlines())
            env['ARTEX_FIXTURE_MARKER'] = existing_env['ARTEX_FIXTURE_MARKER']
        else:
            env['ARTEX_FIXTURE_MARKER'] = secrets.token_urlsafe(24)
    save(state / 'compose.env', ''.join(f'{k}={v}\n' for k, v in env.items()))
    repo = Path(__file__).resolve().parent.parent
    command = ['docker', 'compose', '--project-name', 'artex-task-' + task,
               '--env-file', str(state / 'compose.env'), '-f', str(repo / 'runtime-tools/isolated/compose.yml'),
               '-f', str(repo / 'runtime-tools/isolated/compose.ubuntu.yml')]
    if args.fixture:
        command += ['-f', str(repo / 'runtime-tools/isolated/compose.lab.yml')]
    subprocess.run(command + ['up', '-d'], check=True)
    request = urllib.request.Request(f'http://127.0.0.1:{args.port}/v1/readiness', method='POST',
                                    headers={'Authorization': 'Bearer ' + token})
    # The entrypoint finishes its firewall/token setup before it listens.
    import time
    for attempt in range(12):
        try:
            with urllib.request.urlopen(request, timeout=110) as response:
                readiness = json.load(response)
            break
        except urllib.error.HTTPError:
            raise
        except (urllib.error.URLError, ConnectionError, TimeoutError):
            if attempt == 11:
                raise
            time.sleep(1)
    save(state / 'initial-readiness.json', readiness)
    if not readiness.get('ready') or readiness.get('task_id') != task:
        raise SystemExit('Runner failed acceptance; registration was not published. Inspect initial-readiness.json.')
    # /etc/artex must allow group traversal, but private env files stay root-only.
    if args.registry.parent == Path('/etc/artex'):
        os.chown(args.registry.parent, 0, gid)
        os.chmod(args.registry.parent, 0o750)
    args.registry.mkdir(parents=True, exist_ok=True, mode=0o750)
    os.chown(args.registry, 0, gid)
    os.chmod(args.registry, 0o750)
    token_file = args.registry / (task + '.token')
    save(token_file, token, 0o640)
    os.chown(token_file, 0, gid)
    staged = args.registry / (task + '.pending')
    save(staged, {'task_id': task, 'url': f'http://127.0.0.1:{args.port}', 'token_file': str(token_file)}, 0o640)
    os.chown(staged, 0, gid)
    os.replace(staged, registration)
    print(json.dumps({'task_id': task, 'ready': True, 'workspace': str(workspace), 'registration': str(registration)}))


if __name__ == '__main__':
    main()
