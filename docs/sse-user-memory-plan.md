# SSE 用户长期记忆与 RAG 异步管线改造方案

状态：设计方案，尚未实施。日期：2026-09-20。

涉及项目：

- `LaxCode`：SSE 对话、轮次计数、异步摘要任务、记忆召回及上下文管理。
- `/Users/lxy/Documents/code/laxcode_knowledge`：复用现有分块与 embedding 能力，扩展用户记忆入库入口。

## 1. 已确定的行为

1. SSE 单独装配 ReActService，不注册业务工具、子 Agent 或 read_artifact 工具，使用专用系统提示词。
2. ReAct 轮数按 session 持久化累计，每完成一次循环加一，不按 HTTP 请求重新计数。
3. 每累计三轮，异步提取这三轮原始消息中的用户事实、偏好和长期记忆；没有值得保存的内容则不写库。
4. 长期记忆按 user_id 存储和召回，允许同一用户跨会话使用。
5. 每个新 query 召回当前用户的 top3 记忆 chunk，与 query 一起发送给模型。
6. Message 独立保存附加记忆 chunk，原始 Content 不拼接修改；上下文压缩可回收这些 chunk。
7. 第一版不增加记忆 tool。“记住 xxx”由每三轮摘要窗口处理，不承诺立即保存。

## 2. 现有实现与改造边界

LaxCode 的 SSE 当前通过 `cmd/agentasm.Assemble` 装配通用 Agent。`cmd/agentasm/qa.go` 已有空工具注册表装配，可参考其生命周期；`ReActService` 已有 PromptEnricher，但当前接口仅返回拼接字符串，需要为结构化记忆扩展。`think()` 的 turnCnt 是单次调用局部计数，不能直接作为长期记忆触发依据。

会话元数据实际保存在 session 数据库的 `request_contexts` 表，而非名为 session 的表。轮次计数应扩展这个现有会话头；本方案中的“session 表”均指它。现有上下文压缩的 memory generation 是工作集版本，与本方案的 user_memory 长期记忆不是同一概念。

Python 管线目前是 CLI，不是 HTTP 服务：

- `cmd/__init__.py`：接受 `--doc`、`--db`、`--chunk_config`，按文件路径查重并组装 LangGraph。
- `node/node.py`：ChunkNode 从文件增量读取；EmbeddingNode 同时向量化与写库。
- `store/store.py`：VecWriter 固定写 documents、chunks、chunk_vectors，首批先提交文档，再逐批 embedding 和提交。
- `splitter/by_title.py`：支持标题分节和无标题文本，可以复用。

保留现有知识库导入命令和 CLI/one-shot Agent 行为。新接口默认仍选择 knowledge 目标，用户记忆走独立写入策略。

### 2.1 QA 与 SSE 的装配边界（已确定）

共用底层无工具装配能力，保留 `AssembleQA` 和 `AssembleSSE` 两个入口。公共能力放在 `cmd/agentasm` 内，SSE 不依赖 `cmd/run_qa`，也不将两个入口合并为充斥模式判断的装配函数。

公共构造负责空工具注册表、会话仓储、LLM 客户端、tracer 与请求级资源清理；各入口显式配置系统提示词、查询增强和轮次完成行为。

| 配置 | AssembleQA | AssembleSSE |
| --- | --- | --- |
| 系统提示词 | 知识库问答 | 带用户记忆的对话 |
| 召回数据 | 文档知识库 | 当前 user_id 的长期记忆 |
| 查询增强 | 文档片段 | 可回收的 MemoryChunks |
| 轮次与记忆任务 | 不启用 | 持久化计数，每三轮创建任务 |

```text
cmd/agentasm：公共无工具服务装配
  ├── AssembleQA：配置知识库问答
  └── AssembleSSE：配置用户记忆逻辑

cmd/run_qa：调用 AssembleQA
cmd/run_sse：管理服务器级 worker，调用 AssembleSSE
```

异步 worker 及其依赖由 SSE server 持有并关闭，通过依赖注入接入 SSE 装配。每请求的 Cleanup 只回收请求级资源，不关闭共享 worker 或其仓储、客户端。公共构造不负责启动后台任务。

## 3. ReAct 轮次与异步任务

### 3.1 计数语义

在 request_contexts 增加 `react_turn_count INTEGER NOT NULL DEFAULT 0`。领域会话恢复时加载，压缩工作集时不得重置。

