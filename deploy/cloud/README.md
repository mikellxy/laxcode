# NAS 部署包（Linux x86_64 / AMD64）

在开发机安装 Go、Node/pnpm 和 Docker 后，执行 `make build-cloud` 生成 nas/。
构建会保留已有的 defaults/；首次构建生成空模型配置和 Skills 目录，
不会从开发机主目录自动复制配置、密钥或 Skills。

复制整个 nas 目录到 NAS，例如 /volume1/apps/laxcode。前端已嵌入
laxcode-web，两个程序都是 CGO_ENABLED=0 构建，无需 Go、Node、pnpm、make
或 curl。只需要系统自带的 /bin/sh 及常用 Unix 命令，以及可用的 Docker CLI/daemon。

首次导入离线 Bash 镜像（不需要 NAS 联网下载）：

```sh
cd /volume1/apps/laxcode
docker load -i cloud-bash-amd64.tar
chmod +x laxcode laxcode-web start.sh stop.sh
./start.sh
```

浏览器访问 http://<NAS-IP>:5173。必须以能访问 Docker 的普通用户运行
start.sh，不能使用 sudo ./start.sh；导入镜像如需管理员权限，可单独使用
sudo docker load -i cloud-bash-amd64.tar。Docker 可执行文件必须在 PATH 中。

`start.sh` 会自动在后台运行，等待 Web 服务就绪后返回。关闭终端或 SSH 连接不会
停止服务；重复执行时会提示已运行。控制台输出追加到部署目录的 `server.log`，
`server.pid` 和 `server.state` 记录后台管理进程及对应后端/Web 进程。

```sh
tail -f server.log
```

停止（默认查找 5173，使用自定义端口启动时会读取保存的端口）：

```sh
./stop.sh
```

脚本先发送 TERM，等待最多 25 秒后对仍存活的对应进程发送 KILL，同时停止 Web 和后端。
通过 `/proc` 验证进程属于当前部署目录，不会杀掉同端口上的其他程序。
兼容没有 PID 文件的旧前台启动方式，此时通过 NAS 自带的 `ss` 按端口查找进程。
更换配置、升级二进制或修改启动参数时，先运行 `./stop.sh`，再运行 `./start.sh`。

修改端口：LAXCODE_CLOUD_PORT=8080 ./start.sh。
指定其他镜像：LAXCODE_CLOUD_BASH_IMAGE=your-image:tag ./start.sh。
所有额外命令行参数都会传给后端，例如：

```sh
./start.sh -conf_copy_dir=/volume1/apps/laxcode-defaults
```

默认新用户模板是本包的 defaults/settings.json 和 defaults/skills/。
可自行配置模型并将预置 Skill 包放进 defaults/skills/；重建时原样保留。
镜像预装 jq、Excalidraw 离线绘图依赖和中文字体。
已有用户不会自动获得新增 Skill；如需使用，请另行同步到对应用户的
$HOME/laxcode/{user_id}/agenthome/.laxcode/skills/。
用户可在网页模型配置中填写 API Key 和 Base URL；也可以提前编辑 defaults
里的模板，已有用户不会被模板更新覆盖。没有复制开发机的密钥、会话或私人 Skills。

用户持久数据保存在运行账户的 $HOME/laxcode/，实例文件在 $HOME/.laxcode/，
日志在本包的 log/ 和 server.log。账户 HOME 必须存在且可写。
所有用户的 API Key 按 user_id:provider:model 存在运行账户的
$HOME/.laxcode/configs.db，用户 agenthome 的 settings 不含 Key；模板可保留明文 Key。
备份和迁移时请同时保留 configs.db 与用户数据，不要把该数据库放入 defaults 或项目目录。
更新包时保留自行编辑的 defaults/。删除导入后的镜像 tar 不影响 Docker 中的镜像。

Cloud 服务全局最多同时处理 2 个 Chat/Resume 请求（按 NAS 的 N100 四核、8 GB 内存设定）。
额度覆盖完整 SSE 流及文件统计收尾；超限立即返回 HTTP 429 / SERVER_BUSY 和
Retry-After: 5，不排队，用户可稍后手动重试。启动日志会显示该上限。

Cloud 的 Chat/Resume 在执行前后仅扫描当前项目 WorkDir，以文件大小和修改时间
识别新增/修改产物，包括 Bash 生成的 PNG/SVG。跳过符号链接、Git 元数据及常见
依赖/缓存目录。同一项目的不同 session 不能同时执行；失败或断连也会尝试登记产物。
扫描每次最多 100000 个目录项、5 秒，统计失败时接口会明确报错。

Bash 容器禁用外网；后端仍需访问配置的 LLM API。每次 Bash 的资源限制为
2 CPU、1 GiB 内存和 256 个进程，NAS 的 Docker/内核须支持这些限制以及
当前挂载选项。包内镜像标签为 laxcode-cloud-bash:nas-amd64，
并未覆盖开发机的 laxcode-cloud-bash:local。
项目和用户 tmp 可写，agenthome/.laxcode/skills 单独只读挂载，可执行包内脚本；
其他配置和会话数据不挂载。脚本输出及缓存须写入项目或 $TMPDIR，不能写回 Skills。

本包包含最新的 Skill 选择菜单、任务文件预览和下载功能。
升级时先停止旧服务，替换 laxcode 和 laxcode-web，并重新导入新版 Bash 镜像：

```sh
docker load -i cloud-bash-amd64.tar
chmod +x laxcode laxcode-web start.sh stop.sh
./start.sh
```

保留已修改的 defaults/；如需新增的公开 Skill，可单独合并对应目录。
无需在 NAS 安装 Go、Node 或前端构建工具。
SHA256SUMS 可用于核对复制结果：

```sh
sha256sum -c SHA256SUMS
```
