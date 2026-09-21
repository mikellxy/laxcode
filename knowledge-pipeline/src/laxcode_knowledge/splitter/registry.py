import json
import re

from .by_title import ByTitleSplitter

# 分块器注册表:配置文件里的 text_splitter 名称 -> 分块器类
TEXT_SPLITTERS = {
    "by_title_spliter": ByTitleSplitter,
}

# 匹配 json 里非法的反斜杠转义,如正则里直接写 \s
_INVALID_ESCAPE_RE = re.compile(r'\\(?![\\"/bfnrtu])')


def _load_json(config_path):
    with open(config_path, "r", encoding="utf-8") as f:
        raw = f.read()
    try:
        return json.loads(raw)
    except json.JSONDecodeError as e:
        if "Invalid \\escape" in str(e):
            # 容错:把 \s 这类没写成 \\s 的正则转义修正后重试
            return json.loads(_INVALID_ESCAPE_RE.sub(r"\\\\", raw))
        raise


def load_text_splitter(config_path):
    """按 json 配置文件实例化 text_splitter"""
    config = _load_json(config_path)
    if not isinstance(config, dict):
        raise ValueError("chunk 配置必须是一个 json 对象")

    name = config.get("text_splitter")
    if name not in TEXT_SPLITTERS:
        raise ValueError(
            f"未知的 text_splitter: {name!r},可选: {', '.join(sorted(TEXT_SPLITTERS))}"
        )

    properties = config.get("properties") or {}
    if not isinstance(properties, dict):
        raise ValueError("properties 必须是一个 json 对象")
    try:
        return TEXT_SPLITTERS[name](**properties)
    except (TypeError, re.error) as e:
        raise ValueError(f"实例化 {name} 失败: {e}") from e
