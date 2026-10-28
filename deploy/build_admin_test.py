import base64
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import build_admin


class AdminBuildTest(unittest.TestCase):
    def test_hashes_password_without_shell_interpretation(self):
        password = "secret $(touch /tmp/should-not-exist) `echo nope` ' 中文"
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'admin.json'
            path.write_text(json.dumps({'username': '管理员', 'password': password}))
            encoded = build_admin.compile_config(path)
            config = json.loads(base64.b64decode(encoded))
            self.assertNotIn(password, str(config))
            self.assertNotIn('password', config)
            self.assertEqual(config['username'], '管理员')
            self.assertEqual(base64.b64decode(config['hash']), hashlib.pbkdf2_hmac(
                'sha256', password.encode(), base64.b64decode(config['salt']), 600000, 32))
            self.assertNotEqual(encoded, build_admin.compile_config(path))
            with patch.object(build_admin.sys, 'argv', ['build_admin.py', str(path), '/tmp/proxy', 'dev']), \
                    patch.object(build_admin.subprocess, 'run') as run:
                run.return_value.returncode = 0
                with self.assertRaises(SystemExit) as result:
                    build_admin.main()
                self.assertEqual(result.exception.code, 0)
                self.assertNotIn(password, str(run.call_args))
                self.assertIn('main.adminConfig=', run.call_args.args[0][5])

    def test_rejects_missing_invalid_and_example_credentials(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'admin.json'
            with self.assertRaises(ValueError):
                build_admin.compile_config(path)
            for data in ({}, [], {'username': 'admin', 'password': 'short'},
                         {'username': ' ', 'password': 'long-enough-password'},
                         {'username': 'admin', 'password': 'REPLACE_WITH_A_LONG_RANDOM_PASSWORD'}):
                path.write_text(json.dumps(data))
                with self.assertRaises(ValueError):
                    build_admin.compile_config(path)


if __name__ == '__main__':
    unittest.main()
