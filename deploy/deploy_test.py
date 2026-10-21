import hashlib
import io
import json
from pathlib import Path
import platform
import tarfile
import tempfile
import types
import unittest
from unittest.mock import patch

# deploy.sh is intentionally standalone; execute only its Python definitions.
module = types.ModuleType('host_deploy')
script = Path(__file__).with_name('deploy.sh').read_text().split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
exec(compile(script, 'deploy.sh', 'exec'), module.__dict__)


class DeployTest(unittest.TestCase):
    def test_proxy_catalog_survives_build_context_cleanup(self):
        from configure import generate
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            release = base / 'laxcode'
            (release / 'bin').mkdir(parents=True)
            (release / 'bin/laxcode-cloud').write_bytes(b'binary')
            (release / 'skills').mkdir()
            (release / 'Dockerfile').write_text('FROM base\n')
            (release / 'settings.json').write_text(json.dumps({'model': 'p:m', 'provider_list': [{
                'provider_name': 'p', 'openai_base_url': 'https://example.com',
                'openai_api_key': 'secret-value', 'model_list': [{'model_name': 'm'}]}]}))
            with patch('configure.socket.socket') as socket, patch('configure.require_guard'):
                socket.return_value.__enter__.return_value.getsockname.return_value = ('192.168.1.5', 10000)
                generate(release)
            expected = (release / 'catalog/settings.json').read_bytes()
            module.shutil.rmtree(release / 'image')
            (release / 'image-tag').write_text('laxcode-cloud:test')
            with patch.object(module, 'start') as start, patch.object(module, 'wait_ready'):
                module.start_proxy(base, base / 'state', {})
            args = start.call_args.args[2]
            catalog = Path(next(arg.split('=', 1)[1] for arg in args if str(arg).startswith('-settings-dir=')))
            self.assertEqual((catalog / 'settings.json').read_bytes(), expected)
            self.assertNotIn(b'secret-value', expected)

    def archive(self, root, extra=None, corrupt=False):
        files = {name: b'content' for name in ('bin/laxcode-cloud', 'bin/laxcode-proxy', 'bin/laxcode-manager', 'manager/install.py', 'network.json', 'settings.json', 'configure.py', 'Dockerfile', 'skills/SKILL.md')}
        manifest = {'format': 1, 'os': {'Darwin': 'darwin', 'Linux': 'linux'}[platform.system()],
                    'arch': {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64'}[platform.machine()],
                    'openshell': module.VERSION, 'base_image': module.BASE_IMAGE,
                    'files': {name: hashlib.sha256(data).hexdigest() for name, data in files.items()}}
        files['manifest.json'] = json.dumps(manifest).encode()
        if corrupt:
            files['settings.json'] = b'changed'
        path = root / 'release.tar.gz'
        with tarfile.open(path, 'w:gz') as tar:
            for name, data in files.items():
                member = tarfile.TarInfo(name)
                member.size = len(data)
                tar.addfile(member, io.BytesIO(data))
            if extra:
                tar.addfile(extra)
        return path

    def test_validate_and_reject_corruption(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            module.unpack(self.archive(root), root / 'valid')
            with self.assertRaises(RuntimeError):
                module.unpack(self.archive(root, corrupt=True), root / 'corrupt')

    def test_archive_rejects_escape_and_links(self):
        for name, kind in (('../escape', tarfile.REGTYPE), ('link', tarfile.SYMTYPE), ('hard', tarfile.LNKTYPE)):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                member = tarfile.TarInfo(name)
                member.type, member.linkname = kind, '/etc/passwd'
                with self.assertRaises(RuntimeError):
                    module.unpack(self.archive(root, extra=member), root / 'out')
                self.assertFalse((root / 'escape').exists())

    def test_cleanup_lists_all_pages_with_ownership_selector(self):
        pages = [json.dumps({'sandboxes': [{'name': 'a'}], 'next_page_token': 'next'}),
                 json.dumps({'sandboxes': [{'name': 'b'}]})]
        with patch.object(module, 'run', side_effect=pages) as run:
            self.assertEqual(module.sandboxes({}), [{'name': 'a'}, {'name': 'b'}])
        for call in run.call_args_list:
            self.assertIn('laxcode-app=laxcode', call.args)
        self.assertEqual(run.call_args_list[1].args[-1], 'next')

    def test_pid_reuse_is_never_signalled(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'proxy.pid.json').write_text(json.dumps({'pid': 42, 'identity': 'old'}))
            with patch.object(module, 'process_identity', return_value='unrelated'), patch.object(module.os, 'kill') as kill:
                with self.assertRaises(RuntimeError):
                    module.stop(root, 'proxy')
                kill.assert_not_called()


    def test_build_failure_does_not_stop_existing_service(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            state = base / 'state'
            state.mkdir()
            def run(*args, **kwargs):
                if args[1] == '--version': return 'openshell 0.1.4'
                if args[:3] == ('docker', 'image', 'inspect'):
                    return '[{"Os":"linux","Architecture":"arm64"}]'
                if args[:2] == ('docker', 'build'):
                    raise RuntimeError('build failed')
            def unpack(archive, stage):
                (stage / 'manifest.json').write_text('{}')
                return {'arch': 'arm64'}
            with patch.object(module, 'verify_docker_parent'), patch.object(module, 'run', side_effect=run), patch.object(module, 'unpack', side_effect=unpack), patch.object(module.shutil, 'which', return_value='/bin/tool'), patch.object(module, 'stop') as stop:
                with self.assertRaisesRegex(RuntimeError, 'build failed'):
                    module.restart(base, state, {})
                stop.assert_not_called()

    def test_provider_failure_rolls_back_release(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            state = base / 'state'
            state.mkdir()
            release = base / 'laxcode'
            release.mkdir()
            (release / 'marker').write_text('old')
            def unpack(archive, stage):
                (stage / 'manifest.json').write_text('{}')
                (stage / 'marker').write_text('new')
                (stage / 'image').mkdir()
                return {'arch': 'arm64'}
            def run(*args, **kwargs):
                if args[1] == '--version': return 'openshell 0.1.4'
                if args[:3] == ('docker', 'image', 'inspect'):
                    return '[{"Os":"linux","Architecture":"arm64"}]'
                if len(args) > 2 and args[2] == 'register' and (release / 'marker').read_text() == 'new':
                    raise RuntimeError('provider failed')
            with patch.object(module, 'verify_docker_parent'), patch.object(module, 'run', side_effect=run), patch.object(module, 'unpack', side_effect=unpack), patch.object(module.shutil, 'which', return_value='/bin/tool'), patch.object(module, 'ensure_gateway'), patch.object(module, 'sandboxes', return_value=[]), patch.object(module, 'stop'), patch.object(module, 'start_proxy') as start:
                with self.assertRaisesRegex(RuntimeError, 'provider failed'):
                    module.restart(base, state, {})
                self.assertEqual((release / 'marker').read_text(), 'old')
                start.assert_called_once()

    def test_gateway_has_dedicated_docker_namespace(self):
        import subprocess
        with tempfile.TemporaryDirectory() as temporary:
            state = Path(temporary)
            with patch.object(module, 'managed_pid', return_value=None), patch.object(module, 'run', return_value='unix:///tmp/docker.sock'), patch.object(module.socket, 'socket'), patch.object(module, 'start'), patch.object(module, 'wait_ready'), patch.object(module.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0)):
                module.ensure_gateway(state, {})
            config = (state / 'gateway.toml').read_text()
            self.assertIn('sandbox_label = "laxcode"', config)
            self.assertIn('image_pull_policy = "never"', config)

    def test_wrong_default_parent_rejected_and_probe_removed(self):
        with patch.object(module.platform, 'system', return_value='Linux'), \
                patch.object(module, 'run', side_effect=['container', '', '42', '']) as run, \
                patch.object(module.Path, 'read_text', return_value='0::/system.slice/docker-test.scope\n'):
            with self.assertRaisesRegex(RuntimeError, 'default cgroup parent'):
                module.verify_docker_parent('base')
            self.assertEqual(run.call_args.args, ('docker', 'rm', '-f', 'container'))


if __name__ == '__main__':
    unittest.main()
