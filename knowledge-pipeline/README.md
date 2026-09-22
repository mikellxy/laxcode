# 知识库导入

`knowledge-pipeline` 将 UTF-8 文本文档按标题分块，调用 embedding 模型生成向量，并写入 SQLite 知识库。以下命令在仓库根目录运行。

## 配置向量模型

管线读取 `~/.laxcode/settings.json`。如果已按[主项目配置说明](../docs/settings.md)配置向量模型，可以直接使用。相关字段如下：

| 字段 | 含义 |
| --- | --- |
| `embedding_model` | 要使用的模型，格式为 `provider_name:model_name`。 |
| `embedding_vec_dim` | embedding 返回的向量维度，整数，范围为 1～8192；必须与实际模型一致。 |
| `provider_list` | 服务提供方列表；`embedding_model` 指向的提供方和模型必须在其中。 |
| `provider_name` | 提供方名称，对应 `embedding_model` 冒号前的部分。 |
| `openai_base_url` | 提供方的 OpenAI 兼容 API 地址。 |
| `openai_api_key` | 提供方的 API Key。 |
| `model_list[].model_name` | 模型名称，对应 `embedding_model` 冒号后的部分。 |

例如：

```json
{
  "embedding_model": "example:embedding",
  "embedding_vec_dim": 1024,
  "provider_list": [
    {
      "provider_name": "example",
      "openai_base_url": "https://api.example.com/v1",
      "openai_api_key": "YOUR_API_KEY",
      "model_list": [{ "model_name": "embedding" }]
    }
  ]
}
```

## 配置分块器

管线自动读取 `~/.laxcode/chunk_settings.json`。文件不存在时，直接使用默认的 `ByTitleSplitter`：匹配 Markdown 标题行，`chunk_size` 为 2800，`overlap_size` 为 400。处理普通小说文本时，可参考[示例配置](./chunk_config.example.json)设置“第…回”标题规则。

| 字段 | 含义 |
| --- | --- |
| `text_splitter` | 分块器名称，目前填写 `by_title_spliter`。 |
| `properties.title_re` | 标题行的正则表达式。写在 JSON 中时，正则的反斜杠需要转义，例如 `\\s`。 |
| `properties.chunk_size` | 每次从章节新增的字符数，必须大于 0；默认 2800。 |
| `properties.overlap_size` | 后续块重复上一块末尾的字符数，须满足 `0 <= overlap_size < chunk_size`；默认 400。 |

分块器先按标题划分章节，再按字符数切块；从第二块开始附加重叠文本，因此实际块长度最多为 `chunk_size + overlap_size`。

## 导入文档

```shell
uv sync --project knowledge-pipeline --locked
mkdir -p /tmp/laxcode-qa
"$PWD/knowledge-pipeline/.venv/bin/laxcode-knowledge" \
  --target=knowledge \
  --doc=/absolute/path/to/knowledge.txt \
  --db=/tmp/laxcode-qa/kb.sqlite
```

`--doc` 指向 UTF-8 文本文件的绝对路径；`--db` 指向 SQLite 数据库文件的绝对路径，其父目录须已存在。数据库文件不存在时会自动创建。同一文档路径已导入时，命令会报错以避免重复入库。
