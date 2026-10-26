# laxcode-manager

Linux 宿主网络配额服务，与 proxy/cloud 共用根 `go.mod`。默认每个用户每个北京时间自然日 RX + TX 合计 5,000,000,000 字节；同用户的所有 supervisor 共用一个桶，重建沙箱不重置额度。

总体方案见 [沙箱公网流量配额设计](../docs/laxcode-manager-network-quota-design.md)，epoch 同步流程以 [epoch v2 重构方案](../docs/epoch_opt_v2.md) 为准。

已实现：

- 共同父 cgroup 上的 ingress/egress guard：精确控制对端豁免、一个 LPM 地址分类表、共享锁保护的双向配额。未知组和非公网地址默认拒绝。
- Docker 事件提示加 5 秒全量发现，从 `/workplace` 的宿主 bind source 取得完整用户 UUID，核验 sandbox ID/name/namespace、supervisor PID 和 cgroup；不信任客户端传入的归属，也不读取用户可修改的目录内容。
- 核验 `network=none` workload 的独立网络命名空间和 cgroup 后，复用归属表的保留值 0，仅允许它自己的 IPv4/IPv6 回环启动自检；supervisor 仍不能访问未豁免的宿主回环。
- SQLite WAL/FULL 事务先提交累计授权，再发布 Map；默认 16 MiB 批次、4 MiB 低水位、1 秒快照和补充兜底。
- `openKernel` 检查 ABI、DB 身份、父 cgroup 和配置后创建/接管 pinned Maps/links；`SyncEpochToEbpf(ctx, userID)` 统一处理启动恢复、API、Docker 发现、ring 提示与定时同步；三张桶 map 全无时关闭当天旧 epoch 并保留授权占用，部分条目存在时从 DB 重建原 epoch。
- Unix Socket readiness、绑定、用量 API；proxy 在执行前检查，即使已有缓存 endpoint 也重新核验绑定。

## 构建与检查

支持 Linux amd64/arm64，macOS 可跑账本和 proxy 测试，不运行此数据面。仓库提交了 clang 19 生成的 `quota_bpfel.o`，普通 Go 构建不需要 C 编译器。

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/laxcode-manager ./laxcode-manager
go test -race ./laxcode-manager ./laxcode-proxy
python3 -m unittest discover -s deploy -p '*test.py'

# 修改 C 后，在安装 clang-19 的 Linux 构建机重新生成：
go generate ./laxcode-manager
# macOS 用具备 BPF 后端的 clang-19 时，在该目录运行：
# go run github.com/cilium/ebpf/cmd/bpf2go -go-package main -target bpfel -tags linux \
#   -cc clang-19 -no-strip quota ebpf/quota.c -- -O2 -g -Wall -Werror -Iebpf
```

`ebpf/quota.h` 只定义用到的固定 Linux UAPI 和 helper，未读取内核私有结构，不依赖运行时下载 vmlinux 头文件。依赖固定为 cilium/ebpf v0.19.0；ABI 改动必须升级版本并安排维护迁移。

真实内核测试需要 root 和已挂载的 bpffs；只操作自己新建的 `laxcode-quota-test-*` 临时 cgroup/pins：

```sh
sudo go test -tags=integration ./laxcode-manager -v
# 也可交叉编译测试程序，上传目标 Linux 后运行
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -tags=integration -c \
  -o /tmp/laxcode-quota-tests ./laxcode-manager
