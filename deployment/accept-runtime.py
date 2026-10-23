#!/usr/bin/env python3
"""Exercise the packaged runner against the private, synthetic fixture only."""
import argparse
import datetime
import json
from pathlib import Path
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--url', default='http://127.0.0.1:18880')
parser.add_argument('--token-file', type=Path, required=True)
parser.add_argument('--output', type=Path, required=True)
args = parser.parse_args()
headers = {'Authorization': 'Bearer ' + args.token_file.read_text().strip(), 'Content-Type': 'application/json'}
results = []

def request(path, data=None):
    req = urllib.request.Request(args.url + path, headers=headers, data=None if data is None else json.dumps(data).encode())
    with urllib.request.urlopen(req, timeout=135) as response:
        return json.load(response)

def call(name, data, directory='acceptance'):
    return request('/v1/call', {'name': name, 'input': data, 'directory': directory, 'timeout_seconds': 30})

def flatten(reply):
    return ''.join(part.get('text', part.get('Text', '')) for part in reply.get('result', {}).get('Content', []))

def record(name, passed, detail):
    results.append({'check': name, 'passed': bool(passed), 'detail': detail})
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps({'checked_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'checks': results}, indent=2))
    print(f'{name}: {"PASS" if passed else "FAIL"}', flush=True)
    if not passed:
        raise SystemExit(f'Acceptance failed: {name}: {str(detail)[:1500]}')

ready = request('/v1/readiness', {})
record('all_required_readiness', ready.get('ready'), ready)

for scheme, port in [('http', 8000), ('https', 8443)]:
    reply = call('WebFetch', {'url': f'{scheme}://fixture.test:{port}/'})
    record('fixture_' + scheme, not reply.get('error') and 'HTTP 200' in flatten(reply) and 'Local Notes Lab' in flatten(reply), reply)

reply = call('WebFetch', {'url': 'http://fixture.test:8000/redirect-outside'})
record('redirect_outside_scope_denied', 'HTTP 403' in flatten(reply), reply)

reply = call('Bash', {'command': 'false'})
record('normal_nonzero_exit_is_observation', not reply.get('error') and reply.get('result', {}).get('IsError') and '[exit code 1]' in flatten(reply), reply)
reply = call('Bash', {'command': 'printf expected-success'})
record('healthy_after_negative_command', not reply.get('error') and 'expected-success' in flatten(reply), reply)

for number in range(12):
    reply = call('Bash', {'command': f'printf worker-{number}'}, directory=f'acceptance-worker-{number}')
    record(f'sequential_session_{number}', not reply.get('error') and f'worker-{number}' in flatten(reply), reply)

reply = call('Bash', {'command': 'test ! -e /run/artex-control/token && test ! -e /secrets && test ! -e /config && test ! -e /proc/1/root/run/artex-control/token && test ! -e /var/run/docker.sock && printf control-paths-hidden'}, directory='acceptance-browser')
record('control_paths_hidden_in_actual_tool', not reply.get('error') and 'control-paths-hidden' in flatten(reply), reply)

reply = call('mcp__browser__browser_run_code_unsafe', {'code': "async (page) => { await page.goto('https://fixture.test:8443/'); return await page.title(); }"}, directory='acceptance-browser')
record('browser_https_through_broker', not reply.get('error') and 'Local Notes Lab' in flatten(reply), reply)

reply = call('mcp__browser__browser_run_code_unsafe', {'code': "async (page) => { await page.locator('#definitely-absent').click({timeout: 500}); }"}, directory='acceptance-browser')
record('browser_selector_miss_is_observation', not reply.get('error') and reply.get('result', {}).get('IsError'), reply)

kill_browser = """python3 - <<'PY'
from pathlib import Path
import os, signal
count = 0
for proc in Path('/proc').glob('[0-9]*'):
    try:
        if (proc/'comm').read_text().strip() == 'node':
            os.kill(int(proc.name), signal.SIGKILL)
            count += 1
    except OSError:
        pass
assert count > 0, 'No browser MCP process was killed'
print('browser-process-stopped')
PY"""
reply = call('Bash', {'command': kill_browser}, directory='acceptance-browser')
record('browser_crash_injected', not reply.get('error') and not reply.get('result', {}).get('IsError') and 'browser-process-stopped' in flatten(reply), reply)
reply = call('mcp__browser__browser_snapshot', {}, directory='acceptance-browser')
record('browser_transport_failure_is_infrastructure_error', 'MCP transport/protocol failed' in reply.get('error', ''), reply)

reply = call('Bash', {'command': 'artex-required-command-that-does-not-exist'}, directory='acceptance-missing')
record('missing_dependency_is_infrastructure_error', 'Required shell command could not run' in reply.get('error', ''), reply)

ready = request('/v1/readiness', {})
record('readiness_after_failure_repair', ready.get('ready'), ready)
print('Packaged runtime acceptance passed.', flush=True)
