#!/usr/bin/env python3
"""Restart an already extracted Linux amd64 release in place."""
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
import time
import urllib.request

VERSION = '0.1.4'
BASE_IMAGE = 'laxcode-tools:go1.26.6-v2'
ENDPOINT = 'http://127.0.0.1:18080'
LABEL = 'laxcode-app=laxcode'
IMAGE = 'laxcode-cloud:current'
IMAGE_LABEL = 'com.laxcode.component=cloud'


def run(*args, env=None, capture=False):
    return subprocess.run([str(a) for a in args], env=env, check=True,
                          text=True, stdout=subprocess.PIPE if capture else None).stdout


def verify_release(release):
    manifest = json.loads((release / 'manifest.json').read_text())
    if (manifest.get('format'), manifest.get('os'), manifest.get('arch'),
            manifest.get('openshell'), manifest.get('base_image')) != (2, 'linux', 'amd64', VERSION, BASE_IMAGE):
        raise RuntimeError('Release must target Linux amd64 and the installed dependencies')
    files = manifest['files']
    required = {'bin/laxcode-proxy', 'bin/laxcode-cloud', 'bin/laxcode-manager',
                'deploy.py', 'reset_quota.py', 'configure.py', 'Dockerfile', 'settings.json', 'network.json'}
    if not required <= files.keys() or not (release / 'skills').is_dir():
        raise RuntimeError('Release files missing')
    for path in release.rglob('*'):
        if path.is_symlink() or not (path.is_file() or path.is_dir()):
            raise RuntimeError('Release must contain only regular files and directories')
    for name, digest in files.items():
        path = PurePosixPath(name)
        if path.is_absolute() or '..' in path.parts or str(path) != name or name in ('.', 'manifest.json'):
            raise RuntimeError('Invalid manifest path')
        target = release / name
        if not target.is_file() or hashlib.sha256(target.read_bytes()).hexdigest() != digest:
            raise RuntimeError('Release checksum mismatch: ' + name)
    return manifest


def clean_release(release, manifest):
    # This directory contains only replaceable application files; state lives beside it.
    keep = set(manifest['files']) | {'manifest.json'}
    for path in sorted(release.rglob('*'), key=lambda p: len(p.parts), reverse=True):
        if path.is_file() and str(path.relative_to(release)) not in keep:
            path.unlink()
        elif path.is_dir() and path != release / 'skills' and not any(path.iterdir()):
            path.rmdir()


def process_identity(pid):
    try:
        # Start time plus boot ID survives executable replacement and rejects PID reuse.
        fields = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
        if fields[0] == 'Z':
            return ''
        return Path('/proc/sys/kernel/random/boot_id').read_text().strip() + ':' + fields[19]
    except FileNotFoundError:
        return ''


def managed_pid(state, name):
    record = state / (name + '.pid.json')
    if not record.exists():
        return None
    value = json.loads(record.read_text())
    identity = process_identity(value['pid'])
    previous = value['identity']
    different_boot = ':' in identity and ':' in previous and identity.split(':')[0] != previous.split(':')[0]
    if not identity or different_boot:
        record.unlink()
        return None
    if identity != previous:
        raise RuntimeError(f'{name} PID was reused; refusing to signal another process')
    return value['pid']


def start(state, name, args, env):
    with (state / (name + '.log')).open('ab') as log:
        child = subprocess.Popen([str(a) for a in args], env=env, stdin=subprocess.DEVNULL,
                                 stdout=log, stderr=log, start_new_session=True)
    identity = process_identity(child.pid)
    if child.poll() is not None or not identity:
        raise RuntimeError(f'{name} exited; inspect {state / (name + ".log")}')
    (state / (name + '.pid.json')).write_text(json.dumps({'pid': child.pid, 'identity': identity}))


