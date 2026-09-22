import os
from langchain.chat_models import init_chat_model
from langchain_openai import OpenAIEmbeddings

def get_chat_model():
    name = os.environ.get("OPENAI_MODEL_NAME")
    base_url = os.environ.get("OPENAI_BASE_URL")
    api_key = os.environ.get("OPENAI_API_KEY")

    if not name or not base_url or not api_key:
        raise ValueError()

    model = init_chat_model(
        f"openai:{name}",  # 加 openai: 前缀,走 OpenAI 兼容协议
        base_url=base_url,
        api_key=api_key,
        temperature=0,
    )
    return model

def get_embedding_model(name=None, base_url=None, api_key=None):
    name = name or os.environ.get("OPENAI_EMBEDDING_MODEL_NAME")
    base_url = base_url or os.environ.get("OPENAI_EMBEDDING_BASE_URL")
    api_key = api_key or os.environ.get("OPENAI_EMBEDDING_API_KEY")

    if not name or not base_url or not api_key:
        raise ValueError()

    return OpenAIEmbeddings(
        model=name,
        openai_api_base=base_url,  # 智谱embedding
        openai_api_key=api_key,
        # 部分 OpenAI 兼容服务(如智谱/阿里)只接受字符串数组,
        # 关掉默认的 tiktoken 切分,直接发送原始文本
        check_embedding_ctx_length=False,
    )
