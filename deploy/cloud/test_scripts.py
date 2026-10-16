"""Linux lifecycle smoke test; pass the directory containing the two Linux binaries.

Docker checks are stubbed: this test never starts agent containers or calls an LLM.
Run as a non-root user in an isolated Linux environment with Python 3.
"""
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from urllib.request import urlopen

SOURCE = Path(__file__).resolve().parent
BINARIES = Path(sys.argv.pop(1)).resolve()


class LifecycleTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="laxcode lifecycle ")
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        self.bundle = root / "bundle with spaces"
        self.bundle.mkdir()
        for name in ("laxcode", "laxcode-web"):
            shutil.copy2(BINARIES / name, self.bundle / name)
        for name in ("start.sh", "stop.sh"):
            shutil.copy2(SOURCE / name, self.bundle / name)
        (self.bundle / "defaults").mkdir()
        (self.bundle / "defaults/settings.json").write_text('{"model":"","provider_list":[]}')
        home = root / "home"
        home.mkdir()
        commands = root / "bin"
        commands.mkdir()
        docker = commands / "docker"
        docker.write_text('#!/bin/sh\n[ "$1" != image ] || echo linux/amd64\n')
        docker.chmod(0o755)
        # Also allow native ARM64 test binaries; the shipped bundle remains AMD64.
        uname = commands / "uname"
        uname.write_text('#!/bin/sh\necho x86_64\n')
        uname.chmod(0o755)
        # The production image omits ss; reproduce its output from real Linux sockets.
        ss = commands / "ss"
        ss.write_text('''#!/usr/bin/python3
import glob, os, sys
port = int(sys.argv[-1].rsplit(":", 1)[1])
inodes = set()
for table in ("/proc/net/tcp", "/proc/net/tcp6"):
    for line in open(table).readlines()[1:]:
        fields = line.split()
        if fields[3] == "0A" and int(fields[1].rsplit(":", 1)[1], 16) == port:
            inodes.add(fields[9])
for inode in inodes:
    owners = []
    if "-ltnp" in sys.argv:
        for fd in glob.glob("/proc/[0-9]*/fd/*"):
            try:
                if os.readlink(fd) == "socket:[" + inode + "]":
                    owners.append("pid=" + fd.split("/")[2])
            except OSError:
                pass
    print("LISTEN 0 4096 *:" + str(port) + " *:* " + " ".join(owners))
''')
        ss.chmod(0o755)
        self.env = dict(os.environ, HOME=str(home), PATH=f"{commands}:/usr/bin:/bin")
        self.env.pop("LAXCODE_CLOUD_PORT", None)
        self.addCleanup(lambda: self.run_script("stop.sh", check=False))

    def run_script(self, name, *args, check=True, env=None):
        result = subprocess.run([str(self.bundle / name), *args], env=env or self.env,
                                text=True, capture_output=True, timeout=45)
        if check:
            log = self.bundle / "server.log"
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr +
                             (log.read_text() if log.exists() else ""))
        return result

    def alive(self, pid):
        try:
            os.kill(pid, 0)
            return Path(f"/proc/{pid}/stat").read_text().split(") ", 1)[1][0] != "Z"
        except (ProcessLookupError, FileNotFoundError):
            return False

    def test_background_duplicate_stop_and_custom_port(self):
        for port in (5173, 5183):
            env = dict(self.env, LAXCODE_CLOUD_PORT=str(port))
            self.run_script("start.sh", env=env)
            state = (self.bundle / "server.state").read_text()
            supervisor, backend, web, saved_port = map(int, state.split())
            self.assertEqual(saved_port, port)
            self.assertTrue(all(self.alive(pid) for pid in (supervisor, backend, web)))
            # start.sh returned and its pipes closed; the detached children still serve HTTP.
            with urlopen(f"http://127.0.0.1:{port}/healthz", timeout=5) as response:
                self.assertEqual(response.status, 200)
            self.run_script("start.sh", env=env)
            self.assertEqual((self.bundle / "server.state").read_text(), state)
            self.run_script("stop.sh")
            self.assertFalse(any(self.alive(pid) for pid in (supervisor, backend, web)))
            self.assertFalse((self.bundle / "server.pid").exists())
            self.assertFalse((self.bundle / "server.state").exists())
            self.run_script("stop.sh")

    def test_unrelated_listener_and_stale_pid_are_not_killed(self):
        listener = socket.socket()
        self.addCleanup(listener.close)
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind(("127.0.0.1", 5173))
        listener.listen()
        (self.bundle / "server.pid").write_text(str(os.getpid()))
        result = self.run_script("stop.sh", check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("another process", result.stderr)
        self.assertNotEqual(self.run_script("start.sh", check=False).returncode, 0)
        self.assertEqual(listener.getsockname()[1], 5173)

    def test_failed_backend_cleans_pid_and_rejects_invalid_port(self):
        self.assertNotEqual(self.run_script("start.sh", "-not-a-real-flag", check=False).returncode, 0)
        self.assertFalse((self.bundle / "server.pid").exists())
        self.assertFalse((self.bundle / "server.state").exists())
        for port in ("abc", "0", "65536"):
            result = self.run_script("start.sh", check=False,
                                     env=dict(self.env, LAXCODE_CLOUD_PORT=port))
            self.assertNotEqual(result.returncode, 0)

    def test_stop_without_saved_state(self):
        self.run_script("start.sh")
        supervisor, backend, web, _ = map(int, (self.bundle / "server.state").read_text().split())
        (self.bundle / "server.pid").unlink()
        (self.bundle / "server.state").unlink()
        self.run_script("stop.sh")
        deadline = time.monotonic() + 5
        while self.alive(supervisor) and time.monotonic() < deadline:
            time.sleep(0.1)
        self.assertFalse(any(self.alive(pid) for pid in (supervisor, backend, web)))


if __name__ == "__main__":
    unittest.main()
