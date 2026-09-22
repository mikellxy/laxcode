import json
import os
from pathlib import Path


def _field(obj, name):
    if not isinstance(obj, dict):
        return None
    return next((value for key, value in obj.items() if isinstance(key, str) and key.lower() == name), None)


def _env(name):
    return os.environ.get(name, "").strip()


def load_knowledge_embedding_config():
    """Resolve the knowledge model and vec0 dimensions from LaxCode settings."""
    path = Path.home() / ".laxcode" / "settings.json"
    try:
        settings = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        settings = {}
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"无法读取配置文件 {path}: {exc}") from exc
    if not isinstance(settings, dict):
        raise ValueError(f"配置文件 {path} 必须是 JSON 对象")

    ref = _env("EMBEDDING_MODEL") or _field(settings, "embedding_model")
    name = _env("OPENAI_EMBEDDING_MODEL_NAME")
    base_url = _env("OPENAI_EMBEDDING_BASE_URL")
    api_key = _env("OPENAI_EMBEDDING_API_KEY")
    if not (name and base_url and api_key):
        if not isinstance(ref, str) or ":" not in ref:
            raise ValueError("缺少 embedding_model；请在 ~/.laxcode/settings.json 配置 provider:model")
        provider_name, model_name = ref.split(":", 1)
        providers = _field(settings, "provider_list") or []
        if not isinstance(providers, list):
            raise ValueError("provider_list 必须是数组")
        provider = next((item for item in providers if _field(item, "provider_name") == provider_name), None)
        models = _field(provider, "model_list") or []
        if not isinstance(models, list):
            raise ValueError("model_list 必须是数组")
        model = next((item for item in models if _field(item, "model_name") == model_name), None)
        if model is None:
            raise ValueError(f"embedding_model {ref!r} 未在 provider_list 中定义")
        name = name or model_name
        base_url = base_url or _field(provider, "openai_base_url")
        api_key = api_key or _field(provider, "openai_api_key")
    if not all(isinstance(value, str) and value.strip() for value in (name, base_url, api_key)):
        raise ValueError("embedding 模型缺少名称、服务地址或 API key")

    dimensions = _env("EMBEDDING_VEC_DIM") or _field(settings, "embedding_vec_dim")
    if isinstance(dimensions, bool):
        raise ValueError("embedding_vec_dim 必须是 1 到 8192 的整数")
    try:
        dimension_count = int(dimensions)
    except (TypeError, ValueError) as exc:
        raise ValueError("embedding_vec_dim 必须是 1 到 8192 的整数") from exc
    if str(dimensions).strip() != str(dimension_count) or not 1 <= dimension_count <= 8192:
        raise ValueError("embedding_vec_dim 必须是 1 到 8192 的整数")
    return name, base_url, api_key, dimension_count
