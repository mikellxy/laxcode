import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from laxcode_knowledge.cmd import load_chunk_splitter
from laxcode_knowledge.splitter.by_title import DEFAULT_TITLE_RE, ByTitleSplitter


class ChunkSettingsTests(unittest.TestCase):
    def test_missing_home_config_uses_default_title_rule(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(Path, "home", return_value=Path(directory)):
                splitter = load_chunk_splitter()

        self.assertIsInstance(splitter, ByTitleSplitter)
        self.assertEqual(splitter.title_re.pattern, DEFAULT_TITLE_RE)

    def test_home_config_sets_title_rule_and_chunk_sizes(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config_path = home / ".laxcode" / "chunk_settings.json"
            config_path.parent.mkdir()
            config_path.write_text(json.dumps({
                "text_splitter": "by_title_spliter",
                "properties": {
                    "title_re": r"^第[一二三四五六七八九十百]+回\s+.*$",
                    "chunk_size": 100,
                    "overlap_size": 10,
                },
            }), encoding="utf-8")
            with patch.object(Path, "home", return_value=home):
                splitter = load_chunk_splitter()

        self.assertEqual((splitter.chunk_size, splitter.overlap_size), (100, 10))
        self.assertIsNotNone(splitter.title_re.search("第一回 灵根育孕源流出"))

    def test_invalid_home_config_reports_its_path(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            config_path = home / ".laxcode" / "chunk_settings.json"
            config_path.parent.mkdir()
            config_path.write_text("{invalid", encoding="utf-8")
            with patch.object(Path, "home", return_value=home):
                with self.assertRaisesRegex(ValueError, "chunk_settings.json"):
                    load_chunk_splitter()


if __name__ == "__main__":
    unittest.main()
