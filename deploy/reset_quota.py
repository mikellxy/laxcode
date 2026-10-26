#!/usr/bin/env python3
"""Stop the deployment and remove its quota BPF pins without refunding grants."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import platform
import sqlite3
import sys

from deploy import environment, run, stop_application


LINKS = ('ingress_link', 'egress_link')
MAPS = ('metadata', 'cgroup_owner', 'user_bucket', 'bucket_meta', 'grants',
        'usage', 'control_peers', 'address_class', 'events', 'diagnostics')


def reset_kernel(config):
    state = Path(config.get('state_dir', '/var/lib/laxcode-manager'))
    pins = Path(config.get('pin_path', '/sys/fs/bpf/laxcode-manager'))
    parent = Path(config.get('cgroup_parent', '/sys/fs/cgroup/laxcode.slice'))
    for path in (state, pins, parent):
        if not path.is_absolute() or path.resolve() != path or path == Path('/'):
            raise RuntimeError(f'Refusing non-canonical state path: {path}')
    if pins == Path('/sys/fs/bpf') or parent.parent != Path('/sys/fs/cgroup') or parent.name == 'system.slice':
        raise RuntimeError('Dedicated pin directory and cgroup parent required')
    # The manager uses the same lock; keep it until every pin has been removed.
    with (state / 'manager.lock').open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError('Manager is still running; stop it before resetting quota') from None
        if parent.exists():
            if run('stat', '-f', '-c', '%t', parent, capture=True).strip() != '63677270':
                raise RuntimeError('Parent must be on cgroup v2')
            events = dict(line.split() for line in (parent / 'cgroup.events').read_text().splitlines())
            if events.get('populated') != '0':
                raise RuntimeError('Parent cgroup still has processes; stop its containers before resetting quota')
        entries = list(pins.iterdir()) if pins.exists() else []
        if pins.exists() and run('stat', '-f', '-c', '%t', pins, capture=True).strip() != 'cafe4a11':
            raise RuntimeError('Pin directory must be on bpffs')
        if any(p.name not in LINKS + MAPS or p.is_symlink() or not p.is_file() for p in entries):
            raise RuntimeError('Unexpected entries in pin directory; inspect it manually')
        # Validate the retained ledger before unlinking; empty maps trigger recovery on startup.
        db = sqlite3.connect((state / 'quota.db').as_uri() + '?mode=ro', uri=True)
        try:
            if db.execute('PRAGMA user_version').fetchone()[0] != 3:
                raise RuntimeError('Unsupported quota ledger schema')
            if not db.execute('SELECT db_id FROM metadata WHERE id=1').fetchone():
                raise RuntimeError('Quota ledger metadata missing')
        finally:
            db.close()
        # With manager FDs closed, removing link pins releases their attachments.
        for name in LINKS + MAPS:
            (pins / name).unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.parse_args()
    if os.geteuid() != 0 or platform.system() != 'Linux':
        raise RuntimeError('Quota reset requires root on Linux')
    os.umask(0o077)
    state = Path(__file__).resolve().parent.parent / 'state'
    config = json.loads((state / 'manager.json').read_text())
    with (state / 'deploy.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        stop_application(state, environment(state))
        reset_kernel(config)
    print('Quota BPF pins removed; ledger and grants retained. Run python3 deploy.py to rebuild the guard.')


if __name__ == '__main__':
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print(f'Quota reset failed: {error}', file=sys.stderr)
        sys.exit(1)
