import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import deploy as module


class DeployTest(unittest.TestCase):
    def release(self, root):
        release = root / 'laxcode'
        files = {name: b'content' for name in (
            'bin/laxcode-cloud', 'bin/laxcode-proxy', 'bin/laxcode-manager',
            'deploy.py', 'reset_quota.py', 'network.json', 'settings.json', 'configure.py', 'Dockerfile', 'skills/SKILL.md')}
        for name, data in files.items():
            path = release / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        manifest = {'format': 2, 'os': 'linux', 'arch': 'amd64',
                    'openshell': module.VERSION, 'base_image': module.BASE_IMAGE,
                    'files': {name: hashlib.sha256(data).hexdigest() for name, data in files.items()}}
        (release / 'manifest.json').write_text(json.dumps(manifest))
        return release, manifest

    def test_corrupt_release_is_rejected_before_stopping(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            release, _ = self.release(base)
            (release / 'bin/laxcode-manager').write_bytes(b'corrupt')
            with patch.object(module.os, 'geteuid', return_value=0), \
                    patch.object(module.platform, 'system', return_value='Linux'), \
                    patch.object(module.platform, 'machine', return_value='x86_64'), \
                    patch.object(module, 'stop') as stop:
                with self.assertRaisesRegex(RuntimeError, 'checksum'):
                    module.restart(base, base / 'state', {})
                stop.assert_not_called()

    def test_manifest_rejects_escape_and_release_symlinks(self):
        for name in ('../escape', '/absolute', 'bin/../escape', './escape'):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                release, manifest = self.release(Path(temp))
                manifest['files'][name] = 'bad'
                (release / 'manifest.json').write_text(json.dumps(manifest))
                with self.assertRaisesRegex(RuntimeError, 'manifest path'):
                    module.verify_release(release)
        with tempfile.TemporaryDirectory() as temp:
            release, _ = self.release(Path(temp))
            (release / 'old-link').symlink_to('/tmp')
            with self.assertRaisesRegex(RuntimeError, 'regular files'):
                module.verify_release(release)

    def test_in_place_cleanup_preserves_state_and_removes_stale_files(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            release, _ = self.release(base)
            (base / 'state').mkdir()
            (base / 'state/quota.db').write_bytes(b'ledger')
            (base / 'network.json').write_text('{}')
            for name in ('bin/retired', 'skills/retired/SKILL.md', 'catalog/settings.json', 'profiles/old.json'):
                path = release / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b'old')
            manifest = module.verify_release(release)
            before = release.stat().st_ino
            module.clean_release(release, manifest)
            module.clean_release(release, manifest)
            self.assertEqual(release.stat().st_ino, before)
            self.assertEqual((base / 'state/quota.db').read_bytes(), b'ledger')
            self.assertTrue((base / 'network.json').exists())
            self.assertFalse((release / 'bin/retired').exists())
            self.assertFalse((release / 'skills/retired').exists())
            self.assertFalse((release / 'catalog').exists())
            module.verify_release(release)

    def test_pid_reuse_is_never_signalled(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            (state / 'proxy.pid.json').write_text(json.dumps({'pid': 42, 'identity': 'old'}))
            with patch.object(module, 'process_identity', return_value='unrelated'), patch.object(module.os, 'kill') as kill:
                with self.assertRaises(RuntimeError):
                    module.stop(state, 'proxy')
                kill.assert_not_called()

    def test_stop_waits_for_sigkill_exit_and_retains_pid_on_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            record = state / 'proxy.pid.json'
            record.write_text(json.dumps({'pid': 42, 'identity': 'old'}))
            with patch.object(module, 'process_identity', return_value='old'), \
                    patch.object(module.os, 'kill') as kill, patch.object(module.time, 'sleep'):
                with self.assertRaisesRegex(RuntimeError, 'did not stop'):
                    module.stop(state, 'proxy')
            self.assertEqual([c.args[1] for c in kill.call_args_list], [module.signal.SIGTERM, module.signal.SIGKILL])
            self.assertTrue(record.exists())

    def test_reboot_discards_old_pid_without_signalling_new_process(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            record = state / 'manager.pid.json'
            record.write_text(json.dumps({'pid': 42, 'identity': 'previous-boot:123'}))
            with patch.object(module, 'process_identity', return_value='current-boot:123'), \
                    patch.object(module.os, 'kill') as kill:
                module.stop(state, 'manager')
                kill.assert_not_called()
            self.assertFalse(record.exists())

    def test_manager_retains_configuration_and_waits_for_guard(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            config = base / 'manager.json'
            config.write_text('{"daily_limit_bytes":123456}')
            original = config.read_bytes()
            with patch('configure.manager_configuration') as generate, \
                    patch('configure.host_address', return_value='192.168.1.5'), \
                    patch('configure.require_guard', side_effect=[OSError('starting'), None]) as ready, \
                    patch.object(module, 'start') as start, patch.object(module, 'managed_pid', return_value=42), \
                    patch.object(module.time, 'sleep'):
                module.start_manager(base / 'release', base, {})
            self.assertEqual(config.read_bytes(), original)
            generate.assert_not_called()
            self.assertEqual(ready.call_count, 2)
            self.assertIn(config, start.call_args.args[2])

    def test_cleanup_lists_all_pages_with_ownership_selector(self):
        pages = [json.dumps({'sandboxes': [{'name': 'a'}], 'next_page_token': 'next'}),
                 json.dumps({'sandboxes': [{'name': 'b'}]})]
        with patch.object(module, 'run', side_effect=pages) as run:
            self.assertEqual(module.sandboxes({}), [{'name': 'a'}, {'name': 'b'}])
        for call in run.call_args_list:
            self.assertIn('laxcode-app=laxcode', call.args)
        self.assertEqual(run.call_args_list[1].args[-1], 'next')

    def test_image_replacement_removes_old_and_dangling_but_not_current(self):
        with tempfile.TemporaryDirectory() as temp:
            release = Path(temp)
            (release / 'image').mkdir()
            commands = []
            def run(*args, **kwargs):
                commands.append(args)
                if args[:3] == ('docker', 'image', 'ls'):
                    if args[-1] == '-q':
                        return 'sha256:old\nsha256:current\nsha256:dangling'
                    return 'sha256:old\nsha256:current' if args[-1] == 'laxcode-cloud' else 'sha256:dangling'
                if args[:3] == ('docker', 'image', 'inspect'):
                    return 'sha256:current'
            with patch.object(module, 'run', side_effect=run):
                module.build_image(release)
            self.assertEqual([c[-1] for c in commands if c[:3] == ('docker', 'image', 'rm')],
                             ['sha256:dangling', 'sha256:old'])
            build = next(c for c in commands if c[:2] == ('docker', 'build'))
            self.assertIn('laxcode-cloud:current', build)
            self.assertFalse((release / 'image').exists())

    def test_image_store_may_already_remove_replaced_manifest(self):
        with tempfile.TemporaryDirectory() as temp:
            release = Path(temp)
            (release / 'image').mkdir()
            with patch.object(module, 'run', side_effect=['sha256:old', '', '', '', 'sha256:new', 'sha256:new']) as run:
                module.build_image(release)
            self.assertFalse(any(c.args[:3] == ('docker', 'image', 'rm') for c in run.call_args_list))

    def test_failed_image_build_does_not_delete_previous_image(self):
        with patch.object(module, 'run', side_effect=['sha256:old', '', RuntimeError('build failed')]) as run:
            with self.assertRaisesRegex(RuntimeError, 'build failed'):
                module.build_image(Path('/release'))
            self.assertFalse(any(c.args[:3] == ('docker', 'image', 'rm') for c in run.call_args_list))

    def test_cold_restart_installs_guard_before_gateway_and_failure_never_rolls_back(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            release, manifest = self.release(base)
            events = []
            def run(*args, **kwargs):
                if len(args) > 2:
                    events.append(args[2])
                    if args[2] == 'register':
                        raise RuntimeError('provider failed')
            with patch.object(module, 'preflight', return_value=manifest), \
                    patch.object(module, 'managed_pid', return_value=None), \
                    patch.object(module, 'stop', side_effect=lambda _, name: events.append('stop-' + name)), \
                    patch.object(module, 'start_manager', side_effect=lambda *a: events.append('guard-ready')), \
                    patch.object(module, 'ensure_gateway', side_effect=lambda *a: events.append('gateway')), \
                    patch.object(module, 'remove_sandboxes', side_effect=lambda *a: events.append('delete-sandboxes')), \
                    patch.object(module, 'build_image', side_effect=lambda *a: events.append('image')), \
                    patch.object(module, 'start_proxy') as proxy, patch.object(module, 'run', side_effect=run):
                with self.assertRaisesRegex(RuntimeError, 'provider failed'):
                    module.restart(base, base / 'state', {})
                proxy.assert_not_called()
            self.assertEqual(events, ['stop-proxy', 'stop-manager', 'guard-ready', 'gateway',
                                      'delete-sandboxes', 'generate', 'image', 'register'])
            self.assertEqual(list(base.iterdir()), [release])
            module.verify_release(release)

    def test_gateway_has_dedicated_docker_namespace(self):
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)
            with patch.object(module, 'managed_pid', return_value=None), \
                    patch.object(module, 'run', return_value='unix:///var/run/docker.sock'), \
                    patch.object(module.socket, 'socket'), patch.object(module, 'start'), \
                    patch.object(module, 'wait_ready'), \
                    patch.object(module.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0)):
                module.ensure_gateway(state, {})
            config = (state / 'gateway.toml').read_text()
            self.assertIn('sandbox_label = "laxcode"', config)
            self.assertIn('image_pull_policy = "never"', config)

    def test_wrong_default_parent_rejected_and_probe_removed(self):
        with patch.object(module, 'run', side_effect=['container', '', '42', '']) as run, \
                patch.object(module.Path, 'read_text', return_value='0::/system.slice/docker-test.scope\n'):
            with self.assertRaisesRegex(RuntimeError, 'default cgroup parent'):
                module.verify_docker_parent('base')
            self.assertEqual(run.call_args.args, ('docker', 'rm', '-f', 'container'))

    def test_stop_without_gateway_refuses_to_abandon_running_sandboxes(self):
        with patch.object(module, 'managed_pid', return_value=None), \
                patch.object(module, 'run', return_value='container'), patch.object(module, 'stop') as stop:
            with self.assertRaisesRegex(RuntimeError, 'Gateway is stopped'):
                module.stop_application(Path('/state'), {})
            self.assertEqual([c.args[1] for c in stop.call_args_list], ['proxy'])


if __name__ == '__main__':
    unittest.main()
