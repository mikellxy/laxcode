import os
import sqlite3
import uuid

import sqlite_vec


class VecWriter(object):
    """sqlite-vec 持久化

    表设计:
        documents     文档表,含 path(文档绝对路径,唯一)
        chunks        chunk表,documents 1:N chunks,chunk_id 为 uuid,
                      chunk_seq 为文档内编号,含 title(所属分节标题)
        chunk_vectors 向量表(vec0 虚表),chunks 1:1 chunk_vectors,
                      只存 chunk_id 和向量值,召回时回 chunks 表反查内容

    目录存在但 db 文件不存在时自动创建库文件并 migrate 建表;
    目录不存在则抛 ValueError
    """

    def __init__(self, db_path):
        self.db_path = db_path
        parent = os.path.dirname(db_path)
        if parent and not os.path.isdir(parent):
            raise ValueError(f"数据库目录不存在: {parent}")
        self.created = not os.path.exists(db_path)
        self.conn = sqlite3.connect(db_path)
        self.conn.enable_load_extension(True)
        sqlite_vec.load(self.conn)
        self.conn.enable_load_extension(False)
        self._migrate()

    def _migrate(self):
        """建表迁移,幂等"""
        self.conn.execute(
            """
            CREATE TABLE IF NOT EXISTS documents (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                path TEXT NOT NULL UNIQUE,
                created_at TEXT NOT NULL DEFAULT (datetime('now'))
            )
            """
        )
        self.conn.execute(
            """
            CREATE TABLE IF NOT EXISTS chunks (
                chunk_id TEXT PRIMARY KEY,
                document_id INTEGER NOT NULL REFERENCES documents(id),
                chunk_seq INTEGER NOT NULL,
                content TEXT NOT NULL,
                title TEXT NOT NULL DEFAULT ''
            )
            """
        )
        columns = {row[1] for row in self.conn.execute("PRAGMA table_info(chunks)")}
        if "chunk_id" not in columns:
            # 老库(id 自增整数主键 + chunk_index):重建为新 schema
            self._rebuild_chunks(columns)
        self.conn.commit()

    def _rebuild_chunks(self, columns):
        """老 schema(id 自增整数主键 + chunk_index)重建为 chunk_id(uuid) + chunk_seq,向量一并搬移"""
        title_sql = "title" if "title" in columns else "''"
        old_rows = self.conn.execute(
            f"SELECT id, document_id, chunk_index, content, {title_sql} FROM chunks"
        ).fetchall()

        # 先读出老向量及维度
        had_vector_table = self._has_vector_table()
        old_vectors = {}
        dim = None
        if had_vector_table:
            for old_id, embedding in self.conn.execute("SELECT chunk_id, embedding FROM chunk_vectors"):
                old_vectors[old_id] = embedding
                dim = len(embedding) // 4

        with self.conn:
            if had_vector_table:
                self.conn.execute("DROP TABLE chunk_vectors")
            self.conn.execute("DROP TABLE chunks")
            self.conn.execute(
                """
                CREATE TABLE chunks (
                    chunk_id TEXT PRIMARY KEY,
                    document_id INTEGER NOT NULL REFERENCES documents(id),
                    chunk_seq INTEGER NOT NULL,
                    content TEXT NOT NULL,
                    title TEXT NOT NULL DEFAULT ''
                )
                """
            )
            if dim is not None:
                self._ensure_vector_table(dim)
            for old_id, document_id, chunk_seq, content, title in old_rows:
                chunk_id = str(uuid.uuid4())
                self.conn.execute(
                    "INSERT INTO chunks(chunk_id, document_id, chunk_seq, content, title) "
                    "VALUES (?, ?, ?, ?, ?)",
                    (chunk_id, document_id, chunk_seq, content, title),
                )
                if old_id in old_vectors:
                    self.conn.execute(
                        "INSERT INTO chunk_vectors(chunk_id, embedding) VALUES (?, ?)",
                        (chunk_id, old_vectors[old_id]),
                    )

    def _ensure_vector_table(self, dim):
        # vec0 的维度建表时才能确定,按首个向量的实际维度创建
        self.conn.execute(
            f"""
            CREATE VIRTUAL TABLE IF NOT EXISTS chunk_vectors USING vec0(
                chunk_id TEXT PRIMARY KEY,
                embedding FLOAT[{dim}]
            )
            """
        )

    def _has_vector_table(self):
        row = self.conn.execute(
            "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'chunk_vectors'"
        ).fetchone()
        return row is not None

    def has_document(self, path):
        """path(文档绝对路径)是否已存在于文档表"""
        row = self.conn.execute(
            "SELECT 1 FROM documents WHERE path = ?", (path,)
        ).fetchone()
        return row is not None

    def prepare_document(self, path):
        """获取或创建文档记录;已存在时先清理旧 chunks 及向量,保证重复写入幂等"""
        with self.conn:
            row = self.conn.execute(
                "SELECT id FROM documents WHERE path = ?", (path,)
            ).fetchone()
            if row is not None:
                document_id = row[0]
                if self._has_vector_table():
                    self.conn.execute(
                        "DELETE FROM chunk_vectors WHERE chunk_id IN "
                        "(SELECT chunk_id FROM chunks WHERE document_id = ?)",
                        (document_id,)
                    )
                self.conn.execute(
                    "DELETE FROM chunks WHERE document_id = ?", (document_id,)
                )
            else:
                cursor = self.conn.execute(
                    "INSERT INTO documents(path) VALUES (?)", (path,)
                )
                document_id = cursor.lastrowid
        return document_id

    def save_chunks(self, document_id, chunks, vectors):
        """追加写入一批 chunks 及其向量,chunk_id 为 uuid"""
        if vectors:
            self._ensure_vector_table(len(vectors[0]))
        with self.conn:
            for index, chunk in enumerate(chunks):
                chunk_id = str(uuid.uuid4())
                self.conn.execute(
                    "INSERT INTO chunks(chunk_id, document_id, chunk_seq, content, title) "
                    "VALUES (?, ?, ?, ?, ?)",
                    (
                        chunk_id,
                        document_id,
                        chunk.metadata.get("chunk_seq", index),
                        chunk.page_content,
                        getattr(chunk, "title", "") or "",
                    )
                )
                if vectors:
                    self.conn.execute(
                        "INSERT INTO chunk_vectors(chunk_id, embedding) VALUES (?, ?)",
                        (chunk_id, sqlite_vec.serialize_float32(vectors[index]))
                    )

    def close(self):
        self.conn.close()
