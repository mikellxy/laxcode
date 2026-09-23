# SSE 用户长期记忆：运行与实现

SSE 使用 `AssembleSSE`，与 QA 共用请求资源装配，工具注册表为空。CLI 仍使用原有 Agent 装配。匿名会话正常聊天并累计轮次，但不召回或创建记忆任务；通过创建会话接口绑定 user_id 后才启用长期记忆。

## 部署

Python 项目已复制到本仓库的 `knowledge-pipeline/`（保留独立 pyproject.toml 和 uv.lock，不包含虚拟环境或数据库）。安装依赖：

```sh
cd knowledge-pipeline
uv sync --locked
```

启用长期记忆时，先设置环境变量并初始化记忆 schema（不会调用 embedding）：

```sh
export OPENAI_EMBEDDING_MODEL_NAME='与 Go EMBEDDING_MODEL 解析出的上游模型名一致'
export OPENAI_EMBEDDING_BASE_URL='你的 embedding 服务地址'
export OPENAI_EMBEDDING_API_KEY='你的 embedding API key'
export USER_MEMORY_EXECUTABLE='/absolute/path/LaxCode/knowledge-pipeline/.venv/bin/laxcode-knowledge'
/absolute/path/LaxCode/knowledge-pipeline/.venv/bin/laxcode-knowledge \
  --target user_memory --db /absolute/path/kb.sqlite --dimensions 1024 --init-schema
```

初始化时数据库父目录必须存在。原知识库的 documents/chunks/chunk_vectors 不受迁移影响。启动 SSE 时将同一个文件传给 `-kb`：

```sh
./bin/laxcode -sse -kb=/absolute/path/kb.sqlite -vector-dim=1024
./bin/laxcode -qa -kb=/absolute/path/kb.sqlite
./bin/laxcode -sse -qa -kb=/absolute/path/kb.sqlite -workdir=/tmp/laxcode-qa -addr=127.0.0.1:8090
```

QA 始终必须提供绝对数据库文件路径（`~` 不在程序内展开）。`-sse -qa` 使用与单独 `-qa` 完全相同的 QA ReAct 服务，只将交互层替换为 HTTP/SSE；它不启动用户记忆 worker，也不需要 `-vector-dim`。三个 `OPENAI_EMBEDDING_*` 环境变量全部配置时，单独 SSE 必须同时提供绝对路径 `-kb` 和 1–8192 范围内的 `-vector-dim`（受 sqlite-vec 上限约束）。两种模式均不回退到 `<workDir>/kb/kb.sqlite` 或 `USER_MEMORY_DB`。SSE 的 Python `--db/--dimensions` 参数与 Go 召回使用同一个 `-kb/-vector-dim` 值。`-workdir` 只决定会话等工作数据位置。

缺少任一 embedding 环境变量时，SSE 不校验 `-kb` 或 `-vector-dim`，两者均可省略，也不会打开向量数据库。启用记忆后，SSE 才校验这两个参数，并在启动时自动调用 Python `--init-schema` 幂等创建或检查用户记忆表，成功后才创建 Go retriever；无需手动预初始化。QA 使用前仍需要导入文档知识库。

Go 环境变量或配置文件新增：

| 配置 | 含义 |
| --- | --- |
| USER_MEMORY_EXECUTABLE | 启用记忆时配置，Python 环境中 laxcode-knowledge 可执行文件的绝对路径 |
| USER_MEMORY_CONCURRENCY | worker 并发数，默认 1，范围 1–8 |
| USER_MEMORY_TIMEOUT_SECONDS | 每项任务超时，默认 120 秒，范围 1–3600 |

