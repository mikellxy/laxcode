# laxcode-manager

Linux 宿主网络配额服务，与 proxy/cloud 共用根 `go.mod`。默认每个用户每个北京时间自然日 RX + TX 合计 5,000,000,000 字节；同用户的所有 supervisor 共用一个桶，重建沙箱不重置额度。

详细方案见 [沙箱公网流量配额设计](../docs/laxcode-manager-network-quota-design.md)。

已实现：

- 共同父 cgroup 上的 ingress/egress guard：精确控制对端豁免、一个 LPM 地址分类表、共享锁保护的双向配额。未知组和非公网地址默认拒绝。
- Docker 事件提示加 5 秒全量发现，从 `/workplace` 的宿主 bind source 取得完整用户 UUID，核验 sandbox ID/name/namespace、supervisor PID 和 cgroup；不信任客户端传入的归属，也不读取用户可修改的目录内容。
- SQLite WAL/FULL 事务先提交累计授权，再发布 Map；默认 16 MiB 批次、4 MiB 低水位、1 秒快照和补充兜底。
- pinned Maps/links 同 boot 接管；内核状态丢失后旧授权保守占用，不从 DB 的旧计数退款。
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

## Linux 首次安装

要求专用 Docker daemon、cgroup v2、systemd cgroup driver、BPF spin lock/Ring Buffer/真实 pinned bpf_link 支持。manager/gateway/Docker daemon 自身必须在 `laxcode.slice` 外。

1. 在维护窗口停止 proxy，并通过原 gateway 删除应用沙箱。保留用户目录、gateway/provider 状态。安装器要求 `docker ps -aq` 为空；不会替你删除容器，也不适用于共享 Docker 主机。
2. 从构建包提取 `bin/laxcode-manager` 和 `manager/` 到临时安装目录，或直接使用仓库里的安装器和交叉编译的二进制。
3. 执行一次性迁移；此命令会备份并修改 Docker 默认父组、安装 systemd 服务、重启 Docker：

```sh
sudo python3 deploy/manager/install.py --binary /tmp/laxcode-manager --migrate-docker
sudo curl --unix-socket /run/laxcode-manager/control.sock http://manager/readyz
```

安装器自动读取宿主 DNS、选出的私网存储地址，以及当前部署固定的 gateway `127.0.0.1:18080`，生成精确控制对端。服务以 root 运行，Socket 权限 0600；systemd 收到 guard 已安装的通知后才允许 Docker 启动，Docker 对账随后完成，避免启动依赖环。

4. 加载预先在构建机产出的 `laxcode-tools:go1.26.6-v2` 等镜像，再执行应用 `deploy.sh restart`。不要在已受 guard 保护的专用 Docker 上联网构建镜像，构建容器没有用户绑定，会被拒绝公网访问。部署会实际创建临时容器核验 Docker 默认父组，检查 manager readiness 和存储对端，之后才生成公网 policy。

