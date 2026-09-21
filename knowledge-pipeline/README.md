

## 用户长期记忆入口

保留默认 `--target knowledge --doc ...` 导入路径。用户记忆使用独立的表组、按标题分块（600 字符、无重叠）、由 `--dimensions` 指定维度的向量与原子写入。

先设置 `OPENAI_EMBEDDING_MODEL_NAME`，执行不调用模型的幂等初始化：

```sh
laxcode-knowledge --target user_memory --db /absolute/path/kb.sqlite --dimensions 1024 --init-schema
```

入库：`laxcode-knowledge --target user_memory --db /absolute/path/kb.sqlite --dimensions 1024 --stdin-json`。
stdin 为包含 `user_id`（规范 UUID）、`session_id`、`source_key`（`session_id:react:3`）、`start_turn`（1）、`end_turn`（3）、`content` 的 JSON 对象，最多 1 MiB。每个窗口恰好三轮。stdout 仅返回成功 JSON；日志/错误写 stderr，参数、来源冲突、维度等永久错误退出码为 2，其余错误为 1。

模型配置沿用 `OPENAI_EMBEDDING_MODEL_NAME / OPENAI_EMBEDDING_BASE_URL / OPENAI_EMBEDDING_API_KEY`，必须与 Go 检索侧一致。模型名和维度写入 `user_memory_config`，不允许静默混用。相同来源键重试返回原 `memory_id`，不同正文或用户归属报冲突。网络 embedding 在写事务外执行，主记录、chunk 和向量一次提交。

离线回归：`python -m unittest discover -s tests -v`。测试使用假 embedding，不调用付费模型。


## 在 LaxCode 仓库中使用

本目录是 Python 管线的独立副本。运行 `uv sync --locked` 安装本地依赖，或从任意目录调用 `knowledge-pipeline/run.sh`（需提供实际脚本路径）。脚本使用自身目录定位 Python 项目，传入的文档和数据库路径仍建议使用绝对路径。

SSE 异步用户记忆要求以下环境变量全部非空：`OPENAI_EMBEDDING_MODEL_NAME`、`OPENAI_EMBEDDING_BASE_URL`、`OPENAI_EMBEDDING_API_KEY`。缺少任意一项时，Go 将任务静默标记为 skipped，不调用本管线或摘要模型。启用时将 `USER_MEMORY_EXECUTABLE` 指向本目录 `.venv/bin/laxcode-knowledge` 的绝对路径，并按 [SSE 运行说明](../docs/sse-user-memory.md) 初始化数据库。

启动 Go SSE 或 QA 时必须传入 `-kb=/absolute/path/file.sqlite`。Python 初始化和文档导入的 `--db` 应指向同一文件；SSE 异步入库会自动把 `-kb` 指定路径传给 Python，不再读取 `USER_MEMORY_DB`。
