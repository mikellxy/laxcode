import sqlite3
import tempfile
import unittest
import uuid
from pathlib import Path
from laxcode_knowledge.user_memory import UserMemoryWriter, Conflict

class UserMemoryTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.path = str(Path(self.directory.name) / "memory.sqlite")
        self.writer = UserMemoryWriter(self.path, "test-model", 1024)
        self.data = dict(user_id=str(uuid.uuid4()), session_id="test", source_key="test:react:3", start_turn=1, end_turn=3, content="# 偏好\n简洁中文。\n# 编程\n使用 Go。")
    def tearDown(self):
        self.writer.close()
        self.directory.cleanup()
    def embed(self, texts):
        return [[1.] + [0.] * 1023 for _ in texts]
    def test_idempotency_conflict_and_model(self):
        first = self.writer.ingest(self.data, self.embed)
        second = self.writer.ingest(self.data, lambda _: self.fail("duplicate embedding"))
        self.assertEqual(first["memory_id"], second["memory_id"])
        self.assertTrue(second["already_processed"])
        self.assertEqual(first["chunk_count"], 2)
        with self.assertRaises(Conflict):
            self.writer.ingest(dict(self.data, content="different"), self.embed)
        with self.assertRaises(ValueError):
            UserMemoryWriter(self.path, "different-model", 1024)
        with self.assertRaises(ValueError):
            UserMemoryWriter(self.path, "test-model", 1536)

    def test_configurable_dimensions_drive_vector_schema(self):
        path = str(Path(self.directory.name) / "small.sqlite")
        writer = UserMemoryWriter(path, "small-model", 7)
        try:
            data = dict(self.data, source_key="small:react:3", session_id="small")
            result = writer.ingest(data, lambda texts: [[1.0] * 7 for _ in texts])
            self.assertGreater(result["chunk_count"], 0)
            self.assertEqual(
                writer.conn.execute("SELECT dimensions FROM user_memory_config WHERE id=1").fetchone(),
                (7,),
            )
        finally:
            writer.close()
    def test_ownership_conflict(self):
        self.writer.ingest(self.data, self.embed)
        with self.assertRaises(Conflict):
            self.writer.ingest(dict(self.data, user_id=str(uuid.uuid4())), self.embed)

    def test_knowledge_writer_unchanged(self):
        from laxcode_knowledge.store import VecWriter
        from laxcode_knowledge.splitter import ByTitleSplitter
        writer = VecWriter(self.path)
        try:
            doc = writer.prepare_document("/test/document.md")
            chunks, _, _ = ByTitleSplitter().split_documents("# Test\ncontent", final=True)
            writer.save_chunks(doc, chunks, self.embed(chunks))
            self.assertTrue(writer.has_document("/test/document.md"))
            self.assertEqual(writer.conn.execute("SELECT count(*) FROM chunks").fetchone()[0], 1)
        finally:
            writer.close()

    def test_cli_protocol(self):
        import json
        import os
        import subprocess
        import sys
        self.writer.ingest(self.data, self.embed)
        env = dict(os.environ, OPENAI_EMBEDDING_MODEL_NAME="test-model", PYTHONDONTWRITEBYTECODE="1")
        command = [sys.executable, "-c", "from laxcode_knowledge.cmd import main; main()", "--target", "user_memory", "--db", self.path, "--dimensions", "1024"]
        initialized = subprocess.run(command + ["--init-schema"], capture_output=True, text=True, env=env)
        self.assertEqual(initialized.returncode, 0, initialized.stderr)
        self.assertEqual(json.loads(initialized.stdout)["status"], "initialized")
        result = subprocess.run(command + ["--stdin-json"], input=json.dumps(self.data), capture_output=True, text=True, env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads(result.stdout)["already_processed"])
        result = subprocess.run(command + ["--stdin-json"], input="{}", capture_output=True, text=True, env=env)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(json.loads(result.stderr)["error"], "invalid_input")

    def test_invalid_vectors_write_nothing(self):
        for embed in (lambda _: [], lambda texts: [[0.] for _ in texts], lambda texts: [[float("nan")] * 1024 for _ in texts]):
            with self.assertRaises(ValueError):
                self.writer.ingest(self.data, embed)
        self.assertEqual(self.writer.conn.execute("SELECT count(*) FROM user_memory").fetchone()[0], 0)
    def test_transaction_rolls_back_partial_chunks(self):
        self.writer.conn.execute("CREATE TRIGGER fail_chunk BEFORE INSERT ON user_memory_chunk WHEN NEW.chunk_seq=1 BEGIN SELECT RAISE(ABORT, 'injected'); END")
        with self.assertRaises(sqlite3.IntegrityError):
            self.writer.ingest(self.data, self.embed)
        for table in ("user_memory", "user_memory_chunk", "user_memory_vectors"):
            self.assertEqual(self.writer.conn.execute("SELECT count(*) FROM " + table).fetchone()[0], 0)
    def test_users_filtered_inside_knn(self):
        import sqlite_vec
        self.writer.ingest(self.data, self.embed)
        other = dict(self.data, user_id=str(uuid.uuid4()), session_id="other", source_key="other:react:3")
        self.writer.ingest(other, self.embed)
        rows = self.writer.conn.execute("SELECT user_id FROM user_memory_vectors WHERE embedding MATCH ? AND k=3 AND user_id=?", (sqlite_vec.serialize_float32([1.] + [0.] * 1023), self.data["user_id"])).fetchall()
        self.assertEqual(rows, [(self.data["user_id"],)] * 2)

if __name__ == "__main__":
    unittest.main()
