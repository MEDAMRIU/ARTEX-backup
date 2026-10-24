"""No Docker, accounts or target requests: exercise the operator trust boundary."""
import copy
import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('runtime_provisioner', Path(__file__).with_name('runtime-provisioner.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ProvisionerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name).resolve()
        self.time = dt.datetime(2026, 10, 24, tzinfo=dt.timezone.utc)
        profile = {'id': 'private-lab', 'name': 'Private lab', 'description': 'Synthetic only',
                   'targets': [{'host': 'fixture.test', 'http_ports': [8000, 8443], 'tcp_ports': []}],
                   'http_requests': 30, 'min_interval_ms': 1500, 'duration_seconds': 600, 'fixture': True}
        self.config = {'operator_root': str(root / 'operator'), 'app_workspace_root': str(root / 'app'), 'image': 'sha256:' + 'a' * 64, 'release_root': str(root / 'release'),
                       'state_root': str(root / 'jobs'), 'runtime_root': str(root / 'runtimes'),
                       'registry': str(root / 'registry'), 'max_active': 2, 'profiles': [profile]}
        for key in ['release_root', 'runtime_root', 'registry']:
            Path(self.config[key]).mkdir()
        self.service = module.Provisioner(self.config, clock=lambda: self.time, require_root=False)
        self.calls = []
        def helper(task, record, profile):
            self.calls.append((task, copy.deepcopy(record)))
            (self.service.registry / (task + '.json')).write_text('{}')
        self.service.invoke_helper = helper
        self.service.readiness = lambda task: {'task_id': task, 'profile_id': 'private-lab', 'ready': True}

    def test_repeat_is_idempotent_and_expiry_cannot_reset_budget(self):
        first = self.service.provision('4', 'private-lab')
        receipt = self.service.state / '4.json'
        original = json.loads(receipt.read_text())
        self.time += dt.timedelta(seconds=200)
        self.assertEqual(first, self.service.provision('4', 'private-lab'))
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(original, json.loads(receipt.read_text()))
        self.time += dt.timedelta(seconds=500)
        with patch.object(self.service, 'cleanup_expired'), self.assertRaises(module.Conflict):
            self.service.provision('4', 'private-lab')
        self.assertEqual(original['policy'], json.loads(receipt.read_text())['policy'])
        self.assertEqual(len(self.calls), 1)

    def test_changed_profile_and_existing_manual_task_are_refused(self):
        self.service.provision('4', 'private-lab')
        self.service.profiles['private-lab']['http_requests'] = 31
        with self.assertRaises(module.Conflict):
            self.service.provision('4', 'private-lab')
        (self.service.registry / '5.json').write_text('{}')
        with self.assertRaises(module.Conflict):
            self.service.provision('5', 'private-lab')
        self.assertEqual(len(self.calls), 1)

    def test_slot_allocation_ignores_policy_copies_and_respects_existing_stacks(self):
        state = self.service.runtime / 'task-900000001'
        state.mkdir()
        (state / 'compose.env').write_text('ARTEX_BROKER_IP=172.30.120.2\n')
        self.service.provision('4', 'private-lab')
        self.service.provision('5', 'private-lab')
        self.assertEqual([record['slot'] for _, record in self.calls], [121, 122])
        with self.assertRaises(module.Conflict):
            self.service.provision('6', 'private-lab')

    def test_dns_rebinding_to_private_address_is_rejected_before_provisioning(self):
        profile = self.service.profiles['private-lab']
        profile['fixture'] = False
        profile['targets'] = [{'host': 'approved.example', 'http_ports': [443], 'tcp_ports': []}]
        for addresses in [['127.0.0.1'], ['169.254.169.254'], ['8.8.8.8', '10.0.0.1']]:
            self.service.resolver = lambda *args, **kwargs: [(None, None, None, None, (x, 443)) for x in addresses]
            with self.assertRaises(module.Conflict):
                self.service.provision('4', 'private-lab')
        self.assertEqual(self.calls, [])
        self.assertEqual(list(self.service.state.glob('*.json')), [])

    def test_valid_dns_is_pinned_once_for_retries(self):
        self.service.profiles['private-lab']['fixture'] = False
        self.service.profiles['private-lab']['targets'] = [{'host': 'approved.example', 'http_ports': [443], 'tcp_ports': []}]
        self.service.resolver = lambda *args, **kwargs: [(None, None, None, None, ('8.8.8.8', 443))]
        self.service.provision('4', 'private-lab')
        self.service.resolver = lambda *args, **kwargs: self.fail('Retry resolved a new destination')
        self.service.provision('4', 'private-lab')
        self.assertEqual(self.calls[0][1]['policy']['targets'][0]['ips'], ['8.8.8.8'])

    def test_invalid_identity_unknown_profile_and_concurrent_job_are_refused(self):
        for task in ['../4', '04', '0', '4\n', '4;id', 4]:
            with self.assertRaises(ValueError):
                self.service.provision(task, 'private-lab')
        with self.assertRaises(ValueError):
            self.service.provision('4', 'unapproved')
        self.service.lock.acquire()
        try:
            with self.assertRaises(module.Conflict):
                self.service.provision('4', 'private-lab')
        finally:
            self.service.lock.release()
        self.assertEqual(self.calls, [])

    def test_failed_start_keeps_the_original_policy_for_recovery(self):
        def fail(*args):
            raise module.Conflict('Synthetic startup failure')
        self.service.invoke_helper = fail
        with self.assertRaises(module.Conflict):
            self.service.provision('4', 'private-lab')
        receipt = self.service.state / '4.json'
        original = receipt.read_bytes()
        self.time += dt.timedelta(seconds=90)
        with self.assertRaises(module.Conflict):
            self.service.provision('4', 'private-lab')
        self.assertEqual(original, receipt.read_bytes())

    def test_cleanup_stops_only_own_expired_named_containers_and_preserves_files(self):
        self.service.provision('4', 'private-lab')
        policy = (self.service.state / '4.policy.json').read_bytes()
        self.time += dt.timedelta(seconds=601)
        commands = []
        def run(command, **kwargs):
            commands.append(command)
            class Result:
                returncode = 0
                stdout = b'artex-task-4-runner-1\n'
            return Result()
        with patch.object(module.subprocess, 'run', side_effect=run):
            self.service.cleanup_expired()
        self.assertEqual(commands[-1], ['docker', 'stop', '--time', '5', 'artex-task-4-runner-1'])
        self.assertEqual(policy, (self.service.state / '4.policy.json').read_bytes())
        self.assertTrue((self.service.registry / '4.json').exists())
        self.assertIn('stopped_at', json.loads((self.service.state / '4.json').read_text()))

    def test_operator_limits_and_public_metadata(self):
        for key, value in [('http_requests', 81), ('min_interval_ms', 0), ('duration_seconds', 86400)]:
            config = copy.deepcopy(self.config)
            config['profiles'][0][key] = value
            with self.assertRaises(ValueError):
                module.Provisioner(config, require_root=False)
        public = json.dumps(self.service.public_profiles())
        self.assertNotIn('sha256', public)
        self.assertNotIn(self.config['state_root'], public)

    def test_fixture_tls_exception_is_never_used_for_external_profile(self):
        profile = self.service.profiles['private-lab']
        fixture = self.service.make_policy('4', profile, 120)
        self.assertTrue(fixture['insecure_upstream_tls'])
        self.assertEqual(fixture['targets'][0]['ips'], ['172.30.120.4'])
        external = copy.deepcopy(profile)
        external['fixture'] = False
        external['targets'] = [{'host': 'approved.example', 'http_ports': [443]}]
        self.service.resolver = lambda *args, **kwargs: [(None, None, None, None, ('8.8.8.8', 443))]
        self.assertFalse(self.service.make_policy('5', external, 121)['insecure_upstream_tls'])

    def test_operator_paths_reject_application_owned_ancestor(self):
        with self.assertRaises(ValueError):
            module.root_directory(Path(self.temp.name))

    def test_workspace_pin_uses_open_directory_not_a_replaced_path(self):
        base = Path(self.temp.name).resolve()
        self.service.app_workspace = base / 'app'
        original = self.service.app_workspace / 'tasks/4'
        original.mkdir(parents=True)
        (original / 'marker').write_text('original')
        self.service.workspaces.mkdir(parents=True)
        anchor = self.service.workspaces / 'tasks/4'
        captured = {}
        real_stat = Path.stat
        def mount(command, **kwargs):
            fd, = kwargs['pass_fds']
            self.assertEqual(command, ['mount', '--no-canonicalize', '--bind', f'/proc/self/fd/{fd}', str(anchor)])
            self.assertTrue(os.path.isdir(f'/dev/fd/{fd}'))
            captured['stat'] = os.fstat(fd)
            original.rename(original.with_name('original-renamed'))
            original.symlink_to(base)
        def stat_path(path, *args, **kwargs):
            if path == anchor and 'stat' in captured:
                return captured['stat']
            return real_stat(path, *args, **kwargs)
        with patch.object(module, 'APP_UID', os.getuid()), patch.object(module.subprocess, 'run', side_effect=mount), patch.object(Path, 'stat', stat_path):
            self.assertEqual(self.service.prepare_workspace('4'), original)
        self.assertEqual((original.with_name('original-renamed') / 'marker').read_text(), 'original')


if __name__ == '__main__':
    unittest.main()