- 无工具调用：assistant 成功提交、本轮正常结束时加一。
- 若未来支持工具：该轮 assistant 和全部 tool result 提交完成后加一。
- LLM 错误、请求取消、未完成生成、持久化失败不加一；不能在 for 循环入口计数。
- 以持久化的轮次完成标识防止 Resume、重试重复计数；不能仅根据当前压缩后的消息数量推算。
- 新增或扩展仓储提交方法，在同一个 session DB 事务中提交本轮结束消息、工作集、轮次完成记录和计数。
- 对截断、过滤及 usage 不完整等 finish reason 明确定义是否完成，第一版仅正常完成的生成推进计数，不能只检查 ToolCalls 是否为空。

新增轮次记录 `react_turns`，至少保存 session_id、turn_no、本轮 assistant 的稳定消息标识、原始消息来源序号和 completed_at。唯一约束包括 `(session_id, turn_no)` 及本轮完成标识。来源映射覆盖本轮输入及输出；未来同一用户输入贯穿多个工具循环时，构建摘要窗口按消息 Seq 去重。

老会话默认从零累计，不从可能经过压缩的工作集推断历史轮数。已有未完成轮次在恢复后按新规则完成一次。

### 3.2 持久化任务

在 session DB 增加 `user_memory_jobs`，与第三轮完成事务一起写入，避免跨 kb.sqlite 和 session DB 做伪原子提交。

建议字段：

| 字段 | 含义 |
| --- | --- |
| id | 任务 ID |
| user_id / session_id | 归属，从会话元数据取得 |
| start_turn / end_turn | 固定窗口，例如 1–3、4–6 |
| source_key | 稳定来源键，例如 sessionID:react:3 |
| source_messages | 固定的原始消息快照，或不可变消息引用集合 |
| status | pending / running / retry / succeeded / skipped / failed |
| summary | 已生成摘要，管线重试时复用 |
| attempts / next_attempt_at | 重试次数和时间 |
| lease_until | worker 租约，进程崩溃后可重新领取 |
| last_error / created_at / updated_at | 排错与生命周期 |

以 `(session_id, end_turn)` 唯一。每次新计数满足 `% 3 == 0` 时创建任务。窗口固定在提交时，worker 不读取“执行时最新三轮”。

第一版采用进程内后台 worker + 数据库队列，不为每次请求创建无管理 goroutine。worker 独立于 HTTP request context，有限并发、单任务超时、退避重试、有界重试次数，失败保留记录。启动时恢复过期租约和待处理任务。

摘要只读取原始用户内容及必要的 assistant 对话内容，排除系统提示、ReasoningContent、附加 MemoryChunks 和压缩生成内容。提示词要求只记录有用户依据的可复用事实与偏好，不把 assistant 推测当作用户事实，无有效内容返回空列表。摘要结果先存任务表；空结果标记 skipped，不调用 embedding。

SSE 的 done 不等待摘要和入库；记忆最终一致，紧接第三轮的下一次 query 可能尚未召回新记忆。正常关闭停止领新任务，给在途工作有限退出时间，未完成工作可在重启后恢复。worker 资源由服务器生命周期持有，不随每请求 Cleanup 关闭。

## 4. Message 与召回上下文

建议增加结构（最终命名以实现为准）：

```go
type MemoryChunk struct {
    ID      string `json:"id"`
    Content string `json:"content"`
}

// Message 新增字段；Content 始终保留原始内容。
MemoryChunks []MemoryChunk `json:"memory_chunks,omitempty"`
```

Message.Clone 深复制新切片。原始消息历史不保存附加 chunk；当前 RequestContext 工作集保存它们，以支持断流恢复。历史 HTTP DTO 继续展示原始 Content。

新 query 的流程：

1. 从持久化会话解析 user_id。
2. 对原始 query 做 embedding。
3. 在该 user_id 的记忆中做向量 top3 搜索；不足三条返回实际数量。
4. 将结果写入本轮 user message 的 MemoryChunks，并提交工作集。
5. LLM 请求映射时将 query 与记忆序列化成有明确边界的参考内容；记忆不具备系统指令权限。

扩展结构化增强接口，避免 SSE 继续使用仅返回字符串的 Enrich；现有 QA 接口可兼容保留。provider 和本地 token 统计共用同一套内容构造逻辑，确保统计包含实际发送的记忆片段。

回收规则：新 query 提交时清理旧 user message 的 MemoryChunks，只保留本轮结果；上下文压缩优先清空附加 chunk 并重新统计 token，再决定是否压缩正文。对话压缩摘要和长期记忆摘要都不把附加 chunk 纳入材料，避免循环写回。回收只修改工作集，不删除 kb.sqlite 数据。Resume 使用已有工作集，不重复追加 query 或再次召回。

召回服务失败时，第一版记录错误并降级为原始 query 对话，不影响正常聊天；配置错误和数据库 schema 不兼容应在启动检查时暴露。

## 5. kb.sqlite 数据结构

三张表均写入 user_id。以下是逻辑结构，具体 DDL 与 vec0 元数据过滤语法在实现中验证。

