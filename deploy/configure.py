#!/usr/bin/env python3
"""Register credentials outside sandboxes and produce their public model catalog."""
import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import urlsplit


def prepare(source):
    catalog = copy.deepcopy(source)
    registrations = []
    names = set()
    models = set()
    for provider in catalog.get('provider_list', []):
        name = provider['provider_name']
        if not isinstance(name, str) or not name or name in names:
            raise ValueError('provider names must be nonempty and unique')
        names.add(name)
        if not provider.get('model_list'):
            raise ValueError('provider must have models')
        for model in provider['model_list']:
            model_name = model['model_name']
            qualified = name + ':' + model_name
            if not model_name or qualified in models:
                raise ValueError('model names must be nonempty and unique')
            models.add(qualified)
        url = urlsplit(provider['openai_base_url'])
        if url.scheme != 'https' or not url.hostname or url.username or url.password or url.query or url.fragment:
            raise ValueError(f'{name}: base URL must be HTTPS without credentials, query or fragment')
        key = provider['openai_api_key']
        if not isinstance(key, str) or not key.strip():
            raise ValueError(f'{name}: API key is empty')
        ident = hashlib.sha256((name + '\0' + url.netloc).encode()).hexdigest()[:20]
        env = 'LAXCODE_API_KEY_' + ident.upper()
        profile = {
            'id': 'laxcode-' + ident, 'display_name': name,
            'description': 'LaxCode managed inference', 'category': 'inference', 'inference_capable': True,
            'credentials': [{'name': 'api_key', 'env_vars': [env], 'required': True,
                             'auth_style': 'bearer', 'header_name': 'authorization'}],
            'discovery': {'credentials': ['api_key']},
            'endpoints': [{'host': url.hostname, 'port': url.port or 443, 'protocol': 'rest',
                           'access': 'read-write', 'enforcement': 'enforce'}],
            'binaries': ['/usr/local/bin/laxcode-cloud'],
        }
        registrations.append((profile, env, key))
        provider['openai_api_key'] = env
    if catalog.get('model') not in models:
        raise ValueError('default model must exist in provider catalog')
    return catalog, registrations


def cli(*args, env=None, check=True):
    # Do not relay provider output: some CLI commands include resolved credentials.
    result = subprocess.run(['openshell', *args], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if check and result.returncode:
        raise RuntimeError(f'OpenShell {args[0]} {args[1]} failed (exit {result.returncode})')
    return result.returncode == 0


def generate(release):
    catalog, registrations = prepare(json.loads((release / 'settings.json').read_text()))
    image = release / 'image'
    image.mkdir()
    (image / 'bin').mkdir()
    import shutil
    shutil.copy2(release / 'bin/laxcode-cloud', image / 'bin/laxcode-cloud')
    shutil.copytree(release / 'skills', image / 'skills')
    # Build context deliberately excludes the release's real settings and credentials.
    shutil.copy2(release / 'Dockerfile', image / 'Dockerfile')
    (image / 'settings.json').write_text(json.dumps(catalog, indent=2) + '\n')
    for path in image.rglob('*'):
        path.chmod(0o755 if path.is_dir() or path.stat().st_mode & 0o111 else 0o644)
    profiles = release / 'profiles'
    profiles.mkdir()
    for profile, _, _ in registrations:
        (profiles / (profile['id'] + '.json')).write_text(json.dumps(profile))
    (release / 'providers.json').write_text(json.dumps([p['id'] for p, _, _ in registrations]))
    (release / 'policy.yaml').write_text('version: 1\nfilesystem_policy:\n  include_workdir: true\n  read_only: [/bin, /usr, /lib, /proc, /dev/urandom, /etc, /var/log, /opt]\n  read_write: [/sandbox, /workplace, /tmp, /dev/null]\nlandlock:\n  compatibility: best_effort\nnetwork_policies: {}\n')


def register(release):
    _, registrations = prepare(json.loads((release / 'settings.json').read_text()))
    for profile, env_name, key in registrations:
        name = profile['id']
        profile_path = release / 'profiles' / (name + '.json')
        if cli('profile', 'describe', name, check=False):
            current = subprocess.run(['openshell', 'profile', 'export', name, '-o', 'json'],
                                     check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            profile['resource_version'] = json.loads(current.stdout)['resource_version']
            profile_path.write_text(json.dumps(profile))
            cli('profile', 'update', name, '-f', str(profile_path))
        else:
            cli('profile', 'import', '-f', str(profile_path))
        env = dict(os.environ, **{env_name: key})
        if cli('provider', 'get', name, check=False):
            cli('provider', 'update', name, '--credential', env_name, env=env)
        else:
            cli('provider', 'create', '--name', name, '--type', name,
                '--label', 'laxcode-app=laxcode', '--credential', env_name, env=env)
    print(f'Registered {len(registrations)} providers (credentials kept outside sandbox)', flush=True)


if __name__ == '__main__':
    try:
        action, release = sys.argv[1], Path(sys.argv[2])
        {'generate': generate, 'register': register}[action](release)
    except (KeyError, ValueError, TypeError, RuntimeError, OSError, subprocess.SubprocessError):
        # Never include parser fragments, CLI output, or credential values in logs.
        print('Model configuration failed; verify settings and gateway', file=sys.stderr)
        sys.exit(1)
