import json
import os
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from laxcode_knowledge.models.knowledge_config import load_knowledge_embedding_config
from laxcode_knowledge.models.models import get_embedding_model
from laxcode_knowledge.node import EmbeddingNode
from laxcode_knowledge.store import VecWriter


class KnowledgeConfigTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.home = Path(self.directory.name)
        settings_dir = self.home / ".laxcode"
        settings_dir.mkdir()
        (settings_dir / "settings.json").write_text(json.dumps({
            "EMBEDDING_MODEL": "example:embed",
            "EMBEDDING_VEC_DIM": 7,
            "PROVIDER_LIST": [{
                "PROVIDER_NAME": "example",
                "OPENAI_API_KEY": "file-key",
                "OPENAI_BASE_URL": "https://file.example/v1",
                "MODEL_LIST": [{"MODEL_NAME": "embed"}],
            }],
        }), encoding="utf-8")

    def tearDown(self):
        self.directory.cleanup()

    def test_settings_resolve_model_and_dimensions(self):
        with patch.object(Path, "home", return_value=self.home), patch.dict(os.environ, {}, clear=True):
            config = load_knowledge_embedding_config()
        self.assertEqual(config, ("embed", "https://file.example/v1", "file-key", 7))
        with patch("laxcode_knowledge.models.models.OpenAIEmbeddings") as embeddings:
            get_embedding_model(*config[:3])
            self.assertEqual(embeddings.call_args.kwargs["model"], "embed")

    def test_environment_overrides_individual_settings(self):
        env = {"OPENAI_EMBEDDING_MODEL_NAME": "override", "EMBEDDING_VEC_DIM": "9"}
        with patch.object(Path, "home", return_value=self.home), patch.dict(os.environ, env, clear=True):
            self.assertEqual(load_knowledge_embedding_config(),
                             ("override", "https://file.example/v1", "file-key", 9))

    def test_complete_environment_configuration_without_settings_file(self):
        env = {
            "OPENAI_EMBEDDING_MODEL_NAME": "env-model",
            "OPENAI_EMBEDDING_BASE_URL": "https://env.example/v1",
            "OPENAI_EMBEDDING_API_KEY": "env-key",
            "EMBEDDING_VEC_DIM": "11",
        }
        missing_home = self.home / "missing"
        with patch.object(Path, "home", return_value=missing_home), patch.dict(os.environ, env, clear=True):
            self.assertEqual(load_knowledge_embedding_config(),
                             ("env-model", "https://env.example/v1", "env-key", 11))

    def test_invalid_dimensions_fail_before_database_creation(self):
        with patch.object(Path, "home", return_value=self.home), patch.dict(os.environ, {"EMBEDDING_VEC_DIM": "0"}, clear=True):
            with self.assertRaisesRegex(ValueError, "embedding_vec_dim"):
                load_knowledge_embedding_config()

    def test_configured_dimensions_define_vector_table_and_validate_embeddings(self):
        path = str(self.home / "knowledge.sqlite")
        writer = VecWriter(path, 7)
        try:
            schema = writer.conn.execute("SELECT sql FROM sqlite_master WHERE name='chunk_vectors'").fetchone()[0]
            self.assertIn("FLOAT[7]", schema)
            from langchain_core.documents import Document
            document_id = writer.prepare_document("/example.md")
            with self.assertRaisesRegex(ValueError, "维度"):
                writer.save_chunks(document_id, [Document(page_content="test")], [[1.0] * 8])
            self.assertEqual(writer.conn.execute("SELECT count(*) FROM chunks").fetchone()[0], 0)
        finally:
            writer.close()
        with self.assertRaisesRegex(ValueError, "维度"):
            VecWriter(path, 8)

    def test_bad_embedding_does_not_mark_document_as_imported(self):
        from langchain_core.documents import Document
        writer = VecWriter(str(self.home / "bad.sqlite"), 7)
        try:
            model = type("FakeModel", (), {"embed_documents": lambda self, texts: [[1.0] * 8]})()
            node = EmbeddingNode(model, writer)
            with self.assertRaisesRegex(ValueError, "维度"):
                node.embed({"chunks": [Document(page_content="test")], "document_path": "/example.md"})
            self.assertFalse(writer.has_document("/example.md"))
        finally:
            writer.close()


if __name__ == "__main__":
    unittest.main()