SSE 启动和异步任务执行前检查 `OPENAI_EMBEDDING_MODEL_NAME`、`OPENAI_EMBEDDING_BASE_URL`、`OPENAI_EMBEDDING_API_KEY`。任一未设置、为空或仅包含空白时，跳过记忆功能：不要求 `-kb` 或 `-vector-dim`，不校验 Python 入口、不打开向量库、不调用摘要/embedding，也不报配置错误，普通 SSE 聊天正常。三个变量全部配置时，必须同时指定绝对路径 `-kb` 和 1–8192 范围内的 `-vector-dim`。每三轮产生的任务仍保留记录，由 worker 标记为 `skipped`，不重试、不补跑。只在配置文件中设置 embedding 参数不会替代这三个环境变量检查。

未配置 `USER_MEMORY_EXECUTABLE` 时同样跳过。补齐配置并重启 SSE 后，新任务正常执行。环境齐全且配置了入口时，仍校验入口、模型、schema 和向量检索语法，真实配置错误不会被吞掉。Go 调用 Python 时直接传参数与 stdin JSON，并将自身解析出的 embedding 模型、地址和凭据注入子进程环境，覆盖继承值。`user_memory_config` 固定模型名及 `-vector-dim`；更换向量空间需单独迁移或重建记忆库。

## 消息、轮次和任务

- `messages.react_turn`：仅原始、无工具调用、正常完成的 assistant 记录写入正整数；其他消息及工作集记录为 NULL。数据库约束和 `(session_id, react_turn)` 部分唯一索引防止非法标记及重复轮次。实际类型值为 `original`。
- `request_contexts.react_turn_count`：跨请求累计，压缩/恢复不重置。老会话从零累计，不回算既有历史。首个新窗口从当前用户输入开始。
- `react_turns`：以 assistant Seq 作为稳定完成标识，保存不可变原始来源 Seq。最终 assistant、工作集、计数、来源及每三轮任务在一个事务提交。
- `user_memory_jobs`：窗口固定为 1–3、4–6 等；保存仅含 Seq、role、原始 Content 的去重快照。排除系统、reasoning、工具输出、召回 chunk 和压缩文本。

SSE 不等待任务完成；紧接第 3 轮的 query 可能暂时召回不到新记忆。worker 每秒轮询，任务最多尝试 5 次，指数退避，崩溃后回收过期租约。摘要先保存再调用 Python，重试复用摘要；空事实数组标记 skipped，永久错误和耗尽重试标记 failed。服务器退出取消在途任务并等待有限超时清理，未完成任务保留供重启恢复。

来源键是 `session_id:react:end_turn`。Python 在写事务前完成分块和 embedding，在事务内再次查重并一次提交主记录、chunk 和向量；重复投递返回原 memory_id，正文或归属冲突报错。摘要仅提取用户有依据的事实和偏好，不把 assistant 推测当用户事实。

## 召回与回收

每个新 query 使用持久化 user_id 做 KNN 内部过滤并取 top3，允许同用户跨会话召回。失败时日志记录并降级为原始 query。

`Message.MemoryChunks` 与 `Content` 分离，只保存于工作集。provider 和本地 token 计数共用 `ModelContent()` 序列化，明确将记忆作为参考数据。新 query 原子清理旧工作集 chunk；Resume 复用已保存 chunk，不重复召回。触发压缩时先清空 chunk 并重新计数，必要时才压缩正文。原始历史接口继续展示原文。

## 验证

```sh
go test ./...
# 在 Python 项目中
python -m unittest discover -s tests -v
# Python 写库 / Go 读库兼容性验证（假 embedding）
LAX_MEMORY_TEST_PYTHON=/absolute/path/LaxCode/knowledge-pipeline/.venv/bin/python \
  go test ./internal/infrastructure/knowledgebase ./internal/infrastructure/memorypipeline -count=1
```

测试覆盖持久化轮次、3/6 窗口、重复提交、截断/Resume、租约抢占、摘要复用、空摘要、有限重试、chunk 回收、向量校验、原子回滚、用户隔离和模型不匹配。跨语言测试验证 Python sqlite-vec 0.1.9 与 Go sqlite-vec 0.1.6 对同一数据库的元数据过滤兼容性。常规验证不调用真实付费模型。