```

## Linux 部署

要求专用 Docker daemon、cgroup v2、systemd cgroup driver、bpffs 和真实 pinned bpf_link 支持。基础环境已设置 `cgroup-parent=laxcode.slice` 并预装固定工具/运行时镜像。manager/gateway/Docker daemon 自身必须在该 slice 外。

按 [部署说明](../deploy/README.md) 将唯一制品覆盖解压到 `/root/laxcode_deploy/laxcode/`，执行 `python3 deploy.py`。脚本以 root 直接启动包内 `bin/laxcode-manager`，不安装 manager systemd 服务、不复制到 `/usr/local/bin`。先启动 slice、核验 Docker 默认父组，再启动 manager；guard 和 Docker 对账就绪后才生成公网 policy、启动应用。

首次部署从宿主实际 DNS、私网地址和 gateway 地址生成 `state/manager.json`，之后保留配置。manager PID 和日志同样在 `state/`。每次发布重启 manager，但不会删除或回滚 `/var/lib/laxcode-manager/` 和 BPF pins；状态不匹配时拒绝接管，检查 `state/manager.log`，不自动删状态重试。

宿主重启后需手动执行部署命令，manager 崩溃也不会自动重启。OpenShell 容器保持默认无自动重启策略，避免先于 guard 启动。不要在已受 guard 保护的专用 Docker 上联网构建工具镜像；应用镜像使用预装工具镜像离线 COPY。

## 两个配置入口

### 公网域名：部署目录的 network.json

首版固定开放 `**.*.*`（三段及以上域名），并包含 `github.com`、`golang.org`、`go.dev`、`goproxy.cn`、`npmjs.org`、`pypi.org`、`pythonhosted.org`。端口限 80/443，使用 OpenShell REST 检查；不支持任意 TCP/UDP。provider 保留自己的域名和凭据策略。

构建时的默认文件是 `deploy/defaults/network.json`。部署机器可在 `/root/laxcode_deploy/network.json` 放置同名文件覆盖包内配置，更新代码时仍保留：

```json
{
  "additional_domains": ["example.com", "rust-lang.org"]
}
```

只填小写域名，不带 `https://`、路径、端口、IP 或通配符。添加两段域名通常才有必要；三段及以上已由固定规则覆盖。`additional_domains` 是补充列表，不替换上述默认项。

```sh
cd ~/laxcode_deploy
# 编辑此目录下的 network.json
python3 laxcode/deploy.py
```

此操作重新生成 policy 并重建应用沙箱，会终止正在执行的任务；manager 会重启，当天配额状态保留。不要直接改发布目录里的 `policy.yaml`，下次发布会覆盖。重定向涉及的两段目标域名也必须在列表中。

域名获准不代表可以连内网：eBPF 继续阻断私网、回环、链路本地、元数据等非公网目标，包括域名解析后的地址。官方 OpenShell 0.1.4 的 hostless 任意域名路线未通过前置验证。

### 配额与控制对端：/root/laxcode_deploy/state/manager.json

首次部署生成配置；省略项使用 `config.go` 的默认值。可调整：

```json
{
  "sandbox_namespace": "laxcode",
  "workplaces_root": "/root/laxcode",
  "daily_limit_bytes": 5000000000,
  "grant_chunk_bytes": 16777216,
  "refill_low_watermark_bytes": 4194304,
  "control_peers": [
    {"address": "127.0.0.1", "port": 18080, "protocol": "tcp"},
    {"address": "192.168.1.5", "port": 5173, "protocol": "tcp"},
    {"address": "192.168.1.1", "port": 53, "protocol": "udp"},
    {"address": "192.168.1.1", "port": 53, "protocol": "tcp"}
  ]
}
```

示例地址不能直接套用，应保留首次部署生成的真实地址。满足 `0 < 低水位 < 批次 <= 日上限`；改额度/批次后在维护窗口执行 `python3 deploy.py` 重启应用（不重启 Docker），已创建的用户日上限保留，日上限变更对新建用户日生效。时区固定北京时间，扫描周期固定 1/5 秒，首版不提供动态重载或单用户手工加额度 API。

`control_peers`、namespace、`workplaces_root`、地址分类表属于 pinned 配置身份，不能直接修改后普通重启：身份不匹配会关闭 guard 并拒绝启动。ABI 变更也需要在维护窗口重建对象。

### 维护时清理 eBPF 挂载

发布包带有 `reset_quota.py`。当新规则或 ABI 与已有对象不兼容时，以 root 执行：

```sh
cd /root/laxcode_deploy/laxcode
python3 reset_quota.py
# 如需调整规则，此时编辑 ../state/manager.json
python3 deploy.py
```

脚本会停止 proxy、删除本应用沙箱、停止 manager 和 gateway；取得部署锁与 manager 单实例锁，确认父 cgroup 及其后代没有进程，验证账本身份和 schema 后移除专属 link/Map pins。它保留 DB、历史计数、已授权额度和父 cgroup，不自动启动服务。**执行会中断正在运行的任务。** 同次开机重建后，下次 `SyncEpochToEbpf` 根据三张桶 map 全无将当天旧 active epoch 转为 closed，未确认的授权仍占用当天额度。

