#!/usr/bin/env python3
"""Root-owned, socket-activated provisioning for explicitly approved profiles.

The app can select a profile, never a Docker argument, image, mount or hostname.
Each task keeps its first policy and deadline across retries and service restarts.
"""
import argparse
import datetime as dt
import hashlib
import http.server
import ipaddress
import json
import os
from pathlib import Path
import re
import socket
import socketserver
import stat
import subprocess
import threading
import urllib.request

APP_UID = 10001


def now():
    return dt.datetime.now(dt.timezone.utc)


def parse_time(value):
    result = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
    if result.tzinfo is None:
        raise ValueError('Deadline requires a timezone')
    return result


def save(path, data):
    staged = path.with_suffix('.pending')
    with staged.open('x') as output:
        os.chmod(staged, 0o600)
        json.dump(data, output, indent=2)
        output.write('\n')
        output.flush()
        os.fsync(output.fileno())
    os.replace(staged, path)


class Conflict(Exception):
    pass


def root_directory(path):
    """Every ancestor must prevent the app from renaming operator state/code."""
    if not path.is_absolute():
        raise ValueError('Absolute operator directory required')
    for parent in reversed([path] + list(path.parents)):
        info = parent.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError('Operator paths require root-owned, non-writable ancestors')


class Provisioner:
    def __init__(self, config, clock=now, resolver=socket.getaddrinfo, require_root=True):
        self.config, self.clock, self.resolver = config, clock, resolver
        self.lock = threading.Lock()
        self.profiles = {}
        if not re.fullmatch(r'sha256:[0-9a-f]{64}', config['image']):
            raise ValueError('Use an inspected immutable local image ID')
        self.repo = Path(config['release_root'])
        self.state = Path(config['state_root'])
        self.runtime = Path(config['runtime_root'])
        self.registry = Path(config['registry'])
        self.operator_root = Path(config['operator_root'])
        self.workspaces = self.operator_root / 'workspaces'
        self.app_workspace = Path(config.get('app_workspace_root', '/var/lib/artex/data'))
        for path in [self.repo, self.state, self.runtime, self.registry, self.operator_root, self.workspaces, self.app_workspace]:
            if not path.is_absolute() or path.resolve() != path:
                raise ValueError('Provisioner paths must be absolute without symlinks')
        if require_root:
            root_directory(self.operator_root)
            root_directory(self.repo)
            root_directory(self.registry)
            for path in [self.state, self.runtime, self.workspaces]:
                if not path.is_relative_to(self.operator_root):
                    raise ValueError('All mutable operator state must stay under its root-controlled directory')
                path.mkdir(mode=0o700, exist_ok=True)
                root_directory(path)
        if not 1 <= config['max_active'] <= 2:
            raise ValueError('At most two automated runtimes may be active')
        for profile in config['profiles']:
            ident = profile['id']
            if not re.fullmatch(r'[a-z][a-z0-9-]{0,47}', ident) or ident in self.profiles:
                raise ValueError('Invalid or duplicate profile ID')
            if (not 1 <= profile['http_requests'] <= 80 or
                    not 1500 <= profile['min_interval_ms'] <= 60000 or
                    not 60 <= profile['duration_seconds'] <= 1800):
                raise ValueError('Profile exceeds the bounded request/time limits')
            profile.setdefault('assessment_seconds', max(60, profile['duration_seconds'] - 120))
            if not 60 <= profile['assessment_seconds'] <= profile['duration_seconds'] - 120:
                raise ValueError('Leave at least two minutes for assessment wrap-up')
            if not profile['targets']:
                raise ValueError('Explicit profile targets are required')
            hosts = set()
            for target in profile['targets']:
                host = target['host']
                if (not re.fullmatch(r'[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?', host)
                        or host in hosts or target.get('tcp_ports')
                        or not target.get('http_ports')
                        or any(type(p) is not int or not 1 <= p <= 65535 for p in target['http_ports'])):
                    raise ValueError('Invalid target or raw TCP scope')
                hosts.add(host)
            if profile.get('fixture') and (hosts != {'fixture.test'} or
                    profile['targets'][0]['http_ports'] != [8000, 8443]):
                raise ValueError('Fixture scope must be the private synthetic application')
            self.profiles[ident] = profile
        self.state.mkdir(parents=True, exist_ok=True, mode=0o700)
        os.chmod(self.state, 0o700)

    def prepare_workspace(self, task):
        # Pin the app's own regular directory by FD, then bind it at a root-only
        # anchor. Docker consumes that stable anchor, never a replaceable app path.
        original = self.app_workspace / 'tasks' / task
        anchor = self.workspaces / 'tasks' / task
        anchor.parent.mkdir(mode=0o700, exist_ok=True)
        if os.path.ismount(anchor):
            if anchor.stat().st_uid != APP_UID:
                raise Conflict('Unexpected workspace anchor owner')
            return original
        fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY)
        try:
            for index, part in enumerate(original.parts[1:]):
                created = False
                try:
                    child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                except FileNotFoundError:
                    if index < len(original.parts) - 3:
                        raise Conflict('Application data root is missing')
                    os.mkdir(part, mode=0o700, dir_fd=fd)
                    child = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                    created = True
                os.close(fd)
                fd = child
                if created:
                    os.fchown(fd, APP_UID, APP_UID)
            source = os.fstat(fd)
            if source.st_uid != APP_UID:
                raise Conflict('Application task workspace must belong to the application user')
            anchor.mkdir(mode=0o700, exist_ok=True)
            subprocess.run(['mount', '--no-canonicalize', '--bind', f'/proc/self/fd/{fd}', str(anchor)],
                           pass_fds=(fd,), check=True, timeout=10)
            pinned = anchor.stat()
            if (source.st_dev, source.st_ino) != (pinned.st_dev, pinned.st_ino):
                raise Conflict('Workspace anchor did not pin the original directory')
            return original
        finally:
            os.close(fd)

    def public_profiles(self):
        return {'enabled': True, 'profiles': [
            {k: p[k] for k in ['id', 'name', 'description', 'targets', 'http_requests',
                               'min_interval_ms', 'duration_seconds', 'assessment_seconds']}
            for p in self.profiles.values()]}

    def fingerprint(self, profile):
        return hashlib.sha256(json.dumps({'profile': profile, 'image': self.config['image']},
                                        sort_keys=True).encode()).hexdigest()

    def readiness(self, task):
        descriptor = json.loads((self.registry / (task + '.json')).read_text())
        expected = self.registry / (task + '.token')
        if descriptor['task_id'] != task or descriptor['token_file'] != str(expected):
            raise Conflict('Unexpected task registration; operator inspection required')
        record = json.loads((self.state / (task + '.json')).read_text())
        if descriptor['url'] != f"http://127.0.0.1:{record['port']}":
            raise Conflict('Registration differs from its original provisioning receipt')
        request = urllib.request.Request(descriptor['url'] + '/v1/readiness',
                                        headers={'Authorization': 'Bearer ' + expected.read_text().strip()})
        with urllib.request.urlopen(request, timeout=12) as response:
            ready = json.loads(response.read(65536))
        if (not ready.get('ready') or ready.get('task_id') != task or
                self.clock() >= parse_time(ready['expires_at'])):
            raise Conflict('Existing runtime is not ready or has expired; its budget was not reset')
        return {'task_id': task, 'profile_id': record['profile_id'], 'ready': True}

    def allocate(self):
        used = set()
        for env in self.runtime.glob('task-*/compose.env'):
            for line in env.read_text().splitlines():
                if line.startswith('ARTEX_BROKER_IP='):
                    used.add(int(line.split('=', 1)[1].split('.')[2]))
        for path in self.state.glob('[0-9]*.json'):
            if not path.name.endswith('.policy.json'):
                used.add(json.loads(path.read_text())['slot'])
        for slot in range(120, 248):
            if slot in used:
                continue
            port = 19000 + slot - 120
            with socket.socket() as probe:
                try:
                    probe.bind(('127.0.0.1', port))
                except OSError:
                    continue
            return slot, port
        raise Conflict('No unused runner slot remains; operator cleanup is required')

    def cleanup_expired(self):
        # Stop only this service's expired stacks; retain every file and network.
        for path in self.state.glob('[0-9]*.json'):
            if path.name.endswith('.policy.json'):
                continue
            record = json.loads(path.read_text())
            if record.get('stopped_at') or self.clock() < parse_time(record['policy']['expires_at']):
                continue
            task = record['task_id']
            if not re.fullmatch(r'[1-9][0-9]{0,17}', task):
                raise Conflict('Invalid saved task identity')
            names = [f'artex-task-{task}-{kind}-1' for kind in ['runner', 'broker', 'fixture']]
            result = subprocess.run(['docker', 'ps', '--format', '{{.Names}}'],
                                    capture_output=True, timeout=10, check=True)
            active = set(result.stdout.decode().splitlines())
            running = [name for name in names if name in active]
            if running:
                subprocess.run(['docker', 'stop', '--time', '5'] + running,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                               timeout=25, check=True)
            record['stopped_at'] = self.clock().isoformat()
            save(path, record)

    def make_policy(self, task, profile, slot):
        targets = []
        for target in profile['targets']:
            if profile.get('fixture'):
                addresses = [f'172.30.{slot}.4']
            else:
                addresses = sorted({item[4][0] for item in self.resolver(
                    target['host'], 443, family=socket.AF_INET, type=socket.SOCK_STREAM)})
                if not addresses or any(not ipaddress.ip_address(a).is_global for a in addresses):
                    raise Conflict('Approved hostname did not resolve exclusively to public IPv4 addresses')
            targets.append({'host': target['host'], 'ips': addresses,
                            'http_ports': target['http_ports'], 'tcp_ports': []})
        return {'task_id': task, 'expires_at': (self.clock() + dt.timedelta(
                    seconds=profile['duration_seconds'])).isoformat(),
                'http_requests': profile['http_requests'], 'tcp_connections': 0,
                'min_interval_ms': profile['min_interval_ms'], 'targets': targets,
                'insecure_upstream_tls': bool(profile.get('fixture'))}

    def invoke_helper(self, task, record, profile):
        original = self.prepare_workspace(task)
        command = ['python3', str(self.repo / 'deployment/provision-runtime.py'),
                   '--policy', str(self.state / (task + '.policy.json')),
                   '--image', self.config['image'], '--port', str(record['port']),
                   '--slot', str(record['slot']), '--runtime-root', str(self.runtime),
                   '--registry', str(self.registry), '--workspace-root', str(self.workspaces),
                   '--host-workspace', str(original)]
        if profile.get('fixture'):
            command += ['--fixture']
        if (self.runtime / ('task-' + task)).exists():
            command += ['--finish-existing']
        log = self.state / (task + '.log')
        with log.open('ab') as output:
            os.chmod(log, 0o600)
            result = subprocess.run(command, stdout=output, stderr=output, timeout=160)
        if result.returncode:
            raise Conflict('Provisioning did not finish; saved state and budget were retained for operator review')

    def provision(self, task, profile_id):
        if not isinstance(task, str) or not re.fullmatch(r'[1-9][0-9]{0,17}', task):
            raise ValueError('Canonical task ID required')
        if not isinstance(profile_id, str) or profile_id not in self.profiles:
            raise ValueError('Select an operator-approved execution profile')
        if not self.lock.acquire(blocking=False):
            raise Conflict('Another runner is being provisioned; retry after it finishes')
        try:
            self.cleanup_expired()
            profile = self.profiles[profile_id]
            receipt = self.state / (task + '.json')
            if receipt.exists():
                record = json.loads(receipt.read_text())
                if record['profile_id'] != profile_id or record['fingerprint'] != self.fingerprint(profile):
                    raise Conflict('Task is bound to its original approved profile; create a new task')
                if self.clock() >= parse_time(record['policy']['expires_at']):
                    raise Conflict('Task deadline has expired; retries cannot renew it')
            else:
                if ((self.registry / (task + '.json')).exists() or
                        (self.runtime / ('task-' + task)).exists()):
                    raise Conflict('Task was provisioned outside this service; its state will not be replaced')
                active = sum(self.clock() < parse_time(json.loads(p.read_text())['policy']['expires_at'])
                             for p in self.state.glob('[0-9]*.json')
                             if not p.name.endswith('.policy.json'))
                if active >= self.config['max_active']:
                    raise Conflict('Automated runner limit reached; wait for an existing deadline')
                slot, port = self.allocate()
                policy = self.make_policy(task, profile, slot)
                record = {'task_id': task, 'profile_id': profile_id, 'fingerprint': self.fingerprint(profile),
                          'slot': slot, 'port': port, 'policy': policy}
                save(receipt, record)
                save(self.state / (task + '.policy.json'), policy)
            if not (self.registry / (task + '.json')).exists():
                policy_file = self.state / (task + '.policy.json')
                if json.loads(policy_file.read_text()) != record['policy']:
                    raise Conflict('Saved task policy differs from its original receipt')
                self.invoke_helper(task, record, profile)
            return self.readiness(task)
        finally:
            self.lock.release()


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def respond(self, code, value):
        body = json.dumps(value).encode()
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path != '/v1/profiles':
            return self.respond(404, {'error': 'Unknown endpoint'})
        self.respond(200, self.server.provisioner.public_profiles())

    def do_POST(self):
        if self.path != '/v1/provision':
            return self.respond(404, {'error': 'Unknown endpoint'})
        try:
            length = int(self.headers.get('Content-Length', '0'))
            if not 0 < length <= 1024 or self.headers.get('Transfer-Encoding'):
                raise ValueError('A small fixed-length JSON request is required')
            request = json.loads(self.rfile.read(length))
            if not isinstance(request, dict) or set(request) != {'task_id', 'profile_id'}:
                raise ValueError('Only task_id and profile_id are accepted')
            result = self.server.provisioner.provision(request['task_id'], request['profile_id'])
            self.respond(200, result)
        except (ValueError, TypeError):
            self.respond(400, {'error': 'Invalid task or approved profile request'})
        except Conflict as error:
            self.respond(409, {'error': str(error)})
        except Exception:
            self.respond(503, {'error': 'Provisioning unavailable; original state retained for operator review'})


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    args = parser.parse_args()
    info = args.config.lstat()
    if (os.geteuid() != 0 or not stat.S_ISREG(info.st_mode) or info.st_uid != 0
            or info.st_mode & 0o022 or args.config.resolve() != args.config):
        raise SystemExit('Use a regular, root-owned configuration with no group/world writes')
    if os.environ.get('LISTEN_PID') != str(os.getpid()) or os.environ.get('LISTEN_FDS') != '1':
        raise SystemExit('Start through the root-owned systemd Unix socket unit')
    provisioner = Provisioner(json.loads(args.config.read_text()))
    server = Server(None, Handler, bind_and_activate=False)
    server.socket = socket.socket(fileno=3)
    server.server_address = server.socket.getsockname()
    server.provisioner = provisioner
    stopped = threading.Event()
    def cleanup_loop():
        while not stopped.wait(30):
            if provisioner.lock.acquire(blocking=False):
                try:
                    provisioner.cleanup_expired()
                except Exception:
                    print('Expired runner cleanup failed; original state retained', flush=True)
                finally:
                    provisioner.lock.release()
    threading.Thread(target=cleanup_loop, daemon=True).start()
    try:
        server.serve_forever()
    finally:
        stopped.set()
        server.server_close()


if __name__ == '__main__':
    main()
