# LaxCode 云Agent

![LaxCode 模块架构：proxy、manager、OpenShell 沙箱与 laxcode-cloud](docs/images/laxcode-cloud-architecture.png)

[编辑架构图（Excalidraw）](docs/images/laxcode-cloud-architecture.excalidraw) · [网络配额与部署](laxcode-manager/README.md)

用于单实例 OpenShell 云端部署，本地模式由其他独立分支维护。

宿主机运行 Docker、OpenShell 0.1.4 和 laxcode-proxy；Linux 公网配额由独立的 laxcode-manager 与内核 eBPF 执行。proxy 提供前端，通过 `X-USERID` 将请求转发到每用户独立沙箱；laxcode-cloud 和 Bash 工具在 workload 中执行，supervisor 负责网络策略与凭据注入。

```sh
# 本机/CI：配置 deploy/defaults/settings.json，固定交叉编译 Linux amd64
cd deploy && sh build.sh
# 产物：仓库根目录 release/laxcode.tar.gz，上传 TOS 或复制到 ECS

# ECS：下载到 /root/laxcode_deploy/laxcode.tar.gz 后
cd /root/laxcode_deploy
tar --unlink-first -xzf laxcode.tar.gz && cd laxcode && python3 deploy.py
```

详见 [部署与更新](deploy/README.md)、[模型配置](docs/settings.md)。先预装基础环境和固定工具镜像。只分发一个包，直接覆盖固定目录；manager/proxy 由包内脚本启动，不安装 systemd 服务，不备份旧版本。`state/`、用户目录和配额数据库跨发布保留。公网通过 Caddy 的 HTTPS 入口访问。

宿主 `~/laxcode/{user_id}` 持久化用户 HOME、项目、会话、Skills 与模型选择。空闲沙箱停止后复用；更新应用时重新创建沙箱，保留用户文件。模型密钥由 OpenShell 管理，用户仅从预置模型中选择。

内测版尚无注册登录、计费和套餐权限；访客 UUID 是路由标识。proxy 监听 5173；专用网关仅监听本机回环地址。

## 开发与验证

```sh
make test
make vet
# 前端热更新，API 转发到已启动的本机 proxy
pnpm --dir web dev  # http://localhost:5174
```

Go 1.26.6、Node/pnpm、Python 3 和 Docker 是构建依赖。首次浏览器访问包含操作引导；聊天支持 SSE、项目文件清单和下载、会话性能汇总及后台评估。API 细节参见 docs，所有业务请求均需 `X-USERID`。