| 表 | 字段与约束 |
| --- | --- |
| user_memory | id 主键、user_id 非空、session_id、source_key、start_turn、end_turn、content 完整摘要、created_at；UNIQUE(user_id, source_key)、UNIQUE(id, user_id) |
| user_memory_chunk | chunk_id 主键、user_id 非空、memory_id、chunk_seq、content、title；FOREIGN KEY(memory_id, user_id) REFERENCES user_memory(id, user_id)；UNIQUE(memory_id, chunk_seq) |
| user_memory_vectors | vec0 虚表：chunk_id TEXT PRIMARY KEY、user_id 可用于 KNN 内部过滤的元数据列、embedding FLOAT[1024] |

普通表启用外键约束。向量虚表没有普通外键保障，writer 在同一事务维护 chunk 与向量一一对应，删除和替换也必须覆盖三张表。为普通表用户与关联查询建立索引。

向量搜索必须在 KNN 选 top3 时限定 user_id，不允许全库 top3 后再过滤。实现时针对实际 Python/Go sqlite-vec 版本验证元数据过滤，确认所需语法在两端均可用；具体使用普通元数据列还是 partition key 由兼容性与实测决定。

Python 与 Go 必须使用相同 embedding 模型和向量空间，维度固定匹配现有 Go 的 1024。相同维度并不代表不同模型可混用。写入前验证向量数量、维度及数值有效性，已有表不匹配时明确报错。

user_memory 表的 DDL 由 Python 写入侧统一维护；部署先运行幂等初始化/迁移，Go 启动验证表结构。避免两边分别维护不一致的建表语句。已有 documents 等表不被记忆迁移修改。

## 6. Python 管线接口与实现

### 6.1 目标选择

新增 `--target knowledge|user_memory`，默认 knowledge，映射固定表组：

| target | 主表 / 分块表 / 向量表 |
| --- | --- |
| knowledge | documents / chunks / chunk_vectors |
| user_memory | user_memory / user_memory_chunk / user_memory_vectors |

不接受任意三个表名：表组还决定 schema、用户归属与幂等行为。保留现有 VecWriter 的知识库路径，新增 UserMemoryWriter，共用底层连接和向量序列化能力。

### 6.2 输入与输出

新增 `--stdin-json`，与 `--doc` 互斥。user_memory 目标第一版使用 stdin JSON；现有 knowledge 文件入口保持兼容。

```text
laxcode-knowledge --target user_memory --db /absolute/path/kb/kb.sqlite --stdin-json
```

```json
{
  "user_id": "用户 UUID",
  "session_id": "会话 ID",
  "source_key": "会话 ID:react:3",
  "start_turn": 1,
  "end_turn": 3,
  "content": "# 回答偏好\n用户偏好简洁中文回答。\n# 编程偏好\n示例优先使用 Go。"
}
```

user_id 来自 Go 会话元数据，不能由摘要 LLM 决定。校验 UUID、来源键、轮次范围、非空内容和输入大小。相同 source_key 携带不同内容或归属时报告冲突，不静默覆盖。

成功 stdout 只输出机器可读 JSON，例如：

```json
{"status":"succeeded","memory_id":"UUID","chunk_count":2,"already_processed":false}
```

重复已完成任务返回原结果并设置 already_processed=true。日志写 stderr；错误非零退出，并提供稳定错误分类，区分可重试网络/锁竞争和不可重试参数/维度错误。

增加仅初始化 schema 的 CLI 能力，供部署和 Go 启动前使用，不要求真实 embedding 请求。具体参数名在实现时统一。

### 6.3 流程拆分与事务

抽出可直接调用的 ingest 应用函数，不把业务逻辑只放在 argparse 中。文件输入继续使用增量读取，摘要输入直接提供内存文本，共用分块与 embedding 实现。不要让记忆输入依赖 document_path 或 document_id。

记忆管线执行顺序：

```text
校验 / 幂等查询
  → 文本分块
  → 全部 embedding
  → 校验向量
  → 事务内再次检查幂等键
  → 写 user_memory + chunks + vectors
  → 提交并返回
```

embedding 网络调用不持有数据库写事务。摘要较短，第一版允许整条记忆的 chunks 和向量在内存中准备后一次提交。任何写入错误全量回滚；并发相同任务由唯一键和事务保证单次入库。若 Python 提交成功但 Go 未收到结果，重试返回同一成功结果。

现有知识库 prepare_document 先提交、逐批写入的行为有部分入库风险；用户记忆不复用该提交语义。知识库完整原子替换可另行改进，不在本次扩大范围。

连接设置有限 busy timeout，并采用兼容的 WAL 读写配置，避免 Go 召回与 Python 写入的短时竞争直接变成失败。退出时在 finally 中关闭连接。