普通应用发布不会重装 manager，也不操作其 DB/pins。manager 的兼容升级只替换 `/usr/local/bin/laxcode-manager` 后 `systemctl restart laxcode-manager`；不要运行首次迁移脚本或删除状态。**该操作应安排维护窗口：Docker 使用 `Requires=` 依赖 manager，显式重启 manager 会传播到 Docker，可能中断运行中的沙箱。** 这是 [systemd 的依赖语义](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html#Requires=)，不应把它当成无中断升级。状态不匹配时服务拒绝接管，先排查 `journalctl -u laxcode-manager`。

## 两个配置入口

### 公网域名：部署目录的 network.json

首版固定开放 `**.*.*`（三段及以上域名），并包含 `github.com`、`golang.org`、`go.dev`、`goproxy.cn`、`npmjs.org`、`pypi.org`、`pythonhosted.org`。端口限 80/443，使用 OpenShell REST 检查；不支持任意 TCP/UDP。provider 保留自己的域名和凭据策略。

构建时的默认文件是 `deploy/defaults/network.json`。部署机器可在 `deploy.sh` 旁边放置同名文件覆盖包内配置，更新代码时仍保留：

```json
{
  "additional_domains": ["example.com", "rust-lang.org"]
}
```

只填小写域名，不带 `https://`、路径、端口、IP 或通配符。添加两段域名通常才有必要；三段及以上已由固定规则覆盖。`additional_domains` 是补充列表，不替换上述默认项。

```sh
cd ~/laxcode_deploy
# 编辑此目录下的 network.json
./deploy.sh restart
```

此操作重新生成 policy 并重建应用沙箱，会终止正在执行的任务；manager 和当天余额保持不变。不要直接改发布目录里的 `policy.yaml`，下次发布会覆盖。重定向涉及的两段目标域名也必须在列表中。

域名获准不代表可以连内网：eBPF 继续阻断私网、回环、链路本地、元数据等非公网目标，包括域名解析后的地址。官方 OpenShell 0.1.4 的 hostless 任意域名路线未通过前置验证。

### 配额与控制对端：/etc/laxcode-manager/config.json

安装器生成配置；省略项使用 `config.go` 的默认值。可调整：

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

示例地址不能直接套用，应保留安装器生成的真实地址。满足 `0 < 低水位 < 批次 <= 日上限`；改额度/批次后在维护窗口重启 manager（会连带重启 Docker），已创建的用户日上限保留，日上限变更对新建用户日生效。时区固定北京时间，扫描周期固定 1/5 秒，首版不提供动态重载或单用户手工加额度 API。

`control_peers`、namespace、`workplaces_root`、地址分类表属于 pinned 配置身份，不能直接修改后普通重启：身份不匹配会关闭 guard 并拒绝启动。变更它们需要维护窗口，先停止并删除受管容器，停止 manager，保留 DB，确认父组内没有进程，再移除该服务专属 pins，按新配置重启；旧 active 授权转 unknown 保守占用。**不能在仍有沙箱运行时删除 pins，也不能为修复故障清空 DB。**

持久文件为 `/var/lib/laxcode-manager/quota.db`（含 WAL/SHM）、同目录 `manager.lock`；内核对象位于 `/sys/fs/bpf/laxcode-manager/`。应用回滚不回滚这些状态。

## 查询与故障

```sh
curl --unix-socket /run/laxcode-manager/control.sock http://manager/readyz
curl -H 'X-USERID: <用户UUID>' http://127.0.0.1:5173/api/network-usage
# 历史日：/api/network-usage?day=2026-10-20
```

用量查询不会启动沙箱。`observed_used_bytes` 是本次确认的内核累计值，`confirmed_used_bytes` 已持久化；`held_bytes` 是发放过的额度，不是实际用量；`uncertain_bytes` 是内核丢失后无法确认的保守占用。`stale=true` 表示无法保证实时性；manager 不可访问时 proxy 返回 503，不伪造零用量。

manager 暂停/被 kill 时，已授权额度仍受内核上限和日截止限制；重启接管不清零。检测到 DB/内核不变量或明显时钟跳变会关闭 guard，修复原因后重启。首次初始化中断、缺失部分 pinned 对象或身份不匹配需要人工检查，不能自动删状态重试。

proxy 在发送 SSE 前发现已耗尽会返回 429 和北京时间 `reset_at`；绑定期间发生耗尽会发送 `NETWORK_QUOTA_EXHAUSTED` SSE 错误。已经运行的任务可能表现为网络超时；文件和历史等宿主接口仍可用。

按整包放行可能留下零头；当全部额度已发放、余额不足 128 字节时，API 将其视为无法启动新 HTTP(S) 任务并返回耗尽。内核仍按精确余额计量，不把零头伪造成已用流量。

## 验证范围

实际结果记录在 [设计文档验收部分](../docs/laxcode-manager-network-quota-design.md#14-实施阶段与验收)。本次不自动迁移现有生产 Docker，也不宣称完成宿主重启、IPv6 公网或高负载性能验收。macOS 保持不开放通用公网 policy。

当前 `X-USERID` 仍为内测身份头；上线按用户限额前，入口必须认证并覆盖客户端自填 UUID，否则更换 UUID 可以领取另一用户额度。
