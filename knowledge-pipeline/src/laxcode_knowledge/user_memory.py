"""Atomic user memory ingestion; knowledge document imports remain independent."""
import json
import math
import os
import sqlite3
import sys
import uuid

import sqlite_vec
from laxcode_knowledge.splitter import ByTitleSplitter

MAX_INPUT = 1024 * 1024


class Conflict(ValueError):
    pass


def validate(data):
    if not isinstance(data, dict):
        raise ValueError("input must be an object")
    for key in ("user_id", "session_id", "source_key", "content"):
        if not isinstance(data.get(key), str) or not data[key].strip():
            raise ValueError(f"{key} is required")
    if str(uuid.UUID(data["user_id"])) != data["user_id"]:
        raise ValueError("user_id must be a canonical UUID")
    start, end = data.get("start_turn"), data.get("end_turn")
    if type(start) is not int or type(end) is not int or start < 1 or end != start + 2 or end % 3:
        raise ValueError("invalid three-turn window")
    if data["source_key"] != f"{data['session_id']}:react:{end}":
        raise ValueError("source_key does not match session/window")
    if len(json.dumps(data).encode()) > MAX_INPUT:
        raise ValueError("input too large")


class UserMemoryWriter:
    def __init__(self, path, model, dimensions):
        if not os.path.isabs(path) or not model:
            raise ValueError("absolute db path and embedding model are required")
        if type(dimensions) is not int or not 1 <= dimensions <= 8192:
            raise ValueError("dimensions must be between 1 and 8192")
        self.dimensions = dimensions
        self.conn = sqlite3.connect(path, timeout=5)
        try:
            self.conn.enable_load_extension(True)
            sqlite_vec.load(self.conn)
            self.conn.enable_load_extension(False)
            self.conn.execute("PRAGMA foreign_keys=ON")
            self.conn.execute("PRAGMA journal_mode=WAL")
            with self.conn:
                self.conn.execute("CREATE TABLE IF NOT EXISTS user_memory_config (id INTEGER PRIMARY KEY CHECK(id=1), model TEXT NOT NULL, dimensions INTEGER NOT NULL)")
                self.conn.execute("INSERT OR IGNORE INTO user_memory_config VALUES (1, ?, ?)", (model, dimensions))
                if self.conn.execute("SELECT model, dimensions FROM user_memory_config WHERE id=1").fetchone() != (model, dimensions):
                    raise ValueError("embedding model/dimension mismatch")
                self.conn.execute("""CREATE TABLE IF NOT EXISTS user_memory (
                    id TEXT PRIMARY KEY, user_id TEXT NOT NULL, session_id TEXT NOT NULL,
                    source_key TEXT NOT NULL, start_turn INTEGER NOT NULL, end_turn INTEGER NOT NULL,
                    content TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now')),
                    UNIQUE(user_id, source_key), UNIQUE(id, user_id))""")
                self.conn.execute("""CREATE TABLE IF NOT EXISTS user_memory_chunk (
                    chunk_id TEXT PRIMARY KEY, user_id TEXT NOT NULL, memory_id TEXT NOT NULL,
                    chunk_seq INTEGER NOT NULL, content TEXT NOT NULL, title TEXT NOT NULL,
                    FOREIGN KEY(memory_id,user_id) REFERENCES user_memory(id,user_id),
                    UNIQUE(memory_id,chunk_seq))""")
                self.conn.execute("CREATE INDEX IF NOT EXISTS idx_user_memory_source ON user_memory(source_key)")
                self.conn.execute("CREATE INDEX IF NOT EXISTS idx_user_memory_chunk_user ON user_memory_chunk(user_id, memory_id)")
                self.conn.execute(f"CREATE VIRTUAL TABLE IF NOT EXISTS user_memory_vectors USING vec0(chunk_id TEXT PRIMARY KEY, user_id TEXT, embedding FLOAT[{dimensions}])")
            # Exercise the actual vec schema, including dimensions and KNN metadata filtering.
            self.conn.execute("SELECT chunk_id FROM user_memory_vectors WHERE embedding MATCH ? AND k=3 AND user_id=?", (sqlite_vec.serialize_float32([0.] * dimensions), "schema-check")).fetchall()
        except BaseException:
            self.conn.close()
            raise

    def close(self):
        self.conn.close()

    def existing(self, data):
        owner = self.conn.execute("SELECT user_id FROM user_memory WHERE source_key=? AND user_id<>? LIMIT 1", (data["source_key"], data["user_id"])).fetchone()
        if owner is not None:
            raise Conflict("source key already belongs to another user")
        row = self.conn.execute("SELECT id, session_id, start_turn, end_turn, content FROM user_memory WHERE user_id=? AND source_key=?", (data["user_id"], data["source_key"])).fetchone()
        if row is None:
            return None
        if row[1:] != (data["session_id"], data["start_turn"], data["end_turn"], data["content"]):
            raise Conflict("source key already contains different content or ownership")
        count = self.conn.execute("SELECT count(*) FROM user_memory_chunk WHERE memory_id=?", (row[0],)).fetchone()[0]
        return dict(status="succeeded", memory_id=row[0], chunk_count=count, already_processed=True)

    def ingest(self, data, embed):
        validate(data)
        result = self.existing(data)
        if result:
            return result
        splitter = ByTitleSplitter(chunk_size=600, overlap_size=0)
        remaining = data["content"].encode()
        chunks = []
        while remaining.strip():
            batch, consumed, _ = splitter.split_documents(remaining.decode(), final=True)
            if consumed <= 0:
                raise ValueError("splitter made no progress")
            chunks.extend(batch)
            remaining = remaining[consumed:]
        vectors = embed([c.page_content for c in chunks])
        if len(vectors) != len(chunks) or not chunks:
            raise ValueError("embedding count mismatch")
        for v in vectors:
            if len(v) != self.dimensions or any(not math.isfinite(x) or abs(x) > 3.402823466e38 for x in v):
                raise ValueError("invalid embedding dimensions or values")
        blobs = [sqlite_vec.serialize_float32(v) for v in vectors]
        # No network work inside the short write transaction.
        with self.conn:
            self.conn.execute("BEGIN IMMEDIATE")
            result = self.existing(data)
            if result:
                return result
            mid = str(uuid.uuid4())
            self.conn.execute("INSERT INTO user_memory(id,user_id,session_id,source_key,start_turn,end_turn,content) VALUES (?,?,?,?,?,?,?)", (mid, data["user_id"], data["session_id"], data["source_key"], data["start_turn"], data["end_turn"], data["content"]))
            for seq, (chunk, blob) in enumerate(zip(chunks, blobs)):
                cid = str(uuid.uuid4())
                self.conn.execute("INSERT INTO user_memory_chunk VALUES (?,?,?,?,?,?)", (cid, data["user_id"], mid, seq, chunk.page_content, chunk.title))
                self.conn.execute("INSERT INTO user_memory_vectors(chunk_id,user_id,embedding) VALUES (?,?,?)", (cid, data["user_id"], blob))
        return dict(status="succeeded", memory_id=mid, chunk_count=len(chunks), already_processed=False)


def run(args):
    writer = None
    try:
        model = os.environ.get("OPENAI_EMBEDDING_MODEL_NAME", "")
        writer = UserMemoryWriter(args.db, model, args.dimensions)
        if args.init_schema:
            result = dict(status="initialized")
        else:
            raw = sys.stdin.buffer.read(MAX_INPUT + 1)
            if len(raw) > MAX_INPUT:
                raise ValueError("input too large")
            data = json.loads(raw)
            from laxcode_knowledge.models import get_embedding_model
            # Lazy creation allows idempotent retries without a network call.
            result = writer.ingest(data, lambda texts: get_embedding_model().embed_documents(texts))
        print(json.dumps(result))
    except Exception as exc:
        permanent = isinstance(exc, (ValueError, TypeError, OverflowError, sqlite3.IntegrityError))
        print(json.dumps(dict(error="invalid_input" if permanent else "transient", message=str(exc))), file=sys.stderr)
        raise SystemExit(2 if permanent else 1)
    finally:
        if writer is not None:
            writer.close()
