#!/usr/bin/env python3
"""Explicit one-time migration for a dedicated, empty Linux Docker host."""
import argparse
import ipaddress
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile


def run(*args):
    return subprocess.check_output([str(arg) for arg in args], text=True).strip()


def atomic(path, data, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as out:
        temporary = Path(out.name)
        out.write(data)
    try:
        temporary.chmod(mode)
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def configuration():
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as route:
        route.connect(('8.8.8.8', 80))
        store = route.getsockname()[0]
    address = ipaddress.ip_address(store)
    if not address.is_private or address.is_loopback:
        raise RuntimeError('Private host storage address required')
    peers = [{'address': '127.0.0.1', 'port': 18080, 'protocol': 'tcp'},
             {'address': store, 'port': 5173, 'protocol': 'tcp'}]
    dns = set()
    for line in Path('/etc/resolv.conf').read_text().splitlines():
        parts = line.split()
        if len(parts) >= 2 and parts[0] == 'nameserver':
            ip = ipaddress.ip_address(parts[1])
            if ip.is_link_local or ip.is_unspecified or ip.is_multicast:
                raise RuntimeError('Unsupported DNS control peer')
            dns.add(str(ip))
    if not dns:
        raise RuntimeError('No DNS resolvers found')
    for ip in sorted(dns):
        for protocol in ('tcp', 'udp'):
            peers.append({'address': ip, 'port': 53, 'protocol': protocol})
    return {'sandbox_namespace': 'laxcode', 'workplaces_root': str(Path.home() / 'laxcode'), 'control_peers': peers,
            'daily_limit_bytes': 5000000000, 'grant_chunk_bytes': 16777216,
            'refill_low_watermark_bytes': 4194304}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--migrate-docker', action='store_true',
                        help='Install the guard and restart dedicated Docker with laxcode.slice as default parent')
    args = parser.parse_args()
    if not args.migrate_docker:
        parser.error('One-time migration requires --migrate-docker; see laxcode-manager/README.md')
    if os.geteuid() != 0 or not Path('/sys/fs/cgroup/cgroup.controllers').exists():
        raise RuntimeError('Root and cgroup v2 are required')
    if not args.binary.is_file():
        raise RuntimeError('Manager binary missing')
    if Path('/etc/laxcode-manager/config.json').exists():
        raise RuntimeError('Already installed; upgrade only the binary, retain config/DB/pins, then restart manager')
    info = json.loads(run('docker', 'info', '--format', '{{json .}}'))
    if info['CgroupDriver'] != 'systemd' or str(info['CgroupVersion']) != '2':
        raise RuntimeError('Docker must use systemd and cgroup v2')
    if run('docker', 'ps', '-aq'):
        raise RuntimeError('Remove all Docker containers first; migration is only for a dedicated host')
    daemon = Path('/etc/docker/daemon.json')
    original = daemon.read_bytes() if daemon.exists() else b'{}\n'
    settings = json.loads(original)
    if settings.get('cgroup-parent', 'laxcode.slice') != 'laxcode.slice':
        raise RuntimeError('Existing Docker cgroup-parent differs; resolve manually')
    settings['cgroup-parent'] = 'laxcode.slice'
    cfg = configuration()
    # Validate daemon config before touching the host.
    with tempfile.NamedTemporaryFile(mode='w') as f:
        json.dump(settings, f)
        f.flush()
        run('dockerd', '--validate', '--config-file', f.name)
    unit_dir = Path('/etc/systemd/system')
    here = Path(__file__).resolve().parent
    for name in ('laxcode.slice', 'laxcode-manager.service'):
        if (unit_dir / name).exists():
            raise RuntimeError(f'Existing unit requires manual review: {name}')
    dropin = unit_dir / 'docker.service.d/laxcode-quota.conf'
    if dropin.exists():
        raise RuntimeError('Existing Docker quota drop-in')
    # Existing bpffs is retained. On a new host create its normal mount unit.
    if subprocess.run(['mountpoint', '-q', '/sys/fs/bpf']).returncode:
        mount_unit = unit_dir / 'sys-fs-bpf.mount'
        if mount_unit.exists():
            raise RuntimeError('Existing bpffs unit must be started first')
        atomic(mount_unit, b'[Unit]\nDescription=BPF filesystem\nBefore=laxcode-manager.service\n'
               b'[Mount]\nWhat=bpf\nWhere=/sys/fs/bpf\nType=bpf\n'
               b'[Install]\nWantedBy=multi-user.target\n', 0o644)
        run('systemctl', 'daemon-reload')
        run('systemctl', 'enable', '--now', 'sys-fs-bpf.mount')
    atomic(Path('/usr/local/bin/laxcode-manager'), args.binary.read_bytes(), 0o755)
    atomic(Path('/etc/laxcode-manager/config.json'), (json.dumps(cfg, indent=2) + '\n').encode())
    for name in ('laxcode.slice', 'laxcode-manager.service'):
        atomic(unit_dir / name, (here / name).read_bytes(), 0o644)
    run('systemctl', 'daemon-reload')
    # Type=notify completes after guard installation, before Docker discovery.
    run('systemctl', 'enable', '--now', 'laxcode-manager.service')
    backup = Path('/etc/docker/daemon.json.before-laxcode')
    if not backup.exists():
        atomic(backup, original)
    atomic(daemon, (json.dumps(settings, indent=2) + '\n').encode())
    atomic(dropin, (here / 'docker-quota.conf').read_bytes(), 0o644)
    run('systemctl', 'daemon-reload')
    # A restart failure deliberately leaves the guard installed; do not reopen networking.
    run('systemctl', 'restart', 'docker.service')
    print('Installed. Verify /readyz on /run/laxcode-manager/control.sock before deploying the application.')


if __name__ == '__main__':
    main()