def stop(state, name):
    pid = managed_pid(state, name)
    if pid is None:
        return
    os.kill(pid, signal.SIGTERM)
    for attempt in range(350):
        if not managed_pid(state, name):
            return
        if attempt == 300:
            os.kill(pid, signal.SIGKILL)
        time.sleep(0.1)
    raise RuntimeError(f'{name} did not stop; refusing to continue deployment')


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
            probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
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
    start(state, 'proxy', [release / 'bin/laxcode-proxy', '-addr=0.0.0.0:5173',
                         '-workplaces=' + str(Path.home() / 'laxcode'), '-image=' + IMAGE,
                         '-policy=' + str(release / 'policy.yaml'),
                         '-providers-file=' + str(release / 'providers.json'),
                         '-settings-dir=' + str(release / 'catalog'),
                         '-store-url=' + (release / 'store-url').read_text().strip(), '-skills=' + str(release / 'skills')], env)
    wait_ready(state, 'proxy', 'http://127.0.0.1:5173/healthz')


def verify_docker_parent(image):
    # Verify the daemon's effective default, including flags outside daemon.json.
    ident = run('docker', 'create', '--network=none', '--entrypoint=/bin/sleep',
                image, '30', capture=True).strip()
    try:
        run('docker', 'start', ident, capture=True)
        pid = int(run('docker', 'inspect', '--format', '{{.State.Pid}}', ident, capture=True))
        groups = Path(f'/proc/{pid}/cgroup').read_text().splitlines()
        if not any(line.startswith('0::/laxcode.slice/') for line in groups):
            raise RuntimeError('Docker default cgroup parent is not laxcode.slice; configure dockerd cgroup-parent=laxcode.slice in the base environment')
    finally:
        run('docker', 'rm', '-f', ident, capture=True)


def preflight(release):
    if os.geteuid() != 0 or platform.system() != 'Linux' or platform.machine() != 'x86_64':
        raise RuntimeError('Deployment requires root on Linux amd64')
    manifest = verify_release(release)
    for binary in ('docker', 'openshell', 'openshell-gateway'):
        if not shutil.which(binary):
            raise RuntimeError(f'Install prerequisite: {binary}')
    for binary in ('openshell', 'openshell-gateway'):
        if run(binary, '--version', capture=True).strip().split()[-1] != VERSION:
            raise RuntimeError(f'{binary} must be version {VERSION}')
    endpoint = os.environ.get('DOCKER_HOST') or run('docker', 'context', 'inspect', '--format',
                                                  '{{(index .Endpoints "docker").Host}}', capture=True).strip()
    if not endpoint.startswith('unix://') or Path(endpoint[7:]).resolve() != Path('/var/run/docker.sock').resolve():
        raise RuntimeError('Use the base environment Docker socket /var/run/docker.sock')
    info = json.loads(run('docker', 'info', '--format', '{{json .}}', capture=True))
    if info['CgroupDriver'] != 'systemd' or str(info['CgroupVersion']) != '2':
        raise RuntimeError('Docker requires the systemd driver and cgroup v2')
    if any(line.startswith('0::/laxcode.slice') for line in Path('/proc/self/cgroup').read_text().splitlines()):
        raise RuntimeError('Deploy and manager must run outside laxcode.slice')
    for image in (BASE_IMAGE, f'ghcr.io/nvidia/openshell/supervisor:{VERSION}', f'ghcr.io/nvidia/openshell/sandbox:{VERSION}'):
        details = json.loads(run('docker', 'image', 'inspect', image, capture=True))[0]
        if details['Os'] != 'linux' or details['Architecture'] != 'amd64':
            raise RuntimeError(f'Preinstalled image architecture mismatch: {image}')
    # A slice is a cgroup, not a manager service. Keep its identity stable between deployments.
    run('systemctl', 'start', 'laxcode.slice')
    run('mountpoint', '-q', '/sys/fs/bpf')
    verify_docker_parent(BASE_IMAGE)
    return manifest


def start_manager(release, state, env):
    from configure import host_address, manager_configuration, require_guard
    config = state / 'manager.json'
    if not config.exists():
        config.write_text(json.dumps(manager_configuration(), indent=2) + '\n')
        config.chmod(0o600)
    start(state, 'manager', [release / 'bin/laxcode-manager', '-config', config], env)
    host = host_address()
    for _ in range(120):
        if not managed_pid(state, 'manager'):
            break
        try:
            require_guard(host)
            return
        except (OSError, ValueError):
            time.sleep(0.5)
    raise RuntimeError('Manager is not ready; inspect state/manager.log (DB and BPF pins retained)')


