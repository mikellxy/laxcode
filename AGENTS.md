# eBPF 迭代注意事项

依据 `laxcode-manager/bpf_linux.go` 的 `openKernel()` pinned metadata 校验：

- 接管已有 pins 必须匹配 ABI、DB UUID、父 cgroup ID、配置哈希，且 `Ready` 只能为 0/1。保留不匹配时禁用 guard 并拒绝启动的行为，不得跳过校验。
- 修改 Map 布局或不兼容的程序语义时，评估并更新 `abiVersion`；普通重启不等于迁移已有内核对象。
- `ControlPeers`、`addressRules`、`Namespace`、`WorkplacesRoot` 参与配置哈希；修改内容或哈希序列化方式都会影响接管兼容性。
- 不兼容升级走 `deploy/reset_quota.py` 维护重建流程（会中断任务），保留账本；不要通过手动删除 DB 或 pins 绕过校验。操作细节见 `laxcode-manager/README.md`。
