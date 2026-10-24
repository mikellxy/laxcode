import fcntl
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

import reset_quota as module


class ResetQuotaTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.state = Path(temp.name).resolve()
        self.pins = self.state / 'pins'
        self.pins.mkdir()
        for name in module.LINKS + module.MAPS:
            (self.pins / name).touch()
        self.parent = Path('/sys/fs/cgroup/laxcode-reset-test.slice')
        self.config = dict(state_dir=str(self.state), pin_path=str(self.pins), cgroup_parent=str(self.parent))
        db = sqlite3.connect(self.state / 'quota.db')
        self.addCleanup(db.close)
        self.db = db
        db.executescript("""
PRAGMA user_version=2;
CREATE TABLE metadata(id INTEGER PRIMARY KEY, db_id TEXT, boot_id TEXT);
INSERT INTO metadata VALUES(1,'same-db','same-boot');
CREATE TABLE epochs(status TEXT, granted INTEGER, rx INTEGER, tx INTEGER);
INSERT INTO epochs VALUES('active',300,100,50);
""")
        exists, read_text = Path.exists, Path.read_text
        self.populated = '0'
        for mock in (
            patch.object(module, 'run', side_effect=lambda *args, **kw: '63677270' if args[-1] == self.parent else 'cafe4a11'),
            patch.object(Path, 'exists', lambda p: p == self.parent or exists(p)),
            patch.object(Path, 'read_text', lambda p, *a, **kw: 'populated ' + self.populated + '\nfrozen 0\n'
                         if p == self.parent / 'cgroup.events' else read_text(p, *a, **kw)),
        ):
            mock.start()
            self.addCleanup(mock.stop)

    def marker(self):
        return self.db.execute('SELECT boot_id FROM metadata').fetchone()[0]

    def test_reset_preserves_ledger_and_is_repeatable(self):
        module.reset_kernel(self.config)
        module.reset_kernel(self.config)
        self.assertEqual(list(self.pins.iterdir()), [])
        self.assertEqual(self.db.execute('SELECT db_id,boot_id FROM metadata').fetchone(), ('same-db', ''))
        self.assertEqual(self.db.execute('SELECT * FROM epochs').fetchone(), ('active', 300, 100, 50))

    def test_populated_parent_and_running_manager_are_rejected(self):
        self.populated = '1'
        with self.assertRaisesRegex(RuntimeError, 'still has processes'):
            module.reset_kernel(self.config)
        self.populated = '0'
        with (self.state / 'manager.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(RuntimeError, 'Manager is still running'):
                module.reset_kernel(self.config)
        self.assertEqual(self.marker(), 'same-boot')
        self.assertTrue((self.pins / 'ingress_link').exists())

    def test_unknown_files_and_wrong_filesystem_are_rejected(self):
        extra = self.pins / 'unrelated'
        extra.touch()
        with self.assertRaisesRegex(RuntimeError, 'Unexpected entries'):
            module.reset_kernel(self.config)
        extra.unlink()
        with patch.object(module, 'run', return_value='63677270'):
            with self.assertRaisesRegex(RuntimeError, 'bpffs'):
                module.reset_kernel(self.config)
        self.assertEqual(self.marker(), 'same-boot')

    def test_interruption_leaves_durable_recovery_marker_and_can_retry(self):
        unlink = Path.unlink

        def fail_after_first_link(path, *args, **kwargs):
            if path.name == 'egress_link':
                self.assertEqual(self.marker(), '')
                raise OSError('simulated interruption')
            return unlink(path, *args, **kwargs)

        with patch.object(Path, 'unlink', fail_after_first_link):
            with self.assertRaisesRegex(OSError, 'interruption'):
                module.reset_kernel(self.config)
        self.assertFalse((self.pins / 'ingress_link').exists())
        self.assertTrue((self.pins / 'egress_link').exists())
        module.reset_kernel(self.config)
        self.assertEqual(list(self.pins.iterdir()), [])

    def test_database_failure_never_removes_pins(self):
        self.db.executescript("""CREATE TRIGGER fail_reset BEFORE UPDATE ON metadata
BEGIN SELECT RAISE(ABORT, 'simulated write failure'); END;""")
        with self.assertRaises(sqlite3.IntegrityError):
            module.reset_kernel(self.config)
        self.assertEqual(self.marker(), 'same-boot')
        self.assertEqual(len(list(self.pins.iterdir())), len(module.LINKS + module.MAPS))


if __name__ == '__main__':
    unittest.main()
