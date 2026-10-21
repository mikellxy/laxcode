#!/bin/sh
set -eu
exec python3 - "$0" "$@" <<'PY'
"""Standalone host deployer. Copy this file and laxcode.tar.gz to ~/laxcode_deploy."""
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request

VERSION = '0.1.4'
BASE_IMAGE = 'laxcode-tools:go1.26.6-v2'
ENDPOINT = 'http://127.0.0.1:18080'
LABEL = 'laxcode-app=laxcode'


def run(*args, env=None, capture=False):
    return subprocess.run([str(a) for a in args], env=env, check=True,
                          text=True, stdout=subprocess.PIPE if capture else None).stdout


def unpack(archive, dest):
    with tarfile.open(archive, 'r:gz') as tar:
        seen = set()
        for member in tar.getmembers():
            path = PurePosixPath(member.name)
            if (path.is_absolute() or '..' in path.parts or not path.parts
                    or member.name in seen or not (member.isfile() or member.isdir())):
                raise RuntimeError('Unsafe or duplicate archive entry')
            seen.add(member.name)
            target = dest.joinpath(*path.parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with tar.extractfile(member) as source, target.open('xb') as output:
                    shutil.copyfileobj(source, output)
                target.chmod(0o700 if member.mode & 0o111 else 0o600)
    manifest = json.loads((dest / 'manifest.json').read_text())
    host_os = {'Darwin': 'darwin', 'Linux': 'linux'}.get(platform.system())
    arch = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64'}.get(platform.machine())
    if (manifest.get('format'), manifest.get('os'), manifest.get('arch'),
            manifest.get('openshell'), manifest.get('base_image')) != (1, host_os, arch, VERSION, BASE_IMAGE):
        raise RuntimeError('Release OS/architecture or dependency version does not match this host')
    actual = {str(p.relative_to(dest)): hashlib.sha256(p.read_bytes()).hexdigest()
              for p in dest.rglob('*') if p.is_file() and p != dest / 'manifest.json'}
    if actual != manifest['files'] or not {'bin/laxcode-proxy', 'bin/laxcode-cloud', 'bin/laxcode-manager', 'manager/install.py', 'Dockerfile', 'configure.py', 'settings.json', 'network.json'} <= actual.keys():
        raise RuntimeError('Release file checksums do not match manifest')
    if not (dest / 'skills').is_dir():
        raise RuntimeError('Release skills directory missing')
    return manifest


def process_identity(pid):
    result = subprocess.run(['ps', '-p', str(pid), '-o', 'lstart=', '-o', 'command='],
                            text=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    return result.stdout.strip() if result.returncode == 0 else ''


def managed_pid(state, name):
    record = state / (name + '.pid.json')
    if not record.exists():
        return None
    value = json.loads(record.read_text())
    identity = process_identity(value['pid'])
    if not identity:
        record.unlink()
        return None
    if identity != value['identity']:
        raise RuntimeError(f'{name} PID was reused; refusing to signal another process')
    return value['pid']


def start(state, name, args, env):
    with (state / (name + '.log')).open('ab') as log:
        child = subprocess.Popen([str(a) for a in args], env=env, stdin=subprocess.DEVNULL,
                                 stdout=log, stderr=log, start_new_session=True)
    # Capture identity after exec, not the transient forked Python command.
    for _ in range(50):
        identity = process_identity(child.pid)
        if child.poll() is not None:
            raise RuntimeError(f'{name} exited; inspect {state / (name + ".log")}')
        if str(args[0]) in identity:
            break
        time.sleep(0.1)
    else:
        child.terminate()
        raise RuntimeError(f'{name} failed to exec')
    (state / (name + '.pid.json')).write_text(json.dumps({'pid': child.pid, 'identity': identity}))


def stop(state, name):
    pid = managed_pid(state, name)
    if pid is None:
        return
    os.kill(pid, signal.SIGTERM)
    for _ in range(300):
        if not process_identity(pid):
            break
        time.sleep(0.1)
    else:
        if managed_pid(state, name) == pid:
            os.kill(pid, signal.SIGKILL)
    (state / (name + '.pid.json')).unlink(missing_ok=True)


def healthy(url):
    try:
        with urllib.request.urlopen(url, timeout=2) as response:
            return response.status == 200
    except (OSError, ValueError):
        return False


def wait_ready(state, name, url):
    for _ in range(120):
        if not managed_pid(state, name):
            break
        if healthy(url):
            return
        time.sleep(0.5)
    raise RuntimeError(f'{name} not healthy; inspect {state / (name + ".log")}')


def environment(state):
    # Isolate gateway metadata, credentials and CLI defaults from the host's other OpenShell installations.
    env = {k: v for k, v in os.environ.items() if not k.startswith('OPENSHELL_')}
    env.update(XDG_CONFIG_HOME=str(state / 'config'), XDG_STATE_HOME=str(state / 'data'),
               XDG_DATA_HOME=str(state / 'data'), OPENSHELL_GATEWAY='laxcode',
               OPENSHELL_WORKSPACE='default', OPENSHELL_COLOR='never')
    return env


def ensure_gateway(state, env):
    if managed_pid(state, 'gateway'):
        wait_ready(state, 'gateway', 'http://127.0.0.1:18081/health')
        run('openshell', 'gateway', 'select', 'laxcode', env=env)
        return
    # Never attach to an unrelated service just because it occupies our port.
    for port in (18080, 18081):
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', port))
    endpoint = os.environ.get('DOCKER_HOST') or run('docker', 'context', 'inspect', '--format', '{{(index .Endpoints "docker").Host}}', capture=True).strip()
    if not endpoint.startswith('unix://'):
        raise RuntimeError('Deployment requires a local Docker Unix socket')
    tls = state / 'tls'
    if not (tls / 'jwt/signing.pem').exists():
        run('openshell-gateway', 'generate-certs', '--output-dir', tls, env=env)
    q = lambda value: json.dumps(str(value))
    config = f'''[openshell]
version = 2
[openshell.gateway]
bind_address = "127.0.0.1:18080"
health_bind_address = "127.0.0.1:18081"
compute_driver = "docker"
disable_tls = true
log_level = "info"
[openshell.gateway.auth]
allow_unauthenticated_users = true
[openshell.gateway.gateway_jwt]
signing_key_path = {q(tls / 'jwt/signing.pem')}
public_key_path = {q(tls / 'jwt/public.pem')}
kid_path = {q(tls / 'jwt/kid')}
gateway_id = "laxcode"
[openshell.drivers.docker]
sandbox_label = "laxcode"
socket_path = {q(endpoint[7:])}
default_image = "laxcode-cloud:current"
sandbox_runtime_image = "ghcr.io/nvidia/openshell/sandbox:{VERSION}"
supervisor_image = "ghcr.io/nvidia/openshell/supervisor:{VERSION}"
image_pull_policy = "never"
app_armor_profile = "Unconfined"
allow_driver_config = true
enable_bind_mounts = true
[openshell.drivers.docker.resource_admission]
enabled = false
'''
    path = state / 'gateway.toml'
    path.write_text(config)
    gateway_env = dict(env, OPENSHELL_DB_URL='sqlite:' + str(state / 'gateway.db') + '?mode=rwc')
    start(state, 'gateway', [shutil.which('openshell-gateway'), '--name', 'laxcode', '--config', path], gateway_env)
    wait_ready(state, 'gateway', 'http://127.0.0.1:18081/health')
    selected = subprocess.run(['openshell', 'gateway', 'select', 'laxcode'], env=env,
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if selected.returncode:
        run('openshell', 'gateway', 'add', ENDPOINT, '--local', '--name', 'laxcode', env=env)


def sandboxes(env):
    result, token = [], ''
    while True:
        page = json.loads(run('openshell', 'sandbox', 'list', '--selector', LABEL,
                              '--output', 'json', '--page-token', token, env=env, capture=True))
        result.extend(page.get('sandboxes', []))
        token = page.get('next_page_token', '')
        if not token:
            return result


def start_proxy(base, state, env):
    release = base / 'laxcode'
    image = (release / 'image-tag').read_text().strip()
    start(state, 'proxy', [release / 'bin/laxcode-proxy', '-addr=0.0.0.0:5173',
                         '-workplaces=' + str(Path.home() / 'laxcode'), '-image=' + image,
                         '-policy=' + str(release / 'policy.yaml'),
                         '-providers-file=' + str(release / 'providers.json'),
                         '-settings-dir=' + str(release / 'catalog'),
                         '-store-url=' + (release / 'store-url').read_text().strip(), '-skills=' + str(release / 'skills')], env)
    wait_ready(state, 'proxy', 'http://127.0.0.1:5173/healthz')


def verify_docker_parent(image):
    if platform.system() != 'Linux':
        return
    # Verify the daemon's effective default, including flags outside daemon.json.
    ident = run('docker', 'create', '--network=none', '--entrypoint=/bin/sleep',
                image, '30', capture=True).strip()
    try:
        run('docker', 'start', ident, capture=True)
        pid = int(run('docker', 'inspect', '--format', '{{.State.Pid}}', ident, capture=True))
        groups = Path(f'/proc/{pid}/cgroup').read_text().splitlines()
        if not any(line.startswith('0::/laxcode.slice/') for line in groups):
            raise RuntimeError('Docker default cgroup parent is not laxcode.slice; run the dedicated-host migration first')
    finally:
        run('docker', 'rm', '-f', ident, capture=True)


def restart(base, state, env):
    for binary in ('docker', 'openshell', 'openshell-gateway'):
        if not shutil.which(binary):
            raise RuntimeError(f'Install prerequisite: {binary}')
    for binary in ('openshell', 'openshell-gateway'):
        if run(binary, '--version', capture=True).strip().split()[-1] != VERSION:
            raise RuntimeError(f'{binary} must be version {VERSION}')
    release = base / 'laxcode'
    with tempfile.TemporaryDirectory(prefix='.stage-', dir=base) as temporary:
        stage = Path(temporary) / 'laxcode'
        stage.mkdir()
        manifest = unpack(base / 'laxcode.tar.gz', stage)
        if (base / 'network.json').exists():
            shutil.copy2(base / 'network.json', stage / 'network.json')
        for image in (BASE_IMAGE, f'ghcr.io/nvidia/openshell/supervisor:{VERSION}', f'ghcr.io/nvidia/openshell/sandbox:{VERSION}'):
            details = json.loads(run('docker', 'image', 'inspect', image, capture=True))[0]
            if details['Os'] != 'linux' or details['Architecture'] != manifest['arch']:
                raise RuntimeError(f'Preinstalled image architecture mismatch: {image}')
        verify_docker_parent(BASE_IMAGE)
        run(sys.executable, stage / 'configure.py', 'generate', stage)
        digest = hashlib.sha256((stage / 'manifest.json').read_bytes()).hexdigest()[:16]
        image = 'laxcode-cloud:' + digest
        run('docker', 'build', '--pull=false', '--network=none', '-t', image, stage / 'image')
        run('docker', 'run', '--rm', '--network=none', '--entrypoint', '/bin/sh', image, '-c',
            'test -x /usr/local/bin/laxcode-cloud')
        shutil.rmtree(stage / 'image')
        (stage / 'image-tag').write_text(image)
        ensure_gateway(state, env)
        # Preflight management access before interrupting the current application.
        owned = sandboxes(env)
        previous = base / '.previous'
        if previous.exists() and release.exists():
            shutil.rmtree(previous)
        print('Stopping proxy and removing LaxCode application sandboxes', flush=True)
        stop(state, 'proxy')
        try:
            for sandbox in owned:
                run('openshell', 'sandbox', 'delete', sandbox['name'], env=env)
            for _ in range(240):
                if not sandboxes(env):
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError('OpenShell sandbox deletion did not finish; release not replaced')
            if release.exists():
                release.rename(previous)
            stage.rename(release)
            run(sys.executable, release / 'configure.py', 'register', release, env=env)
            start_proxy(base, state, env)
            run('docker', 'tag', image, 'laxcode-cloud:current')
        except Exception:
            stop(state, 'proxy')
            if previous.exists():
                if release.exists():
                    shutil.rmtree(release)
                previous.rename(release)
            if release.exists():
                run(sys.executable, release / 'configure.py', 'register', release, env=env)
                start_proxy(base, state, env)
                print('Previous release restored; user files were preserved', flush=True)
            raise
        print('LaxCode ready: http://localhost:5173 (gateway kept running)', flush=True)


def main():
    os.umask(0o077)
    base = Path(sys.argv[1]).resolve().parent
    command = sys.argv[2] if len(sys.argv) == 3 else ''
    if command not in ('restart', 'stop', 'status'):
        raise RuntimeError('Usage: deploy.sh restart|stop|status')
    state = base / 'state'
    state.mkdir(mode=0o700, exist_ok=True)
    with (base / '.deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        env = environment(state)
        if command == 'restart':
            restart(base, state, env)
        elif command == 'stop':
            stop(state, 'proxy')
            if managed_pid(state, 'gateway'):
                for sandbox in sandboxes(env):
                    if sandbox['phase'] not in ('Stopped', 'Completed'):
                        run('openshell', 'sandbox', 'stop', sandbox['name'], env=env)
            print('Application stopped; gateway and user files retained')
        else:
            for name in ('gateway', 'proxy'):
                print(f'{name}: {managed_pid(state, name) or "stopped"}')


if __name__ == '__main__':
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        # No settings parser fragments or credential-bearing command output.
        print(f'Deployment failed ({type(error).__name__}): ' +
              (str(error) if isinstance(error, RuntimeError) else 'inspect state logs and prerequisites'), file=sys.stderr)
        sys.exit(1)
PY
