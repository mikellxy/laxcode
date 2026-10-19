# 配置文件使用说明

云端管理员配置位于 `deploy/defaults/settings.json`，可从 `deploy/defaults/settings.example.json` 复制。填写真实 API Key 后随发布包传到宿主机，用于注册 provider；不要提交到 Git，也不要放入镜像。

部署脚本生成环境变量引用版 `catalog/settings.json`，替换应用发布后重新注册 OpenShell provider。proxy 将平台配置同步到用户 `agenthome/.laxcode/settings.json`，在沙箱中以单文件只读挂载保护；cloud 从 `$HOME/.laxcode/settings.json` 加载，镜像不包含配置。用户模型选择单独持久化。其中 `openai_api_key` 为合法环境变量名（例如 `LAXCODE_API_KEY_...`），cloud 通过 `os.Getenv` 读取 OpenShell 占位凭据。模型名含点或短横线时，不能直接拼成 shell 变量名，因此部署脚本生成稳定标识。

配置键使用小写 `snake_case`。请填写实际支持 OpenAI 兼容接口的服务及模型。不要将填入真实密钥的文件提交到仓库。`settings.json` 是一个完整的 JSON 对象，下面的代码块按功能拆开说明。

---

## 主模型与模型目录

> [!TIP]
> `model` 选中主模型，用于处理 agent 循环，格式为 `provider_name:model_name`

```json
{
  "model": "example:chat",
  "provider_list": [
    {
      "provider_name": "example",
      "openai_api_key": "YOUR_API_KEY",
      "openai_base_url": "https://api.example.com/v1",
      "model_list": [
        { "model_name": "chat" },
        { "model_name": "summary" }
      ]
    }
  ]
}
```

`provider_name` 和同一 provider 内的 `model_name` 必须分别唯一。`model` 必须指向配置中的现有条目。
未填写 `model` 时，默认选中目录中的第一个模型。`model_name` 是配置中的名称；
可通过 `upstream_model` 指定实际请求的模型名。

## 云端启动参数

每个沙箱仅服务一个固定用户。cloud 支持 `-addr`（默认 `127.0.0.1:8080`）、`-plan`。用户身份由 `LAXCODE_USER_ID` 注入，HOME 为 `/workplace/agenthome`；不再支持 `-settings_dir`。

网页只能选择管理员预置的模型，不能新增 provider 或填写密钥。选择持久化到用户 `.laxcode/model-selection.json`，不修改平台目录；升级时所选模型被移除则恢复平台默认。旧的 `env.conf`、`configs.db`、`-conf_copy_dir` 和 `-cloud_bash_image` 已移除。

---

## 模型 Token 预算

> [!TIP]
> 主模型和压缩模型分别使用所选模型条目中的 `limit.context` 与 `limit.output`。

```json
{
  "provider_list": [
    {
      "provider_name": "example",
      "openai_api_key": "YOUR_API_KEY",
      "openai_base_url": "https://api.example.com/v1",
      "model_list": [
        {
          "model_name": "chat",
          "limit": { "context": 128000, "output": 8192 }
        },
        {
          "model_name": "summary",
          "limit": { "context": 128000, "output": 8192 }
        }
      ]
    }
  ]
}
```

`limit.context` 和 `limit.output` 都必须大于 0，且 `output` 必须小于 `context`。
省略整个 `limit` 时，默认使用 `context: 200000`、`output: 16384`；显式填写时
必须同时提供两个有效值。原有顶层 `openai_context_window`、`openai_max_output_tokens`、
`compaction_openai_context_window`、`compaction_openai_max_output_tokens` 已移除，
上下文窗口和输出预算统一使用所选模型的 `limit`。

---

## 压缩模型

> [!TIP]
> `compaction_model` 指定用于上下文压缩的模型；不填时继承主模型。

```json
{
  "compaction_model": "example:summary"
}
```

---
