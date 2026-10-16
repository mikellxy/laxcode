# 配置文件使用说明

配置模板：[settings.json](./settings.json)。将模板复制到 `~/.laxcode/settings.json`，再替换模型名、API Key 和服务地址：

```shell
mkdir -p ~/.laxcode
cp docs/settings.json ~/.laxcode/settings.json
```

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

本分支启动后始终按用户隔离运行，支持：

- `-addr`：后端监听地址，默认 `127.0.0.1:8090`。
- `-plan`：启用计划模式。
- `-conf_copy_dir`：新用户模板目录，直接包含 `settings.json` 与 `skills/`；默认使用服务账户的 `~/.laxcode`。
- `-cloud_bash_image`：预装的 Docker Bash 镜像，默认 `laxcode-cloud-bash:local`。

服务账户的 settings 是初始化模板，可保留明文 API Key；新用户的 Key 写入服务账户的
`~/.laxcode/configs.db`，用户 `agenthome/.laxcode/settings.json` 不含 Key。
已有用户的配置不会被模板覆盖，可在网页中新增和切换模型。

`~/.laxcode/env.conf` 在服务启动时注入进程环境，供上游 API 代理等基础设施使用；
用户配置加载不会修改服务进程环境。模型与凭据由 settings 管理。

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
