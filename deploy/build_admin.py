"""Build the proxy with an administrator password verifier, never its plaintext password."""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys


def compile_config(path):
    try:
        config = json.loads(Path(path).read_text())
        username, password = config['username'], config['password']
        if (set(config) != {'username', 'password'} or not isinstance(username, str)
                or not isinstance(password, str) or not username.strip()
                or username != username.strip() or len(username.encode()) > 128
                or not 12 <= len(password.encode()) <= 1024
                or password == 'REPLACE_WITH_A_LONG_RANDOM_PASSWORD'):
            raise ValueError()
    except (OSError, ValueError, KeyError, TypeError):
        raise ValueError('Invalid admin configuration: set username and a password of 12–1024 bytes') from None
    salt = os.urandom(16)
    digest = hashlib.pbkdf2_hmac('sha256', password.encode(), salt, 600000, 32)
    verifier = {'username': username, 'salt': base64.b64encode(salt).decode(),
                'hash': base64.b64encode(digest).decode()}
    return base64.b64encode(json.dumps(verifier).encode()).decode()


def main():
    if len(sys.argv) != 4:
        sys.exit('Usage: build_admin.py CONFIG OUTPUT GUIDE_VERSION')
    config, output, guide = sys.argv[1:]
    if not re.fullmatch(r'[a-zA-Z0-9._-]{1,64}', guide):
        sys.exit('Invalid guide version')
    try:
        verifier = compile_config(config)
    except ValueError as error:
        sys.exit(str(error))
    # Structured arguments keep shell metacharacters in credentials out of shell code.
    result = subprocess.run(['go', 'build', '-tags=webdist', '-trimpath',
                             '-ldflags', f'-s -w -X main.guideVersion={guide} -X main.adminConfig={verifier}',
                             '-o', output, './laxcode-proxy'])
    sys.exit(result.returncode)


if __name__ == '__main__':
    main()
