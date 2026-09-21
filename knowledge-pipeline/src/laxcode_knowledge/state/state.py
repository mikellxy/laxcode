from langchain.messages import AnyMessage
from typing_extensions import TypedDict, Annotated
import operator


class MessagesState(TypedDict):
    messages: Annotated[list[AnyMessage], operator.add]
    llm_calls: int


class IngestState(TypedDict, total=False):
    document_path: str
    read_offset: int                      # chunk_node 下次从第几个字节开始读
    pending_text: str                     # 已读出但尚未被分块器消费的残余文本
    doc_finished: bool                    # 文档是否已读完且残余文本已消费完
    chunks: list                          # 当前批次的分块
    document_id: int
    num_chunks: Annotated[int, operator.add]       # 跨批次累计分块数
    processed_bytes: Annotated[int, operator.add]  # 跨批次累计已处理文档字节数
    current_title: str                    # 当前批次处理的分节标题