### 6.4 分块与模型配置

复用 ByTitleSplitter，新增记忆配置，初始 chunk_size=600、overlap_size=0；摘要按事实或偏好组织 Markdown 小节。小节数量和 chunk 数不要求等于三，top3 是召回上限。

保留 OPENAI_EMBEDDING_* 环境变量配置，由部署保证与 Go EmbedOpenai* 指向同一模型。需要以实际返回向量验证维度，而非仅依赖配置。

## 7. Go 与 Python 的调用边界

Go 后台 worker 负责生成摘要与任务状态；Python 只负责接收摘要、分块、embedding 和事务写库，不再次做偏好提取。

增加管线端口与 CLI 适配器，使用 exec.CommandContext 直接调用已配置的 Python 环境中的可执行入口，以 stdin 传 JSON，不通过 shell 拼接命令。可执行路径、kb 路径、任务超时及并发数由配置提供，使用绝对路径。

向量库提交与 session DB 的任务 succeeded 无法同事务，因此采用持久化任务 + 稳定来源键实现至少一次投递与幂等效果。同用户多个会话允许并发生成任务，数据库写入事务保持短小。

当前 POST /chat 支持隐式创建未绑定用户的会话，长期记忆无法为其推断 user_id。第一版对这类旧/匿名会话保留普通聊天，跳过记忆入库与召回并记录原因；显式通过已有创建会话接口绑定 user_id 的会话启用记忆。不得使用空 user_id 作为共享记忆空间。用户归属标识不代替身份认证。

## 8. 代码落点与实施顺序

| 项目 | 改造位置 |
| --- | --- |
| LaxCode | cmd/agentasm 抽取公共无工具装配，保留 AssembleQA 并新增 AssembleSSE；cmd/run_sse 接入服务器级 worker 生命周期 |
| LaxCode | domain/session 与 infrastructure/sessionrepo：计数、轮次来源、任务表与事务接口 |
| LaxCode | application/reactservice：完成轮次钩子、结构化召回、工作集清理 |
| LaxCode | domain/sharedkernel/message.go：MemoryChunks 与 Clone |
| LaxCode | provider、token 计数与 compactor：统一请求内容构造及 chunk 回收 |
| LaxCode | 新增长期记忆应用服务、用户限定检索仓储、Python 管线适配器 |
| laxcode_knowledge | cmd：target、stdin JSON、初始化入口、JSON 输出 |
| laxcode_knowledge | node / ingest：分离输入、embedding 和提交编排 |
| laxcode_knowledge | store：UserMemoryWriter、schema、幂等及原子事务 |
| laxcode_knowledge | 新增记忆分块配置与测试 |

实施顺序：

1. 先完成 Python schema、输入协议、幂等 writer 与假 embedding 测试。
2. Go 实现用户限定检索，并验证 Python 写入的真实 SQLite 文件可由 Go 读取。
3. 扩展 Message、请求映射、token 统计与压缩回收。
4. 实现会话轮次和任务事务、worker、摘要与子进程适配器。
5. SSE 专用装配接入，补充配置与运行说明，完成跨进程集成验证。

## 9. 验收与测试

- SSE 主请求不携带工具定义；CLI/one-shot 和现有 QA 不改变行为。
- 同 session 跨 HTTP 请求在第 3、6 轮各产生一条任务；重启继续计数，压缩不重置。
- 失败、取消、Resume 和重复提交不重复推进已完成轮次或创建任务。
- worker 延迟执行时仍提取原定三轮；摘要排除召回 chunk 和 reasoning；空摘要跳过管线。
- 两个用户写入相似记忆，分别召回时互不泄漏，过滤发生在 top3 选择内部。
- 同用户跨 session 能召回；匿名会话不进入共享记忆空间。
- Message 克隆无切片共享；原始历史保持 query；Resume 可恢复 chunk；压缩能释放 chunk 并减少计数。
- Python 写入失败无孤立主记录、chunk 或向量；重复来源键幂等，内容冲突报错。
- Python 成功提交后模拟 Go 崩溃，重试不新增重复记忆；失败租约可恢复。
- 向量数量、维度错误和模型配置不匹配有明确处理；不调用真实付费模型完成常规单测。
- Go/Python 使用相同测试库验证 sqlite-vec 兼容性、用户过滤和读写竞争。
- 现有 knowledge CLI 导入与相关 Go 回归测试通过。

本次不包含记忆语义去重、偏好冲突覆盖、过期淘汰、用户手动管理界面或立即记忆 tool；source_key 去重只解决任务重复投递，不能消除不同窗口提取出的相同事实。后续可基于已保存的来源与时间字段扩展这些能力。
