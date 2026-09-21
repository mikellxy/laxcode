import re

from langchain_core.documents import Document

# 默认标题正则:markdown 标题行
DEFAULT_TITLE_RE = r"^#{1,6}\s+\S"


class Chunk(Document):
    """分块对象:在 Document 基础上增加 title 属性(所属分节标题)"""

    title: str = ""


class ByTitleSplitter(object):
    """按标题分节的增量文本分块器

    每次调用 split_documents 只处理一个完整的"分节":
    从文本开头匹配最近的标题,取该标题到下一个标题之间的内容(含该标题行本身),
    按 chunk_size 硬切分块;第 2 块起开头冗余上一块结尾的 overlap_size 字符,
    因此单个 chunk 最大为 chunk_size + overlap_size 字符。

    返回 (chunks, 本次消费的字节数, 标题):
    - chunks: Chunk 对象列表,带 page_content / title 属性
    - 字节数: 本次从文本开头消费了多少字节,调用方据此推进文档读取进度
    - 标题: 本次处理的分节标题;无标题分节为空字符串

    分节不完整(最近的标题后面还找不到下一个标题)且 final=False 时,
    不消费、不分块,调用方继续读入更多文本后重试;
    final=True 表示文本已到文档末尾,最后一个没有"下一个标题"的分节也会被处理。
    """

    def __init__(self, title_re=DEFAULT_TITLE_RE, chunk_size=2800, overlap_size=400):
        if chunk_size <= 0:
            raise ValueError("chunk_size 必须大于 0")
        if not 0 <= overlap_size < chunk_size:
            raise ValueError("overlap_size 必须满足 0 <= overlap_size < chunk_size")
        if isinstance(title_re, str):
            self.title_re = re.compile(title_re, re.MULTILINE)
        else:
            self.title_re = title_re
        self.chunk_size = chunk_size
        self.overlap_size = overlap_size

    def split_documents(self, text, final=False):
        if not text.strip():
            return [], 0, ""

        first = self.title_re.search(text)

        # 全文没有标题:文档已读完时整段作为无标题分节,否则等待更多文本
        if first is None:
            if not final:
                return [], 0, ""
            return self._split_section(text, ""), len(text.encode("utf-8")), ""

        # 首个标题之前还有正文(前言):先作为无标题分节处理掉
        if text[:first.start()].strip():
            section = text[:first.start()]
            return self._split_section(section, ""), len(section.encode("utf-8")), ""

        # 找下一个标题确定分节边界;找不到且文档未读完,等待更多文本
        second = self.title_re.search(text, first.end())
        if second is None and not final:
            return [], 0, ""

        title = first.group(0).strip()
        end = len(text) if second is None else second.start()
        section = text[first.start():end]
        consumed = len(text[:end].encode("utf-8"))
        return self._split_section(section, title), consumed, title

    def _split_section(self, section, title):
        section = section.strip()
        if not section:
            return []

        # 首块从分节开头(标题行)开始,无冗余
        chunks = [Chunk(page_content=section[:self.chunk_size], title=title)]
        pos = self.chunk_size
        while pos < len(section):
            if not section[pos:].strip():
                # 剩余全是空白,不再产生 chunk
                break
            # 开头冗余上一块结尾的 overlap_size 字符
            chunks.append(
                Chunk(page_content=section[pos - self.overlap_size:pos + self.chunk_size], title=title)
            )
            pos += self.chunk_size
        return chunks