脚本从 `../state/manager.json` 读取配置，省略项沿用 manager 的路径默认值。若要变更 `state_dir`、`pin_path` 或 `cgroup_parent`，必须先用旧路径配置执行清理；不要把旧账本留在原处后用空账本启动。父组仍有其他容器、manager 仍持锁、目录有未知条目或数据库不可用时会拒绝清理。脚本中途失败可排除原因后重跑；不要手工删除 DB/WAL/SHM 或只删除 pins。

本次内核 ABI 升为 3：grants 仅保留累计 bytes，usage 移除 notified_version，ring 事件仅携带 userID；metadata 保持 72 字节和完整身份校验。ABI 不兼容仍需按以上维护流程重建内核对象。

账本 schema 为 3，移除 epoch.version、metadata.boot_id/last_day 和 unknown 状态。本次按未上线、旧数据已清空的前提修改新建表结构，不提供旧 schema 自动迁移。部署前由操作者按重构方案准备新表；维护脚本保留现有 schema 3 账本，不删除 DB。

同次开机保留 Maps。每次同步先从 user_bucket 找 epochID；指针尚未发布时从 DB 的最新 active epoch 找回。三张桶 map 部分存在只视为初始化中断，不检查残留计数，删除残留后按 DB 重建；三项全无时才关闭当天旧 active 并创建新 epoch。部分 pinned 对象缺失仍拒绝启动，需要维护清理，不能与桶条目不完整混淆。

跨天按快照、关闭旧 epoch、发布新桶的顺序执行，接受快照至切换间少量漏计。历史 epoch 和旧桶条目由持有同一用户锁的异步任务清理，始终排除清理时当前指针指向的 epoch，失败后重试。正常追加授权只更新 grants，usage 由内核累计，仅重建时从 DB 初始化。

持久文件为 `/var/lib/laxcode-manager/quota.db`（含 WAL/SHM）、同目录 `manager.lock`；内核对象位于 `/sys/fs/bpf/laxcode-manager/`。应用回滚不回滚这些状态。

## 查询与故障

```sh
curl --unix-socket /run/laxcode-manager/control.sock http://manager/readyz
curl -H 'X-USERID: <用户UUID>' http://127.0.0.1:5173/api/network-usage
# 历史日：/api/network-usage?day=2026-10-20
```

用量查询不会启动沙箱，但会同步目标用户当前日的 epoch，并按低水位补充授权；未知 UUID 会建立账本用户。`observed_used_bytes` 是本次确认的内核累计值，`confirmed_used_bytes` 已持久化；`held_bytes` 是发放过的额度，不是实际用量；`uncertain_bytes` 是 closed epoch 中 granted − RX − TX 的总和，属于不再发放的保守占用。`stale=true` 表示无法保证实时性；manager 不可访问时 proxy 返回 503，不伪造零用量。

manager 暂停/被 kill 时，已授权额度仍受内核上限和日截止限制；重启接管不清零。检测到 DB/内核不变量异常会关闭 guard，修复原因后重启。缺失部分 pinned 对象或身份不匹配需要人工检查，不能自动删状态重试。

proxy 在发送 SSE 前发现已耗尽会返回 429 和北京时间 `reset_at`；绑定期间发生耗尽会发送 `NETWORK_QUOTA_EXHAUSTED` SSE 错误。已经运行的任务可能表现为网络超时；文件和历史等宿主接口仍可用。

按整包放行可能留下零头；当全部额度已发放、余额不足 128 字节时，API 将其视为无法启动新 HTTP(S) 任务并返回耗尽。内核仍按精确余额计量，不把零头伪造成已用流量。

本轮不检测时钟跳变或记账日期回退；北京时间日期在每次用户同步取得锁后读取一次。

## 验证范围

实际结果记录在 [设计文档验收部分](../docs/laxcode-manager-network-quota-design.md#14-实施阶段与验收)。本次不自动迁移现有生产 Docker，也不宣称完成宿主重启、IPv6 公网或高负载性能验收。macOS 保持不开放通用公网 policy。

当前 `X-USERID` 仍为内测身份头；上线按用户限额前，入口必须认证并覆盖客户端自填 UUID，否则更换 UUID 可以领取另一用户额度。
