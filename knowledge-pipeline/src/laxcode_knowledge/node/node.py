import codecs

from laxcode_knowledge.tools import Registry
from laxcode_knowledge.state import MessagesState

from langchain.messages import ToolMessage, SystemMessage


class LLMNode(object):

    def __init__(self, model):
        self.model = model

    def call(self, state: dict):
        system_message = SystemMessage(
            content="You are a helpful assistant tasked with performing arithmetic on a set of inputs."
        )
        response = self.model.invoke([system_message] + state["messages"])
        return {
            "messages": [response],
            "llm_calls": state.get('llm_calls', 0) + 1
        }

    @classmethod
    def name(cls):
        return "llm_node"


class ToolNode(object):

    def __init__(self, tool_registry: Registry):
        self.tool_registry = tool_registry

    def execute(self, state: dict):
        """Performs the tool call"""

        result = []
        for tool_call in state["messages"][-1].tool_calls:
            tool = self.tool_registry.tools_by_name[tool_call["name"]]
            observation = tool.invoke(tool_call["args"])
            result.append(ToolMessage(content=observation, tool_call_id=tool_call["id"]))
        return {"messages": result}

    @classmethod
    def name(cls):
        return "tool_node"


class ReActNode(object):

    def __init__(self, tool_node, end):
        self.tool_node = tool_node
        self.end = end

    def should_continue(self, state: MessagesState):
        """Decide if we should continue the loop or stop based upon whether the LLM made a tool call"""

        messages = state["messages"]
        last_message = messages[-1]

        # If the LLM makes a tool call, then perform an action
        if last_message.tool_calls:
            return self.tool_node

        # Otherwise, we stop (reply to the user)
        return self.end

    @classmethod
    def name(cls):
        return "react_node"

class EmbeddingNode(object):

    def __init__(self, model, vec_writer):
        self.model = model
        self.vec_writer = vec_writer

    def embed(self, state):
        chunks = state["chunks"]
        if not chunks:
            return {"num_chunks": 0}
        vectors = self.model.embed_documents([chunk.page_content for chunk in chunks])
        self.vec_writer.validate_vectors(chunks, vectors)
        document_id = state.get("document_id")
        if document_id is None:
            # 首个批次:获取/创建文档记录,并清理历史数据保证幂等
            document_id = self.vec_writer.prepare_document(state["document_path"])
        self.vec_writer.save_chunks(document_id, chunks, vectors)
        return {"document_id": document_id, "num_chunks": len(chunks)}


    @classmethod
    def name(cls):
        return "embedding_node"

class ChunkNode(object):
    def __init__(self, text_splitter, max_bytes=100000):
        """
        text_splitter: 有 split_documents(text, final) 方法的分块器,
        返回 (chunks, 本次消费的字节数, 分节标题)
        """
        self.text_splitter = text_splitter
        self.max_bytes = max_bytes

    def chunk(self, state):
        document_path = state["document_path"]
        offset = state.get("read_offset", 0)
        pending = state.get("pending_text", "")

        # 二进制读取,一次最多 max_bytes 字节,从上次读到的位置继续
        with open(document_path, "rb") as f:
            f.seek(offset)
            data = f.read(self.max_bytes)
        file_exhausted = len(data) < self.max_bytes

        # 增量解码 utf-8,末尾不完整的字符保留到下一批次,避免半个汉字
        content = ""
        read_bytes = 0
        if data:
            decoder = codecs.getincrementaldecoder("utf-8")()
            content = decoder.decode(data, final=file_exhausted)
            read_bytes = len(content.encode("utf-8"))
            if not file_exhausted and read_bytes == 0:
                # max_bytes 小于单个 utf-8 字符字节数时强制前进,避免死循环
                content = data.decode("utf-8", errors="ignore")
                read_bytes = len(data)

        # 上次未消费完的残余文本 + 本次新读到的文本
        text = pending + content
        chunks, processed_bytes, title = self.text_splitter.split_documents(
            text, final=file_exhausted
        )

        # 按分块器返回的消费字节数截掉已处理部分,剩余的留到下一批次
        remaining = text.encode("utf-8")[processed_bytes:].decode("utf-8")

        # chunk_seq 跨批次连续递增(文档内编号)
        base_index = state.get("num_chunks", 0)
        for index, chunk in enumerate(chunks):
            chunk.metadata["chunk_seq"] = base_index + index

        return {
            "chunks": chunks,
            "read_offset": offset + read_bytes,
            "pending_text": remaining,
            "doc_finished": file_exhausted and not remaining.strip(),
            "processed_bytes": processed_bytes,
            "current_title": title,
        }

    @classmethod
    def name(cls):
        return "chunk_node"
