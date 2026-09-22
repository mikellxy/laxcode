import argparse
import os
from pathlib import Path

from typing_extensions import Literal

from laxcode_knowledge.models import get_embedding_model
from laxcode_knowledge.models.knowledge_config import load_knowledge_embedding_config
from laxcode_knowledge.node import ChunkNode, EmbeddingNode
from laxcode_knowledge.splitter import ByTitleSplitter, load_text_splitter
from laxcode_knowledge.state import IngestState
from laxcode_knowledge.store import VecWriter

from langgraph.graph import StateGraph, START, END


def should_continue(state: IngestState) -> Literal["chunk_node", END]:
    """文档未读完则回到 chunk_node 继续读,否则结束"""
    if state.get("doc_finished"):
        return END
    return "chunk_node"


def load_chunk_splitter():
    config_path = Path.home() / ".laxcode" / "chunk_settings.json"
    if not config_path.exists():
        return ByTitleSplitter()
    try:
        return load_text_splitter(str(config_path))
    except (ValueError, OSError) as exc:
        raise ValueError(f"加载 chunk 配置失败: {config_path}: {exc}") from exc


def main() -> None:
    parser = argparse.ArgumentParser(description="文档向量化:分块 -> 嵌入 -> 写入 sqlite-vec")
    group = parser.add_mutually_exclusive_group()
    group.add_argument("--doc", help="文档绝对路径")
    group.add_argument("--stdin-json", action="store_true")
    parser.add_argument("--target", choices=("knowledge", "user_memory"), default="knowledge")
    parser.add_argument("--init-schema", action="store_true")
    parser.add_argument("--dimensions", type=int, help="user_memory vector dimensions")
    parser.add_argument("--db", required=True, help="sqlite-vec 数据库文件绝对路径")
    args = parser.parse_args()

    if args.target == "user_memory":
        if args.doc or not (args.stdin_json or args.init_schema):
            parser.error("user_memory requires --stdin-json or --init-schema")
        if args.dimensions is None or not 1 <= args.dimensions <= 8192:
            parser.error("user_memory requires --dimensions between 1 and 8192")
        from laxcode_knowledge.user_memory import run
        run(args)
        return
    if not args.doc or args.stdin_json or args.init_schema or args.dimensions is not None:
        parser.error("knowledge requires --doc")

    doc_path = os.path.abspath(args.doc)
    db_path = os.path.abspath(args.db)
    if not os.path.isfile(doc_path):
        raise SystemExit(f"文档不存在: {doc_path}")

    try:
        text_splitter = load_chunk_splitter()
    except ValueError as exc:
        raise SystemExit(str(exc)) from exc

    try:
        name, base_url, api_key, dimensions = load_knowledge_embedding_config()
        model = get_embedding_model(name, base_url, api_key)
    except ValueError as e:
        raise SystemExit(str(e)) from e

    try:
        vec_writer = VecWriter(db_path, dimensions)
    except ValueError as e:
        raise SystemExit(str(e))
    if vec_writer.created:
        print(f"数据库不存在,已创建并建表: {db_path}")

    # 启动工作流之前查重:文档已在文档表中则直接报错退出
    if vec_writer.has_document(doc_path):
        vec_writer.close()
        raise SystemExit(f"文档已存在,请不要重复向量化文档: {doc_path}")

    chunk_node = ChunkNode(text_splitter)
    embedding_node = EmbeddingNode(model, vec_writer)

    builder = StateGraph(IngestState)
    # Add nodes
    builder.add_node(chunk_node.name(), chunk_node.chunk)
    builder.add_node(embedding_node.name(), embedding_node.embed)
    # Add edges to connect nodes
    builder.add_edge(START, chunk_node.name())
    builder.add_edge(chunk_node.name(), embedding_node.name())
    # 文档未读完则循环回 chunk_node 继续读,否则去到 END
    builder.add_conditional_edges(embedding_node.name(), should_continue)

    graph = builder.compile()

    try:
        result = graph.invoke({"document_path": doc_path, "read_offset": 0})
    finally:
        vec_writer.close()

    # 打印工作总结
    print("=" * 40)
    print("文档向量化完成")
    print(f"文档路径: {doc_path}")
    print(f"文档ID:   {result.get('document_id')}")
    print(f"分块数量: {result.get('num_chunks', 0)}")
    print(f"处理字节: {result.get('processed_bytes', 0)}")
    print(f"数据库:   {db_path}")
    print("=" * 40)

__all__ = ['main']
