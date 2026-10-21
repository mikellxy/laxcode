import copy
import unittest
from configure import prepare

class ConfigTest(unittest.TestCase):
    def test_public_domain_scope_and_invalid_entries(self):
        from configure import network_hosts
        hosts = network_hosts({'additional_domains': ['example.com', 'github.com']})
        self.assertEqual(hosts.count('github.com'), 1)
        self.assertIn('**.*.*', hosts)
        self.assertIn('example.com', hosts)
        for host in ['*', '**', '*.com', '127.0.0.1', 'https://example.com', 'example.com:443', 'UPPER.com', 'a..com']:
            with self.subTest(host=host), self.assertRaises(ValueError):
                network_hosts({'additional_domains': [host]})

    def test_credentials_are_removed_without_mutating_source(self):
        source = {'model': 'ark:m', 'provider_list': [{'provider_name': 'ark', 'openai_base_url': 'https://example.com/api/v1', 'openai_api_key': 'real-secret', 'model_list': [{'model_name': 'm'}]}]}
        original = copy.deepcopy(source)
        public, registrations = prepare(source)
        self.assertEqual(source, original)
        profile, env, key = registrations[0]
        self.assertEqual(key, 'real-secret')
        self.assertEqual(public['provider_list'][0]['openai_api_key'], env)
        self.assertNotIn('real-secret', str(public) + str(profile))
        self.assertEqual(profile['binaries'], ['/usr/local/bin/laxcode-cloud'])
        self.assertEqual(profile['endpoints'][0]['host'], 'example.com')
        for bad in ['http://example.com', 'https://user:secret@example.com', 'https://example.com?key=x']:
            source['provider_list'][0]['openai_base_url'] = bad
            with self.assertRaises(ValueError): prepare(source)


class ReleaseConfigTest(unittest.TestCase):
    def test_build_context_excludes_real_credentials(self):
        import json
        from pathlib import Path
        import tempfile
        from configure import generate
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'bin').mkdir()
            (root / 'bin/laxcode-cloud').write_bytes(b'binary')
            (root / 'skills').mkdir()
            (root / 'skills/SKILL.md').write_text('skill')
            (root / 'Dockerfile').write_text('FROM base\n')
            (root / 'settings.json').write_text(json.dumps({'model': 'p:m', 'provider_list': [{'provider_name': 'p', 'openai_base_url': 'https://example.com', 'openai_api_key': 'secret-value', 'model_list': [{'model_name': 'm'}]}]}))
            from unittest.mock import patch
            with patch('configure.socket.socket') as socket, patch('configure.platform.system', return_value='Linux'), patch('configure.require_guard') as guard:
                socket.return_value.__enter__.return_value.getsockname.return_value = ('192.168.1.5', 10000)
                generate(root)
            guard.assert_called_once_with('192.168.1.5')
            policy = json.loads((root / 'policy.yaml').read_text())
            rule = policy['network_policies']['laxcode_storage']
            self.assertEqual(rule['binaries'], [{'path': '/usr/local/bin/laxcode-cloud'}])
            self.assertEqual(rule['endpoints'][0]['allowed_ips'], ['192.168.1.5/32'])
            self.assertEqual(rule['endpoints'][0]['rules'], [{'allow': {'method': 'POST', 'path': '/internal/store'}}])
            public_rule = policy['network_policies']['laxcode_public']
            self.assertEqual(public_rule['binaries'], [{'path': '/**'}])
            self.assertIn('**.*.*', [e['host'] for e in public_rule['endpoints']])
            self.assertTrue(all(e['ports'] == [80, 443] and 'allowed_ips' not in e
                                and e['enforcement'] == 'enforce' for e in public_rule['endpoints']))
            for path in (root / 'image').rglob('*'):
                if path.is_file():
                    self.assertNotIn(b'secret-value', path.read_bytes())
            self.assertIn('secret-value', (root / 'settings.json').read_text())
            self.assertEqual({str(p.relative_to(root / 'image')) for p in (root / 'image').rglob('*')},
                             {'bin', 'bin/laxcode-cloud', 'Dockerfile'})
            public = (root / 'catalog/settings.json').read_text()
            self.assertEqual((root / 'catalog/settings.json').stat().st_mode & 0o777, 0o444)
            self.assertNotIn('secret-value', public)
            self.assertTrue(json.loads(public)['provider_list'][0]['openai_api_key'].startswith('LAXCODE_API_KEY_'))
            self.assertTrue((root / 'skills/SKILL.md').is_file())

    def test_profile_update_uses_current_resource_version(self):
        import json
        from pathlib import Path
        import tempfile
        from unittest.mock import patch
        import subprocess
        from configure import generate, register
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'profiles').mkdir()
            source = {'model': 'p:m', 'provider_list': [{'provider_name': 'p', 'openai_base_url': 'https://example.com', 'openai_api_key': 'secret-value', 'model_list': [{'model_name': 'm'}]}]}
            (root / 'settings.json').write_text(json.dumps(source))
            with patch('configure.cli', return_value=True) as cli, patch('configure.subprocess.run', return_value=subprocess.CompletedProcess([], 0, b'{"resource_version":7}')):
                register(root)
            profile = json.loads(next((root / 'profiles').iterdir()).read_text())
            self.assertEqual(profile['resource_version'], 7)
            update = [call for call in cli.call_args_list if call.args[:2] == ('provider', 'update')][0]
            self.assertNotIn('secret-value', str(update.args))
            self.assertIn('secret-value', update.kwargs['env'].values())

if __name__ == '__main__': unittest.main()
