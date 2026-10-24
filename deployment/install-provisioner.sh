#!/bin/sh
# Explicit operator install on the dedicated host, after reviewing this release.
set -eu
test "$(id -u)" -eq 0
release=$(realpath "$1")
case "$release" in /opt/artex/releases/*) ;; *) exit 1 ;; esac
image=$2
install -d -m 700 -o root -g root /var/lib/artex-operator
install -d -m 700 -o root -g root /var/lib/artex-operator/jobs /var/lib/artex-operator/runtime /var/lib/artex-operator/workspaces /var/lib/artex-operator/docker-client
python3 - "$release" "$image" <<'PY'
import json, os, re, sys
from pathlib import Path
release, image = sys.argv[1:]
if not re.fullmatch(r'sha256:[0-9a-f]{64}', image):
    raise SystemExit('Pass the inspected immutable local image ID')
config = {
    'operator_root': '/var/lib/artex-operator', 'release_root': release,
    'state_root': '/var/lib/artex-operator/jobs', 'runtime_root': '/var/lib/artex-operator/runtime',
    'registry': '/etc/artex/runners', 'image': image, 'max_active': 2,
    'profiles': [
        {'id': 'private-fixture', 'name': 'Private synthetic lab',
         'description': 'Private training app only. Up to 4 minutes and 80 HTTP requests; no external hosts.',
         'fixture': True, 'duration_seconds': 600, 'assessment_seconds': 240,
         'http_requests': 80, 'min_interval_ms': 1500,
         'targets': [{'host': 'fixture.test', 'http_ports': [8000, 8443], 'tcp_ports': []}]},
        {'id': 'zusott-probe', 'name': 'Zusott — free probe accounts',
         'description': 'zusott.com and disposable mail at api.mail.tm. Up to 20 minutes and 80 HTTP requests. No purchases or third-party account data.',
         'fixture': False, 'duration_seconds': 1500, 'assessment_seconds': 1200,
         'http_requests': 80, 'min_interval_ms': 1500,
         'targets': [{'host': 'zusott.com', 'http_ports': [443], 'tcp_ports': []},
                     {'host': 'api.mail.tm', 'http_ports': [443], 'tcp_ports': []}]}
    ]}
path = Path('/etc/artex/provisioner.json')
staged = path.with_suffix('.pending')
with staged.open('x') as output:
    os.chmod(staged, 0o600)
    json.dump(config, output, indent=2)
    output.write('\n')
os.replace(staged, path)
env = Path('/etc/artex/app.env')
lines = [line for line in env.read_text().splitlines() if not line.startswith('ARTEX_PROVISIONER_SOCKET=')]
lines.append('ARTEX_PROVISIONER_SOCKET=/run/artex-provisioner.sock')
env.write_text('\n'.join(lines) + '\n')
env.chmod(0o600)
PY
install -m 644 "$release/deployment/artex-provisioner.service" /etc/systemd/system/artex-provisioner.service
install -m 644 "$release/deployment/artex-provisioner.socket" /etc/systemd/system/artex-provisioner.socket
systemctl daemon-reload
systemctl enable --now artex-provisioner.socket
# Restart the app only in the deployment job, after backing up its environment.