def remove_sandboxes(env):
    for sandbox in sandboxes(env):
        run('openshell', 'sandbox', 'delete', sandbox['name'], env=env)
    for _ in range(240):
        if not sandboxes(env):
            return
        time.sleep(0.5)
    raise RuntimeError('OpenShell sandbox deletion did not finish')


def build_image(release):
    # Include labelled dangling images left by an interrupted earlier deployment.
    old = set(run('docker', 'image', 'ls', '--no-trunc', '-q', 'laxcode-cloud', capture=True).split())
    old.update(run('docker', 'image', 'ls', '--no-trunc', '-q', '--filter', 'label=' + IMAGE_LABEL, capture=True).split())
    run('docker', 'build', '--pull=false', '--network=none', '-t', IMAGE, release / 'image')
    run('docker', 'run', '--rm', '--network=none', '--entrypoint', '/bin/sh', IMAGE, '-c',
        'test -x /usr/local/bin/laxcode-cloud')
    current = run('docker', 'image', 'inspect', '--format', '{{.Id}}', IMAGE, capture=True).strip()
    # containerd image stores may already discard an unreferenced old manifest on retag.
    remaining = set(run('docker', 'image', 'ls', '--no-trunc', '-q', capture=True).split())
    for image in sorted((old & remaining) - {current}):
        run('docker', 'image', 'rm', image)
    shutil.rmtree(release / 'image')


def restart(base, state, env):
    release = base / 'laxcode'
    manifest = preflight(release)
    print('Stopping proxy and replacing application sandboxes', flush=True)
    stop(state, 'proxy')
    if managed_pid(state, 'gateway'):
        remove_sandboxes(env)
    stop(state, 'manager')
    clean_release(release, manifest)
    # On a cold boot, install the guard before starting even the gateway.
    start_manager(release, state, env)
    ensure_gateway(state, env)
    remove_sandboxes(env)
    run(sys.executable, release / 'configure.py', 'generate', release)
    build_image(release)
    run(sys.executable, release / 'configure.py', 'register', release, env=env)
    try:
        start_proxy(base, state, env)
    except BaseException:
        stop(state, 'proxy')
        raise
    print('LaxCode ready: http://127.0.0.1:5173 (manager/proxy started; gateway retained)', flush=True)


def stop_application(state, env):
    stop(state, 'proxy')
    if managed_pid(state, 'gateway'):
        remove_sandboxes(env)
    else:
        containers = run('docker', 'ps', '-q', '--filter',
                         'label=openshell.ai/sandbox-namespace=laxcode', capture=True).strip()
        if containers:
            raise RuntimeError('Gateway is stopped but sandbox containers remain; run restart to recover first')
    stop(state, 'manager')
    stop(state, 'gateway')
    print('Services stopped; user data, quota ledger and BPF pins retained')


def main():
    os.umask(0o077)
    base = Path(__file__).resolve().parent.parent
    command = sys.argv[1] if len(sys.argv) == 2 else 'restart' if len(sys.argv) == 1 else ''
    if command not in ('restart', 'stop', 'status'):
        raise RuntimeError('Usage: python3 deploy.py [restart|stop|status]')
    state = base / 'state'
    state.mkdir(mode=0o700, exist_ok=True)
    with (state / 'deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        env = environment(state)
        if command == 'restart':
            restart(base, state, env)
        elif command == 'stop':
            stop_application(state, env)
        else:
            for name in ('manager', 'gateway', 'proxy'):
                print(f'{name}: {managed_pid(state, name) or "stopped"}')


if __name__ == '__main__':
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        # No settings parser fragments or credential-bearing command output.
        print(f'Deployment failed ({type(error).__name__}): ' +
              (str(error) if isinstance(error, RuntimeError) else 'inspect state logs and prerequisites'), file=sys.stderr)
        sys.exit(1)
